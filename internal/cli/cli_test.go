package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/FulgerX2007/book2audio/internal/config"
	"github.com/FulgerX2007/book2audio/internal/execx"
	"github.com/FulgerX2007/book2audio/internal/translate"
	"github.com/FulgerX2007/book2audio/internal/tts"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

// newTestApp returns an App with the given commands and its output buffers.
func newTestApp(cmds ...Command) (app *App, stdout, stderr *bytes.Buffer) {
	stdout, stderr = new(bytes.Buffer), new(bytes.Buffer)
	return &App{Commands: cmds, Stdout: stdout, Stderr: stderr}, stdout, stderr
}

// capture returns a Setup whose RunFunc stores the invocation in *got.
func capture(got **Invocation) func(*flag.FlagSet) RunFunc {
	return func(*flag.FlagSet) RunFunc {
		return func(_ context.Context, inv *Invocation) error {
			*got = inv
			return nil
		}
	}
}

func TestBuiltinCommands(t *testing.T) {
	want := []string{"init", "extract", "chapters", "translate", "synth", "build", "run", "status"}

	var got []string
	for _, cmd := range Builtin(DefaultDeps()) {
		got = append(got, cmd.Name)
		if cmd.Setup == nil {
			t.Errorf("command %q has no Setup", cmd.Name)
		}
		if cmd.Summary == "" || cmd.Args == "" {
			t.Errorf("command %q lacks usage text", cmd.Name)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("built-in commands = %v, want %v", got, want)
	}
}

func TestRunTopLevel(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout []string
		wantStderr []string
	}{
		{
			name:       "no arguments prints usage as an error",
			args:       nil,
			wantCode:   ExitUsage,
			wantStderr: []string{"Использование:", "Подкоманды:"},
		},
		{
			name:       "--help lists every command",
			args:       []string{"--help"},
			wantCode:   ExitOK,
			wantStdout: []string{"init", "extract", "chapters", "translate", "synth", "build", "run", "status", "--force"},
		},
		{
			name:       "-h",
			args:       []string{"-h"},
			wantCode:   ExitOK,
			wantStdout: []string{"Подкоманды:"},
		},
		{
			name:       "help",
			args:       []string{"help"},
			wantCode:   ExitOK,
			wantStdout: []string{"Подкоманды:"},
		},
		{
			name:       "unknown command",
			args:       []string{"frobnicate"},
			wantCode:   ExitUsage,
			wantStderr: []string{`неизвестная подкоманда "frobnicate"`, "Подкоманды:"},
		},
		{
			name:       "help for an unknown command",
			args:       []string{"help", "frobnicate"},
			wantCode:   ExitUsage,
			wantStderr: []string{`неизвестная подкоманда "frobnicate"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			for _, want := range tt.wantStdout {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
				}
			}
			for _, want := range tt.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr lacks %q:\n%s", want, stderr.String())
				}
			}
			if tt.wantCode == ExitOK && stderr.Len() != 0 {
				t.Errorf("unexpected stderr on success:\n%s", stderr.String())
			}
		})
	}
}

func TestDispatchParsesFlagsAndArguments(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantArgs []string
		wantOpts Options
	}{
		{
			name:     "positional only",
			args:     []string{"demo", "book.pdf"},
			wantArgs: []string{"book.pdf"},
		},
		{
			name:     "no arguments at all",
			args:     []string{"demo"},
			wantArgs: nil,
		},
		{
			name:     "flags before the positional argument",
			args:     []string{"demo", "--config", "c.yaml", "--workdir", "w", "--force", "-v", "book.pdf"},
			wantArgs: []string{"book.pdf"},
			wantOpts: Options{ConfigPath: "c.yaml", Workdir: "w", Force: true, Verbose: true},
		},
		{
			name:     "flags after the positional argument",
			args:     []string{"demo", "book.pdf", "--force", "--workdir=w"},
			wantArgs: []string{"book.pdf"},
			wantOpts: Options{Workdir: "w", Force: true},
		},
		{
			name:     "flags between positional arguments",
			args:     []string{"demo", "a", "-v", "b", "-config", "c.yaml", "c"},
			wantArgs: []string{"a", "b", "c"},
			wantOpts: Options{ConfigPath: "c.yaml", Verbose: true},
		},
		{
			name:     "everything after -- is positional",
			args:     []string{"demo", "--", "--force"},
			wantArgs: []string{"--force"},
		},
		{
			name:     "-- in the middle",
			args:     []string{"demo", "a", "--force", "--", "-v", "b"},
			wantArgs: []string{"a", "-v", "b"},
			wantOpts: Options{Force: true},
		},
		{
			name:     "a lone dash is positional",
			args:     []string{"demo", "-", "--force"},
			wantArgs: []string{"-"},
			wantOpts: Options{Force: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *Invocation
			app, _, stderr := newTestApp(Command{Name: "demo", Setup: capture(&got)})

			if code := app.Run(context.Background(), tt.args); code != ExitOK {
				t.Fatalf("exit code = %d, want %d; stderr:\n%s", code, ExitOK, stderr.String())
			}
			if got == nil {
				t.Fatal("the command was not run")
			}
			if !slices.Equal(got.Args, tt.wantArgs) {
				t.Errorf("Args = %q, want %q", got.Args, tt.wantArgs)
			}
			if got.Options != tt.wantOpts {
				t.Errorf("Options = %+v, want %+v", got.Options, tt.wantOpts)
			}
			if got.Stdout != app.Stdout || got.Stderr != app.Stderr {
				t.Error("the invocation does not carry the App output streams")
			}
		})
	}
}

func TestDispatchSelectsCommandByName(t *testing.T) {
	var first, second *Invocation
	app, _, _ := newTestApp(
		Command{Name: "first", Setup: capture(&first)},
		Command{Name: "second", Setup: capture(&second)},
	)

	if code := app.Run(context.Background(), []string{"second", "x"}); code != ExitOK {
		t.Fatalf("exit code = %d, want %d", code, ExitOK)
	}
	if first != nil {
		t.Error(`"first" was run for the "second" subcommand`)
	}
	if second == nil {
		t.Error(`"second" was not run`)
	}
}

func TestCommandOwnFlags(t *testing.T) {
	var slug string
	var got *Invocation
	cmd := Command{Name: "demo", Setup: func(fs *flag.FlagSet) RunFunc {
		fs.StringVar(&slug, "slug", "", "имя рабочей папки")
		return capture(&got)(fs)
	}}
	app, _, stderr := newTestApp(cmd)

	code := app.Run(context.Background(), []string{"demo", "book.pdf", "--slug", "my-book", "--force"})
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", code, ExitOK, stderr.String())
	}
	if slug != "my-book" {
		t.Errorf("slug = %q, want %q", slug, "my-book")
	}
	if !got.Force || !slices.Equal(got.Args, []string{"book.pdf"}) {
		t.Errorf("Force = %v, Args = %q; want true, [book.pdf]", got.Force, got.Args)
	}
}

func TestCommandHelp(t *testing.T) {
	for _, args := range [][]string{{"demo", "--help"}, {"demo", "book.pdf", "-h"}, {"help", "demo"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var got *Invocation
			cmd := Command{
				Name:    "demo",
				Args:    "<book.pdf>",
				Summary: "показательная подкоманда",
				Setup: func(fs *flag.FlagSet) RunFunc {
					fs.String("slug", "", "имя рабочей папки")
					return capture(&got)(fs)
				},
			}
			app, stdout, stderr := newTestApp(cmd)

			if code := app.Run(context.Background(), args); code != ExitOK {
				t.Fatalf("exit code = %d, want %d", code, ExitOK)
			}
			if got != nil {
				t.Error("the command was run although help was requested")
			}
			for _, want := range []string{
				"Использование: book2audio demo [флаги] <book.pdf>",
				"показательная подкоманда",
				"-config", "-workdir", "-force", "-v", "-slug", "имя рабочей папки",
			} {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("help lacks %q:\n%s", want, stdout.String())
				}
			}
			if stderr.Len() != 0 {
				t.Errorf("unexpected stderr:\n%s", stderr.String())
			}
		})
	}
}

func TestBadFlagIsUsageError(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown flag", []string{"demo", "--nope"}, "nope"},
		{"unknown flag after a positional argument", []string{"demo", "book.pdf", "--nope"}, "nope"},
		{"flag without its value", []string{"demo", "--config"}, "config"},
		{"bad boolean value", []string{"demo", "--force=maybe"}, "force"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *Invocation
			app, stdout, stderr := newTestApp(Command{Name: "demo", Setup: capture(&got)})

			if code := app.Run(context.Background(), tt.args); code != ExitUsage {
				t.Errorf("exit code = %d, want %d", code, ExitUsage)
			}
			if got != nil {
				t.Error("the command was run despite the bad command line")
			}
			if !strings.Contains(stderr.String(), tt.want) || !strings.Contains(stderr.String(), "Использование: book2audio demo") {
				t.Errorf("stderr lacks the error or the usage:\n%s", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("unexpected stdout:\n%s", stdout.String())
			}
		})
	}
}

func TestCommandErrors(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantCode  int
		wantUsage bool
	}{
		{"plain error", errors.New("диск переполнен"), ExitError, false},
		{"usage error", &UsageError{Msg: "нужен путь к PDF"}, ExitUsage, true},
		{"wrapped usage error", errors.Join(errors.New("контекст"), &UsageError{Msg: "нужен путь к PDF"}), ExitUsage, true},
		{"not implemented", ErrNotImplemented, ExitError, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := Command{Name: "demo", Setup: func(*flag.FlagSet) RunFunc {
				return func(context.Context, *Invocation) error { return tt.err }
			}}
			app, _, stderr := newTestApp(cmd)

			if code := app.Run(context.Background(), []string{"demo"}); code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if !strings.Contains(stderr.String(), "book2audio demo: "+tt.err.Error()) {
				t.Errorf("stderr lacks the error message:\n%s", stderr.String())
			}
			if gotUsage := strings.Contains(stderr.String(), "Использование:"); gotUsage != tt.wantUsage {
				t.Errorf("usage printed = %v, want %v", gotUsage, tt.wantUsage)
			}
		})
	}
}

// runBuiltin runs the real book2audio commands and returns the exit code and
// both output streams.
func runBuiltin(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = Run(context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// writeBook creates a stand-in PDF and returns its path and the workdir to use.
func writeBook(t *testing.T) (pdf, workdir string) {
	t.Helper()
	tmp := t.TempDir()
	pdf = filepath.Join(tmp, "My Book.pdf")
	if err := os.WriteFile(pdf, []byte("pdf-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	return pdf, filepath.Join(tmp, "work")
}

func TestInitCommand(t *testing.T) {
	t.Run("creates the working folder", func(t *testing.T) {
		pdf, workdir := writeBook(t)

		code, stdout, stderr := runBuiltin("init", pdf, "--workdir", workdir)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
		}
		dir := filepath.Join(workdir, "my-book")
		if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
			t.Errorf("the working folder was not created: %v", err)
		}
		if !strings.Contains(stdout, dir) {
			t.Errorf("stdout does not name the working folder:\n%s", stdout)
		}
	})
	t.Run("--slug names the folder", func(t *testing.T) {
		pdf, workdir := writeBook(t)

		if code, _, stderr := runBuiltin("init", pdf, "--workdir", workdir, "--slug", "custom"); code != ExitOK {
			t.Fatalf("exit code = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
		}
		if _, err := os.Stat(filepath.Join(workdir, "custom", "manifest.json")); err != nil {
			t.Errorf("the folder named by --slug was not created: %v", err)
		}
	})
	t.Run("without a PDF is a usage error", func(t *testing.T) {
		code, _, stderr := runBuiltin("init")
		if code != ExitUsage || !strings.Contains(stderr, "Использование: book2audio init") {
			t.Errorf("exit code = %d, stderr:\n%s", code, stderr)
		}
	})
	t.Run("missing PDF is a runtime error", func(t *testing.T) {
		code, _, stderr := runBuiltin("init", filepath.Join(t.TempDir(), "absent.pdf"))
		if code != ExitError || !strings.Contains(stderr, "absent.pdf") {
			t.Errorf("exit code = %d, stderr:\n%s", code, stderr)
		}
	})
}

func TestStageCommandsOpenTheBook(t *testing.T) {
	stages := []string{"extract", "chapters", "translate", "synth", "build", "run", "status"}
	// Stages that are still stubs; the list shrinks as stages are implemented.
	stubs := []string{"run", "status"}

	t.Run("initialised book reaches the stage", func(t *testing.T) {
		pdf, workdir := writeBook(t)
		if code, _, stderr := runBuiltin("init", pdf, "--workdir", workdir); code != ExitOK {
			t.Fatalf("init failed:\n%s", stderr)
		}
		for _, stage := range stubs {
			// Both forms of the argument: the PDF and the working folder.
			for _, arg := range []string{pdf, filepath.Join(workdir, "my-book")} {
				code, _, stderr := runBuiltin(stage, arg, "--workdir", workdir)
				if code != ExitError || !strings.Contains(stderr, ErrNotImplemented.Error()) {
					t.Errorf("%s %s: exit code = %d, stderr:\n%s", stage, arg, code, stderr)
				}
			}
		}
	})
	t.Run("book without a working folder", func(t *testing.T) {
		pdf, workdir := writeBook(t)
		code, _, stderr := runBuiltin("extract", pdf, "--workdir", workdir)
		if code != ExitError || !strings.Contains(stderr, "init") {
			t.Errorf("exit code = %d, stderr:\n%s", code, stderr)
		}
	})
	t.Run("without an argument is a usage error", func(t *testing.T) {
		for _, stage := range stages {
			code, _, stderr := runBuiltin(stage)
			if code != ExitUsage || !strings.Contains(stderr, "Использование: book2audio "+stage) {
				t.Errorf("%s: exit code = %d, stderr:\n%s", stage, code, stderr)
			}
		}
	})
	t.Run("book locked by another process", func(t *testing.T) {
		pdf, workdir := writeBook(t)
		if code, _, stderr := runBuiltin("init", pdf, "--workdir", workdir); code != ExitOK {
			t.Fatalf("init failed:\n%s", stderr)
		}
		lock, err := workspace.AcquireLock(filepath.Join(workdir, "my-book"))
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Release()

		code, _, stderr := runBuiltin("extract", pdf, "--workdir", workdir)
		if code != ExitError || !strings.Contains(stderr, workspace.ErrLocked.Error()) {
			t.Errorf("exit code = %d, stderr:\n%s", code, stderr)
		}
	})
}

func TestVerboseSelectsDebugLogging(t *testing.T) {
	tests := []struct {
		args      []string
		wantDebug bool
	}{
		{[]string{"demo"}, false},
		{[]string{"demo", "-v"}, true},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			var got *Invocation
			app, _, stderr := newTestApp(Command{Name: "demo", Setup: capture(&got)})
			ctx := context.Background()

			if code := app.Run(ctx, tt.args); code != ExitOK {
				t.Fatalf("exit code = %d, want %d", code, ExitOK)
			}
			if !got.Log.Enabled(ctx, slog.LevelInfo) {
				t.Error("info logging is disabled")
			}
			if enabled := got.Log.Enabled(ctx, slog.LevelDebug); enabled != tt.wantDebug {
				t.Errorf("debug logging enabled = %v, want %v", enabled, tt.wantDebug)
			}

			got.Log.Info("этап завершён", "stage", "extract")
			if !strings.Contains(stderr.String(), "stage=extract") {
				t.Errorf("the log does not go to stderr:\n%s", stderr.String())
			}
		})
	}
}

func TestContextReachesCommand(t *testing.T) {
	type key struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "marker"))
	cancel()

	var gotValue any
	var gotErr error
	cmd := Command{Name: "demo", Setup: func(*flag.FlagSet) RunFunc {
		return func(ctx context.Context, _ *Invocation) error {
			gotValue, gotErr = ctx.Value(key{}), ctx.Err()
			return nil
		}
	}}
	app, _, _ := newTestApp(cmd)

	if code := app.Run(ctx, []string{"demo"}); code != ExitOK {
		t.Fatalf("exit code = %d, want %d", code, ExitOK)
	}
	if gotValue != "marker" {
		t.Errorf("context value = %v, want %q", gotValue, "marker")
	}
	if !errors.Is(gotErr, context.Canceled) {
		t.Errorf("context error = %v, want context.Canceled", gotErr)
	}
}

func TestExtractCommand(t *testing.T) {
	pdf, workdir := writeBook(t)
	fake := execx.NewFakeRunner()
	fake.Stub("pdfinfo", []byte("Title:   Demo\nPages:   2\n"), nil)
	// Each page is long enough to count as having a text layer (no OCR).
	const filler = " — a page with enough plain text to have a real text layer\n"
	fake.Stub("pdftotext", []byte("first page"+filler+"\fsecond page"+filler+"\f"), nil)
	tr := &translate.FakeTranslator{}
	syn := &tts.FakeSynthesizer{}
	app, stdout, stderr := newTestApp(Builtin(Deps{
		Runner:         fake,
		NewTranslator:  func(config.Translate) (translate.Translator, error) { return tr, nil },
		NewSynthesizer: func(config.TTS, execx.Runner) (tts.Synthesizer, error) { return syn, nil },
	})...)

	if code := app.Run(context.Background(), []string{"init", pdf, "--workdir", workdir}); code != ExitOK {
		t.Fatalf("init failed:\n%s", stderr.String())
	}
	stdout.Reset()
	if code := app.Run(context.Background(), []string{"extract", pdf, "--workdir", workdir}); code != ExitOK {
		t.Fatalf("extract: exit code = %d; stderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Страниц: 2, извлечено сейчас: 2") {
		t.Errorf("stdout lacks the summary:\n%s", stdout.String())
	}
	got, err := os.ReadFile(filepath.Join(workdir, "my-book", "01-pages", "p0002.txt"))
	if err != nil || string(got) != "second page"+filler {
		t.Errorf("page 2 = %q, %v", got, err)
	}

	// The next stage works on the extracted pages.
	stdout.Reset()
	if code := app.Run(context.Background(), []string{"chapters", pdf, "--workdir", workdir}); code != ExitOK {
		t.Fatalf("chapters: exit code = %d; stderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Глав: 1, записано сейчас: 1") {
		t.Errorf("stdout lacks the chapters summary:\n%s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(workdir, "my-book", "02-chapters", "ch01.md")); err != nil {
		t.Errorf("the chapter file was not written: %v", err)
	}

	// Translation needs a model in the book config.
	if code := app.Run(context.Background(), []string{"translate", pdf, "--workdir", workdir}); code != ExitError ||
		!strings.Contains(stderr.String(), "translate.model") {
		t.Errorf("translate without a model: exit code = %d; stderr:\n%s", code, stderr.String())
	}
	bookConfig := filepath.Join(workdir, "my-book", "config.yaml")
	if err := os.WriteFile(bookConfig, []byte("translate:\n  model: test-model\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := app.Run(context.Background(), []string{"translate", pdf, "--workdir", workdir, "--chapter", "1"}); code != ExitOK {
		t.Fatalf("translate: exit code = %d; stderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "переведено сейчас: 1") || tr.Calls() != 1 {
		t.Errorf("translator calls = %d; stdout:\n%s", tr.Calls(), stdout.String())
	}
	translated, err := os.ReadFile(filepath.Join(workdir, "my-book", "03-translated", "ch01.ru.md"))
	if err != nil || !strings.Contains(string(translated), translate.FakePrefix+"first page") {
		t.Errorf("translated chapter = %q, %v", translated, err)
	}
	if code := app.Run(context.Background(), []string{"translate", pdf, "--workdir", workdir, "--chapter", "7"}); code != ExitError {
		t.Errorf("translate of an unknown chapter: exit code = %d, want %d", code, ExitError)
	}

	// Speech needs the engine arguments in the book config.
	if code := app.Run(context.Background(), []string{"synth", pdf, "--workdir", workdir}); code != ExitError ||
		!strings.Contains(stderr.String(), "tts.args") {
		t.Errorf("synth without engine arguments: exit code = %d; stderr:\n%s", code, stderr.String())
	}
	if err := os.WriteFile(bookConfig, []byte("translate:\n  model: test-model\ntts:\n  args: [\"-f\", \"{out}\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := app.Run(context.Background(), []string{"synth", pdf, "--workdir", workdir}); code != ExitOK {
		t.Fatalf("synth: exit code = %d; stderr:\n%s", code, stderr.String())
	}
	// The chapter is a title and one paragraph: two segments.
	if !strings.Contains(stdout.String(), "сегментов: 2, озвучено сейчас: 2") || syn.Calls() != 2 {
		t.Errorf("synthesizer calls = %d; stdout:\n%s", syn.Calls(), stdout.String())
	}

	// The audiobook is encoded by (a fake) ffmpeg.
	fake.Handle("ffmpeg", func(call execx.Call) ([]byte, error) {
		return nil, os.WriteFile(call.Args[len(call.Args)-1], call.Input, 0o644)
	})
	stdout.Reset()
	if code := app.Run(context.Background(), []string{"build", pdf, "--workdir", workdir}); code != ExitOK {
		t.Fatalf("build: exit code = %d; stderr:\n%s", code, stderr.String())
	}
	book := filepath.Join(workdir, "my-book", "out", "my-book.m4b")
	if _, err := os.Stat(book); err != nil || !strings.Contains(stdout.String(), book) {
		t.Errorf("the audiobook %s: %v; stdout:\n%s", book, err, stdout.String())
	}

	// A failing tool makes the subcommand fail.
	fake.Stub("pdfinfo", nil, errors.New("pdfinfo: exit status 1"))
	if code := app.Run(context.Background(), []string{"extract", pdf, "--workdir", workdir}); code != ExitError {
		t.Errorf("extract with a failing tool: exit code = %d, want %d", code, ExitError)
	}
}
