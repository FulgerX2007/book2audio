package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/FulgerX2007/book2audio/internal/chapters"
	"github.com/FulgerX2007/book2audio/internal/chunk"
	"github.com/FulgerX2007/book2audio/internal/execx"
	"github.com/FulgerX2007/book2audio/internal/speech"
	"github.com/FulgerX2007/book2audio/internal/tts"
	"github.com/FulgerX2007/book2audio/internal/workpool"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

// StageSynth holds one unit per audio segment; the unit ID is the input
// hash of the segment.
const StageSynth = "synth"

// SynthOptions select what the synth stage works on.
type SynthOptions struct {
	Chapter int // chapter number, or AllChapters
	// Runner runs tts.accent_command; it is needed only when that command
	// is configured.
	Runner execx.Runner
}

// SynthResult summarises a run of the synth stage.
type SynthResult struct {
	Chapters    int // chapters selected
	Segments    int // segments of the selected chapters
	Synthesized int // segments synthesised by this run
	Failed      int // segments that could not be synthesised
}

// SegmentRef is an element of chNN.segments.json: the segments of a
// chapter in reading order.
type SegmentRef struct {
	Hash          string `json:"hash"`
	EndsParagraph bool   `json:"ends_paragraph,omitempty"`
	Heading       bool   `json:"heading,omitempty"`
}

// synthJob is one segment to synthesise.
type synthJob struct {
	chapter int
	hash    string
	text    string
}

// Synth turns the translated chapters into audio segments. Only segments
// without up-to-date audio are synthesised. A segment that fails is
// recorded in the manifest and does not stop the others; the stage then
// returns workspace.ErrUnitsFailed.
func Synth(ctx context.Context, ws *workspace.Workspace, syn tts.Synthesizer, log *slog.Logger, opts SynthOptions) (SynthResult, error) {
	var res SynthResult
	if err := ws.Config.RequireTTS(); err != nil {
		return res, err
	}
	starts, err := chapters.Load(ws)
	if err != nil {
		return res, err
	}

	var jobs []synthJob
	var keep []string
	refsByChapter := make(map[int][]SegmentRef)
	queued := make(map[string]bool)
	selected := false
	for _, s := range starts {
		if opts.Chapter != AllChapters && opts.Chapter != s.N {
			continue
		}
		selected = true
		text, err := os.ReadFile(ws.TranslatedChapterPath(s.N))
		if errors.Is(err, fs.ErrNotExist) {
			return res, fmt.Errorf("глава %d ещё не переведена: сначала выполните translate", s.N)
		}
		if err != nil {
			return res, err
		}
		segments := speech.Prepare(chunk.ParseChapter(string(text)), ws.Config.TTS.SegmentChars)
		res.Chapters++
		res.Segments += len(segments)

		refs := make([]SegmentRef, len(segments))
		for i, seg := range segments {
			hash := segmentHash(ws, seg.Text)
			refs[i] = SegmentRef{Hash: hash, EndsParagraph: seg.EndsParagraph, Heading: seg.Heading}
			keep = append(keep, hash)
			if queued[hash] {
				continue // the same text occurs twice: synthesise it once
			}
			queued[hash] = true
			need, err := ws.Manifest.NeedsWork(StageSynth, hash, hash)
			if err != nil {
				return res, err
			}
			if !need {
				continue
			}
			unit := workspace.Unit{In: hash, Chapter: chapters.UnitID(s.N)}
			// As in the translate stage: a cache file that is already
			// there is the finished audio of exactly this segment.
			if _, err := os.Stat(ws.SegmentPath(hash)); err == nil {
				if err := ws.Manifest.MarkDone(StageSynth, hash, unit, ws.SegmentPath(hash)); err != nil {
					return res, err
				}
				continue
			}
			jobs = append(jobs, synthJob{chapter: s.N, hash: hash, text: seg.Text})
		}
		refsByChapter[s.N] = refs
	}
	if opts.Chapter != AllChapters && !selected {
		return res, fmt.Errorf("главы %d нет в chapters.json", opts.Chapter)
	}

	if len(jobs) > 0 {
		log.Info("озвучка", "segments", len(jobs), "workers", ws.Config.TTS.Workers)
	}
	if err := accentJobs(ctx, ws, opts.Runner, jobs, log); err != nil {
		return res, err
	}
	var mu sync.Mutex // guards res
	// record stores the outcome of one segment in the manifest.
	record := func(ctx context.Context, job synthJob, synthErr error) error {
		unit := workspace.Unit{In: job.hash, Chapter: chapters.UnitID(job.chapter)}
		out := ws.SegmentPath(job.hash)
		if synthErr != nil {
			if ctx.Err() != nil {
				return ctx.Err() // interrupted, not a failure of this segment
			}
			log.Warn("сегмент не озвучен", "chapter", unit.Chapter, "err", synthErr, "text", preview(job.text))
			mu.Lock()
			res.Failed++
			mu.Unlock()
			return ws.Manifest.MarkFailed(StageSynth, job.hash, unit, out, synthErr)
		}
		if err := ws.Manifest.MarkDone(StageSynth, job.hash, unit, out); err != nil {
			return err
		}
		mu.Lock()
		res.Synthesized++
		mu.Unlock()
		return nil
	}

	if batch, ok := syn.(tts.BatchSynthesizer); ok {
		// One engine start per worker: each gets an equal share of the segments.
		err = workpool.Run(ctx, ws.Config.TTS.Workers, splitJobs(jobs, ws.Config.TTS.Workers), func(ctx context.Context, part []synthJob) error {
			items := make([]tts.Item, len(part))
			for i, job := range part {
				items[i] = tts.Item{Text: job.text, OutPath: ws.SegmentPath(job.hash)}
			}
			errs, err := batch.SynthesizeBatch(ctx, items)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return err // the engine itself does not work: stop the stage
			}
			for i, job := range part {
				if err := record(ctx, job, errs[i]); err != nil {
					return err
				}
			}
			return nil
		})
	} else {
		err = workpool.Run(ctx, ws.Config.TTS.Workers, jobs, func(ctx context.Context, job synthJob) error {
			return record(ctx, job, syn.Synthesize(ctx, job.text, ws.SegmentPath(job.hash)))
		})
	}
	if err != nil {
		return res, err
	}

	for n, refs := range refsByChapter {
		data, err := json.MarshalIndent(refs, "", "  ")
		if err != nil {
			return res, err
		}
		if err := workspace.WriteFileAtomic(ws.SegmentsJSONPath(n), append(data, '\n'), 0o644); err != nil {
			return res, err
		}
	}

	// Units of segments that are gone are dropped only on a full run: with
	// one chapter selected the segments of the others are not known here.
	if opts.Chapter == AllChapters {
		if err := ws.Manifest.Prune(StageSynth, keep); err != nil {
			return res, err
		}
	}
	if res.Failed > 0 {
		return res, fmt.Errorf("%w: озвучка, сегментов с ошибкой: %d", workspace.ErrUnitsFailed, res.Failed)
	}
	return res, nil
}

