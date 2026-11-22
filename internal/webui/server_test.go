package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FulgerX2007/book2audio/internal/config"
	"github.com/FulgerX2007/book2audio/internal/execx"
	"github.com/FulgerX2007/book2audio/internal/pipeline"
	"github.com/FulgerX2007/book2audio/internal/translate"
	"github.com/FulgerX2007/book2audio/internal/tts"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fixture is a server over an empty working directory with fake backends.
type fixture struct {
	t      *testing.T
	server *Server
	http   *httptest.Server
	tr     *translate.FakeTranslator
	syn    *tts.FakeSynthesizer
}

func pageBody(name string) string {
	return fmt.Sprintf("Paragraph %s opens here. It goes on for a while and then it ends.", name)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("translate:\n  model: test-model\nextract:\n  ocr_min_chars: 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := execx.NewFakeRunner()
	pages := []string{"Chapter 1: The Start\n" + pageBody("A"), "Chapter 2: The End\n" + pageBody("B")}
	runner.Stub("pdfinfo", fmt.Appendf(nil, "Title: Demo Book\nAuthor: Somebody\nPages: %d\n", len(pages)), nil)
	runner.Stub("pdftotext", []byte(strings.Join(pages, "\f")+"\f"), nil)

	f := &fixture{t: t, tr: &translate.FakeTranslator{}, syn: &tts.FakeSynthesizer{}}
	f.server = &Server{
		Workdir:    filepath.Join(dir, "books"),
		ConfigPath: cfg,
		Log:        quiet,
		Deps: pipeline.Deps{
			Runner:         runner,
			NewTranslator:  func(config.Translate) (translate.Translator, error) { return f.tr, nil },
			NewSynthesizer: func(config.TTS, execx.Runner) (tts.Synthesizer, error) { return f.syn, nil },
			FetchVoice: func(_ context.Context, _, dir, voice string, _ *slog.Logger) (string, error) {
				return filepath.Join(dir, voice+".onnx"), nil
			},
		},
	}
	f.http = httptest.NewServer(f.server.Handler())
	t.Cleanup(func() {
		f.http.Close()
		f.server.Close()
	})
	return f
}

// do sends a request with the header that marks it as coming from the page.
func (f *fixture) do(method, path string, body io.Reader, contentType string) (int, []byte) {
	f.t.Helper()
	req, err := http.NewRequest(method, f.http.URL+path, body)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set(RequestHeader, "1")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return res.StatusCode, data
}

func (f *fixture) json(method, path string, payload any, into any) int {
	f.t.Helper()
	var body io.Reader
	if payload != nil {
		data, _ := json.Marshal(payload)
		body = bytes.NewReader(data)
	}
	status, data := f.do(method, path, body, "application/json")
	if into != nil {
		if err := json.Unmarshal(data, into); err != nil {
			f.t.Fatalf("%s %s: status %d, body %q: %v", method, path, status, data, err)
		}
	}
	return status
}

// upload adds a book from an uploaded file and returns the status and the
// decoded answer.
func (f *fixture) upload(fileName, slug string) (int, map[string]string) {
	f.t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if fileName != "" {
		part, _ := form.CreateFormFile("file", fileName)
		part.Write([]byte("%PDF-1.4 stand-in for " + fileName))
	}
	form.WriteField("slug", slug)
	form.Close()
	status, data := f.do(http.MethodPost, "/api/books", &body, form.FormDataContentType())
	var answer map[string]string
	if err := json.Unmarshal(data, &answer); err != nil {
		f.t.Fatalf("add book: status %d, body %q", status, data)
	}
	return status, answer
}

// wait polls the book until its job is no longer running.
func (f *fixture) wait(slug string) detailJSON {
	f.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var d detailJSON
		if status := f.json(http.MethodGet, "/api/books/"+slug, nil, &d); status != http.StatusOK {
			f.t.Fatalf("detail: status %d", status)
		}
		if d.Job != nil && !d.Job.Running {
			return d
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("the run did not end: %+v", d.Job)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAddListAndRun(t *testing.T) {
	f := newFixture(t)

	var books []bookJSON
	if status := f.json(http.MethodGet, "/api/books", nil, &books); status != http.StatusOK || len(books) != 0 {
		t.Fatalf("empty directory: status %d, books %+v", status, books)
	}

	status, added := f.upload("My Book.pdf", "demo")
	if status != http.StatusCreated || added["slug"] != "demo" || !strings.HasSuffix(added["configPath"], filepath.Join("demo", "config.yaml")) {
		t.Fatalf("add book: status %d, answer %v", status, added)
	}
	// The upload folder is not a book.
	f.json(http.MethodGet, "/api/books", nil, &books)
	if len(books) != 1 || books[0].Slug != "demo" || books[0].Running {
		t.Fatalf("books = %+v, want only demo", books)
	}

	var d detailJSON
	f.json(http.MethodGet, "/api/books/demo", nil, &d)
	if d.Job != nil || len(d.AllStages) != len(pipeline.Stages) || d.TargetLang != "ru" || len(d.Chapters) != 0 {
		t.Fatalf("detail before a run = %+v", d)
	}

	if status := f.json(http.MethodPost, "/api/books/demo/run", map[string]any{"to": pipeline.StageSynth}, nil); status != http.StatusAccepted {
		t.Fatalf("run: status %d", status)
	}
	d = f.wait("demo")
	if d.Job.Error != "" || d.Job.Stopped || d.Job.To != pipeline.StageSynth || d.Job.Finished == "" {
		t.Fatalf("job = %+v, want a clean finish", d.Job)
	}
	if d.Title != "Demo Book" || d.Author != "Somebody" || d.Pages != 2 {
		t.Errorf("book = %q by %q, %d pages", d.Title, d.Author, d.Pages)
	}
	if len(d.Chapters) != 2 || !d.Chapters[0].Translated || d.Chapters[0].Segments == 0 || d.Chapters[0].SegmentsDone != d.Chapters[0].Segments {
		t.Errorf("chapters = %+v, want two translated and synthesised", d.Chapters)
	}
	for _, s := range d.Stages {
		if s.Failed != 0 || (s.Name != pipeline.StageBuild && (s.Total == 0 || s.Done != s.Total)) {
			t.Errorf("stage %+v, want everything done up to synth", s)
		}
	}
	if log := strings.Join(d.Job.Log, "\n"); !strings.Contains(log, "stage=translate") || !strings.Contains(log, "synth:") {
		t.Errorf("log = %q, want the stage lines", log)
	}
	if f.tr.Calls() == 0 || f.syn.Calls() == 0 {
		t.Error("the backends were not used")
	}

	// A second run repeats nothing.
	calls := f.tr.Calls() + f.syn.Calls()
	f.json(http.MethodPost, "/api/books/demo/run", map[string]any{"to": pipeline.StageSynth}, nil)
	f.wait("demo")
	if got := f.tr.Calls() + f.syn.Calls(); got != calls {
		t.Errorf("backend calls after the second run = %d, want %d", got, calls)
	}
}

func TestStopAndConflict(t *testing.T) {
	f := newFixture(t)
	f.upload("book.pdf", "")

	entered, release := make(chan struct{}, 8), make(chan struct{})
	f.tr.Fail = func(translate.Request) error {
		entered <- struct{}{}
		<-release
		return nil
	}
	if status := f.json(http.MethodPost, "/api/books/book/run", nil, nil); status != http.StatusAccepted {
		t.Fatalf("run: status %d", status)
	}
	<-entered // the run is inside the translate stage

	var d detailJSON
	f.json(http.MethodGet, "/api/books/book", nil, &d)
	if d.Job == nil || !d.Job.Running || !d.Running {
		t.Fatalf("job while running = %+v", d.Job)
	}
	var answer map[string]string
	if status := f.json(http.MethodPost, "/api/books/book/run", nil, &answer); status != http.StatusConflict || !strings.Contains(answer["error"], "already") {
		t.Errorf("second run: status %d, %v; want a conflict", status, answer)
	}

	if status := f.json(http.MethodPost, "/api/books/book/stop", nil, nil); status != http.StatusAccepted {
		t.Fatalf("stop: status %d", status)
	}
	close(release)
	d = f.wait("book")
	if !d.Job.Stopped || d.Job.Error != "" {
		t.Errorf("job after stop = %+v, want stopped without an error", d.Job)
	}
	if status := f.json(http.MethodPost, "/api/books/book/stop", nil, nil); status != http.StatusConflict {
		t.Errorf("stop with nothing running: status %d, want a conflict", status)
	}

	// The book is free again: the next run finishes the work.
	f.tr.Fail = nil
	f.json(http.MethodPost, "/api/books/book/run", map[string]any{"to": pipeline.StageTranslate}, nil)
	if d = f.wait("book"); d.Job.Error != "" || d.Job.Stopped {
		t.Errorf("run after a stop = %+v", d.Job)
	}
}

func TestRunFailureIsReported(t *testing.T) {
	f := newFixture(t)
	f.upload("book.pdf", "")
	f.tr.Fail = func(translate.Request) error { return fmt.Errorf("model is down") }
	f.json(http.MethodPost, "/api/books/book/run", nil, nil)
	d := f.wait("book")
	if !strings.Contains(d.Job.Error, pipeline.StageTranslate) || d.Job.Stopped {
		t.Errorf("job = %+v, want the failed stage named", d.Job)
	}
	if len(d.Failures) == 0 || !strings.Contains(d.Failures[0].Error, "model is down") {
		t.Errorf("failures = %+v", d.Failures)
	}
}

func TestBadRequests(t *testing.T) {
	f := newFixture(t)
	f.upload("book.pdf", "")
	tests := []struct {
		name, method, path string
		payload            any
		status             int
		want               string
	}{
		{"unknown book", http.MethodGet, "/api/books/nobody", nil, http.StatusNotFound, "nobody"},
		{"run of an unknown book", http.MethodPost, "/api/books/nobody/run", nil, http.StatusNotFound, ""},
		{"the upload folder is not a book", http.MethodGet, "/api/books/_pdf", nil, http.StatusNotFound, ""},
		{"unknown stage", http.MethodPost, "/api/books/book/run", map[string]any{"from": "polish"}, http.StatusBadRequest, "polish"},
		{"malformed body", http.MethodPost, "/api/books/book/run", "not an object", http.StatusBadRequest, "request body"},
		{"no such output", http.MethodGet, "/api/books/book/out/book.m4b", nil, http.StatusNotFound, "no output file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var answer map[string]string
			status := f.json(tt.method, tt.path, tt.payload, &answer)
			if status != tt.status {
				t.Errorf("status = %d, want %d", status, tt.status)
			}
			if !strings.Contains(answer["error"], tt.want) {
				t.Errorf("error = %q, want it to contain %q", answer["error"], tt.want)
			}
		})
	}
}

// Paths that try to leave the working directory never reach a file.
func TestPathsOutsideTheWorkdir(t *testing.T) {
	f := newFixture(t)
	f.upload("book.pdf", "")
	for _, path := range []string{
		"/api/books/..",
		"/api/books/%2e%2e",
		"/api/books/book/out/..%2Fmanifest.json",
		"/api/books/book/out/%2e%2e%2fconfig.yaml",
		"/api/books/..%2F..%2Fetc/out/passwd",
	} {
		status, data := f.do(http.MethodGet, path, nil, "")
		if status == http.StatusOK || strings.Contains(string(data), "translate") || strings.Contains(string(data), "sha256") {
			t.Errorf("GET %s: status %d, body %q", path, status, data)
		}
	}
}

func TestAddBookErrors(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name, file, slug, want string
	}{
		{"nothing chosen", "", "", "choose a PDF or EPUB"},
		{"not a book", "notes.txt", "", "not a PDF or EPUB"},
		{"bad folder name", "book.pdf", "../escape", "invalid folder name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, answer := f.upload(tt.file, tt.slug)
			if status != http.StatusBadRequest || !strings.Contains(answer["error"], tt.want) {
				t.Errorf("status %d, answer %v; want 400 with %q", status, answer, tt.want)
			}
		})
	}
	var books []bookJSON
	if f.json(http.MethodGet, "/api/books", nil, &books); len(books) != 0 {
		t.Errorf("books after failed adds = %+v", books)
	}

	// A PDF that is already on this computer is added by its path.
	pdf := filepath.Join(t.TempDir(), "local.pdf")
	os.WriteFile(pdf, []byte("%PDF-1.4 local"), 0o644)
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	form.WriteField("path", pdf)
	form.Close()
	if status, data := f.do(http.MethodPost, "/api/books", &body, form.FormDataContentType()); status != http.StatusCreated || !strings.Contains(string(data), `"slug":"local"`) {
		t.Errorf("add by path: status %d, body %s", status, data)
	}
}

