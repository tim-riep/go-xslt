package xsd

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// --- the differential gate ---------------------------------------------------
//
// ValidateNode reuses the whole validation engine but reaches it down a
// different road: a detached, text-normalized CLONE instead of a freshly
// parsed document. That road is exactly where "a node built outside the normal
// path is missing an invariant other code silently depends on" bugs live, so
// the guard is empirical rather than argued — every instance document of the
// W3C xsdtests suite goes through BOTH entry points and the verdicts must
// agree, case by case, in both schema versions.

// findXSDTests locates the (gitignored, local-only) W3C suite by walking up
// from the package directory to THIS module's root, where the conformance
// harness's own clone instructions put it. The walk stops at go.mod on
// purpose: from a git worktree it would otherwise climb into the surrounding
// checkout and silently test against a different tree's corpus.
func findXSDTests() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if p := filepath.Join(dir, "test", "conformance", "xsdtests"); isDir(p) {
			return p
		}
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return "" // module root reached without finding the suite
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// unmarshalSuite decodes one catalog file. The suite carries a handful of
// non-UTF-8 documents, so BOMs are stripped first and any declared encoding is
// passed through rather than rejected — the catalog data this reads (hrefs and
// verdicts) is ASCII either way.
func unmarshalSuite(b []byte, v any) error {
	dec := xml.NewDecoder(strings.NewReader(xmltree.DecodeBOM(string(b))))
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	dec.Strict = false
	return dec.Decode(v)
}

type diffSuite struct {
	Refs []struct {
		Href string `xml:"href,attr"`
	} `xml:"testSetRef"`
}

type diffSet struct {
	Name    string      `xml:"name,attr"`
	Version string      `xml:"version,attr"`
	Groups  []diffGroup `xml:"testGroup"`
}

type diffGroup struct {
	Name      string     `xml:"name,attr"`
	Version   string     `xml:"version,attr"`
	Schema    *diffSchem `xml:"schemaTest"`
	Instances []diffInst `xml:"instanceTest"`
}

type diffSchem struct {
	Version  string     `xml:"version,attr"`
	Docs     []diffHref `xml:"schemaDocument"`
	Expected []diffExp  `xml:"expected"`
}

type diffInst struct {
	Name     string    `xml:"name,attr"`
	Version  string    `xml:"version,attr"`
	Doc      diffHref  `xml:"instanceDocument"`
	Expected []diffExp `xml:"expected"`
}

type diffHref struct {
	Href string `xml:"href,attr"`
}

type diffExp struct {
	Validity string `xml:"validity,attr"`
	Version  string `xml:"version,attr"`
}

// diffVersionApplies mirrors the suite's version-token semantics (xsts.xsd).
func diffVersionApplies(attr, runVer string) bool {
	if attr == "" {
		return true
	}
	for _, tok := range strings.Fields(attr) {
		if tok == runVer {
			return true
		}
	}
	return false
}

// diffTally counts one version pass.
type diffTally struct {
	agree      int
	parseSkip  int // instance not well-formed — a parser question, not a bridge one
	noVerdict  int // one side (or both) reported ErrUnsupported
	mismatches []string
}

func TestValidateNodeMatchesValidate(t *testing.T) {
	root := findXSDTests()
	if root == "" {
		t.Skip("W3C xsdtests suite not present (test/conformance/xsdtests)")
	}
	b, err := os.ReadFile(filepath.Join(root, "suite.xml"))
	if err != nil {
		t.Skipf("no suite.xml: %v", err)
	}
	var suite diffSuite
	if err := unmarshalSuite(b, &suite); err != nil {
		t.Fatalf("suite.xml: %v", err)
	}
	// A cap keeps this usable as a quick check; unset it runs the whole corpus,
	// which is the number the gate actually reports.
	limit := 0
	if v := os.Getenv("XSD_DIFF_LIMIT"); v != "" {
		limit, _ = strconv.Atoi(v)
	}

	total := 0
	for _, ver := range []string{"1.0", "1.1"} {
		tally := &diffTally{}
		for _, ref := range suite.Refs {
			setPath := filepath.Clean(filepath.Join(root, ref.Href))
			diffRunSet(t, setPath, ver, tally, limit)
			if limit > 0 && tally.agree >= limit {
				break
			}
		}
		total += tally.agree
		for i, m := range tally.mismatches {
			if i >= 25 {
				t.Errorf("XSD %s: ... and %d more mismatches", ver, len(tally.mismatches)-25)
				break
			}
			t.Errorf("XSD %s: %s", ver, m)
		}
		t.Logf("XSD %s: %d instances agree, %d mismatch, %d no-verdict, %d unparsable",
			ver, tally.agree, len(tally.mismatches), tally.noVerdict, tally.parseSkip)
	}
	if total < 1000 {
		t.Fatalf("differential coverage too thin to mean anything: only %d instances compared", total)
	}
}

