// Package cli implements the book2audio command-line interface: subcommand
// dispatch, the flags shared by all subcommands and usage output.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"text/tabwriter"

	"github.com/FulgerX2007/book2audio/internal/chapters"
	"github.com/FulgerX2007/book2audio/internal/config"
	"github.com/FulgerX2007/book2audio/internal/execx"
	"github.com/FulgerX2007/book2audio/internal/extract"
	"github.com/FulgerX2007/book2audio/internal/pipeline"
	"github.com/FulgerX2007/book2audio/internal/translate"
	"github.com/FulgerX2007/book2audio/internal/tts"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

const prog = "book2audio"

// Exit codes returned by Run.
const (
	ExitOK    = 0 // success
	ExitError = 1 // the subcommand failed
	ExitUsage = 2 // wrong command line
)

// ErrNotImplemented is returned by subcommands that are still stubs.
var ErrNotImplemented = errors.New("подкоманда ещё не реализована")

// UsageError is returned by a subcommand when it was called with wrong
// arguments. The dispatcher prints the subcommand usage and exits with
// ExitUsage.
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

// Options are the flags shared by every subcommand.
type Options struct {
	ConfigPath string // --config: config file used for this run
	Workdir    string // --workdir: directory holding the per-book folders
	Force      bool   // --force: overwrite files that were edited by hand
	Verbose    bool   // -v: debug logging
}

// Invocation is one parsed call of a subcommand.
type Invocation struct {
	Options
	Args   []string // positional arguments
	Stdout io.Writer
	Stderr io.Writer
	Log    *slog.Logger
}

// RunFunc executes a subcommand.
type RunFunc func(ctx context.Context, inv *Invocation) error

// Command describes one subcommand.
type Command struct {
	Name    string
	Args    string // positional arguments as shown in the usage text
	Summary string
	// Setup registers the subcommand's own flags on fs and returns the
	// function that runs it once the command line has been parsed.
	Setup func(fs *flag.FlagSet) RunFunc
}

// App dispatches the command line to one of its commands.
type App struct {
	Commands []Command
	Stdout   io.Writer
	Stderr   io.Writer
}

// New returns an App with the built-in book2audio commands.
func New(stdout, stderr io.Writer) *App {
	return &App{Commands: Builtin(DefaultDeps()), Stdout: stdout, Stderr: stderr}
}

// Run parses args (without the program name), runs the selected built-in
// subcommand and returns the process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return New(stdout, stderr).Run(ctx, args)
}

// Run parses args (without the program name), runs the selected subcommand
// and returns the process exit code.
func (a *App) Run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.usage(a.Stderr)
		return ExitUsage
	}

	name, rest := args[0], args[1:]
	switch name {
	case "-h", "-help", "--help":
		a.usage(a.Stdout)
		return ExitOK
	case "help":
		if len(rest) == 0 {
			a.usage(a.Stdout)
			return ExitOK
		}
		// "help <command>" is the same as "<command> --help".
		name, rest = rest[0], []string{"--help"}
	}

	cmd, ok := a.lookup(name)
	if !ok {
		fmt.Fprintf(a.Stderr, "%s: неизвестная подкоманда %q\n\n", prog, name)
		a.usage(a.Stderr)
		return ExitUsage
	}
	return a.runCommand(ctx, cmd, rest)
}

func (a *App) lookup(name string) (Command, bool) {
	for _, cmd := range a.Commands {
		if cmd.Name == name {
			return cmd, true
		}
	}
	return Command{}, false
}

func (a *App) runCommand(ctx context.Context, cmd Command, args []string) int {
	fs := flag.NewFlagSet(cmd.Name, flag.ContinueOnError)
	// Errors and usage are printed here, not by the flag package.
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	var opts Options
	fs.StringVar(&opts.ConfigPath, "config", "", "файл конфига на этот запуск (по умолчанию — config.yaml книги)")
	fs.StringVar(&opts.Workdir, "workdir", "", "каталог с рабочими папками книг (по умолчанию — из конфига)")
	fs.BoolVar(&opts.Force, "force", false, "перезаписывать файлы, изменённые вручную")
	fs.BoolVar(&opts.Verbose, "v", false, "подробный журнал")
	run := cmd.Setup(fs)

	positional, err := parseInterspersed(fs, args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		commandUsage(a.Stdout, cmd, fs)
		return ExitOK
	case err != nil:
		fmt.Fprintf(a.Stderr, "%s %s: %v\n\n", prog, cmd.Name, err)
		commandUsage(a.Stderr, cmd, fs)
		return ExitUsage
	}

	inv := &Invocation{
		Options: opts,
		Args:    positional,
		Stdout:  a.Stdout,
		Stderr:  a.Stderr,
		Log:     newLogger(a.Stderr, opts.Verbose),
	}
	if err := run(ctx, inv); err != nil {
		fmt.Fprintf(a.Stderr, "%s %s: %v\n", prog, cmd.Name, err)
		if _, ok := errors.AsType[*UsageError](err); ok {
			fmt.Fprintln(a.Stderr)
			commandUsage(a.Stderr, cmd, fs)
			return ExitUsage
		}
		return ExitError
	}
	return ExitOK
}

