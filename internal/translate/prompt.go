package translate

import (
	"strings"

	"github.com/FulgerX2007/book2audio/internal/workspace"
)

// The prompt templates. Changing them changes PromptHash, so every chunk is
// translated again with the new prompt.
const (
	systemTemplate = `You are a professional literary translator. Translate the text from {source} into {target}.

Rules:
- Translate everything; do not omit, shorten or add anything.
- Keep the paragraph breaks. Paragraphs are separated by a blank line; the translation must have the same number of paragraphs in the same order.
- A line that starts with "# " is a heading: keep the "# " prefix and translate the heading.
- Write natural, fluent {target}, as a book meant to be read aloud.
- Output only the translation: no notes, no explanations, no quotation marks around the whole text.`

	glossaryTemplate = `

Use these translations of names and terms, in the right grammatical form:`

	contextTemplate = `The passage before the text, for context only. Do not translate it and do not repeat it:
<context>
{context}
</context>

`
	textTemplate = `Translate this text:
<text>
{text}
</text>`
)

// languageNames gives the model a language name instead of a bare code.
var languageNames = map[string]string{
	"ar": "Arabic", "bg": "Bulgarian", "cs": "Czech", "da": "Danish", "de": "German", "el": "Greek",
	"en": "English", "es": "Spanish", "fi": "Finnish", "fr": "French", "he": "Hebrew", "hu": "Hungarian",
	"it": "Italian", "ja": "Japanese", "ko": "Korean", "nl": "Dutch", "no": "Norwegian", "pl": "Polish",
	"pt": "Portuguese", "ro": "Romanian", "ru": "Russian", "sv": "Swedish", "tr": "Turkish",
	"uk": "Ukrainian", "zh": "Chinese",
}

// languageName returns the English name of a language code ("en-US" gives
// "English"); an unknown code is returned as it is.
func languageName(code string) string {
	primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(code)), "-")
	primary, _, _ = strings.Cut(primary, "_")
	if name, ok := languageNames[primary]; ok {
		return name
	}
	return code
}

// Message is one chat message sent to the model.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// BuildMessages renders the system and the user message for a request.
func BuildMessages(req Request) []Message {
	names := strings.NewReplacer("{source}", languageName(req.SourceLang), "{target}", languageName(req.TargetLang))
	var system strings.Builder
	system.WriteString(names.Replace(systemTemplate))
	if len(req.Glossary) > 0 {
		system.WriteString(glossaryTemplate)
		for _, t := range req.Glossary {
			system.WriteString("\n- " + t.Source + " → " + t.Target)
		}
	}

	var user strings.Builder
	if ctx := strings.TrimSpace(req.PrevContext); ctx != "" {
		user.WriteString(strings.Replace(contextTemplate, "{context}", ctx, 1))
	}
	user.WriteString(strings.Replace(textTemplate, "{text}", req.Text, 1))

	return []Message{
		{Role: "system", Content: system.String()},
		{Role: "user", Content: user.String()},
	}
}

// PromptHash identifies the prompt templates. It is part of the input hash
// of every translation chunk.
func PromptHash() string {
	return promptHash(systemTemplate, glossaryTemplate, contextTemplate, textTemplate)
}

func promptHash(templates ...string) string {
	return workspace.HashStrings(templates...)
}
