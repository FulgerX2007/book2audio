package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/FulgerX2007/book2audio/internal/audio"
	"github.com/FulgerX2007/book2audio/internal/config"
	"github.com/FulgerX2007/book2audio/internal/execx"
	"github.com/FulgerX2007/book2audio/internal/translate"
	"github.com/FulgerX2007/book2audio/internal/tts"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

// spokenBook returns a book that is translated and synthesised by fakes.
func spokenBook(t *testing.T, chs ...chapterText) *workspace.Workspace {
	t.Helper()
	ws := translatedBook(t, chs...)
	synthAll(t, ws, &tts.FakeSynthesizer{})
	return ws
}

// encoder is a fake ffmpeg: it writes the PCM it receives to the output
// file and keeps the metadata file it was given.
type encoder struct {
	*execx.FakeRunner
	metadata []string // content of the metadata file of each call that had one
}

func newEncoder() *encoder {
	e := &encoder{FakeRunner: execx.NewFakeRunner()}
	e.Handle("ffmpeg", func(call execx.Call) ([]byte, error) {
		// The second "-i" names the metadata file of an M4B encoding.
		inputs := 0
		for i, arg := range call.Args {
			if arg != "-i" {
				continue
			}
			if inputs++; inputs == 2 {
				data, err := os.ReadFile(call.Args[i+1])
				if err != nil {
					return nil, err
				}
				e.metadata = append(e.metadata, string(data))
			}
		}
		return nil, os.WriteFile(call.Args[len(call.Args)-1], call.Input, 0o644)
	})
	return e
}

func buildBook(t *testing.T, ws *workspace.Workspace, runner execx.Runner) BuildResult {
	t.Helper()
	res, err := Build(context.Background(), ws, runner, quiet)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return res
}

// chapterFrames is the expected length of a chapter built from fake audio:
// its segments plus a pause after every paragraph but the last.
func chapterFrames(t *testing.T, ws *workspace.Workspace, n int) int {
	t.Helper()
	refs, err := LoadSegments(ws, n)
	if err != nil {
		t.Fatal(err)
	}
	pause := tts.FakeFormat.SampleRate * ws.Config.Audio.ParagraphPauseMS / 1000
	frames := 0
	for i, ref := range refs {
		clip, err := audio.ReadWAV(ws.SegmentPath(ref.Hash))
		if err != nil {
			t.Fatal(err)
		}
		frames += clip.Frames()
		if ref.EndsParagraph && i < len(refs)-1 {
			frames += pause
		}
	}
	return frames
}

func TestBuildM4B(t *testing.T) {
	ws := spokenBook(t, twoChapters()...)
	enc := newEncoder()

	res := buildBook(t, ws, enc)
	out := filepath.Join(ws.OutDir(), filepath.Base(ws.Dir)+".m4b")
	if res.Chapters != 2 || res.Joined != 2 || res.Encoded != 1 || !slices.Equal(res.Outputs, []string{out}) {
		t.Errorf("Result = %+v, want 2 chapters joined and one file %s", res, out)
	}

	// The chapter WAV files hold the segments with pauses between paragraphs.
	frames1, frames2 := chapterFrames(t, ws, 1), chapterFrames(t, ws, 2)
	for n, want := range map[int]int{1: frames1, 2: frames2} {
		clip, err := audio.ReadWAV(ws.ChapterWAVPath(n))
		if err != nil || clip.Frames() != want || clip.Format != tts.FakeFormat {
			t.Errorf("chapter %d WAV: %d frames (%v), %v; want %d frames", n, clip.Frames(), clip.Format, err, want)
		}
	}

	// ffmpeg received the PCM of both chapters, in order, and nothing else.
	calls := enc.CallsTo("ffmpeg")
	if len(calls) != 1 {
		t.Fatalf("ffmpeg ran %d times, want once", len(calls))
	}
	if got, want := len(calls[0].Input), (frames1+frames2)*tts.FakeFormat.FrameSize(); got != want {
		t.Errorf("ffmpeg received %d bytes of PCM, want %d", got, want)
	}
	args := strings.Join(calls[0].Args, " ")
	if !strings.Contains(args, "-f s16le -ar 22050 -ac 1 -i pipe:0") || !strings.Contains(args, "-b:a 64k") {
		t.Errorf("ffmpeg arguments:\n%s", args)
	}

	// Chapter marks carry the translated titles and exact times.
	if len(enc.metadata) != 1 {
		t.Fatalf("metadata files = %d, want 1", len(enc.metadata))
	}
	chapters := audio.Chapters(
		[]string{translate.FakePrefix + "Chapter 1", translate.FakePrefix + "Chapter 2"},
		[]int{frames1, frames2}, tts.FakeFormat.SampleRate)
	want := audio.FFMetadata(audio.Tags{Title: filepath.Base(ws.Dir)}, chapters)
	if enc.metadata[0] != want {
		t.Errorf("metadata =\n%s\nwant\n%s", enc.metadata[0], want)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("the audiobook was not written: %v", err)
	}
}