func diffRunSet(t *testing.T, setPath, ver string, tally *diffTally, limit int) {
	b, err := os.ReadFile(setPath)
	if err != nil {
		return
	}
	var set diffSet
	if err := unmarshalSuite(b, &set); err != nil {
		return
	}
	if !diffVersionApplies(set.Version, ver) {
		return
	}
	dir := filepath.Dir(setPath)
	for _, g := range set.Groups {
		if limit > 0 && tally.agree >= limit {
			return
		}
		diffRunGroup(t, g, dir, ver, tally)
	}
}

// diffRunGroup compares the two entry points over one test group's instances.
// It is panic-guarded for the same reason the conformance harness is: one
// pathological schema must not abort the sweep — but unlike there, a panic is
// a hard failure, since it can only come from the bridge (the string path is
// already known to survive this corpus).
func diffRunGroup(t *testing.T, g diffGroup, dir, ver string, tally *diffTally) {
	defer func() {
		if e := recover(); e != nil {
			t.Errorf("panic in group %s: %v", g.Name, e)
		}
	}()
	if !diffVersionApplies(g.Version, ver) || g.Schema == nil ||
		!diffVersionApplies(g.Schema.Version, ver) || len(g.Schema.Docs) == 0 {
		return
	}
	// Only groups whose schema COMPILES have instances worth comparing.
	var docs []string
	var baseDir string
	for i, d := range g.Schema.Docs {
		p := filepath.Clean(filepath.Join(dir, d.Href))
		data, err := os.ReadFile(p)
		if err != nil {
			return
		}
		docs = append(docs, string(data))
		if i == 0 {
			baseDir = filepath.Dir(p)
		}
	}
	v := Version10
	if ver == "1.1" {
		v = Version11
	}
	sch, err := Compile(docs, baseDir, v)
	if err != nil || sch == nil {
		return
	}
	for _, it := range g.Instances {
		if !diffVersionApplies(it.Version, ver) {
			continue
		}
		p := filepath.Clean(filepath.Join(dir, it.Doc.Href))
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		diffOneInstance(sch, v, it.Name, string(data), tally)
	}
}

func diffOneInstance(sch *Schema, v Version, name, inst string, tally *diffTally) {
	doc, perr := xmltree.ParseLenient11(inst)
	if perr != nil || xmltree.RootElement(doc) == nil {
		tally.parseSkip++
		return
	}
	root := xmltree.RootElement(doc)

	strRes, strErr := sch.Validate(inst)
	if errors.Is(strErr, ErrUnsupported) {
		tally.noVerdict++
		return
	}
	if strErr != nil {
		tally.mismatches = append(tally.mismatches,
			fmt.Sprintf("%s: Validate returned a non-verdict error: %v", name, strErr))
		return
	}

	// Validate merges xsi:schemaLocation hints before walking; ValidateNode
	// deliberately does not (XSLT's component set is static). Replicate the
	// merge here so what is being compared is the BRIDGE, not that documented
	// difference in where components come from.
	useSch := sch
	if hinted := sch.hintDocs(root); len(hinted) > 0 {
		merged := append(append([]string{}, sch.sourceDocs...), hinted...)
		if ms, err := Compile(merged, sch.baseDir, v); err == nil {
			useSch = ms
		}
	}

	// Document: true because this differential mirrors Validate, whose input
	// IS a whole instance document — so the document-level constraints
	// (ID uniqueness, IDREF resolution) apply, exactly as they do there. An
	// ELEMENT-rooted episode deliberately skips them (see
	// idTable.elementScope), which is a divergence from Validate BY DESIGN
	// and not one this test should be asked to reproduce.
	opts := NodeValidateOptions{Strict: true, Document: true}
	// The one other deliberate divergence: Validate assesses an UNDECLARED
	// document element "as a type" via its xsi:type (instance.go's
	// validateElement), while strict ValidateNode requires a declaration by
	// contract (XTTE1512). Same episode, different way in — so ask for it the
	// way ValidateNode spells it, which exercises the Type option too.
	if _, declared := useSch.elements[nameOf(root)]; !declared {
		if q, ok := root.Attr(xsiNS, "type"); ok {
			n := resolveQName(root, q)
			_, complex := useSch.types[n].(*ComplexType)
			opts = NodeValidateOptions{Document: true, Type: &xmltree.SchemaTypeName{
				Namespace: n.Space, Local: n.Local, Complex: complex}}
		}
	}

	nodeErr := useSch.ValidateNode(root, opts)
	if errors.Is(nodeErr, ErrUnsupported) {
		tally.noVerdict++
		return
	}
	if strRes.Valid == (nodeErr == nil) {
		tally.agree++
		return
	}
	detail := "valid"
	if nodeErr != nil {
		detail = nodeErr.Error()
	}
	tally.mismatches = append(tally.mismatches, fmt.Sprintf(
		"%s: Validate says valid=%v, ValidateNode says valid=%v (%s)",
		name, strRes.Valid, nodeErr == nil, detail))
}

