package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleProfiles() Profiles {
	temp := 0.5
	return Profiles{
		Translate: TranslateProfiles{
			Default: "cloud",
			Profiles: map[string]TranslateProfile{
				"cloud": {BaseURL: "https://api.example.com/v1", Model: "big", APIKeyEnv: "B2A_KEY",
					Headers: map[string]string{"x-workspace": "w1"}, Workers: 6, Temperature: &temp},
				"local": {BaseURL: "http://localhost:11434/v1", Model: "small"},
			},
		},
		Speech: SpeechProfiles{
			Default: "silero",
			Profiles: map[string]SpeechProfile{
				"silero": {Backend: BackendSilero, Command: "/opt/silero_tts.py", Args: []string{"--speaker", "xenia"}, SegmentChars: 800},
				"piper":  {Backend: BackendPiper, Command: "piper", Voice: "en_US-lessac-medium"},
			},
		},
	}
}

func TestProfilesSaveAndLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "books") // created on demand
	if got, err := LoadProfiles(dir); err != nil || len(got.Translate.Profiles) != 0 || got.Speech.Default != "" {
		t.Fatalf("a directory without the file: %+v, %v", got, err)
	}
	if err := SaveProfiles(dir, sampleProfiles()); err != nil {
		t.Fatalf("SaveProfiles: %v", err)
	}
	got, err := LoadProfiles(dir)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	if got.Translate.Default != "cloud" || got.Translate.Profiles["cloud"].Headers["x-workspace"] != "w1" ||
		*got.Translate.Profiles["cloud"].Temperature != 0.5 || got.Speech.Profiles["silero"].Args[1] != "xenia" {
		t.Errorf("loaded = %+v", got)
	}
	if names := strings.Join(got.Speech.Names(), ","); names != "piper,silero" {
		t.Errorf("speech names = %s", names)
	}
	text, _ := os.ReadFile(ProfilesPath(dir))
	if !strings.HasPrefix(string(text), "# Translation and speech profiles") {
		t.Errorf("the file has no heading comment:\n%s", text)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.part")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestApplyProfiles(t *testing.T) {
	dir := t.TempDir()
	if err := SaveProfiles(dir, sampleProfiles()); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name               string
		trProfile, ttsProf string
		wantModel          string
		wantBackend        string
		wantTrUsed         string
		wantTTSUsed        string
	}{
		{"defaults for a book that names none", "", "", "big", BackendSilero, "cloud", "silero"},
		{"named profiles", "local", "piper", "small", BackendPiper, "local", "piper"},
		{"own settings", ProfileOwn, ProfileOwn, "book-model", BackendPiper, "", ""},
		{"own translation, default speech", ProfileOwn, "", "book-model", BackendSilero, "", "silero"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Translate.Model, cfg.Translate.Workers, cfg.Translate.ChunkChars = "book-model", 2, 2000
			cfg.Translate.Profile, cfg.TTS.Profile = tt.trProfile, tt.ttsProf
			if err := ApplyProfiles(&cfg, dir); err != nil {
				t.Fatalf("ApplyProfiles: %v", err)
			}
			if cfg.Translate.Model != tt.wantModel || cfg.TTS.Backend != tt.wantBackend ||
				cfg.Translate.ProfileUsed != tt.wantTrUsed || cfg.TTS.ProfileUsed != tt.wantTTSUsed {
				t.Errorf("model %q, backend %q, used %q/%q", cfg.Translate.Model, cfg.TTS.Backend, cfg.Translate.ProfileUsed, cfg.TTS.ProfileUsed)
			}
			// Settings a profile does not hold stay the book's.
			if cfg.Translate.ChunkChars != 2000 {
				t.Errorf("chunk_chars = %d, want the book's 2000", cfg.Translate.ChunkChars)
			}
		})
	}

	// What a profile carries, in full.
	cfg := Default()
	cfg.Translate.Headers = map[string]string{"old": "1"}
	if err := ApplyProfiles(&cfg, dir); err != nil {
		t.Fatal(err)
	}
	if cfg.Translate.Workers != 6 || cfg.Translate.Temperature != 0.5 || cfg.Translate.APIKeyEnv != "B2A_KEY" ||
		len(cfg.Translate.Headers) != 1 || cfg.Translate.Headers["x-workspace"] != "w1" ||
		cfg.TTS.SegmentChars != 800 || cfg.TTS.Command != "/opt/silero_tts.py" {
		t.Errorf("translate = %+v\ntts = %+v", cfg.Translate, cfg.TTS)
	}
	cfg = Default()
	cfg.Translate.Profile = "local" // no key, no workers of its own
	ApplyProfiles(&cfg, dir)
	if cfg.Translate.APIKeyEnv != "" || cfg.Translate.Workers != Default().Translate.Workers {
		t.Errorf("local profile: key %q, workers %d", cfg.Translate.APIKeyEnv, cfg.Translate.Workers)
	}
}

