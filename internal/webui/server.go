// Package webui serves the browser interface of book2audio: the list of
// books in a working directory, the progress of each one, and starting and
// stopping the pipeline.
package webui

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FulgerX2007/book2audio/internal/config"
	"github.com/FulgerX2007/book2audio/internal/pipeline"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

//go:embed index.html
var indexHTML []byte

// RequestHeader must be present on every request that changes something. A
// page of another site cannot add it without a CORS preflight, which this
// server never answers, so such a page cannot start a run or upload a file.
const RequestHeader = "X-Book2audio"

// uploadsDir is the folder inside the working directory where uploaded
// books are kept.
const uploadsDir = "_pdf"

// maxUpload limits the size of an uploaded book.
const maxUpload = 2 << 30

// Server is the browser interface over one working directory.
type Server struct {
	Workdir    string        // directory of the book working folders
	ConfigPath string        // config copied into a new book; empty: defaults
	Deps       pipeline.Deps // backends used by the runs
	Log        *slog.Logger  // log of the server itself
	// AnyHost turns off the check of the Host header. It is needed when the
	// server listens on a non-loopback address; by default only requests
	// addressed to localhost are served, which defeats DNS rebinding.
	AnyHost bool

	mu   sync.Mutex
	jobs map[string]*job // by slug
	wg   sync.WaitGroup
}

// slugRe is the form of a working folder name accepted in a URL.
var slugRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Handler returns the HTTP handler of the interface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(indexHTML)
	})
	mux.HandleFunc("GET /api/books", s.listBooks)
	mux.HandleFunc("POST /api/books", s.addBook)
	mux.HandleFunc("GET /api/books/{slug}", s.bookDetail)
	mux.HandleFunc("POST /api/books/{slug}/run", s.startRun)
	mux.HandleFunc("POST /api/books/{slug}/stop", s.stopRun)
	mux.HandleFunc("POST /api/books/{slug}/settings", s.saveSettings)
	mux.HandleFunc("GET /api/books/{slug}/out/{name}", s.download)
	return s.guard(mux)
}