func TestDownload(t *testing.T) {
	f := newFixture(t)
	f.upload("book.pdf", "")
	out := filepath.Join(f.server.Workdir, "book", "out")
	os.MkdirAll(out, 0o755)
	os.WriteFile(filepath.Join(out, "book.m4b"), []byte("audio"), 0o644)
	os.WriteFile(filepath.Join(out, "notes.txt"), []byte("not audio"), 0o644)

	var d detailJSON
	f.json(http.MethodGet, "/api/books/book", nil, &d)
	if len(d.Outputs) != 1 || d.Outputs[0] != "book.m4b" {
		t.Fatalf("outputs = %v, want only the audio file", d.Outputs)
	}
	if status, data := f.do(http.MethodGet, "/api/books/book/out/book.m4b", nil, ""); status != http.StatusOK || string(data) != "audio" {
		t.Errorf("download: status %d, body %q", status, data)
	}
	if status, _ := f.do(http.MethodGet, "/api/books/book/out/notes.txt", nil, ""); status != http.StatusNotFound {
		t.Errorf("download of another file: status %d, want 404", status)
	}
}

// A page of another site must not be able to start a run or reach the
// interface under another host name.
func TestGuard(t *testing.T) {
	f := newFixture(t)
	f.upload("book.pdf", "")

	request := func(method, path, host string, header bool) int {
		req, _ := http.NewRequest(method, f.http.URL+path, nil)
		if host != "" {
			req.Host = host
		}
		if header {
			req.Header.Set(RequestHeader, "1")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	if got := request(http.MethodPost, "/api/books/book/run", "", false); got != http.StatusForbidden {
		t.Errorf("POST without the header: status %d, want 403", got)
	}
	if got := request(http.MethodGet, "/api/books", "evil.example:8080", true); got != http.StatusForbidden {
		t.Errorf("GET under another host name: status %d, want 403", got)
	}
	for _, host := range []string{"localhost:8080", "127.0.0.1", "[::1]:8080"} {
		if got := request(http.MethodGet, "/api/books", host, false); got != http.StatusOK {
			t.Errorf("GET with Host %s: status %d, want 200", host, got)
		}
	}
	if got := request(http.MethodGet, "/", "", false); got != http.StatusOK {
		t.Errorf("the page: status %d", got)
	}
	var d detailJSON
	if f.json(http.MethodGet, "/api/books/book", nil, &d); d.Job != nil {
		t.Error("a rejected request started a run")
	}

	f.server.AnyHost = true
	if got := request(http.MethodGet, "/api/books", "nas.lan:8080", false); got != http.StatusOK {
		t.Errorf("AnyHost: status %d, want 200", got)
	}
}

func TestJobKeepsTheLastLines(t *testing.T) {
	j := &job{}
	for i := range logLines + 50 {
		fmt.Fprintf(j, "line %d\n", i)
	}
	j.Write([]byte("an unfinished "))
	j.Write([]byte("line\nand a tail"))
	lines := j.snapshot().Log
	if len(lines) != logLines || lines[len(lines)-1] != "an unfinished line" || lines[0] != "line 51" {
		t.Errorf("kept %d lines, first %q, last %q", len(lines), lines[0], lines[len(lines)-1])
	}
}

func TestServeStopsWithTheContext(t *testing.T) {
	f := newFixture(t)
	ln := httptest.NewUnstartedServer(nil).Listener
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.server.Serve(ctx, ln) }()

	res, err := http.Get("http://" + ln.Addr().String() + "/api/books")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d", res.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after the context was cancelled")
	}
}

