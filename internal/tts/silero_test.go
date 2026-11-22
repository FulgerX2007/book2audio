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

func sileroConfig() config.TTS {
	cfg := config.Default().TTS
	cfg.Backend = config.BackendSilero
	cfg.Command = "silero_tts.py"
	cfg.Args = []string{"--speaker", "xenia"}
	return cfg
}

// FakeSileroScript answers like scripts/silero_tts.py: for every input line
// it writes a WAV and prints "ok". Lines whose text contains a word from
// failing get "err" instead and no file.
func fakeSileroScript(t *testing.T, failing ...string) *execx.FakeRunner {
	t.Helper()
	fake := execx.NewFakeRunner()
	fake.Handle("silero_tts.py", func(call execx.Call) ([]byte, error) {
		var out strings.Builder
		for line := range strings.Lines(string(call.Input)) {
			path, text, _ := strings.Cut(strings.TrimRight(line, "\n"), "\t")
			if slices.ContainsFunc(failing, func(w string) bool { return strings.Contains(text, w) }) {
				out.WriteString("err text is too long\n")
				continue
			}
			if err := os.WriteFile(path, goodWAV(t), 0o644); err != nil {
				return nil, err
			}
			out.WriteString("ok\n")
		}
		return []byte(out.String()), nil
	})
	return fake
}

func TestSileroBatch(t *testing.T) {
	fake := fakeSileroScript(t, "плохой")
	syn, err := New(sileroConfig(), fake)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	batch, ok := syn.(BatchSynthesizer)
	if !ok {
		t.Fatalf("New returned %T, want a BatchSynthesizer", syn)
	}
	dir := t.TempDir()
	items := []Item{
		{Text: "Первый сегмент.", OutPath: filepath.Join(dir, "a.wav")},
		{Text: "Этот плохой.", OutPath: filepath.Join(dir, "b.wav")},
		{Text: "Третий,\nв две строки.", OutPath: filepath.Join(dir, "c.wav")},
	}

	errs, err := batch.SynthesizeBatch(context.Background(), items)
	if err != nil {
		t.Fatalf("SynthesizeBatch: %v", err)
	}
	if errs[0] != nil || errs[2] != nil {
		t.Errorf("errors of the good segments = %v, %v", errs[0], errs[2])
	}
	if errs[1] == nil || !strings.Contains(errs[1].Error(), "text is too long") {
		t.Errorf("error of the failed segment = %v", errs[1])
	}

	// The engine is started once, with one line per segment.
	calls := fake.CallsTo("silero_tts.py")
	if len(calls) != 1 || !slices.Equal(calls[0].Args, []string{"--speaker", "xenia"}) {
		t.Fatalf("calls = %+v, want one with the configured arguments", calls)
	}
	want := items[0].OutPath + "\tПервый сегмент.\n" + items[1].OutPath + "\tЭтот плохой.\n" + items[2].OutPath + "\tТретий, в две строки.\n"
	if got := string(calls[0].Input); got != want {
		t.Errorf("stdin =\n%q\nwant\n%q", got, want)
	}
	for i, wantFile := range []bool{true, false, true} {
		_, err := audio.ReadWAV(items[i].OutPath)
		if (err == nil) != wantFile {
			t.Errorf("file of segment %d: %v", i, err)
		}
	}

	if errs, err := batch.SynthesizeBatch(context.Background(), nil); err != nil || errs != nil {
		t.Errorf("empty batch = %v, %v", errs, err)
	}
	if len(fake.Calls()) != 1 {
		t.Error("the engine was started for an empty batch")
	}
}

func TestSileroSynthesizeSingle(t *testing.T) {
	syn, _ := NewSilero(sileroConfig(), fakeSileroScript(t, "плохой"))
	dir := t.TempDir()
	if err := syn.Synthesize(context.Background(), "Текст.", filepath.Join(dir, "a.wav")); err != nil {
		t.Errorf("Synthesize: %v", err)
	}
	if err := syn.Synthesize(context.Background(), "плохой", filepath.Join(dir, "b.wav")); err == nil {
		t.Error("Synthesize of a failing segment succeeded")
	}
}

