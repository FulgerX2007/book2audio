package tts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// VoiceFetcher makes the files of a Piper voice available locally and
// returns the path of its model.
type VoiceFetcher func(ctx context.Context, baseURL, dir, voice string, log *slog.Logger) (string, error)

// DefaultVoicesDir returns where downloaded voices are kept when
// tts.voices_dir is not set: a folder in the user cache directory, shared by
// all books.
func DefaultVoicesDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("voices folder: %w (set tts.voices_dir)", err)
	}
	return filepath.Join(cache, "book2audio", "voices"), nil
}

// voiceFiles returns the paths of the two files of a voice inside the voice
// repository: the model and its settings. A voice "en_US-lessac-medium"
// lives in en/en_US/lessac/medium/.
func voiceFiles(voice string) ([]string, error) {
	first, last := strings.Index(voice, "-"), strings.LastIndex(voice, "-")
	if first <= 0 || last <= first+1 || last == len(voice)-1 {
		return nil, fmt.Errorf("tts.voice: must look like en_US-lessac-medium, got %q", voice)
	}
	locale, name, quality := voice[:first], voice[first+1:last], voice[last+1:]
	lang, _, ok := strings.Cut(locale, "_")
	if !ok || lang == "" {
		return nil, fmt.Errorf("tts.voice: must look like en_US-lessac-medium, got %q", voice)
	}
	dir := strings.Join([]string{lang, locale, name, quality}, "/")
	return []string{dir + "/" + voice + ".onnx.json", dir + "/" + voice + ".onnx"}, nil
}

// EnsureVoice downloads the files of a Piper voice into dir unless they are
// already there and returns the path of the model. A file appears under its
// final name only when it is complete.
func EnsureVoice(ctx context.Context, baseURL, dir, voice string, log *slog.Logger) (string, error) {
	files, err := voiceFiles(voice)
	if err != nil {
		return "", err
	}
	model := filepath.Join(dir, voice+".onnx")
	for _, file := range files {
		target := filepath.Join(dir, filepath.Base(file))
		if info, err := os.Stat(target); err == nil && info.Size() > 0 {
			continue
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		source := strings.TrimRight(baseURL, "/") + "/" + file
		log.Info("downloading the voice", "voice", voice, "file", filepath.Base(file), "to", dir)
		if err := download(ctx, source, target); err != nil {
			return "", fmt.Errorf("voice %s: %w", voice, err)
		}
	}
	return model, nil
}

// download saves the content of a URL to target through a temporary file.
func download(ctx context.Context, source, target string) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", source, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%s is not in the voice repository: check tts.voice", source)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("downloading %s: the server answered %s", source, resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".*.part")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	n, err := io.Copy(tmp, resp.Body)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", source, err)
	}
	if n == 0 {
		return fmt.Errorf("downloading %s: the file is empty", source)
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}
