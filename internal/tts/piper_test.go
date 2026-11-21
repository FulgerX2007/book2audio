package tts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/FulgerX2007/book2audio/internal/audio"
	"github.com/FulgerX2007/book2audio/internal/config"
	"github.com/FulgerX2007/book2audio/internal/execx"
)

func piperConfig() config.TTS {
	cfg := config.Default().TTS
	cfg.Args = []string{"--model", "voices/ru_RU-irina-medium.onnx", "--output_file", "{out}"}
	return cfg
}

// outArg returns the path the engine was told to write to.
func outArg(call execx.Call) string {
	return call.Args[slices.Index(call.Args, "--output_file")+1]
}

// fakePiper returns a runner whose "piper" writes the given WAV content to
// its output file.
func fakePiper(t *testing.T, wav []byte) *execx.FakeRunner {
	t.Helper()
	fake := execx.NewFakeRunner()
	fake.Handle("piper", func(call execx.Call) ([]byte, error) {
		return nil, os.WriteFile(outArg(call), wav, 0o644)
	})
	return fake
}

func goodWAV(t *testing.T) []byte {
	t.Helper()
	data, err := audio.EncodeWAV(audio.Clip{Format: FakeFormat, Data: make([]byte, 2000)})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPiperSynthesize(t *testing.T) {
	fake := fakePiper(t, goodWAV(t))
	p, err := NewPiper(piperConfig(), fake)
	if err != nil {
		t.Fatalf("NewPiper: %v", err)
	}
	out := filepath.Join(t.TempDir(), "seg.wav")

	if err := p.Synthesize(context.Background(), "Первая строка,\nвторая  строка.", out); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	calls := fake.CallsTo("piper")
	if len(calls) != 1 {
		t.Fatalf("piper ran %d times, want once", len(calls))
	}
	call := calls[0]
	if !slices.Equal(call.Args[:3], []string{"--model", "voices/ru_RU-irina-medium.onnx", "--output_file"}) {
		t.Errorf("arguments = %v", call.Args)
	}
	// The engine writes to a temporary file that is renamed afterwards.
	if tmp := outArg(call); tmp == out || !strings.HasPrefix(tmp, out) {
		t.Errorf("the engine wrote to %q, want a temporary file next to %q", tmp, out)
	}
	if got := string(call.Input); got != "Первая строка, вторая строка.\n" {
		t.Errorf("stdin = %q, want the text on a single line", got)
	}

	clip, err := audio.ReadWAV(out)
	if err != nil || clip.Frames() != 1000 {
		t.Errorf("result = %d frames, %v", clip.Frames(), err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(out)); len(entries) != 1 {
		t.Errorf("temporary files were left behind: %v", entries)
	}
}

func TestPiperPlaceholderInsideAnArgument(t *testing.T) {
	cfg := piperConfig()
	cfg.Command = "python3"
	cfg.Args = []string{"-m", "piper", "-m", "ru_RU-irina-medium", "--output-file={out}"}
	fake := execx.NewFakeRunner()
	fake.Handle("python3", func(call execx.Call) ([]byte, error) {
		path := strings.TrimPrefix(call.Args[len(call.Args)-1], "--output-file=")
		return nil, os.WriteFile(path, goodWAV(t), 0o644)
	})
	p, err := NewPiper(cfg, fake)
	if err != nil {
		t.Fatalf("NewPiper: %v", err)
	}
	out := filepath.Join(t.TempDir(), "seg.wav")
	if err := p.Synthesize(context.Background(), "Текст.", out); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("the result is missing: %v", err)
	}
	// The configured arguments are not modified by a call.
	if cfg.Args[4] != "--output-file={out}" {
		t.Errorf("the config arguments were changed: %v", cfg.Args)
	}
}

func TestPiperErrors(t *testing.T) {
	boom := errors.New("piper: exit status 1: model not found")
	tests := []struct {
		name   string
		runner func(t *testing.T) *execx.FakeRunner
		text   string
		want   string
	}{
		{
			name: "engine fails",
			runner: func(t *testing.T) *execx.FakeRunner {
				fake := execx.NewFakeRunner()
				fake.Stub("piper", nil, boom)
				return fake
			},
			text: "Текст.", want: "model not found",
		},
		{
			name:   "engine is not installed",
			runner: func(t *testing.T) *execx.FakeRunner { return execx.NewFakeRunner() },
			text:   "Текст.", want: "tts.command",
		},
		{
			name: "engine writes nothing",
			runner: func(t *testing.T) *execx.FakeRunner {
				fake := execx.NewFakeRunner()
				fake.Stub("piper", nil, nil)
				return fake
			},
			text: "Текст.", want: "wrote no output file",
		},
		{
			name:   "engine writes an empty file",
			runner: func(t *testing.T) *execx.FakeRunner { return fakePiper(t, nil) },
			text:   "Текст.", want: "not a WAV",
		},
		{
			name:   "engine writes garbage",
			runner: func(t *testing.T) *execx.FakeRunner { return fakePiper(t, []byte("Traceback (most recent call last)")) },
			text:   "Текст.", want: "not a WAV",
		},
		{
			name: "engine writes a WAV without samples",
			runner: func(t *testing.T) *execx.FakeRunner {
				data, _ := audio.EncodeWAV(audio.Clip{Format: FakeFormat})
				return fakePiper(t, data)
			},
			text: "Текст.", want: "empty audio file",
		},
		{
			name:   "empty text",
			runner: func(t *testing.T) *execx.FakeRunner { return fakePiper(t, goodWAV(t)) },
			text:   " \n ", want: "empty text",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewPiper(piperConfig(), tt.runner(t))
			if err != nil {
				t.Fatalf("NewPiper: %v", err)
			}
			dir := t.TempDir()
			out := filepath.Join(dir, "seg.wav")
			err = p.Synthesize(context.Background(), tt.text, out)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q", err, tt.want)
			}
			// A failed call leaves neither a result nor a temporary file.
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("files were left behind: %v", entries)
			}
		})
	}
}

