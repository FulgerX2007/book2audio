package chunk

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// paragraph returns a paragraph of about n characters made of short
// sentences that mention id.
func paragraph(id string, n int) string {
	var b strings.Builder
	for i := 1; b.Len() < n; i++ {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "Sentence %d of paragraph %s goes here.", i, id)
	}
	return b.String()
}

func chunkTexts(chunks []Chunk) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.Text
	}
	return out
}

func TestParseChapter(t *testing.T) {
	tests := []struct {
		name string
		text string
		want Chapter
	}{
		{
			name: "title and paragraphs",
			text: "# Chapter 1: The Start\n\nFirst paragraph.\n\nSecond paragraph.\n",
			want: Chapter{Title: "Chapter 1: The Start", Paragraphs: []string{"First paragraph.", "Second paragraph."}},
		},
		{
			name: "line breaks inside a paragraph, extra blank lines, CRLF",
			text: "# Title\r\n\r\n\r\nline one\r\nline two\r\n\r\n\r\n  next  \r\n",
			want: Chapter{Title: "Title", Paragraphs: []string{"line one line two", "next"}},
		},
		{
			name: "no title",
			text: "Just text.\n\nMore text.",
			want: Chapter{Paragraphs: []string{"Just text.", "More text."}},
		},
		{
			name: "a later line with # is a paragraph",
			text: "# Title\n\nText.\n\n# Not a title\n",
			want: Chapter{Title: "Title", Paragraphs: []string{"Text.", "# Not a title"}},
		},
		{name: "title only", text: "# Title\n", want: Chapter{Title: "Title"}},
		{name: "empty", text: "", want: Chapter{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseChapter(tt.text)
			if got.Title != tt.want.Title || !slices.Equal(got.Paragraphs, tt.want.Paragraphs) {
				t.Errorf("ParseChapter = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReadChapter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ch01.md")
	if err := os.WriteFile(path, []byte("# T\n\nP.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch, err := ReadChapter(path)
	if err != nil || ch.Title != "T" || !slices.Equal(ch.Paragraphs, []string{"P."}) {
		t.Errorf("ReadChapter = %+v, %v", ch, err)
	}
	if _, err := ReadChapter(filepath.Join(t.TempDir(), "absent.md")); err == nil {
		t.Error("ReadChapter of a missing file succeeded")
	}
}

func TestSplitPacksWholeParagraphs(t *testing.T) {
	ch := Chapter{Title: "Chapter 1", Paragraphs: []string{
		paragraph("A", 400), paragraph("B", 400), paragraph("C", 400), paragraph("D", 400), paragraph("E", 400),
	}}
	chunks := Split(ch, 1000)

	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3: %q", len(chunks), chunkTexts(chunks))
	}
	if !strings.HasPrefix(chunks[0].Text, "# Chapter 1\n\nSentence 1 of paragraph A") {
		t.Errorf("the first chunk does not start with the title: %q", chunks[0].Text)
	}
	for i, c := range chunks {
		if n := utf8.RuneCountInString(c.Text); n > 1000 {
			t.Errorf("chunk %d has %d characters, want at most 1000", i, n)
		}
		if c.Continues {
			t.Errorf("chunk %d is marked as continuing a paragraph", i)
		}
	}
	// Nothing is lost, duplicated or reordered.
	want := "# Chapter 1\n\n" + strings.Join(ch.Paragraphs, "\n\n")
	if got := strings.Join(chunkTexts(chunks), "\n\n"); got != want {
		t.Errorf("the chunks do not add up to the chapter")
	}
	if chunks[0].Context != "" {
		t.Errorf("the first chunk has a context: %q", chunks[0].Context)
	}
	for i := 1; i < len(chunks); i++ {
		ctx := chunks[i].Context
		if ctx == "" || !strings.HasSuffix(chunks[i-1].Text, ctx) || utf8.RuneCountInString(ctx) > contextChars {
			t.Errorf("context of chunk %d is not the end of chunk %d: %q", i, i-1, ctx)
		}
		if !strings.HasPrefix(ctx, "Sentence") && !strings.HasPrefix(ctx, "of") && strings.HasPrefix(ctx, "entence") {
			t.Errorf("context of chunk %d starts in the middle of a word: %q", i, ctx)
		}
	}
}

func TestSplitIsDeterministic(t *testing.T) {
	ch := Chapter{Title: "T", Paragraphs: []string{paragraph("A", 700), paragraph("B", 900), paragraph("C", 300)}}
	first := Split(ch, 1000)
	for range 5 {
		if !slices.Equal(Split(ch, 1000), first) {
			t.Fatal("Split gives different results for the same chapter")
		}
	}
}

// TestSplitInsertionKeepsEarlierChunks checks the property acceptance
// criterion 3 relies on: inserting a paragraph leaves the chunks above the
// insertion point untouched.
func TestSplitInsertionKeepsEarlierChunks(t *testing.T) {
	var paragraphs []string
	for i := range 12 {
		paragraphs = append(paragraphs, paragraph(string(rune('A'+i)), 350))
	}
	before := Split(Chapter{Title: "T", Paragraphs: paragraphs}, 1000)

	for at := range paragraphs {
		edited := slices.Insert(slices.Clone(paragraphs), at, paragraph("NEW", 350))
		after := Split(Chapter{Title: "T", Paragraphs: edited}, 1000)

		// Chunks that end before the paragraph at the insertion point are the same.
		same := 0
		for same < len(before) && same < len(after) && before[same] == after[same] {
			same++
		}
		wantSame := 0
		for _, c := range before {
			if strings.Contains(c.Text, "paragraph "+string(rune('A'+at))+" ") {
				break
			}
			wantSame++
		}
		// The chunk right before the insertion may absorb the new paragraph.
		if same < wantSame-1 {
			t.Errorf("insertion before paragraph %d changed chunk %d, although %d chunks end above it", at, same, wantSame)
		}
	}
}

func TestSplitLongParagraph(t *testing.T) {
	long := paragraph("L", 2500)
	ch := Chapter{Paragraphs: []string{"Short one.", long, "Short two."}}
	chunks := Split(ch, 1000)

	if len(chunks) < 4 {
		t.Fatalf("got %d chunks, want the long paragraph cut into several: %q", len(chunks), chunkTexts(chunks))
	}
	if chunks[0].Text != "Short one." || chunks[len(chunks)-1].Text != "Short two." {
		t.Errorf("the short paragraphs were merged with the long one: %q", chunkTexts(chunks))
	}
	var pieces []string
	for i, c := range chunks[1 : len(chunks)-1] {
		if n := utf8.RuneCountInString(c.Text); n > 1000 {
			t.Errorf("piece %d has %d characters", i, n)
		}
		if !strings.HasSuffix(c.Text, ".") || !strings.HasPrefix(c.Text, "Sentence") {
			t.Errorf("piece %d is not made of whole sentences: %q", i, c.Text)
		}
		if c.Continues != (i > 0) {
			t.Errorf("piece %d: Continues = %v", i, c.Continues)
		}
		pieces = append(pieces, c.Text)
	}
	if strings.Join(pieces, " ") != long {
		t.Error("the pieces do not add up to the long paragraph")
	}
	if chunks[len(chunks)-1].Continues {
		t.Error("the paragraph after the long one is marked as its continuation")
	}
}

func TestSplitEdgeCases(t *testing.T) {
	t.Run("empty chapter", func(t *testing.T) {
		if chunks := Split(Chapter{}, 1000); len(chunks) != 0 {
			t.Errorf("chunks = %q, want none", chunkTexts(chunks))
		}
	})
	t.Run("title only", func(t *testing.T) {
		if got := chunkTexts(Split(Chapter{Title: "Only"}, 1000)); !slices.Equal(got, []string{"# Only"}) {
			t.Errorf("chunks = %q", got)
		}
	})
	t.Run("single paragraph", func(t *testing.T) {
		if got := chunkTexts(Split(Chapter{Paragraphs: []string{"One."}}, 1000)); !slices.Equal(got, []string{"One."}) {
			t.Errorf("chunks = %q", got)
		}
	})
	t.Run("one sentence longer than the limit stays whole", func(t *testing.T) {
		huge := strings.Repeat("word ", 400) + "end."
		chunks := Split(Chapter{Paragraphs: []string{huge}}, 500)
		if len(chunks) != 1 || chunks[0].Text != huge {
			t.Errorf("got %d chunks", len(chunks))
		}
	})
	t.Run("paragraphs exactly at the limit", func(t *testing.T) {
		a, b := strings.Repeat("a", 499), strings.Repeat("b", 499)
		if got := chunkTexts(Split(Chapter{Paragraphs: []string{a, b}}, 1000)); len(got) != 1 {
			t.Errorf("499+2+499 characters fit into 1000, got %d chunks", len(got))
		}
		if got := chunkTexts(Split(Chapter{Paragraphs: []string{a + "a", b}}, 1000)); len(got) != 2 {
			t.Errorf("500+2+499 characters do not fit into 1000, got %d chunks", len(got))
		}
	})
}

func TestSentences(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"simple", "One. Two! Three? Four…", []string{"One.", "Two!", "Three?", "Four…"}},
		{"abbreviations", "Dr. Brown met Mr. Green at St. Mary's. They talked.", []string{"Dr. Brown met Mr. Green at St. Mary's.", "They talked."}},
		{"initials", "J. R. Smith wrote it. E. Jones read it.", []string{"J. R. Smith wrote it.", "E. Jones read it."}},
		{"latin abbreviations", "Tools, e.g. Hammers, help. Others, i.e. Saws, cut.", []string{"Tools, e.g. Hammers, help.", "Others, i.e. Saws, cut."}},
		{"quotes", `"Stop!" he said. "Why?" she asked.`, []string{`"Stop!" he said.`, `"Why?" she asked.`}},
		{"closing quote after the mark", "She said, “Go.” He went.", []string{"She said, “Go.”", "He went."}},
		{"ellipsis in the middle", "Well... maybe. Yes.", []string{"Well... maybe.", "Yes."}},
		{"decimal numbers and versions", "It costs 3.50 euros. Version 1.2 is out.", []string{"It costs 3.50 euros.", "Version 1.2 is out."}},
		{"lower case after a full stop", "See p. five. Then stop.", []string{"See p. five.", "Then stop."}},
		{"russian", "Это было в 1990 г. Москве не спалось. И т.д. Потом всё стихло.", []string{"Это было в 1990 г. Москве не спалось.", "И т.д. Потом всё стихло."}},
		{"russian plain", "Первое предложение. Второе предложение! Третье?", []string{"Первое предложение.", "Второе предложение!", "Третье?"}},
		{"number starts a sentence", "He left. 1990 was hard.", []string{"He left.", "1990 was hard."}},
		{"no end mark", "no end mark here", []string{"no end mark here"}},
		{"extra spaces", "  One.   Two.  ", []string{"One.", "Two."}},
		{"empty", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Sentences(tt.in); !slices.Equal(got, tt.want) {
				t.Errorf("Sentences(%q) =\n%q\nwant\n%q", tt.in, got, tt.want)
			}
		})
	}
}

func TestJoin(t *testing.T) {
	chunks := []Chunk{{Text: "# T\n\nA."}, {Text: "B1."}, {Text: "B2.", Continues: true}, {Text: "C."}}
	got := Join(chunks, []string{"# Т\n\nА.\n", " Б1. ", "Б2.", "В."})
	if want := "# Т\n\nА.\n\nБ1. Б2.\n\nВ.\n"; got != want {
		t.Errorf("Join = %q, want %q", got, want)
	}

	// Splitting a chapter and joining the untranslated chunks restores it.
	ch := Chapter{Title: "Title", Paragraphs: []string{paragraph("A", 300), paragraph("L", 2500), paragraph("B", 300)}}
	parts := Split(ch, 1000)
	back := ParseChapter(Join(parts, chunkTexts(parts)))
	if back.Title != ch.Title || !slices.Equal(back.Paragraphs, ch.Paragraphs) {
		t.Error("Split followed by Join does not restore the chapter")
	}
}
