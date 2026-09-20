package xpath

import "testing"

func TestFnStringFamily(t *testing.T) {
	cases := []struct{ expr, want string }{
		// string-to-codepoints: ToString returns the first item's lexical.
		{"string-to-codepoints('ABC')", "65"},
		{"string-to-codepoints('a')", "97"},
		{"string-to-codepoints('')", ""},
		// codepoints-to-string: build string from code points.
		{"codepoints-to-string((72, 105))", "Hi"},
		{"codepoints-to-string(string-to-codepoints('ABC'))", "ABC"},
		{"codepoints-to-string(())", ""},
		// compare: -1 / 0 / 1, empty when either arg empty.
		{"compare('a', 'b')", "-1"},
		{"compare('b', 'a')", "1"},
		{"compare('a', 'a')", "0"},
		{"compare((), 'a')", ""},
		{"compare('a', ())", ""},
		// codepoint-equal: boolean, empty when either arg empty.
		{"codepoint-equal('abc', 'abc')", "true"},
		{"codepoint-equal('abc', 'abd')", "false"},
		{"codepoint-equal((), 'a')", ""},
		// normalize-unicode: returns input unchanged; accepts form arg.
		{"normalize-unicode('café')", "café"},
		{"normalize-unicode('abc', 'NFC')", "abc"},
		{"normalize-unicode('')", ""},
	}
	for _, c := range cases {
		if got := xpStr(t, c.expr); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}
