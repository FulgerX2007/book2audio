package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("Default() does not validate: %v", err)
	}
}

func TestParseEmptyGivesDefaults(t *testing.T) {
	for name, doc := range map[string]string{
		"empty":         "",
		"comments only": "# ничего не задано\n",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Parse(strings.NewReader(doc))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !reflect.DeepEqual(got, Default()) {
				t.Errorf("Parse = %+v, want the defaults", got)
			}
		})
	}
}

func TestParseOverridesDefaults(t *testing.T) {
	const doc = `
source_lang: de
book:
  title: Собственный отчёт
  author: Автор
extract:
  ocr_dpi: 400
translate:
  model: local-model
  base_url: https://example.test/v1
  temperature: 0        # an explicit zero must not be replaced by the default
  max_retries: 0
tts:
  args: ["--model", "voices/ru.onnx", "--output_file", "{out}"]
audio:
  format: mp3
  bitrate: 96k
  paragraph_pause_ms: 0
`
	got, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := Default()
	want.SourceLang = "de"
	want.Book = Book{Title: "Собственный отчёт", Author: "Автор"}
	want.Extract.OCRDPI = 400
	want.Translate.Model = "local-model"
	want.Translate.BaseURL = "https://example.test/v1"
	want.Translate.Temperature = 0
	want.Translate.MaxRetries = 0
	want.TTS.Args = []string{"--model", "voices/ru.onnx", "--output_file", "{out}"}
	want.Audio = Audio{Format: FormatMP3, Bitrate: "96k", ParagraphPauseMS: 0}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want string // substring of the error
	}{
		{"malformed YAML", "translate: [unclosed", "разбор YAML"},
		{"unknown top-level key", "sourse_lang: en\n", "sourse_lang"},
		{"unknown nested key", "audio:\n  bitrat: 64k\n", "bitrat"},
		{"wrong type", "extract:\n  ocr_dpi: много\n", "разбор YAML"},
		{"invalid value", "audio:\n  format: ogg\n", "audio.format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.doc))
			if err == nil {
				t.Fatal("Parse succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q lacks %q", err, tt.want)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	t.Run("empty path gives defaults", func(t *testing.T) {
		got, err := Load("")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !reflect.DeepEqual(got, Default()) {
			t.Errorf("Load(\"\") = %+v, want the defaults", got)
		}
	})

	t.Run("valid file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("target_lang: uk\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.TargetLang != "uk" {
			t.Errorf("TargetLang = %q, want %q", got.TargetLang, "uk")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("error = %v, want os.ErrNotExist", err)
		}
	})

	t.Run("invalid file names the path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("workdir: ''\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil {
			t.Fatal("Load succeeded, want an error")
		}
		if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "workdir") {
			t.Errorf("error %q lacks the path or the field", err)
		}
	})
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string // field named in the problem
	}{
		{"empty source_lang", func(c *Config) { c.SourceLang = " " }, "source_lang"},
		{"empty target_lang", func(c *Config) { c.TargetLang = "" }, "target_lang"},
		{"empty workdir", func(c *Config) { c.Workdir = "" }, "workdir"},
		{"negative ocr_min_chars", func(c *Config) { c.Extract.OCRMinChars = -1 }, "extract.ocr_min_chars"},
		{"ocr_dpi too low", func(c *Config) { c.Extract.OCRDPI = 10 }, "extract.ocr_dpi"},
		{"ocr_dpi too high", func(c *Config) { c.Extract.OCRDPI = 5000 }, "extract.ocr_dpi"},
		{"no ocr workers", func(c *Config) { c.Extract.OCRWorkers = 0 }, "extract.ocr_workers"},
		{"source_lang without a tesseract code", func(c *Config) { c.SourceLang = "tlh" }, "extract.ocr_lang"},
		{"empty heading_regex", func(c *Config) { c.Chapters.HeadingRegex = "" }, "chapters.heading_regex"},
		{"broken heading_regex", func(c *Config) { c.Chapters.HeadingRegex = "(" }, "chapters.heading_regex"},
		{"heading_max_chars", func(c *Config) { c.Chapters.HeadingMaxChars = 0 }, "chapters.heading_max_chars"},
		{"toc_max_headings", func(c *Config) { c.Chapters.TOCMaxHeadings = 0 }, "chapters.toc_max_headings"},
		{"fallback_pages", func(c *Config) { c.Chapters.FallbackPages = 0 }, "chapters.fallback_pages"},
		{"unknown translate backend", func(c *Config) { c.Translate.Backend = "deepl" }, "translate.backend"},
		{"base_url without scheme", func(c *Config) { c.Translate.BaseURL = "localhost:11434/v1" }, "translate.base_url"},
		{"base_url with wrong scheme", func(c *Config) { c.Translate.BaseURL = "ftp://example.test" }, "translate.base_url"},
		{"empty base_url", func(c *Config) { c.Translate.BaseURL = "" }, "translate.base_url"},
		{"negative temperature", func(c *Config) { c.Translate.Temperature = -0.1 }, "translate.temperature"},
		{"temperature too high", func(c *Config) { c.Translate.Temperature = 2.5 }, "translate.temperature"},
		{"chunk_chars too small", func(c *Config) { c.Translate.ChunkChars = 50 }, "translate.chunk_chars"},
		{"no translate workers", func(c *Config) { c.Translate.Workers = 0 }, "translate.workers"},
		{"negative max_retries", func(c *Config) { c.Translate.MaxRetries = -1 }, "translate.max_retries"},
		{"unknown tts backend", func(c *Config) { c.TTS.Backend = "espeak" }, "tts.backend"},
		{"empty tts command", func(c *Config) { c.TTS.Command = "" }, "tts.command"},
		{"tts args without placeholder", func(c *Config) { c.TTS.Args = []string{"--model", "v.onnx"} }, "tts.args"},
		{"accent args without a command", func(c *Config) { c.TTS.AccentArgs = []string{"--fast"} }, "tts.accent_args"},
		{"segment_chars too small", func(c *Config) { c.TTS.SegmentChars = 10 }, "tts.segment_chars"},
		{"no tts workers", func(c *Config) { c.TTS.Workers = 0 }, "tts.workers"},
		{"unknown audio format", func(c *Config) { c.Audio.Format = "ogg" }, "audio.format"},
		{"bitrate without unit", func(c *Config) { c.Audio.Bitrate = "64" }, "audio.bitrate"},
		{"zero bitrate", func(c *Config) { c.Audio.Bitrate = "0k" }, "audio.bitrate"},
		{"negative pause", func(c *Config) { c.Audio.ParagraphPauseMS = -1 }, "audio.paragraph_pause_ms"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(&cfg)

			err := cfg.Validate()
			verr, ok := errors.AsType[*ValidationError](err)
			if !ok {
				t.Fatalf("Validate() = %v, want a *ValidationError", err)
			}
			if len(verr.Problems) != 1 || !strings.HasPrefix(verr.Problems[0], tt.want+": ") {
				t.Errorf("problems = %q, want exactly one about %q", verr.Problems, tt.want)
			}
		})
	}
}

