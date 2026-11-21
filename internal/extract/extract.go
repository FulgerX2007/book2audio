// Package extract implements the first pipeline stage: the text of every
// PDF page is written to 01-pages/pNNNN.txt.
package extract

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/FulgerX2007/book2audio/internal/execx"
	"github.com/FulgerX2007/book2audio/internal/workpool"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

// Stage is the name of the stage in the manifest.
const Stage = "extract"

// Info is what pdfinfo reports about a PDF.
type Info struct {
	Pages  int
	Title  string
	Author string
}

// Result summarises a run of the stage.
type Result struct {
	Pages     int // pages in the PDF
	Extracted int // pages written by this run
	OCR       int // of them, pages recognised by OCR
	Failed    int // pages that could not be extracted
}

// UnitID returns the manifest unit of page n (1-based).
func UnitID(n int) string { return fmt.Sprintf("p%04d", n) }

// Run extracts the pages that are missing or were made with other settings.
// Pages that are up to date are not touched. It returns
// workspace.ErrUnitsFailed when some pages could not be extracted.
func Run(ctx context.Context, ws *workspace.Workspace, runner execx.Runner, log *slog.Logger) (Result, error) {
	info, err := ReadInfo(ctx, runner, ws.PDF)
	if err != nil {
		return Result{}, err
	}
	src := ws.Manifest.GetSource()
	if src.Pages != info.Pages || src.Title != info.Title || src.Author != info.Author {
		src.Pages, src.Title, src.Author = info.Pages, info.Title, info.Author
		if err := ws.Manifest.SetSource(src); err != nil {
			return Result{}, err
		}
	}

	ocrLang, err := ws.Config.OCRLang()
	if err != nil {
		return Result{}, err
	}
	res := Result{Pages: info.Pages}
	ids := make([]string, 0, info.Pages)
	hashes := make(map[int]string) // pages that need work → input hash
	for n := 1; n <= info.Pages; n++ {
		id := UnitID(n)
		ids = append(ids, id)
		in := workspace.HashStrings(Stage, src.SHA256, strconv.Itoa(n),
			strconv.Itoa(ws.Config.Extract.OCRMinChars), ocrLang, strconv.Itoa(ws.Config.Extract.OCRDPI))
		need, err := ws.Manifest.NeedsWork(Stage, id, in)
		if err != nil {
			return res, err
		}
		if need {
			hashes[n] = in
		}
	}

	if len(hashes) > 0 {
		log.Info("извлечение текста", "pages", info.Pages, "todo", len(hashes))
		pages, err := ReadPages(ctx, runner, ws.PDF, info.Pages)
		if err != nil {
			return res, err
		}
		// save writes the text of page n and records the unit as done.
		save := func(n int, text string, ocr bool) error {
			out := ws.PagePath(n)
			if err := workspace.WriteFileAtomic(out, []byte(text), 0o644); err != nil {
				return fmt.Errorf("страница %d: %w", n, err)
			}
			return ws.Manifest.MarkDone(Stage, UnitID(n), workspace.Unit{In: hashes[n], OCR: ocr}, out)
		}

		// Pages with a text layer are written first, so they are kept even
		// when OCR cannot run at all.
		var scanned []int
		for n := 1; n <= info.Pages; n++ {
			if _, ok := hashes[n]; !ok {
				continue
			}
			if err := ctx.Err(); err != nil {
				return res, err
			}
			if needsOCR(pages[n-1], ws.Config.Extract.OCRMinChars) {
				scanned = append(scanned, n)
				continue
			}
			if err := save(n, pages[n-1], false); err != nil {
				return res, err
			}
			res.Extracted++
		}

		if len(scanned) > 0 {
			log.Info("распознавание страниц без текстового слоя", "pages", len(scanned), "lang", ocrLang)
			if err := checkOCRLang(ctx, runner, ocrLang); err != nil {
				return res, err
			}
			tmpDir, err := os.MkdirTemp("", "book2audio-ocr-")
			if err != nil {
				return res, err
			}
			defer os.RemoveAll(tmpDir)

			var mu sync.Mutex // guards res
			err = workpool.Run(ctx, ws.Config.Extract.OCRWorkers, scanned, func(ctx context.Context, n int) error {
				text, err := ocrPage(ctx, runner, ws.PDF, n, ocrLang, ws.Config.Extract.OCRDPI, tmpDir)
				if err != nil {
					if ctx.Err() != nil {
						return ctx.Err() // interrupted, not a failure of this page
					}
					log.Warn("страница не распознана", "page", n, "err", err)
					mu.Lock()
					res.Failed++
					mu.Unlock()
					return ws.Manifest.MarkFailed(Stage, UnitID(n), workspace.Unit{In: hashes[n], OCR: true}, ws.PagePath(n), err)
				}
				if err := save(n, text, true); err != nil {
					return err
				}
				mu.Lock()
				res.Extracted++
				res.OCR++
				mu.Unlock()
				return nil
			})
			if err != nil {
				return res, err
			}
		}
	}

	if err := ws.Manifest.Prune(Stage, ids); err != nil {
		return res, err
	}
	if res.Failed > 0 {
		return res, fmt.Errorf("%w: извлечение, страниц с ошибкой: %d", workspace.ErrUnitsFailed, res.Failed)
	}
	return res, nil
}

// ReadInfo runs pdfinfo on the PDF.
func ReadInfo(ctx context.Context, runner execx.Runner, pdf string) (Info, error) {
	if _, err := os.Stat(pdf); err != nil {
		return Info{}, fmt.Errorf("исходный PDF: %w", err)
	}
	out, err := runner.Run(ctx, execx.Cmd{Name: "pdfinfo", Args: []string{"-enc", "UTF-8", pdf}})
	if err != nil {
		return Info{}, err
	}
	return parseInfo(out)
}

// parseInfo reads the "Key:   value" lines printed by pdfinfo.
func parseInfo(out []byte) (Info, error) {
	var info Info
	pagesSeen := false
	for line := range strings.Lines(string(out)) {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "Title":
			info.Title = value
		case "Author":
			info.Author = value
		case "Pages":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				return Info{}, fmt.Errorf("pdfinfo: непонятное число страниц %q", value)
			}
			info.Pages, pagesSeen = n, true
		}
	}
	if !pagesSeen {
		return Info{}, fmt.Errorf("pdfinfo: в выводе нет числа страниц")
	}
	return info, nil
}

// ReadPages runs pdftotext once and returns the text of each of the
// wantPages pages.
func ReadPages(ctx context.Context, runner execx.Runner, pdf string, wantPages int) ([]string, error) {
	out, err := runner.Run(ctx, execx.Cmd{Name: "pdftotext", Args: []string{"-enc", "UTF-8", pdf, "-"}})
	if err != nil {
		return nil, err
	}
	pages := splitPages(out)
	if len(pages) != wantPages {
		return nil, fmt.Errorf("pdftotext вернул страниц: %d, а pdfinfo сообщает %d", len(pages), wantPages)
	}
	return pages, nil
}

// splitPages splits the output of pdftotext at form feeds. pdftotext ends
// every page, the last one included, with a form feed, so the empty tail
// after the final one is not a page.
func splitPages(out []byte) []string {
	parts := bytes.Split(out, []byte{'\f'})
	if n := len(parts); n > 0 && len(bytes.TrimSpace(parts[n-1])) == 0 {
		parts = parts[:n-1]
	}
	pages := make([]string, len(parts))
	for i, p := range parts {
		pages[i] = string(p)
	}
	return pages
}

// textChars counts the characters of a page that are not white space.
func textChars(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}
