package xslt

import (
	"strings"
	"testing"
)

// TestSupportsStreamingProperty pins the capability claim, which used to be
// wrong for every caller that was not the conformance harness: the engine does
// stream a streamable xsl:source-document in bounded memory, so the default
// answer is "yes", and only a host that deliberately presents a non-streaming
// processor says otherwise.
func TestSupportsStreamingProperty(t *testing.T) {
	run := func() string {
		sheet := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/"><xsl:value-of select="system-property('xsl:supports-streaming')"/></xsl:template>
</xsl:stylesheet>`
		ss, err := CompileFrom(sheet, "")
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		out, _, err := ss.Transform("<_/>", nil)
		if err != nil {
			t.Fatalf("transform: %v", err)
		}
		return strings.TrimSpace(out)
	}

	if got := run(); got != "yes" {
		t.Errorf("default supports-streaming = %q, want \"yes\"", got)
	}
	// Independent of the XTSE3430 enforcement switch, which is what it used to
	// be tied to: enforcement decides what happens to a streamable declaration
	// the analysis cannot prove, not whether the feature exists.
	enforceBefore := strmEnforce.Load()
	SetEnforceStreamability(!enforceBefore)
	if got := run(); got != "yes" {
		t.Errorf("supports-streaming = %q after toggling enforcement, want \"yes\"", got)
	}
	SetEnforceStreamability(enforceBefore)

	SetStreamingClaim(false)
	if got := run(); got != "no" {
		t.Errorf("supports-streaming = %q after the host opted out, want \"no\"", got)
	}
	SetStreamingClaim(true)
	if got := run(); got != "yes" {
		t.Errorf("supports-streaming = %q after the host opted back in, want \"yes\"", got)
	}
}