func TestSettings(t *testing.T) {
	f := newFixture(t)
	f.upload("book.pdf", "")
	t.Setenv("B2A_TEST_UI_KEY", "secret")

	var d detailJSON
	f.json(http.MethodGet, "/api/books/book", nil, &d)
	if !d.Settings.Translate || d.Settings.SourceLang != "en" || d.Settings.TargetLang != "ru" || d.Settings.Model != "test-model" || d.Settings.KeySet {
		t.Fatalf("settings = %+v", d.Settings)
	}

	// Translation with another model and key variable.
	var saved settingsJSON
	status := f.json(http.MethodPost, "/api/books/book/settings", settingsJSON{
		Translate: true, SourceLang: "en", TargetLang: "de", Model: " big-model ",
		BaseURL: "https://api.example.com/v1", APIKeyEnv: "B2A_TEST_UI_KEY",
	}, &saved)
	if status != http.StatusOK || saved.Model != "big-model" || saved.TargetLang != "de" || !saved.KeySet || !saved.Translate {
		t.Fatalf("save: status %d, settings %+v", status, saved)
	}
	cfgPath := filepath.Join(f.server.Workdir, "book", "config.yaml")
	text, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(text), "secret") {
		t.Error("the key itself was written to the config")
	}
	// Settings the page does not show are kept.
	if cfg, err := config.Load(cfgPath); err != nil || cfg.Extract.OCRMinChars != 0 || cfg.Translate.BaseURL != "https://api.example.com/v1" {
		t.Errorf("config after the save = %+v, %v", cfg.Translate, err)
	}

	// Translation off: the book is read in its own language, without a model.
	status = f.json(http.MethodPost, "/api/books/book/settings", settingsJSON{Translate: false, SourceLang: "en", TargetLang: "whatever"}, &saved)
	if status != http.StatusOK || saved.Translate || saved.TargetLang != "en" || saved.Model != "big-model" {
		t.Fatalf("translation off: status %d, settings %+v", status, saved)
	}
	f.json(http.MethodPost, "/api/books/book/run", map[string]any{"to": pipeline.StageTranslate}, nil)
	d = f.wait("book")
	if d.Job.Error != "" || f.tr.Calls() != 0 {
		t.Errorf("run without translation: job %+v, translator calls %d", d.Job, f.tr.Calls())
	}
	if d.Settings.Translate {
		t.Errorf("settings after the run = %+v", d.Settings)
	}
}