func TestBuildSkipsWhatIsUpToDate(t *testing.T) {
	ws := spokenBook(t, twoChapters()...)
	enc := newEncoder()
	buildBook(t, ws, enc)

	res := buildBook(t, ws, enc)
	if res.Joined != 0 || res.Encoded != 0 || len(res.Outputs) != 1 {
		t.Errorf("second run: Result = %+v, want nothing rebuilt and the output still listed", res)
	}
	if n := len(enc.CallsTo("ffmpeg")); n != 1 {
		t.Errorf("ffmpeg ran %d times, want once", n)
	}
}

func TestBuildSettingsChangeReencodesWithoutRejoining(t *testing.T) {
	for name, change := range map[string]func(*workspace.Workspace){
		"bitrate":    func(ws *workspace.Workspace) { ws.Config.Audio.Bitrate = "96k" },
		"book title": func(ws *workspace.Workspace) { ws.Config.Book.Title = "Маяк" },
		"author":     func(ws *workspace.Workspace) { ws.Config.Book.Author = "Автор" },
	} {
		t.Run(name, func(t *testing.T) {
			ws := spokenBook(t, twoChapters()...)
			enc := newEncoder()
			buildBook(t, ws, enc)

			change(ws)
			res := buildBook(t, ws, enc)
			if res.Joined != 0 || res.Encoded != 1 {
				t.Errorf("Result = %+v, want the book re-encoded and no chapter rejoined", res)
			}
		})
	}

	t.Run("pause length rejoins and re-encodes", func(t *testing.T) {
		ws := spokenBook(t, twoChapters()...)
		enc := newEncoder()
		buildBook(t, ws, enc)

		ws.Config.Audio.ParagraphPauseMS = 800
		res := buildBook(t, ws, enc)
		if res.Joined != 2 || res.Encoded != 1 {
			t.Errorf("Result = %+v, want both chapters rejoined and the book re-encoded", res)
		}
	})
}

func TestBuildUsesTagsFromConfigThenPDF(t *testing.T) {
	ws := spokenBook(t, chapterText{1, "Chapter 1", paras("A")})
	src := ws.Manifest.GetSource()
	src.Title, src.Author = "PDF Title", "PDF Author"
	if err := ws.Manifest.SetSource(src); err != nil {
		t.Fatal(err)
	}

	enc := newEncoder()
	buildBook(t, ws, enc)
	if !strings.Contains(enc.metadata[0], "title=PDF Title\n") || !strings.Contains(enc.metadata[0], "artist=PDF Author\n") {
		t.Errorf("tags from the PDF are missing:\n%s", enc.metadata[0])
	}

	ws.Config.Book = config.Book{Title: "Свой заголовок", Author: "Свой автор"}
	buildBook(t, ws, enc)
	last := enc.metadata[len(enc.metadata)-1]
	if !strings.Contains(last, "title=Свой заголовок\n") || !strings.Contains(last, "artist=Свой автор\n") {
		t.Errorf("tags from the config do not win:\n%s", last)
	}
}

func TestBuildMP3(t *testing.T) {
	ws := spokenBook(t, twoChapters()...)
	ws.Config.Audio.Format = config.FormatMP3
	enc := newEncoder()

	res := buildBook(t, ws, enc)
	want := []string{
		filepath.Join(ws.OutDir(), "01-ru-chapter-1.mp3"),
		filepath.Join(ws.OutDir(), "02-ru-chapter-2.mp3"),
	}
	if res.Encoded != 2 || !slices.Equal(res.Outputs, want) {
		t.Errorf("Result = %+v, want the files %v", res, want)
	}
	calls := enc.CallsTo("ffmpeg")
	if len(calls) != 2 {
		t.Fatalf("ffmpeg ran %d times, want twice", len(calls))
	}
	for i, call := range calls {
		args := strings.Join(call.Args, " ")
		for _, tag := range []string{"-metadata title=" + translate.FakePrefix + "Chapter " + string(rune('1'+i)), "-metadata track=" + string(rune('1'+i)), "-c:a libmp3lame"} {
			if !strings.Contains(args, tag) {
				t.Errorf("call %d lacks %q:\n%s", i, tag, args)
			}
		}
		if got, wantBytes := len(call.Input), chapterFrames(t, ws, i+1)*tts.FakeFormat.FrameSize(); got != wantBytes {
			t.Errorf("call %d received %d bytes, want %d", i, got, wantBytes)
		}
	}
	for _, path := range want {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was not written: %v", path, err)
		}
	}

	// Switching the format encodes the M4B without rejoining the chapters
	// and drops the MP3 units.
	ws.Config.Audio.Format = config.FormatM4B
	res = buildBook(t, ws, enc)
	if res.Joined != 0 || res.Encoded != 1 || !strings.HasSuffix(res.Outputs[0], ".m4b") {
		t.Errorf("after switching to m4b: Result = %+v", res)
	}
	if ids := ws.Manifest.UnitIDs(StageBuild); !slices.Equal(ids, []string{"book", "ch01", "ch02"}) {
		t.Errorf("units = %v, want [book ch01 ch02]", ids)
	}
}

