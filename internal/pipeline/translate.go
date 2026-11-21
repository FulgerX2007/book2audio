// Package pipeline implements the stages that work on chapters: translate,
// synth and build, and the commands that run and report on all stages.
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/FulgerX2007/book2audio/internal/chapters"
	"github.com/FulgerX2007/book2audio/internal/chunk"
	"github.com/FulgerX2007/book2audio/internal/translate"
	"github.com/FulgerX2007/book2audio/internal/workpool"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

// Manifest stages of the translation.
const (
	// StageTranslate holds one unit per chunk; the unit ID is the input
	// hash of the chunk, so a chunk keeps its translation wherever it
	// moves inside the book.
	StageTranslate = "translate"
	// StageTranslated holds one unit per assembled chapter file.
	StageTranslated = "translated"
)

// AllChapters selects every chapter in the options of a stage.
const AllChapters = -1

// Bounds of the length of a translation relative to its source. A result
// outside them is a truncated or runaway answer.
const (
	minLengthRatio   = 0.3
	maxLengthRatio   = 3.0
	minCheckedLength = 40 // shorter texts are not checked: ratios of a few words vary too much
)

// progressEvery is how often a stage reports its progress, in units.
const progressEvery = 10

// TranslateOptions select what the translate stage works on.
type TranslateOptions struct {
	Chapter int // chapter number, or AllChapters
}

// TranslateResult summarises a run of the translate stage.
type TranslateResult struct {
	Chapters   int // chapters selected
	Chunks     int // chunks of the selected chapters
	Translated int // chunks translated by this run
	Failed     int // chunks that could not be translated
	Assembled  int // chapter files written by this run
	Kept       int // chapter files left alone because they were edited by hand
}

// chunkRef is an element of chNN.chunks.json: the chunks of a chapter in order.
type chunkRef struct {
	Hash      string `json:"hash"`
	Continues bool   `json:"continues,omitempty"`
}

// chapterPlan is the work of the stage for one chapter.
type chapterPlan struct {
	n      int
	chunks []chunk.Chunk
	hashes []string // input hash of chunks[i]
	terms  [][]translate.Term
}

// translateJob is one chunk to translate.
type translateJob struct {
	chapter int
	hash    string
	req     translate.Request
}

