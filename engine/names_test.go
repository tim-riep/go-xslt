package engine_test

import (
	"strings"
	"testing"

	"github.com/tim-riep/go-xslt/engine"
)

// Issue #23: the name rules must agree. `castable as xs:NCName`, xsl:element /
// xsl:attribute / xsl:namespace / xsl:processing-instruction name checks and
// the XML parser all follow the XML 1.0 5th edition Name production.
const nameConsistencyXSL = `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"
    xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xsl:output method="text"/>
  <xsl:param name="list" as="xs:string"/>
  <xsl:variable name="names" select="tokenize($list, '&#9;')"/>
  <xsl:template name="xsl:initial-template">
    <xsl:for-each select="$names">
      <xsl:variable name="n" select="."/>
      <xsl:value-of select="string-join(string-to-codepoints($n) ! string(.), '.')"/>
      <xsl:text> castable=</xsl:text><xsl:value-of select="$n castable as xs:NCName"/>
      <xsl:text> element=</xsl:text>
      <xsl:try>
        <xsl:variable name="e"><xsl:element name="{$n}"/></xsl:variable>
        <xsl:text>ok</xsl:text>
        <xsl:catch><xsl:text>fail </xsl:text><xsl:value-of select="$err:code"/></xsl:catch>
      </xsl:try>
      <xsl:text> attribute=</xsl:text>
      <xsl:try>
        <xsl:variable name="e"><e><xsl:attribute name="{$n}">v</xsl:attribute></e></xsl:variable>
        <xsl:text>ok</xsl:text>
        <xsl:catch><xsl:text>fail </xsl:text><xsl:value-of select="$err:code"/></xsl:catch>
      </xsl:try>
      <xsl:text> pi=</xsl:text>
      <xsl:try>
        <xsl:variable name="e"><xsl:processing-instruction name="{$n}">v</xsl:processing-instruction></xsl:variable>
        <xsl:text>ok</xsl:text>
        <xsl:catch><xsl:text>fail </xsl:text><xsl:value-of select="$err:code"/></xsl:catch>
      </xsl:try>
      <xsl:text> namespace=</xsl:text>
      <xsl:try>
        <xsl:variable name="e"><e><xsl:namespace name="{$n}" select="'urn:x'"/></e></xsl:variable>
        <xsl:text>ok</xsl:text>
        <xsl:catch><xsl:text>fail </xsl:text><xsl:value-of select="$err:code"/></xsl:catch>
      </xsl:try>
      <xsl:text>&#10;</xsl:text>
    </xsl:for-each>
  </xsl:template>
</xsl:stylesheet>`

func runNames(t *testing.T, names ...string) []string {
	t.Helper()
	xsl := strings.Replace(nameConsistencyXSL, `xmlns:xs=`, `xmlns:err="http://www.w3.org/2005/xqt-errors" xmlns:xs=`, 1)
	res := engine.Transform(engine.Request{
		Stylesheet:      xsl,
		InitialTemplate: "xsl:initial-template",
		Params:          map[string]string{"list": strings.Join(names, "\t")},
	})
	if res.HasErrors() {
		t.Fatalf("diagnostics: %+v", res.Diagnostics)
	}
	return strings.Split(strings.TrimRight(res.Output, "\n"), "\n")
}

func TestNameRulesAgreeOnFifthEditionNames(t *testing.T) {
	names := []string{"\U0001F600", "\u09EA", "a\U0001F600", "\U00010000x", "\u0133", "a\u0300", "x\u00B7"}
	for i, line := range runNames(t, names...) {
		want := "castable=true element=ok attribute=ok pi=ok namespace=ok"
		if !strings.HasSuffix(line, want) {
			t.Errorf("name %U...: %q, want suffix %q", []rune(names[i])[0], line, want)
		}
	}
}

func TestNameRulesAgreeOnIllegalNames(t *testing.T) {
	names := []string{"1a", "a\uFFFE", "\u0300a", "-a", "a b", "\u00D7", "\u00B5", "a:b:c", ""}
	lines := runNames(t, names...)
	if len(lines) != len(names) {
		t.Fatalf("got %d lines: %q", len(lines), lines)
	}
	for i, line := range lines {
		if !strings.Contains(line, "castable=false") {
			t.Errorf("name %q: %q is castable", names[i], line)
		}
		if strings.Contains(line, "=ok") {
			t.Errorf("name %q: %q is accepted somewhere", names[i], line)
		}
	}
}