func TestSettingsErrors(t *testing.T) {
	f := newFixture(t)
	f.upload("book.pdf", "")
	tests := []struct {
		name string
		req  any
		code int
		want string
	}{
		{"no model", settingsJSON{Translate: true, SourceLang: "en", TargetLang: "ru"}, http.StatusBadRequest, "name the model"},
		{"no target", settingsJSON{Translate: true, SourceLang: "en", Model: "m"}, http.StatusBadRequest, "language to translate into"},
		{"same languages", settingsJSON{Translate: true, SourceLang: "en", TargetLang: "en-US", Model: "m"}, http.StatusBadRequest, "is the same"},
		{"no source", settingsJSON{Translate: false}, http.StatusBadRequest, "source_lang"},
		{"bad address", settingsJSON{Translate: true, SourceLang: "en", TargetLang: "ru", Model: "m", BaseURL: "nowhere"}, http.StatusBadRequest, "translate.base_url"},
		{"malformed body", "text", http.StatusBadRequest, "request body"},
	}
	before, _ := os.ReadFile(filepath.Join(f.server.Workdir, "book", "config.yaml"))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var answer map[string]string
			status := f.json(http.MethodPost, "/api/books/book/settings", tt.req, &answer)
			if status != tt.code || !strings.Contains(answer["error"], tt.want) {
				t.Errorf("status %d, error %q; want %d with %q", status, answer["error"], tt.code, tt.want)
			}
		})
	}
	if after, _ := os.ReadFile(filepath.Join(f.server.Workdir, "book", "config.yaml")); string(after) != string(before) {
		t.Error("a rejected save changed the config")
	}
	var answer map[string]string
	if status := f.json(http.MethodPost, "/api/books/nobody/settings", settingsJSON{}, &answer); status != http.StatusNotFound {
		t.Errorf("unknown book: status %d", status)
	}

	// Not while the book is being processed.
	entered, release := make(chan struct{}, 8), make(chan struct{})
	f.tr.Fail = func(translate.Request) error { entered <- struct{}{}; <-release; return nil }
	f.json(http.MethodPost, "/api/books/book/run", nil, nil)
	<-entered
	status := f.json(http.MethodPost, "/api/books/book/settings", settingsJSON{Translate: false, SourceLang: "en"}, &answer)
	if status != http.StatusConflict || !strings.Contains(answer["error"], "stop the run") {
		t.Errorf("save during a run: status %d, %v", status, answer)
	}
	close(release)
	f.wait("book")
}