// guard rejects requests that a page of another site could have made.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.AnyHost && !loopbackHost(r.Host) {
			http.Error(w, "this interface answers only to localhost", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get(RequestHeader) == "" {
			http.Error(w, "the "+RequestHeader+" header is required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loopbackHost reports whether a Host header names this machine.
func loopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Close stops every running job and waits for it to release its book.
func (s *Server) Close() {
	s.mu.Lock()
	for _, j := range s.jobs {
		j.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// --- JSON shapes ---

type stageJSON struct {
	Name   string `json:"name"`
	Units  string `json:"units"`
	Total  int    `json:"total"`
	Done   int    `json:"done"`
	Failed int    `json:"failed"`
}

type chapterJSON struct {
	N              int    `json:"n"`
	Title          string `json:"title"`
	Chunks         int    `json:"chunks"`
	ChunksDone     int    `json:"chunksDone"`
	ChunksFailed   int    `json:"chunksFailed"`
	Translated     bool   `json:"translated"`
	Segments       int    `json:"segments"`
	SegmentsDone   int    `json:"segmentsDone"`
	SegmentsFailed int    `json:"segmentsFailed"`
	Audio          bool   `json:"audio"`
}

type failureJSON struct {
	Stage   string `json:"stage"`
	Chapter string `json:"chapter"`
	Error   string `json:"error"`
	Out     string `json:"out"`
}

type bookJSON struct {
	Slug    string      `json:"slug"`
	Title   string      `json:"title"`
	Author  string      `json:"author"`
	PDF     string      `json:"pdf"`
	Pages   int         `json:"pages"`
	Running bool        `json:"running"`
	Error   string      `json:"error,omitempty"` // the folder could not be read
	Stages  []stageJSON `json:"stages"`
}

// settingsJSON are the book settings that can be changed in the browser.
type settingsJSON struct {
	// Translate is false for a book read in its own language: the target
	// language is then the source language and no model is used.
	Translate  bool   `json:"translate"`
	SourceLang string `json:"sourceLang"`
	TargetLang string `json:"targetLang"`
	Model      string `json:"model"`
	BaseURL    string `json:"baseUrl"`
	APIKeyEnv  string `json:"apiKeyEnv"`
	// Headers are the extra request headers of the translation server, one
	// "name: value" per line: a workspace or project ID, for example.
	Headers string `json:"headers"`
	// KeySet tells whether the variable named by APIKeyEnv has a value in
	// the environment of this server. The key itself never leaves it.
	KeySet bool `json:"keySet"`

	// The speech engine: "piper" or "silero", its command and arguments
	// (separated by spaces). Piper without arguments downloads the voice of
	// the audiobook's language.
	TTSBackend string   `json:"ttsBackend"`
	TTSCommand string   `json:"ttsCommand"`
	TTSArgs    string   `json:"ttsArgs"`
	Backends   []string `json:"backends"`  // the engines to choose from
	AutoVoice  string   `json:"autoVoice"` // the Piper voice that would be downloaded, if any
}

func settingsOf(cfg config.Config) settingsJSON {
	return settingsJSON{
		Translate:  !cfg.SameLanguage(),
		SourceLang: cfg.SourceLang,
		TargetLang: cfg.TargetLang,
		Model:      cfg.Translate.Model,
		BaseURL:    cfg.Translate.BaseURL,
		APIKeyEnv:  cfg.Translate.APIKeyEnv,
		Headers:    formatHeaders(cfg.Translate.Headers),
		KeySet:     cfg.Translate.APIKeyEnv != "" && os.Getenv(cfg.Translate.APIKeyEnv) != "",
		TTSBackend: cfg.TTS.Backend,
		TTSCommand: cfg.TTS.Command,
		TTSArgs:    strings.Join(cfg.TTS.Args, " "),
		Backends:   []string{config.BackendPiper, config.BackendSilero},
		AutoVoice:  autoVoice(cfg),
	}
}

// formatHeaders renders headers one "name: value" per line, sorted.
func formatHeaders(headers map[string]string) string {
	lines := make([]string, 0, len(headers))
	for name, value := range headers {
		lines = append(lines, name+": "+value)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// parseHeaders reads "name: value" lines; blank lines are skipped.
func parseHeaders(text string) (map[string]string, error) {
	headers := map[string]string{}
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || name == "" || value == "" {
			return nil, fmt.Errorf("extra header %q: write it as name: value", line)
		}
		headers[name] = value
	}
	return headers, nil
}

// autoVoice names the Piper voice that the synth stage would download for
// this config, or "" when it would not download one.
func autoVoice(cfg config.Config) string {
	if cfg.AutoVoice() {
		return cfg.PiperVoice()
	}
	return ""
}

type detailJSON struct {
	bookJSON
	Settings   settingsJSON  `json:"settings"`
	Dir        string        `json:"dir"`
	ConfigPath string        `json:"configPath"`
	SourceLang string        `json:"sourceLang"`
	TargetLang string        `json:"targetLang"`
	AllStages  []string      `json:"allStages"`
	Chapters   []chapterJSON `json:"chapters"`
	Failures   []failureJSON `json:"failures"`
	Edited     []string      `json:"edited"`
	Outputs    []string      `json:"outputs"`
	Job        *jobJSON      `json:"job"`
}

// --- handlers ---

func (s *Server) listBooks(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.Workdir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	books := []bookJSON{}
	for _, e := range entries {
		if !e.IsDir() || !slugRe.MatchString(e.Name()) {
			continue
		}
		dir := filepath.Join(s.Workdir, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
			continue // not a book
		}
		ws, st, err := s.read(e.Name())
		if err != nil {
			books = append(books, bookJSON{Slug: e.Name(), Title: e.Name(), Error: err.Error()})
			continue
		}
		books = append(books, s.book(e.Name(), ws, st))
	}
	writeJSON(w, http.StatusOK, books)
}

func (s *Server) bookDetail(w http.ResponseWriter, r *http.Request) {
	slug, ok := s.slug(w, r)
	if !ok {
		return
	}
	ws, st, err := s.read(slug)
	if err != nil {
		writeError(w, statusOf(err), err)
		return
	}
	d := detailJSON{
		bookJSON:   s.book(slug, ws, st),
		Settings:   settingsOf(ws.Config),
		Dir:        ws.Dir,
		ConfigPath: ws.ConfigPath(),
		SourceLang: ws.Config.SourceLang,
		TargetLang: ws.Config.TargetLang,
		AllStages:  pipeline.Stages,
		Chapters:   []chapterJSON{},
		Failures:   []failureJSON{},
		Edited:     append([]string{}, st.Edited...),
		Outputs:    outputs(ws),
	}
	for _, c := range st.Chapters {
		d.Chapters = append(d.Chapters, chapterJSON{
			N: c.N, Title: c.Title,
			Chunks: c.Chunks, ChunksDone: c.ChunksDone, ChunksFailed: c.ChunksFailed, Translated: c.Translated,
			Segments: c.Segments, SegmentsDone: c.SegmentsDone, SegmentsFailed: c.SegmentsFailed, Audio: c.Audio,
		})
	}
	for _, f := range st.Failures {
		d.Failures = append(d.Failures, failureJSON{Stage: f.Stage, Chapter: f.Chapter, Error: f.Error, Out: f.Out})
	}
	if j := s.job(slug); j != nil {
		snap := j.snapshot()
		d.Job = &snap
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	slug, ok := s.slug(w, r)
	if !ok {
		return
	}
	var req struct {
		From  string `json:"from"`
		To    string `json:"to"`
		Force bool   `json:"force"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("request body: %w", err))
		return
	}
	for _, stage := range []string{req.From, req.To} {
		if stage != "" && !slices.Contains(pipeline.Stages, stage) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("unknown stage %q", stage))
			return
		}
	}
	if _, _, err := s.read(slug); err != nil {
		writeError(w, statusOf(err), err)
		return
	}
	if err := s.start(slug, pipeline.RunOptions{From: req.From, To: req.To}, req.Force); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (s *Server) stopRun(w http.ResponseWriter, r *http.Request) {
	slug, ok := s.slug(w, r)
	if !ok {
		return
	}
	j := s.job(slug)
	if j == nil || !j.snapshot().Running {
		writeError(w, http.StatusConflict, errors.New("the book is not being processed by this interface"))
		return
	}
	j.cancel()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "stopping"})
}

// saveSettings writes the translation settings of a book into its
// config.yaml; the rest of the file, comments included, is kept.
func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	slug, ok := s.slug(w, r)
	if !ok {
		return
	}
	var req settingsJSON
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("request body: %w", err))
		return
	}
	ws, _, err := s.read(slug)
	if err != nil {
		writeError(w, statusOf(err), err)
		return
	}
	if j := s.job(slug); j != nil && j.snapshot().Running {
		writeError(w, http.StatusConflict, errors.New("the book is being processed: stop the run before changing its settings"))
		return
	}

	source, target := strings.TrimSpace(req.SourceLang), strings.TrimSpace(req.TargetLang)
	set := map[string]any{"source_lang": source}
	if backend := strings.TrimSpace(req.TTSBackend); backend != "" {
		// A request without the speech fields leaves the engine as it is.
		command := strings.TrimSpace(req.TTSCommand)
		if command == "" {
			writeError(w, http.StatusBadRequest, errors.New("name the command that runs the speech engine"))
			return
		}
		set["tts.backend"] = backend
		set["tts.command"] = command
		set["tts.args"] = strings.Fields(req.TTSArgs)
	}
	if req.Translate {
		model := strings.TrimSpace(req.Model)
		probe := config.Default()
		probe.SourceLang, probe.TargetLang = source, target
		switch {
		case target == "":
			err = errors.New("choose the language to translate into")
		case probe.SameLanguage():
			err = errors.New("the language to translate into is the same as the language of the book: choose another one or turn translation off")
		case model == "":
			err = errors.New("name the model that translates")
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		set["target_lang"] = target
		set["translate.model"] = model
		// An empty address is never valid: it means "leave it as it is".
		if base := strings.TrimSpace(req.BaseURL); base != "" {
			set["translate.base_url"] = base
		}
		set["translate.api_key_env"] = strings.TrimSpace(req.APIKeyEnv)
		headers, err := parseHeaders(req.Headers)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		set["translate.headers"] = headers
	} else {
		// A book read in its own language; the model settings stay as they are.
		set["target_lang"] = source
	}

	path := ws.ConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	updated, err := config.UpdateYAML(data, set)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := workspace.WriteFileAtomic(path, updated, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.Log.Info("settings saved", "book", slug, "translate", req.Translate)
	cfg, err := config.Load(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, settingsOf(cfg))
}

func (s *Server) addBook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("upload: %w", err))
		return
	}
	defer r.MultipartForm.RemoveAll()

	slug := strings.TrimSpace(r.FormValue("slug"))
	if slug != "" && !slugRe.MatchString(slug) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid folder name %q: use letters, digits, dots, dashes and underscores", slug))
		return
	}

	pdf := strings.TrimSpace(r.FormValue("path"))
	if file, header, err := r.FormFile("file"); err == nil {
		defer file.Close()
		if pdf, err = s.saveUpload(file, header.Filename); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	} else if !errors.Is(err, http.ErrMissingFile) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("upload: %w", err))
		return
	}
	if pdf == "" {
		writeError(w, http.StatusBadRequest, errors.New("choose a PDF or EPUB file or give its path on this computer"))
		return
	}

	ws, err := workspace.Init(pdf, workspace.InitOptions{ConfigPath: s.ConfigPath, Workdir: s.Workdir, Slug: slug})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.Log.Info("book added", "dir", ws.Dir)
	writeJSON(w, http.StatusCreated, map[string]string{"slug": filepath.Base(ws.Dir), "configPath": ws.ConfigPath()})
}

// saveUpload stores an uploaded book in the working directory and returns its
// path. The file appears under its name only when it is complete.
func (s *Server) saveUpload(src io.Reader, name string) (string, error) {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if ext := strings.ToLower(filepath.Ext(name)); (ext != ".pdf" && ext != ".epub") || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("upload: %q is not a PDF or EPUB file", name)
	}
	dir := filepath.Join(s.Workdir, uploadsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, name+".*.part")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return "", fmt.Errorf("upload: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	target := filepath.Join(dir, name)
	if err := os.Rename(tmp.Name(), target); err != nil {
		return "", err
	}
	return target, nil
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	slug, ok := s.slug(w, r)
	if !ok {
		return
	}
	ws, _, err := s.read(slug)
	if err != nil {
		writeError(w, statusOf(err), err)
		return
	}
	name := r.PathValue("name")
	if !slices.Contains(outputs(ws), name) {
		writeError(w, http.StatusNotFound, fmt.Errorf("no output file %q", name))
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	http.ServeFile(w, r, filepath.Join(ws.OutDir(), name))
}

// --- helpers ---

// slug returns the validated book name of a request.
func (s *Server) slug(w http.ResponseWriter, r *http.Request) (string, bool) {
	slug := r.PathValue("slug")
	if !slugRe.MatchString(slug) || slug == uploadsDir {
		writeError(w, http.StatusNotFound, fmt.Errorf("no book %q", slug))
		return "", false
	}
	return slug, true
}

// read opens a book without locking it and reads its state. That is safe
// while a run is in progress: the manifest is replaced atomically.
//
// On some file systems it is not quite atomic for a reader: on a Windows
// drive mounted in WSL a read that meets the replacement fails ("no data
// available") or sees half a file. Such a read is simply tried again.
func (s *Server) read(slug string) (*workspace.Workspace, pipeline.Status, error) {
	var ws *workspace.Workspace
	var st pipeline.Status
	err := retry(readAttempts, readDelay, func() error {
		var err error
		if ws, err = workspace.Open(filepath.Join(s.Workdir, slug), workspace.OpenOptions{}); err != nil {
			return err
		}
		st, err = pipeline.ReadStatus(ws)
		return err
	})
	return ws, st, err
}

const (
	readAttempts = 6
	readDelay    = 40 * time.Millisecond
)

// retry calls fn until it succeeds, up to attempts times with a pause in
// between. A book that does not exist is not worth another try.
func retry(attempts int, delay time.Duration, fn func() error) error {
	var err error
	for i := range attempts {
		if i > 0 {
			time.Sleep(delay)
		}
		if err = fn(); err == nil || statusOf(err) == http.StatusNotFound {
			return err
		}
	}
	return err
}

func (s *Server) book(slug string, ws *workspace.Workspace, st pipeline.Status) bookJSON {
	b := bookJSON{
		Slug:   slug,
		Title:  firstNonEmpty(ws.Config.Book.Title, ws.Source.Title, slug),
		Author: firstNonEmpty(ws.Config.Book.Author, ws.Source.Author),
		PDF:    ws.Source.Path,
		Pages:  ws.Source.Pages,
		Stages: []stageJSON{},
	}
	if j := s.job(slug); j != nil {
		b.Running = j.snapshot().Running
	}
	for _, stage := range st.Stages {
		b.Stages = append(b.Stages, stageJSON{
			Name: stage.Name, Units: stage.What,
			Total: stage.Counts.Total, Done: stage.Counts.Done, Failed: stage.Counts.Failed,
		})
	}
	return b
}

// outputs lists the finished audio files of a book.
func outputs(ws *workspace.Workspace) []string {
	names := []string{}
	entries, err := os.ReadDir(ws.OutDir())
	if err != nil {
		return names
	}
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if e.Type().IsRegular() && (ext == ".m4b" || ext == ".mp3") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func statusOf(err error) int {
	if errors.Is(err, workspace.ErrNotInitialized) || errors.Is(err, fs.ErrNotExist) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// Serve runs the interface on the listener until ctx is cancelled, then
// stops the running jobs.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: s.Handler()}
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	if err != nil {
		return err
	}
	<-done
	s.Close()
	return nil
}
