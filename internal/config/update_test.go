package config

import (
	"strings"
	"testing"
)

func TestUpdateYAML(t *testing.T) {
	const original = `# My book
source_lang: en
target_lang: ru   # the language of the audiobook

translate:
  # the model on the server
  model: old-model
  workers: 3
tts:
  backend: silero
  command: /opt/silero_tts.py
`
	out, err := UpdateYAML([]byte(original), map[string]any{
		"target_lang":           "en",
		"translate.model":       "new-model",
		"translate.base_url":    "https://api.example.com/v1",
		"translate.api_key_env": "",
		"tts.args":              []string{"--speaker", "xenia", "a b"},
	})
	if err != nil {
		t.Fatalf("UpdateYAML: %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"# My book", "# the language of the audiobook", "# the model on the server",
		"target_lang: en", "model: new-model", "workers: 3", "base_url: https://api.example.com/v1",
		`api_key_env: ""`, "backend: silero", `args: ["--speaker", "xenia", "a b"]`, "command: /opt/silero_tts.py",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the result lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "old-model") {
		t.Errorf("the old value is still there:\n%s", text)
	}
	cfg, err := Parse(strings.NewReader(text))
	if err != nil {
		t.Fatalf("the result does not parse: %v", err)
	}
	if len(cfg.TTS.Args) != 3 || cfg.TTS.Args[2] != "a b" {
		t.Errorf("tts.args = %q", cfg.TTS.Args)
	}
	if !cfg.SameLanguage() || cfg.Translate.Model != "new-model" || cfg.Translate.Workers != 3 || cfg.Translate.APIKeyEnv != "" {
		t.Errorf("config = %+v", cfg.Translate)
	}
}

func TestUpdateYAMLCreatesSections(t *testing.T) {
	for name, original := range map[string]string{
		"empty file":       "",
		"only comments":    "# nothing here yet\n",
		"section is empty": "translate:\n",
	} {
		t.Run(name, func(t *testing.T) {
			out, err := UpdateYAML([]byte(original), map[string]any{"translate.model": "m", "source_lang": "de"})
			if err != nil {
				t.Fatalf("UpdateYAML: %v", err)
			}
			cfg, err := Parse(strings.NewReader(string(out)))
			if err != nil || cfg.Translate.Model != "m" || cfg.SourceLang != "de" {
				t.Errorf("config = %+v, %v\n%s", cfg.Translate, err, out)
			}
		})
	}
}

func TestUpdateYAMLErrors(t *testing.T) {
	tests := []struct {
		name, original string
		set            map[string]any
		want           string
	}{
		{"result is not a valid config", "source_lang: en\n", map[string]any{"translate.base_url": "not a url"}, "translate.base_url"},
		{"empty language", "", map[string]any{"target_lang": ""}, "target_lang"},
		{"broken YAML", "translate: [", map[string]any{"translate.model": "m"}, "parsing YAML"},
		{"section is a value", "translate: 5\n", map[string]any{"translate.model": "m"}, "is not a section"},
		{"value is a section", "translate:\n  model:\n    a: 1\n", map[string]any{"translate.model": "m"}, "is not a simple value"},
		{"list replaced by a string is fine, a section is not", "tts:\n  args:\n    a: 1\n", map[string]any{"tts.args": []string{"x"}}, "is not a list"},
		{"unsupported value", "", map[string]any{"translate.workers": 3}, "unsupported value"},
		{"top level is a list", "- a\n", map[string]any{"translate.model": "m"}, "must be a mapping"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := UpdateYAML([]byte(tt.original), tt.set)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

// A list written in block style is replaced as a whole, and an empty list
// is written as [].
func TestUpdateYAMLLists(t *testing.T) {
	const original = "tts:\n  backend: piper\n  args:\n    - --model\n    - v.onnx\n    - --output_file\n    - \"{out}\"\n"
	out, err := UpdateYAML([]byte(original), map[string]any{"tts.args": []string{}})
	if err != nil {
		t.Fatalf("UpdateYAML: %v", err)
	}
	if !strings.Contains(string(out), "args: []") || strings.Contains(string(out), "v.onnx") {
		t.Errorf("result:\n%s", out)
	}
	cfg, _ := Parse(strings.NewReader(string(out)))
	if len(cfg.TTS.Args) != 0 || !cfg.AutoVoice() {
		t.Errorf("tts = %+v", cfg.TTS)
	}
}

func TestUpdateYAMLMaps(t *testing.T) {
	const original = "translate:\n  model: m\n  headers:\n    old-header: x\n"
	out, err := UpdateYAML([]byte(original), map[string]any{
		"translate.headers": map[string]string{"anthropic-workspace-id": "wrkspc_1", "x-b": "2"},
	})
	if err != nil {
		t.Fatalf("UpdateYAML: %v", err)
	}
	cfg, err := Parse(strings.NewReader(string(out)))
	if err != nil || len(cfg.Translate.Headers) != 2 || cfg.Translate.Headers["anthropic-workspace-id"] != "wrkspc_1" {
		t.Errorf("headers = %v, %v\n%s", cfg.Translate.Headers, err, out)
	}

	// An empty map removes the headers; a new section is created when missing.
	out, err = UpdateYAML(out, map[string]any{"translate.headers": map[string]string{}})
	if err != nil || !strings.Contains(string(out), "headers: {}") {
		t.Errorf("empty map: %v\n%s", err, out)
	}
	out, err = UpdateYAML([]byte("source_lang: en\n"), map[string]any{"translate.headers": map[string]string{"a": "b"}})
	if cfg, perr := Parse(strings.NewReader(string(out))); err != nil || perr != nil || cfg.Translate.Headers["a"] != "b" {
		t.Errorf("new section: %v %v\n%s", err, perr, out)
	}
	if _, err := UpdateYAML([]byte("translate:\n  headers: [a]\n"), map[string]any{"translate.headers": map[string]string{"a": "b"}}); err == nil {
		t.Error("a list was replaced by a map")
	}
}
