package audio

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
	"time"

	"github.com/FulgerX2007/book2audio/internal/execx"
)

func clip(f Format, frames int) Clip { return Clip{Format: f, Data: tone(f, frames)} }

func TestJoin(t *testing.T) {
	a, b, c := clip(mono16, 100), clip(mono16, 200), clip(mono16, 300)
	pause := 100 * time.Millisecond // 2205 frames at 22050 Hz

	got, err := Join([]Clip{a, b, c}, []bool{true, false, true}, pause)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if want := 100 + 2205 + 200 + 300; got.Frames() != want {
		t.Fatalf("Frames = %d, want %d: a pause after the first clip only, none at the end", got.Frames(), want)
	}
	silence := make([]byte, 2205*2)
	want := slices.Concat(a.Data, silence, b.Data, c.Data)
	if !bytes.Equal(got.Data, want) || got.Format != mono16 {
		t.Error("the joined data is not the clips with silence in between")
	}
	if got.Duration() != FramesDuration(2805, 22050) {
		t.Errorf("Duration = %v", got.Duration())
	}

	single, err := Join([]Clip{a}, []bool{true}, pause)
	if err != nil || !bytes.Equal(single.Data, a.Data) {
		t.Errorf("a single clip must come out unchanged: %v", err)
	}
	none, err := Join([]Clip{a, b}, []bool{true, true}, 0)
	if err != nil || none.Frames() != 300 {
		t.Errorf("zero pause: %d frames, %v", none.Frames(), err)
	}
}

func TestJoinErrors(t *testing.T) {
	a := clip(mono16, 100)
	other := clip(Format{SampleRate: 16000, Channels: 1, BitsPerSample: 16}, 100)

	if _, err := Join(nil, nil, 0); err == nil {
		t.Error("Join of nothing succeeded")
	}
	if _, err := Join([]Clip{a, a}, []bool{true}, 0); err == nil {
		t.Error("Join with a wrong number of pause flags succeeded")
	}
	_, err := Join([]Clip{a, other}, []bool{false, false}, 0)
	if err == nil || !strings.Contains(err.Error(), "другой формат") || !strings.Contains(err.Error(), "16000") {
		t.Errorf("mixed formats: error = %v", err)
	}
}

func TestChapters(t *testing.T) {
	got := Chapters([]string{"Вступление", "Глава 1", "Глава 2"}, []int{22050, 33075, 11025}, 22050)
	want := []Chapter{
		{"Вступление", 0, time.Second},
		{"Глава 1", time.Second, 2500 * time.Millisecond},
		{"Глава 2", 2500 * time.Millisecond, 3 * time.Second},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Chapters = %+v, want %+v", got, want)
	}

	// Many chapters of an awkward length: marks follow the frame count
	// exactly instead of accumulating rounding errors.
	frames := make([]int, 1000)
	titles := make([]string, 1000)
	for i := range frames {
		frames[i] = 7351 // a third of a second, not a whole number of milliseconds
	}
	many := Chapters(titles, frames, 22050)
	if last := many[999].End; last != FramesDuration(7351*1000, 22050) {
		t.Errorf("end of the last chapter = %v, want %v", last, FramesDuration(7351*1000, 22050))
	}
	for i := 1; i < len(many); i++ {
		if many[i].Start != many[i-1].End {
			t.Fatalf("chapter %d does not start where chapter %d ends", i, i-1)
		}
	}
}

func TestFFMetadata(t *testing.T) {
	got := FFMetadata(Tags{Title: "Маяк; том #1", Author: "А = Б"}, []Chapter{
		{"Глава 1: начало", 0, 1500 * time.Millisecond},
		{"Глава 2\\конец", 1500 * time.Millisecond, 3 * time.Second},
	})
	want := `;FFMETADATA1
title=Маяк\; том \#1
album=Маяк\; том \#1
artist=А \= Б
genre=Audiobook
[CHAPTER]
TIMEBASE=1/1000
START=0
END=1500
title=Глава 1: начало
[CHAPTER]
TIMEBASE=1/1000
START=1500
END=3000
title=Глава 2\\конец
`
	if got != want {
		t.Errorf("FFMetadata =\n%s\nwant\n%s", got, want)
	}

	bare := FFMetadata(Tags{}, nil)
	if bare != ";FFMETADATA1\ngenre=Audiobook\n" {
		t.Errorf("FFMetadata without tags = %q", bare)
	}
	if got := FFMetadata(Tags{Title: "две\nстроки"}, nil); !strings.Contains(got, "title=две\\\nстроки\n") {
		t.Errorf("a line break in a tag is not escaped: %q", got)
	}
}