func TestApplyProfilesErrors(t *testing.T) {
	dir := t.TempDir()
	if err := SaveProfiles(dir, sampleProfiles()); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.Translate.Profile = "gone"
	if err := ApplyProfiles(&cfg, dir); err == nil || !strings.Contains(err.Error(), `"gone"`) || !strings.Contains(err.Error(), "cloud, local") {
		t.Errorf("unknown translation profile: %v", err)
	}
	cfg = Default()
	cfg.TTS.Profile = "gone"
	if err := ApplyProfiles(&cfg, dir); err == nil || !strings.Contains(err.Error(), "tts.profile") {
		t.Errorf("unknown speech profile: %v", err)
	}

	// No file and no default: a book keeps its settings, a named profile is an error.
	empty := t.TempDir()
	cfg = Default()
	cfg.Translate.Model = "book-model"
	if err := ApplyProfiles(&cfg, empty); err != nil || cfg.Translate.Model != "book-model" || cfg.Translate.ProfileUsed != "" {
		t.Errorf("no profiles: %v, %+v", err, cfg.Translate)
	}
	cfg.Translate.Profile = "cloud"
	if err := ApplyProfiles(&cfg, empty); err == nil || !strings.Contains(err.Error(), "profiles: none") {
		t.Errorf("named profile without a file: %v", err)
	}

	os.WriteFile(ProfilesPath(empty), []byte("translate:\n  profiles: [\n"), 0o644)
	if _, err := LoadProfiles(empty); err == nil || !strings.Contains(err.Error(), "parsing YAML") {
		t.Errorf("broken file: %v", err)
	}
	os.WriteFile(ProfilesPath(empty), []byte("translate:\n  unknown_key: 1\n"), 0o644)
	if _, err := LoadProfiles(empty); err == nil {
		t.Error("an unknown key was accepted")
	}
}

func TestProfilesValidate(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Profiles)
		want   string
	}{
		{"no model", func(p *Profiles) { p.Translate.Profiles["x"] = TranslateProfile{BaseURL: "http://h/v1"} }, "translate.profiles.x.model"},
		{"no address", func(p *Profiles) { p.Translate.Profiles["x"] = TranslateProfile{Model: "m"} }, "translate.profiles.x.base_url"},
		{"bad address", func(p *Profiles) { p.Translate.Profiles["x"] = TranslateProfile{Model: "m", BaseURL: "nowhere"} }, "translate.base_url"},
		{"reserved name", func(p *Profiles) {
			p.Translate.Profiles[ProfileOwn] = TranslateProfile{Model: "m", BaseURL: "http://h/v1"}
		}, "invalid profile name"},
		{"name with a slash", func(p *Profiles) { p.Speech.Profiles["a/b"] = SpeechProfile{Backend: BackendPiper, Command: "piper"} }, "invalid profile name"},
		{"default that does not exist", func(p *Profiles) { p.Translate.Default = "gone" }, "translate.default"},
		{"speech default that does not exist", func(p *Profiles) { p.Speech.Default = "gone" }, "speech.default"},
		{"speech without a command", func(p *Profiles) { p.Speech.Profiles["x"] = SpeechProfile{Backend: BackendPiper} }, "speech.profiles.x.command"},
		{"unknown engine", func(p *Profiles) { p.Speech.Profiles["x"] = SpeechProfile{Backend: "robot", Command: "r"} }, "tts.backend"},
		{"bad header name", func(p *Profiles) {
			p.Translate.Profiles["x"] = TranslateProfile{Model: "m", BaseURL: "http://h/v1", Headers: map[string]string{"a b": "c"}}
		}, "translate.headers"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := sampleProfiles()
			tt.change(&p)
			err := p.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Validate = %v, want an error containing %q", err, tt.want)
			}
			if SaveProfiles(t.TempDir(), p) == nil {
				t.Error("invalid profiles were saved")
			}
		})
	}
	if err := sampleProfiles().Validate(); err != nil {
		t.Errorf("the sample is invalid: %v", err)
	}
	for name, want := range map[string]bool{"cloud": true, "My API 2": true, "own": false, "": false, " x": false, "a/b": false, "x ": false} {
		if got := ValidProfileName(name); got != want {
			t.Errorf("ValidProfileName(%q) = %v, want %v", name, got, want)
		}
	}
}