// segmentHash is the input hash of a segment: its text and everything that
// selects the voice. The arguments are hashed as configured, before the
// output path is substituted, so the hash does not depend on where the
// book is stored.
func segmentHash(ws *workspace.Workspace, text string) string {
	cfg := ws.Config.TTS
	parts := append([]string{StageSynth, text, cfg.Backend, cfg.Command}, cfg.Args...)
	// The stress marks change what is spoken, so the accent command is part
	// of the hash. Without one the hash is what it was before the option
	// existed, and audio made earlier stays valid.
	if cfg.AccentCommand != "" {
		parts = append(parts, "accent", cfg.AccentCommand)
		parts = append(parts, cfg.AccentArgs...)
	}
	return workspace.HashStrings(parts...)
}

// accentJobs passes the texts of the jobs through tts.accent_command, which
// puts stress marks into them. The command is started once for all
// segments, because such tools load a language model at start-up: it gets
// one segment per line and must return the same number of lines.
func accentJobs(ctx context.Context, ws *workspace.Workspace, runner execx.Runner, jobs []synthJob, log *slog.Logger) error {
	cfg := ws.Config.TTS
	if cfg.AccentCommand == "" || len(jobs) == 0 {
		return nil
	}
	if runner == nil {
		return errors.New("tts.accent_command задана, но запуск внешних команд не настроен")
	}
	var input strings.Builder
	for _, job := range jobs {
		// A segment is a single line already; make sure of it, since the
		// protocol is line-based.
		input.WriteString(strings.Join(strings.Fields(job.text), " "))
		input.WriteByte('\n')
	}

	log.Info("расстановка ударений", "segments", len(jobs), "command", cfg.AccentCommand)
	out, err := runner.Run(ctx, execx.Cmd{Name: cfg.AccentCommand, Args: cfg.AccentArgs, Stdin: strings.NewReader(input.String())})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("расстановка ударений (tts.accent_command): %w", err)
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n"), "\n")
	if len(lines) != len(jobs) {
		return fmt.Errorf("расстановка ударений: команда %s вернула строк: %d, а сегментов %d — на каждую строку ввода нужна одна строка вывода",
			cfg.AccentCommand, len(lines), len(jobs))
	}
	for i, line := range lines {
		if line = strings.TrimSpace(line); line == "" {
			return fmt.Errorf("расстановка ударений: команда %s вернула пустую строку для сегмента %d", cfg.AccentCommand, i+1)
		} else {
			jobs[i].text = line
		}
	}
	return nil
}

// LoadSegments reads chNN.segments.json of chapter n.
func LoadSegments(ws *workspace.Workspace, n int) ([]SegmentRef, error) {
	path := ws.SegmentsJSONPath(n)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("глава %d ещё не озвучена: сначала выполните synth", n)
	}
	if err != nil {
		return nil, err
	}
	var refs []SegmentRef
	if err := json.Unmarshal(data, &refs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return refs, nil
}

// preview returns the start of a segment text for a log message.
func preview(text string) string {
	const maxRunes = 80
	if r := []rune(text); len(r) > maxRunes {
		return string(r[:maxRunes]) + "…"
	}
	return text
}

// splitJobs deals the jobs into at most n parts of nearly equal size,
// keeping their order inside a part.
func splitJobs(jobs []synthJob, n int) [][]synthJob {
	if n < 1 {
		n = 1
	}
	n = min(n, len(jobs))
	parts := make([][]synthJob, n)
	for i, job := range jobs {
		parts[i%n] = append(parts[i%n], job)
	}
	return parts
}
