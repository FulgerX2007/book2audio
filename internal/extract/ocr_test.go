package extract

import (
	"context"
	"errors"
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

// withOCR adds fake pdftoppm and tesseract to the runner. Recognised text is
// "ocr of pNNNN"; pages listed in failing make tesseract fail.
func withOCR(fake *execx.FakeRunner, langs string, failing ...string) *execx.FakeRunner {
	fake.Handle("pdftoppm", func(call execx.Call) ([]byte, error) {
		base := call.Args[len(call.Args)-1]
		return nil, os.WriteFile(base+".png", []byte("png"), 0o644)
	})
	fake.Handle("tesseract", func(call execx.Call) ([]byte, error) {
		if call.Args[0] == "--list-langs" {
			return []byte("List of available languages in \"/usr/share/tessdata/\" (2):\n" + langs + "\n"), nil
		}
		if _, err := os.Stat(call.Args[0]); err != nil {
			return nil, err
		}
		page := strings.TrimSuffix(filepath.Base(call.Args[0]), ".png")
		if slices.Contains(failing, page) {
			return nil, errors.New("tesseract: exit status 1: bad image")
		}
		return []byte("ocr of " + page + "\n\f"), nil
	})
	return fake
}

func TestNeedsOCR(t *testing.T) {
	tests := []struct {
		text string
		min  int
		want bool
	}{
		{"", 50, true},
		{"  \n\n ", 50, true},
		{"12", 50, true},
		{strings.Repeat("a ", 50), 50, false},
		{strings.Repeat("a", 49), 50, true},
		{"", 0, false},
	}
	for _, tt := range tests {
		if got := needsOCR(tt.text, tt.min); got != tt.want {
			t.Errorf("needsOCR(%d chars, min %d) = %v, want %v", textChars(tt.text), tt.min, got, tt.want)
		}
	}
}

func TestRunRecognisesPagesWithoutText(t *testing.T) {
	ws := newBook(t, fakePDF(t), "extract:\n  ocr_dpi: 250\n")
	fake := withOCR(fakeTools("one "+longText, "", "three "+longText, " 4 \n"), "eng\nosd")

	res, err := Run(context.Background(), ws, fake, quiet)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res != (Result{Pages: 4, Extracted: 4, OCR: 2}) {
		t.Errorf("Result = %+v, want 4 pages, 2 of them by OCR", res)
	}
	if got := readPage(t, ws, 2); got != "ocr of p0002\n" {
		t.Errorf("page 2 = %q, want the recognised text without the form feed", got)
	}
	if got := readPage(t, ws, 1); got != "one "+longText {
		t.Errorf("page 1 = %q, want the text layer", got)
	}
	for page, wantOCR := range map[string]bool{"p0001": false, "p0002": true, "p0003": false, "p0004": true} {
		if u, _ := ws.Manifest.Unit(Stage, page); u.OCR != wantOCR || u.Status != workspace.StatusDone {
			t.Errorf("unit %s = %+v, want done with ocr=%v", page, u, wantOCR)
		}
	}

	// Only the pages without text are rendered, with the configured settings.
	var rendered []string
	for _, call := range fake.CallsTo("pdftoppm") {
		args := strings.Join(call.Args, " ")
		if !strings.Contains(args, "-r 250 -png -singlefile") || !slices.Contains(call.Args, ws.PDF) {
			t.Errorf("pdftoppm arguments: %v", call.Args)
		}
		rendered = append(rendered, call.Args[slices.Index(call.Args, "-f")+1])
		if call.Args[slices.Index(call.Args, "-l")+1] != rendered[len(rendered)-1] {
			t.Errorf("pdftoppm renders more than one page: %v", call.Args)
		}
	}
	slices.Sort(rendered)
	if !slices.Equal(rendered, []string{"2", "4"}) {
		t.Errorf("rendered pages = %v, want [2 4]", rendered)
	}
	for _, call := range fake.CallsTo("tesseract") {
		if call.Args[0] == "--list-langs" {
			continue
		}
		if !slices.Equal(call.Args[1:], []string{"stdout", "-l", "eng"}) {
			t.Errorf("tesseract arguments: %v", call.Args)
		}
		if !slices.Contains(call.Env, "OMP_THREAD_LIMIT=1") {
			t.Errorf("tesseract runs without OMP_THREAD_LIMIT=1: %v", call.Env)
		}
		if _, err := os.Stat(call.Args[0]); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the page image %s was left behind", call.Args[0])
		}
	}
}