func TestSileroErrors(t *testing.T) {
	item := func(t *testing.T) []Item {
		return []Item{{Text: "Текст.", OutPath: filepath.Join(t.TempDir(), "a.wav")}}
	}
	boom := errors.New("silero_tts.py: exit status 1: No module named 'scipy'")

	t.Run("engine cannot start", func(t *testing.T) {
		fake := execx.NewFakeRunner()
		fake.Stub("silero_tts.py", nil, boom)
		syn, _ := NewSilero(sileroConfig(), fake)
		if _, err := syn.SynthesizeBatch(context.Background(), item(t)); !errors.Is(err, boom) {
			t.Errorf("error = %v, want %v", err, boom)
		}
	})
	t.Run("script is not found", func(t *testing.T) {
		syn, _ := NewSilero(sileroConfig(), execx.NewFakeRunner())
		_, err := syn.SynthesizeBatch(context.Background(), item(t))
		if !errors.Is(err, execx.ErrToolNotFound) || !strings.Contains(err.Error(), "tts.command") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("wrong number of answers", func(t *testing.T) {
		fake := execx.NewFakeRunner()
		fake.Stub("silero_tts.py", []byte("ok\nok\n"), nil)
		syn, _ := NewSilero(sileroConfig(), fake)
		_, err := syn.SynthesizeBatch(context.Background(), item(t))
		if err == nil || !strings.Contains(err.Error(), "answered 2 lines for 1 segments") {
			t.Errorf("error = %v", err)
		}
	})
	for name, tt := range map[string]struct {
		reply string
		write []byte
		want  string
	}{
		"ok without a file":     {"ok\n", nil, "wrote no file"},
		"ok with garbage":       {"ok\r\n", []byte("not a wav"), "not a WAV"},
		"unknown answer":        {"maybe\n", nil, "unexpected answer"},
		"error without message": {"err\n", nil, "Silero"},
	} {
		t.Run(name, func(t *testing.T) {
			items := item(t)
			fake := execx.NewFakeRunner()
			fake.Handle("silero_tts.py", func(execx.Call) ([]byte, error) {
				if tt.write != nil {
					os.WriteFile(items[0].OutPath, tt.write, 0o644)
				}
				return []byte(tt.reply), nil
			})
			syn, _ := NewSilero(sileroConfig(), fake)
			errs, err := syn.SynthesizeBatch(context.Background(), items)
			if err != nil || errs[0] == nil || !strings.Contains(errs[0].Error(), tt.want) {
				t.Errorf("errs = %v, err = %v; want a segment error containing %q", errs, err, tt.want)
			}
		})
	}
	t.Run("empty command", func(t *testing.T) {
		cfg := sileroConfig()
		cfg.Command = ""
		if _, err := NewSilero(cfg, execx.NewFakeRunner()); err == nil {
			t.Error("NewSilero accepted an empty command")
		}
	})
}

// Words in Latin letters reach the engine rewritten in Cyrillic; the output
// path, which may hold Latin letters, is not touched.
func TestSileroRussianizesLatinWords(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "segment.wav")
	fake := execx.NewFakeRunner()
	var input string
	fake.Handle("silero_tts.py", func(call execx.Call) ([]byte, error) {
		input = string(call.Input)
		if err := os.WriteFile(out, goodWAV(t), 0o644); err != nil {
			return nil, err
		}
		return []byte("ok\n"), nil
	})
	cfg := config.Default().TTS
	cfg.Backend, cfg.Command, cfg.Args = config.BackendSilero, "silero_tts.py", nil
	s, err := NewSilero(cfg, fake)
	if err != nil {
		t.Fatalf("NewSilero: %v", err)
	}
	if err := s.Synthesize(context.Background(), "Журнал Wired и FBI.", out); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if want := out + "\tЖурнал виред и эф би ай.\n"; input != want {
		t.Errorf("engine input = %q, want %q", input, want)
	}
}