// --- accessor surface --------------------------------------------------------

const bridgeSchema = `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema"
    xmlns:t="urn:t" targetNamespace="urn:t" elementFormDefault="qualified">
  <xs:simpleType name="Zip">
    <xs:restriction base="xs:string"><xs:pattern value="[0-9]{5}"/></xs:restriction>
  </xs:simpleType>
  <xs:simpleType name="Zip5"><xs:restriction base="t:Zip"/></xs:simpleType>
  <xs:complexType name="Addr">
    <xs:sequence>
      <xs:element name="zip" type="t:Zip"/>
      <xs:element name="n" type="xs:int" minOccurs="0"/>
    </xs:sequence>
    <xs:attribute name="kind" type="t:Zip"/>
  </xs:complexType>
  <xs:complexType name="FullAddr">
    <xs:complexContent>
      <xs:extension base="t:Addr">
        <xs:sequence><xs:element name="c" type="xs:string"/></xs:sequence>
      </xs:extension>
    </xs:complexContent>
  </xs:complexType>
  <xs:element name="head" type="t:Addr"/>
  <xs:element name="sub" type="t:FullAddr" substitutionGroup="t:head"/>
  <xs:element name="anon"><xs:complexType><xs:sequence/></xs:complexType></xs:element>
  <xs:attribute name="gkind" type="t:Zip"/>
</xs:schema>`

func bridgeCompile(t *testing.T) *Schema {
	t.Helper()
	s, err := Compile([]string{bridgeSchema}, "", Version11)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return s
}