func TestSpeechSettings(t *testing.T) {
	f := newFixture(t)
	f.upload("book.pdf", "")

	var d detailJSON
	f.json(http.MethodGet, "/api/books/book", nil, &d)
	// The default: Piper without arguments, so the Russian voice would be downloaded.
	if d.Settings.TTSBackend != config.BackendPiper || d.Settings.TTSCommand != "piper" || d.Settings.TTSArgs != "" ||
		d.Settings.AutoVoice != "ru_RU-irina-medium" || len(d.Settings.Backends) != 2 {
		t.Fatalf("settings = %+v", d.Settings)
	}

	var saved settingsJSON
	status := f.json(http.MethodPost, "/api/books/book/settings", settingsJSON{
		Translate: true, SourceLang: "en", TargetLang: "ru", Model: "m",
		TTSBackend: config.BackendSilero, TTSCommand: " /opt/silero_tts.py ", TTSArgs: "--speaker   xenia",
	}, &saved)
	if status != http.StatusOK || saved.TTSBackend != config.BackendSilero || saved.TTSCommand != "/opt/silero_tts.py" ||
		saved.TTSArgs != "--speaker xenia" || saved.AutoVoice != "" {
		t.Fatalf("save: status %d, settings %+v", status, saved)
	}
	cfg, err := config.Load(filepath.Join(f.server.Workdir, "book", "config.yaml"))
	if err != nil || len(cfg.TTS.Args) != 2 || cfg.TTS.Args[1] != "xenia" {
		t.Errorf("tts in the config = %+v, %v", cfg.TTS, err)
	}

	// A request without the speech fields leaves the engine alone.
	f.json(http.MethodPost, "/api/books/book/settings", settingsJSON{Translate: false, SourceLang: "en"}, &saved)
	if saved.TTSBackend != config.BackendSilero || saved.TTSArgs != "--speaker xenia" {
		t.Errorf("engine after a save without speech fields = %+v", saved)
	}

	var answer map[string]string
	for name, req := range map[string]settingsJSON{
		"unknown backend": {SourceLang: "en", TTSBackend: "robot", TTSCommand: "x"},
		"no command":      {SourceLang: "en", TTSBackend: config.BackendPiper},
	} {
		if status := f.json(http.MethodPost, "/api/books/book/settings", req, &answer); status != http.StatusBadRequest {
			t.Errorf("%s: status %d, %v", name, status, answer)
		}
	}
}

func TestRetry(t *testing.T) {
	transient := errors.New("no data available")
	tests := []struct {
		name      string
		errs      []error // returned by the calls, in order; then nil
		wantCalls int
		wantErr   error
	}{
		{"first try succeeds", nil, 1, nil},
		{"a transient error is tried again", []error{transient, transient}, 3, nil},
		{"gives up after the last attempt", []error{transient, transient, transient, transient}, 4, transient},
		{"a missing book is not tried again", []error{fs.ErrNotExist}, 1, fs.ErrNotExist},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			err := retry(4, time.Millisecond, func() error {
				calls++
				if calls <= len(tt.errs) {
					return tt.errs[calls-1]
				}
				return nil
			})
			if calls != tt.wantCalls || !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Errorf("calls = %d, err = %v; want %d calls and %v", calls, err, tt.wantCalls, tt.wantErr)
			}
		})
	}
}
