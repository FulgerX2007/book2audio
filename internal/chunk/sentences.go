package chunk

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// abbreviations end with a full stop that does not end a sentence. They are
// compared in lower case, without the final dot.
var abbreviations = map[string]bool{
	"mr": true, "mrs": true, "ms": true, "dr": true, "prof": true, "st": true, "jr": true, "sr": true,
	"vs": true, "etc": true, "e.g": true, "i.e": true, "no": true, "vol": true, "fig": true, "cf": true,
	"г": true, "гг": true, "т.д": true, "т.п": true, "т.е": true, "т.к": true, "им": true, "ул": true,
	"др": true, "пр": true, "см": true, "стр": true, "руб": true, "тыс": true, "млн": true, "млрд": true,
}

// Sentences splits a paragraph into sentences. A sentence ends with ".",
// "!", "?" or "…", optionally followed by closing quotes or brackets, when
// white space and a capital letter, a digit or an opening quote follow.
// Full stops after abbreviations ("Dr.", "т.д.") and initials ("J. Smith")
// do not end a sentence. The sentences are returned without the white
// space between them.
func Sentences(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if !strings.ContainsRune(".!?…", r) {
			continue
		}
		// Take the whole run of end marks and closing quotes: `?!”`, `...`.
		end := i
		for end < len(s) {
			next, n := utf8.DecodeRuneInString(s[end:])
			if !strings.ContainsRune(`.!?…"'”’»)]`, next) {
				break
			}
			end += n
		}
		rest := s[end:]
		trimmed := strings.TrimLeftFunc(rest, unicode.IsSpace)
		if len(trimmed) == len(rest) || trimmed == "" {
			i = end
			continue // no white space after the mark, or the end of the text
		}
		first, _ := utf8.DecodeRuneInString(trimmed)
		if !unicode.IsUpper(first) && !unicode.IsDigit(first) && !strings.ContainsRune(`"'“‘«([—–-`, first) {
			i = end
			continue
		}
		if r == '.' && end == i && endsWithAbbreviation(s[start:i-size]) {
			continue
		}
		out = append(out, strings.TrimSpace(s[start:end]))
		start = len(s) - len(trimmed)
		i = start
	}
	if tail := strings.TrimSpace(s[start:]); tail != "" {
		out = append(out, tail)
	}
	return out
}

// endsWithAbbreviation reports whether the text before a full stop ends
// with a known abbreviation or a single-letter initial.
func endsWithAbbreviation(before string) bool {
	i := strings.LastIndexFunc(before, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(`("'“«`, r)
	})
	word := before[i+1:]
	if word == "" {
		return false
	}
	if utf8.RuneCountInString(word) == 1 {
		r, _ := utf8.DecodeRuneInString(word)
		if unicode.IsUpper(r) {
			return true // an initial: "J. Smith"
		}
	}
	return abbreviations[strings.ToLower(word)]
}
