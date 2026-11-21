package chapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/FulgerX2007/book2audio/internal/clean"
	"github.com/FulgerX2007/book2audio/internal/config"
	"github.com/FulgerX2007/book2audio/internal/execx"
	"github.com/FulgerX2007/book2audio/internal/extract"
	"github.com/FulgerX2007/book2audio/internal/testdata"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// full is a line as wide as the body lines of the test pages.
const full = "This line is as wide as the lines of this page usually are, yes"

func defaultOptions() Options {
	c := config.Default().Chapters
	return Options{
		Heading:       regexp.MustCompile(c.HeadingRegex),
		MaxChars:      c.HeadingMaxChars,
		TOCMax:        c.TOCMaxHeadings,
		FallbackPages: c.FallbackPages,
	}
}

// para returns a three-line paragraph that starts with the given words.
func para(start string) string {
	return start + " " + full[len(start)+1:] + "\n" + full + "\nand it ends here."
}

// newBook creates a working folder whose extract stage has produced the
// given pages (through fake poppler tools).
func newBook(t *testing.T, pages ...string) *workspace.Workspace {
	t.Helper()
	tmp := t.TempDir()
	pdf := filepath.Join(tmp, "book.pdf")
	cfg := filepath.Join(tmp, "config.yaml")
	for path, content := range map[string]string{pdf: "%PDF-fake", cfg: "extract:\n  ocr_min_chars: 0\n"} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := workspace.Init(pdf, workspace.InitOptions{ConfigPath: cfg, Workdir: filepath.Join(tmp, "work")})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	setPages(t, ws, pages...)
	return ws
}

// setPages (re)runs the extract stage with fake tools that return pages.
func setPages(t *testing.T, ws *workspace.Workspace, pages ...string) {
	t.Helper()
	fake := execx.NewFakeRunner()
	fake.Stub("pdfinfo", fmt.Appendf(nil, "Pages: %d\n", len(pages)), nil)
	fake.Stub("pdftotext", []byte(strings.Join(pages, "\f")+"\f"), nil)
	// Remove the old page files so that every page is extracted again.
	for n := range len(pages) {
		os.Remove(ws.PagePath(n + 1))
	}
	if _, err := extract.Run(context.Background(), ws, fake, quiet); err != nil {
		t.Fatalf("extract: %v", err)
	}
}

