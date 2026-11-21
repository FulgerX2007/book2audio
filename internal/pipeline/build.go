package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/FulgerX2007/book2audio/internal/audio"
	"github.com/FulgerX2007/book2audio/internal/chapters"
	"github.com/FulgerX2007/book2audio/internal/chunk"
	"github.com/FulgerX2007/book2audio/internal/config"
	"github.com/FulgerX2007/book2audio/internal/execx"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

// StageBuild holds one unit per joined chapter ("ch01") and one per output
// file ("book" for the M4B, "mp3-ch01" for an MP3).
const StageBuild = "build"

// bookUnit is the manifest unit of the M4B file.
const bookUnit = "book"

// maxFileNameRunes limits the title part of an MP3 file name.
const maxFileNameRunes = 60

// BuildResult summarises a run of the build stage.
type BuildResult struct {
	Chapters int      // chapters of the book
	Joined   int      // chapter WAV files written by this run
	Encoded  int      // output files encoded by this run
	Outputs  []string // the output files of the book, encoded now or earlier
}

// builtChapter is a chapter ready for encoding.
type builtChapter struct {
	n      int
	title  string
	in     string // input hash of the chapter WAV
	wav    string
	format audio.Format
	frames int
}

// Build joins the audio segments of every chapter and encodes the book: one
// M4B file with chapter marks, or one MP3 file per chapter. Nothing is
// rebuilt while its input stays the same.
func Build(ctx context.Context, ws *workspace.Workspace, runner execx.Runner, log *slog.Logger) (BuildResult, error) {
	var res BuildResult
	starts, err := chapters.Load(ws)
	if err != nil {
		return res, err
	}
	res.Chapters = len(starts)

	var built []builtChapter
	var keep []string
	for _, s := range starts {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		ch, joined, err := joinChapter(ws, s)
		if err != nil {
			return res, err
		}
		if joined {
			res.Joined++
		}
		if len(built) > 0 && ch.format != built[0].format {
			return res, fmt.Errorf("глава %d озвучена в другом формате (%s), чем глава %d (%s): переозвучьте книгу одним голосом",
				ch.n, ch.format, built[0].n, built[0].format)
		}
		built = append(built, ch)
		keep = append(keep, chapters.UnitID(s.N))
	}
	if len(built) == 0 {
		return res, errors.New("в книге нет глав")
	}

	tags := bookTags(ws)
	if err := os.MkdirAll(ws.OutDir(), 0o755); err != nil {
		return res, err
	}
	switch ws.Config.Audio.Format {
	case config.FormatM4B:
		out, encoded, err := encodeBook(ctx, ws, runner, built, tags, log)
		if err != nil {
			return res, err
		}
		keep = append(keep, bookUnit)
		res.Outputs = []string{out}
		if encoded {
			res.Encoded++
		}
	case config.FormatMP3:
		for i, ch := range built {
			out, encoded, err := encodeChapterMP3(ctx, ws, runner, ch, i+1, tags, log)
			if err != nil {
				return res, err
			}
			keep = append(keep, mp3Unit(ch.n))
			res.Outputs = append(res.Outputs, out)
			if encoded {
				res.Encoded++
			}
		}
	default:
		return res, fmt.Errorf("audio.format: неизвестный формат %q", ws.Config.Audio.Format)
	}

	return res, ws.Manifest.Prune(StageBuild, keep)
}

// joinChapter writes the WAV file of a chapter from its segments, unless it
// is up to date. The segments are taken strictly from chNN.segments.json.
func joinChapter(ws *workspace.Workspace, s chapters.Start) (ch builtChapter, joined bool, err error) {
	refs, err := LoadSegments(ws, s.N)
	if err != nil {
		return ch, false, err
	}
	if len(refs) == 0 {
		return ch, false, fmt.Errorf("в главе %d нет текста для озвучки", s.N)
	}
	pause := time.Duration(ws.Config.Audio.ParagraphPauseMS) * time.Millisecond

	parts := []string{StageBuild, strconv.Itoa(ws.Config.Audio.ParagraphPauseMS)}
	for _, ref := range refs {
		parts = append(parts, ref.Hash, strconv.FormatBool(ref.EndsParagraph))
	}
	ch = builtChapter{n: s.N, title: chapterTitle(ws, s), in: workspace.HashStrings(parts...), wav: ws.ChapterWAVPath(s.N)}
	id := chapters.UnitID(s.N)

	need, err := ws.Manifest.NeedsWork(StageBuild, id, ch.in)
	if err != nil {
		return ch, false, err
	}
	if !need {
		clip, err := audio.ReadWAV(ch.wav)
		if err != nil {
			return ch, false, err
		}
		ch.format, ch.frames = clip.Format, clip.Frames()
		return ch, false, nil
	}

	clips := make([]audio.Clip, len(refs))
	pauses := make([]bool, len(refs))
	for i, ref := range refs {
		clip, err := audio.ReadWAV(ws.SegmentPath(ref.Hash))
		if errors.Is(err, fs.ErrNotExist) {
			return ch, false, fmt.Errorf("глава %d: сегмент %d не озвучен: сначала выполните synth", s.N, i+1)
		}
		if err != nil {
			return ch, false, fmt.Errorf("глава %d: %w", s.N, err)
		}
		clips[i], pauses[i] = clip, ref.EndsParagraph
	}
	clip, err := audio.Join(clips, pauses, pause)
	if err != nil {
		return ch, false, fmt.Errorf("глава %d: %w", s.N, err)
	}
	if err := audio.WriteWAV(ch.wav, clip); err != nil {
		return ch, false, err
	}
	ch.format, ch.frames = clip.Format, clip.Frames()
	return ch, true, ws.Manifest.MarkDone(StageBuild, id, workspace.Unit{In: ch.in}, ch.wav)
}

