package extract

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/FulgerX2007/book2audio/internal/execx"
)

// needsOCR reports whether a page has too little text to have a real text
// layer and must be recognised from its image.
func needsOCR(text string, minChars int) bool {
	return textChars(text) < minChars
}

// checkOCRLang verifies that tesseract has the language data for lang
// ("eng" or a combination such as "deu+eng").
func checkOCRLang(ctx context.Context, runner execx.Runner, lang string) error {
	out, err := runner.Run(ctx, execx.Cmd{Name: "tesseract", Args: []string{"--list-langs"}})
	if err != nil {
		return err
	}
	// The first line is a caption; the rest are language codes.
	installed := strings.Fields(string(out))
	for want := range strings.SplitSeq(lang, "+") {
		if !slices.Contains(installed, want) {
			return fmt.Errorf("в tesseract нет языковых данных %q: установите их (пакет tesseract-ocr-%s) или задайте extract.ocr_lang", want, want)
		}
	}
	return nil
}

// ocrPage renders page n of the PDF to an image in tmpDir and returns the
// text recognised by tesseract.
func ocrPage(ctx context.Context, runner execx.Runner, pdf string, n int, lang string, dpi int, tmpDir string) (string, error) {
	base := filepath.Join(tmpDir, UnitID(n))
	png := base + ".png"
	defer os.Remove(png)

	page := strconv.Itoa(n)
	// -singlefile: write exactly <base>.png, without a page number suffix.
	if _, err := runner.Run(ctx, execx.Cmd{
		Name: "pdftoppm",
		Args: []string{"-r", strconv.Itoa(dpi), "-png", "-singlefile", "-f", page, "-l", page, pdf, base},
	}); err != nil {
		return "", err
	}
	out, err := runner.Run(ctx, execx.Cmd{
		Name: "tesseract",
		Args: []string{png, "stdout", "-l", lang},
		// Pages are recognised in parallel by the stage; tesseract's own
		// OpenMP threads would only oversubscribe the CPU.
		Env: []string{"OMP_THREAD_LIMIT=1"},
	})
	if err != nil {
		return "", err
	}
	// tesseract ends its output with a form feed.
	return strings.TrimRight(string(out), "\f"), nil
}
