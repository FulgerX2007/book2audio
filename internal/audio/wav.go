// Package audio reads, joins and encodes the audio of a book: PCM WAV
// segments are concatenated in Go, ffmpeg is used only for encoding.
package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/FulgerX2007/book2audio/internal/workspace"
)

// ErrNotPCM is wrapped by the error returned for a WAV file that does not
// hold plain integer PCM.
var ErrNotPCM = errors.New("файл не содержит PCM-аудио")

// WAV format tags.
const (
	formatPCM        = 1
	formatExtensible = 0xFFFE
)

// Format describes PCM audio.
type Format struct {
	SampleRate    int // samples per second, e.g. 22050
	Channels      int
	BitsPerSample int // 8, 16, 24 or 32
}

// FrameSize is the number of bytes of one sample of all channels.
func (f Format) FrameSize() int { return f.Channels * f.BitsPerSample / 8 }

func (f Format) String() string {
	return fmt.Sprintf("%d Гц, каналов: %d, %d бит", f.SampleRate, f.Channels, f.BitsPerSample)
}

func (f Format) valid() bool {
	return f.SampleRate > 0 && f.Channels > 0 && f.BitsPerSample > 0 && f.BitsPerSample%8 == 0
}

// Clip is PCM audio in memory.
type Clip struct {
	Format
	Data []byte // little-endian samples, FrameSize bytes per frame
}

// Frames is the number of sample frames of the clip.
func (c Clip) Frames() int {
	if size := c.FrameSize(); size > 0 {
		return len(c.Data) / size
	}
	return 0
}

// Duration is the exact playing time of the clip.
func (c Clip) Duration() time.Duration { return FramesDuration(c.Frames(), c.SampleRate) }

// FramesDuration converts a number of frames to time, rounded down to a
// nanosecond.
func FramesDuration(frames, sampleRate int) time.Duration {
	if sampleRate <= 0 {
		return 0
	}
	return time.Duration(int64(frames) * int64(time.Second) / int64(sampleRate))
}

// Silence returns d of silence in the format f, a whole number of frames.
func Silence(f Format, d time.Duration) []byte {
	frames := int(int64(d) * int64(f.SampleRate) / int64(time.Second))
	return make([]byte, frames*f.FrameSize())
}

// ReadWAV reads a PCM WAV file.
func ReadWAV(path string) (Clip, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Clip{}, err
	}
	clip, err := ParseWAV(data)
	if err != nil {
		return Clip{}, fmt.Errorf("%s: %w", path, err)
	}
	return clip, nil
}

// ParseWAV parses the content of a PCM WAV file: a RIFF container with a
// "fmt " and a "data" chunk; other chunks are skipped.
func ParseWAV(data []byte) (Clip, error) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return Clip{}, errors.New("это не WAV-файл")
	}
	var clip Clip
	haveFormat := false
	for pos := 12; pos+8 <= len(data); {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		body := data[pos+8:]
		// A writer that streamed the file may have left the size of the
		// last chunk unset or too large: take what is there.
		if size > len(body) {
			size = len(body)
		}
		body = body[:size]

		switch id {
		case "fmt ":
			if len(body) < 16 {
				return Clip{}, errors.New("повреждён блок fmt")
			}
			tag := binary.LittleEndian.Uint16(body[0:2])
			if tag == formatExtensible && len(body) >= 26 {
				tag = binary.LittleEndian.Uint16(body[24:26]) // the first field of the sub-format GUID
			}
			if tag != formatPCM {
				return Clip{}, fmt.Errorf("%w (код формата %d)", ErrNotPCM, tag)
			}
			clip.Format = Format{
				Channels:      int(binary.LittleEndian.Uint16(body[2:4])),
				SampleRate:    int(binary.LittleEndian.Uint32(body[4:8])),
				BitsPerSample: int(binary.LittleEndian.Uint16(body[14:16])),
			}
			if !clip.valid() {
				return Clip{}, fmt.Errorf("недопустимый формат: %s", clip.Format)
			}
			haveFormat = true
		case "data":
			if !haveFormat {
				return Clip{}, errors.New("блок data стоит раньше блока fmt")
			}
			// Drop an incomplete trailing frame.
			clip.Data = body[:len(body)-len(body)%clip.FrameSize()]
			return clip, nil
		}
		pos += 8 + size + size%2 // chunks are word-aligned
	}
	if !haveFormat {
		return Clip{}, errors.New("в WAV-файле нет блока fmt")
	}
	return Clip{}, errors.New("в WAV-файле нет блока data")
}

// EncodeWAV renders the clip as a canonical 44-byte-header WAV file.
func EncodeWAV(clip Clip) ([]byte, error) {
	if !clip.valid() {
		return nil, fmt.Errorf("недопустимый формат: %s", clip.Format)
	}
	if len(clip.Data) > 0xFFFFFFFF-36 {
		return nil, errors.New("аудио слишком велико для WAV-файла")
	}
	var b bytes.Buffer
	le := binary.LittleEndian
	b.WriteString("RIFF")
	binary.Write(&b, le, uint32(36+len(clip.Data)))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, le, uint32(16))
	binary.Write(&b, le, uint16(formatPCM))
	binary.Write(&b, le, uint16(clip.Channels))
	binary.Write(&b, le, uint32(clip.SampleRate))
	binary.Write(&b, le, uint32(clip.SampleRate*clip.FrameSize()))
	binary.Write(&b, le, uint16(clip.FrameSize()))
	binary.Write(&b, le, uint16(clip.BitsPerSample))
	b.WriteString("data")
	binary.Write(&b, le, uint32(len(clip.Data)))
	b.Write(clip.Data)
	return b.Bytes(), nil
}

// WriteWAV writes the clip to path atomically.
func WriteWAV(path string, clip Clip) error {
	data, err := EncodeWAV(clip)
	if err != nil {
		return err
	}
	return workspace.WriteFileAtomic(path, data, 0o644)
}
