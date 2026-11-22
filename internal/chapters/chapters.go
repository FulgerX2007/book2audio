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
	"github.com/FulgerX2007/book2audio/internal/epub"
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
	// Russian titles for a Russian audiobook; English ones otherwise, which
	// the translate stage turns into the target language.
	opt.IntroTitle, opt.SectionTitle = Titles(ws.Config.TargetPrimary())

	// An EPUB already knows its chapters and paragraphs: nothing is cleaned
	// or guessed from the shape of the lines.
	fromEPUB := epub.IsEPUB(ws.PDF)
	var (
		pages    [][]clean.Line
		detected []Start
		toc      map[int]bool
	)
	if fromEPUB {
		pages = clean.PlainPages(raw)
		detected = marked(pages, opt)
	} else {
		pages = clean.Pages(raw)
		detected, toc = Detect(pages, opt)
	}
	res := Result{TOCPages: len(toc)}

	indexIn := workspace.HashStrings(Stage, clean.RulesVersion, pagesHash, opt.Heading.String(),
		strconv.Itoa(opt.MaxChars), strconv.Itoa(opt.TOCMax), strconv.Itoa(opt.FallbackPages))
	if fromEPUB {
		indexIn = workspace.HashStrings(indexIn, "epub")
	}
	if opt.IntroTitle != IntroTitle {
		// The generated titles enter the hash only when they differ from the
		// Russian ones, so the index of an existing Russian book stays valid.
		indexIn = workspace.HashStrings(indexIn, opt.IntroTitle, opt.SectionTitle)
	}
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
		paragraphs := body
		if fromEPUB {
			in = workspace.HashStrings(in, "epub")
			paragraphs = plainBody
		}
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
			log.Warn("the chapter was edited by hand and is kept; --force rebuilds it", "file", out)
			res.Kept++
			continue
		}
		if err := workspace.WriteFileAtomic(out, []byte(Format(start.Title, paragraphs(lines, start, opt))), 0o644); err != nil {
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
		return nil, "", fmt.Errorf("the text is not extracted yet: run extract first")
	}
	parts := make([]string, 0, n)
	for p := 1; p <= n; p++ {
		u, ok := ws.Manifest.Unit(extract.Stage, extract.UnitID(p))
		if !ok || u.Status != workspace.StatusDone {
			return nil, "", fmt.Errorf("page %d is not extracted: run extract first", p)
		}
		data, err := os.ReadFile(ws.PagePath(p))
		if err != nil {
			return nil, "", fmt.Errorf("page %d: %w", p, err)
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
			log.Info("the chapter starts were taken from the hand-edited chapters.json", "chapters", len(starts))
			return starts, true, nil
		}
	}

	if len(detected) == 0 {
		return nil, false, fmt.Errorf("no text was found in the book: there is nothing to make chapters from")
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
			return nil, fmt.Errorf("the chapters are not made yet: run chapters first")
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

// marked returns the chapter starts of a book read from an EPUB: every page
// whose first line carries extract.ChapterMark begins a chapter. Text
// before the first of them is chapter 0; a book without any mark is split
// into one section per page that has text.
func marked(pages [][]clean.Line, opt Options) []Start {
	intro, section := opt.titles()
	var starts []Start
	var before *clean.Line // the first line of text ahead of the first chapter
	n := 0
	for _, lines := range pages {
		if len(lines) == 0 {
			continue
		}
		first := lines[0]
		if title, ok := strings.CutPrefix(first.Text, extract.ChapterMark); ok {
			n++
			starts = append(starts, Start{N: n, Title: clean.Normalize(title), StartPage: first.Page, StartLine: first.Num})
		} else if n == 0 && before == nil {
			before = &first
		}
	}
	switch {
	case len(starts) == 0:
		for _, lines := range pages {
			if len(lines) > 0 {
				n++
				starts = append(starts, Start{N: n, Title: fmt.Sprintf(section, n), StartPage: lines[0].Page, StartLine: lines[0].Num})
			}
		}
	case before != nil:
		starts = append([]Start{{N: 0, Title: intro, StartPage: before.Page, StartLine: before.Num}}, starts...)
	}
	return starts
}

// plainBody returns the paragraphs of a chapter of a book read from an
// EPUB: every line is a paragraph. Lines with the chapter mark are the
// title of this chapter or of a chapter merged into it by a hand-edited
// chapters.json: the first is left out, later ones are kept as text.
func plainBody(lines []clean.Line, _ Start, _ Options) []string {
	var paragraphs []string
	for i, l := range lines {
		text, isTitle := strings.CutPrefix(l.Text, extract.ChapterMark)
		if text == "" || (isTitle && i == 0) {
			continue
		}
		paragraphs = append(paragraphs, clean.Normalize(text))
	}
	return paragraphs
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
