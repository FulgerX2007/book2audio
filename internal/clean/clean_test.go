package clean

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// texts returns the Text of every line.
func texts(lines []Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Text
	}
	return out
}

// page builds the lines of one page from text, as Pages would number them.
func page(n int, text string) []Line { return splitLines(n, text) }

func TestPagesRemovesHeadersFootersAndPageNumbers(t *testing.T) {
	pages := []string{
		"The Guide\n\nFirst page text.\nMore text.\n\n1\n",
		"The Guide\n\nSecond page text.\n\n2\n",
		"The Guide\n\nThird page text.\n\n3\n",
		"The Guide\n\nFourth page text.\n\n4\n",
	}
	got := Pages(pages)

	want := [][]string{
		{"First page text.", "More text."},
		{"Second page text."},
		{"Third page text."},
		{"Fourth page text."},
	}
	for i := range pages {
		if !slices.Equal(texts(got[i]), want[i]) {
			t.Errorf("page %d = %q, want %q", i+1, texts(got[i]), want[i])
		}
	}
	// Line numbers still refer to the raw page text.
	if l := got[0][0]; l.Page != 1 || l.Num != 3 {
		t.Errorf("first kept line = %+v, want page 1, line 3", l)
	}
	if l := got[0][1]; l.Num != 4 {
		t.Errorf("second kept line = %+v, want line 4", l)
	}
}

