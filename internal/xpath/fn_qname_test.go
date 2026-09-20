package xpath

import "testing"

// TestFnQNameNoCtx exercises the qname-family functions that do not need an
// element namespace context (evaluated against the default <r/> document).
func TestFnQNameNoCtx(t *testing.T) {
	cases := []struct{ expr, want string }{
		// fn:QName builds a QName; its string value is the lexical form.
		{"QName('http://example.com/ns', 'p:local')", "p:local"},
		{"QName('http://example.com/ns', 'local')", "local"},
		{"string(QName('urn:x', 'a:b'))", "a:b"},

		// prefix-from-QName
		{"prefix-from-QName(QName('urn:x', 'p:local'))", "p"},
		{"string-length(string(prefix-from-QName(QName('urn:x', 'local'))))", "0"},

		// local-name-from-QName
		{"local-name-from-QName(QName('urn:x', 'p:local'))", "local"},
		{"local-name-from-QName(QName('', 'bare'))", "bare"},

		// namespace-uri-from-QName
		{"namespace-uri-from-QName(QName('http://example.com/ns', 'p:local'))", "http://example.com/ns"},
		{"string-length(namespace-uri-from-QName(QName('', 'bare')))", "0"},

		// empty-sequence handling on the accessors
		{"string-length(string(prefix-from-QName(())))", "0"},
		{"string-length(string(local-name-from-QName(())))", "0"},
		{"string-length(string(namespace-uri-from-QName(())))", "0"},
	}
	for _, c := range cases {
		if got := xpStr(t, c.expr); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}

// TestFnQNameCtx exercises the functions that resolve against an element's
// in-scope namespaces.
func TestFnQNameCtx(t *testing.T) {
	const doc = `<r xmlns:p="urn:p" xmlns="urn:default"><c/></r>`
	cases := []struct{ expr, xml, want string }{
		// namespace-uri-for-prefix resolves a declared prefix.
		{"namespace-uri-for-prefix('p', /*)", doc, "urn:p"},
		// The default namespace is bound to the empty prefix.
		{"namespace-uri-for-prefix('', /*)", doc, "urn:default"},
		// Unbound prefix -> empty sequence -> empty string.
		{"string-length(string(namespace-uri-for-prefix('zzz', /*)))", doc, "0"},

		// in-scope-prefixes yields the declared prefixes plus "xml"; join them
		// in sorted order for a deterministic comparison.
		{"string-join(in-scope-prefixes(/*), ',')", doc, ",p,xml"},

		// resolve-QName uses the element's in-scope namespaces.
		{"resolve-QName('p:thing', /*)", doc, "p:thing"},
		{"namespace-uri-from-QName(resolve-QName('p:thing', /*))", doc, "urn:p"},
		{"local-name-from-QName(resolve-QName('p:thing', /*))", doc, "thing"},
		// Unprefixed name resolves to the default-element namespace.
		{"namespace-uri-from-QName(resolve-QName('plain', /*))", doc, "urn:default"},
		// Empty-sequence input -> empty sequence.
		{"string-length(string(resolve-QName((), /*)))", doc, "0"},
	}
	for _, c := range cases {
		if got := xpStrCtx(t, c.expr, c.xml); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}