// Translate translates the chunks that have no up-to-date translation and
// assembles the chapters whose chunks are all translated. A chunk that
// fails is recorded in the manifest and does not stop the others; the stage
// then returns workspace.ErrUnitsFailed.
func Translate(ctx context.Context, ws *workspace.Workspace, tr translate.Translator, log *slog.Logger, opts TranslateOptions) (TranslateResult, error) {
	var res TranslateResult
	if err := ws.Config.RequireTranslate(); err != nil {
		return res, err
	}
	starts, err := chapters.Load(ws)
	if err != nil {
		return res, err
	}
	glossary, err := translate.LoadGlossary(ws.GlossaryPath())
	if err != nil {
		return res, err
	}

	// Every chapter is planned, also when only one is selected: the units
	// of the others must survive the pruning below.
	var plans []chapterPlan
	var keep []string
	selected := false
	for _, s := range starts {
		plan, err := planChapter(ws, s.N, glossary)
		if err != nil {
			return res, err
		}
		plans = append(plans, plan)
		keep = append(keep, plan.hashes...)
		selected = selected || opts.Chapter == s.N
	}
	if opts.Chapter != AllChapters && !selected {
		return res, fmt.Errorf("chapter %d is not in chapters.json", opts.Chapter)
	}

	var jobs []translateJob
	queued := make(map[string]bool)
	for _, plan := range plans {
		if opts.Chapter != AllChapters && opts.Chapter != plan.n {
			continue
		}
		res.Chapters++
		res.Chunks += len(plan.chunks)
		// Written before any chunk is translated, so that status can show
		// "done of total" for the chapter while the stage is running.
		if err := writeChunkList(ws, plan); err != nil {
			return res, err
		}
		for i, c := range plan.chunks {
			hash := plan.hashes[i]
			if queued[hash] {
				continue // the same text occurs twice: translate it once
			}
			need, err := ws.Manifest.NeedsWork(StageTranslate, hash, hash)
			if err != nil {
				return res, err
			}
			if !need {
				continue
			}
			queued[hash] = true
			// The cache file is named by the input hash and written
			// atomically, so a file that is already there is the finished
			// translation of exactly this chunk: its unit was pruned when
			// an edit removed the chunk from the book, and the edit has
			// now been undone.
			if _, err := os.Stat(ws.ChunkPath(hash)); err == nil {
				unit := workspace.Unit{In: hash, Chapter: chapters.UnitID(plan.n)}
				if err := ws.Manifest.MarkDone(StageTranslate, hash, unit, ws.ChunkPath(hash)); err != nil {
					return res, err
				}
				continue
			}
			jobs = append(jobs, translateJob{chapter: plan.n, hash: hash, req: translate.Request{
				Text:        c.Text,
				SourceLang:  ws.Config.SourceLang,
				TargetLang:  ws.Config.TargetLang,
				Glossary:    plan.terms[i],
				PrevContext: c.Context,
			}})
		}
	}

	if len(jobs) > 0 {
		log.Info("translating", "chunks", len(jobs), "workers", ws.Config.Translate.Workers)
	}
	var mu sync.Mutex // guards res
	err = workpool.Run(ctx, ws.Config.Translate.Workers, jobs, func(ctx context.Context, job translateJob) error {
		unit := workspace.Unit{In: job.hash, Chapter: chapters.UnitID(job.chapter)}
		out := ws.ChunkPath(job.hash)

		text, err := translateChecked(ctx, tr, job.req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err() // interrupted, not a failure of this chunk
			}
			attrs := []any{"chapter", unit.Chapter, "err", err, "text", preview(job.req.Text)}
			// Keep the answer that was rejected: it shows what went wrong
			// (a refusal, a summary, a truncated text) and can be fixed
			// and moved to the cache path by hand.
			if rejected, ok := errors.AsType[*rejectedTranslation](err); ok && rejected.text != "" {
				path := RejectedPath(out)
				if werr := workspace.WriteFileAtomic(path, []byte(rejected.text+"\n"), 0o644); werr == nil {
					attrs = append(attrs, "answer", path)
				}
			}
			log.Warn("chunk not translated", attrs...)
			mu.Lock()
			res.Failed++
			mu.Unlock()
			return ws.Manifest.MarkFailed(StageTranslate, job.hash, unit, out, err)
		}
		if err := workspace.WriteFileAtomic(out, []byte(text+"\n"), 0o644); err != nil {
			return err
		}
		if err := ws.Manifest.MarkDone(StageTranslate, job.hash, unit, out); err != nil {
			return err
		}
		mu.Lock()
		res.Translated++
		done := res.Translated
		mu.Unlock()
		if done%progressEvery == 0 {
			log.Info("chunks translated", "done", done, "of", len(jobs))
		}
		return nil
	})
	if err != nil {
		return res, err
	}

	keepChapters := make([]string, 0, len(plans))
	for _, plan := range plans {
		keepChapters = append(keepChapters, chapters.UnitID(plan.n))
		if opts.Chapter != AllChapters && opts.Chapter != plan.n {
			continue
		}
		written, kept, err := assembleChapter(ws, plan, log)
		if err != nil {
			return res, err
		}
		if written {
			res.Assembled++
		}
		if kept {
			res.Kept++
		}
	}

	if err := ws.Manifest.Prune(StageTranslate, keep); err != nil {
		return res, err
	}
	if err := ws.Manifest.Prune(StageTranslated, keepChapters); err != nil {
		return res, err
	}
	if res.Failed > 0 {
		return res, fmt.Errorf("%w: translate, failed chunks: %d", workspace.ErrUnitsFailed, res.Failed)
	}
	return res, nil
}

// planChapter cuts a chapter into chunks and computes their input hashes.
func planChapter(ws *workspace.Workspace, n int, glossary translate.Glossary) (chapterPlan, error) {
	ch, err := chunk.ReadChapter(ws.ChapterPath(n))
	if err != nil {
		return chapterPlan{}, fmt.Errorf("chapter %d: %w", n, err)
	}
	cfg := ws.Config
	plan := chapterPlan{n: n, chunks: chunk.Split(ch, cfg.Translate.ChunkChars)}
	for _, c := range plan.chunks {
		terms := glossary.Match(c.Text)
		// Only the terms that occur in the chunk enter its hash, so editing
		// a term re-translates just the chunks that mention it. The context
		// is a hint and is deliberately left out.
		parts := []string{
			StageTranslate, c.Text,
			cfg.Translate.Backend, cfg.Translate.Model, cfg.SourceLang, cfg.TargetLang,
			strconv.FormatFloat(cfg.Translate.Temperature, 'g', -1, 64),
			translate.PromptHash(),
		}
		for _, t := range terms {
			parts = append(parts, t.Source, t.Target)
		}
		plan.hashes = append(plan.hashes, workspace.HashStrings(parts...))
		plan.terms = append(plan.terms, terms)
	}
	return plan, nil
}

