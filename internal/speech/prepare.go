// Package speech prepares the translated text of a chapter for the speech
// engine: it removes what must not be read aloud and cuts the text into
// segments.
package speech

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/FulgerX2007/book2audio/internal/chunk"
)

// URLWord is read instead of a web address in a Russian audiobook.
const URLWord = "ссылка"

// URLWordFor returns the word read instead of a web address in an
// audiobook in the language lang (a primary subtag such as "ru").
func URLWordFor(lang string) string {
	if lang == "ru" || lang == "" {
		return URLWord
	}
	return "link"
}

// Segment is a piece of text synthesised in one call of the speech engine.
type Segment struct {
	Text string
	// EndsParagraph is set on the last segment of a paragraph; a pause
	// follows it in the audio.
	EndsParagraph bool
	// Heading is set on the segment that holds the chapter title.
	Heading bool
}

var (
	urlRe = regexp.MustCompile(`(?i)\b(?:https?://|www\.)[^\s<>"«»]+`)
	// Footnote references: "[12]", "[*]" and superscript digits.
	footnoteRe = regexp.MustCompile(`\[(?:\d{1,3}|\*+)\]|[⁰¹²³⁴-⁹]+`)
	// Markdown emphasis and code marks around words: **bold**, _italic_,
	// `code`. The marks must stand at word boundaries, so the underscores
	// of my_file_name are left alone.
	emphasisRe = regexp.MustCompile("(^|[^\\p{L}\\p{N}_*`])(?:\\*{1,3}|_{1,3}|`)(\\S(?:.*?\\S)??)(?:\\*{1,3}|_{1,3}|`)($|[^\\p{L}\\p{N}_*`])")
	// A Markdown heading or quote mark at the start of a line.
	linePrefixRe = regexp.MustCompile(`(?m)^\s*(?:#{1,6}|>)\s+`)
	spaceRe      = regexp.MustCompile(`\s+`)
	// A space left before punctuation after something was removed.
	spaceBeforePunctRe = regexp.MustCompile(`\s+([.,;:!?…])`)
)

var replacer = strings.NewReplacer(
	"…", "...",
	"–", "—",
	"[", "(", "]", ")",
	"«", `"`, "»", `"`, "„", `"`, "“", `"`, "”", `"`,
	"‘", "'", "’", "'",
	" ", " ", "­", "",
)

// Clean prepares text for reading aloud: Markdown marks, footnote
// references and web addresses are removed or replaced, and characters that
// speech engines handle unevenly (typographic quotes, dashes, ellipses,
// square brackets) are brought to one plain form. Line breaks become
// spaces: engines treat a new line as a new utterance.
func Clean(s string) string { return CleanLang(s, "ru") }

// CleanLang is Clean for an audiobook in the language lang.
func CleanLang(s, lang string) string {
	s = linePrefixRe.ReplaceAllString(s, "")
	s = urlRe.ReplaceAllString(s, URLWordFor(lang))
	s = footnoteRe.ReplaceAllString(s, "")
	// Emphasis may be nested ("**_word_**"): peel it layer by layer.
	for range 3 {
		s = emphasisRe.ReplaceAllString(s, "$1$2$3")
	}
	s = replacer.Replace(s)
	s = spaceRe.ReplaceAllString(s, " ")
	s = spaceBeforePunctRe.ReplaceAllString(s, "$1")
	return strings.TrimSpace(s)
}

// Prepare turns a chapter into segments. The title is a segment of its own.
// A paragraph is one segment; a paragraph longer than maxChars is cut
// between sentences. A segment never spans two paragraphs, so editing a
// paragraph changes only its own segments.
func Prepare(ch chunk.Chapter, maxChars int) []Segment { return PrepareLang(ch, maxChars, "ru") }

// PrepareLang is Prepare for an audiobook in the language lang.
func PrepareLang(ch chunk.Chapter, maxChars int, lang string) []Segment {
	var segments []Segment
	if title := CleanLang(ch.Title, lang); title != "" {
		segments = append(segments, Segment{Text: title, EndsParagraph: true, Heading: true})
	}
	for _, p := range ch.Paragraphs {
		text := CleanLang(p, lang)
		if text == "" {
			continue
		}
		pieces := splitParagraph(text, maxChars)
		for i, piece := range pieces {
			segments = append(segments, Segment{Text: piece, EndsParagraph: i == len(pieces)-1})
		}
	}
	return segments
}

// splitParagraph packs the sentences of a paragraph into pieces of at most
// maxChars characters. A single longer sentence is kept whole.
func splitParagraph(text string, maxChars int) []string {
	if utf8.RuneCountInString(text) <= maxChars {
		return []string{text}
	}
	var pieces []string
	var cur []string
	curLen := 0
	for _, s := range chunk.Sentences(text) {
		n := utf8.RuneCountInString(s)
		if len(cur) > 0 && curLen+1+n > maxChars {
			pieces = append(pieces, strings.Join(cur, " "))
			cur, curLen = nil, 0
		}
		if len(cur) > 0 {
			curLen++
		}
		cur = append(cur, s)
		curLen += n
	}
	if len(cur) > 0 {
		pieces = append(pieces, strings.Join(cur, " "))
	}
	return pieces
}
