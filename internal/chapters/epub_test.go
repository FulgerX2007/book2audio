package chapters

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FulgerX2007/book2audio/internal/epub/epubtest"
	"github.com/FulgerX2007/book2audio/internal/execx"
	"github.com/FulgerX2007/book2audio/internal/extract"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

// epubBook creates a working folder for an EPUB and extracts it. The fake
// runner has no tools: an EPUB must not need any.
func epubBook(t *testing.T, spec epubtest.Spec) *workspace.Workspace {
	t.Helper()
	tmp := t.TempDir()
	book := filepath.Join(tmp, "tides.epub")
	epubtest.Write(t, book, spec)
	ws, err := workspace.Init(book, workspace.InitOptions{Workdir: filepath.Join(tmp, "work")})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	res, err := extract.Run(context.Background(), ws, execx.NewFakeRunner(), quiet)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if res.Pages != len(spec.Docs) || res.Extracted != len(spec.Docs) || res.OCR != 0 {
		t.Fatalf("extract result = %+v", res)
	}
	return ws
}

func TestRunEPUB(t *testing.T) {
	ws := epubBook(t, epubtest.Sample())
	if src := ws.Manifest.GetSource(); src.Title != "The Tide Tables" || src.Author != "A. Keeper" || src.Pages != 4 {
		t.Errorf("source = %+v", src)
	}

	res := run(t, ws)
	if res.Chapters != 3 || res.Written != 3 || res.TOCPages != 0 {
		t.Fatalf("Result = %+v, want the introduction and two chapters", res)
	}
	want := map[int]string{
		// The title page is not in the table of contents: it is chapter 0.
		0: "# Вступление\n\nThe Tide Tables\n\nA small book for tests.\n",
		// Paragraphs are taken as they are: long lines are not joined, the
		// heading is not repeated, the footnote mark is gone.
		1: "# Chapter 1: High Water\n\nThe first paragraph of chapter one has a word in italics.\n\n" +
			"The second paragraph is broken in two lines & uses an entity too.\n",
		// A document outside the table of contents continues its chapter.
		2: "# Chapter 2: Low Water\n\nChapter two opens here.\n\nA quoted line.\n\nFirst item.\n\nSecond item.\n\n" +
			"Chapter two goes on in another file.\n",
	}
	for n, text := range want {
		if got := readFile(t, ws.ChapterPath(n)); got != text {
			t.Errorf("chapter %d =\n%q\nwant\n%q", n, got, text)
		}
	}

	// Nothing changed: nothing is written again.
	if _, err := extract.Run(context.Background(), ws, execx.NewFakeRunner(), quiet); err != nil {
		t.Fatal(err)
	}
	if res := run(t, ws); res.Written != 0 {
		t.Errorf("second run wrote %d chapters, want 0", res.Written)
	}
}

// A book whose documents have neither a table of contents entry nor a
// heading is split into one section per document.
func TestRunEPUBWithoutStructure(t *testing.T) {
	ws := epubBook(t, epubtest.Spec{Title: "Plain", TOC: "none", Docs: []epubtest.Doc{
		{Name: "a.xhtml", Body: "<p>First document.</p>"},
		{Name: "empty.xhtml", Body: "<p> </p>"},
		{Name: "b.xhtml", Body: "<p>Second document.</p>"},
	}})
	if res := run(t, ws); res.Chapters != 2 {
		t.Fatalf("Result = %+v, want two sections", res)
	}
	if got := readFile(t, ws.ChapterPath(2)); got != "# Часть 2\n\nSecond document.\n" {
		t.Errorf("section 2 = %q", got)
	}
}

// chapters.json of an EPUB can be edited like any other: here two chapters
// are merged, and the title of the second one stays in the text.
func TestRunEPUBHandEditedStarts(t *testing.T) {
	ws := epubBook(t, epubtest.Sample())
	run(t, ws)
	path := ws.ChaptersJSONPath()
	edited := `[{"n": 1, "title": "Everything", "start_page": 2, "start_line": 1}]`
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	res := run(t, ws)
	if !res.Manual || res.Chapters != 1 {
		t.Fatalf("Result = %+v, want one chapter from the edited file", res)
	}
	got := readFile(t, ws.ChapterPath(1))
	if !strings.HasPrefix(got, "# Everything\n\nThe first paragraph") || !strings.Contains(got, "\n\nChapter 2: Low Water\n\nChapter two opens here.") {
		t.Errorf("merged chapter = %q", got)
	}
	if strings.Contains(got, extract.ChapterMark) && strings.Count(got, "# ") != 1 {
		t.Errorf("a chapter mark was left in the text: %q", got)
	}
}

func TestEPUBPage(t *testing.T) {
	ws := epubBook(t, epubtest.Spec{Title: "Marks", Docs: []epubtest.Doc{
		{Name: "a.xhtml", TOC: "One", Body: "<h1>One</h1><p># not a title</p><p>Text.</p>"},
	}})
	if got := readFile(t, ws.PagePath(1)); got != "# One\n\nnot a title\n\nText.\n\n" {
		t.Errorf("page = %q", got)
	}
}
