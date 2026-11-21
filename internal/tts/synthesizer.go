// Package tts defines the speech synthesis backend interface and its
// implementations.
package tts

import (
	"context"
	"fmt"

	"github.com/FulgerX2007/book2audio/internal/config"
	"github.com/FulgerX2007/book2audio/internal/execx"
)

// Synthesizer turns text into speech.
//
// Synthesize writes a PCM WAV file with the speech for text to outPath. The
// file must appear atomically: outPath either does not exist or holds the
// complete result, because the synth stage takes an existing file for a
// finished segment. Implementations must be safe for concurrent use.
type Synthesizer interface {
	Synthesize(ctx context.Context, text, outPath string) error
}

// Item is one segment of a batch.
type Item struct {
	Text    string
	OutPath string
}

// BatchSynthesizer is implemented by engines that are expensive to start
// and can work through many segments in one start. The synth stage prefers
// it over Synthesize.
//
// SynthesizeBatch returns one error per item (nil for a segment that was
// synthesised), or a single error when the engine could not run at all.
// Like Synthesize, it must make every output file appear atomically.
type BatchSynthesizer interface {
	Synthesizer
	SynthesizeBatch(ctx context.Context, items []Item) ([]error, error)
}

// New returns the synthesizer selected by cfg.Backend. External engines are
// run through runner.
func New(cfg config.TTS, runner execx.Runner) (Synthesizer, error) {
	switch cfg.Backend {
	case config.BackendPiper:
		return NewPiper(cfg, runner)
	case config.BackendSilero:
		return NewSilero(cfg, runner)
	default:
		return nil, fmt.Errorf("tts.backend: неизвестный бэкенд %q", cfg.Backend)
	}
}
