package translate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/FulgerX2007/book2audio/internal/config"
)

func TestParseGlossary(t *testing.T) {
	const doc = `
# имена
terms:
  - source: Mr. Darcy
    target: мистер Дарси
  - source: " Pemberley "
    target: Пемберли
`
	g, err := ParseGlossary(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("ParseGlossary: %v", err)
	}
	want := []Term{{"Mr. Darcy", "мистер Дарси"}, {"Pemberley", "Пемберли"}}
	if !slices.Equal(g.Terms, want) {
		t.Errorf("terms = %+v, want %+v", g.Terms, want)
	}
}

func TestParseGlossaryEmpty(t *testing.T) {
	for name, doc := range map[string]string{
		"empty file":    "",
		"comments only": "# пусто\n",
		"empty list":    "terms: []\n",
	} {
		g, err := ParseGlossary(strings.NewReader(doc))
		if err != nil || len(g.Terms) != 0 {
			t.Errorf("%s: terms = %+v, err = %v; want no terms", name, g.Terms, err)
		}
	}
}

func TestParseGlossaryErrors(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want string
	}{
		{"malformed YAML", "terms: [", "parsing YAML"},
		{"unknown key", "term:\n  - source: a\n", "parsing YAML"},
		{"unknown field of a term", "terms:\n  - source: a\n    target: b\n    note: c\n", "parsing YAML"},
		{"missing target", "terms:\n  - source: a\n", "both source and target"},
		{"missing source", "terms:\n  - target: b\n", "both source and target"},
		{"duplicate", "terms:\n  - {source: Darcy, target: Дарси}\n  - {source: DARCY, target: Дарси}\n", "twice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseGlossary(strings.NewReader(tt.doc))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadGlossary(t *testing.T) {
	dir := t.TempDir()

	g, err := LoadGlossary(filepath.Join(dir, "absent.yaml"))
	if err != nil || len(g.Terms) != 0 {
		t.Errorf("missing file: terms = %+v, err = %v; want an empty glossary", g.Terms, err)
	}

	good := filepath.Join(dir, "glossary.yaml")
	if err := os.WriteFile(good, []byte("terms:\n  - {source: Darcy, target: Дарси}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if g, err := LoadGlossary(good); err != nil || len(g.Terms) != 1 {
		t.Errorf("LoadGlossary = %+v, %v", g, err)
	}

	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("terms: ["), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGlossary(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Errorf("error = %v, want one naming the file", err)
	}
	if _, err := LoadGlossary(dir); err == nil {
		t.Error("LoadGlossary of a directory succeeded")
	}
}

func TestGlossaryMatch(t *testing.T) {
	g := Glossary{Terms: []Term{{"Mr. Darcy", "мистер Дарси"}, {"Pemberley", "Пемберли"}, {"fog bell", "туманный колокол"}}}
	tests := []struct {
		text string
		want []string
	}{
		{"He met Mr. Darcy at Pemberley.", []string{"Mr. Darcy", "Pemberley"}},
		{"PEMBERLEY in winter", []string{"Pemberley"}},
		{"Fog bell rang twice.", []string{"fog bell"}},
		{"Nothing relevant here.", nil},
		{"", nil},
	}
	for _, tt := range tests {
		var got []string
		for _, term := range g.Match(tt.text) {
			got = append(got, term.Source)
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("Match(%q) = %q, want %q", tt.text, got, tt.want)
		}
	}
	if got := (Glossary{}).Match("anything"); got != nil {
		t.Errorf("empty glossary matched %+v", got)
	}
}

func TestNew(t *testing.T) {
	cfg := config.Default().Translate
	cfg.Model = "local-model"
	tr, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := tr.(*OpenAI); !ok {
		t.Errorf("New returned %T, want *OpenAI", tr)
	}

	cfg.Backend = "deepl"
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "deepl") {
		t.Errorf("unknown backend: error = %v", err)
	}

	cfg = config.Default().Translate
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "translate.model") {
		t.Errorf("without a model: error = %v", err)
	}
}

func TestFakeTranslator(t *testing.T) {
	fake := &FakeTranslator{}
	got, err := fake.Translate(context.Background(), Request{Text: "# Title\n\nFirst.\n\nSecond."})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if want := "# [ru] Title\n\n[ru] First.\n\n[ru] Second."; got != want {
		t.Errorf("Translate = %q, want %q", got, want)
	}
	if fake.Calls() != 1 || fake.Requests()[0].Text != "# Title\n\nFirst.\n\nSecond." {
		t.Errorf("recorded requests = %+v", fake.Requests())
	}

	boom := errors.New("сбой")
	fake.Fail = func(req Request) error {
		if strings.Contains(req.Text, "bad") {
			return boom
		}
		return nil
	}
	if _, err := fake.Translate(context.Background(), Request{Text: "bad one"}); !errors.Is(err, boom) {
		t.Errorf("error = %v, want %v", err, boom)
	}
	if _, err := fake.Translate(context.Background(), Request{Text: "good one"}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	fake.Reply = func(Request) string { return "готово" }
	if got, _ := fake.Translate(context.Background(), Request{Text: "x"}); got != "готово" {
		t.Errorf("Reply was not used: %q", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fake.Translate(ctx, Request{Text: "x"}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled context: error = %v", err)
	}
	if fake.Calls() != 4 {
		t.Errorf("Calls = %d, want 4 (the cancelled call is not recorded)", fake.Calls())
	}
}
