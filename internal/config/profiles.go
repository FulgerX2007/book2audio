package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ProfilesFile is the name of the file, in the directory of the book
// working folders, that holds the translation and speech profiles shared by
// the books.
const ProfilesFile = "profiles.yaml"

// ProfileOwn as translate.profile or tts.profile makes a book use the
// settings of its own config.yaml even when the profiles file has a default.
const ProfileOwn = "own"

// TranslateProfile is a named connection to a translation server.
type TranslateProfile struct {
	BaseURL   string            `yaml:"base_url"`
	Model     string            `yaml:"model"`
	APIKeyEnv string            `yaml:"api_key_env"`
	Headers   map[string]string `yaml:"headers,omitempty"`
	// Workers and Temperature replace the book's values only when set.
	Workers     int      `yaml:"workers,omitempty"`
	Temperature *float64 `yaml:"temperature,omitempty"`
}

// SpeechProfile is a named speech engine with its voice.
type SpeechProfile struct {
	Backend string   `yaml:"backend"`
	Command string   `yaml:"command"`
	Args    []string `yaml:"args,flow"`
	Voice   string   `yaml:"voice,omitempty"`
	// SegmentChars and Workers replace the book's values only when set.
	SegmentChars int `yaml:"segment_chars,omitempty"`
	Workers      int `yaml:"workers,omitempty"`
}

// TranslateProfiles are the translation profiles of a books directory.
type TranslateProfiles struct {
	// Default names the profile of every book that does not choose one.
	Default  string                      `yaml:"default"`
	Profiles map[string]TranslateProfile `yaml:"profiles"`
}

// SpeechProfiles are the speech profiles of a books directory.
type SpeechProfiles struct {
	Default  string                   `yaml:"default"`
	Profiles map[string]SpeechProfile `yaml:"profiles"`
}

// Profiles is the content of the profiles file.
type Profiles struct {
	Translate TranslateProfiles `yaml:"translate"`
	Speech    SpeechProfiles    `yaml:"speech"`
}

