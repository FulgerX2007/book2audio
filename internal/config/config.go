// Package config loads and validates the book2audio configuration.
package config

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Supported backends and output formats.
const (
	BackendOpenAI = "openai" // translate.backend: OpenAI-compatible chat completions API
	BackendPiper  = "piper"  // tts.backend: Piper run as a subprocess

	FormatM4B = "m4b" // audio.format: one file with chapter markers
	FormatMP3 = "mp3" // audio.format: one file per chapter
)

// OutPlaceholder is replaced in tts.args with the path of the WAV file the
// engine must write.
const OutPlaceholder = "{out}"

var (
	translateBackends = []string{BackendOpenAI}
	ttsBackends       = []string{BackendPiper}
	audioFormats      = []string{FormatM4B, FormatMP3}

	bitrateRe = regexp.MustCompile(`^[1-9][0-9]*k$`)
)

// Config is the configuration of one book.
type Config struct {
	SourceLang string    `yaml:"source_lang"`
	TargetLang string    `yaml:"target_lang"`
	Workdir    string    `yaml:"workdir"`
	Book       Book      `yaml:"book"`
	Extract    Extract   `yaml:"extract"`
	Chapters   Chapters  `yaml:"chapters"`
	Translate  Translate `yaml:"translate"`
	TTS        TTS       `yaml:"tts"`
	Audio      Audio     `yaml:"audio"`
}

// Book holds optional tags of the audiobook; empty values are taken from the
// PDF metadata.
type Book struct {
	Title  string `yaml:"title"`
	Author string `yaml:"author"`
}

// Extract configures text extraction and the OCR fallback.
type Extract struct {
	OCRMinChars int    `yaml:"ocr_min_chars"` // a page with fewer characters goes to OCR
	OCRLang     string `yaml:"ocr_lang"`      // tesseract language code; empty: derived from source_lang
	OCRDPI      int    `yaml:"ocr_dpi"`
	OCRWorkers  int    `yaml:"ocr_workers"`
}

// Chapters configures chapter detection.
type Chapters struct {
	HeadingRegex    string `yaml:"heading_regex"`
	HeadingMaxChars int    `yaml:"heading_max_chars"` // a heading is a standalone line no longer than this
	TOCMaxHeadings  int    `yaml:"toc_max_headings"`  // a page with more matches is a table of contents
	FallbackPages   int    `yaml:"fallback_pages"`    // section size when no headings are found
}

// Translate configures the translation stage.
type Translate struct {
	Backend     string  `yaml:"backend"`
	BaseURL     string  `yaml:"base_url"`
	Model       string  `yaml:"model"`
	APIKeyEnv   string  `yaml:"api_key_env"` // name of the environment variable holding the API key
	Temperature float64 `yaml:"temperature"`
	ChunkChars  int     `yaml:"chunk_chars"`
	Workers     int     `yaml:"workers"`
	MaxRetries  int     `yaml:"max_retries"` // transport failures only: 429, 5xx, timeout
}

// TTS configures the speech synthesis stage.
type TTS struct {
	Backend      string   `yaml:"backend"`
	Command      string   `yaml:"command"`
	Args         []string `yaml:"args"`          // must contain OutPlaceholder
	SegmentChars int      `yaml:"segment_chars"` // a longer paragraph is split by sentences
	Workers      int      `yaml:"workers"`
}

// Audio configures the final assembly.
type Audio struct {
	Format           string `yaml:"format"`
	Bitrate          string `yaml:"bitrate"`
	ParagraphPauseMS int    `yaml:"paragraph_pause_ms"`
}

