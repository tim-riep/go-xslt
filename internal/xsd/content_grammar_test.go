package xsd

import (
	"errors"
	"strings"
	"testing"
)

// TestComplexTypeContentGrammar pins issue #22: an xs:assert (or any other
// child of the implicit content model) placed directly in an xs:complexType
// that has xs:simpleContent / xs:complexContent used to be accepted and
// never evaluated. The schema-for-schemas gives that complexType exactly
// (annotation?, simpleContent) or (annotation?, complexContent); the
// assertions belong inside the xs:extension / xs:restriction.
func TestComplexTypeContentGrammar(t *testing.T) {
	const head = "<xs:schema xmlns:xs=\"http://www.w3.org/2001/XMLSchema\">\n"
	const tail = "\n</xs:schema>"
	wrap := func(ct string) string { return head + ct + tail }

	rejected := []struct {
		name, ct string
		line     int // of the offending element in wrap(ct)
		col      int
		inV10    bool // also an error under XSD 1.0 (else only 1.1 has the element)
	}{
		{"assert next to simpleContent",
			"<xs:complexType name=\"t\">\n  <xs:simpleContent><xs:extension base=\"xs:integer\"/></xs:simpleContent>\n  <xs:assert test=\". ge 2\"/>\n</xs:complexType>",
			4, 3, false},
		{"attribute next to simpleContent",
			"<xs:complexType name=\"t\">\n  <xs:simpleContent><xs:extension base=\"xs:integer\"/></xs:simpleContent>\n  <xs:attribute name=\"a\"/>\n</xs:complexType>",
			4, 3, true},
		{"openContent next to simpleContent",
			"<xs:complexType name=\"t\">\n  <xs:simpleContent><xs:extension base=\"xs:integer\"/></xs:simpleContent>\n  <xs:openContent><xs:any/></xs:openContent>\n</xs:complexType>",
			4, 3, false},
		{"openContent after complexContent",
			"<xs:complexType name=\"t\">\n  <xs:complexContent><xs:extension base=\"xs:anyType\"/></xs:complexContent>\n  <xs:openContent><xs:any/></xs:openContent>\n</xs:complexType>",
			4, 3, false},
		{"attribute next to complexContent",
			"<xs:complexType name=\"t\">\n  <xs:complexContent><xs:extension base=\"xs:anyType\"/></xs:complexContent>\n  <xs:attribute name=\"a\"/>\n</xs:complexType>",
			4, 3, true},
		{"anyAttribute next to complexContent",
			"<xs:complexType name=\"t\">\n  <xs:complexContent><xs:extension base=\"xs:anyType\"/></xs:complexContent>\n  <xs:anyAttribute/>\n</xs:complexType>",
			4, 3, true},
		{"attributeGroup before simpleContent",
			"<xs:attributeGroup name=\"g\"/>\n<xs:complexType name=\"t\">\n  <xs:attributeGroup ref=\"g\"/>\n  <xs:simpleContent><xs:extension base=\"xs:integer\"/></xs:simpleContent>\n</xs:complexType>",
			4, 3, true},
		{"particle next to complexContent",
			"<xs:complexType name=\"t\">\n  <xs:complexContent><xs:extension base=\"xs:anyType\"/></xs:complexContent>\n  <xs:sequence/>\n</xs:complexType>",
			4, 3, true},
		{"two simpleContents",
			"<xs:complexType name=\"t\">\n  <xs:simpleContent><xs:extension base=\"xs:integer\"/></xs:simpleContent>\n  <xs:simpleContent><xs:extension base=\"xs:integer\"/></xs:simpleContent>\n</xs:complexType>",
			4, 3, true},
		{"simpleContent and complexContent",
			"<xs:complexType name=\"t\">\n  <xs:simpleContent><xs:extension base=\"xs:integer\"/></xs:simpleContent>\n  <xs:complexContent><xs:extension base=\"xs:anyType\"/></xs:complexContent>\n</xs:complexType>",
			4, 3, true},
		{"two model groups",
			"<xs:complexType name=\"t\">\n  <xs:sequence/>\n  <xs:choice/>\n</xs:complexType>",
			4, 3, true},
		{"two openContents",
			"<xs:complexType name=\"t\">\n  <xs:openContent><xs:any/></xs:openContent>\n  <xs:openContent><xs:any/></xs:openContent>\n</xs:complexType>",
			4, 3, false},
		{"extension and restriction in simpleContent",
			"<xs:complexType name=\"t\">\n  <xs:simpleContent>\n    <xs:extension base=\"xs:integer\"/>\n    <xs:restriction base=\"xs:integer\"/>\n  </xs:simpleContent>\n</xs:complexType>",
			5, 5, true},
		{"two extensions in complexContent",
			"<xs:complexType name=\"t\">\n  <xs:complexContent>\n    <xs:extension base=\"xs:anyType\"/>\n    <xs:extension base=\"xs:anyType\"/>\n  </xs:complexContent>\n</xs:complexType>",
			5, 5, true},
		{"empty simpleContent",
			"<xs:complexType name=\"t\">\n  <xs:simpleContent/>\n</xs:complexType>",
			3, 3, true},
		{"anonymous type in an element",
			"<xs:element name=\"n\">\n <xs:complexType>\n  <xs:simpleContent><xs:extension base=\"xs:integer\"/></xs:simpleContent>\n  <xs:assert test=\". ge 2\"/>\n </xs:complexType>\n</xs:element>",
			5, 3, false},
	}
	for _, c := range rejected {
		for _, ver := range []Version{Version10, Version11} {
			_, err := Compile([]string{wrap(c.ct)}, "", ver)
			if err == nil {
				t.Errorf("%s, XSD %v: accepted, want a schema error", c.name, ver)
				continue
			}
			if errors.Is(err, ErrUnsupported) {
				t.Errorf("%s, XSD %v: ErrUnsupported, want a verdict", c.name, ver)
				continue
			}
			var xe *Error
			if !errors.As(err, &xe) {
				t.Errorf("%s, XSD %v: %T %v is not an *Error", c.name, ver, err, err)
				continue
			}
			if ver == Version10 && !c.inV10 {
				continue // an element XSD 1.0 does not have: refused by its own check
			}
			if xe.Line != c.line || xe.Col != c.col {
				t.Errorf("%s, XSD %v: error at %d:%d, want %d:%d (%v)", c.name, ver, xe.Line, xe.Col, c.line, c.col, err)
			}
			if xe.Code != errElementContent {
				t.Errorf("%s, XSD %v: code %q, want %q (%v)", c.name, ver, xe.Code, errElementContent, err)
			}
		}
	}

	// The assertion placed where it belongs is accepted and evaluated.
	inside := wrap(`<xs:element name="n" type="t"/>
<xs:complexType name="t">
  <xs:simpleContent><xs:extension base="xs:integer"><xs:assert test="xs:integer(.) ge 2"/></xs:extension></xs:simpleContent>
</xs:complexType>`)
	sch, err := Compile([]string{inside}, "", Version11)
	if err != nil {
		t.Fatalf("assert inside xs:extension: %v", err)
	}
	for _, c := range []struct {
		inst  string
		valid bool
	}{{"<n>1</n>", false}, {"<n>2</n>", true}} {
		r, err := sch.Validate(c.inst)
		if err != nil || r.Valid != c.valid {
			t.Errorf("%s: valid=%v err=%v, want valid=%v", c.inst, r != nil && r.Valid, err, c.valid)
		}
	}

	// Positive controls: every legal arrangement stays legal.
	accepted := []struct{ name, ct string }{
		{"annotation then simpleContent", `<xs:complexType name="b"><xs:simpleContent><xs:extension base="xs:integer"/></xs:simpleContent></xs:complexType><xs:complexType name="t"><xs:annotation/><xs:simpleContent><xs:restriction base="b"><xs:minInclusive value="1"/><xs:assert test="true()"/></xs:restriction></xs:simpleContent></xs:complexType>`},
		{"annotation then complexContent", `<xs:complexType name="t"><xs:annotation/><xs:complexContent><xs:extension base="xs:anyType"><xs:sequence/><xs:attribute name="a"/></xs:extension></xs:complexContent></xs:complexType>`},
		{"empty", `<xs:complexType name="t"/>`},
		{"implicit model with everything", `<xs:complexType name="t"><xs:annotation/><xs:openContent mode="interleave"><xs:any/></xs:openContent><xs:sequence><xs:element name="e" minOccurs="0"/></xs:sequence><xs:attribute name="a"/><xs:anyAttribute/><xs:assert test="true()"/><xs:assert test="true()"/></xs:complexType>`},
		{"implicit model, group ref and attributeGroup", `<xs:group name="g"><xs:sequence/></xs:group><xs:attributeGroup name="ag"/><xs:complexType name="t"><xs:group ref="g"/><xs:attributeGroup ref="ag"/></xs:complexType>`},
	}
	for _, c := range accepted {
		if _, err := Compile([]string{wrap(c.ct)}, "", Version11); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// ...and the 1.0 subset of them.
	for _, c := range accepted[:3] {
		if strings.Contains(c.ct, "assert") {
			continue
		}
		if _, err := Compile([]string{wrap(c.ct)}, "", Version10); err != nil {
			t.Errorf("%s, XSD 1.0: %v", c.name, err)
		}
	}
	if _, err := Compile([]string{wrap(accepted[4].ct)}, "", Version10); err != nil {
		t.Errorf("%s, XSD 1.0: %v", accepted[4].name, err)
	}
}

// TestMisplacedAssertionElements pins the neighbours of issue #22: xs:assert
// and xs:assertion are accepted only where the schema for schemas lists them
// (xs:assert in a complex type's content, xs:assertion among the facets of a
// simple type or simple-content restriction), nowhere else.
func TestMisplacedAssertionElements(t *testing.T) {
	wrap := func(body string) string {
		return `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">` + body + `</xs:schema>`
	}
	for name, body := range map[string]string{
		"assert under element":                `<xs:element name="e"><xs:assert test="true()"/></xs:element>`,
		"assert under simpleType restriction": `<xs:simpleType name="s"><xs:restriction base="xs:int"><xs:assert test="true()"/></xs:restriction></xs:simpleType>`,
		"assert under simpleType":             `<xs:simpleType name="s"><xs:assert test="true()"/><xs:restriction base="xs:int"/></xs:simpleType>`,
		"assertion under complexContent":      `<xs:complexType name="t"><xs:complexContent><xs:restriction base="xs:anyType"><xs:assertion test="true()"/></xs:restriction></xs:complexContent></xs:complexType>`,
		"assertion under extension":           `<xs:complexType name="t"><xs:simpleContent><xs:extension base="xs:int"><xs:assertion test="true()"/></xs:extension></xs:simpleContent></xs:complexType>`,
		"assertion under complexType":         `<xs:complexType name="t"><xs:assertion test="true()"/></xs:complexType>`,
		"assertion under element":             `<xs:element name="e"><xs:assertion test="true()"/></xs:element>`,
	} {
		if _, err := Compile([]string{wrap(body)}, "", Version11); err == nil || errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: want a schema error, got %v", name, err)
		}
	}
	for name, body := range map[string]string{
		"assertion in simpleType restriction":    `<xs:simpleType name="s"><xs:restriction base="xs:int"><xs:assertion test="$value gt 0"/></xs:restriction></xs:simpleType>`,
		"assertion in simpleContent restriction": `<xs:complexType name="b"><xs:simpleContent><xs:extension base="xs:int"/></xs:simpleContent></xs:complexType><xs:complexType name="t"><xs:simpleContent><xs:restriction base="b"><xs:assertion test="true()"/></xs:restriction></xs:simpleContent></xs:complexType>`,
		"assert in complexContent extension":     `<xs:complexType name="t"><xs:complexContent><xs:extension base="xs:anyType"><xs:assert test="true()"/></xs:extension></xs:complexContent></xs:complexType>`,
	} {
		if _, err := Compile([]string{wrap(body)}, "", Version11); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
