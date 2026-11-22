package extract

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/FulgerX2007/book2audio/internal/epub"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

// ChapterMark begins the first line of a page that starts a chapter in a
// book read from an EPUB; the chapter title follows it.
const ChapterMark = "# "

// runEPUB extracts an EPUB: one "page" per document of its reading order,
// one paragraph per line. No external tool is needed.
func runEPUB(ctx context.Context, ws *workspace.Workspace, log *slog.Logger) (Result, error) {
	book, err := epub.Read(ws.PDF)
	if err != nil {
		return Result{}, err
	}
	pages := len(book.Sections)
	src := ws.Manifest.GetSource()
	if src.Pages != pages || src.Title != book.Title || src.Author != book.Author {
		src.Pages, src.Title, src.Author = pages, book.Title, book.Author
		if err := ws.Manifest.SetSource(src); err != nil {
			return Result{}, err
		}
	}

	res := Result{Pages: pages}
	ids := make([]string, 0, pages)
	for n := 1; n <= pages; n++ {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		id := UnitID(n)
		ids = append(ids, id)
		in := workspace.HashStrings(Stage, src.SHA256, strconv.Itoa(n), "epub")
		need, err := ws.Manifest.NeedsWork(Stage, id, in)
		if err != nil {
			return res, err
		}
		if !need {
			continue
		}
		if res.Extracted == 0 {
			log.Info("extracting text", "documents", pages)
		}
		out := ws.PagePath(n)
		if err := workspace.WriteFileAtomic(out, []byte(EPUBPage(book.Sections[n-1])), 0o644); err != nil {
			return res, fmt.Errorf("document %d: %w", n, err)
		}
		if err := ws.Manifest.MarkDone(Stage, id, workspace.Unit{In: in}, out); err != nil {
			return res, err
		}
		res.Extracted++
	}
	return res, ws.Manifest.Prune(Stage, ids)
}

// EPUBPage renders a document of an EPUB as a page file: the chapter title
// after ChapterMark when the document starts a chapter, then one paragraph
// per line, separated by blank lines.
func EPUBPage(s epub.Section) string {
	var b strings.Builder
	if s.Title != "" {
		b.WriteString(ChapterMark)
		b.WriteString(s.Title)
		b.WriteString("\n\n")
	}
	for _, p := range s.Paragraphs {
		// A paragraph that happens to begin like the mark must not read as one.
		if strings.HasPrefix(p, ChapterMark) {
			p = strings.TrimLeft(p, "# ")
		}
		b.WriteString(p)
		b.WriteString("\n\n")
	}
	return b.String()
}
