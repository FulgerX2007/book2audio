package clean

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// shortLine is the share of the usual line width below which a line that
// ends a sentence is taken for the last line of its paragraph.
const shortLine = 0.85

// Paragraphs joins the lines of one chapter into paragraphs.
//
// pdftotext separates blocks of text with blank lines but usually prints
// the paragraphs of one block without a gap, so a paragraph ends:
//   - at a blank line;
//   - after a line that ends a sentence and is clearly shorter than the
//     usual line (the last line of a justified paragraph);
//   - at a page break, unless the last line of the page stops in the middle
//     of a sentence — then the paragraph continues on the next page.
func Paragraphs(lines []Line) []string {
	width := usualWidth(lines)

	var paragraphs []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			paragraphs = append(paragraphs, Normalize(joinLines(cur)))
			cur = nil
		}
	}

	prevPage := 0
	for _, line := range lines {
		if prevPage != 0 && line.Page != prevPage && len(cur) > 0 && endsSentence(cur[len(cur)-1]) {
			flush()
		}
		prevPage = line.Page

		if line.Text == "" {
			flush()
			continue
		}
		cur = append(cur, line.Text)
		if endsSentence(line.Text) && float64(utf8.RuneCountInString(line.Text)) < shortLine*float64(width) {
			flush()
		}
	}
	flush()
	return paragraphs
}

// usualWidth estimates the width of a full line: the 90th percentile of the
// line lengths, in characters.
func usualWidth(lines []Line) int {
	var lengths []int
	for _, line := range lines {
		if line.Text != "" {
			lengths = append(lengths, utf8.RuneCountInString(line.Text))
		}
	}
	if len(lengths) == 0 {
		return 0
	}
	slices.Sort(lengths)
	return lengths[(len(lengths)-1)*9/10]
}

// endsSentence reports whether the line ends with sentence-final
// punctuation, possibly followed by closing quotes or brackets.
func endsSentence(s string) bool {
	s = strings.TrimRight(s, `"'”’»)]`)
	r, _ := utf8.DecodeLastRuneInString(s)
	return strings.ContainsRune(".!?…", r)
}

// joinLines joins the lines of one paragraph. A word split by a hyphen at
// the end of a line is put together again when it continues with a
// lower-case letter ("exam-" + "ple" gives "example"); before a capital
// letter or a digit the hyphen is kept ("Anglo-" + "Saxon").
func joinLines(lines []string) string {
	var b strings.Builder
	for i, line := range lines {
		if i == 0 {
			b.WriteString(line)
			continue
		}
		prev := lines[i-1]
		first, _ := utf8.DecodeRuneInString(line)
		switch {
		case splitWord(prev) && unicode.IsLower(first):
			// Drop the hyphen already written and continue the word.
			joined := b.String()
			b.Reset()
			b.WriteString(strings.TrimSuffix(strings.TrimSuffix(joined, "-"), "­"))
		case splitWord(prev):
			// Keep the hyphen, no space.
		default:
			b.WriteByte(' ')
		}
		b.WriteString(line)
	}
	return b.String()
}

// splitWord reports whether the line ends with a letter followed by a
// hyphen, that is, in the middle of a word.
func splitWord(s string) bool {
	rest, ok := strings.CutSuffix(s, "-")
	if !ok {
		if rest, ok = strings.CutSuffix(s, "­"); !ok {
			return false
		}
	}
	r, _ := utf8.DecodeLastRuneInString(rest)
	return unicode.IsLetter(r)
}

var normalizer = strings.NewReplacer(
	"­", "", // soft hyphen
	"ﬀ", "ff", "ﬁ", "fi", "ﬂ", "fl", "ﬃ", "ffi", "ﬄ", "ffl", // ligatures
	"``", "“", "''", "”", // TeX-style quotes
	" -- ", " — ", " - ", " — ", // a dash typed as hyphens
)

// Normalize unifies characters that PDFs render in several ways: white
// space of any kind becomes a single space, ligatures become letters, soft
// hyphens disappear, dashes typed as hyphens become dashes.
func Normalize(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return normalizer.Replace(s)
}