func TestPagesHeaderVariants(t *testing.T) {
	tests := []struct {
		name  string
		pages []string
		want  [][]string
	}{
		{
			name: "page number inside the header, left and right pages differ",
			pages: []string{
				"12   The Guide\nText a.", "The Guide   13\nText b.", "14   The Guide\nText c.",
				"The Guide   15\nText d.", "16   The Guide\nText e.", "The Guide   17\nText f.",
			},
			want: [][]string{{"Text a."}, {"Text b."}, {"Text c."}, {"Text d."}, {"Text e."}, {"Text f."}},
		},
		{
			name: "footer with the book title",
			pages: []string{
				"Text a.\n\nThe Guide", "Text b.\n\nThe Guide", "Text c.\n\nThe Guide",
			},
			want: [][]string{{"Text a."}, {"Text b."}, {"Text c."}},
		},
		{
			name: "a line repeated on two pages only is text",
			pages: []string{
				"Refrain\nText a.", "Refrain\nText b.", "Other\nText c.", "Another\nText d.",
			},
			want: [][]string{{"Refrain", "Text a."}, {"Refrain", "Text b."}, {"Other", "Text c."}, {"Another", "Text d."}},
		},
		{
			name: "page number above the header",
			pages: []string{
				"7\nThe Guide\nText a.", "8\nThe Guide\nText b.", "9\nThe Guide\nText c.",
			},
			want: [][]string{{"Text a."}, {"Text b."}, {"Text c."}},
		},
		{
			name: "blank lines inside a page are kept, at the edges dropped",
			pages: []string{
				"\n\nBlock one.\n\nBlock two.\n\n\n",
			},
			want: [][]string{{"Block one.", "", "Block two."}},
		},
		{
			name:  "empty and blank pages",
			pages: []string{"", "  \n \n", "Text."},
			want:  [][]string{nil, nil, {"Text."}},
		},
		{
			name:  "a page that is only a page number",
			pages: []string{"\n17\n"},
			want:  [][]string{nil},
		},
		{
			name:  "a page that is only a heading keeps it",
			pages: []string{"Part Two\n"},
			want:  [][]string{{"Part Two"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Pages(tt.pages)
			for i := range tt.pages {
				if !slices.Equal(texts(got[i]), tt.want[i]) {
					t.Errorf("page %d = %q, want %q", i+1, texts(got[i]), tt.want[i])
				}
			}
		})
	}
}

func TestIsPageNumber(t *testing.T) {
	for s, want := range map[string]bool{
		"1": true, "123": true, "- 12 -": true, "— 7 —": true, "Page 4": true, "стр. 15": true, "с. 3": true,
		"xii": true, "iv": true,
		"": false, "I": false, "i": false, "12345": false, "1984 was a year": false, "Chapter 1": false,
		"XII": false, "mild": false, "3.": false,
	} {
		if got := isPageNumber(s); got != want {
			t.Errorf("isPageNumber(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestParagraphs(t *testing.T) {
	full := "This line is as wide as the lines of this page usually are, yes" // a full-width line
	tests := []struct {
		name  string
		lines []Line
		want  []string
	}{
		{
			name:  "blank line separates paragraphs",
			lines: page(1, "First paragraph\ncontinues here.\n\nSecond paragraph."),
			want:  []string{"First paragraph continues here.", "Second paragraph."},
		},
		{
			name:  "short line that ends a sentence closes the paragraph",
			lines: page(1, full+"\n"+full+"\nand it ends.\n"+full+"\n"+full+"\nthe second one ends too."),
			want: []string{
				full + " " + full + " and it ends.",
				full + " " + full + " the second one ends too.",
			},
		},
		{
			name:  "full-width line that ends a sentence does not close it",
			lines: page(1, full+"\n"+full+"\nThis line is full width and it happens to end with a full stop.\n"+full+"\nend."),
			want: []string{
				full + " " + full + " This line is full width and it happens to end with a full stop. " + full + " end.",
			},
		},
		{
			name:  "short line without sentence end continues",
			lines: page(1, full+"\nand then\n"+full+"\nend."),
			want:  []string{full + " and then " + full + " end."},
		},
		{
			name:  "closing quote after the full stop",
			lines: page(1, full+"\n"+full+"\nshe said.”\n"+full+"\nend."),
			want:  []string{full + " " + full + " she said.”", full + " end."},
		},
		{
			name:  "paragraph continues over a page break",
			lines: append(page(1, full+"\nthe sentence goes on to the"), page(2, "next page and ends there.\n\nNew paragraph.")...),
			want:  []string{full + " the sentence goes on to the next page and ends there.", "New paragraph."},
		},
		{
			name:  "page break after a finished sentence starts a paragraph",
			lines: append(page(1, full+"\n"+full+"\nThe page ends exactly with the end of a full-width sentence here."), page(2, full+"\nend.")...),
			want: []string{
				full + " " + full + " The page ends exactly with the end of a full-width sentence here.",
				full + " end.",
			},
		},
		{
			name:  "single line",
			lines: page(1, "Only one line."),
			want:  []string{"Only one line."},
		},
		{
			name:  "standalone short lines stay separate",
			lines: page(1, "* * *\n\nA Subheading\n\nText follows here."),
			want:  []string{"* * *", "A Subheading", "Text follows here."},
		},
		{
			name:  "drop cap is joined to its word",
			lines: page(1, "A Heading\n\nO\n\nnce the lamp was lit, the keeper waited.\n\nNext paragraph."),
			want:  []string{"A Heading", "Once the lamp was lit, the keeper waited.", "Next paragraph."},
		},
		{
			name:  "drop cap after an opening quote",
			lines: page(1, "“W\n\nhat a night,” he said."),
			want:  []string{"“What a night,” he said."},
		},
		{
			name:  "cyrillic drop cap",
			lines: page(1, "О\n\nднажды вечером он поднялся наверх."),
			want:  []string{"Однажды вечером он поднялся наверх."},
		},
		{
			name:  "a single letter before a capital is not a drop cap",
			lines: page(1, "A\n\nThe first answer.\n\nB\n\nThe second answer."),
			want:  []string{"A", "The first answer.", "B", "The second answer."},
		},
		{
			name:  "a single letter at the end stays",
			lines: page(1, "Some text.\n\nI"),
			want:  []string{"Some text.", "I"},
		},
		{name: "no lines", lines: nil, want: nil},
		{name: "blank lines only", lines: page(1, "\n\n"), want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Paragraphs(tt.lines); !slices.Equal(got, tt.want) {
				t.Errorf("Paragraphs =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestJoinLines(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{"plain", []string{"one", "two"}, "one two"},
		{"word split by a hyphen", []string{"an exam-", "ple of it"}, "an example of it"},
		{"word split by a soft hyphen", []string{"an exam­", "ple of it"}, "an example of it"},
		{"compound before a capital keeps the hyphen", []string{"the Anglo-", "Saxon words"}, "the Anglo-Saxon words"},
		{"hyphen before a digit is kept", []string{"model T-", "34 tank"}, "model T-34 tank"},
		{"dash at the end of a line is not a split word", []string{"wait —", "then go"}, "wait — then go"},
		{"spaced hyphen is not a split word", []string{"wait -", "then go"}, "wait - then go"},
		{"cyrillic word", []string{"пере-", "нос слова"}, "перенос слова"},
		{"three lines", []string{"a hy-", "phen-", "ated word"}, "a hyphenated word"},
		{"single line", []string{"alone"}, "alone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := joinLines(tt.lines); got != tt.want {
				t.Errorf("joinLines(%q) = %q, want %q", tt.lines, got, tt.want)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"  two   spaces\tand nbsp ", "two spaces and nbsp"},
		{"ﬁrst ﬂoor oﬃce", "first floor office"},
		{"co­operate", "cooperate"},
		{"``quoted''", "“quoted”"},
		{"wait -- what", "wait — what"},
		{"wait - what", "wait — what"},
		{"well-known", "well-known"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Normalize(tt.in); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestEndsSentence(t *testing.T) {
	for s, want := range map[string]bool{
		"It ends.": true, "Does it?": true, "Yes!": true, "And so…": true, "“It ends.”": true, "(It ends.)": true,
		"no end": false, "a comma,": false, "a colon:": false, "": false, "”": false,
	} {
		if got := endsSentence(s); got != want {
			t.Errorf("endsSentence(%q) = %v, want %v", s, got, want)
		}
	}
}

// TestGoldenSamplePages cleans the raw pages of the sample PDF (as printed
// by pdftotext) and compares the paragraphs with the golden file.
func TestGoldenSamplePages(t *testing.T) {
	var pages []string
	for n := 1; n <= 4; n++ {
		data, err := os.ReadFile(filepath.Join("testdata", fmt.Sprintf("page%d.txt", n)))
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, string(data))
	}

	var all []Line
	for _, lines := range Pages(pages) {
		for _, l := range lines {
			if strings.Contains(l.Text, "A Short Field Guide") {
				t.Errorf("the running header survived on page %d", l.Page)
			}
		}
		all = append(all, lines...)
	}
	got := strings.Join(Paragraphs(all), "\n\n") + "\n"

	golden := filepath.Join("testdata", "paragraphs.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("paragraphs differ from %s (run with -update to rewrite):\n%s", golden, got)
	}
}
