package xsd

import (
	"strings"
	"testing"
)

// schemaFor wraps a simple-type body in a minimal schema declaring element
// <v> of that type, in no target namespace.
func schemaFor(typeBody string) string {
	return `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
	  <xs:element name="v" type="vt"/>
	  <xs:simpleType name="vt">` + typeBody + `</xs:simpleType>
	</xs:schema>`
}

func TestSimpleTypeValidation(t *testing.T) {
	cases := []struct {
		name    string
		typeXSD string
		value   string
		valid   bool
	}{
		{"decimal-in-range", `<xs:restriction base="xs:decimal"><xs:minInclusive value="0"/><xs:maxInclusive value="100"/></xs:restriction>`, "42.5", true},
		{"decimal-below-min", `<xs:restriction base="xs:decimal"><xs:minInclusive value="0"/></xs:restriction>`, "-1", false},
		// 18-digit exact comparison (the case float64 would round wrong).
		{"decimal-exact-boundary", `<xs:restriction base="xs:decimal"><xs:minExclusive value="-999999999999999999"/></xs:restriction>`, "-999999999999999998", true},
		{"decimal-exact-violation", `<xs:restriction base="xs:decimal"><xs:minExclusive value="-999999999999999999"/></xs:restriction>`, "-999999999999999999", false},
		{"integer-not-decimal", `<xs:restriction base="xs:integer"/>`, "1.5", false},
		{"date-valid", `<xs:restriction base="xs:date"/>`, "2024-02-29", true},
		{"date-bad-day", `<xs:restriction base="xs:date"/>`, "2023-02-29", false},
		{"enumeration-hit", `<xs:restriction base="xs:string"><xs:enumeration value="red"/><xs:enumeration value="green"/></xs:restriction>`, "green", true},
		{"enumeration-miss", `<xs:restriction base="xs:string"><xs:enumeration value="red"/></xs:restriction>`, "blue", false},
		{"maxLength-ok", `<xs:restriction base="xs:string"><xs:maxLength value="3"/></xs:restriction>`, "abc", true},
		{"maxLength-exceeded", `<xs:restriction base="xs:string"><xs:maxLength value="3"/></xs:restriction>`, "abcd", false},
		{"pattern-ok", `<xs:restriction base="xs:string"><xs:pattern value="[a-z]+"/></xs:restriction>`, "abc", true},
		{"pattern-miss", `<xs:restriction base="xs:string"><xs:pattern value="[a-z]+"/></xs:restriction>`, "abc1", false},
		{"length-ignored-on-qname", `<xs:restriction base="xs:QName"><xs:length value="1"/></xs:restriction>`, "a", true},
		{"list-maxlength-ok", `<xs:list itemType="xs:int"/>`, "1 2 3", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Validate([]string{schemaFor(tc.typeXSD)}, `<v>`+tc.value+`</v>`, "", Version10)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Valid != tc.valid {
				t.Fatalf("value %q: got valid=%v, want %v (errors: %v)", tc.value, res.Valid, tc.valid, res.Errors)
			}
		})
	}
}

func TestListLengthFacet(t *testing.T) {
	schema := `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
	  <xs:element name="v" type="vt"/>
	  <xs:simpleType name="lu"><xs:list itemType="xs:int"/></xs:simpleType>
	  <xs:simpleType name="vt"><xs:restriction base="lu"><xs:maxLength value="2"/></xs:restriction></xs:simpleType>
	</xs:schema>`
	for _, tc := range []struct {
		val   string
		valid bool
	}{{"1 2", true}, {"1 2 3", false}} {
		res, err := Validate([]string{schema}, `<v>`+tc.val+`</v>`, "", Version10)
		if err != nil {
			t.Fatalf("%q: %v", tc.val, err)
		}
		if res.Valid != tc.valid {
			t.Fatalf("list %q: got valid=%v want %v", tc.val, res.Valid, tc.valid)
		}
	}
}

func TestComplexTypeValidation(t *testing.T) {
	schema := `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
	  <xs:element name="v">
	    <xs:complexType>
	      <xs:sequence>
	        <xs:element name="a" type="xs:string"/>
	        <xs:element name="b" type="xs:int" minOccurs="0" maxOccurs="unbounded"/>
	      </xs:sequence>
	      <xs:attribute name="id" type="xs:int" use="required"/>
	    </xs:complexType>
	  </xs:element>
	</xs:schema>`
	cases := []struct {
		name  string
		xml   string
		valid bool
	}{
		{"ok", `<v id="1"><a>x</a><b>2</b><b>3</b></v>`, true},
		{"missing-required-attr", `<v><a>x</a></v>`, false},
		{"bad-attr-type", `<v id="x"><a>x</a></v>`, false},
		{"missing-required-child", `<v id="1"><b>2</b></v>`, false},
		{"bad-child-type", `<v id="1"><a>x</a><b>notint</b></v>`, false},
		{"undeclared-child", `<v id="1"><a>x</a><c/></v>`, false},
		{"undeclared-attr", `<v id="1" bogus="y"><a>x</a></v>`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Validate([]string{schema}, tc.xml, "", Version10)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Valid != tc.valid {
				t.Fatalf("got valid=%v want %v (errors: %v)", res.Valid, tc.valid, res.Errors)
			}
		})
	}
}

