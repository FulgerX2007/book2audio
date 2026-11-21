// Package chapters implements the second pipeline stage: the cleaned page
// text is split into chapters, written to 02-chapters/chNN.md.
package chapters

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/FulgerX2007/book2audio/internal/clean"
)

// IntroTitle is the title of chapter 0: the text before the first heading.
const IntroTitle = "Вступление"

// shortLine mirrors the paragraph rule of package clean: a heading that is
// followed directly by text must be clearly shorter than a full line.
const shortLine = 0.85

// Start is where a chapter begins. A chapter ends where the next one
// begins. This is the element of the hand-editable chapters.json.
type Start struct {
	N         int    `json:"n"`          // chapter number; 0 is the text before the first heading
	Title     string `json:"title"`      // as read aloud and shown in the audiobook
	StartPage int    `json:"start_page"` // 1-based page
	StartLine int    `json:"start_line"` // 1-based line in 01-pages/pNNNN.txt
}

// Options control chapter detection.
type Options struct {
	Heading       *regexp.Regexp // a heading line matches it
	MaxChars      int            // and is not longer than this
	TOCMax        int            // a page with more matching lines is a table of contents
	FallbackPages int            // section size when no heading is found
}

// Detect finds the chapter starts in the cleaned pages (see clean.Pages)
// and the pages that are tables of contents.
//
// A heading is a line that matches the heading pattern, stands at the top
// of a page or after a blank line, is short and does not read like a
// sentence. Lines of a table of contents match the pattern too, so a page
// with more than TOCMax matches yields no chapters.
//
// Without any heading the book is cut into sections of FallbackPages pages.
func Detect(pages [][]clean.Line, opt Options) (starts []Start, toc map[int]bool) {
	var all []clean.Line
	for _, lines := range pages {
		all = append(all, lines...)
	}
	width := clean.UsualWidth(all)

	toc = make(map[int]bool)
	for i, lines := range pages {
		matches := 0
		for _, l := range lines {
			if opt.Heading.MatchString(l.Text) {
				matches++
			}
		}
		if matches > opt.TOCMax {
			toc[i+1] = true
		}
	}

	var first *clean.Line // first line of the book text
	for i, lines := range pages {
		if toc[i+1] || len(lines) == 0 {
			continue
		}
		if first == nil {
			first = &lines[0]
		}
		for j, l := range lines {
			if !isHeading(lines, j, opt, width) {
				continue
			}
			starts = append(starts, Start{N: len(starts) + 1, Title: l.Text, StartPage: l.Page, StartLine: l.Num})
		}
	}
	if first == nil {
		return nil, toc // no text at all
	}

	if len(starts) == 0 {
		return fallback(pages, toc, opt.FallbackPages), toc
	}
	if starts[0].StartPage != first.Page || starts[0].StartLine != first.Num {
		intro := Start{N: 0, Title: IntroTitle, StartPage: first.Page, StartLine: first.Num}
		starts = append([]Start{intro}, starts...)
	}
	return starts, toc
}

// isHeading reports whether line j of a page is a chapter heading.
func isHeading(lines []clean.Line, j int, opt Options, width int) bool {
	text := lines[j].Text
	n := utf8.RuneCountInString(text)
	if text == "" || n > opt.MaxChars || !opt.Heading.MatchString(text) {
		return false
	}
	if j > 0 && lines[j-1].Text != "" {
		return false // in the middle of a block of text
	}
	if strings.ContainsRune(".,;", rune(text[len(text)-1])) {
		return false // reads like a sentence: "Chapter 3 covers fog."
	}
	alone := j == len(lines)-1 || lines[j+1].Text == ""
	return alone || float64(n) < shortLine*float64(width)
}

// fallback cuts the pages with text into sections of size pages.
func fallback(pages [][]clean.Line, toc map[int]bool, size int) []Start {
	var starts []Start
	inSection := 0
	for i, lines := range pages {
		if toc[i+1] || len(lines) == 0 {
			continue
		}
		if inSection == 0 {
			n := len(starts) + 1
			starts = append(starts, Start{N: n, Title: fmt.Sprintf("Часть %d", n), StartPage: lines[0].Page, StartLine: lines[0].Num})
		}
		if inSection++; inSection == size {
			inSection = 0
		}
	}
	return starts
}

// Validate checks a list of chapter starts, as read from chapters.json.
func Validate(starts []Start, pages int) error {
	if len(starts) == 0 {
		return fmt.Errorf("the chapter list is empty")
	}
	seen := make(map[int]bool)
	for i, s := range starts {
		switch {
		case s.N < 0:
			return fmt.Errorf("chapter %d: the number must not be negative", s.N)
		case seen[s.N]:
			return fmt.Errorf("chapter %d is listed twice", s.N)
		case strings.TrimSpace(s.Title) == "":
			return fmt.Errorf("chapter %d: the title is empty", s.N)
		case s.StartPage < 1 || s.StartPage > pages:
			return fmt.Errorf("chapter %d: page %d is outside the document (%d pages)", s.N, s.StartPage, pages)
		case s.StartLine < 1:
			return fmt.Errorf("chapter %d: the line number must be at least 1", s.N)
		}
		seen[s.N] = true
		if i > 0 && !before(starts[i-1], s) {
			return fmt.Errorf("chapter %d does not start after chapter %d: chapters must be in order and must not overlap",
				s.N, starts[i-1].N)
		}
	}
	return nil
}

// before reports whether a starts strictly before b.
func before(a, b Start) bool {
	if a.StartPage != b.StartPage {
		return a.StartPage < b.StartPage
	}
	return a.StartLine < b.StartLine
}

// Split distributes the lines of the book between the chapters: chapter i
// gets the lines from its start up to the start of chapter i+1. Pages in
// toc are left out, as are lines before the first start.
func Split(pages [][]clean.Line, toc map[int]bool, starts []Start) [][]clean.Line {
	out := make([][]clean.Line, len(starts))
	cur := -1
	for i, lines := range pages {
		for _, l := range lines {
			for cur+1 < len(starts) && !lineBefore(l, starts[cur+1]) {
				cur++
			}
			if cur >= 0 && !toc[i+1] {
				out[cur] = append(out[cur], l)
			}
		}
	}
	return out
}

// lineBefore reports whether the line stands before the chapter start.
func lineBefore(l clean.Line, s Start) bool {
	if l.Page != s.StartPage {
		return l.Page < s.StartPage
	}
	return l.Num < s.StartLine
}
