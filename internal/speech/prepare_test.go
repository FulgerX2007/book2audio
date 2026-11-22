package speech

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/FulgerX2007/book2audio/internal/chunk"
)

func TestClean(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text is kept", "Лампа зажжена, и смотритель ждёт.", "Лампа зажжена, и смотритель ждёт."},
		{"bold and italic", "Это **важно**, а это _нет_ и *тоже*.", "Это важно, а это нет и тоже."},
		{"nested emphasis", "Очень **_важное_** слово.", "Очень важное слово."},
		{"inline code", "Команда `go test` проходит.", "Команда go test проходит."},
		{"heading mark", "# Глава 1", "Глава 1"},
		{"quote mark", "> Цитата из журнала.", "Цитата из журнала."},
		{"numbered footnote", "Маяк построен в 1890 году[12].", "Маяк построен в 1890 году."},
		{"star footnote and superscript", "Слово[*] и ещё слово².", "Слово и ещё слово."},
		{"footnote before a comma", "Колокол [3], как известно, тяжёл.", "Колокол, как известно, тяжёл."},
		{"http address", "Подробнее: https://example.org/path?x=1 и далее.", "Подробнее: ссылка и далее."},
		{"www address", "Сайт www.example.org работает.", "Сайт ссылка работает."},
		{"typographic quotes", "Он сказал: «Пора», — и ушёл. „Да“, “нет”.", `Он сказал: "Пора", — и ушёл. "Да", "нет".`},
		{"ellipsis and en dash", "Ну… может быть – да.", "Ну... может быть — да."},
		{"square brackets", "Текст [пояснение редактора] дальше.", "Текст (пояснение редактора) дальше."},
		{"line breaks and spaces", "первая строка\nвторая  строка и\tтретья", "первая строка вторая строка и третья"},
		{"soft hyphen", "сло­во", "слово"},
		{"snake_case is not emphasis", "файл my_file_name.txt", "файл my_file_name.txt"},
		{"multiplication sign is kept", "2 * 3 = 6", "2 * 3 = 6"},
		{"empty", "", ""},
		{"only marks", "  **  ", "**"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Clean(tt.in); got != tt.want {
				t.Errorf("Clean(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// sentences returns n sentences that mention id.
func sentences(id string, n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("Это предложение номер %d абзаца %s.", i+1, id)
	}
	return strings.Join(parts, " ")
}

func texts(segments []Segment) []string {
	out := make([]string, len(segments))
	for i, s := range segments {
		out[i] = s.Text
	}
	return out
}

func TestPrepare(t *testing.T) {
	ch := chunk.Chapter{
		Title:      "Глава 1: **Начало**",
		Paragraphs: []string{"Первый абзац.", sentences("бета", 10), "Третий\nабзац."},
	}
	got := Prepare(ch, 120)

	if got[0] != (Segment{Text: "Глава 1: Начало", EndsParagraph: true, Heading: true}) {
		t.Errorf("heading segment = %+v", got[0])
	}
	if got[1] != (Segment{Text: "Первый абзац.", EndsParagraph: true}) {
		t.Errorf("first paragraph = %+v", got[1])
	}
	last := got[len(got)-1]
	if last != (Segment{Text: "Третий абзац.", EndsParagraph: true}) {
		t.Errorf("last paragraph = %+v, want the line break turned into a space", last)
	}

	// The long paragraph is cut into several segments of whole sentences.
	middle := got[2 : len(got)-1]
	if len(middle) < 3 {
		t.Fatalf("the long paragraph gave %d segments, want several", len(middle))
	}
	for i, s := range middle {
		if n := utf8.RuneCountInString(s.Text); n > 120 {
			t.Errorf("segment %d has %d characters, want at most 120", i, n)
		}
		if !strings.HasPrefix(s.Text, "Это предложение") || !strings.HasSuffix(s.Text, ".") {
			t.Errorf("segment %d is not made of whole sentences: %q", i, s.Text)
		}
		if s.Heading || s.EndsParagraph != (i == len(middle)-1) {
			t.Errorf("segment %d flags = %+v", i, s)
		}
		if strings.Contains(s.Text, "\n") {
			t.Errorf("segment %d contains a line break", i)
		}
	}
	if strings.Join(texts(middle), " ") != sentences("бета", 10) {
		t.Error("the segments do not add up to the paragraph")
	}
}

func TestPrepareEdgeCases(t *testing.T) {
	t.Run("empty chapter", func(t *testing.T) {
		if got := Prepare(chunk.Chapter{}, 100); len(got) != 0 {
			t.Errorf("segments = %+v, want none", got)
		}
	})
	t.Run("no title", func(t *testing.T) {
		got := Prepare(chunk.Chapter{Paragraphs: []string{"Текст."}}, 100)
		if len(got) != 1 || got[0].Heading {
			t.Errorf("segments = %+v", got)
		}
	})
	t.Run("paragraph that is empty after cleaning is dropped", func(t *testing.T) {
		got := Prepare(chunk.Chapter{Paragraphs: []string{"[1]", "Текст."}}, 100)
		if !slices.Equal(texts(got), []string{"Текст."}) {
			t.Errorf("segments = %q", texts(got))
		}
	})
	t.Run("one sentence longer than the limit stays whole", func(t *testing.T) {
		long := strings.Repeat("слово ", 100) + "конец."
		got := Prepare(chunk.Chapter{Paragraphs: []string{long}}, 50)
		if len(got) != 1 || got[0].Text != long || !got[0].EndsParagraph {
			t.Errorf("got %d segments", len(got))
		}
	})
	t.Run("paragraph exactly at the limit is one segment", func(t *testing.T) {
		p := strings.Repeat("а", 99) + "."
		if got := Prepare(chunk.Chapter{Paragraphs: []string{p}}, 100); len(got) != 1 {
			t.Errorf("got %d segments, want 1", len(got))
		}
	})
}

// TestPrepareEditChangesOnlyItsParagraph checks the property behind
// acceptance criterion 3: editing one paragraph leaves the segments of the
// other paragraphs as they were.
func TestPrepareEditChangesOnlyItsParagraph(t *testing.T) {
	paragraphs := []string{sentences("альфа", 6), sentences("бета", 6), sentences("гамма", 6), sentences("дельта", 6)}
	before := texts(Prepare(chunk.Chapter{Title: "Глава", Paragraphs: paragraphs}, 120))

	edited := slices.Clone(paragraphs)
	edited[1] = "Совсем другой текст. " + sentences("бета", 7)
	after := texts(Prepare(chunk.Chapter{Title: "Глава", Paragraphs: edited}, 120))

	for _, text := range before {
		if strings.Contains(text, "абзаца бета") {
			continue
		}
		if !slices.Contains(after, text) {
			t.Errorf("a segment of an untouched paragraph changed: %q", text)
		}
	}
	changed := 0
	for _, text := range after {
		if !slices.Contains(before, text) {
			changed++
			if !strings.Contains(text, "абзаца бета") && !strings.Contains(text, "Совсем другой") {
				t.Errorf("a new segment outside the edited paragraph: %q", text)
			}
		}
	}
	if changed == 0 {
		t.Error("the edit changed no segment")
	}
}

func TestURLWordFollowsTheLanguage(t *testing.T) {
	tests := []struct{ lang, want string }{
		{"ru", "see ссылка now"},
		{"", "see ссылка now"},
		{"en", "see link now"},
		{"de", "see link now"},
	}
	for _, tt := range tests {
		if got := CleanLang("see https://example.org/page now", tt.lang); got != tt.want {
			t.Errorf("CleanLang(lang %q) = %q, want %q", tt.lang, got, tt.want)
		}
	}
	ch := chunk.Chapter{Title: "One", Paragraphs: []string{"Read https://example.org today."}}
	segs := PrepareLang(ch, 500, "en")
	if len(segs) != 2 || segs[1].Text != "Read link today." {
		t.Errorf("PrepareLang = %+v", segs)
	}
}
