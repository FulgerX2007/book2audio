package translate

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Glossary is the hand-edited list of terms of a book (glossary.yaml).
type Glossary struct {
	Terms []Term `yaml:"terms"`
}

// LoadGlossary reads glossary.yaml. A missing file is an empty glossary.
func LoadGlossary(path string) (Glossary, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Glossary{}, nil
	}
	if err != nil {
		return Glossary{}, fmt.Errorf("glossary: %w", err)
	}
	defer f.Close()

	g, err := ParseGlossary(f)
	if err != nil {
		return Glossary{}, fmt.Errorf("glossary %s: %w", path, err)
	}
	return g, nil
}

// ParseGlossary reads a glossary in YAML:
//
//	terms:
//	  - source: Mr. Darcy
//	    target: мистер Дарси
//
// A term listed twice (compared without regard to case) is an error: it
// would be unclear which translation applies.
func ParseGlossary(r io.Reader) (Glossary, error) {
	var g Glossary
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&g); err != nil && !errors.Is(err, io.EOF) {
		return Glossary{}, fmt.Errorf("parsing YAML: %w", err)
	}

	seen := make(map[string]bool)
	for i := range g.Terms {
		t := &g.Terms[i]
		t.Source, t.Target = strings.TrimSpace(t.Source), strings.TrimSpace(t.Target)
		if t.Source == "" || t.Target == "" {
			return Glossary{}, fmt.Errorf("term %d: both source and target are required", i+1)
		}
		key := strings.ToLower(t.Source)
		if seen[key] {
			return Glossary{}, fmt.Errorf("term %q is listed twice", t.Source)
		}
		seen[key] = true
	}
	return g, nil
}

// Match returns the terms whose source occurs in text, in glossary order.
// The comparison ignores case, so a term also matches at the start of a
// sentence and in a heading.
func (g Glossary) Match(text string) []Term {
	lower := strings.ToLower(text)
	var out []Term
	for _, t := range g.Terms {
		if strings.Contains(lower, strings.ToLower(t.Source)) {
			out = append(out, t)
		}
	}
	return out
}
