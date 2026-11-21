package workspace

import (
	"strings"
	"unicode"
)

// maxSlugRunes limits the length of a working-folder name.
const maxSlugRunes = 64

// Slug turns a file or book name into a working-folder name: lower case,
// letters and digits of any script are kept, every other run of characters
// becomes a single "-". The result may be empty when name has no letters or
// digits.
func Slug(name string) string {
	var b strings.Builder
	n := 0
	pendingDash := false
	for _, r := range strings.ToLower(name) {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			pendingDash = true
			continue
		}
		if pendingDash && n > 0 {
			if n+1 >= maxSlugRunes {
				break
			}
			b.WriteByte('-')
			n++
		}
		pendingDash = false
		if n >= maxSlugRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
