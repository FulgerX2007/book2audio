package extract

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/FulgerX2007/book2audio/internal/execx"
	"github.com/FulgerX2007/book2audio/internal/testdata"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// longText is a page body long enough not to be taken for a scanned page.
const longText = "This page carries enough plain text to count as a page with a real text layer."

// newBook creates a working folder for the PDF at pdf. cfg, when not empty,
// is the book config.
func newBook(t *testing.T, pdf, cfg string) *workspace.Workspace {
	t.Helper()
	opts := workspace.InitOptions{Workdir: filepath.Join(t.TempDir(), "work")}
	if cfg != "" {
		opts.ConfigPath = filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(opts.ConfigPath, []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := workspace.Init(pdf, opts)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return ws
}

// fakePDF writes a stand-in PDF file; the fake tools never read it.
func fakePDF(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "book.pdf")
	if err := os.WriteFile(path, []byte("%PDF-fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeTools returns a runner whose pdfinfo and pdftotext describe a PDF with
// the given pages.
func fakeTools(pages ...string) *execx.FakeRunner {
	fake := execx.NewFakeRunner()
	fake.Stub("pdfinfo", fmt.Appendf(nil, "Title:          Demo Book\nAuthor:         Somebody\nPages:          %d\nEncrypted:      no\n", len(pages)), nil)
	fake.Stub("pdftotext", []byte(strings.Join(pages, "\f")+"\f"), nil)
	return fake
}

func readPage(t *testing.T, ws *workspace.Workspace, n int) string {
	t.Helper()
	data, err := os.ReadFile(ws.PagePath(n))
	if err != nil {
		t.Fatalf("page %d: %v", n, err)
	}
	return string(data)
}

func TestParseInfo(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		want    Info
		wantErr string
	}{
		{
			name: "typical output",
			out:  "Title:           A Guide: Part One\nAuthor:          Jane Roe\nCreator:         TeX\nPages:           371\nPage size:       612 x 792 pts (letter)\n",
			want: Info{Pages: 371, Title: "A Guide: Part One", Author: "Jane Roe"},
		},
		{
			name: "no title and author",
			out:  "Producer:  x\nPages:     1\n",
			want: Info{Pages: 1},
		},
		{name: "no page count", out: "Title: x\n", wantErr: "нет числа страниц"},
		{name: "garbled page count", out: "Pages: many\n", wantErr: "непонятное число страниц"},
		{name: "zero pages", out: "Pages: 0\n", wantErr: "непонятное число страниц"},
		{name: "empty output", out: "", wantErr: "нет числа страниц"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseInfo([]byte(tt.out))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseInfo: %v", err)
			}
			if got != tt.want {
				t.Errorf("parseInfo = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSplitPages(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []string
	}{
		{"form feed after every page", "one\n\ftwo\n\f", []string{"one\n", "two\n"}},
		{"no trailing form feed", "one\n\ftwo\n", []string{"one\n", "two\n"}},
		{"empty page in the middle is kept", "one\f\fthree\f", []string{"one", "", "three"}},
		{"empty last page is kept", "one\f\f", []string{"one", ""}},
		{"single page", "only\f", []string{"only"}},
		{"white space after the last form feed", "one\f\n", []string{"one"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := splitPages([]byte(tt.out)); !slices.Equal(got, tt.want) {
				t.Errorf("splitPages = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTextChars(t *testing.T) {
	if got := textChars(" ab\n\tв г \f"); got != 4 {
		t.Errorf("textChars = %d, want 4", got)
	}
}

func TestRunWritesPagesAndSource(t *testing.T) {
	ws := newBook(t, fakePDF(t), "")
	fake := fakeTools("first "+longText+"\n", "second "+longText+"\n", "third "+longText+"\n")

	res, err := Run(context.Background(), ws, fake, quiet)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res != (Result{Pages: 3, Extracted: 3}) {
		t.Errorf("Result = %+v, want 3 pages extracted", res)
	}
	for n, prefix := range []string{"first ", "second ", "third "} {
		if got := readPage(t, ws, n+1); got != prefix+longText+"\n" {
			t.Errorf("page %d = %q", n+1, got)
		}
	}
	if c := ws.Manifest.Counts(Stage); c != (workspace.Counts{Total: 3, Done: 3}) {
		t.Errorf("Counts = %+v, want 3 done", c)
	}
	src := ws.Manifest.GetSource()
	if src.Pages != 3 || src.Title != "Demo Book" || src.Author != "Somebody" {
		t.Errorf("source = %+v, want the pdfinfo values", src)
	}

	for _, call := range fake.Calls() {
		if !slices.Contains(call.Args, ws.PDF) {
			t.Errorf("%s was not given the PDF path: %v", call.Name, call.Args)
		}
	}
	if args := fake.CallsTo("pdftotext")[0].Args; args[len(args)-1] != "-" {
		t.Errorf("pdftotext does not write to stdout: %v", args)
	}
}

func TestRunSkipsPagesThatAreUpToDate(t *testing.T) {
	ws := newBook(t, fakePDF(t), "")
	fake := fakeTools("one "+longText, "two "+longText)
	if _, err := Run(context.Background(), ws, fake, quiet); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	res, err := Run(context.Background(), ws, fake, quiet)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if res != (Result{Pages: 2}) {
		t.Errorf("Result = %+v, want nothing extracted", res)
	}
	if n := len(fake.CallsTo("pdftotext")); n != 1 {
		t.Errorf("pdftotext ran %d times, want once", n)
	}

	// A lost page file is restored; the other page is left alone.
	if err := os.Remove(ws.PagePath(2)); err != nil {
		t.Fatal(err)
	}
	res, err = Run(context.Background(), ws, fake, quiet)
	if err != nil {
		t.Fatalf("third Run: %v", err)
	}
	if res.Extracted != 1 || readPage(t, ws, 2) != "two "+longText {
		t.Errorf("Result = %+v, page 2 = %q", res, readPage(t, ws, 2))
	}
}

func TestRunReextractsWhenSettingsChange(t *testing.T) {
	pdf := fakePDF(t)
	ws := newBook(t, pdf, "")
	fake := fakeTools("one "+longText, "two "+longText)
	if _, err := Run(context.Background(), ws, fake, quiet); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if err := os.WriteFile(ws.ConfigPath(), []byte("extract:\n  ocr_dpi: 400\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reopened, err := workspace.Open(ws.Dir, workspace.OpenOptions{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	res, err := Run(context.Background(), reopened, fake, quiet)
	if err != nil {
		t.Fatalf("Run after the config change: %v", err)
	}
	if res.Extracted != 2 {
		t.Errorf("Extracted = %d, want both pages redone after an OCR setting changed", res.Extracted)
	}
}

func TestRunPrunesPagesThatNoLongerExist(t *testing.T) {
	ws := newBook(t, fakePDF(t), "")
	if _, err := Run(context.Background(), ws, fakeTools("1 "+longText, "2 "+longText, "3 "+longText), quiet); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := Run(context.Background(), ws, fakeTools("1 "+longText, "2 "+longText), quiet); err != nil {
		t.Fatalf("Run with fewer pages: %v", err)
	}
	if got := ws.Manifest.UnitIDs(Stage); !slices.Equal(got, []string{"p0001", "p0002"}) {
		t.Errorf("units = %v, want [p0001 p0002]", got)
	}
}

func TestRunErrors(t *testing.T) {
	boom := errors.New("exit status 1")

	t.Run("pdfinfo fails", func(t *testing.T) {
		ws := newBook(t, fakePDF(t), "")
		fake := fakeTools("x")
		fake.Stub("pdfinfo", nil, boom)
		if _, err := Run(context.Background(), ws, fake, quiet); !errors.Is(err, boom) {
			t.Errorf("error = %v, want %v", err, boom)
		}
	})
	t.Run("pdftotext fails", func(t *testing.T) {
		ws := newBook(t, fakePDF(t), "")
		fake := fakeTools("x")
		fake.Stub("pdftotext", nil, boom)
		if _, err := Run(context.Background(), ws, fake, quiet); !errors.Is(err, boom) {
			t.Errorf("error = %v, want %v", err, boom)
		}
		if c := ws.Manifest.Counts(Stage); c.Total != 0 {
			t.Errorf("units were recorded although nothing was extracted: %+v", c)
		}
	})
	t.Run("page count mismatch", func(t *testing.T) {
		ws := newBook(t, fakePDF(t), "")
		fake := fakeTools("one", "two")
		fake.Stub("pdfinfo", []byte("Pages: 5\n"), nil)
		_, err := Run(context.Background(), ws, fake, quiet)
		if err == nil || !strings.Contains(err.Error(), "страниц: 2") || !strings.Contains(err.Error(), "5") {
			t.Errorf("error = %v, want a page count mismatch", err)
		}
	})
	t.Run("the PDF is gone", func(t *testing.T) {
		pdf := fakePDF(t)
		ws := newBook(t, pdf, "")
		if err := os.Remove(pdf); err != nil {
			t.Fatal(err)
		}
		fake := fakeTools("x")
		if _, err := Run(context.Background(), ws, fake, quiet); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("error = %v, want os.ErrNotExist", err)
		}
		if n := len(fake.Calls()); n != 0 {
			t.Errorf("tools were run for a missing PDF: %d calls", n)
		}
	})
	t.Run("cancelled context", func(t *testing.T) {
		ws := newBook(t, fakePDF(t), "")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Run(ctx, ws, fakeTools("x"), quiet); !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
	})
}

// TestRunOnSamplePDF runs the stage with the real poppler tools.
func TestRunOnSamplePDF(t *testing.T) {
	for _, tool := range []string{"pdfinfo", "pdftotext"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	ws := newBook(t, testdata.Write(t, testdata.SamplePDF), "")

	res, err := Run(context.Background(), ws, execx.OSRunner{}, quiet)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res != (Result{Pages: testdata.SamplePages, Extracted: testdata.SamplePages}) {
		t.Errorf("Result = %+v, want %d pages extracted", res, testdata.SamplePages)
	}
	if src := ws.Manifest.GetSource(); src.Title != "A Short Field Guide to Lighthouses" {
		t.Errorf("title = %q", src.Title)
	}

	for page, want := range map[int][]string{
		1: {"Contents", "Chapter 4: The Relief Boat"},
		2: {"A Short Field Guide to Lighthouses", "Chapter 1: The Lamp Room", "ninety-two steps"},
		3: {"By morning the oil is low", "Chapter 2: Keeping the Log"},
		4: {"Chapter 3: Fog Signals", "the climbing begins again"},
	} {
		text := readPage(t, ws, page)
		for _, s := range want {
			if !strings.Contains(text, s) {
				t.Errorf("page %d lacks %q", page, s)
			}
		}
		if strings.Contains(text, "\f") {
			t.Errorf("page %d contains a form feed", page)
		}
	}
}
