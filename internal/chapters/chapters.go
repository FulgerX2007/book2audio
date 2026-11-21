package chapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/FulgerX2007/book2audio/internal/clean"
	"github.com/FulgerX2007/book2audio/internal/extract"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

// Stage is the name of the stage in the manifest.
const Stage = "chapters"

// indexUnit is the manifest unit of chapters.json.
const indexUnit = "index"

// UnitID returns the manifest unit of chapter n.
func UnitID(n int) string { return fmt.Sprintf("ch%02d", n) }

// Result summarises a run of the stage.
type Result struct {
	Chapters int  // chapters in chapters.json
	Written  int  // chapter files written by this run
	Kept     int  // chapter files left alone because they were edited by hand
	TOCPages int  // pages left out as tables of contents
	Manual   bool // the chapter starts were taken from a hand-edited chapters.json
}

// Run cleans the page text, finds the chapters and writes one file per
// chapter. Files edited by hand — chapters.json and the chapter files — are
// not overwritten unless the manifest has Force set.
func Run(ctx context.Context, ws *workspace.Workspace, log *slog.Logger) (Result, error) {
	raw, pagesHash, err := readPages(ws)
	if err != nil {
		return Result{}, err
	}
	heading, err := regexp.Compile(ws.Config.Chapters.HeadingRegex)
	if err != nil {
		return Result{}, fmt.Errorf("chapters.heading_regex: %w", err)
	}
	opt := Options{
		Heading:       heading,
		MaxChars:      ws.Config.Chapters.HeadingMaxChars,
		TOCMax:        ws.Config.Chapters.TOCMaxHeadings,
		FallbackPages: ws.Config.Chapters.FallbackPages,
	}

	pages := clean.Pages(raw)
	detected, toc := Detect(pages, opt)
	res := Result{TOCPages: len(toc)}

	indexIn := workspace.HashStrings(Stage, clean.RulesVersion, pagesHash, opt.Heading.String(),
		strconv.Itoa(opt.MaxChars), strconv.Itoa(opt.TOCMax), strconv.Itoa(opt.FallbackPages))
	starts, manual, err := resolveStarts(ws, detected, indexIn, len(raw), log)
	if err != nil {
		return res, err
	}
	res.Chapters, res.Manual = len(starts), manual

	keep := []string{indexUnit}
	for i, lines := range Split(pages, toc, starts) {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		start := starts[i]
		id := UnitID(start.N)
		keep = append(keep, id)

		in := workspace.HashStrings(Stage, clean.RulesVersion, start.Title, sourceText(lines))
		need, err := ws.Manifest.NeedsWork(Stage, id, in)
		if err != nil {
			return res, err
		}
		if !need {
			// Up to date — unless --force asks to discard a hand edit.
			edited, err := ws.Manifest.Edited(Stage, id)
			if err != nil {
				return res, err
			}
			if !ws.Manifest.Force || !edited {
				continue
			}
		}
		out := ws.ChapterPath(start.N)
		if protected, err := ws.Manifest.Protected(Stage, id); err != nil {
			return res, err
		} else if protected {
			log.Warn("глава изменена вручную и не перезаписана; --force пересоздаст её", "file", out)
			res.Kept++
			continue
		}
		if err := workspace.WriteFileAtomic(out, []byte(Format(start.Title, body(lines, start, opt))), 0o644); err != nil {
			return res, err
		}
		if err := ws.Manifest.MarkDone(Stage, id, workspace.Unit{In: in}, out); err != nil {
			return res, err
		}
		res.Written++
	}

	return res, ws.Manifest.Prune(Stage, keep)
}