// profileNameRe is the form of a profile name.
var profileNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,39}$`)

// ValidProfileName reports whether name can name a profile.
func ValidProfileName(name string) bool {
	return profileNameRe.MatchString(name) && name != ProfileOwn && strings.TrimSpace(name) == name
}

// ProfilesPath returns the path of the profiles file of a books directory.
func ProfilesPath(workdir string) string { return filepath.Join(workdir, ProfilesFile) }

// Names returns the translation profile names, sorted.
func (p TranslateProfiles) Names() []string { return sortedKeys(p.Profiles) }

// Names returns the speech profile names, sorted.
func (p SpeechProfiles) Names() []string { return sortedKeys(p.Profiles) }

func sortedKeys[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

const invalidName = "invalid profile name (use letters, digits, spaces, dots and dashes; \"" + ProfileOwn + "\" is reserved)"

// Validate reports every problem of the profiles at once.
func (p Profiles) Validate() error {
	var probs problems
	for _, name := range p.Translate.Names() {
		prof := p.Translate.Profiles[name]
		key := "translate.profiles." + name
		switch {
		case !ValidProfileName(name):
			probs.addf(key, invalidName)
		case strings.TrimSpace(prof.Model) == "":
			probs.addf(key+".model", "is not set")
		case strings.TrimSpace(prof.BaseURL) == "":
			probs.addf(key+".base_url", "is not set")
		default:
			// A profile must make a valid config over the defaults.
			cfg := Default()
			prof.apply(&cfg)
			if err := cfg.Validate(); err != nil {
				probs.addf(key, "%s", strings.TrimPrefix(err.Error(), "invalid config: "))
			}
		}
	}
	if d := p.Translate.Default; d != "" {
		if _, ok := p.Translate.Profiles[d]; !ok {
			probs.addf("translate.default", "names the profile %q, which does not exist", d)
		}
	}

	for _, name := range p.Speech.Names() {
		prof := p.Speech.Profiles[name]
		key := "speech.profiles." + name
		switch {
		case !ValidProfileName(name):
			probs.addf(key, invalidName)
		case strings.TrimSpace(prof.Command) == "":
			probs.addf(key+".command", "is not set")
		default:
			cfg := Default()
			prof.apply(&cfg)
			if err := cfg.Validate(); err != nil {
				probs.addf(key, "%s", strings.TrimPrefix(err.Error(), "invalid config: "))
			}
		}
	}
	if d := p.Speech.Default; d != "" {
		if _, ok := p.Speech.Profiles[d]; !ok {
			probs.addf("speech.default", "names the profile %q, which does not exist", d)
		}
	}
	return probs.err()
}

// LoadProfiles reads the profiles of a books directory. A directory
// without the file has no profiles.
func LoadProfiles(workdir string) (Profiles, error) {
	path := ProfilesPath(workdir)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Profiles{}, nil
	}
	if err != nil {
		return Profiles{}, fmt.Errorf("profiles: %w", err)
	}
	var p Profiles
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil && !errors.Is(err, io.EOF) {
		return Profiles{}, fmt.Errorf("profiles %s: parsing YAML: %w", path, err)
	}
	if err := p.Validate(); err != nil {
		return Profiles{}, fmt.Errorf("profiles %s: %w", path, err)
	}
	return p, nil
}

// SaveProfiles validates the profiles and writes them to the books
// directory. The file appears only when it is complete.
func SaveProfiles(workdir string, p Profiles) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Translate.Profiles == nil {
		p.Translate.Profiles = map[string]TranslateProfile{}
	}
	if p.Speech.Profiles == nil {
		p.Speech.Profiles = map[string]SpeechProfile{}
	}
	body, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	header := "# Translation and speech profiles shared by the books of this directory.\n" +
		"# A book uses the profiles named by translate.profile and tts.profile in its\n" +
		"# config.yaml, or the defaults below; \"" + ProfileOwn + "\" keeps the book's own settings.\n"
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(workdir, ProfilesFile+".*.part")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	if _, err := tmp.WriteString(header + string(body)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), ProfilesPath(workdir))
}

// apply writes the profile over the translate settings of cfg.
func (prof TranslateProfile) apply(cfg *Config) {
	t := &cfg.Translate
	t.Backend = BackendOpenAI
	t.BaseURL, t.Model, t.APIKeyEnv, t.Headers = prof.BaseURL, prof.Model, prof.APIKeyEnv, prof.Headers
	if prof.Workers > 0 {
		t.Workers = prof.Workers
	}
	if prof.Temperature != nil {
		t.Temperature = *prof.Temperature
	}
}

// apply writes the profile over the speech settings of cfg.
func (prof SpeechProfile) apply(cfg *Config) {
	t := &cfg.TTS
	t.Backend, t.Command, t.Args, t.Voice = prof.Backend, prof.Command, prof.Args, prof.Voice
	if prof.SegmentChars > 0 {
		t.SegmentChars = prof.SegmentChars
	}
	if prof.Workers > 0 {
		t.Workers = prof.Workers
	}
}

// ApplyProfiles replaces the translate and speech settings of a book's
// config with those of its profiles: the ones named by translate.profile
// and tts.profile, or the defaults of the books directory. A part whose
// profile is ProfileOwn, or that names none where there is no default,
// keeps the book's own settings. The names of the profiles that were
// applied are left in cfg.Translate.ProfileUsed and cfg.TTS.ProfileUsed.
func ApplyProfiles(cfg *Config, workdir string) error {
	trName, ttsName := strings.TrimSpace(cfg.Translate.Profile), strings.TrimSpace(cfg.TTS.Profile)
	if trName == ProfileOwn && ttsName == ProfileOwn {
		return nil
	}
	profiles, err := LoadProfiles(workdir)
	if err != nil {
		return err
	}
	return profiles.Apply(cfg, workdir)
}

// Apply is ApplyProfiles with the profiles already loaded; workdir is used
// in error messages only.
func (p Profiles) Apply(cfg *Config, workdir string) error {
	applied := false
	if name := pick(cfg.Translate.Profile, p.Translate.Default); name != "" {
		prof, ok := p.Translate.Profiles[name]
		if !ok {
			return missingProfile("translate.profile", name, workdir, p.Translate.Names())
		}
		prof.apply(cfg)
		cfg.Translate.ProfileUsed = name
		applied = true
	}
	if name := pick(cfg.TTS.Profile, p.Speech.Default); name != "" {
		prof, ok := p.Speech.Profiles[name]
		if !ok {
			return missingProfile("tts.profile", name, workdir, p.Speech.Names())
		}
		prof.apply(cfg)
		cfg.TTS.ProfileUsed = name
		applied = true
	}
	if !applied {
		return nil
	}
	return cfg.Validate()
}

// TranslateFor returns the translation profile in effect for a book whose
// translate.profile is choice; "" means the book's own settings.
func (p Profiles) TranslateFor(choice string) string { return pick(choice, p.Translate.Default) }

// SpeechFor is TranslateFor for tts.profile.
func (p Profiles) SpeechFor(choice string) string { return pick(choice, p.Speech.Default) }

// pick returns the profile to apply for a book's choice: its own name, the
// default when it names none, and "" for ProfileOwn.
func pick(choice, def string) string {
	switch choice = strings.TrimSpace(choice); choice {
	case ProfileOwn:
		return ""
	case "":
		return def
	}
	return choice
}

func missingProfile(key, name, workdir string, have []string) error {
	list := "none"
	if len(have) > 0 {
		list = strings.Join(have, ", ")
	}
	return fmt.Errorf("%s: the profile %q is not in %s (profiles: %s)", key, name, ProfilesPath(workdir), list)
}
