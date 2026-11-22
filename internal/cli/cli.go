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
	"strings"
	"text/tabwriter"

	"github.com/FulgerX2007/book2audio/internal/chapters"
	"github.com/FulgerX2007/book2audio/internal/execx"
	"github.com/FulgerX2007/book2audio/internal/extract"
	"github.com/FulgerX2007/book2audio/internal/pipeline"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

const prog = "book2audio"

// Exit codes returned by Run.
const (
	ExitOK    = 0 // success
	ExitError = 1 // the subcommand failed
	ExitUsage = 2 // wrong command line
)

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
		fmt.Fprintf(a.Stderr, "%s: unknown subcommand %q\n\n", prog, name)
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
	fs.StringVar(&opts.ConfigPath, "config", "", "config file for this run (default: the book's config.yaml)")
	fs.StringVar(&opts.Workdir, "workdir", "", "directory of the book working folders (default: from the config)")
	fs.BoolVar(&opts.Force, "force", false, "overwrite files that were edited by hand")
	fs.BoolVar(&opts.Verbose, "v", false, "verbose log")
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
	fmt.Fprintf(w, "%s — PDF to a Russian audiobook split by chapters\n\n", prog)
	fmt.Fprintf(w, "Usage:\n  %s <subcommand> [flags] <arguments>\n\nSubcommands:\n", prog)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, cmd := range a.Commands {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", cmd.Name, cmd.Args, cmd.Summary)
	}
	tw.Flush()
	fmt.Fprintf(w, "\nCommon flags: --config, --workdir, --force, -v\n")
	fmt.Fprintf(w, "Help for a subcommand: %s <subcommand> --help\n", prog)
}

func commandUsage(w io.Writer, cmd Command, fs *flag.FlagSet) {
	fmt.Fprintf(w, "Usage: %s %s [flags] %s\n\n%s\n\nFlags:\n", prog, cmd.Name, cmd.Args, cmd.Summary)
	fs.SetOutput(w)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
}

// Deps are the outside-world dependencies of the built-in commands; tests
// replace them with fakes.
type Deps = pipeline.Deps

// DefaultDeps returns the real dependencies: processes and network.
func DefaultDeps() Deps { return pipeline.DefaultDeps() }

// Builtin returns the book2audio subcommands in pipeline order.
func Builtin(deps Deps) []Command {
	runner := deps.Runner
	const book = "<book.pdf | work/<slug>>"
	return []Command{
		{Name: "init", Args: "<book.pdf>", Summary: "create the working folder of a book", Setup: setupInit},
		{Name: "extract", Args: book, Summary: "extract the text page by page (scans go through OCR)", Setup: setupExtract(runner)},
		{Name: "chapters", Args: book, Summary: "clean the text and split it into chapters", Setup: setupChapters},
		{Name: "translate", Args: book, Summary: "translate the chapters", Setup: setupTranslate(deps)},
		{Name: "synth", Args: book, Summary: "synthesise the translated chapters", Setup: setupSynth(deps)},
		{Name: "build", Args: book, Summary: "build the audiobook (M4B or MP3)", Setup: setupBuild(deps)},
		{Name: "run", Args: book, Summary: "run all stages in order", Setup: setupRun(deps)},
		{Name: "status", Args: book, Summary: "show the state of the book by stage", Setup: setupStatus},
	}
}

