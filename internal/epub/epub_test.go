package epub_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/FulgerX2007/book2audio/internal/epub"
	"github.com/FulgerX2007/book2audio/internal/epub/epubtest"
)

func write(t *testing.T, spec epubtest.Spec) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "book.epub")
	epubtest.Write(t, path, spec)
	return path
}

func TestIsEPUB(t *testing.T) {
	for name, want := range map[string]bool{"a.epub": true, "/x/Book.EPUB": true, "a.pdf": false, "epub": false, "a.epub.pdf": false} {
		if got := epub.IsEPUB(name); got != want {
			t.Errorf("IsEPUB(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestReadSample(t *testing.T) {
	for _, toc := range []string{"ncx", "nav"} {
		t.Run(toc, func(t *testing.T) {
			spec := epubtest.Sample()
			spec.TOC = toc
			book, err := epub.Read(write(t, spec))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if book.Title != "The Tide Tables" || book.Author != "A. Keeper" {
				t.Errorf("book = %q by %q", book.Title, book.Author)
			}
			want := []epub.Section{
				// Not in the table of contents: no title, its heading stays text.
				{Paragraphs: []string{"The Tide Tables", "A small book for tests."}},
				// The label of the table of contents is the title; the
				// headings the document opens with are not repeated.
				{Title: "Chapter 1: High Water", Paragraphs: []string{
					"The first paragraph of chapter one has a word in italics.",
					"The second paragraph is broken in two lines & uses an entity too.",
				}},
				{Title: "Chapter 2: Low Water", Paragraphs: []string{
					"Chapter two opens here.", "A quoted line.", "First item.", "Second item.",
				}},
				{Paragraphs: []string{"Chapter two goes on in another file."}},
			}
			if len(book.Sections) != len(want) {
				t.Fatalf("sections = %d, want %d: %+v", len(book.Sections), len(want), book.Sections)
			}
			for i, s := range book.Sections {
				if s.Title != want[i].Title || !slices.Equal(s.Paragraphs, want[i].Paragraphs) {
					t.Errorf("section %d = %+v\nwant %+v", i, s, want[i])
				}
			}
		})
	}
}

// Without a table of contents a document that opens with a heading begins
// a chapter.
func TestReadWithoutTOC(t *testing.T) {
	spec := epubtest.Sample()
	spec.TOC = "none"
	book, err := epub.Read(write(t, spec))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var titles []string
	for _, s := range book.Sections {
		titles = append(titles, s.Title)
	}
	want := []string{"The Tide Tables", "Chapter 1. High Water", "Chapter 2: Low Water", ""}
	if !slices.Equal(titles, want) {
		t.Errorf("titles = %q, want %q", titles, want)
	}
	if got := book.Sections[0].Paragraphs; !slices.Equal(got, []string{"A small book for tests."}) {
		t.Errorf("the heading was kept as text: %q", got)
	}
}

func TestReadErrors(t *testing.T) {
	dir := t.TempDir()
	notZip := filepath.Join(dir, "plain.epub")
	os.WriteFile(notZip, []byte("not a zip archive"), 0o644)
	if _, err := epub.Read(notZip); err == nil || !strings.Contains(err.Error(), "plain.epub") {
		t.Errorf("a file that is not an archive: %v", err)
	}
	if _, err := epub.Read(filepath.Join(dir, "missing.epub")); err == nil {
		t.Error("a missing file was read")
	}
	empty := filepath.Join(dir, "empty.epub")
	epubtest.Write(t, empty, epubtest.Spec{Title: "Empty"})
	if _, err := epub.Read(empty); err == nil || !strings.Contains(err.Error(), "no text documents") {
		t.Errorf("a book without documents: %v", err)
	}
}

// Markup that is not well-formed XML still yields its text.
func TestReadLooseMarkup(t *testing.T) {
	spec := epubtest.Spec{Title: "Loose", TOC: "none", Docs: []epubtest.Doc{
		{Name: "a.xhtml", Body: `<p>One line<br>and another.<p>An unclosed paragraph &mdash; with a dash.<img src="x.png"><hr><p>Last.`},
	}}
	book, err := epub.Read(write(t, spec))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	got := book.Sections[0].Paragraphs
	want := []string{"One line and another.", "An unclosed paragraph — with a dash.", "Last."}
	if !slices.Equal(got, want) {
		t.Errorf("paragraphs = %q, want %q", got, want)
	}
}
