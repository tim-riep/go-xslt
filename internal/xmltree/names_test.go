package xmltree

import "testing"

// The XML 1.0 (5th edition) \u00A72.3 productions, transcribed from the
// specification independently of the tables in names.go.
func specNameStart(r rune) bool {
	switch {
	case r == ':', r >= 'A' && r <= 'Z', r == '_', r >= 'a' && r <= 'z',
		r >= 0xC0 && r <= 0xD6, r >= 0xD8 && r <= 0xF6, r >= 0xF8 && r <= 0x2FF,
		r >= 0x370 && r <= 0x37D, r >= 0x37F && r <= 0x1FFF,
		r >= 0x200C && r <= 0x200D, r >= 0x2070 && r <= 0x218F,
		r >= 0x2C00 && r <= 0x2FEF, r >= 0x3001 && r <= 0xD7FF,
		r >= 0xF900 && r <= 0xFDCF, r >= 0xFDF0 && r <= 0xFFFD,
		r >= 0x10000 && r <= 0xEFFFF:
		return true
	}
	return false
}

func specNameChar(r rune) bool {
	return specNameStart(r) || r == '-' || r == '.' || r >= '0' && r <= '9' ||
		r == 0xB7 || r >= 0x300 && r <= 0x36F || r >= 0x203F && r <= 0x2040
}

func TestNamePredicatesMatchSpec(t *testing.T) {
	for r := rune(0); r <= 0x10FFFF; r++ {
		if got, want := IsNameStartChar(r), specNameStart(r); got != want {
			t.Fatalf("IsNameStartChar(%U) = %v, spec says %v", r, got, want)
		}
		if got, want := IsNameChar(r), specNameChar(r); got != want {
			t.Fatalf("IsNameChar(%U) = %v, spec says %v", r, got, want)
		}
		if got, want := IsNCNameStartChar(r), specNameStart(r) && r != ':'; got != want {
			t.Fatalf("IsNCNameStartChar(%U) = %v, want %v", r, got, want)
		}
		if got, want := IsNCNameChar(r), specNameChar(r) && r != ':'; got != want {
			t.Fatalf("IsNCNameChar(%U) = %v, want %v", r, got, want)
		}
	}
}

// Every rune the tokenizer (encoding/xml's 4th-edition tables) accepts in a
// name must be a 5th-edition name character too, or the repair pre-pass could
// never be needed in one direction only.
func TestTokenizerNamesAreFifthEditionNames(t *testing.T) {
	for r := rune(0x80); r <= 0xFFFF; r++ {
		if inNameRanges(nameStartRanges, r) && !IsNameStartChar(r) {
			t.Errorf("tokenizer starts a name with %U, the 5th edition does not", r)
		}
		if inNameRanges(nameCharRanges, r) && !IsNameChar(r) {
			t.Errorf("tokenizer continues a name with %U, the 5th edition does not", r)
		}
	}
}

func TestNameStrings(t *testing.T) {
	for _, c := range []struct {
		s        string
		ncn, nam bool
	}{
		{"a", true, true}, {"_x.y-z", true, true}, {"a:b", false, true},
		{":a", false, true}, {"", false, false}, {"1a", false, false},
		{"-a", false, false}, {".a", false, false}, {"a b", false, false},
		{"\U0001F600", true, true}, {"\u09EA", true, true},
		{"a\U0001F600", true, true}, {"\U00010000x", true, true},
		{"x\u09EA", true, true}, {"\u0300a", false, false}, {"a\u0300", true, true},
		{"a\uFFFE", false, false}, {"a\uFFFF", false, false},
		{"\U000F0000", false, false}, {"\u00D7", false, false},
		{"\u00B5", true, true}, // U+00B5 MICRO SIGN lies in [#xF8-#x2FF]? no: in [#xD8-#xF6]'s gap
		{"a\xff", false, false},
	} {
		if c.s == "\u00B5" {
			// 0xB5 is below 0xC0: not a NameStartChar.
			c.ncn, c.nam = false, false
		}
		if got := IsNCName(c.s); got != c.ncn {
			t.Errorf("IsNCName(%q) = %v, want %v", c.s, got, c.ncn)
		}
		if got := IsName(c.s); got != c.nam {
			t.Errorf("IsName(%q) = %v, want %v", c.s, got, c.nam)
		}
	}
}