func TestIdentityConstraints(t *testing.T) {
	// <root> contains <item id=.. ref=..>; id is a key, ref is a keyref to it.
	schema := `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
	  <xs:element name="root">
	    <xs:complexType>
	      <xs:sequence>
	        <xs:element name="item" maxOccurs="unbounded">
	          <xs:complexType>
	            <xs:attribute name="id" type="xs:string"/>
	            <xs:attribute name="ref" type="xs:string"/>
	          </xs:complexType>
	        </xs:element>
	      </xs:sequence>
	    </xs:complexType>
	    <xs:key name="itemKey"><xs:selector xpath="item"/><xs:field xpath="@id"/></xs:key>
	    <xs:keyref name="itemRef" refer="itemKey"><xs:selector xpath="item"/><xs:field xpath="@ref"/></xs:keyref>
	  </xs:element>
	</xs:schema>`
	cases := []struct {
		name  string
		xml   string
		valid bool
	}{
		{"unique-keys-good-ref", `<root><item id="a"/><item id="b" ref="a"/></root>`, true},
		{"duplicate-key", `<root><item id="a"/><item id="a"/></root>`, false},
		{"dangling-keyref", `<root><item id="a" ref="zzz"/></root>`, false},
		{"keyref-missing-ok", `<root><item id="a"/><item id="b"/></root>`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Validate([]string{schema}, tc.xml, "", Version10)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Valid != tc.valid {
				t.Fatalf("got valid=%v want %v (errors: %v)", res.Valid, tc.valid, res.Errors)
			}
		})
	}
}

func TestUniqueConstraint(t *testing.T) {
	schema := `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
	  <xs:element name="root">
	    <xs:complexType><xs:sequence>
	      <xs:element name="p" type="xs:string" maxOccurs="unbounded"/>
	    </xs:sequence></xs:complexType>
	    <xs:unique name="u"><xs:selector xpath="p"/><xs:field xpath="."/></xs:unique>
	  </xs:element>
	</xs:schema>`
	for _, tc := range []struct {
		xml   string
		valid bool
	}{
		{`<root><p>x</p><p>y</p></root>`, true},
		{`<root><p>x</p><p>x</p></root>`, false},
	} {
		res, err := Validate([]string{schema}, tc.xml, "", Version10)
		if err != nil {
			t.Fatalf("%s: %v", tc.xml, err)
		}
		if res.Valid != tc.valid {
			t.Fatalf("unique %q: got valid=%v want %v", tc.xml, res.Valid, tc.valid)
		}
	}
}

func TestImportMissingLocationCompiles(t *testing.T) {
	// An xs:import whose location can't be resolved is legal — its namespace's
	// components are simply unavailable. The schema still compiles.
	schema := `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
	  <xs:import namespace="urn:x" schemaLocation="does-not-exist.xsd"/>
	  <xs:element name="v" type="xs:string"/>
	</xs:schema>`
	res, err := Validate([]string{schema}, `<v>hi</v>`, "", Version10)
	if err != nil {
		t.Fatalf("compile with unresolved import: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected valid, got %v", res.Errors)
	}
}

func TestSimpleTypeAssertion(t *testing.T) {
	// The simple-type xs:assertion facet (M31): $value is the typed value.
	schema := `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
	  <xs:element name="v" type="vt"/>
	  <xs:simpleType name="vt"><xs:restriction base="xs:integer">
	    <xs:assertion test="$value > 0"/>
	  </xs:restriction></xs:simpleType>
	</xs:schema>`
	sch, err := Compile([]string{schema}, "", Version11)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	res, err := sch.Validate(`<v>3</v>`)
	if err != nil || !res.Valid {
		t.Fatalf("positive value: valid=%v err=%v", res, err)
	}
	res, err = sch.Validate(`<v>-3</v>`)
	if err != nil || res.Valid {
		t.Fatalf("negative value must fail the assertion: valid=%v err=%v", res, err)
	}
}

func TestLargeInstanceCountingMatcher(t *testing.T) {
	// >600 children route to the counting matcher (bigmatch.go).
	schema := `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
	  <xs:element name="doc"><xs:complexType><xs:sequence>
	    <xs:element name="a" maxOccurs="unbounded"/>
	    <xs:element name="b"/>
	  </xs:sequence></xs:complexType></xs:element>
	</xs:schema>`
	sch, err := Compile([]string{schema}, "", Version11)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	big := strings.Repeat("<a/>", 1500)
	res, err := sch.Validate(`<doc>` + big + `<b/></doc>`)
	if err != nil || !res.Valid {
		t.Fatalf("1500 a's + b must be valid: %v %v", res, err)
	}
	res, err = sch.Validate(`<doc>` + big + `</doc>`)
	if err != nil || res.Valid {
		t.Fatalf("missing trailing b must be invalid: %v %v", res, err)
	}
	res, err = sch.Validate(`<doc>` + big + `<b/><c/></doc>`)
	if err != nil || res.Valid {
		t.Fatalf("stray trailing c must be invalid: %v %v", res, err)
	}
}