// readPages returns the raw text of every page and a hash of all of them.
func readPages(ws *workspace.Workspace) (pages []string, hash string, err error) {
	n := ws.Manifest.GetSource().Pages
	if n == 0 {
		return nil, "", fmt.Errorf("текст ещё не извлечён: сначала выполните extract")
	}
	parts := make([]string, 0, n)
	for p := 1; p <= n; p++ {
		u, ok := ws.Manifest.Unit(extract.Stage, extract.UnitID(p))
		if !ok || u.Status != workspace.StatusDone {
			return nil, "", fmt.Errorf("страница %d не извлечена: сначала выполните extract", p)
		}
		data, err := os.ReadFile(ws.PagePath(p))
		if err != nil {
			return nil, "", fmt.Errorf("страница %d: %w", p, err)
		}
		pages = append(pages, string(data))
		parts = append(parts, string(data))
	}
	return pages, workspace.HashStrings(parts...), nil
}

// resolveStarts decides where the chapters begin: a chapters.json edited by
// hand wins over detection; otherwise the detected starts are written to it.
func resolveStarts(ws *workspace.Workspace, detected []Start, in string, pages int, log *slog.Logger) (starts []Start, manual bool, err error) {
	path := ws.ChaptersJSONPath()
	_, statErr := os.Stat(path)
	exists := statErr == nil
	if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return nil, false, statErr
	}

	if exists && !ws.Manifest.Force {
		// The file is the user's when it differs from what the stage wrote
		// or when the stage has never written it.
		_, known := ws.Manifest.Unit(Stage, indexUnit)
		edited, err := ws.Manifest.Edited(Stage, indexUnit)
		if err != nil {
			return nil, false, err
		}
		if !known || edited {
			starts, err := Load(ws)
			if err != nil {
				return nil, false, err
			}
			if err := Validate(starts, pages); err != nil {
				return nil, false, fmt.Errorf("%s: %w", path, err)
			}
			log.Info("границы глав взяты из chapters.json, изменённого вручную", "chapters", len(starts))
			return starts, true, nil
		}
	}

	if len(detected) == 0 {
		return nil, false, fmt.Errorf("в книге не найдено текста: главы не из чего собрать")
	}
	need, err := ws.Manifest.NeedsWork(Stage, indexUnit, in)
	if err != nil {
		return nil, false, err
	}
	if need || ws.Manifest.Force {
		data, err := json.MarshalIndent(detected, "", "  ")
		if err != nil {
			return nil, false, err
		}
		if err := workspace.WriteFileAtomic(path, append(data, '\n'), 0o644); err != nil {
			return nil, false, err
		}
		if err := ws.Manifest.MarkDone(Stage, indexUnit, workspace.Unit{In: in}, path); err != nil {
			return nil, false, err
		}
	}
	return detected, false, nil
}

// Load reads chapters.json of the book.
func Load(ws *workspace.Workspace) ([]Start, error) {
	path := ws.ChaptersJSONPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("главы ещё не выделены: сначала выполните chapters")
		}
		return nil, err
	}
	var starts []Start
	if err := json.Unmarshal(data, &starts); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return starts, nil
}

// sourceText is the text of the chapter lines that the chapter file is made
// from; its hash tells whether the file is up to date.
func sourceText(lines []clean.Line) string {
	var b strings.Builder
	page := 0
	for _, l := range lines {
		if l.Page != page {
			b.WriteString("\f")
			page = l.Page
		}
		b.WriteString(l.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

// body returns the paragraphs of a chapter. The heading line itself is left
// out: the title is written as the first line of the file.
func body(lines []clean.Line, start Start, opt Options) []string {
	if len(lines) > 0 {
		first := lines[0].Text
		if first == start.Title || (opt.Heading.MatchString(first) && utf8.RuneCountInString(first) <= opt.MaxChars) {
			lines = lines[1:]
		}
	}
	return clean.Paragraphs(lines)
}

// Format renders a chapter file: "# Title" and the paragraphs, separated by
// blank lines.
func Format(title string, paragraphs []string) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(clean.Normalize(title))
	b.WriteByte('\n')
	for _, p := range paragraphs {
		b.WriteByte('\n')
		b.WriteString(p)
		b.WriteByte('\n')
	}
	return b.String()
}
