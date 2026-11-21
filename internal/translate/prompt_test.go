package translate

import (
	"strings"
	"testing"
)

func TestBuildMessagesPlain(t *testing.T) {
	msgs := BuildMessages(Request{Text: "# Chapter 1\n\nThe lamp is lit.", SourceLang: "en", TargetLang: "ru"})

	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Fatalf("messages = %+v, want a system and a user message", msgs)
	}
	system, user := msgs[0].Content, msgs[1].Content
	for _, want := range []string{"from English into Russian", "only what stands between <text> and </text>", "same number of paragraphs", `"# "`, "Output only the translation"} {
		if !strings.Contains(system, want) {
			t.Errorf("the system message lacks %q:\n%s", want, system)
		}
	}
	if strings.Contains(system, "{") || strings.Contains(system, "names and terms") {
		t.Errorf("the system message has a placeholder or a glossary section:\n%s", system)
	}
	if strings.Contains(system, "<previous>") {
		t.Errorf("the system message has a context section although no context was given:\n%s", system)
	}
	if want := "<text>\n# Chapter 1\n\nThe lamp is lit.\n</text>"; user != want {
		t.Errorf("user message = %q, want %q", user, want)
	}
}

func TestBuildMessagesWithGlossaryAndContext(t *testing.T) {
	msgs := BuildMessages(Request{
		Text:        "Mr. Darcy rang the fog bell.",
		SourceLang:  "en-GB",
		TargetLang:  "ru",
		Glossary:    []Term{{"Mr. Darcy", "мистер Дарси"}, {"fog bell", "туманный колокол"}},
		PrevContext: "  The night was thick with fog.  ",
	})
	system, user := msgs[0].Content, msgs[1].Content

	if !strings.Contains(system, "from English into Russian") {
		t.Errorf("the region subtag was not dropped:\n%s", system)
	}
	glossary := "in the right grammatical form:\n- Mr. Darcy → мистер Дарси\n- fog bell → туманный колокол"
	context := "Never translate it and never repeat it.\n<previous>\nThe night was thick with fog.\n</previous>"
	if !strings.Contains(system, glossary) || !strings.HasSuffix(system, context) {
		t.Errorf("the glossary or the context section of the system message:\n%s", system)
	}
	// The user message holds the text and nothing else: a context placed
	// next to the text was sometimes translated instead of it.
	if want := "<text>\nMr. Darcy rang the fog bell.\n</text>"; user != want {
		t.Errorf("user message = %q, want %q", user, want)
	}
}

func TestBuildMessagesKeepsPlaceholdersInText(t *testing.T) {
	// Text that looks like a template placeholder must pass through intact.
	msgs := BuildMessages(Request{Text: "Use {text} and {context} here.", PrevContext: "About {text}.", SourceLang: "en", TargetLang: "ru"})
	user := msgs[1].Content
	if user != "<text>\nUse {text} and {context} here.\n</text>" {
		t.Errorf("user message = %q", user)
	}
	if !strings.Contains(msgs[0].Content, "<previous>\nAbout {text}.\n</previous>") {
		t.Errorf("system message = %q", msgs[0].Content)
	}
}

func TestLanguageName(t *testing.T) {
	for code, want := range map[string]string{
		"en": "English", "EN": "English", "en-US": "English", "pt_BR": "Portuguese", "ru": "Russian",
		"ro": "Romanian", "tlh": "tlh", "": "",
	} {
		if got := languageName(code); got != want {
			t.Errorf("languageName(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestPromptHash(t *testing.T) {
	if PromptHash() != PromptHash() || len(PromptHash()) != 64 {
		t.Error("PromptHash is not a stable sha256 hex digest")
	}
	base := promptHash(systemTemplate, glossaryTemplate, contextTemplate, textTemplate)
	if base != PromptHash() {
		t.Error("PromptHash does not cover exactly the four templates")
	}
	changed := promptHash(systemTemplate+" Be brief.", glossaryTemplate, contextTemplate, textTemplate)
	if changed == base {
		t.Error("a changed template does not change the hash")
	}
}
