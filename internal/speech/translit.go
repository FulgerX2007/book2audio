package speech

import (
	"regexp"
	"strings"
)

// LatinRulesVersion identifies the rules of RussianizeLatin. It is part of
// the hash of a segment that has Latin letters and is read by a Russian-only
// engine: bump it when the rules change, and those segments are synthesised
// again.
const LatinRulesVersion = "1"

var latinWordRe = regexp.MustCompile(`[A-Za-z]+(?:['’][A-Za-z]+)*`)

// HasLatin reports whether text has a word in Latin letters.
func HasLatin(text string) bool { return latinWordRe.MatchString(text) }

// RussianizeLatin rewrites the words in Latin letters with Cyrillic ones so
// that a speech engine which reads only Russian can pronounce them: names,
// titles and terms left in English inside a Russian text. The result
// approximates the English pronunciation by spelling rules; short words in
// capitals are spelled out letter by letter.
func RussianizeLatin(text string) string {
	return latinWordRe.ReplaceAllStringFunc(text, func(word string) string {
		if isAcronym(word) {
			return spellOut(word)
		}
		return russianizeWord(strings.ToLower(word))
	})
}

// isAcronym reports whether a word is read letter by letter: up to three
// capitals ("C", "FBI"), or up to five capitals without a vowel ("HTTP"). A
// longer word with vowels is read as a word ("NATO").
func isAcronym(word string) bool {
	if len(word) > 5 || word != strings.ToUpper(word) || strings.ContainsAny(word, "'’") {
		return false
	}
	return len(word) <= 3 || !strings.ContainsAny(word, "AEIOUY")
}

var letterNames = map[rune]string{
	'A': "эй", 'B': "би", 'C': "си", 'D': "ди", 'E': "и", 'F': "эф", 'G': "джи",
	'H': "эйч", 'I': "ай", 'J': "джей", 'K': "кей", 'L': "эл", 'M': "эм", 'N': "эн",
	'O': "оу", 'P': "пи", 'Q': "кью", 'R': "ар", 'S': "эс", 'T': "ти", 'U': "ю",
	'V': "ви", 'W': "дабл-ю", 'X': "экс", 'Y': "уай", 'Z': "зед",
}

func spellOut(word string) string {
	names := make([]string, 0, len(word))
	for _, r := range word {
		names = append(names, letterNames[r])
	}
	return strings.Join(names, " ")
}

// latinGroups are letter groups with a sound of their own, longest first.
var latinGroups = []struct{ from, to string }{
	{"tion", "шн"}, {"sion", "жн"}, {"ough", "о"}, {"augh", "о"},
	{"igh", "ай"}, {"tch", "ч"}, {"sch", "ск"},
	{"sh", "ш"}, {"ch", "ч"}, {"th", "т"}, {"ph", "ф"}, {"wh", "в"}, {"ck", "к"},
	{"qu", "кв"}, {"ng", "нг"},
	{"oo", "у"}, {"ee", "и"}, {"ea", "и"}, {"ou", "ау"}, {"ow", "оу"},
	{"ai", "эй"}, {"ay", "эй"}, {"ey", "и"}, {"oi", "ой"}, {"oy", "ой"},
	{"au", "о"}, {"aw", "о"}, {"ew", "ью"},
}

// latinStarts are groups that sound different at the start of a word.
var latinStarts = []struct{ from, to string }{{"kn", "н"}, {"wr", "р"}, {"ps", "с"}}

var latinLetters = map[byte]string{
	'a': "а", 'b': "б", 'd': "д", 'f': "ф", 'g': "г", 'h': "х", 'i': "и", 'j': "дж",
	'k': "к", 'l': "л", 'm': "м", 'n': "н", 'o': "о", 'p': "п", 'q': "к", 'r': "р",
	's': "с", 't': "т", 'v': "в", 'w': "в", 'x': "кс", 'z': "з",
}

// longVowels are the sounds of a vowel before a consonant and a final
// silent e: "line" is "лайн".
var longVowels = map[byte]string{'a': "эй", 'i': "ай", 'o': "оу", 'u': "ю"}

func isLatinVowel(c byte) bool { return strings.IndexByte("aeiouy", c) >= 0 }

// russianizeWord rewrites one lower-case word.
func russianizeWord(word string) string {
	word = strings.NewReplacer("'", "", "’", "").Replace(word)
	n := len(word)
	// A final e after a consonant is silent when the word has another vowel.
	silentE := n > 2 && word[n-1] == 'e' && !isLatinVowel(word[n-2]) &&
		strings.ContainsAny(word[:n-2], "aeiouy")

	var out strings.Builder
	for i := 0; i < n; {
		rest := word[i:]
		if i == 0 {
			if to, size := matchGroup(latinStarts, rest); size > 0 {
				out.WriteString(to)
				i += size
				continue
			}
		}
		if to, size := matchGroup(latinGroups, rest); size > 0 {
			out.WriteString(to)
			i += size
			continue
		}
		c := word[i]
		switch {
		case i == n-1 && c == 'e' && silentE:
			// silent
		case silentE && i == n-3 && longVowels[c] != "" && (i == 0 || !isLatinVowel(word[i-1])):
			out.WriteString(longVowels[c])
		case c == 'c':
			if i+1 < n && strings.IndexByte("eiy", word[i+1]) >= 0 {
				out.WriteString("с")
			} else {
				out.WriteString("к")
			}
		case c == 'e':
			if i == 0 {
				out.WriteString("э")
			} else {
				out.WriteString("е")
			}
		case c == 'u':
			// Short u before a consonant that closes the syllable: "club".
			closed := i+1 < n && !isLatinVowel(word[i+1]) && (i+2 >= n || !isLatinVowel(word[i+2]))
			if closed {
				out.WriteString("а")
			} else {
				out.WriteString("у")
			}
		case c == 'y':
			if i == 0 && n > 1 && isLatinVowel(word[1]) {
				out.WriteString("й")
			} else {
				out.WriteString("и")
			}
		case i > 0 && c == word[i-1] && !isLatinVowel(c):
			// a doubled consonant is read once
		default:
			out.WriteString(latinLetters[c])
		}
		i++
	}
	return out.String()
}

func matchGroup(groups []struct{ from, to string }, s string) (string, int) {
	for _, g := range groups {
		if strings.HasPrefix(s, g.from) {
			return g.to, len(g.from)
		}
	}
	return "", 0
}
