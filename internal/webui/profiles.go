package webui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/FulgerX2007/book2audio/internal/config"
	"github.com/FulgerX2007/book2audio/internal/pipeline"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

// The two kinds of shared profiles, as they appear in URLs.
const (
	kindTranslate = "translate"
	kindSpeech    = "speech"
)

// translateProfileJSON is a translation profile as shown in the browser.
type translateProfileJSON struct {
	Name      string `json:"name"`
	Model     string `json:"model"`
	BaseURL   string `json:"baseUrl"`
	APIKeyEnv string `json:"apiKeyEnv"`
	Headers   string `json:"headers"` // one "name: value" per line
	Workers   int    `json:"workers"` // 0: the book's own value
	Default   bool   `json:"default"`
	KeySet    bool   `json:"keySet"`
	Books     int    `json:"books"` // books that use the profile
}

// speechProfileJSON is a speech profile as shown in the browser.
type speechProfileJSON struct {
	Name         string `json:"name"`
	Backend      string `json:"backend"`
	Command      string `json:"command"`
	Args         string `json:"args"` // separated by spaces
	Voice        string `json:"voice"`
	SegmentChars int    `json:"segmentChars"` // 0: the book's own value
	Default      bool   `json:"default"`
	Books        int    `json:"books"`
}

type profilesJSON struct {
	Path      string                 `json:"path"`
	Backends  []string               `json:"backends"`
	Translate []translateProfileJSON `json:"translate"`
	Speech    []speechProfileJSON    `json:"speech"`
}

// bookChoice is what one book says about profiles in its config.yaml.
type bookChoice struct {
	slug, path          string
	translate, speech   string // translate.profile and tts.profile as written
	translated, synthed bool   // the book already has translated chunks / audio segments
}

// bookChoices reads the profile choices of every book of the directory.
// Books that cannot be read are skipped.
func (s *Server) bookChoices() []bookChoice {
	entries, _ := os.ReadDir(s.Workdir)
	var out []bookChoice
	for _, e := range entries {
		if !e.IsDir() || !slugRe.MatchString(e.Name()) {
			continue
		}
		dir := filepath.Join(s.Workdir, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
			continue
		}
		path := filepath.Join(dir, "config.yaml")
		cfg, err := config.Load(path)
		if err != nil {
			continue
		}
		c := bookChoice{slug: e.Name(), path: path,
			translate: strings.TrimSpace(cfg.Translate.Profile), speech: strings.TrimSpace(cfg.TTS.Profile)}
		if m, err := workspace.LoadManifest(dir); err == nil {
			c.translated = m.Counts(pipeline.StageTranslate).Done > 0
			c.synthed = m.Counts(pipeline.StageSynth).Done > 0
		}
		out = append(out, c)
	}
	return out
}

func (s *Server) profilesView(p config.Profiles) profilesJSON {
	choices := s.bookChoices()
	view := profilesJSON{
		Path:      config.ProfilesPath(s.Workdir),
		Backends:  []string{config.BackendPiper, config.BackendSilero},
		Translate: []translateProfileJSON{},
		Speech:    []speechProfileJSON{},
	}
	for _, name := range p.Translate.Names() {
		prof := p.Translate.Profiles[name]
		users := 0
		for _, c := range choices {
			if p.TranslateFor(c.translate) == name {
				users++
			}
		}
		view.Translate = append(view.Translate, translateProfileJSON{
			Name: name, Model: prof.Model, BaseURL: prof.BaseURL, APIKeyEnv: prof.APIKeyEnv,
			Headers: formatHeaders(prof.Headers), Workers: prof.Workers,
			Default: p.Translate.Default == name, KeySet: keySet(prof.APIKeyEnv), Books: users,
		})
	}
	for _, name := range p.Speech.Names() {
		prof := p.Speech.Profiles[name]
		users := 0
		for _, c := range choices {
			if p.SpeechFor(c.speech) == name {
				users++
			}
		}
		view.Speech = append(view.Speech, speechProfileJSON{
			Name: name, Backend: prof.Backend, Command: prof.Command, Args: strings.Join(prof.Args, " "),
			Voice: prof.Voice, SegmentChars: prof.SegmentChars, Default: p.Speech.Default == name, Books: users,
		})
	}
	return view
}

func (s *Server) listProfiles(w http.ResponseWriter, r *http.Request) {
	p, err := config.LoadProfiles(s.Workdir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.profilesView(p))
}

