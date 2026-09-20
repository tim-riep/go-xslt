package xslt

import (
	"strings"
	"testing"
)

// TestMapInstruction exercises xsl:map / xsl:map-entry. Because the node-tree
// exec model cannot return a non-node map value, the constructed map is read
// back through the registered context functions last-map() / current-map()
// (see instr_map.go for the rationale). map:get/map:size/map:keys/map:contains
// then query that map exactly as they would any XPath 3.1 map.
func TestMapInstruction(t *testing.T) {
	const header = `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/r">`
	const footer = `  </xsl:template>
</xsl:stylesheet>`

	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "string-key select-value",
			body: `<xsl:map>
			          <xsl:map-entry key="'a'" select="1"/>
			          <xsl:map-entry key="'b'" select="2"/>
			        </xsl:map>
			        <xsl:value-of select="map:get(last-map(),'a')"/>|<xsl:value-of select="map:get(last-map(),'b')"/>`,
			want: "1|2",
		},
		{
			name: "map size",
			body: `<xsl:map>
			          <xsl:map-entry key="'a'" select="10"/>
			          <xsl:map-entry key="'b'" select="20"/>
			          <xsl:map-entry key="'c'" select="30"/>
			        </xsl:map>
			        <xsl:value-of select="map:size(last-map())"/>`,
			want: "3",
		},
		{
			name: "integer key lookup",
			body: `<xsl:map>
			          <xsl:map-entry key="1" select="'one'"/>
			          <xsl:map-entry key="2" select="'two'"/>
			        </xsl:map>
			        <xsl:value-of select="map:get(last-map(),2)"/>`,
			want: "two",
		},
		{
			name: "contains and keys",
			body: `<xsl:map>
			          <xsl:map-entry key="'x'" select="'X'"/>
			          <xsl:map-entry key="'y'" select="'Y'"/>
			        </xsl:map>
			        <xsl:value-of select="map:contains(last-map(),'x')"/>|<xsl:value-of select="map:contains(last-map(),'z')"/>|<xsl:value-of select="string-join(map:keys(last-map()),',')"/>`,
			want: "true|false|x,y",
		},
		{
			name: "sequence-constructor body value",
			body: `<xsl:map>
			          <xsl:map-entry key="'greeting'">Hello</xsl:map-entry>
			        </xsl:map>
			        <xsl:value-of select="map:get(last-map(),'greeting')"/>`,
			want: "Hello",
		},
		{
			name: "computed key and value",
			body: `<xsl:map>
			          <xsl:map-entry key="concat('k', 1)" select="1 + 2"/>
			        </xsl:map>
			        <xsl:value-of select="map:get(last-map(),'k1')"/>`,
			want: "3",
		},
		{
			// XSLT 3.0 §11.5: two xsl:map-entry instructions in the same
			// xsl:map supplying the SAME key is the dynamic error XTDE3365
			// (W3C maps-908 relies on catching it) — not a silent last-wins
			// overwrite, which is what this test asserted before the error
			// was implemented.
			name: "duplicate key is XTDE3365",
			body: `<xsl:try>
			          <xsl:map>
			            <xsl:map-entry key="'d'" select="'first'"/>
			            <xsl:map-entry key="'d'" select="'second'"/>
			          </xsl:map>
			          <xsl:catch errors="*:XTDE3365">caught</xsl:catch>
			        </xsl:try>`,
			want: "caught",
		},
		{
			name: "empty map",
			body: `<xsl:map/>
			        <xsl:value-of select="map:size(last-map())"/>`,
			want: "0",
		},
		{
			name: "standalone map-entry observable via last-map",
			body: `<xsl:map-entry key="'solo'" select="'v'"/>
			        <xsl:value-of select="map:get(last-map(),'solo')"/>`,
			want: "v",
		},
		{
			name: "current-map reads siblings during build",
			body: `<xsl:map>
			          <xsl:map-entry key="'a'" select="'A'"/>
			          <xsl:map-entry key="'count'" select="map:size(current-map())"/>
			        </xsl:map>
			        <xsl:value-of select="map:get(last-map(),'count')"/>`,
			want: "1",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sheet := header + c.body + footer
			got := strings.TrimSpace(transform(t, sheet, `<r/>`, nil))
			if got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}

	// xsl:map merges the single-entry maps its contained sequence constructor
	// produces with "duplicates: reject" semantics (XSLT 3.0 §11.7, unlike
	// fn:map-merge's own default "use-first") — a repeated key within the
	// SAME enclosing xsl:map is a dynamic error (err:XTDE3365), not a silent
	// last-write-wins overwrite (W3C xslt30-test error-3365a).
	t.Run("duplicate key is a dynamic error", func(t *testing.T) {
		sheet := header + `<xsl:map>
			          <xsl:map-entry key="'d'" select="'first'"/>
			          <xsl:map-entry key="'d'" select="'second'"/>
			        </xsl:map>` + footer
		ss, err := Compile(sheet)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		_, _, err = ss.Transform(`<r/>`, nil)
		if err == nil {
			t.Fatal("want err:XTDE3365, got success")
		}
		if !strings.Contains(err.Error(), "XTDE3365") {
			t.Errorf("got error %v, want XTDE3365", err)
		}
	})
}