// Default returns the configuration used for keys absent from the config
// file. It passes Validate; translate.model and tts.args have no default and
// are checked by RequireTranslate and RequireTTS.
func Default() Config {
	return Config{
		SourceLang: "en",
		TargetLang: "ru",
		Workdir:    "work",
		Extract: Extract{
			OCRMinChars: 50,
			OCRDPI:      300,
			OCRWorkers:  4,
		},
		Chapters: Chapters{
			HeadingRegex:    `^(Chapter|Part|Глава|Часть)\s+\S+`,
			HeadingMaxChars: 80,
			TOCMaxHeadings:  3,
			FallbackPages:   20,
		},
		Translate: Translate{
			Backend:     BackendOpenAI,
			BaseURL:     "http://localhost:11434/v1",
			APIKeyEnv:   "B2A_API_KEY",
			Temperature: 0.2,
			ChunkChars:  3500,
			Workers:     4,
			MaxRetries:  5,
		},
		TTS: TTS{
			Backend:      BackendPiper,
			Command:      "piper",
			SegmentChars: 1200,
			Workers:      2,
		},
		Audio: Audio{
			Format:           FormatM4B,
			Bitrate:          "64k",
			ParagraphPauseMS: 400,
		},
	}
}

// Load reads and validates the config file at path. An empty path yields the
// defaults.
func Load(path string) (Config, error) {
	if path == "" {
		return Default(), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("конфиг: %w", err)
	}
	defer f.Close()

	cfg, err := Parse(f)
	if err != nil {
		return Config{}, fmt.Errorf("конфиг %s: %w", path, err)
	}
	return cfg, nil
}