func TestRunWithoutScannedPagesNeverCallsOCR(t *testing.T) {
	ws := newBook(t, fakePDF(t), "")
	fake := withOCR(fakeTools("one "+longText, "two "+longText), "eng")

	if _, err := Run(context.Background(), ws, fake, quiet); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := len(fake.CallsTo("tesseract")) + len(fake.CallsTo("pdftoppm")); n != 0 {
		t.Errorf("OCR tools were called %d times for a PDF with a text layer", n)
	}
}

func TestRunOCRFailureIsRecordedPerPage(t *testing.T) {
	ws := newBook(t, fakePDF(t), "")
	fake := withOCR(fakeTools("", "two "+longText, ""), "eng", "p0001")

	res, err := Run(context.Background(), ws, fake, quiet)
	if !errors.Is(err, workspace.ErrUnitsFailed) {
		t.Fatalf("error = %v, want ErrUnitsFailed", err)
	}
	if res != (Result{Pages: 3, Extracted: 2, OCR: 1, Failed: 1}) {
		t.Errorf("Result = %+v, want one failed page and the others done", res)
	}
	u, _ := ws.Manifest.Unit(Stage, "p0001")
	if u.Status != workspace.StatusFailed || !strings.Contains(u.Error, "bad image") || u.Out != "01-pages/p0001.txt" {
		t.Errorf("failed unit = %+v", u)
	}
	if _, err := os.Stat(ws.PagePath(1)); !errors.Is(err, os.ErrNotExist) {
		t.Error("a page file was written for the failed page")
	}

	// The next run retries only the failed page.
	before := len(fake.CallsTo("pdftoppm"))
	fixed := withOCR(fake, "eng")
	res, err = Run(context.Background(), ws, fixed, quiet)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if res.Extracted != 1 || res.OCR != 1 {
		t.Errorf("Result = %+v, want only the failed page redone", res)
	}
	if n := len(fake.CallsTo("pdftoppm")) - before; n != 1 {
		t.Errorf("pdftoppm ran %d times on the retry, want once", n)
	}
}

func TestRunOCRLanguageDataMissing(t *testing.T) {
	ws := newBook(t, fakePDF(t), "source_lang: de\n")
	fake := withOCR(fakeTools("one "+longText, ""), "eng\nosd")

	_, err := Run(context.Background(), ws, fake, quiet)
	if err == nil || !strings.Contains(err.Error(), `"deu"`) {
		t.Fatalf("error = %v, want a missing language data error", err)
	}
	if got := readPage(t, ws, 1); got != "one "+longText {
		t.Errorf("the page with a text layer was not kept: %q", got)
	}
	if n := len(fake.CallsTo("pdftoppm")); n != 0 {
		t.Errorf("pages were rendered although OCR cannot run: %d calls", n)
	}
}

func TestCheckOCRLang(t *testing.T) {
	fake := withOCR(execx.NewFakeRunner(), "deu\neng\nosd")
	for lang, wantErr := range map[string]bool{"eng": false, "deu+eng": false, "rus": true, "eng+rus": true} {
		err := checkOCRLang(context.Background(), fake, lang)
		if (err != nil) != wantErr {
			t.Errorf("checkOCRLang(%q) = %v, wantErr %v", lang, err, wantErr)
		}
	}
	if err := checkOCRLang(context.Background(), execx.NewFakeRunner(), "eng"); !errors.Is(err, execx.ErrToolNotFound) {
		t.Errorf("without tesseract: error = %v, want ErrToolNotFound", err)
	}
}

// TestRunOnScanPDF recognises a real image-only page with tesseract.
func TestRunOnScanPDF(t *testing.T) {
	for _, tool := range []string{"pdfinfo", "pdftotext", "pdftoppm", "tesseract"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	if err := checkOCRLang(context.Background(), execx.OSRunner{}, "eng"); err != nil {
		t.Skipf("tesseract has no English data: %v", err)
	}
	ws := newBook(t, testdata.Write(t, testdata.ScanPDF), "")

	res, err := Run(context.Background(), ws, execx.OSRunner{}, quiet)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res != (Result{Pages: 1, Extracted: 1, OCR: 1}) {
		t.Errorf("Result = %+v, want one page recognised by OCR", res)
	}
	text := readPage(t, ws, 1)
	for _, want := range []string{"Chapter 2", "Keeping the Log", "tin box"} {
		if !strings.Contains(text, want) {
			t.Errorf("the recognised text lacks %q:\n%s", want, text)
		}
	}
}