// fakeFFmpeg writes its standard input to the output file (the last
// argument), like an encoder that copies.
func fakeFFmpeg() *execx.FakeRunner {
	fake := execx.NewFakeRunner()
	fake.Handle("ffmpeg", func(call execx.Call) ([]byte, error) {
		return nil, os.WriteFile(call.Args[len(call.Args)-1], call.Input, 0o644)
	})
	return fake
}

func TestEncodeM4BArguments(t *testing.T) {
	fake := fakeFFmpeg()
	out := filepath.Join(t.TempDir(), "book.m4b")
	pcm := tone(mono16, 500)

	err := EncodeM4B(context.Background(), fake, bytes.NewReader(pcm), mono16, "/tmp/meta.txt", out, "64k")
	if err != nil {
		t.Fatalf("EncodeM4B: %v", err)
	}
	call := fake.CallsTo("ffmpeg")[0]
	args := strings.Join(call.Args, " ")
	for _, want := range []string{
		"-f s16le -ar 22050 -ac 1 -i pipe:0",
		"-i /tmp/meta.txt",
		"-map 0:a -map_metadata 1 -map_chapters 1",
		"-c:a aac -b:a 64k",
		"-f ipod " + out + ".part",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("ffmpeg arguments lack %q:\n%s", want, args)
		}
	}
	if !bytes.Equal(call.Input, pcm) {
		t.Error("the PCM was not streamed to ffmpeg unchanged")
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, pcm) {
		t.Error("the encoded file was not moved to the output path")
	}
	if _, err := os.Stat(out + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Error("the temporary file was left behind")
	}
}

func TestEncodeMP3Arguments(t *testing.T) {
	fake := fakeFFmpeg()
	out := filepath.Join(t.TempDir(), "01.mp3")
	stereo24 := Format{SampleRate: 44100, Channels: 2, BitsPerSample: 24}

	err := EncodeMP3(context.Background(), fake, bytes.NewReader(tone(stereo24, 10)), stereo24, out, "96k",
		MP3Tags{Title: "Глава 1", Album: "Маяк", Artist: "Автор", Track: 3})
	if err != nil {
		t.Fatalf("EncodeMP3: %v", err)
	}
	args := fake.CallsTo("ffmpeg")[0].Args
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-f s24le -ar 44100 -ac 2 -i pipe:0", "-c:a libmp3lame -b:a 96k",
		"-metadata title=Глава 1", "-metadata album=Маяк", "-metadata artist=Автор", "-metadata track=3",
		"-f mp3 " + out + ".part",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("ffmpeg arguments lack %q:\n%s", want, joined)
		}
	}

	// Empty tags are not passed at all.
	fake = fakeFFmpeg()
	if err := EncodeMP3(context.Background(), fake, bytes.NewReader(tone(mono16, 10)), mono16, out, "64k", MP3Tags{Title: "Только заголовок"}); err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(fake.CallsTo("ffmpeg")[0].Args, " ")
	if strings.Contains(joined, "album=") || strings.Contains(joined, "artist=") || strings.Contains(joined, "track=") {
		t.Errorf("empty tags were passed to ffmpeg:\n%s", joined)
	}
}