// saveProfile creates or replaces one profile. The first profile of a kind
// becomes the default.
func (s *Server) saveProfile(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind != kindTranslate && kind != kindSpeech {
		writeError(w, http.StatusNotFound, fmt.Errorf("no profiles of kind %q", kind))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.profilesMu.Lock()
	defer s.profilesMu.Unlock()
	p, err := config.LoadProfiles(s.Workdir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	oldTranslate, oldSpeech := p.Translate.Default, p.Speech.Default

	var name string
	if kind == kindTranslate {
		var req translateProfileJSON
		if err := json.Unmarshal(body, &req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("request body: %w", err))
			return
		}
		headers, err := parseHeaders(req.Headers)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		name = strings.TrimSpace(req.Name)
		prof := p.Translate.Profiles[name] // keeps the fields the page does not show
		prof.Model, prof.BaseURL = strings.TrimSpace(req.Model), strings.TrimSpace(req.BaseURL)
		prof.APIKeyEnv, prof.Headers, prof.Workers = strings.TrimSpace(req.APIKeyEnv), headers, req.Workers
		if len(headers) == 0 {
			prof.Headers = nil
		}
		if p.Translate.Profiles == nil {
			p.Translate.Profiles = map[string]config.TranslateProfile{}
		}
		first := len(p.Translate.Profiles) == 0
		p.Translate.Profiles[name] = prof
		p.Translate.Default = newDefault(p.Translate.Default, name, req.Default || first)
	} else {
		var req speechProfileJSON
		if err := json.Unmarshal(body, &req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("request body: %w", err))
			return
		}
		name = strings.TrimSpace(req.Name)
		prof := p.Speech.Profiles[name]
		prof.Backend, prof.Command = strings.TrimSpace(req.Backend), strings.TrimSpace(req.Command)
		prof.Args, prof.Voice, prof.SegmentChars = strings.Fields(req.Args), strings.TrimSpace(req.Voice), req.SegmentChars
		if p.Speech.Profiles == nil {
			p.Speech.Profiles = map[string]config.SpeechProfile{}
		}
		first := len(p.Speech.Profiles) == 0
		p.Speech.Profiles[name] = prof
		p.Speech.Default = newDefault(p.Speech.Default, name, req.Default || first)
	}
	if !config.ValidProfileName(name) {
		writeError(w, http.StatusBadRequest, errors.New("give the profile a name: letters, digits, spaces, dots and dashes"))
		return
	}
	if err := p.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// A new default must not silently change books that are already
	// translated or voiced: they keep their own settings until asked.
	pinned := s.pinStarted(oldTranslate != p.Translate.Default, oldSpeech != p.Speech.Default)
	if err := config.SaveProfiles(s.Workdir, p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.Log.Info("profile saved", "kind", kind, "name", name)
	writeJSON(w, http.StatusOK, map[string]any{"profiles": s.profilesView(p), "pinned": pinned})
}

// newDefault returns the default after a profile was saved: the profile
// when it asks to be the default, nothing when it was and no longer is.
func newDefault(current, name string, wants bool) string {
	switch {
	case wants:
		return name
	case current == name:
		return ""
	}
	return current
}

// pinStarted writes "own" as the profile of the books that follow the
// default and already have results of that stage, for the kinds whose
// default is about to change. It returns the names of the changed books.
func (s *Server) pinStarted(translate, speech bool) []string {
	pinned := []string{}
	if !translate && !speech {
		return pinned
	}
	for _, c := range s.bookChoices() {
		set := map[string]any{}
		if translate && c.translate == "" && c.translated {
			set["translate.profile"] = config.ProfileOwn
		}
		if speech && c.speech == "" && c.synthed {
			set["tts.profile"] = config.ProfileOwn
		}
		if len(set) == 0 {
			continue
		}
		data, err := os.ReadFile(c.path)
		if err != nil {
			continue
		}
		updated, err := config.UpdateYAML(data, set)
		if err != nil {
			s.Log.Warn("book not pinned to its own settings", "book", c.slug, "err", err)
			continue
		}
		if err := workspace.WriteFileAtomic(c.path, updated, 0o644); err != nil {
			s.Log.Warn("book not pinned to its own settings", "book", c.slug, "err", err)
			continue
		}
		pinned = append(pinned, c.slug)
	}
	sort.Strings(pinned)
	return pinned
}

// deleteProfile removes a profile that no book uses, by name or as the
// default: a book must not lose its settings behind its back.
func (s *Server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind != kindTranslate && kind != kindSpeech {
		writeError(w, http.StatusNotFound, fmt.Errorf("no profiles of kind %q", kind))
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("request body: %w", err))
		return
	}
	s.profilesMu.Lock()
	defer s.profilesMu.Unlock()
	p, err := config.LoadProfiles(s.Workdir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	var users []string
	for _, c := range s.bookChoices() {
		if (kind == kindTranslate && p.TranslateFor(c.translate) == req.Name) || (kind == kindSpeech && p.SpeechFor(c.speech) == req.Name) {
			users = append(users, c.slug)
		}
	}
	if len(users) > 0 {
		writeError(w, http.StatusConflict, fmt.Errorf("the profile is used by: %s. Choose another profile for these books first", strings.Join(users, ", ")))
		return
	}

	found := false
	if kind == kindTranslate {
		if _, found = p.Translate.Profiles[req.Name]; found {
			delete(p.Translate.Profiles, req.Name)
			if p.Translate.Default == req.Name {
				p.Translate.Default = ""
			}
		}
	} else {
		if _, found = p.Speech.Profiles[req.Name]; found {
			delete(p.Speech.Profiles, req.Name)
			if p.Speech.Default == req.Name {
				p.Speech.Default = ""
			}
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("no profile %q", req.Name))
		return
	}
	if err := config.SaveProfiles(s.Workdir, p); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.Log.Info("profile deleted", "kind", kind, "name", req.Name)
	writeJSON(w, http.StatusOK, map[string]any{"profiles": s.profilesView(p), "pinned": []string{}})
}
