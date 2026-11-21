package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var mono16 = Format{SampleRate: 22050, Channels: 1, BitsPerSample: 16}

// tone returns frames of non-zero PCM data in the format f.
func tone(f Format, frames int) []byte {
	data := make([]byte, frames*f.FrameSize())
	for i := range data {
		data[i] = byte(i%251 + 1)
	}
	return data
}

// chunkBytes renders one RIFF chunk.
func chunkBytes(id string, body []byte) []byte {
	var b bytes.Buffer
	b.WriteString(id)
	binary.Write(&b, binary.LittleEndian, uint32(len(body)))
	b.Write(body)
	if len(body)%2 == 1 {
		b.WriteByte(0)
	}
	return b.Bytes()
}

// riff wraps chunks into a WAVE container.
func riff(chunks ...[]byte) []byte {
	body := append([]byte("WAVE"), bytes.Join(chunks, nil)...)
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(len(body)))
	b.Write(body)
	return b.Bytes()
}

// fmtChunk renders a "fmt " chunk body with the given format tag.
func fmtChunk(tag uint16, f Format, extra []byte) []byte {
	var b bytes.Buffer
	le := binary.LittleEndian
	binary.Write(&b, le, tag)
	binary.Write(&b, le, uint16(f.Channels))
	binary.Write(&b, le, uint32(f.SampleRate))
	binary.Write(&b, le, uint32(f.SampleRate*f.FrameSize()))
	binary.Write(&b, le, uint16(f.FrameSize()))
	binary.Write(&b, le, uint16(f.BitsPerSample))
	b.Write(extra)
	return chunkBytes("fmt ", b.Bytes())
}

func TestWAVRoundTrip(t *testing.T) {
	for _, f := range []Format{mono16, {SampleRate: 16000, Channels: 1, BitsPerSample: 16}, {SampleRate: 44100, Channels: 2, BitsPerSample: 24}} {
		clip := Clip{Format: f, Data: tone(f, 1000)}
		path := filepath.Join(t.TempDir(), "clip.wav")
		if err := WriteWAV(path, clip); err != nil {
			t.Fatalf("WriteWAV: %v", err)
		}
		got, err := ReadWAV(path)
		if err != nil {
			t.Fatalf("ReadWAV: %v", err)
		}
		if got.Format != f || !bytes.Equal(got.Data, clip.Data) {
			t.Errorf("round trip of %v changed the clip", f)
		}
		if info, _ := os.Stat(path); info.Size() != int64(44+len(clip.Data)) {
			t.Errorf("file size = %d, want a 44-byte header and the data", info.Size())
		}
	}
}

func TestClipDuration(t *testing.T) {
	clip := Clip{Format: mono16, Data: make([]byte, 22050*2*3)}
	if clip.Frames() != 66150 || clip.Duration() != 3*time.Second {
		t.Errorf("Frames = %d, Duration = %v; want 66150 frames, 3s", clip.Frames(), clip.Duration())
	}
	stereo := Clip{Format: Format{SampleRate: 48000, Channels: 2, BitsPerSample: 16}, Data: make([]byte, 48000*4/2)}
	if stereo.Duration() != 500*time.Millisecond {
		t.Errorf("Duration = %v, want 500ms", stereo.Duration())
	}
	if (Clip{}).Frames() != 0 || (Clip{}).Duration() != 0 {
		t.Error("an empty clip has frames or a duration")
	}
	if got := FramesDuration(1, 3); got != time.Second/3 {
		t.Errorf("FramesDuration(1, 3) = %v", got)
	}
}

func TestSilence(t *testing.T) {
	got := Silence(mono16, 400*time.Millisecond)
	if len(got) != 8820*2 {
		t.Errorf("Silence = %d bytes, want %d", len(got), 8820*2)
	}
	for _, b := range got {
		if b != 0 {
			t.Fatal("silence contains non-zero samples")
		}
	}
	if len(Silence(mono16, 0)) != 0 {
		t.Error("zero duration gives samples")
	}
	// A whole number of frames, also for a format with a larger frame.
	stereo := Format{SampleRate: 44100, Channels: 2, BitsPerSample: 16}
	if n := len(Silence(stereo, 333*time.Millisecond)); n%stereo.FrameSize() != 0 {
		t.Errorf("Silence = %d bytes, not a whole number of frames", n)
	}
}

