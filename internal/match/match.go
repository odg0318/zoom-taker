// Package match provides the text similarity primitive used by the
// ME/REMOTE bleed filter: punctuation/whitespace normalization followed by a
// difflib-style SequenceMatcher ratio over runes. Runes (not bytes) matter —
// Korean text is multi-byte and a byte-based ratio would silently diverge
// from the Python original.
package match

import (
	"unicode"

	"github.com/pmezard/go-difflib/difflib"
)

// Normalize strips whitespace and punctuation the way the Python pipeline
// did (re.sub(r"[\s.,!?~'\"…]+", "", text)), preserving case. Unicode's
// whitespace set stands in for the regex \s class.
func Normalize(text string) string {
	out := make([]rune, 0, len(text))
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		switch r {
		case '.', ',', '!', '?', '~', '\'', '"', '…':
		default:
			out = append(out, r)
		}
	}
	return string(out)
}

// Ratio compares a and b after normalization and returns the difflib
// SequenceMatcher ratio in [0, 1]. go-difflib works on element slices, so a
// sequence is fed one rune per element (not bytes — Korean is multi-byte).
// Both empty inputs yield 1.0, matching Python's difflib.
func Ratio(a, b string) float64 {
	elems := func(s string) []string {
		rs := []rune(s)
		out := make([]string, len(rs))
		for i, r := range rs {
			out[i] = string(r)
		}
		return out
	}
	m := difflib.NewMatcher(elems(Normalize(a)), elems(Normalize(b)))
	return m.Ratio()
}