// setupInit is the Setup of "init".
func setupInit(fs *flag.FlagSet) RunFunc {
	slug := fs.String("slug", "", "working folder name (default: from the file name)")
	return func(_ context.Context, inv *Invocation) error {
		if len(inv.Args) != 1 {
			return &UsageError{Msg: "exactly one argument is required: the path of the PDF"}
		}
		ws, err := workspace.Init(inv.Args[0], workspace.InitOptions{
			ConfigPath: inv.ConfigPath,
			Workdir:    inv.Workdir,
			Slug:       *slug,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(inv.Stdout, "Working folder: %s\nBook config:    %s\n", ws.Dir, ws.ConfigPath())
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
			fmt.Fprintf(inv.Stdout, "Pages: %d, extracted now: %d (by OCR: %d), failed: %d\n",
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
		fmt.Fprintf(inv.Stdout, "Chapters: %d, written now: %d, kept with hand edits: %d, table of contents pages skipped: %d\n",
			res.Chapters, res.Written, res.Kept, res.TOCPages)
		if res.Manual {
			fmt.Fprintln(inv.Stdout, "The chapter starts were taken from the hand-edited chapters.json.")
		}
		return nil
	}
}

// setupTranslate is the Setup of "translate".
func setupTranslate(deps Deps) func(*flag.FlagSet) RunFunc {
	return func(fs *flag.FlagSet) RunFunc {
		chapter := fs.Int("chapter", pipeline.AllChapters, "translate only the chapter with this number")
		return func(ctx context.Context, inv *Invocation) error {
			ws, err := openBook(inv)
			if err != nil {
				return err
			}
			defer ws.Close()

			if !ws.Config.SameLanguage() {
				if err := ws.Config.RequireTranslate(); err != nil {
					return err
				}
			}
			tr, err := deps.Translator(ws.Config)
			if err != nil {
				return err
			}
			res, err := pipeline.Translate(ctx, ws, tr, inv.Log, pipeline.TranslateOptions{Chapter: *chapter})
			fmt.Fprintf(inv.Stdout, "Chapters: %d, chunks: %d, translated now: %d, failed: %d, chapters assembled: %d, kept with hand edits: %d\n",
				res.Chapters, res.Chunks, res.Translated, res.Failed, res.Assembled, res.Kept)
			return err
		}
	}
}

// setupSynth is the Setup of "synth".
func setupSynth(deps Deps) func(*flag.FlagSet) RunFunc {
	return func(fs *flag.FlagSet) RunFunc {
		chapter := fs.Int("chapter", pipeline.AllChapters, "synthesise only the chapter with this number")
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
			res, err := pipeline.Synth(ctx, ws, syn, inv.Log, pipeline.SynthOptions{Chapter: *chapter, Runner: deps.Runner})
			fmt.Fprintf(inv.Stdout, "Chapters: %d, segments: %d, synthesised now: %d, failed: %d\n",
				res.Chapters, res.Segments, res.Synthesized, res.Failed)
			return err
		}
	}
}

// setupBuild is the Setup of "build".
func setupBuild(deps Deps) func(*flag.FlagSet) RunFunc {
	return func(*flag.FlagSet) RunFunc {
		return func(ctx context.Context, inv *Invocation) error {
			ws, err := openBook(inv)
			if err != nil {
				return err
			}
			defer ws.Close()

			res, err := pipeline.Build(ctx, ws, deps.Runner, inv.Log)
			if err != nil {
				return err
			}
			fmt.Fprintf(inv.Stdout, "Chapters: %d, joined now: %d, files encoded now: %d\n", res.Chapters, res.Joined, res.Encoded)
			for _, out := range res.Outputs {
				fmt.Fprintln(inv.Stdout, out)
			}
			return nil
		}
	}
}

// setupRun is the Setup of "run".
func setupRun(deps Deps) func(*flag.FlagSet) RunFunc {
	return func(fs *flag.FlagSet) RunFunc {
		stages := strings.Join(pipeline.Stages, ", ")
		from := fs.String("from", "", "first stage to run ("+stages+")")
		to := fs.String("to", "", "last stage to run")
		return func(ctx context.Context, inv *Invocation) error {
			ws, err := openBook(inv)
			if errors.Is(err, workspace.ErrNotInitialized) && len(inv.Args) == 1 {
				// "run book.pdf" on a new book creates its working folder.
				if _, err := workspace.Init(inv.Args[0], workspace.InitOptions{ConfigPath: inv.ConfigPath, Workdir: inv.Workdir}); err != nil {
					return err
				}
				ws, err = openBook(inv)
			}
			if err != nil {
				return err
			}
			defer ws.Close()

			return pipeline.Run(ctx, ws, deps, inv.Log, inv.Stdout, pipeline.RunOptions{From: *from, To: *to})
		}
	}
}

// setupStatus is the Setup of "status".
func setupStatus(*flag.FlagSet) RunFunc {
	return func(_ context.Context, inv *Invocation) error {
		if len(inv.Args) != 1 {
			return &UsageError{Msg: "exactly one argument is required: the PDF or the working folder of the book"}
		}
		// status only reads, so it does not take the lock: it can be run
		// while another process is working on the book.
		ws, err := workspace.Open(inv.Args[0], workspace.OpenOptions{ConfigPath: inv.ConfigPath, Workdir: inv.Workdir})
		if err != nil {
			return err
		}

		st, err := pipeline.ReadStatus(ws)
		if err != nil {
			return err
		}
		st.Write(inv.Stdout)
		return nil
	}
}

// openBook opens and locks the working folder named by the single
// positional argument of a stage subcommand. The caller must Close it.
func openBook(inv *Invocation) (*workspace.Workspace, error) {
	if len(inv.Args) != 1 {
		return nil, &UsageError{Msg: "exactly one argument is required: the PDF or the working folder of the book"}
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
