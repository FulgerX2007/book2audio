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
	out, err := UpdateYAML([]byte(original), map[string]string{
		"target_lang":           "en",
		"translate.model":       "new-model",
		"translate.base_url":    "https://api.example.com/v1",
		"translate.api_key_env": "",
	})
	if err != nil {
		t.Fatalf("UpdateYAML: %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"# My book", "# the language of the audiobook", "# the model on the server",
		"target_lang: en", "model: new-model", "workers: 3", "base_url: https://api.example.com/v1",
		`api_key_env: ""`, "backend: silero", "command: /opt/silero_tts.py",
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
			out, err := UpdateYAML([]byte(original), map[string]string{"translate.model": "m", "source_lang": "de"})
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
		set            map[string]string
		want           string
	}{
		{"result is not a valid config", "source_lang: en\n", map[string]string{"translate.base_url": "not a url"}, "translate.base_url"},
		{"empty language", "", map[string]string{"target_lang": ""}, "target_lang"},
		{"broken YAML", "translate: [", map[string]string{"translate.model": "m"}, "parsing YAML"},
		{"section is a value", "translate: 5\n", map[string]string{"translate.model": "m"}, "is not a section"},
		{"value is a section", "translate:\n  model:\n    a: 1\n", map[string]string{"translate.model": "m"}, "is not a simple value"},
		{"top level is a list", "- a\n", map[string]string{"translate.model": "m"}, "must be a mapping"},
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
