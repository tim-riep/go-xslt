package xslt

import (
	"strings"
	"testing"
)

// TestStreamShapesAheadOfTheClassifier holds the shapes this executor can drive
// but streamability.go does not (yet) approve to the same differential standard
// as everything else: with the classifier gate lifted they must stream AND
// produce byte-identical output to full materialization.
//
// Without this, those shapes would only ever be asserted to "fall back", which
// proves nothing about the code that runs them — and the day the classifier
// catches up, an untested path would go live. Each case names the reason the
// classifier declines it.
func TestStreamShapesAheadOfTheClassifier(t *testing.T) {
	dir := t.TempDir()
	strbWriteDoc(t, dir, "d.xml", 40)

	cases := []struct{ name, why, body string }{
		{
			name: "boolean-predicate-in-record-path",
			why:  "a consuming predicate on the record path makes the whole select consuming",
			body: `<xsl:for-each select="/data/rec[v = 3]"><xsl:value-of select="position()"/>:<xsl:value-of select="@id"/>,</xsl:for-each>`,
		},
		{
			name: "existence-predicate-in-record-path",
			why:  "same, for a bare existence test",
			body: `<xsl:for-each select="/data/rec[pad]"><xsl:value-of select="@id"/>,</xsl:for-each>`,
		},
		{
			name: "not-predicate-in-record-path",
			why:  "same, wrapped in fn:not",
			body: `<xsl:for-each select="/data/rec[not(v = 0)]"><xsl:value-of select="v"/></xsl:for-each>`,
		},
		{
			name: "predicated-records-dispatched-by-apply-templates",
			why:  "same, with the records dispatched to a template rule",
			body: `<xsl:apply-templates select="/data/rec[v = 1]"/>`,
		},
		{
			name: "grouping-over-predicated-records",
			why:  "same, feeding a boundary-driven grouping",
			body: `<xsl:for-each-group select="/data/rec[not(v = 5)]" group-ending-with="rec[v = 0]">(<xsl:value-of select="count(current-group())"/>)</xsl:for-each-group>`,
		},
		{
			name: "two-current-group-reads",
			why:  "two separate reads of current-group() in one body",
			body: `<xsl:for-each-group select="/data/rec" group-starting-with="rec[v = 1]">[<xsl:value-of select="count(current-group())"/>:<xsl:value-of select="current-group()[1]/@id"/>]</xsl:for-each-group>`,
		},
	}

	rules := `
  <xsl:template match="rec">(<xsl:value-of select="@id"/>=<xsl:value-of select="v"/>)</xsl:template>`

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mk := func(streamable string) string {
				return strbSheetHdr +
					`<xsl:source-document href="d.xml" streamable="` + streamable + `">` +
					tc.body + `</xsl:source-document>` + "\n" +
					strings.TrimSuffix(strbSheetFtr, "</xsl:stylesheet>") + rules + "\n</xsl:stylesheet>"
			}
			// The buffered reference is taken with the gate in place, so it is
			// produced exactly as a real run would produce it.
			want, alsoStreamed := strbRun1(t, mk("no"), dir)
			if alsoStreamed != 0 {
				t.Fatalf("streamable=\"no\" took the streaming path %d time(s)", alsoStreamed)
			}
			strbSkipClassify = true
			got, streamed := strbRun1(t, mk("yes"), dir)
			strbSkipClassify = false
			if streamed != 1 {
				t.Fatalf("with the classifier gate lifted this shape must stream, it ran %d time(s) (%s)", streamed, tc.why)
			}
			if got != want {
				t.Errorf("streamed output differs from materialized output\n got: %q\nwant: %q", got, want)
			}
			if got == "" {
				t.Errorf("produced nothing")
			}
		})
	}
}