func TestParserAcceptsFifthEditionNames(t *testing.T) {
	xsl := `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="xml" omit-xml-declaration="yes"/>
  <xsl:template match="/*">
    <out><xsl:value-of select="name()"/>|<xsl:value-of select="count(descendant::*)"/>|<xsl:copy-of select="."/></out>
  </xsl:template>
</xsl:stylesheet>`
	for _, c := range []struct{ src, want string }{
		{"<\U0001F600/>", "<out>\U0001F600|0|<\U0001F600/></out>"},
		{"<a><\u09EA/></a>", "<out>a|1|<a><\u09EA/></a></out>"},
		{"<?xml version=\"1.0\"?><a \U0001F600x=\"1\"><\u0133/></a>", "<out>a|1|<a \U0001F600x=\"1\"><\u0133/></a></out>"},
	} {
		res := engine.Transform(engine.Request{Stylesheet: xsl, Source: c.src})
		if res.HasErrors() {
			t.Errorf("%q: %+v", c.src, res.Diagnostics)
			continue
		}
		if strings.TrimSpace(res.Output) != c.want {
			t.Errorf("%q: got %q, want %q", c.src, res.Output, c.want)
		}
	}
	for _, src := range []string{"<a\uFFFE/>", "<1a/>", "<a><\u0300b/></a>"} {
		if res := engine.Transform(engine.Request{Stylesheet: xsl, Source: src}); !res.HasErrors() {
			t.Errorf("%q was accepted: %q", src, res.Output)
		}
	}
}

// A stylesheet may itself use such names, in literal result elements and in
// XPath name tests, and fn:parse-xml reads them like any other document.
func TestFifthEditionNamesInStylesheetAndParseXML(t *testing.T) {
	xsl := `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="xml" omit-xml-declaration="yes"/>
  <xsl:template match="/">
    <\U0001F600 \u09EA="v"><xsl:value-of select="count(parse-xml('&lt;r>&lt;\U0001F600/>&lt;\U0001F600/>&lt;/r>')/r/\U0001F600)"/><xsl:value-of select="count(/r/\u09EA)"/></\U0001F600>
  </xsl:template>
</xsl:stylesheet>`
	xsl = strings.NewReplacer(`\U0001F600`, "\U0001F600", `\u09EA`, "\u09EA").Replace(xsl)
	res := engine.Transform(engine.Request{Stylesheet: xsl, Source: "<r><\u09EA/></r>"})
	if res.HasErrors() {
		t.Fatalf("%+v", res.Diagnostics)
	}
	if want := "<\U0001F600 \u09EA=\"v\">21</\U0001F600>"; strings.TrimSpace(res.Output) != want {
		t.Errorf("got %q, want %q", res.Output, want)
	}
}

// The prefix and the namespace declaration of a 5th-edition name come through
// the engine unchanged: name(), namespace-uri(), the namespace axis and a
// deep copy.
func TestFifthEditionPrefixThroughEngine(t *testing.T) {
	xsl := `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="xml" omit-xml-declaration="yes"/>
  <xsl:template match="/">
    <xsl:copy-of select="."/>|<xsl:value-of select="name(/*), namespace-uri(/*), prefix-from-QName(node-name(/*)), /*/namespace::*[not(name()='xml')]/name()" separator=" "/>
  </xsl:template>
</xsl:stylesheet>`
	src := "<\U0001F600:a xmlns:\U0001F600=\"u\"><!-- c \U0001F600 --><?p \U0001F600?><\u09EA b\U0001F600=\"1\"/></\U0001F600:a>"
	res := engine.Transform(engine.Request{Stylesheet: xsl, Source: src})
	if res.HasErrors() {
		t.Fatalf("%+v", res.Diagnostics)
	}
	want := src + "|\U0001F600:a u \U0001F600 \U0001F600"
	if got := strings.TrimSpace(res.Output); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if strings.Contains(res.Output, "Zq5e") {
		t.Errorf("a repair token leaked: %q", res.Output)
	}
}
