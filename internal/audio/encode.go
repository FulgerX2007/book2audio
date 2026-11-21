package audio

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/FulgerX2007/book2audio/internal/execx"
)

// rawFormats maps the sample size to the name of the raw PCM format of
// ffmpeg.
var rawFormats = map[int]string{8: "u8", 16: "s16le", 24: "s24le", 32: "s32le"}

// inputArgs describes raw PCM read from standard input.
func inputArgs(f Format) ([]string, error) {
	raw, ok := rawFormats[f.BitsPerSample]
	if !ok {
		return nil, fmt.Errorf("неподдерживаемая разрядность звука: %d бит", f.BitsPerSample)
	}
	return []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", raw, "-ar", strconv.Itoa(f.SampleRate), "-ac", strconv.Itoa(f.Channels), "-i", "pipe:0",
	}, nil
}

// EncodeM4B encodes raw PCM in the format f, read from pcm, into an M4B
// audiobook at outPath: AAC audio with the tags and chapter marks taken
// from the ffmpeg metadata file at metadataPath. The PCM is streamed to
// ffmpeg, so the book never has to fit into memory. The file appears at
// outPath only when the encoding has succeeded.
func EncodeM4B(ctx context.Context, runner execx.Runner, pcm io.Reader, f Format, metadataPath, outPath, bitrate string) error {
	args, err := inputArgs(f)
	if err != nil {
		return err
	}
	tmp := outPath + ".part"
	args = append(args,
		"-i", metadataPath,
		"-map", "0:a", "-map_metadata", "1", "-map_chapters", "1",
		"-c:a", "aac", "-b:a", bitrate,
		"-f", "ipod", tmp, // the MP4 flavour used for .m4a and .m4b
	)
	return runEncoder(ctx, runner, args, pcm, tmp, outPath)
}

// MP3Tags are the tags of one MP3 file of an audiobook.
type MP3Tags struct {
	Title  string // chapter title
	Album  string // book title
	Artist string // book author
	Track  int    // 1-based position in the book
}

// EncodeMP3 encodes raw PCM in the format f, read from pcm, into an MP3
// file at outPath. The file appears only when the encoding has succeeded.
func EncodeMP3(ctx context.Context, runner execx.Runner, pcm io.Reader, f Format, outPath, bitrate string, tags MP3Tags) error {
	args, err := inputArgs(f)
	if err != nil {
		return err
	}
	tmp := outPath + ".part"
	args = append(args, "-c:a", "libmp3lame", "-b:a", bitrate, "-id3v2_version", "3")
	for _, tag := range []struct{ key, value string }{
		{"title", tags.Title}, {"album", tags.Album}, {"artist", tags.Artist},
	} {
		if tag.value != "" {
			args = append(args, "-metadata", tag.key+"="+tag.value)
		}
	}
	if tags.Track > 0 {
		args = append(args, "-metadata", "track="+strconv.Itoa(tags.Track))
	}
	args = append(args, "-f", "mp3", tmp)
	return runEncoder(ctx, runner, args, pcm, tmp, outPath)
}

func runEncoder(ctx context.Context, runner execx.Runner, args []string, pcm io.Reader, tmp, outPath string) error {
	defer os.Remove(tmp) // no-op after a successful rename
	if _, err := runner.Run(ctx, execx.Cmd{Name: "ffmpeg", Args: args, Stdin: pcm}); err != nil {
		return err
	}
	if info, err := os.Stat(tmp); err != nil || info.Size() == 0 {
		return fmt.Errorf("ffmpeg не создал файл %s", outPath)
	}
	return os.Rename(tmp, outPath)
}