func run(t *testing.T, ws *workspace.Workspace) Result {
	t.Helper()
	res, err := Run(context.Background(), ws, quiet)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func titles(starts []Start) []string {
	out := make([]string, len(starts))
	for i, s := range starts {
		out[i] = fmt.Sprintf("%d:%s@%d/%d", s.N, s.Title, s.StartPage, s.StartLine)
	}
	return out
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name    string
		pages   []string
		want    []string // n:title@page/line
		wantTOC []int
	}{
		{
			name: "headings at the top of a page and after a blank line",
			pages: []string{
				"Chapter 1: The Start\n" + para("One"),
				para("Two") + "\n\nChapter 2: The Middle\n" + para("Three"),
			},
			want: []string{"1:Chapter 1: The Start@1/1", "2:Chapter 2: The Middle@2/5"},
		},
		{
			name: "text before the first heading is chapter 0",
			pages: []string{
				para("Intro") + "\n\nChapter 1\n\n" + para("One"),
			},
			want: []string{"0:" + IntroTitle + "@1/1", "1:Chapter 1@1/5"},
		},
		{
			name: "table of contents page gives no chapters",
			pages: []string{
				"Contents\nChapter 1: A\nChapter 2: B\nChapter 3: C\nChapter 4: D",
				"Chapter 1: A\n" + para("One"),
			},
			want:    []string{"1:Chapter 1: A@2/1"},
			wantTOC: []int{1},
		},
		{
			name: "a sentence that starts like a heading is not one",
			pages: []string{
				"Chapter 1\n" + para("One") + "\n\n" +
					"Chapter 3 of this guide shows how the keeper winds the fog bell\n" + full + "\nand so on.\n\n" +
					"Chapter 3 covers fog.\n\n" + para("Two"),
			},
			want: []string{"1:Chapter 1@1/1"},
		},
		{
			name: "a matching line inside a block of text is not a heading",
			pages: []string{
				"Chapter 1\n" + para("One") + "\nChapter 9\n" + para("Two"),
			},
			want: []string{"1:Chapter 1@1/1"},
		},
		{
			name: "russian headings",
			pages: []string{
				"Глава 1\n" + para("Один") + "\n\nЧасть вторая\n" + para("Два"),
			},
			want: []string{"1:Глава 1@1/1", "2:Часть вторая@1/6"},
		},
		{
			name: "heading longer than the limit is text",
			pages: []string{
				"Chapter 1\n" + para("One") + "\n\n" +
					"Chapter 2 in which a very long line pretends to be a heading although it is far too long for one\n\n" + para("Two"),
			},
			want: []string{"1:Chapter 1@1/1"},
		},
		{
			name: "no headings: sections of fallback_pages pages",
			pages: func() []string {
				pages := make([]string, 45)
				for i := range pages {
					// Distinct first lines without digits: not a running header.
					pages[i] = para(fmt.Sprintf("%c%c", 'A'+i%26, 'a'+i/26))
				}
				pages[9] = "" // an empty page does not count
				return pages
			}(),
			want: []string{"1:Часть 1@1/1", "2:Часть 2@22/1", "3:Часть 3@42/1"},
		},
		{
			name:  "no text at all",
			pages: []string{"", "\n\n"},
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			starts, toc := Detect(clean.Pages(tt.pages), defaultOptions())
			if got := titles(starts); !slices.Equal(got, tt.want) {
				t.Errorf("starts = %q, want %q", got, tt.want)
			}
			var gotTOC []int
			for page := range toc {
				gotTOC = append(gotTOC, page)
			}
			slices.Sort(gotTOC)
			if !slices.Equal(gotTOC, tt.wantTOC) {
				t.Errorf("TOC pages = %v, want %v", gotTOC, tt.wantTOC)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	ok := []Start{{N: 0, Title: "Intro", StartPage: 1, StartLine: 1}, {N: 1, Title: "One", StartPage: 1, StartLine: 9}, {N: 2, Title: "Two", StartPage: 3, StartLine: 1}}
	if err := Validate(ok, 5); err != nil {
		t.Fatalf("Validate of a correct list: %v", err)
	}

	tests := []struct {
		name   string
		mutate func([]Start) []Start
		want   string
	}{
		{"empty list", func([]Start) []Start { return nil }, "пуст"},
		{"negative number", func(s []Start) []Start { s[0].N = -1; return s }, "отрицательным"},
		{"duplicate number", func(s []Start) []Start { s[2].N = 1; return s }, "дважды"},
		{"empty title", func(s []Start) []Start { s[1].Title = " "; return s }, "пустой заголовок"},
		{"page outside the document", func(s []Start) []Start { s[2].StartPage = 6; return s }, "вне документа"},
		{"page zero", func(s []Start) []Start { s[0].StartPage = 0; return s }, "вне документа"},
		{"line zero", func(s []Start) []Start { s[1].StartLine = 0; return s }, "номер строки"},
		{"same position twice", func(s []Start) []Start { s[1].StartLine = 1; return s }, "по порядку"},
		{"out of order", func(s []Start) []Start { s[1], s[2] = s[2], s[1]; return s }, "по порядку"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.mutate(slices.Clone(ok)), 5)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestRunSplitsChaptersInTheMiddleOfAPage(t *testing.T) {
	ws := newBook(t,
		"Chapter 1: The Start\n"+para("First"),
		para("Second")+"\n\nChapter 2: The Middle\n"+para("Third"),
		para("Fourth"),
	)
	res := run(t, ws)
	if res != (Result{Chapters: 2, Written: 2}) {
		t.Errorf("Result = %+v, want 2 chapters written", res)
	}

	one, two := readFile(t, ws.ChapterPath(1)), readFile(t, ws.ChapterPath(2))
	if !strings.HasPrefix(one, "# Chapter 1: The Start\n\nFirst ") || !strings.HasPrefix(two, "# Chapter 2: The Middle\n\nThird ") {
		t.Errorf("chapter files do not start with the title and the first paragraph:\n%s\n---\n%s", one, two)
	}
	// Page 2 is shared: its first paragraph belongs to chapter 1, the rest to chapter 2.
	for word, want := range map[string][2]int{"First": {1, 0}, "Second": {1, 0}, "Third": {0, 1}, "Fourth": {0, 1}} {
		got := [2]int{strings.Count(one, word+" "), strings.Count(two, word+" ")}
		if got != want {
			t.Errorf("paragraph %q occurs %v times in chapters 1 and 2, want %v", word, got, want)
		}
	}
	if strings.Count(one, "Chapter") != 1 || strings.Count(two, "Chapter") != 1 {
		t.Error("the heading line was left in the chapter body")
	}

	var starts []Start
	if err := json.Unmarshal([]byte(readFile(t, ws.ChaptersJSONPath())), &starts); err != nil {
		t.Fatalf("chapters.json: %v", err)
	}
	if got := titles(starts); !slices.Equal(got, []string{"1:Chapter 1: The Start@1/1", "2:Chapter 2: The Middle@2/5"}) {
		t.Errorf("chapters.json = %q", got)
	}
}

func TestRunIntroAndTableOfContents(t *testing.T) {
	ws := newBook(t,
		"Contents\nChapter 1: A\nChapter 2: B\nChapter 3: C\nChapter 4: D",
		para("Foreword")+"\n\nChapter 1: A\n"+para("Body"),
	)
	res := run(t, ws)
	if res.Chapters != 2 || res.TOCPages != 1 {
		t.Errorf("Result = %+v, want the intro and one chapter, one TOC page", res)
	}
	intro := readFile(t, ws.ChapterPath(0))
	if !strings.HasPrefix(intro, "# "+IntroTitle+"\n\nForeword ") {
		t.Errorf("chapter 0 = %q", intro)
	}
	if strings.Contains(intro, "Contents") || strings.Contains(intro, "Chapter") {
		t.Errorf("the table of contents leaked into chapter 0:\n%s", intro)
	}
}

func TestRunIsIdempotent(t *testing.T) {
	ws := newBook(t, "Chapter 1\n"+para("One")+"\n\nChapter 2\n"+para("Two"))
	run(t, ws)

	res := run(t, ws)
	if res.Written != 0 || res.Chapters != 2 {
		t.Errorf("second run: Result = %+v, want nothing written", res)
	}
}

func TestRunKeepsHandEditedChapter(t *testing.T) {
	ws := newBook(t, "Chapter 1\n"+para("One")+"\n\nChapter 2\n"+para("Two"))
	run(t, ws)
	edited := "# Chapter 1\n\nText fixed by hand.\n"
	if err := os.WriteFile(ws.ChapterPath(1), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	// Same input: the edited file is simply up to date.
	if res := run(t, ws); res.Written != 0 || res.Kept != 0 {
		t.Errorf("Result = %+v, want nothing to do", res)
	}

	// The source of chapter 1 changes: the edited file still wins.
	setPages(t, ws, "Chapter 1\n"+para("Changed")+"\n\nChapter 2\n"+para("Two"))
	res := run(t, ws)
	if res.Kept != 1 || res.Written != 0 {
		t.Errorf("Result = %+v, want the edited chapter kept and nothing written", res)
	}
	if got := readFile(t, ws.ChapterPath(1)); got != edited {
		t.Errorf("the hand-edited chapter was overwritten:\n%s", got)
	}

	// --force rebuilds it from the source.
	ws.Manifest.Force = true
	res = run(t, ws)
	if got := readFile(t, ws.ChapterPath(1)); !strings.Contains(got, "Changed ") {
		t.Errorf("with Force the chapter was not rebuilt (Result %+v):\n%s", res, got)
	}
}

func TestRunUsesHandEditedChaptersJSON(t *testing.T) {
	ws := newBook(t, "Chapter 1\n"+para("One")+"\n\nChapter 2\n"+para("Two"))
	run(t, ws)

	// The user merges both chapters into one and renames it.
	manual := `[{"n": 1, "title": "The Whole Story", "start_page": 1, "start_line": 1}]`
	if err := os.WriteFile(ws.ChaptersJSONPath(), []byte(manual), 0o644); err != nil {
		t.Fatal(err)
	}
	res := run(t, ws)
	if !res.Manual || res.Chapters != 1 || res.Written != 1 {
		t.Errorf("Result = %+v, want one manual chapter written", res)
	}
	got := readFile(t, ws.ChapterPath(1))
	if !strings.HasPrefix(got, "# The Whole Story\n") || !strings.Contains(got, "One ") || !strings.Contains(got, "Two ") {
		t.Errorf("the merged chapter:\n%s", got)
	}
	if readFile(t, ws.ChaptersJSONPath()) != manual {
		t.Error("the hand-edited chapters.json was overwritten")
	}
	if ids := ws.Manifest.UnitIDs(Stage); !slices.Equal(ids, []string{"ch01", "index"}) {
		t.Errorf("units = %v, want the stale chapter 2 pruned", ids)
	}

	// It stays the source of the starts on later runs…
	if res := run(t, ws); !res.Manual || res.Written != 0 {
		t.Errorf("later run: Result = %+v", res)
	}
	// …until --force restores detection.
	ws.Manifest.Force = true
	if res := run(t, ws); res.Manual || res.Chapters != 2 {
		t.Errorf("with Force: Result = %+v, want the two detected chapters", res)
	}
}

func TestRunRejectsBrokenChaptersJSON(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"not JSON", "{oops", "chapters.json"},
		{"overlapping chapters", `[{"n":1,"title":"A","start_page":1,"start_line":5},{"n":2,"title":"B","start_page":1,"start_line":2}]`, "по порядку"},
		{"page outside the document", `[{"n":1,"title":"A","start_page":9,"start_line":1}]`, "вне документа"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := newBook(t, "Chapter 1\n"+para("One"))
			run(t, ws)
			if err := os.WriteFile(ws.ChaptersJSONPath(), []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Run(context.Background(), ws, quiet)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestRunNeedsExtractedPages(t *testing.T) {
	tmp := t.TempDir()
	pdf := filepath.Join(tmp, "book.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Init(pdf, workspace.InitOptions{Workdir: filepath.Join(tmp, "work")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), ws, quiet); err == nil || !strings.Contains(err.Error(), "extract") {
		t.Errorf("error = %v, want a hint to run extract first", err)
	}

	empty := newBook(t, "", "")
	if _, err := Run(context.Background(), empty, quiet); err == nil || !strings.Contains(err.Error(), "не найдено текста") {
		t.Errorf("error = %v, want a no-text error", err)
	}
}

func TestFormat(t *testing.T) {
	got := Format("Chapter  1", []string{"First.", "Second."})
	if want := "# Chapter 1\n\nFirst.\n\nSecond.\n"; got != want {
		t.Errorf("Format = %q, want %q", got, want)
	}
	if got := Format("Title", nil); got != "# Title\n" {
		t.Errorf("Format without paragraphs = %q", got)
	}
}

// TestRunOnSamplePDF runs extract and chapters on the real fixture.
func TestRunOnSamplePDF(t *testing.T) {
	for _, tool := range []string{"pdfinfo", "pdftotext"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	ws, err := workspace.Init(testdata.Write(t, testdata.SamplePDF), workspace.InitOptions{Workdir: filepath.Join(t.TempDir(), "work")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extract.Run(context.Background(), ws, execx.OSRunner{}, quiet); err != nil {
		t.Fatalf("extract: %v", err)
	}

	res := run(t, ws)
	if res != (Result{Chapters: 5, Written: 5, TOCPages: 1}) {
		t.Errorf("Result = %+v, want the intro and four chapters, one TOC page", res)
	}
	want := map[int][]string{ // chapter → title, first words, last words
		0: {"# " + IntroTitle, "This guide was written", "a part of its own."},
		1: {"# Chapter 1: The Lamp Room", "The lamp room sits", "polished before breakfast."},
		2: {"# Chapter 2: Keeping the Log", "Every keeper writes", "are old enemies."},
		3: {"# Chapter 3: Fog Signals", "Fog arrives without", "with one ear open."},
		4: {"# Chapter 4: The Relief Boat", "Once a month", "the climbing begins again."},
	}
	for n, w := range want {
		text := readFile(t, ws.ChapterPath(n))
		paragraphs := strings.Split(strings.TrimSpace(text), "\n\n")
		if paragraphs[0] != w[0] || !strings.HasPrefix(paragraphs[1], w[1]) || !strings.HasSuffix(paragraphs[len(paragraphs)-1], w[2]) {
			t.Errorf("chapter %d:\n%s", n, text)
		}
		if strings.Contains(text, "A Short Field Guide") || strings.Contains(text, "Contents") {
			t.Errorf("chapter %d contains a running header or the table of contents", n)
		}
	}
}
