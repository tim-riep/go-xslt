package xpath

import (
	"regexp"
	"testing"
	"unicode/utf8"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// TestNameClassesMatchXMLTreePredicates checks, for every code point, that
// the character classes xs:NCName / xs:Name casting and the \i \c regex escapes
// are built from, and the lexer's isNameStart / isNameChar, say what
// xmltree's predicates (the one definition of an XML name) say.
func TestNameClassesMatchXMLTreePredicates(t *testing.T) {
	ncStart := regexp.MustCompile("^[" + NCNameStartClass + "]$")
	ncChar := regexp.MustCompile("^[" + NCNameCharClass + "]$")
	nStart := regexp.MustCompile("^[" + NameStartClass + "]$")
	nChar := regexp.MustCompile("^[" + NameCharClass + "]$")
	for r := rune(0); r <= 0x10FFFF; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue // not representable in a Go string
		}
		s := string(r)
		if utf8.RuneLen(r) < 0 {
			continue
		}
		check := func(what string, got, want bool) {
			if got != want {
				t.Fatalf("%s(%U) = %v, xmltree says %v", what, r, got, want)
			}
		}
		check("NCNameStartClass", ncStart.MatchString(s), xmltree.IsNCNameStartChar(r))
		check("NCNameCharClass", ncChar.MatchString(s), xmltree.IsNCNameChar(r))
		check("NameStartClass", nStart.MatchString(s), xmltree.IsNameStartChar(r))
		check("NameCharClass", nChar.MatchString(s), xmltree.IsNameChar(r))
		check("isNameStart", isNameStart(r), xmltree.IsNameStartChar(r))
		check("isNameChar", isNameChar(r), xmltree.IsNameChar(r))
	}
}

func TestNCNameCastMatchesPredicate(t *testing.T) {
	for _, s := range []string{
		"\U0001F600", "\u09EA", "a\U0001F600", "\U00010000x", "a", "1a", "a\uFFFE",
		"\u0300a", "a\u0300", "a:b", "", "-a", "a-", "\u00D7", "x\u00B7",
	} {
		_, err := CastTo(NewString(s), XSncname)
		if got, want := err == nil, xmltree.IsNCName(s); got != want {
			t.Errorf("cast %q to xs:NCName: ok=%v, IsNCName=%v", s, got, want)
		}
	}
}