// translateChecked translates a request and validates the result. A result
// that fails the validation is requested once more; transport failures are
// retried by the translator itself.
func translateChecked(ctx context.Context, tr translate.Translator, req translate.Request) (string, error) {
	var lastErr error
	for range 2 {
		text, err := tr.Translate(ctx, req)
		if err != nil {
			return "", err
		}
		text = strings.TrimSpace(text)
		if lastErr = checkTranslation(req.Text, text); lastErr == nil {
			return text, nil
		}
		lastErr = &rejectedTranslation{err: lastErr, text: text}
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	return "", lastErr
}

// rejectedTranslation is the error of an answer that failed the validation;
// it carries the answer.
type rejectedTranslation struct {
	err  error
	text string
}

func (e *rejectedTranslation) Error() string { return e.err.Error() }
func (e *rejectedTranslation) Unwrap() error { return e.err }

// RejectedPath is where the answer of the model is kept when the
// translation of a chunk is rejected; chunkPath is the cache path of the
// chunk. The name differs from the cache path, so the file is never taken
// for a finished translation.
func RejectedPath(chunkPath string) string {
	return strings.TrimSuffix(chunkPath, ".ru.txt") + ".rejected.txt"
}

// checkTranslation rejects an empty translation and one whose length is far
// from the length of the source.
func checkTranslation(source, translated string) error {
	if translated == "" {
		return errors.New("the translation is empty")
	}
	src := utf8.RuneCountInString(source)
	if src < minCheckedLength {
		return nil
	}
	ratio := float64(utf8.RuneCountInString(translated)) / float64(src)
	if ratio < minLengthRatio || ratio > maxLengthRatio {
		return fmt.Errorf("the translation is %.2f times as long as the source (allowed: %.1f to %.1f)",
			ratio, minLengthRatio, maxLengthRatio)
	}
	return nil
}

// writeChunkList writes chNN.chunks.json: the chunks of the chapter in order.
func writeChunkList(ws *workspace.Workspace, plan chapterPlan) error {
	refs := make([]chunkRef, len(plan.chunks))
	for i, c := range plan.chunks {
		refs[i] = chunkRef{Hash: plan.hashes[i], Continues: c.Continues}
	}
	data, err := json.MarshalIndent(refs, "", "  ")
	if err != nil {
		return err
	}
	return workspace.WriteFileAtomic(ws.ChunksJSONPath(plan.n), append(data, '\n'), 0o644)
}

// assembleChapter writes chNN.chunks.json and, when every chunk of the
// chapter is translated, the chapter file chNN.ru.md. A chapter file edited
// by hand is kept.
func assembleChapter(ws *workspace.Workspace, plan chapterPlan, log *slog.Logger) (written, kept bool, err error) {
	texts := make([]string, len(plan.chunks))
	complete := true
	for i := range plan.chunks {
		u, ok := ws.Manifest.Unit(StageTranslate, plan.hashes[i])
		if !ok || u.Status != workspace.StatusDone {
			complete = false
			continue
		}
		data, err := os.ReadFile(ws.ChunkPath(plan.hashes[i]))
		if err != nil {
			return false, false, err
		}
		texts[i] = string(data)
	}
	if err := writeChunkList(ws, plan); err != nil {
		return false, false, err
	}
	if !complete {
		return false, false, nil
	}

	id := chapters.UnitID(plan.n)
	in := workspace.HashStrings(append([]string{StageTranslated}, plan.hashes...)...)
	need, err := ws.Manifest.NeedsWork(StageTranslated, id, in)
	if err != nil {
		return false, false, err
	}
	if !need {
		// Up to date — unless --force asks to discard a hand edit.
		edited, err := ws.Manifest.Edited(StageTranslated, id)
		if err != nil || !ws.Manifest.Force || !edited {
			return false, false, err
		}
	}
	out := ws.TranslatedChapterPath(plan.n)
	if protected, err := ws.Manifest.Protected(StageTranslated, id); err != nil {
		return false, false, err
	} else if protected {
		log.Warn("the chapter translation was edited by hand and is kept; --force reassembles it from the chunks", "file", out)
		return false, true, nil
	}
	if err := workspace.WriteFileAtomic(out, []byte(chunk.Join(plan.chunks, texts)), 0o644); err != nil {
		return false, false, err
	}
	return true, false, ws.Manifest.MarkDone(StageTranslated, id, workspace.Unit{In: in}, out)
}