func TestNewPiperValidatesConfig(t *testing.T) {
	cfg := piperConfig()
	cfg.Args = []string{"--model", "voice.onnx"}
	if _, err := NewPiper(cfg, execx.NewFakeRunner()); err == nil || !strings.Contains(err.Error(), "{out}") {
		t.Errorf("args without the placeholder: error = %v", err)
	}
	cfg = piperConfig()
	cfg.Command = " "
	if _, err := NewPiper(cfg, execx.NewFakeRunner()); err == nil || !strings.Contains(err.Error(), "tts.command") {
		t.Errorf("empty command: error = %v", err)
	}
}

func TestNew(t *testing.T) {
	syn, err := New(piperConfig(), execx.NewFakeRunner())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := syn.(*Piper); !ok {
		t.Errorf("New returned %T, want *Piper", syn)
	}
	cfg := piperConfig()
	cfg.Backend = "espeak"
	if _, err := New(cfg, execx.NewFakeRunner()); err == nil || !strings.Contains(err.Error(), "espeak") {
		t.Errorf("unknown backend: error = %v", err)
	}
}

func TestFakeSynthesizer(t *testing.T) {
	fake := &FakeSynthesizer{}
	dir := t.TempDir()
	out := filepath.Join(dir, "a.wav")

	if err := fake.Synthesize(context.Background(), "Десять бук", out); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	clip, err := audio.ReadWAV(out)
	if err != nil {
		t.Fatalf("ReadWAV: %v", err)
	}
	if clip.Format != FakeFormat || clip.Frames() != 10*fakeFramesPerChar || clip.Frames() != FakeFrames("Десять бук") {
		t.Errorf("clip = %v, %d frames; want %v, %d frames", clip.Format, clip.Frames(), FakeFormat, 10*fakeFramesPerChar)
	}

	boom := errors.New("сбой")
	fake.Fail = func(text string) error {
		if text == "плохой" {
			return boom
		}
		return nil
	}
	bad := filepath.Join(dir, "bad.wav")
	if err := fake.Synthesize(context.Background(), "плохой", bad); !errors.Is(err, boom) {
		t.Errorf("error = %v, want %v", err, boom)
	}
	if _, err := os.Stat(bad); !errors.Is(err, os.ErrNotExist) {
		t.Error("a file was written for a failed call")
	}

	fake.Fail = nil
	fake.Format = audio.Format{SampleRate: 16000, Channels: 1, BitsPerSample: 16}
	other := filepath.Join(dir, "b.wav")
	if err := fake.Synthesize(context.Background(), "текст", other); err != nil {
		t.Fatal(err)
	}
	if clip, _ := audio.ReadWAV(other); clip.SampleRate != 16000 {
		t.Errorf("Format override ignored: %v", clip.Format)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := fake.Synthesize(ctx, "x", out); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled context: error = %v", err)
	}
	if !slices.Equal(fake.Texts(), []string{"Десять бук", "плохой", "текст"}) || fake.Calls() != 3 {
		t.Errorf("recorded texts = %q", fake.Texts())
	}
}