func TestValidateAcceptsBoundaryValues(t *testing.T) {
	cfg := Default()
	cfg.Extract.OCRMinChars = 0
	cfg.Extract.OCRDPI = 72
	cfg.Translate.Temperature = 2
	cfg.Translate.MaxRetries = 0
	cfg.Translate.BaseURL = "https://api.example.test"
	cfg.TTS.Args = []string{"--output_file={out}"}
	cfg.TTS.AccentCommand = "accent.py" // a command without arguments is fine
	cfg.Audio.ParagraphPauseMS = 0

	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	cfg := Default()
	cfg.TargetLang = ""
	cfg.Audio.Format = "ogg"
	cfg.TTS.Workers = 0

	verr, ok := errors.AsType[*ValidationError](cfg.Validate())
	if !ok {
		t.Fatal("Validate() did not return a *ValidationError")
	}
	if len(verr.Problems) != 3 {
		t.Errorf("got %d problems, want 3: %q", len(verr.Problems), verr.Problems)
	}
	for _, field := range []string{"target_lang", "audio.format", "tts.workers"} {
		if !strings.Contains(verr.Error(), field) {
			t.Errorf("error %q does not mention %q", verr, field)
		}
	}
}

func TestOCRLang(t *testing.T) {
	tests := []struct {
		name       string
		sourceLang string
		ocrLang    string
		want       string
		wantErr    bool
	}{
		{"derived from source_lang", "en", "", "eng", false},
		{"region subtag is ignored", "en-US", "", "eng", false},
		{"underscore separator", "pt_BR", "", "por", false},
		{"case and spaces are ignored", " DE ", "", "deu", false},
		{"romanian", "ro", "", "ron", false},
		{"chinese", "zh", "", "chi_sim", false},
		{"explicit value wins", "en", "deu+eng", "deu+eng", false},
		{"explicit value for an unknown language", "tlh", "tlh", "tlh", false},
		{"unknown language", "tlh", "", "", true},
		{"empty source_lang", "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.SourceLang = tt.sourceLang
			cfg.Extract.OCRLang = tt.ocrLang

			got, err := cfg.OCRLang()
			if (err != nil) != tt.wantErr {
				t.Fatalf("OCRLang() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("OCRLang() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRequireTranslate(t *testing.T) {
	cfg := Default()
	err := cfg.RequireTranslate()
	if err == nil || !strings.Contains(err.Error(), "translate.model") {
		t.Errorf("RequireTranslate() = %v, want a problem about translate.model", err)
	}

	cfg.Translate.Model = "local-model"
	if err := cfg.RequireTranslate(); err != nil {
		t.Errorf("RequireTranslate() = %v, want nil", err)
	}
}

func TestRequireTTS(t *testing.T) {
	cfg := Default()
	err := cfg.RequireTTS()
	if err == nil || !strings.Contains(err.Error(), "tts.args") {
		t.Errorf("RequireTTS() = %v, want a problem about tts.args", err)
	}

	cfg.TTS.Args = []string{"--model", "voices/ru.onnx", "--output_file", "{out}"}
	if err := cfg.RequireTTS(); err != nil {
		t.Errorf("RequireTTS() = %v, want nil", err)
	}
}