func TestBridgeAccessors(t *testing.T) {
	s := bridgeCompile(t)

	if n, ok := s.TypeByName("urn:t", "Zip"); !ok || n.Complex || n.Local != "Zip" {
		t.Errorf("TypeByName(t:Zip) = %+v, %v", n, ok)
	}
	if n, ok := s.TypeByName("urn:t", "Addr"); !ok || !n.Complex {
		t.Errorf("TypeByName(t:Addr) = %+v, %v — want a complex type", n, ok)
	}
	if n, ok := s.TypeByName(xsNS, "string"); !ok || n.Complex || n.Local != "string" {
		t.Errorf("TypeByName(xs:string) = %+v, %v", n, ok)
	}
	if _, ok := s.TypeByName("urn:t", "Nope"); ok {
		t.Error("TypeByName resolved a name the schema does not declare")
	}
	// XDM-only names are not schema types (same gate as an instance's xsi:type).
	if _, ok := s.TypeByName(xsNS, "untypedAtomic"); ok {
		t.Error("TypeByName resolved xs:untypedAtomic, which is not a schema type")
	}

	if n, ok := s.ElementDeclared("urn:t", "head"); !ok || n.Local != "Addr" || !n.Complex {
		t.Errorf("ElementDeclared(t:head) = %+v, %v", n, ok)
	}
	// A declaration with an ANONYMOUS type gets a SYNTHETIC identity, which is
	// what lets schema-element(N) work for the inline-complexType shape most
	// real schemas use (see Schema.anonName). The name is per-Schema and
	// per-component, so the only thing worth pinning is that it exists, lives
	// in the reserved namespace, and resolves back to the same component.
	anon, ok := s.ElementDeclared("urn:t", "anon")
	if !ok || anon.Namespace != anonTypeNS || anon.Local == "" || !anon.Complex {
		t.Errorf("ElementDeclared(t:anon) = %+v, %v — want a synthetic anonymous-type identity", anon, ok)
	}
	if again, _ := s.ElementDeclared("urn:t", "anon"); again != anon {
		t.Errorf("anonymous-type identity is not stable: %+v then %+v", anon, again)
	}
	if !s.DerivesFrom(anon, anon) {
		t.Error("an anonymous type must derive from itself (typeByName cannot resolve its synthetic name)")
	}
	if _, ok := s.ElementDeclared("urn:t", "zip"); ok {
		t.Error("ElementDeclared found a LOCAL element declaration; only globals count")
	}
	if n, ok := s.AttributeDeclared("urn:t", "gkind"); !ok || n.Local != "Zip" || n.Complex {
		t.Errorf("AttributeDeclared(t:gkind) = %+v, %v", n, ok)
	}
	if _, ok := s.AttributeDeclared("urn:t", "kind"); ok {
		t.Error("AttributeDeclared found a LOCAL attribute use; only globals count")
	}

	tn := func(local string) xmltree.SchemaTypeName {
		n, _ := s.TypeByName("urn:t", local)
		return n
	}
	bi := func(local string) xmltree.SchemaTypeName {
		n, ok := s.TypeByName(xsNS, local)
		if !ok {
			t.Fatalf("built-in xs:%s did not resolve", local)
		}
		return n
	}
	cases := []struct {
		got, want xmltree.SchemaTypeName
		expect    bool
		why       string
	}{
		{tn("Zip"), tn("Zip"), true, "identity"},
		{tn("Zip5"), tn("Zip"), true, "restriction of a named simple type"},
		{tn("Zip"), tn("Zip5"), false, "derivation is not symmetric"},
		{tn("FullAddr"), tn("Addr"), true, "complex extension"},
		{tn("Addr"), tn("FullAddr"), false, "complex extension, reversed"},
		{tn("Zip"), tn("Addr"), false, "unrelated types"},
		{bi("int"), bi("integer"), true, "the built-in atomic hierarchy"},
		{bi("integer"), bi("int"), false, "the built-in hierarchy, reversed"},
		{tn("Zip"), bi("string"), true, "a user type restricting a built-in"},
		{tn("Zip"), bi("anyType"), true, "everything derives from xs:anyType"},
		{tn("Addr"), bi("anyType"), true, "a complex type derives from xs:anyType"},
		{bi("int"), tn("Zip"), false, "a built-in does not derive from a user type"},
		{xmltree.SchemaTypeName{Namespace: "urn:t", Local: "sub"},
			xmltree.SchemaTypeName{Namespace: "urn:t", Local: "head"}, true,
			"substitution-group membership (element names, no such types)"},
		{xmltree.SchemaTypeName{Namespace: "urn:t", Local: "head"},
			xmltree.SchemaTypeName{Namespace: "urn:t", Local: "sub"}, false,
			"substitution-group membership, reversed"},
		{xmltree.SchemaTypeName{}, tn("Zip"), false, "an anonymous type derives from nothing"},
		{tn("Zip"), xmltree.SchemaTypeName{}, false, "nothing derives from an anonymous type"},
	}
	for _, c := range cases {
		if got := s.DerivesFrom(c.got, c.want); got != c.expect {
			t.Errorf("DerivesFrom(%s, %s) = %v, want %v (%s)",
				c.got.Local, c.want.Local, got, c.expect, c.why)
		}
	}
	// A nil resolver answers false everywhere — the seam's documented default —
	// and a compiled Schema must satisfy the interface it is injected through.
	var _ xpath.SchemaTypeResolver = s
}

// --- ValidateNode behaviour ---------------------------------------------------

