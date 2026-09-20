package xslt

import "testing"

func TestAccumulator(t *testing.T) {
	const hdr = `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>`

	cases := []struct {
		name  string
		sheet string
		src   string
		want  string
	}{
		{
			// A simple counting accumulator with a start-phase rule.
			// XSLT 3.0 §18.2: before(item) is the PRE-DESCENT value — the
			// count after the item's OWN start rule has fired — and
			// after(item) the post-descent value, so for a leaf both are the
			// same (conformance: accumulator-001/051).
			name: "counting accumulator before/after",
			sheet: hdr + `
  <xsl:accumulator name="count" initial-value="0">
    <xsl:accumulator-rule match="item" select="$value + 1"/>
  </xsl:accumulator>
  <xsl:template match="/r">
    <xsl:for-each select="item">b<xsl:value-of select="accumulator-before('count')"/>a<xsl:value-of select="accumulator-after('count')"/><xsl:text> </xsl:text></xsl:for-each>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<r><item/><item/><item/></r>`,
			want: "b1a1 b2a2 b3a3 ",
		},
		{
			// after() at the document root gives the final accumulated count.
			name: "final count via after at root",
			sheet: hdr + `
  <xsl:accumulator name="count" initial-value="0">
    <xsl:accumulator-rule match="item" select="$value + 1"/>
  </xsl:accumulator>
  <xsl:template match="/r">
    <xsl:value-of select="accumulator-after('count')"/>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<r><item/><item/><item/><item/></r>`,
			want: "4",
		},
		{
			// A summing accumulator over element string values.
			name: "summing accumulator",
			sheet: hdr + `
  <xsl:accumulator name="sum" initial-value="0">
    <xsl:accumulator-rule match="n" select="$value + number(.)"/>
  </xsl:accumulator>
  <xsl:template match="/r">
    <xsl:for-each select="n"><xsl:value-of select="accumulator-after('sum')"/>,</xsl:for-each>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<r><n>10</n><n>5</n><n>20</n></r>`,
			want: "10,15,35,",
		},
		{
			// Rule with a sequence-constructor body computing the new value from
			// $value. Concatenates a running label string.
			name: "body-valued rule",
			sheet: hdr + `
  <xsl:accumulator name="trail" initial-value="''">
    <xsl:accumulator-rule match="item"><xsl:value-of select="concat($value, @id)"/></xsl:accumulator-rule>
  </xsl:accumulator>
  <xsl:template match="/r">
    <xsl:for-each select="item"><xsl:value-of select="accumulator-after('trail')"/><xsl:text> </xsl:text></xsl:for-each>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<r><item id="a"/><item id="b"/><item id="c"/></r>`,
			want: "a ab abc ",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := transform(t, tc.sheet, tc.src, nil)
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

// TestAccumulatorEndPhase checks end-phase before/after semantics directly.
func TestAccumulatorEndPhase(t *testing.T) {
	const hdr = `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>`

	sheet := hdr + `
  <xsl:accumulator name="closed" initial-value="0">
    <xsl:accumulator-rule match="sec" phase="end" select="$value + 1"/>
  </xsl:accumulator>
  <xsl:template match="/r">
    <xsl:for-each select="sec">b<xsl:value-of select="accumulator-before('closed')"/>a<xsl:value-of select="accumulator-after('closed')"/><xsl:text> </xsl:text></xsl:for-each>
  </xsl:template>
</xsl:stylesheet>`
	// First sec: before=0 (nothing closed yet), after=1 (its end rule fired).
	// Second sec: before=1 (first already closed), after=2.
	got := transform(t, sheet, `<r><sec/><sec/></r>`, nil)
	want := "b0a1 b1a2 "
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
