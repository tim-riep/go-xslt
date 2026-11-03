package xpath

import (
	"math/rand"
	"strings"
	"testing"
	"unicode"
)

// The alphabet mixes every kind of whitespace strings.Fields knows (ASCII,
// and Unicode spaces such as U+00A0 and U+2003) with letters of one to four
// bytes.
var fastpathAlphabet = []rune{' ', ' ', ' ', '\t', '\n', '\v', '\f', '\r', '\u0085', ' ', ' ', '　',
	'a', 'b', 'z', 'A', 'Z', '0', '9', 'é', 'ß', 'İ', 'ǅ', 'ﬁ', 'Ϳ', 'ϳ', '日', '😀'}

func randomString(r *rand.Rand, alphabet []rune, maxLen int) string {
	n := r.Intn(maxLen + 1)
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteRune(alphabet[r.Intn(len(alphabet))])
	}
	return b.String()
}

// TestNormalizeSpaceMatchesFields pins normalizeSpace to the definition it
// replaced, strings.Join(strings.Fields(s), " "), over random input.
func TestNormalizeSpaceMatchesFields(t *testing.T) {
	r := rand.New(rand.NewSource(8))
	for i := 0; i < 200000; i++ {
		s := randomString(r, fastpathAlphabet, 12)
		if got, want := normalizeSpace(s), strings.Join(strings.Fields(s), " "); got != want {
			t.Fatalf("normalizeSpace(%q) = %q, want %q", s, got, want)
		}
	}
	for _, s := range []string{"", " ", "a", " a", "a ", "a b", "a  b", "a\tb", " a ", "a b", "\xff a \xff"} {
		if got, want := normalizeSpace(s), strings.Join(strings.Fields(s), " "); got != want {
			t.Fatalf("normalizeSpace(%q) = %q, want %q", s, got, want)
		}
	}
}

// TestCaseMapASCIIMatchesFull pins the ASCII fast path of upper-case and
// lower-case to the full Unicode mapping it short-cuts.
func TestCaseMapASCIIMatchesFull(t *testing.T) {
	ascii := make([]rune, 0, 128)
	for c := rune(0); c < 128; c++ {
		ascii = append(ascii, c)
	}
	r := rand.New(rand.NewSource(8))
	for i := 0; i < 200000; i++ {
		s := randomString(r, ascii, 10)
		if got, want := upperCaseFull(s), mapCase(s, specialUpper, unicode.ToUpper); got != want {
			t.Fatalf("upper-case(%q) = %q, want %q", s, got, want)
		}
		if got, want := lowerCaseFull(s), mapCase(s, specialLower, unicode.ToLower); got != want {
			t.Fatalf("lower-case(%q) = %q, want %q", s, got, want)
		}
	}
	for _, s := range []string{"ß", "ﬁ", "İstanbul", "aͿb", "ǅ"} {
		if got, want := upperCaseFull(s), mapCase(s, specialUpper, unicode.ToUpper); got != want {
			t.Fatalf("upper-case(%q) = %q, want %q", s, got, want)
		}
	}
}
