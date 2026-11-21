package tts

import (
	"context"
	"sync"
	"unicode/utf8"

	"github.com/FulgerX2007/book2audio/internal/audio"
)

// FakeFormat is the audio format written by FakeSynthesizer: the format of
// the medium-quality Piper voices.
var FakeFormat = audio.Format{SampleRate: 22050, Channels: 1, BitsPerSample: 16}

// fakeFramesPerChar makes the fake speak 100 characters per second.
const fakeFramesPerChar = 220

// FakeSynthesizer is a Synthesizer for tests. It writes silence whose
// length is proportional to the length of the text, in FakeFormat, and
// records every call. It is safe for concurrent use.
type FakeSynthesizer struct {
	// Fail, when set, is called before synthesising; a non-nil error is
	// returned to the caller and no file is written.
	Fail func(text string) error
	// Format overrides FakeFormat when its SampleRate is set.
	Format audio.Format

	mu    sync.Mutex
	texts []string
}

// Synthesize implements Synthesizer.
func (f *FakeSynthesizer) Synthesize(ctx context.Context, text, outPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	f.texts = append(f.texts, text)
	f.mu.Unlock()

	if f.Fail != nil {
		if err := f.Fail(text); err != nil {
			return err
		}
	}
	format := FakeFormat
	if f.Format.SampleRate != 0 {
		format = f.Format
	}
	frames := utf8.RuneCountInString(text) * fakeFramesPerChar
	return audio.WriteWAV(outPath, audio.Clip{Format: format, Data: make([]byte, frames*format.FrameSize())})
}

// FakeFrames is the number of frames FakeSynthesizer writes for text.
func FakeFrames(text string) int { return utf8.RuneCountInString(text) * fakeFramesPerChar }

// Texts returns the synthesised texts in call order.
func (f *FakeSynthesizer) Texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

// Calls returns the number of Synthesize calls.
func (f *FakeSynthesizer) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.texts)
}