func TestBuildMP3RenamedChapter(t *testing.T) {
	ws := spokenBook(t, chapterText{1, "Chapter 1", paras("A")})
	ws.Config.Audio.Format = config.FormatMP3
	enc := newEncoder()
	old := buildBook(t, ws, enc).Outputs[0]

	// The user renames the chapter in the translated file.
	path := ws.TranslatedChapterPath(1)
	writeFile(t, path, strings.Replace(readFile(t, path), "# "+translate.FakePrefix+"Chapter 1", "# Глава первая", 1))
	synthAll(t, ws, &tts.FakeSynthesizer{})

	res := buildBook(t, ws, enc)
	if want := filepath.Join(ws.OutDir(), "01-глава-первая.mp3"); !slices.Equal(res.Outputs, []string{want}) {
		t.Errorf("Outputs = %v, want %s", res.Outputs, want)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the file with the old name %s was left behind", old)
	}
}

func TestBuildErrors(t *testing.T) {
	t.Run("chapter is not synthesised", func(t *testing.T) {
		ws := translatedBook(t, twoChapters()...)
		_, err := Build(context.Background(), ws, newEncoder(), quiet)
		if err == nil || !strings.Contains(err.Error(), "synth") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("segment audio is missing", func(t *testing.T) {
		ws := spokenBook(t, twoChapters()...)
		refs, _ := LoadSegments(ws, 1)
		os.Remove(ws.SegmentPath(refs[2].Hash))
		enc := newEncoder()
		_, err := Build(context.Background(), ws, enc, quiet)
		if err == nil || !strings.Contains(err.Error(), "segment 3 is not synthesised") {
			t.Errorf("error = %v", err)
		}
		if len(enc.Calls()) != 0 {
			t.Error("ffmpeg was run although a segment is missing")
		}
	})
	t.Run("chapters in different audio formats", func(t *testing.T) {
		ws := translatedBook(t, twoChapters()...)
		syn := &tts.FakeSynthesizer{}
		if _, err := Synth(context.Background(), ws, syn, quiet, SynthOptions{Chapter: 1}); err != nil {
			t.Fatal(err)
		}
		syn.Format = audio.Format{SampleRate: 16000, Channels: 1, BitsPerSample: 16}
		if _, err := Synth(context.Background(), ws, syn, quiet, SynthOptions{Chapter: 2}); err != nil {
			t.Fatal(err)
		}
		_, err := Build(context.Background(), ws, newEncoder(), quiet)
		if err == nil || !strings.Contains(err.Error(), "a different format") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("ffmpeg fails", func(t *testing.T) {
		ws := spokenBook(t, twoChapters()...)
		boom := errors.New("ffmpeg: exit status 1")
		fake := execx.NewFakeRunner()
		fake.Stub("ffmpeg", nil, boom)
		res, err := Build(context.Background(), ws, fake, quiet)
		if !errors.Is(err, boom) {
			t.Fatalf("error = %v, want %v", err, boom)
		}
		if res.Joined != 2 {
			t.Errorf("Result = %+v, want the chapters joined before the failure", res)
		}
		// The next run only has to encode.
		enc := newEncoder()
		if res := buildBook(t, ws, enc); res.Joined != 0 || res.Encoded != 1 {
			t.Errorf("retry: Result = %+v", res)
		}
	})
	t.Run("chapters stage has not run", func(t *testing.T) {
		ws := newBook(t)
		os.Remove(ws.ChaptersJSONPath())
		if _, err := Build(context.Background(), ws, newEncoder(), quiet); err == nil || !strings.Contains(err.Error(), "chapters") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("cancelled context", func(t *testing.T) {
		ws := spokenBook(t, twoChapters()...)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Build(ctx, ws, newEncoder(), quiet); !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
	})
}

// TestBuildWithRealFFmpeg encodes a fake-spoken book with the real ffmpeg.
func TestBuildWithRealFFmpeg(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	ws := spokenBook(t, twoChapters()...)
	res := buildBook(t, ws, execx.OSRunner{})

	out, err := execx.OSRunner{}.Run(context.Background(), execx.Cmd{
		Name: "ffprobe",
		Args: []string{"-v", "error", "-show_chapters", "-show_entries", "stream=codec_name", "-of", "compact", res.Outputs[0]},
	})
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	if n := bytes.Count(out, []byte("chapter|")); n != 2 {
		t.Errorf("the audiobook has %d chapter marks, want 2:\n%s", n, out)
	}
	for _, want := range []string{"tag:title=" + translate.FakePrefix + "Chapter 1", "tag:title=" + translate.FakePrefix + "Chapter 2", "codec_name=aac"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("ffprobe output lacks %q:\n%s", want, out)
		}
	}
}
