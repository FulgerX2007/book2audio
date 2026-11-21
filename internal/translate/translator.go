// Package translate defines the translation backend interface, the
// glossary and the prompt sent to the model.
package translate

import (
	"context"
	"fmt"

	"github.com/FulgerX2007/book2audio/internal/config"
)

// Term is a glossary entry: how a name or a term must be translated.
type Term struct {
	Source string `yaml:"source"`
	Target string `yaml:"target"`
}

// Request is one piece of text to translate.
type Request struct {
	Text       string
	SourceLang string // language code from the config, e.g. "en"
	TargetLang string // e.g. "ru"
	// Glossary holds only the terms that occur in Text.
	Glossary []Term
	// PrevContext is the end of the preceding text. It is shown to the
	// model for continuity and must not be translated.
	PrevContext string
}

// Translator translates one request. Implementations must be safe for
// concurrent use: the translate stage calls them from several workers.
type Translator interface {
	Translate(ctx context.Context, req Request) (string, error)
}

// New returns the translator selected by cfg.Backend.
func New(cfg config.Translate) (Translator, error) {
	switch cfg.Backend {
	case config.BackendOpenAI:
		return NewOpenAI(cfg)
	default:
		return nil, fmt.Errorf("translate.backend: unknown backend %q", cfg.Backend)
	}
}
