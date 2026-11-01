package xpath

import (
	"math"
	"math/big"
	"testing"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// TestTyCanonicalLexical checks the canonical lexical forms of F&O 3.1
// §19.1.2.1 (casting numerics to xs:string): xs:double/xs:float use decimal
// notation for 1e-6 <= |x| < 1e6 and otherwise a mantissa with a decimal
// point and a bare exponent; xs:decimal has no exponent and no superfluous
// trailing zeros.
func TestTyCanonicalLexical(t *testing.T) {
	tyCheck(t, []tyCase{
		{`string(xs:double('1e6'))`, "1.0E6"},
		{`string(xs:double('1.5e-7'))`, "1.5E-7"},
		{`string(xs:double('-2.5e10'))`, "-2.5E10"},
		{`string(xs:double('123456.5'))`, "123456.5"},
		{`string(xs:float('1e7'))`, "1.0E7"},
		{`string(xs:double('-0'))`, "-0"},
		{`string(xs:double('0'))`, "0"},
		{`string(xs:double('INF'))`, "INF"},
		{`string(xs:double('-INF'))`, "-INF"},
		{`string(xs:decimal('1.50'))`, "1.5"},
		{`string(xs:decimal('-0.000000000000000000000000019999'))`, "-0.000000000000000000000000019999"},
		{`string(xs:decimal('100'))`, "100"},
		{`string(xs:decimal('0.125'))`, "0.125"},
	})
}

// TestTyMapSameKey checks op:same-key (F&O 3.1 §17.1.1), which decides
// whether two atomic values are the same map key.
func TestTyMapSameKey(t *testing.T) {
	tyCheck(t, []tyCase{
		// Numerics compare in the exact value space; NaN is the same key as NaN.
		{`map{xs:double('NaN'): 1}(xs:float('NaN'))`, "1"},
		{`map{xs:double('INF'): 'p', xs:double('-INF'): 'n'}(xs:float('-INF'))`, "n"},
		{`map{xs:double('INF'): 'p', xs:double('-INF'): 'n'}(xs:float('INF'))`, "p"},
		{`map{1: 'a'}(1.0)`, "a"},
		{`map{1: 'a'}(xs:double(1))`, "a"},
		{`map{1.5: 'a'}(xs:float(1.5))`, "a"},
		{`count(map{xs:double('1.1'): 'd'}(1.1))`, "0"},
		{`map:size(map{1: 'a', 1.0: 'b'})`, "!XQDY0137"}, // duplicate keys in a constructor (XPath 3.1 §3.11.1.1)
		// Booleans are not numbers.
		{`map{true(): 'y'}(true())`, "y"},
		{`map{false(): 'n'}(false())`, "n"},
		{`count(map{true(): 'y'}(1))`, "0"},
		// QNames compare by expanded name, not prefix.
		{`map{QName('urn:a', 'p:x'): 1}(QName('urn:a', 'q:x'))`, "1"},
		{`count(map{QName('urn:a', 'x'): 1}(QName('urn:b', 'x')))`, "0"},
		{`count(map{QName('urn:a', 'x'): 1}('x'))`, "0"},
		// xs:string, xs:anyURI and xs:untypedAtomic compare by codepoints.
		{`map{'a': 1}(xs:anyURI('a'))`, "1"},
		{`map{'a': 1}(xs:untypedAtomic('a'))`, "1"},
		{`map{'a': 1}(xs:token('a'))`, "1"},
		{`count(map{'1': 1}(1))`, "0"},
		// Durations by value, across subtypes.
		{`map{xs:yearMonthDuration('P1Y'): 1}(xs:duration('P12M'))`, "1"},
		{`map{xs:dayTimeDuration('PT60S'): 1}(xs:duration('PT1M'))`, "1"},
		{`count(map{xs:dayTimeDuration('PT1S'): 1}(xs:dayTimeDuration('PT2S')))`, "0"},
		// Date/time values: same instant and same timezone presence.
		{`map{xs:time('17:00:00Z'): 1}(xs:time('12:00:00-05:00'))`, "1"},
		{`map{xs:date('2000-01-01'): 1}(xs:date('2000-01-01'))`, "1"},
		{`count(map{xs:date('2000-01-01'): 1}(xs:date('2000-01-01Z')))`, "0"},
		{`count(map{xs:date('2000-01-01'): 1}(xs:dateTime('2000-01-01T00:00:00')))`, "0"},
		{`map{xs:dateTimeStamp('2000-01-01T00:00:00Z'): 1}(xs:dateTime('2000-01-01T00:00:00Z'))`, "1"},
		// Binary values: same type and same octets.
		{`map{xs:hexBinary('0F'): 1}(xs:hexBinary('0f'))`, "1"},
		{`count(map{xs:hexBinary('0F'): 1}(xs:base64Binary('Dw==')))`, "0"},
		{`map{xs:base64Binary('Dw=='): 1}(xs:base64Binary('Dw=='))`, "1"},
		// map:put with a same key replaces the entry, key included
		// (F&O 3.1 §17.1.6).
		{`map:keys(map:put(map{3: 'a'}, xs:float(3), 'b')) instance of xs:float`, "true"},
		{`map:put(map{3: 'a'}, xs:float(3), 'b')(3)`, "b"},
		{`map:size(map:put(map{3: 'a'}, 3.0, 'b'))`, "1"},
	})

	// The Go-level API agrees with the same rule.
	m := NewMap()
	m.Put(NewInteger(1), NewString("a"))
	m.Put(1.0, NewString("b")) // a bare float64 key is an xs:double
	m.Put(true, NewString("t"))
	m.Put("s", NewString("str"))
	if m.Size() != 3 || ToString(m.Get(NewDouble(1))) != "b" {
		t.Errorf("1 and 1e0 must be one key: size %d, get %q", m.Size(), ToString(m.Get(NewDouble(1))))
	}
	if !m.Contains(NewBool(true)) || !m.Contains(NewString("s")) || m.Contains(NewString("1")) {
		t.Errorf("Contains gives the wrong answer")
	}
	if m.Contains(false) {
		t.Errorf("false must not be a key")
	}
	c := m.With(NewInteger(2), NewString("x"))
	if m.Size() != 3 || c.Size() != 4 || ToString(c.Get(true)) != "t" {
		t.Errorf("With must leave its source alone: %d %d", m.Size(), c.Size())
	}
	if len(Items(m.Get(NewInteger(99)))) != 0 {
		t.Errorf("an absent key must give the empty sequence")
	}
	nan := NewMap()
	nan.Put(math.NaN(), NewString("n"))
	nan.Put(math.Inf(1), NewString("i"))
	nan.Put(math.Inf(-1), NewString("j"))
	if ToString(nan.Get(NewDouble(math.NaN()))) != "n" || ToString(nan.Get(math.Inf(-1))) != "j" {
		t.Errorf("NaN/INF bare float keys")
	}
}

// TestTyKeyString pins KeyString/CompositeKeyString to the "eq" equivalence
// that xsl:key indexing relies on (XSLT 3.0 §20.2.2): numerics by value
// across subtypes, NaN never equal to anything, date/time by instant,
// QNames by expanded name, and no collision between a number and a string.
func TestTyKeyString(t *testing.T) {
	dt := func(s string) *Atomic {
		a, err := CastTo(NewString(s), XSdateTime)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	eq := []struct{ a, b Item }{
		{NewInteger(10), NewDouble(10)},
		{NewDouble(3.5), NewDecimal(big.NewRat(7, 2))},
		{dt("2000-01-01T12:00:00-05:00"), dt("2000-01-01T17:00:00Z")},
		{dt("2000-01-01T12:00:00"), dt("2000-01-01T12:00:00")},
		{NewQName(xmltree.Name{Space: "urn:a", Local: "x", Prefix: "p"}), NewQName(xmltree.Name{Space: "urn:a", Local: "x", Prefix: "q"})},
		{NewString("a"), NewUntyped("a")},
		{float64(2), NewInteger(2)},
	}
	for _, c := range eq {
		if KeyString(c.a) != KeyString(c.b) {
			t.Errorf("KeyString(%v) != KeyString(%v)", c.a, c.b)
		}
	}
	ne := []struct{ a, b Item }{
		{NewString("4"), NewInteger(4)},
		{NewDouble(math.NaN()), NewDouble(math.NaN())},
		{dt("2000-01-01T12:00:00"), dt("2000-01-01T12:00:00Z")},
		{NewQName(xmltree.Name{Space: "urn:a", Local: "x"}), NewString("x")},
		{NewInteger(1), NewInteger(2)},
	}
	for _, c := range ne {
		if KeyString(c.a) == KeyString(c.b) {
			t.Errorf("KeyString(%v) == KeyString(%v), want distinct", c.a, c.b)
		}
	}
	if KeyString(NewMap()) != "" {
		t.Errorf("a non-atomizable item keys by its (empty) string value")
	}
	// Composite keys: the tuple as a whole, order and arity significant.
	if CompositeKeyString([]Item{NewString("a"), NewInteger(1)}) != CompositeKeyString([]Item{NewUntyped("a"), NewDouble(1)}) {
		t.Errorf("composite keys of eq tuples must match")
	}
	for _, pair := range [][2][]Item{
		{{NewString("a"), NewString("b")}, {NewString("b"), NewString("a")}},
		{{NewString("a:1"), NewString("b")}, {NewString("a"), NewString("1:b")}},
		{{NewString("a")}, {NewString("a"), NewString("")}},
	} {
		if CompositeKeyString(pair[0]) == CompositeKeyString(pair[1]) {
			t.Errorf("composite keys %v and %v must differ", pair[0], pair[1])
		}
	}
}

// TestTyConversions pins the exported XPath 1.0-style conversion helpers
// (XPath 1.0 §4.2 string(), §4.3 boolean(), §4.4 number()) and the XPath 3.1
// effective boolean value (§2.4.3, FORG0006 for values that have none).
func TestTyConversions(t *testing.T) {
	doc, _ := xmltree.Parse("<r><a>x</a><b> 4.5 </b></r>")
	r := doc.Children[0]
	a, b := r.Children[0], r.Children[1]
	date, _ := CastTo(NewString("2000-01-01"), XSdate)

	ebv := []struct {
		in   Object
		want bool
		err  bool
	}{
		{Sequence{}, false, false},
		{NodeSet{a, b}, true, false},
		{Sequence{a, NewInteger(1)}, true, false},
		{Sequence{NewInteger(1), NewInteger(2)}, false, true},
		{true, true, false},
		{"", false, false},
		{"x", true, false},
		{math.NaN(), false, false},
		{float64(2), true, false},
		{NewBool(false), false, false},
		{NewString(""), false, false},
		{NewUntyped("u"), true, false},
		{NewAnyURI(""), false, false},
		{NewInteger(0), false, false},
		{NewDouble(math.NaN()), false, false},
		{NewDecimal(big.NewRat(1, 2)), true, false},
		{date, false, true},
		{NewMap(), false, true},
	}
	for _, c := range ebv {
		got, err := EffectiveBoolErr(c.in)
		if (err != nil) != c.err || (err == nil && got != c.want) {
			t.Errorf("EffectiveBoolErr(%v) = %v %v, want %v err=%v", c.in, got, err, c.want, c.err)
		}
		if err != nil && tyErrCode(err) != "FORG0006" {
			t.Errorf("EffectiveBoolErr(%v) code = %v, want FORG0006", c.in, err)
		}
		if !c.err {
			if got := ToBool(c.in); got != c.want {
				t.Errorf("ToBool(%v) = %v, want %v", c.in, got, c.want)
			}
		}
	}
	if ToBool(NodeSet{}) || !ToBool(NodeSet{a}) {
		t.Errorf("ToBool on node-sets")
	}

	num := []struct {
		in   Object
		want float64
	}{
		{float64(1.5), 1.5}, {true, 1}, {false, 0}, {" 3.5 ", 3.5},
		{NodeSet{b}, 4.5}, {Sequence{NewInteger(4)}, 4}, {Sequence{true}, 1},
		{Sequence{b}, 4.5}, {NewDecimal(big.NewRat(1, 4)), 0.25},
		{NewString("7"), 7}, {NewBool(true), 1},
	}
	for _, c := range num {
		if got := ToNumber(c.in); got != c.want {
			t.Errorf("ToNumber(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, in := range []Object{"", "abc", NodeSet{a}, Sequence{}, NewMap()} {
		if got := ToNumber(in); !math.IsNaN(got) {
			t.Errorf("ToNumber(%v) = %v, want NaN", in, got)
		}
	}

	str := []struct {
		in   Object
		want string
	}{
		{"s", "s"}, {true, "true"}, {false, "false"},
		{math.NaN(), "NaN"}, {math.Inf(1), "Infinity"}, {math.Inf(-1), "-Infinity"},
		{float64(12), "12"}, {0.5, "0.5"}, {1e21, "1000000000000000000000"},
		{NodeSet{}, ""}, {NodeSet{b, a}, "x"}, {Sequence{}, ""},
		{Sequence{NewInteger(3), NewInteger(4)}, "3"}, {NewDouble(2), "2"},
		{NewMap(), ""}, {NewArray(nil), ""},
		{Sequence{true}, "true"}, {Sequence{false}, "false"}, {Sequence{"x"}, "x"},
		{Sequence{2.5}, "2.5"}, {Sequence{NewMap()}, ""}, {Sequence{NewArray(nil)}, ""},
		{Sequence{a}, "x"},
	}
	for _, c := range str {
		if got := ToString(c.in); got != c.want {
			t.Errorf("ToString(%v) = %q, want %q", c.in, got, c.want)
		}
	}

	if ns, ok := ToNodeSet(Sequence{a, b}); !ok || len(ns) != 2 {
		t.Errorf("ToNodeSet(sequence of nodes) = %v %v", ns, ok)
	}
	if ns, ok := ToNodeSet(a); !ok || len(ns) != 1 {
		t.Errorf("ToNodeSet(node) = %v %v", ns, ok)
	}
	if ns, ok := ToNodeSet(NodeSet{a}); !ok || len(ns) != 1 {
		t.Errorf("ToNodeSet(NodeSet) = %v %v", ns, ok)
	}
	for _, in := range []Object{Sequence{a, NewInteger(1)}, "x", NewInteger(1)} {
		if _, ok := ToNodeSet(in); ok {
			t.Errorf("ToNodeSet(%v) must fail", in)
		}
	}
}

// TestTyDocumentOrderUnnumbered checks NodeSet.Unique (document order,
// duplicates removed) on a tree built in memory, whose nodes carry no order
// numbers: XDM 3.1 §2.4 puts a node's namespace and attribute nodes after it
// and before its children, and ancestors before descendants.
func TestTyDocumentOrderUnnumbered(t *testing.T) {
	root := xmltree.NewElement(xmltree.Name{Local: "root"})
	a := xmltree.NewElement(xmltree.Name{Local: "a"})
	b := xmltree.NewElement(xmltree.Name{Local: "b"})
	root.Append(a)
	root.Append(b)
	attr := xmltree.NewAttribute(xmltree.Name{Local: "x"}, "1")
	root.Attrs = append(root.Attrs, attr)
	attr.Parent = root
	c := xmltree.NewElement(xmltree.Name{Local: "c"})
	a.Append(c)
	ns := NodeSet{b, c, attr, a, root, c, b}.Unique()
	want := []*xmltree.Node{root, attr, a, c, b}
	if len(ns) != len(want) {
		t.Fatalf("Unique gave %d nodes, want %d", len(ns), len(want))
	}
	for i := range want {
		if ns[i] != want[i] {
			t.Errorf("position %d: %s, want %s", i, ns[i].Name.Local, want[i].Name.Local)
		}
	}
	if ToString(NodeSet{b, c}) != c.StringValue() {
		t.Errorf("string() of a node-set is the first node's in document order")
	}
}

// TestTyAtomicAccessors pins the small exported accessors on *Atomic.
func TestTyAtomicAccessors(t *testing.T) {
	ymd, _ := CastTo(NewString("P1Y"), XSyearMonthDuration)
	dur, _ := CastTo(NewString("P1Y"), XSduration)
	date, _ := CastTo(NewString("2000-01-01Z"), XSdate)
	if !date.IsOrderableNonString() || !ymd.IsOrderableNonString() || dur.IsOrderableNonString() ||
		NewInteger(1).IsOrderableNonString() || NewString("a").IsOrderableNonString() {
		t.Errorf("IsOrderableNonString: xs:duration has no ordering; dates and the two subtypes do")
	}
	if !dur.IsUnorderedDuration() || ymd.IsUnorderedDuration() {
		t.Errorf("IsUnorderedDuration")
	}
	if !NewString("a").IsStringFamily() || NewUntyped("a").IsStringFamily() {
		t.Errorf("IsStringFamily: xs:untypedAtomic is not in the string family")
	}
	if n, ok := NewQName(xmltree.Name{Space: "u", Local: "l"}).QNameValue(); !ok || n.Local != "l" || n.Space != "u" {
		t.Errorf("QNameValue")
	}
	if _, ok := NewString("x").QNameValue(); ok {
		t.Errorf("QNameValue of a string must be !ok")
	}
	if tm, tz := date.TimeValue(); !tz || tm.Year() != 2000 {
		t.Errorf("TimeValue = %v %v", tm, tz)
	}
	if _, tz := NewString("x").TimeValue(); tz {
		t.Errorf("TimeValue of a string")
	}
	if v, ok := IntegerBig(NewInteger(5)); !ok || v.Int64() != 5 {
		t.Errorf("IntegerBig(5) = %v %v", v, ok)
	}
	if _, ok := IntegerBig(NewDouble(5)); ok {
		t.Errorf("IntegerBig(double) must be !ok")
	}
	if !NewBool(true).Bool() {
		t.Errorf("Bool")
	}
	var nilA *Atomic
	if nilA.SchemaType() != nil {
		t.Errorf("nil SchemaType")
	}
	if d, ok := NewDecimalFromString("2.50"); !ok || d.Lexical() != "2.5" {
		t.Errorf("NewDecimalFromString = %v %v", d, ok)
	}
	if _, ok := NewDecimalFromString("x"); ok {
		t.Errorf("NewDecimalFromString(x) must fail")
	}
	if NewBool(true).Float() != 1 || NewString("2.5").Float() != 2.5 {
		t.Errorf("Float of boolean/string")
	}
	big1 := new(big.Int).Lsh(big.NewInt(1), 70)
	if NewIntegerBig(big1).Float() != math.Ldexp(1, 70) {
		t.Errorf("Float of a big integer")
	}
	if tp, ok := AtomTypeByName("xs:date"); !ok || tp != XSdate {
		t.Errorf("AtomTypeByName(xs:date)")
	}
	if AtomType(120).String() != "xs:anyAtomicType" {
		t.Errorf("an unknown AtomType renders as xs:anyAtomicType")
	}
}

// TestTyAnnotatedNodes covers the typed value and string value of nodes that
// carry a schema type annotation (XDM 3.1 §3.3.1): the typed value is the
// value cast to the annotation; the string value is the schema normalized
// value (whiteSpace preserve / replace / collapse by type); a list-typed
// node's typed value is a sequence; a nilled element's typed value is empty;
// an element-only element has no typed value (FOTY0012, F&O 3.1 §2.1.4).
func TestTyAnnotatedNodes(t *testing.T) {
	doc := &xmltree.Node{Kind: xmltree.KindDocument}
	root := xmltree.NewElement(xmltree.Name{Local: "root"})
	doc.Append(root)
	mk := func(local, text string, anno AtomType) *xmltree.Node {
		el := xmltree.NewElement(xmltree.Name{Local: local})
		el.Append(xmltree.NewText(text))
		if anno != 0 {
			el.SetTypeAnno(int32(anno))
		}
		root.Append(el)
		return el
	}
	mk("int", "003", XSinteger)
	mk("norm", "a\tb", XSnormalizedString)
	mk("tok", "  a   b ", XStoken)
	mk("str", " a ", XSstring)
	list := mk("list", " 1 2  3 ", XSinteger)
	list.SetListTyped(true)
	nilled := mk("nil", "", XSinteger)
	nilled.SetNilled(true)
	dec := mk("dec", "1.50", XSdecimal)
	hs := tyT("hatsize")
	hsEl := mk("hs", "7", XSinteger)
	hsEl.SetSchemaType(&hs)
	un := tyT("numOrDate")
	unEl := mk("un", "8", XSinteger)
	unEl.SetSchemaType(&un)
	unEl.SetValueType(&hs)
	eo := xmltree.NewElement(xmltree.Name{Local: "eo"})
	base := tyT("Base")
	eo.SetSchemaType(&base)
	eo.Append(xmltree.NewText("\n "))
	eo.Append(xmltree.NewElement(xmltree.Name{Local: "child"}))
	root.Append(eo)
	qn := xmltree.NewElement(xmltree.Name{Local: "qn"})
	qn.AddNS(&xmltree.Node{Kind: xmltree.KindNamespace, Name: xmltree.Name{Local: "p"}, Value: "urn:p"})
	qa := xmltree.NewAttribute(xmltree.Name{Local: "q"}, " p:local ")
	qa.SetTypeAnno(int32(XSqname))
	qn.Attrs = append(qn.Attrs, qa)
	qa.Parent = qn
	root.Append(qn)

	cases := []tyCase{
		{`data(int) instance of xs:integer`, "true"},
		{`data(int) + 1`, "4"},
		{`string(int)`, "003"},
		{`string-to-codepoints(string(norm))[2]`, "32"},
		{`string(tok)`, "a b"},
		{`string(str)`, " a "},
		{`count(data(list))`, "3"},
		{`sum(data(list))`, "6"},
		{`data(list)[1] instance of xs:integer`, "true"},
		{`count(data(nil))`, "0"},
		{`data(dec) instance of xs:decimal`, "true"},
		{`data(hs) instance of my:hatsize`, "true"},
		{`data(un) instance of my:hatsize`, "true"},
		{`data(eo)`, "!FOTY0012"},
		{`namespace-uri-from-QName(data(qn/@q))`, "urn:p"},
		{`local-name-from-QName(data(qn/@q))`, "local"},
		{`function($x as xs:integer*) {sum($x)}(list)`, "6"},
	}
	for _, c := range cases {
		if got := tySchemaRun(c.expr, root); got != c.want {
			t.Errorf("%s\n  got  %q\n  want %q", c.expr, got, c.want)
		}
	}

	// The exported accessors.
	if s, ok := TypedNodeLexical(list); !ok || s != "1 2 3" {
		t.Errorf("TypedNodeLexical(list) = %q %v", s, ok)
	}
	if s, ok := TypedNodeLexical(dec); !ok || s != "1.5" {
		t.Errorf("TypedNodeLexical(dec) = %q %v", s, ok)
	}
	if _, ok := TypedNodeLexical(root); ok {
		t.Errorf("an unannotated element has no typed lexical")
	}
	if _, ok := TypedNodeLexical(doc); ok {
		t.Errorf("a document node is not handled by TypedNodeLexical")
	}
	if _, ok := TypedNodeLexical(nil); ok {
		t.Errorf("TypedNodeLexical(nil)")
	}
	if at, ok := NodeAnnotation(dec); !ok || at != XSdecimal {
		t.Errorf("NodeAnnotation(dec) = %v %v", at, ok)
	}
	if _, ok := NodeAnnotation(root); ok || IsTypeAnnotated(nil) {
		t.Errorf("unannotated nodes")
	}
}
