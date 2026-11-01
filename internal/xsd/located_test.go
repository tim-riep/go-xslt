package xsd

import (
	"errors"
	"testing"
)

// A schema error found by the schema-for-schemas walk carries the line and
// column of the element it concerns.
func TestSchemaErrorPosition(t *testing.T) {
	src := "<xs:schema xmlns:xs=\"http://www.w3.org/2001/XMLSchema\">\n" +
		"  <xs:element name=\"n\">\n" +
		"    <xs:complexType foo=\"1\"/>\n" +
		"  </xs:element>\n</xs:schema>"
	_, err := Compile([]string{src}, "", Version11)
	var xe *Error
	if !errors.As(err, &xe) {
		t.Fatalf("want an *Error, got %v", err)
	}
	if xe.Line != 3 || xe.Col != 5 {
		t.Errorf("error at %d:%d, want 3:5 (%v)", xe.Line, xe.Col, err)
	}
}