func TestEncodeErrors(t *testing.T) {
	out := filepath.Join(t.TempDir(), "book.m4b")
	pcm := func() *bytes.Reader { return bytes.NewReader(tone(mono16, 10)) }

	t.Run("ffmpeg fails", func(t *testing.T) {
		boom := errors.New("ffmpeg: exit status 1: Unknown encoder")
		fake := execx.NewFakeRunner()
		fake.Handle("ffmpeg", func(call execx.Call) ([]byte, error) {
			os.WriteFile(call.Args[len(call.Args)-1], []byte("partial"), 0o644)
			return nil, boom
		})
		if err := EncodeM4B(context.Background(), fake, pcm(), mono16, "meta", out, "64k"); !errors.Is(err, boom) {
			t.Errorf("error = %v, want %v", err, boom)
		}
		for _, path := range []string{out, out + ".part"} {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s was left behind after a failed encoding", path)
			}
		}
	})
	t.Run("ffmpeg writes nothing", func(t *testing.T) {
		fake := execx.NewFakeRunner()
		fake.Stub("ffmpeg", nil, nil)
		err := EncodeMP3(context.Background(), fake, pcm(), mono16, out, "64k", MP3Tags{})
		if err == nil || !strings.Contains(err.Error(), "не создал файл") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("ffmpeg is not installed", func(t *testing.T) {
		err := EncodeM4B(context.Background(), execx.NewFakeRunner(), pcm(), mono16, "meta", out, "64k")
		if !errors.Is(err, execx.ErrToolNotFound) {
			t.Errorf("error = %v, want ErrToolNotFound", err)
		}
	})
	t.Run("unsupported sample size", func(t *testing.T) {
		odd := Format{SampleRate: 22050, Channels: 1, BitsPerSample: 12}
		fake := fakeFFmpeg()
		if err := EncodeM4B(context.Background(), fake, pcm(), odd, "meta", out, "64k"); err == nil || !strings.Contains(err.Error(), "12 бит") {
			t.Errorf("error = %v", err)
		}
		if err := EncodeMP3(context.Background(), fake, pcm(), odd, out, "64k", MP3Tags{}); err == nil {
			t.Error("EncodeMP3 accepted an unsupported sample size")
		}
		if len(fake.Calls()) != 0 {
			t.Error("ffmpeg was run for an unsupported format")
		}
	})
}

// probe runs ffprobe on the file and returns its compact output.
func probe(t *testing.T, path string, entries ...string) string {
	t.Helper()
	args := append([]string{"-v", "error"}, entries...)
	args = append(args, "-of", "compact", path)
	out, err := execx.OSRunner{}.Run(context.Background(), execx.Cmd{Name: "ffprobe", Args: args})
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	return string(out)
}

// TestEncodeWithRealFFmpeg encodes a short book with the real ffmpeg and
// checks the result with ffprobe.
func TestEncodeWithRealFFmpeg(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	dir := t.TempDir()
	// Two chapters of 1.5 s and 0.5 s.
	frames := []int{33075, 11025}
	pcm := append(tone(mono16, frames[0]), tone(mono16, frames[1])...)

	t.Run("m4b with chapters", func(t *testing.T) {
		meta := filepath.Join(dir, "meta.txt")
		chapters := Chapters([]string{"Глава 1: Маяк", "Глава 2; журнал"}, frames, mono16.SampleRate)
		if err := os.WriteFile(meta, []byte(FFMetadata(Tags{Title: "Тестовая книга", Author: "Автор"}, chapters)), 0o644); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(dir, "book.m4b")
		if err := EncodeM4B(context.Background(), execx.OSRunner{}, bytes.NewReader(pcm), mono16, meta, out, "64k"); err != nil {
			t.Fatalf("EncodeM4B: %v", err)
		}

		got := probe(t, out, "-show_chapters", "-show_entries", "format=duration:format_tags=title,artist:stream=codec_name")
		for _, want := range []string{
			"start_time=0.000000|end=1500|end_time=1.500000|tag:title=Глава 1: Маяк",
			"start_time=1.500000|end=2000|end_time=2.000000|tag:title=Глава 2; журнал",
			"codec_name=aac",
			"tag:title=Тестовая книга", "tag:artist=Автор",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("ffprobe output lacks %q:\n%s", want, got)
			}
		}
		if !strings.Contains(got, "duration=2.0") && !strings.Contains(got, "duration=1.9") {
			t.Errorf("duration is not about 2 s:\n%s", got)
		}
	})
	t.Run("mp3 with tags", func(t *testing.T) {
		out := filepath.Join(dir, "01.mp3")
		err := EncodeMP3(context.Background(), execx.OSRunner{}, bytes.NewReader(pcm), mono16, out, "64k",
			MP3Tags{Title: "Глава 1", Album: "Тестовая книга", Artist: "Автор", Track: 1})
		if err != nil {
			t.Fatalf("EncodeMP3: %v", err)
		}
		got := probe(t, out, "-show_entries", "format_tags=title,album,artist,track:stream=codec_name")
		for _, want := range []string{"codec_name=mp3", "tag:title=Глава 1", "tag:album=Тестовая книга", "tag:artist=Автор", "tag:track=1"} {
			if !strings.Contains(got, want) {
				t.Errorf("ffprobe output lacks %q:\n%s", want, got)
			}
		}
	})
}
