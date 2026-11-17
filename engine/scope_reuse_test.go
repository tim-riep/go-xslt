package engine_test

import (
	"strings"
	"testing"

	"github.com/tim-riep/go-xslt/engine"
)

// TestFunctionFrameNotRecycledUnderClosure: a stylesheet function's frame is
// reused for the next call unless a function item captured the scope during
// the call. Closures made by successive calls must each keep their own
// parameter, and calls made after them must see fresh frames.
func TestFunctionFrameNotRecycledUnderClosure(t *testing.T) {
	ss := `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"
  xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:f="urn:f" exclude-result-prefixes="xs f">
  <xsl:output method="text"/>
  <xsl:function name="f:adder" as="function(xs:integer) as xs:integer">
    <xsl:param name="n" as="xs:integer"/>
    <xsl:variable name="m" select="$n * 10"/>
    <xsl:sequence select="function($x as xs:integer) as xs:integer { $x + $n + $m }"/>
  </xsl:function>
  <xsl:function name="f:plain" as="xs:integer">
    <xsl:param name="n" as="xs:integer"/>
    <xsl:variable name="m" select="$n + 1"/>
    <xsl:sequence select="$m * 2"/>
  </xsl:function>
  <xsl:template name="xsl:initial-template">
    <xsl:variable name="a" select="f:adder(1)"/>
    <xsl:variable name="p1" select="f:plain(5)"/>
    <xsl:variable name="b" select="f:adder(2)"/>
    <xsl:variable name="p2" select="f:plain(7)"/>
    <xsl:variable name="c" select="f:adder(3)"/>
    <xsl:value-of select="($a(100), $b(100), $c(100), $p1, $p2, $a(0), f:plain(1), $b(0))" separator=","/>
  </xsl:template>
</xsl:stylesheet>`
	r := engine.Transform(engine.Request{Stylesheet: ss})
	if r.HasErrors() {
		t.Fatalf("%v", r.Diagnostics)
	}
	want := "111,122,133,12,16,11,4,22"
	if got := strings.TrimSpace(r.Output); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
