package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"

	"github.com/FulgerX2007/book2audio/internal/chapters"
	"github.com/FulgerX2007/book2audio/internal/config"
	"github.com/FulgerX2007/book2audio/internal/execx"
	"github.com/FulgerX2007/book2audio/internal/extract"
	"github.com/FulgerX2007/book2audio/internal/translate"
	"github.com/FulgerX2007/book2audio/internal/tts"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

// Stages are the pipeline stages in the order they run.
var Stages = []string{extract.Stage, chapters.Stage, StageTranslate, StageSynth, StageBuild}

// Deps are the outside-world dependencies of the stages; tests replace them
// with fakes.
type Deps struct {
	// Runner runs the external tools.
	Runner execx.Runner
	// NewTranslator creates the translation backend of a book.
	NewTranslator func(cfg config.Translate) (translate.Translator, error)
	// NewSynthesizer creates the speech backend of a book.
	NewSynthesizer func(cfg config.TTS, runner execx.Runner) (tts.Synthesizer, error)
	// FetchVoice makes a Piper voice available locally; nil means
	// tts.EnsureVoice, which downloads it.
	FetchVoice tts.VoiceFetcher
}

// DefaultDeps returns the real dependencies: processes and network.
func DefaultDeps() Deps {
	return Deps{Runner: execx.OSRunner{}, NewTranslator: translate.New, NewSynthesizer: tts.New}
}

// Synthesizer returns the speech backend of a book. When the engine
// arguments are to be built from a Piper voice (Config.AutoVoice), the voice
// is downloaded if it is missing and the arguments are written into
// ws.Config, so that they enter the segment hashes like hand-written ones.
func (d Deps) Synthesizer(ctx context.Context, ws *workspace.Workspace, log *slog.Logger) (tts.Synthesizer, error) {
	if ws.Config.AutoVoice() {
		dir := ws.Config.TTS.VoicesDir
		if dir == "" {
			var err error
			if dir, err = tts.DefaultVoicesDir(); err != nil {
				return nil, err
			}
		}
		fetch := d.FetchVoice
		if fetch == nil {
			fetch = tts.EnsureVoice
		}
		model, err := fetch(ctx, ws.Config.TTS.VoicesURL, dir, ws.Config.PiperVoice(), log)
		if err != nil {
			return nil, err
		}
		ws.Config.TTS.Args = []string{"--model", model, "--output_file", config.OutPlaceholder}
	}
	return d.NewSynthesizer(ws.Config.TTS, d.Runner)
}

// Translator returns the translator of a book: the configured backend, or a
// pass-through when the book is read in its own language, which needs no
// model settings.
func (d Deps) Translator(cfg config.Config) (translate.Translator, error) {
	if cfg.SameLanguage() {
		return translate.Passthrough{}, nil
	}
	return d.NewTranslator(cfg.Translate)
}

// RunOptions select the stages of a run.
type RunOptions struct {
	From string // first stage to run; empty: the first stage
	To   string // last stage to run; empty: the last stage
}

// Run executes the stages From..To in order and prints a line about each to
// out. It stops at the first stage that fails, also when the stage only has
// failed units: a later stage would work on incomplete input.
func Run(ctx context.Context, ws *workspace.Workspace, deps Deps, log *slog.Logger, out io.Writer, opts RunOptions) error {
	stages, err := selectStages(opts)
	if err != nil {
		return err
	}
	// Fail before any work when a later stage is not configured.
	if slices.Contains(stages, StageTranslate) && !ws.Config.SameLanguage() {
		if err := ws.Config.RequireTranslate(); err != nil {
			return err
		}
	}
	if slices.Contains(stages, StageSynth) && !ws.Config.AutoVoice() {
		if err := ws.Config.RequireTTS(); err != nil {
			return err
		}
	}

	for _, stage := range stages {
		if err := ctx.Err(); err != nil {
			return err
		}
		log.Info("stage", "stage", stage)
		summary, err := runStage(ctx, ws, deps, log, stage)
		if summary != "" {
			fmt.Fprintf(out, "%-9s %s\n", stage+":", summary)
		}
		if err != nil {
			return fmt.Errorf("stage %s: %w", stage, err)
		}
	}
	return nil
}

// selectStages returns the stages From..To.
func selectStages(opts RunOptions) ([]string, error) {
	from, to := 0, len(Stages)-1
	if opts.From != "" {
		if from = slices.Index(Stages, opts.From); from < 0 {
			return nil, unknownStage("--from", opts.From)
		}
	}
	if opts.To != "" {
		if to = slices.Index(Stages, opts.To); to < 0 {
			return nil, unknownStage("--to", opts.To)
		}
	}
	if from > to {
		return nil, fmt.Errorf("the --from stage %s comes after the --to stage %s", opts.From, opts.To)
	}
	return Stages[from : to+1], nil
}

func unknownStage(flag, name string) error {
	return fmt.Errorf("%s: unknown stage %q (stages: %s)", flag, name, strings.Join(Stages, ", "))
}

// runStage runs one stage on the whole book and returns a one-line summary.
func runStage(ctx context.Context, ws *workspace.Workspace, deps Deps, log *slog.Logger, stage string) (string, error) {
	switch stage {
	case extract.Stage:
		res, err := extract.Run(ctx, ws, deps.Runner, log)
		return fmt.Sprintf("%d pages, %d extracted now (%d by OCR), %d failed",
			res.Pages, res.Extracted, res.OCR, res.Failed), err
	case chapters.Stage:
		res, err := chapters.Run(ctx, ws, log)
		return fmt.Sprintf("%d chapters, %d written now, %d kept with hand edits",
			res.Chapters, res.Written, res.Kept), err
	case StageTranslate:
		tr, err := deps.Translator(ws.Config)
		if err != nil {
			return "", err
		}
		res, err := Translate(ctx, ws, tr, log, TranslateOptions{Chapter: AllChapters})
		return fmt.Sprintf("%d chunks, %d translated now, %d failed, %d chapters assembled, %d kept with hand edits",
			res.Chunks, res.Translated, res.Failed, res.Assembled, res.Kept), err
	case StageSynth:
		syn, err := deps.Synthesizer(ctx, ws, log)
		if err != nil {
			return "", err
		}
		res, err := Synth(ctx, ws, syn, log, SynthOptions{Chapter: AllChapters, Runner: deps.Runner})
		return fmt.Sprintf("%d segments, %d synthesised now, %d failed",
			res.Segments, res.Synthesized, res.Failed), err
	case StageBuild:
		res, err := Build(ctx, ws, deps.Runner, log)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d chapters joined now, %d files encoded now\n%s",
			res.Joined, res.Encoded, strings.Join(res.Outputs, "\n")), nil
	}
	return "", fmt.Errorf("unknown stage %q", stage)
}