// Parse reads a YAML config from r on top of the defaults and validates it.
// Unknown keys are an error, so a misspelled key is not silently ignored.
func Parse(r io.Reader) (Config, error) {
	cfg := Default()
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	// An empty document (io.EOF) means "all defaults".
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("разбор YAML: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// ValidationError lists every problem found in a config.
type ValidationError struct {
	Problems []string // "field: what is wrong"
}

func (e *ValidationError) Error() string {
	return "некорректный конфиг: " + strings.Join(e.Problems, "; ")
}

// problems collects validation findings.
type problems []string

func (p *problems) addf(field, format string, args ...any) {
	*p = append(*p, field+": "+fmt.Sprintf(format, args...))
}

func (p problems) err() error {
	if len(p) == 0 {
		return nil
	}
	return &ValidationError{Problems: p}
}

// Validate checks every value that is set. Settings that are required only
// by a particular stage are checked by RequireTranslate and RequireTTS, so a
// book can be initialised and its text extracted before they are filled in.
func (c Config) Validate() error {
	var p problems

	if strings.TrimSpace(c.SourceLang) == "" {
		p.addf("source_lang", "не задано")
	}
	if strings.TrimSpace(c.TargetLang) == "" {
		p.addf("target_lang", "не задано")
	}
	if strings.TrimSpace(c.Workdir) == "" {
		p.addf("workdir", "не задано")
	}

	if c.Extract.OCRMinChars < 0 {
		p.addf("extract.ocr_min_chars", "не может быть отрицательным")
	}
	if c.Extract.OCRDPI < 72 || c.Extract.OCRDPI > 1200 {
		p.addf("extract.ocr_dpi", "должно быть от 72 до 1200")
	}
	if c.Extract.OCRWorkers < 1 {
		p.addf("extract.ocr_workers", "должно быть не меньше 1")
	}
	if strings.TrimSpace(c.SourceLang) != "" {
		if _, err := c.OCRLang(); err != nil {
			p.addf("extract.ocr_lang", "%v", err)
		}
	}

	if c.Chapters.HeadingRegex == "" {
		p.addf("chapters.heading_regex", "не задано")
	} else if _, err := regexp.Compile(c.Chapters.HeadingRegex); err != nil {
		p.addf("chapters.heading_regex", "некорректное регулярное выражение: %v", err)
	}
	if c.Chapters.HeadingMaxChars < 1 {
		p.addf("chapters.heading_max_chars", "должно быть не меньше 1")
	}
	if c.Chapters.TOCMaxHeadings < 1 {
		p.addf("chapters.toc_max_headings", "должно быть не меньше 1")
	}
	if c.Chapters.FallbackPages < 1 {
		p.addf("chapters.fallback_pages", "должно быть не меньше 1")
	}

	if !slices.Contains(translateBackends, c.Translate.Backend) {
		p.addf("translate.backend", "неизвестный бэкенд %q (допустимо: %s)",
			c.Translate.Backend, strings.Join(translateBackends, ", "))
	}
	if u, err := url.Parse(c.Translate.BaseURL); err != nil ||
		(u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		p.addf("translate.base_url", "нужен адрес вида http(s)://хост[/путь], получено %q", c.Translate.BaseURL)
	}
	if c.Translate.Temperature < 0 || c.Translate.Temperature > 2 {
		p.addf("translate.temperature", "должно быть от 0 до 2")
	}
	if c.Translate.ChunkChars < 200 {
		p.addf("translate.chunk_chars", "должно быть не меньше 200")
	}
	if c.Translate.Workers < 1 {
		p.addf("translate.workers", "должно быть не меньше 1")
	}
	if c.Translate.MaxRetries < 0 {
		p.addf("translate.max_retries", "не может быть отрицательным")
	}

	if !slices.Contains(ttsBackends, c.TTS.Backend) {
		p.addf("tts.backend", "неизвестный бэкенд %q (допустимо: %s)",
			c.TTS.Backend, strings.Join(ttsBackends, ", "))
	}
	if strings.TrimSpace(c.TTS.Command) == "" {
		p.addf("tts.command", "не задано")
	}
	if len(c.TTS.Args) > 0 && !c.TTS.hasOutPlaceholder() {
		p.addf("tts.args", "нет подстановки %s для пути выходного файла", OutPlaceholder)
	}
	if c.TTS.SegmentChars < 100 {
		p.addf("tts.segment_chars", "должно быть не меньше 100")
	}
	if c.TTS.Workers < 1 {
		p.addf("tts.workers", "должно быть не меньше 1")
	}

	if !slices.Contains(audioFormats, c.Audio.Format) {
		p.addf("audio.format", "неизвестный формат %q (допустимо: %s)",
			c.Audio.Format, strings.Join(audioFormats, ", "))
	}
	if !bitrateRe.MatchString(c.Audio.Bitrate) {
		p.addf("audio.bitrate", "нужно значение вида 64k, получено %q", c.Audio.Bitrate)
	}
	if c.Audio.ParagraphPauseMS < 0 {
		p.addf("audio.paragraph_pause_ms", "не может быть отрицательным")
	}

	return p.err()
}

// RequireTranslate checks the settings without defaults that the translate
// stage cannot work without.
func (c Config) RequireTranslate() error {
	var p problems
	if strings.TrimSpace(c.Translate.Model) == "" {
		p.addf("translate.model", "не задано")
	}
	return p.err()
}

// RequireTTS checks the settings without defaults that the synth stage
// cannot work without.
func (c Config) RequireTTS() error {
	var p problems
	if !c.TTS.hasOutPlaceholder() {
		p.addf("tts.args", "нужны аргументы запуска с подстановкой %s для пути выходного файла", OutPlaceholder)
	}
	return p.err()
}

func (t TTS) hasOutPlaceholder() bool {
	return slices.ContainsFunc(t.Args, func(arg string) bool {
		return strings.Contains(arg, OutPlaceholder)
	})
}

// tesseractLangs maps a primary language subtag to the tesseract language
// code used for OCR.
var tesseractLangs = map[string]string{
	"ar": "ara",
	"bg": "bul",
	"cs": "ces",
	"da": "dan",
	"de": "deu",
	"el": "ell",
	"en": "eng",
	"es": "spa",
	"fi": "fin",
	"fr": "fra",
	"he": "heb",
	"hu": "hun",
	"it": "ita",
	"ja": "jpn",
	"ko": "kor",
	"nl": "nld",
	"no": "nor",
	"pl": "pol",
	"pt": "por",
	"ro": "ron",
	"ru": "rus",
	"sv": "swe",
	"tr": "tur",
	"uk": "ukr",
	"zh": "chi_sim",
}

// OCRLang returns the tesseract language code for OCR: extract.ocr_lang when
// it is set, otherwise the code derived from source_lang ("en" or "en-US"
// gives "eng").
func (c Config) OCRLang() (string, error) {
	if lang := strings.TrimSpace(c.Extract.OCRLang); lang != "" {
		return lang, nil
	}
	primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(c.SourceLang)), "-")
	primary, _, _ = strings.Cut(primary, "_")
	if lang, ok := tesseractLangs[primary]; ok {
		return lang, nil
	}
	return "", fmt.Errorf("для source_lang %q нет известного кода tesseract, задайте его явно", c.SourceLang)
}
