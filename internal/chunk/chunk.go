// Package chunk reads chapter files and cuts a chapter into the pieces that
// are sent to the translator.
package chunk

import (
	"os"
	"strings"
	"unicode/utf8"
)

// headingPrefix marks the title line of a chapter file and of the first
// chunk of a chapter.
const headingPrefix = "# "

// contextChars is how much of the previous chunk is passed to the
// translator as context.
const contextChars = 300

// Chapter is the content of a chapter file.
type Chapter struct {
	Title      string
	Paragraphs []string
}

// Chunk is a piece of a chapter that is translated in one request.
type Chunk struct {
	// Text holds whole paragraphs separated by blank lines. The first
	// chunk of a chapter starts with the "# Title" line.
	Text string
	// Context is the end of the previous chunk of the chapter. It helps
	// the translator and is not translated itself.
	Context string
	// Continues is set when the chunk goes on with the paragraph the
	// previous chunk ended in: a paragraph longer than the limit is cut
	// between sentences.
	Continues bool
}

// ParseChapter parses the text of a chapter file: an optional "# Title"
// line and paragraphs separated by blank lines. Line breaks inside a
// paragraph are treated as spaces.
func ParseChapter(text string) Chapter {
	var ch Chapter
	text = strings.ReplaceAll(text, "\r\n", "\n")
	for i, block := range splitBlocks(text) {
		if title, ok := strings.CutPrefix(block, headingPrefix); ok && i == 0 {
			ch.Title = strings.TrimSpace(title)
			continue
		}
		ch.Paragraphs = append(ch.Paragraphs, block)
	}
	return ch
}

// ReadChapter reads and parses a chapter file.
func ReadChapter(path string) (Chapter, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Chapter{}, err
	}
	return ParseChapter(string(data)), nil
}

// splitBlocks returns the blocks of text separated by blank lines, each
// with its lines joined by single spaces.
func splitBlocks(text string) []string {
	var blocks []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, strings.Join(cur, " "))
			cur = nil
		}
	}
	for line := range strings.SplitSeq(text, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return blocks
}

// Split cuts the chapter into chunks of at most maxChars characters. A
// chunk holds whole paragraphs; only a paragraph longer than the limit is
// cut, between sentences. The result depends on nothing but the chapter
// text, and a change in the chapter never alters the chunks that end
// before the changed paragraph.
func Split(ch Chapter, maxChars int) []Chunk {
	var chunks []Chunk
	var cur []string
	curLen := 0
	emit := func(text string, continues bool) {
		c := Chunk{Text: text, Continues: continues}
		if n := len(chunks); n > 0 {
			c.Context = tail(chunks[n-1].Text, contextChars)
		}
		chunks = append(chunks, c)
	}
	flush := func() {
		if len(cur) > 0 {
			emit(strings.Join(cur, "\n\n"), false)
			cur, curLen = nil, 0
		}
	}

	paragraphs := ch.Paragraphs
	if ch.Title != "" {
		paragraphs = append([]string{headingPrefix + ch.Title}, paragraphs...)
	}
	for _, p := range paragraphs {
		n := utf8.RuneCountInString(p)
		if n > maxChars {
			flush()
			for i, piece := range splitLong(p, maxChars) {
				emit(piece, i > 0)
			}
			continue
		}
		if len(cur) > 0 && curLen+2+n > maxChars {
			flush()
		}
		if len(cur) > 0 {
			curLen += 2
		}
		cur = append(cur, p)
		curLen += n
	}
	flush()
	return chunks
}

// splitLong cuts a paragraph that exceeds maxChars into pieces of whole
// sentences. A single sentence longer than the limit is kept in one piece.
func splitLong(p string, maxChars int) []string {
	var pieces []string
	var cur []string
	curLen := 0
	for _, s := range Sentences(p) {
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

// tail returns about the last n characters of s, starting at a word.
func tail(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	cut := string(runes[len(runes)-n:])
	if i := strings.IndexAny(cut, " \n"); i >= 0 {
		cut = cut[i+1:]
	}
	return strings.TrimSpace(cut)
}

// Join assembles translated chunks back into the text of a chapter file.
// texts[i] is the translation of chunks[i].
func Join(chunks []Chunk, texts []string) string {
	var b strings.Builder
	for i, text := range texts {
		text = strings.TrimSpace(text)
		switch {
		case i == 0:
		case chunks[i].Continues:
			b.WriteByte(' ')
		default:
			b.WriteString("\n\n")
		}
		b.WriteString(text)
	}
	b.WriteByte('\n')
	return b.String()
}