func TestParseWAVVariants(t *testing.T) {
	data := tone(mono16, 10)
	tests := []struct {
		name string
		file []byte
		want int // frames
	}{
		{"canonical", riff(fmtChunk(formatPCM, mono16, nil), chunkBytes("data", data)), 10},
		{"extra chunks before and between", riff(chunkBytes("LIST", []byte("odd")), fmtChunk(formatPCM, mono16, nil), chunkBytes("fact", make([]byte, 4)), chunkBytes("data", data)), 10},
		{"extensible format with PCM sub-format", riff(fmtChunk(formatExtensible, mono16, append(make([]byte, 8), 1, 0, 0, 0)), chunkBytes("data", data)), 10},
		{"incomplete trailing frame is dropped", riff(fmtChunk(formatPCM, mono16, nil), chunkBytes("data", append(bytes.Clone(data), 7))), 10},
		{"no samples", riff(fmtChunk(formatPCM, mono16, nil), chunkBytes("data", nil)), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clip, err := ParseWAV(tt.file)
			if err != nil {
				t.Fatalf("ParseWAV: %v", err)
			}
			if clip.Format != mono16 || clip.Frames() != tt.want {
				t.Errorf("clip = %v, %d frames; want %v, %d frames", clip.Format, clip.Frames(), mono16, tt.want)
			}
		})
	}

	t.Run("data size left unset by a streaming writer", func(t *testing.T) {
		file := riff(fmtChunk(formatPCM, mono16, nil), chunkBytes("data", data))
		// Overwrite the size of the data chunk with 0xFFFFFFFF.
		pos := bytes.Index(file, []byte("data")) + 4
		binary.LittleEndian.PutUint32(file[pos:], 0xFFFFFFFF)
		clip, err := ParseWAV(file)
		if err != nil || clip.Frames() != 10 {
			t.Errorf("clip = %d frames, %v; want 10 frames", clip.Frames(), err)
		}
	})
}

func TestParseWAVErrors(t *testing.T) {
	data := tone(mono16, 10)
	tests := []struct {
		name string
		file []byte
		want string
	}{
		{"empty file", nil, "не WAV"},
		{"not RIFF", []byte("ID3\x04 this is an mp3 file, not a wav"), "не WAV"},
		{"RIFF but not WAVE", append([]byte("RIFF\x04\x00\x00\x00AVI "), data...), "не WAV"},
		{"no fmt chunk", riff(chunkBytes("LIST", []byte("info"))), "нет блока fmt"},
		{"no data chunk", riff(fmtChunk(formatPCM, mono16, nil)), "нет блока data"},
		{"data before fmt", riff(chunkBytes("data", data), fmtChunk(formatPCM, mono16, nil)), "раньше блока fmt"},
		{"short fmt chunk", riff(chunkBytes("fmt ", make([]byte, 8)), chunkBytes("data", data)), "повреждён блок fmt"},
		{"float samples", riff(fmtChunk(3, Format{SampleRate: 22050, Channels: 1, BitsPerSample: 32}, nil), chunkBytes("data", data)), "код формата 3"},
		{"zero sample rate", riff(fmtChunk(formatPCM, Format{Channels: 1, BitsPerSample: 16}, nil), chunkBytes("data", data)), "недопустимый формат"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseWAV(tt.file)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}
	if _, err := ParseWAV(riff(fmtChunk(3, mono16, nil), chunkBytes("data", data))); !errors.Is(err, ErrNotPCM) {
		t.Errorf("float WAV: error = %v, want ErrNotPCM", err)
	}
}

func TestReadWriteWAVErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadWAV(filepath.Join(dir, "absent.wav")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: error = %v", err)
	}
	bad := filepath.Join(dir, "bad.wav")
	if err := os.WriteFile(bad, []byte("not a wav"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWAV(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Errorf("error = %v, want one naming the file", err)
	}
	if err := WriteWAV(filepath.Join(dir, "x.wav"), Clip{}); err == nil {
		t.Error("WriteWAV accepted a clip without a format")
	}
	if err := WriteWAV(filepath.Join(dir, "absent", "x.wav"), Clip{Format: mono16}); err == nil {
		t.Error("WriteWAV into a missing directory succeeded")
	}
}
