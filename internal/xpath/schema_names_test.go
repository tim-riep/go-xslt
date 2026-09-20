package xpath

import (
	"strings"
	"testing"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// fakeSchema stands in for a host's compiled schema: a flat component table
// plus one derivation edge. It is deliberately NOT internal/xsd — the point of
// these tests is that the wiring between a parsed type test and a node's
// annotation is correct on its own, independent of whatever validator supplies
// the components.
type fakeSchema struct {
	types  map[string]xmltree.SchemaTypeName
	elems  map[string]xmltree.SchemaTypeName
	attrs  map[string]xmltree.SchemaTypeName
	derive map[xmltree.SchemaTypeName]xmltree.SchemaTypeName // subtype -> its base
}

const fakeNS = "http://example.com/ns"

func newFakeSchema() *fakeSchema {
	base := xmltree.SchemaTypeName{Namespace: fakeNS, Local: "BaseType", Complex: true}
	sub := xmltree.SchemaTypeName{Namespace: fakeNS, Local: "SubType", Complex: true}
	strLike := xmltree.SchemaTypeName{Namespace: fakeNS, Local: "Code"}
	return &fakeSchema{
		types:  map[string]xmltree.SchemaTypeName{"BaseType": base, "SubType": sub, "Code": strLike},
		elems:  map[string]xmltree.SchemaTypeName{"item": base},
		attrs:  map[string]xmltree.SchemaTypeName{"code": strLike},
		derive: map[xmltree.SchemaTypeName]xmltree.SchemaTypeName{sub: base},
	}
}

// expand mimics the host's prefix resolution: only "my" is bound.
func (f *fakeSchema) expand(lexical string) (string, bool) {
	if !strings.HasPrefix(lexical, "my:") {
		return "", false
	}
	return strings.TrimPrefix(lexical, "my:"), true
}

func (f *fakeSchema) LookupSchemaType(lexical string) (xmltree.SchemaTypeName, bool) {
	local, ok := f.expand(lexical)
	if !ok {
		return xmltree.SchemaTypeName{}, false
	}
	t, found := f.types[local]
	return t, found
}

func (f *fakeSchema) LookupSchemaElement(lexical string) (xmltree.Name, xmltree.SchemaTypeName, bool) {
	local, ok := f.expand(lexical)
	if !ok {
		return xmltree.Name{}, xmltree.SchemaTypeName{}, false
	}
	t, found := f.elems[local]
	return xmltree.Name{Space: fakeNS, Local: local}, t, found
}

func (f *fakeSchema) LookupSchemaAttribute(lexical string) (xmltree.Name, xmltree.SchemaTypeName, bool) {
	local, ok := f.expand(lexical)
	if !ok {
		return xmltree.Name{}, xmltree.SchemaTypeName{}, false
	}
	t, found := f.attrs[local]
	return xmltree.Name{Space: fakeNS, Local: local}, t, found
}

func (f *fakeSchema) DerivesFrom(got, want xmltree.SchemaTypeName) bool {
	for cur := got; ; {
		if cur == want {
			return true
		}
		next, ok := f.derive[cur]
		if !ok {
			return false
		}
		cur = next
	}
}

// fakeNSResolver binds the one prefix the test expressions use.
type fakeNSResolver struct{}

func (fakeNSResolver) ResolveNS(prefix string) (string, bool) {
	if prefix == "my" {
		return fakeNS, true
	}
	return "", false
}

func schemaTestCtx(n *xmltree.Node, f *fakeSchema) *Context {
	c := &Context{Node: n, Pos: 1, Size: 1, NS: fakeNSResolver{}}
	if f != nil {
		c.SchemaTypes = f
	}
	return c
}

// annotatedElement builds <my:item/> carrying the given type annotation, the
// shape internal/xsd's validate-in-place bridge will produce.
func annotatedElement(local string, t *xmltree.SchemaTypeName) *xmltree.Node {
	doc := &xmltree.Node{Kind: xmltree.KindDocument}
	el := xmltree.NewElement(xmltree.Name{Space: fakeNS, Local: local, Prefix: "my"})
	el.SchemaType = t
	doc.Append(el)
	return el
}

// TestSchemaTypeTestResolution is the core wiring proof: a user-defined type
// name in element(N,T) is resolved ONCE at parse time and then matched by
// identity and derivation against the node's own annotation.
func TestSchemaTypeTestResolution(t *testing.T) {
	f := newFakeSchema()
	base := f.types["BaseType"]
	sub := f.types["SubType"]
	other := xmltree.SchemaTypeName{Namespace: fakeNS, Local: "Unrelated", Complex: true}

	st, err := ParseSeqTypeStringSchemaAware("element(my:item, my:BaseType)", f)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if st.Item.Node == nil || st.Item.Node.SchemaType == nil {
		t.Fatalf("the type name was not resolved into the node test")
	}
	if *st.Item.Node.SchemaType != base {
		t.Fatalf("resolved to %+v, want %+v", *st.Item.Node.SchemaType, base)
	}

	cases := []struct {
		name string
		anno *xmltree.SchemaTypeName
		want bool
	}{
		{"exactly the named type", &base, true},
		{"a subtype of it", &sub, true},
		{"an unrelated type", &other, false},
		{"unannotated (nothing has validated it)", nil, false},
	}
	for _, tc := range cases {
		el := annotatedElement("item", tc.anno)
		if got := MatchesItemTypeOf(st, el, schemaTestCtx(el, f)); got != tc.want {
			t.Errorf("%s: instance of element(my:item, my:BaseType) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestSchemaTypeTestFailsClosed pins the two ways the match must answer false
// rather than guess: no derivation resolver at all, and a type name the host
// does not know.
func TestSchemaTypeTestFailsClosed(t *testing.T) {
	f := newFakeSchema()
	sub := f.types["SubType"]

	st, err := ParseSeqTypeStringSchemaAware("element(my:item, my:BaseType)", f)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// A subtype matches only because the resolver says so; with no resolver
	// installed nothing is known to derive from anything.
	el := annotatedElement("item", &sub)
	if MatchesItemTypeOf(st, el, schemaTestCtx(el, nil)) {
		t.Error("a derivation matched with no SchemaTypeResolver installed")
	}

	// An unknown name resolves to nothing, leaving the built-in-only reading,
	// which accounts for no user type and therefore matches nothing.
	st2, err := ParseSeqTypeStringSchemaAware("element(my:item, my:NoSuchType)", f)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if st2.Item.Node.SchemaType != nil {
		t.Fatal("an unknown type name was resolved")
	}
	for _, anno := range []*xmltree.SchemaTypeName{nil, &sub} {
		el := annotatedElement("item", anno)
		if MatchesItemTypeOf(st2, el, schemaTestCtx(el, f)) {
			t.Error("element(my:item, my:NoSuchType) matched something")
		}
	}
}

// TestSchemaElementTest covers schema-element()/schema-attribute(): rejected
// outright without a schema (the unchanged default), resolved to the
// declaration's expanded name and declared type with one.
func TestSchemaElementTest(t *testing.T) {
	f := newFakeSchema()

	if _, err := Parse("schema-element(my:item)"); err == nil ||
		!strings.Contains(err.Error(), "XPST0008") {
		t.Errorf("schema-element with no schema in scope: err = %v, want XPST0008", err)
	}
	if _, err := ParseSchemaAware("schema-element(my:nope)", f); err == nil ||
		!strings.Contains(err.Error(), "XPST0008") {
		t.Errorf("schema-element naming no declaration: err = %v, want XPST0008", err)
	}

	st, err := ParseSeqTypeStringSchemaAware("schema-element(my:item)", f)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if st.Item.Node == nil || st.Item.Node.Local != "item" || st.Item.Node.URI != fakeNS {
		t.Fatalf("declaration name not resolved into the test: %+v", st.Item.Node)
	}
	base, sub := f.types["BaseType"], f.types["SubType"]
	for _, tc := range []struct {
		anno *xmltree.SchemaTypeName
		want bool
	}{{&base, true}, {&sub, true}, {nil, false}} {
		el := annotatedElement("item", tc.anno)
		if got := MatchesItemTypeOf(st, el, schemaTestCtx(el, f)); got != tc.want {
			t.Errorf("schema-element(my:item) against %v = %v, want %v", tc.anno, got, tc.want)
		}
	}

	// A wrong NAME is rejected even when the type would derive correctly.
	el := annotatedElement("other", &base)
	if MatchesItemTypeOf(st, el, schemaTestCtx(el, f)) {
		t.Error("schema-element(my:item) matched an element with a different name")
	}
}

// TestSchemaNamesInPathAndPattern checks the same resolution reaches an
// ordinary location step and an XSLT match pattern, not just a sequence type.
func TestSchemaNamesInPathAndPattern(t *testing.T) {
	f := newFakeSchema()
	base := f.types["BaseType"]

	doc := &xmltree.Node{Kind: xmltree.KindDocument}
	root := xmltree.NewElement(xmltree.Name{Local: "root"})
	doc.Append(root)
	typed := xmltree.NewElement(xmltree.Name{Space: fakeNS, Local: "item", Prefix: "my"})
	typed.SchemaType = &base
	root.Append(typed)
	plain := xmltree.NewElement(xmltree.Name{Space: fakeNS, Local: "item", Prefix: "my"})
	root.Append(plain)

	p, err := ParseSchemaAware("count(my:item[. instance of element(my:item, my:BaseType)])", f)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	v, err := p.Eval(schemaTestCtx(root, f))
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if n := ToNumber(v); n != 1 {
		t.Errorf("annotated children counted = %v, want 1", n)
	}

	pat, err := ParsePatternSchemaAware("schema-element(my:item)", f)
	if err != nil {
		t.Fatalf("pattern: %v", err)
	}
	ok, err := pat.Match(typed, schemaTestCtx(typed, f))
	if err != nil || !ok {
		t.Errorf("pattern against the annotated element = %v (%v), want true", ok, err)
	}
	ok, err = pat.Match(plain, schemaTestCtx(plain, f))
	if err != nil || ok {
		t.Errorf("pattern against the unannotated element = %v (%v), want false", ok, err)
	}
}

// TestSchemaUnawareParseUnchanged pins that nothing above alters an ordinary
// parse: no lookup means no resolved type, and the built-in reading of
// element(N,T) is the one that applies.
func TestSchemaUnawareParseUnchanged(t *testing.T) {
	st, err := ParseSeqTypeString("element(e, xs:anyType)")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if st.Item.Node.SchemaType != nil {
		t.Error("a schema type was resolved with no lookup supplied")
	}
	doc := &xmltree.Node{Kind: xmltree.KindDocument}
	el := xmltree.NewElement(xmltree.Name{Local: "e"})
	doc.Append(el)
	if !MatchesItemTypeOf(st, el, schemaTestCtx(el, nil)) {
		t.Error("element(e, xs:anyType) no longer matches a plain element")
	}
}
