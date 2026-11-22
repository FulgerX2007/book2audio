package speech

import "testing"

func TestRussianizeLatin(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain word", "sandbox", "сандбокс"},
		{"sh and oo", "shook", "шук"},
		{"th and ee", "three", "три"},
		{"ch and doubled consonant", "chess", "чес"},
		{"ph", "phone", "фоун"},
		{"silent e with a long vowel", "line", "лайн"},
		{"silent e, long a", "game", "гэйм"},
		{"final e kept in a short word", "be", "бе"},
		{"c before e and before a", "center cat", "сентер кат"},
		{"short u", "club", "клаб"},
		{"open u", "super", "супер"},
		{"qu and ck", "quick", "квик"},
		{"tion", "station", "сташн"},
		{"igh", "night", "найт"},
		{"kn at the start", "know", "ноу"},
		{"y at the start and the end", "yes city", "йес сити"},
		{"x", "box", "бокс"},
		{"capitalised name", "London", "лондон"},
		{"apostrophe", "don't", "донт"},
		{"acronym of three letters", "FBI", "эф би ай"},
		{"acronym without vowels", "HTTP", "эйч ти ти пи"},
		{"long capitals with vowels are a word", "NATO", "нато"},
		{"single capital is named", "Plan B and I", "план би анд ай"},
		{"single small letter is read", "a b", "а б"},
		{"Russian text is left alone", "Привет, мир! 1997", "Привет, мир! 1997"},
		{"mixed sentence keeps punctuation", "Компания Apple (NSA) и «Box».", "Компания апл (эн эс эй) и «бокс»."},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RussianizeLatin(tt.in); got != tt.want {
				t.Errorf("RussianizeLatin(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestHasLatin(t *testing.T) {
	for text, want := range map[string]bool{
		"Привет":        false,
		"Привет, world": true,
		"1997 — 2010.":  false,
		"":              false,
		"x":             true,
	} {
		if got := HasLatin(text); got != want {
			t.Errorf("HasLatin(%q) = %v, want %v", text, got, want)
		}
	}
}
