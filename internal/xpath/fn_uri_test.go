package xpath

import "testing"

func TestFnURI(t *testing.T) {
	cases := []struct{ expr, want string }{
		// encode-for-uri: space and reserved chars escaped, unreserved kept.
		{"fn:encode-for-uri('http://www.example.com/00/Weather/CA/Los%20Angeles#ocean')",
			"http%3A%2F%2Fwww.example.com%2F00%2FWeather%2FCA%2FLos%2520Angeles%23ocean"},
		{"fn:encode-for-uri('a b')", "a%20b"},
		{"fn:encode-for-uri('-_.~')", "-_.~"},
		{"fn:encode-for-uri('')", ""},

		// iri-to-uri: reserved/delimiter chars preserved, spaces escaped.
		{"fn:iri-to-uri('http://www.example.com/?a=b&c=d')",
			"http://www.example.com/?a=b&c=d"},
		{"fn:iri-to-uri('http://example.com/a b')", "http://example.com/a%20b"},

		// escape-html-uri: only chars outside 32..126 are escaped.
		{"fn:escape-html-uri('http://www.example.com/00/Weather/CA/Los Angeles#ocean')",
			"http://www.example.com/00/Weather/CA/Los Angeles#ocean"},
		{"fn:escape-html-uri('a\tb')", "a%09b"},

		// resolve-uri with explicit base.
		{"fn:resolve-uri('b/c.txt', 'http://a/dir/file')", "http://a/dir/b/c.txt"},
		{"fn:resolve-uri('http://x/y', 'http://a/b')", "http://x/y"},
		{"fn:resolve-uri('../g', 'http://a/b/c/d')", "http://a/b/g"},
		// single-arg form: no static base, empty sequence.
		{"fn:resolve-uri('b/c.txt')", ""},
	}
	for _, c := range cases {
		if got := xpStr(t, c.expr); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}
