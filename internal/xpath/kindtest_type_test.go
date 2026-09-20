package xpath

import (
	"testing"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

type testNSMap map[string]string

func (m testNSMap) ResolveNS(prefix string) (string, bool) { u, ok := m[prefix]; return u, ok }

// kindTestTypeOK used to match an element(name, TYPE) test by the TYPE's LOCAL
// name alone, so p:untyped matched whatever p was bound to — including a
// namespace that has no such type, and (once schema components exist) a
// user-defined type that merely shares a built-in's local name.
func TestKindTestTypeNamespace(t *testing.T) {
	ns := testNSMap{
		"xsd":   "http://www.w3.org/2001/XMLSchema",
		"other": "http://example.com/types",
	}
	doc, err := xmltree.Parse(`<r><e a="1"/></r>`)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		expr string
		want string
	}{
		// The XSD namespace, however it is spelled, still matches.
		{"count(//e[. instance of element(*, xs:untyped)])", "1"},
		{"count(//e[. instance of element(*, xsd:untyped)])", "1"},
		{"count(//e[. instance of element(*, Q{http://www.w3.org/2001/XMLSchema}untyped)])", "1"},
		{"count(//e/@a[. instance of attribute(*, xs:untypedAtomic)])", "1"},
		// Some other namespace names no type this processor knows.
		{"count(//e[. instance of element(*, other:untyped)])", "0"},
		{"count(//e[. instance of element(*, Q{http://example.com/types}untyped)])", "0"},
		{"count(//e[. instance of element(*, Q{http://example.com/types}anyType)])", "0"},
		{"count(//e/@a[. instance of attribute(*, other:untypedAtomic)])", "0"},
		// An unbound prefix names nothing at all.
		{"count(//e[. instance of element(*, nobody:untyped)])", "0"},
	}
	for _, c := range cases {
		p, err := Parse(c.expr)
		if err != nil {
			t.Errorf("parse %q: %v", c.expr, err)
			continue
		}
		v, err := p.Eval(&Context{Node: doc, Pos: 1, Size: 1, NS: ns})
		if err != nil {
			t.Errorf("eval %q: %v", c.expr, err)
			continue
		}
		if got := ToString(v); got != c.want {
			t.Errorf("%s = %s, want %s", c.expr, got, c.want)
		}
	}
}
