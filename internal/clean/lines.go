// Package clean turns the raw text of PDF pages into paragraphs: it drops
// running headers, footers and page numbers, then joins the lines of each
// paragraph.
package clean

import (
	"regexp"
	"strings"
)

// RulesVersion identifies the cleaning rules. It is part of the input hash
// of the chapters stage: bump it when the rules change, so the chapter
// files are rebuilt.
const RulesVersion = "1"

// minRepeats is how many pages must start (or end) with the same line for
// it to be taken as a running header (or footer).
const minRepeats = 3

// Line is one line of a page.
type Line struct {
	Page int    // 1-based page number
	Num  int    // 1-based line number in the raw page file
	Text string // without surrounding white space; "" for a blank line
}

var (
	// "12", "- 12 -", "— 12 —"
	arabicPageRe = regexp.MustCompile(`^[-–—\s]*\d{1,4}[-–—\s]*$`)
	// "Page 12", "Стр. 12", "с. 12"
	labelPageRe = regexp.MustCompile(`(?i)^(page|стр\.?|с\.)\s*\d{1,4}$`)
	// "xii" — a well-formed roman numeral (the empty match is excluded by the caller)
	romanPageRe = regexp.MustCompile(`(?i)^m{0,3}(cm|cd|d?c{0,3})(xc|xl|l?x{0,3})(ix|iv|v?i{0,3})$`)

	digitsRe = regexp.MustCompile(`\d+`)
	spacesRe = regexp.MustCompile(`\s+`)
)

// Pages splits the raw text of each page into lines and removes what is not
// part of the book text: page numbers and running headers and footers that
// stand first or last on a page. Line numbers refer to the raw page text,
// so a position found in the result can be looked up in 01-pages/pNNNN.txt.
// Blank lines inside a page are kept; they separate blocks of text.
func Pages(pages []string) [][]Line {
	out := make([][]Line, len(pages))
	for i, text := range pages {
		out[i] = trimBlank(splitLines(i+1, text))
	}

	// A page number may stand outside or inside the running header, so the
	// numbers are dropped both before and after the headers.
	dropEdges(out, isPageNumber)
	dropRepeated(out, firstIndex)
	dropRepeated(out, lastIndex)
	dropEdges(out, isPageNumber)
	return out
}

func splitLines(page int, text string) []Line {
	var lines []Line
	num := 0
	for raw := range strings.SplitSeq(text, "\n") {
		num++
		lines = append(lines, Line{Page: page, Num: num, Text: strings.TrimSpace(raw)})
	}
	return lines
}

// trimBlank removes blank lines at the start and at the end.
func trimBlank(lines []Line) []Line {
	for len(lines) > 0 && lines[0].Text == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1].Text == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func firstIndex(lines []Line) int { return 0 }
func lastIndex(lines []Line) int  { return len(lines) - 1 }

// dropEdges removes the first and the last line of every page while they
// satisfy drop.
func dropEdges(pages [][]Line, drop func(string) bool) {
	for i, lines := range pages {
		for len(lines) > 0 && drop(lines[0].Text) {
			lines = trimBlank(lines[1:])
		}
		for len(lines) > 0 && drop(lines[len(lines)-1].Text) {
			lines = trimBlank(lines[:len(lines)-1])
		}
		pages[i] = lines
	}
}

// dropRepeated removes the line at the position chosen by index (first or
// last of a page) when the same line, digits aside, stands at that position
// on at least minRepeats pages.
func dropRepeated(pages [][]Line, index func([]Line) int) {
	counts := make(map[string]int)
	for _, lines := range pages {
		if len(lines) > 0 {
			counts[headerKey(lines[index(lines)].Text)]++
		}
	}
	for i, lines := range pages {
		if len(lines) == 0 {
			continue
		}
		at := index(lines)
		if counts[headerKey(lines[at].Text)] < minRepeats {
			continue
		}
		if at == 0 {
			lines = lines[1:]
		} else {
			lines = lines[:at]
		}
		pages[i] = trimBlank(lines)
	}
}

// headerKey normalises a line for comparison between pages: the page number
// inside a header ("Guide   12") must not make the headers differ.
func headerKey(s string) string {
	s = digitsRe.ReplaceAllString(s, "#")
	return strings.ToLower(spacesRe.ReplaceAllString(s, " "))
}

func isPageNumber(s string) bool {
	if s == "" {
		return false
	}
	if arabicPageRe.MatchString(s) || labelPageRe.MatchString(s) {
		return true
	}
	// A lower-case roman numeral (front matter), but not a one-letter word.
	return len(s) > 1 && s == strings.ToLower(s) && romanPageRe.MatchString(s)
}