// parseInterspersed parses args with fs and returns the positional arguments.
// Unlike fs.Parse alone it accepts flags after positional arguments
// ("init book.pdf --slug x"). Everything after a "--" is positional.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	for i, arg := range args {
		if arg == "--" {
			args, tail = args[:i], args[i+1:]
			break
		}
	}

	var positional []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		// fs.Parse stopped at the first non-flag argument: keep it and go on.
		positional = append(positional, args[0])
		args = args[1:]
	}
	return append(positional, tail...), nil
}

func newLogger(w io.Writer, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

func (a *App) usage(w io.Writer) {
	fmt.Fprintf(w, "%s — PDF → русская аудиокнига по главам\n\n", prog)
	fmt.Fprintf(w, "Использование:\n  %s <подкоманда> [флаги] <аргументы>\n\nПодкоманды:\n", prog)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, cmd := range a.Commands {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", cmd.Name, cmd.Args, cmd.Summary)
	}
	tw.Flush()
	fmt.Fprintf(w, "\nОбщие флаги: --config, --workdir, --force, -v\n")
	fmt.Fprintf(w, "Справка по подкоманде: %s <подкоманда> --help\n", prog)
}

func commandUsage(w io.Writer, cmd Command, fs *flag.FlagSet) {
	fmt.Fprintf(w, "Использование: %s %s [флаги] %s\n\n%s\n\nФлаги:\n", prog, cmd.Name, cmd.Args, cmd.Summary)
	fs.SetOutput(w)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
}

// Deps are the outside-world dependencies of the built-in commands; tests
// replace them with fakes.
type Deps struct {
	// Runner runs the external tools.
	Runner execx.Runner
	// NewTranslator creates the translation backend of a book.
	NewTranslator func(cfg config.Translate) (translate.Translator, error)
	// NewSynthesizer creates the speech backend of a book.
	NewSynthesizer func(cfg config.TTS, runner execx.Runner) (tts.Synthesizer, error)
}

// DefaultDeps returns the real dependencies: processes and network.
func DefaultDeps() Deps {
	return Deps{Runner: execx.OSRunner{}, NewTranslator: translate.New, NewSynthesizer: tts.New}
}

// Builtin returns the book2audio subcommands in pipeline order.
func Builtin(deps Deps) []Command {
	runner := deps.Runner
	const book = "<book.pdf | work/<slug>>"
	return []Command{
		{Name: "init", Args: "<book.pdf>", Summary: "создать рабочую папку книги", Setup: setupInit},
		{Name: "extract", Args: book, Summary: "извлечь текст по страницам (сканы — через OCR)", Setup: setupExtract(runner)},
		{Name: "chapters", Args: book, Summary: "очистить текст и разбить его на главы", Setup: setupChapters},
		{Name: "translate", Args: book, Summary: "перевести главы", Setup: setupTranslate(deps)},
		{Name: "synth", Args: book, Summary: "озвучить переведённые главы", Setup: setupSynth(deps)},
		{Name: "build", Args: book, Summary: "собрать аудиокнигу (M4B или MP3)", Setup: stub},
		{Name: "run", Args: book, Summary: "выполнить все этапы подряд", Setup: stub},
		{Name: "status", Args: book, Summary: "показать состояние книги по этапам", Setup: stub},
	}
}

// setupInit is the Setup of "init".
func setupInit(fs *flag.FlagSet) RunFunc {
	slug := fs.String("slug", "", "имя рабочей папки (по умолчанию — из имени файла)")
	return func(_ context.Context, inv *Invocation) error {
		if len(inv.Args) != 1 {
			return &UsageError{Msg: "нужен ровно один аргумент — путь к PDF"}
		}
		ws, err := workspace.Init(inv.Args[0], workspace.InitOptions{
			ConfigPath: inv.ConfigPath,
			Workdir:    inv.Workdir,
			Slug:       *slug,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(inv.Stdout, "Рабочая папка: %s\nКонфиг книги:  %s\n", ws.Dir, ws.ConfigPath())
		return nil
	}
}

// setupExtract is the Setup of "extract".
func setupExtract(runner execx.Runner) func(*flag.FlagSet) RunFunc {
	return func(*flag.FlagSet) RunFunc {
		return func(ctx context.Context, inv *Invocation) error {
			ws, err := openBook(inv)
			if err != nil {
				return err
			}
			defer ws.Close()

			res, err := extract.Run(ctx, ws, runner, inv.Log)
			fmt.Fprintf(inv.Stdout, "Страниц: %d, извлечено сейчас: %d (через OCR: %d), с ошибкой: %d\n",
				res.Pages, res.Extracted, res.OCR, res.Failed)
			return err
		}
	}
}

// setupChapters is the Setup of "chapters".
func setupChapters(*flag.FlagSet) RunFunc {
	return func(ctx context.Context, inv *Invocation) error {
		ws, err := openBook(inv)
		if err != nil {
			return err
		}
		defer ws.Close()

		res, err := chapters.Run(ctx, ws, inv.Log)
		if err != nil {
			return err
		}
		fmt.Fprintf(inv.Stdout, "Глав: %d, записано сейчас: %d, оставлено с ручными правками: %d, страниц оглавления пропущено: %d\n",
			res.Chapters, res.Written, res.Kept, res.TOCPages)
		if res.Manual {
			fmt.Fprintln(inv.Stdout, "Границы глав взяты из chapters.json, изменённого вручную.")
		}
		return nil
	}
}

// setupTranslate is the Setup of "translate".
func setupTranslate(deps Deps) func(*flag.FlagSet) RunFunc {
	return func(fs *flag.FlagSet) RunFunc {
		chapter := fs.Int("chapter", pipeline.AllChapters, "перевести только главу с этим номером")
		return func(ctx context.Context, inv *Invocation) error {
			ws, err := openBook(inv)
			if err != nil {
				return err
			}
			defer ws.Close()

			if err := ws.Config.RequireTranslate(); err != nil {
				return err
			}
			tr, err := deps.NewTranslator(ws.Config.Translate)
			if err != nil {
				return err
			}
			res, err := pipeline.Translate(ctx, ws, tr, inv.Log, pipeline.TranslateOptions{Chapter: *chapter})
			fmt.Fprintf(inv.Stdout, "Глав: %d, фрагментов: %d, переведено сейчас: %d, с ошибкой: %d, собрано глав: %d, оставлено с ручными правками: %d\n",
				res.Chapters, res.Chunks, res.Translated, res.Failed, res.Assembled, res.Kept)
			return err
		}
	}
}

// setupSynth is the Setup of "synth".
func setupSynth(deps Deps) func(*flag.FlagSet) RunFunc {
	return func(fs *flag.FlagSet) RunFunc {
		chapter := fs.Int("chapter", pipeline.AllChapters, "озвучить только главу с этим номером")
		return func(ctx context.Context, inv *Invocation) error {
			ws, err := openBook(inv)
			if err != nil {
				return err
			}
			defer ws.Close()

			if err := ws.Config.RequireTTS(); err != nil {
				return err
			}
			syn, err := deps.NewSynthesizer(ws.Config.TTS, deps.Runner)
			if err != nil {
				return err
			}
			res, err := pipeline.Synth(ctx, ws, syn, inv.Log, pipeline.SynthOptions{Chapter: *chapter})
			fmt.Fprintf(inv.Stdout, "Глав: %d, сегментов: %d, озвучено сейчас: %d, с ошибкой: %d\n",
				res.Chapters, res.Segments, res.Synthesized, res.Failed)
			return err
		}
	}
}

// openBook opens and locks the working folder named by the single
// positional argument of a stage subcommand. The caller must Close it.
func openBook(inv *Invocation) (*workspace.Workspace, error) {
	if len(inv.Args) != 1 {
		return nil, &UsageError{Msg: "нужен ровно один аргумент — путь к PDF или к рабочей папке книги"}
	}
	ws, err := workspace.Open(inv.Args[0], workspace.OpenOptions{
		ConfigPath: inv.ConfigPath,
		Workdir:    inv.Workdir,
	})
	if err != nil {
		return nil, err
	}
	if err := ws.Lock(); err != nil {
		return nil, err
	}
	ws.Manifest.Force = inv.Force
	return ws, nil
}

// stub is the Setup of a stage subcommand that is not implemented yet: it
// opens and locks the book like the real stage will, then reports that the
// stage is missing.
func stub(*flag.FlagSet) RunFunc {
	return func(_ context.Context, inv *Invocation) error {
		ws, err := openBook(inv)
		if err != nil {
			return err
		}
		defer ws.Close()
		return ErrNotImplemented
	}
}