func bridgeParse(t *testing.T, src string) *xmltree.Node {
	t.Helper()
	doc, err := xmltree.ParseLenient11(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	el := xmltree.RootElement(doc)
	if el == nil {
		t.Fatalf("no document element in %q", src)
	}
	return el
}

func TestValidateNodeAnnotates(t *testing.T) {
	s := bridgeCompile(t)
	el := bridgeParse(t, `<t:head xmlns:t="urn:t" kind="99999"><t:zip>12345</t:zip><t:n>7</t:n></t:head>`)
	if err := s.ValidateNode(el, NodeValidateOptions{Strict: true}); err != nil {
		t.Fatalf("ValidateNode: %v", err)
	}
	// The root is complex: exact identity reaches SchemaType, and TypeAnno
	// stays 0 because complex content has no typed VALUE to atomize.
	if el.SchemaType == nil || el.SchemaType.Local != "Addr" || !el.SchemaType.Complex {
		t.Errorf("root SchemaType = %+v, want t:Addr complex", el.SchemaType)
	}
	if el.TypeAnno != 0 {
		t.Errorf("root TypeAnno = %d, want 0 (complex content)", el.TypeAnno)
	}
	zip := el.Children[0]
	if zip.SchemaType == nil || zip.SchemaType.Local != "Zip" || zip.SchemaType.Complex {
		t.Errorf("zip SchemaType = %+v, want t:Zip simple", zip.SchemaType)
	}
	if xpath.AtomType(zip.TypeAnno) != xpath.XSstring {
		t.Errorf("zip TypeAnno = %d, want xs:string (t:Zip's nearest primitive)", zip.TypeAnno)
	}
	// A BUILT-IN type is named too. TypeAnno alone cannot serve: element(*,
	// xs:int) and schema-element(N) ask "is this node's type T, or derived
	// from T?" through Node.SchemaType, and a nil there means "no type
	// identity at all", which would make every built-in-typed node
	// unmatchable (see builtinRefName).
	n := el.Children[1]
	if xpath.AtomType(n.TypeAnno) != xpath.XSint {
		t.Errorf("n TypeAnno = %d, want xs:int", n.TypeAnno)
	}
	if n.SchemaType == nil || n.SchemaType.Local != "int" ||
		n.SchemaType.Namespace != "http://www.w3.org/2001/XMLSchema" || n.SchemaType.Complex {
		t.Errorf("n SchemaType = %+v, want xs:int simple", n.SchemaType)
	}
	if len(el.Attrs) != 1 {
		t.Fatalf("expected one attribute, got %d", len(el.Attrs))
	}
	if a := el.Attrs[0]; a.SchemaType == nil || a.SchemaType.Local != "Zip" ||
		xpath.AtomType(a.TypeAnno) != xpath.XSstring {
		t.Errorf("@kind annotation = %+v/%d, want t:Zip / xs:string", a.SchemaType, a.TypeAnno)
	}
}

func TestValidateNodeLeavesInvalidTreesAlone(t *testing.T) {
	s := bridgeCompile(t)
	el := bridgeParse(t, `<t:head xmlns:t="urn:t"><t:zip>nope</t:zip></t:head>`)
	if err := s.ValidateNode(el, NodeValidateOptions{Strict: true}); err == nil {
		t.Fatal("expected an invalid verdict for a zip that fails its pattern facet")
	}
	// Nothing is annotated on failure: a half-validated tree must not be left
	// claiming types it never earned.
	if el.SchemaType != nil || el.TypeAnno != 0 || el.Children[0].SchemaType != nil {
		t.Errorf("annotations leaked from a FAILED validation: root=%+v child=%+v",
			el.SchemaType, el.Children[0].SchemaType)
	}
}

func TestValidateNodeStrictVsLax(t *testing.T) {
	s := bridgeCompile(t)
	src := `<t:other xmlns:t="urn:t"><x/></t:other>`

	el := bridgeParse(t, src)
	if err := s.ValidateNode(el, NodeValidateOptions{Strict: true}); err == nil {
		t.Error("strict validation of an undeclared element must fail (XTTE1512-shaped)")
	}
	el = bridgeParse(t, src)
	if err := s.ValidateNode(el, NodeValidateOptions{}); err != nil {
		t.Errorf("lax validation of an undeclared element must be tolerated, got %v", err)
	}
	if el.SchemaType != nil || el.TypeAnno != 0 {
		t.Errorf("lax validation annotated an unassessed element: %+v", el.SchemaType)
	}
	// Lax still fails an element that DOES match a declaration and is invalid.
	el = bridgeParse(t, `<t:head xmlns:t="urn:t"><t:zip>nope</t:zip></t:head>`)
	if err := s.ValidateNode(el, NodeValidateOptions{}); err == nil {
		t.Error("lax validation must still reject an invalid match (XTTE1510-shaped)")
	}
}

func TestValidateNodeAgainstNamedType(t *testing.T) {
	s := bridgeCompile(t)
	el := bridgeParse(t, `<anything xmlns:t="urn:t"><t:zip>12345</t:zip></anything>`)
	typ := &xmltree.SchemaTypeName{Namespace: "urn:t", Local: "Addr", Complex: true}
	if err := s.ValidateNode(el, NodeValidateOptions{Type: typ}); err != nil {
		t.Fatalf("[xsl:]type validation: %v", err)
	}
	if el.SchemaType == nil || el.SchemaType.Local != "Addr" {
		t.Errorf("root SchemaType = %+v, want t:Addr", el.SchemaType)
	}
	el = bridgeParse(t, `<anything xmlns:t="urn:t"><t:zip>nope</t:zip></anything>`)
	if err := s.ValidateNode(el, NodeValidateOptions{Type: typ}); err == nil {
		t.Error("[xsl:]type validation must reject content the named type rejects")
	}
	el = bridgeParse(t, `<anything/>`)
	if err := s.ValidateNode(el, NodeValidateOptions{
		Type: &xmltree.SchemaTypeName{Namespace: "urn:t", Local: "Nope"}}); err == nil {
		t.Error("an unresolvable [xsl:]type must fail, not validate vacuously")
	}
}

// TestValidateNodeNormalizesConstructedText is the reason the walk runs on a
// clone at all: XSLT builds element content one instruction at a time, so a
// constructed element routinely carries several adjacent text nodes and empty
// ones — a shape the parser never produces and the validator's per-child text
// inspection is written against.
func TestValidateNodeNormalizesConstructedText(t *testing.T) {
	s := bridgeCompile(t)
	mk := func(parts ...string) *xmltree.Node {
		head := xmltree.NewElement(xmltree.Name{Space: "urn:t", Local: "head", Prefix: "t"})
		head.NS = append(head.NS, &xmltree.Node{Kind: xmltree.KindNamespace,
			Name: xmltree.Name{Local: "t"}, Value: "urn:t", Parent: head})
		zip := xmltree.NewElement(xmltree.Name{Space: "urn:t", Local: "zip", Prefix: "t"})
		for _, p := range parts {
			zip.Append(xmltree.NewText(p))
		}
		head.Append(zip)
		return head
	}
	// Split across three nodes, with empty runs on both ends — "12345" only
	// after normalization.
	el := mk("", "12", "", "3", "45", "")
	if err := s.ValidateNode(el, NodeValidateOptions{Strict: true}); err != nil {
		t.Fatalf("constructed content with split text nodes: %v", err)
	}
	if xpath.AtomType(el.Children[0].TypeAnno) != xpath.XSstring {
		t.Errorf("zip TypeAnno = %d, want xs:string", el.Children[0].TypeAnno)
	}
	// The caller's tree keeps its own structure: only annotations are written.
	if got := len(el.Children[0].Children); got != 6 {
		t.Errorf("ValidateNode restructured the caller's tree: %d text children, want 6", got)
	}
	// And the same split content still FAILS when it should.
	el = mk("12", "34")
	if err := s.ValidateNode(el, NodeValidateOptions{Strict: true}); err == nil {
		t.Error("split text that normalizes to \"1234\" must fail t:Zip's 5-digit pattern")
	}
}

// TestValidateNodeSubtreeIDScoping pins the third adaptation: a subtree of a
// larger tree is not "the document", so an IDREF inside it that resolves
// somewhere else must not be reported as dangling — while a WHOLE document
// still gets the full cvc-id.1 check.
func TestValidateNodeSubtreeIDScoping(t *testing.T) {
	const idSchema = `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
	  <xs:element name="wrap"><xs:complexType><xs:sequence>
	    <xs:element ref="a"/></xs:sequence></xs:complexType></xs:element>
	  <xs:element name="a"><xs:complexType>
	    <xs:attribute name="r" type="xs:IDREF"/></xs:complexType></xs:element>
	</xs:schema>`
	s, err := Compile([]string{idSchema}, "", Version10)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	doc := bridgeParse(t, `<wrap><a r="elsewhere"/></wrap>`)
	sub := doc.Children[0]
	if err := s.ValidateNode(sub, NodeValidateOptions{Strict: true}); err != nil {
		t.Errorf("a subtree's unresolved IDREF must not be reported as dangling: %v", err)
	}
	// A DOCUMENT-scoped episode still resolves IDREFs document-wide. The scope
	// is the CALLER's to declare (NodeValidateOptions.Document) rather than
	// something inferred from the node's parent: a subtree copied into an XSLT
	// temporary tree hangs off a document-node FRAGMENT that was never a
	// document, and inferring from that reported dangling IDREFs for every
	// such copy (accumulator-073).
	doc2 := bridgeParse(t, `<wrap><a r="elsewhere"/></wrap>`)
	if err := s.ValidateNode(doc2, NodeValidateOptions{Strict: true, Document: true}); err == nil {
		t.Error("a document-scoped episode with a dangling IDREF must still fail cvc-id.1")
	}
	// …and without that declaration it does not, even for a whole document.
	doc3 := bridgeParse(t, `<wrap><a r="elsewhere"/></wrap>`)
	if err := s.ValidateNode(doc3, NodeValidateOptions{Strict: true}); err != nil {
		t.Errorf("an element-scoped episode must not apply document-level ID rules: %v", err)
	}
}

// TestValidateNodeIgnoresSchemaLocationHints pins the second adaptation: for
// XSLT the component set is fixed by xsl:import-schema, so an instance's own
// xsi:schemaLocation must not be able to add declarations behind the
// stylesheet's back.
func TestValidateNodeIgnoresSchemaLocationHints(t *testing.T) {
	dir := t.TempDir()
	hint := `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
	  <xs:element name="hinted" type="xs:string"/></xs:schema>`
	if err := os.WriteFile(filepath.Join(dir, "hint.xsd"), []byte(hint), 0o600); err != nil {
		t.Fatal(err)
	}
	base := `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
	  <xs:element name="other" type="xs:string"/></xs:schema>`
	s, err := Compile([]string{base}, dir, Version10)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	inst := `<hinted xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"` +
		` xsi:noNamespaceSchemaLocation="hint.xsd">x</hinted>`
	// The string entry point DOES pick the hint up — that behaviour is
	// load-bearing for the XSD suite and must not change.
	if res, err := s.Validate(inst); err != nil || !res.Valid {
		t.Fatalf("Validate should still honour xsi:noNamespaceSchemaLocation: %v %+v", err, res)
	}
	if err := s.ValidateNode(bridgeParse(t, inst), NodeValidateOptions{Strict: true}); err == nil {
		t.Error("ValidateNode must not load instance-discovered schema documents")
	}
}

// TestValidateNodeRejectsUnannotatableKinds pins the fail-closed contract: a
// kind with no schema type must be refused, never silently reported valid.
func TestValidateNodeRejectsUnannotatableKinds(t *testing.T) {
	s := bridgeCompile(t)
	for _, n := range []*xmltree.Node{
		nil,
		xmltree.NewText("x"),
		{Kind: xmltree.KindComment, Value: "c"},
		{Kind: xmltree.KindDocument}, // a document with no document element
	} {
		if err := s.ValidateNode(n, NodeValidateOptions{Strict: true}); err == nil {
			t.Errorf("ValidateNode(%v) returned valid; want a refusal", n)
		}
	}
}

// TestValidateDoesNotAnnotate guards the additive promise from the other side:
// the string entry point records no node types at all, so nothing it validates
// comes back carrying annotations it did not before.
func TestValidateDoesNotAnnotate(t *testing.T) {
	s := bridgeCompile(t)
	res, err := s.Validate(`<t:head xmlns:t="urn:t"><t:zip>12345</t:zip></t:head>`)
	if err != nil || !res.Valid {
		t.Fatalf("Validate: %v %+v", err, res)
	}
	if s.nodeTypes != nil {
		t.Error("Validate populated the ValidateNode annotation table")
	}
}
