package tts

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// voiceServer serves the files of one voice and records the request paths.
func voiceServer(t *testing.T, files map[string]string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

func TestEnsureVoiceDownloadsOnce(t *testing.T) {
	const base = "/repo/en/en_US/lessac/medium/en_US-lessac-medium"
	srv, requests := voiceServer(t, map[string]string{base + ".onnx": "model", base + ".onnx.json": "{}"})
	dir := filepath.Join(t.TempDir(), "voices") // created on demand

	model, err := EnsureVoice(context.Background(), srv.URL+"/repo/", dir, "en_US-lessac-medium", quietLog)
	if err != nil {
		t.Fatalf("EnsureVoice: %v", err)
	}
	if want := filepath.Join(dir, "en_US-lessac-medium.onnx"); model != want {
		t.Errorf("model path = %q, want %q", model, want)
	}
	for name, want := range map[string]string{"en_US-lessac-medium.onnx": "model", "en_US-lessac-medium.onnx.json": "{}"} {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	if got := requests(); len(got) != 2 {
		t.Errorf("requests = %v, want one per file", got)
	}

	// A voice that is already there is not downloaded again.
	if _, err := EnsureVoice(context.Background(), srv.URL+"/repo", dir, "en_US-lessac-medium", quietLog); err != nil {
		t.Fatalf("second EnsureVoice: %v", err)
	}
	if got := requests(); len(got) != 2 {
		t.Errorf("requests after the second call = %v, want no new ones", got)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.part")); len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
}

func TestEnsureVoiceErrors(t *testing.T) {
	const base = "/ru/ru_RU/irina/medium/ru_RU-irina-medium"
	tests := []struct {
		name  string
		files map[string]string
		voice string
		want  string
	}{
		{"unknown voice", nil, "ru_RU-nobody-medium", "check tts.voice"},
		{"model missing on the server", map[string]string{base + ".onnx.json": "{}"}, "ru_RU-irina-medium", "check tts.voice"},
		{"empty file", map[string]string{base + ".onnx.json": "{}", base + ".onnx": ""}, "ru_RU-irina-medium", "is empty"},
		{"malformed name", nil, "irina", "tts.voice"},
		{"name without a region", nil, "ru-irina-medium", "tts.voice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := voiceServer(t, tt.files)
			dir := t.TempDir()
			_, err := EnsureVoice(context.Background(), srv.URL, dir, tt.voice, quietLog)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("EnsureVoice error = %v, want one containing %q", err, tt.want)
			}
			// No model under its final name, no temporary file.
			if left, _ := filepath.Glob(filepath.Join(dir, "*.onnx*")); len(left) > 1 {
				t.Errorf("files left behind: %v", left)
			}
			if _, err := os.Stat(filepath.Join(dir, tt.voice+".onnx")); err == nil {
				t.Error("an incomplete model appeared under its final name")
			}
		})
	}
}

func TestEnsureVoiceServerFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	_, err := EnsureVoice(context.Background(), srv.URL, t.TempDir(), "ru_RU-irina-medium", quietLog)
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("EnsureVoice error = %v, want the server status", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EnsureVoice(ctx, srv.URL, t.TempDir(), "ru_RU-irina-medium", quietLog); err == nil {
		t.Error("EnsureVoice with a cancelled context succeeded")
	}
}