// chapterTitle is the title read aloud and shown by players: the heading of
// the translated chapter, or the source title when the file has none.
func chapterTitle(ws *workspace.Workspace, s chapters.Start) string {
	if ch, err := chunk.ReadChapter(ws.TranslatedChapterPath(s.N)); err == nil && ch.Title != "" {
		return ch.Title
	}
	return s.Title
}

// bookTags are the title and author of the book: from the config, else from
// the PDF metadata, else the name of the working folder.
func bookTags(ws *workspace.Workspace) audio.Tags {
	src := ws.Manifest.GetSource()
	tags := audio.Tags{Title: ws.Config.Book.Title, Author: ws.Config.Book.Author}
	if tags.Title == "" {
		tags.Title = src.Title
	}
	if tags.Title == "" {
		tags.Title = filepath.Base(ws.Dir)
	}
	if tags.Author == "" {
		tags.Author = src.Author
	}
	return tags
}

// encodeBook encodes all chapters into one M4B file with chapter marks.
func encodeBook(ctx context.Context, ws *workspace.Workspace, runner execx.Runner, built []builtChapter, tags audio.Tags, log *slog.Logger) (out string, encoded bool, err error) {
	out = filepath.Join(ws.OutDir(), filepath.Base(ws.Dir)+".m4b")
	parts := []string{StageBuild, config.FormatM4B, ws.Config.Audio.Bitrate, tags.Title, tags.Author}
	titles := make([]string, len(built))
	frames := make([]int, len(built))
	for i, ch := range built {
		parts = append(parts, ch.in, ch.title)
		titles[i], frames[i] = ch.title, ch.frames
	}
	in := workspace.HashStrings(parts...)
	if need, err := ws.Manifest.NeedsWork(StageBuild, bookUnit, in); err != nil || !need {
		return out, false, err
	}

	meta, err := os.CreateTemp("", "book2audio-meta-*.txt")
	if err != nil {
		return out, false, err
	}
	defer os.Remove(meta.Name())
	_, err = meta.WriteString(audio.FFMetadata(tags, audio.Chapters(titles, frames, built[0].format.SampleRate)))
	if closeErr := meta.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return out, false, err
	}

	log.Info("кодирование аудиокниги", "file", out, "chapters", len(built))
	// The chapters are streamed one by one, so only one of them is in
	// memory at a time.
	pr, pw := io.Pipe()
	go func() {
		for _, ch := range built {
			clip, err := audio.ReadWAV(ch.wav)
			if err == nil {
				_, err = pw.Write(clip.Data)
			}
			if err != nil {
				pw.CloseWithError(err)
				return
			}
		}
		pw.Close()
	}()
	err = audio.EncodeM4B(ctx, runner, pr, built[0].format, meta.Name(), out, ws.Config.Audio.Bitrate)
	pr.Close() // unblocks the writer when the encoder stopped early
	if err != nil {
		return out, false, err
	}
	return out, true, ws.Manifest.MarkDone(StageBuild, bookUnit, workspace.Unit{In: in}, out)
}

func mp3Unit(n int) string { return "mp3-" + chapters.UnitID(n) }

// encodeChapterMP3 encodes one chapter into an MP3 file named
// "NN-<title>.mp3", where NN is its position in the book.
func encodeChapterMP3(ctx context.Context, ws *workspace.Workspace, runner execx.Runner, ch builtChapter, track int, tags audio.Tags, log *slog.Logger) (out string, encoded bool, err error) {
	name := fmt.Sprintf("%02d", track)
	if slug := workspace.Slug(ch.title); slug != "" {
		if r := []rune(slug); len(r) > maxFileNameRunes {
			slug = string(r[:maxFileNameRunes])
		}
		name += "-" + slug
	}
	out = filepath.Join(ws.OutDir(), name+".mp3")
	id := mp3Unit(ch.n)
	in := workspace.HashStrings(StageBuild, config.FormatMP3, ws.Config.Audio.Bitrate, ch.in, ch.title,
		tags.Title, tags.Author, strconv.Itoa(track), name)
	if need, err := ws.Manifest.NeedsWork(StageBuild, id, in); err != nil || !need {
		return out, false, err
	}

	// The title, and with it the file name, may have changed: remove the
	// file written under the old name.
	if old, ok := ws.Manifest.Unit(StageBuild, id); ok && old.Out != "" {
		if oldPath := filepath.Join(ws.Dir, filepath.FromSlash(old.Out)); oldPath != out {
			os.Remove(oldPath)
		}
	}

	clip, err := audio.ReadWAV(ch.wav)
	if err != nil {
		return out, false, err
	}
	log.Info("кодирование главы", "file", out)
	err = audio.EncodeMP3(ctx, runner, bytes.NewReader(clip.Data), clip.Format, out, ws.Config.Audio.Bitrate,
		audio.MP3Tags{Title: ch.title, Album: tags.Title, Artist: tags.Author, Track: track})
	if err != nil {
		return out, false, err
	}
	return out, true, ws.Manifest.MarkDone(StageBuild, id, workspace.Unit{In: in}, out)
}
