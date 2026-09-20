package xslt

import (
	"strings"
	"testing"
)

func TestTryCatch(t *testing.T) {
	const hdr = `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"
  xmlns:err="http://www.w3.org/2005/xqt-errors">
  <xsl:output method="text"/>
`
	src := `<r><n>10</n></r>`

	cases := []struct {
		name string
		body string
		want string
	}{
		{
			// Try body succeeds: its output is appended, catch is ignored.
			name: "success",
			body: `<xsl:try>
  <xsl:value-of select="n"/>
  <xsl:catch errors="*">CAUGHT</xsl:catch>
</xsl:try>`,
			want: "10",
		},
		{
			// Integer division by zero fails (err:FOAR0001); wildcard catch runs.
			name: "wildcard catch",
			body: `<xsl:try>
  <xsl:value-of select="1 idiv 0"/>
  <xsl:catch>caught</xsl:catch>
</xsl:try>`,
			want: "caught",
		},
		{
			// Specific matching error code catches the failure.
			name: "specific code match",
			body: `<xsl:try>
  <xsl:value-of select="1 idiv 0"/>
  <xsl:catch errors="err:FOAR0001">div-by-zero</xsl:catch>
</xsl:try>`,
			want: "div-by-zero",
		},
		{
			// First non-matching catch is skipped, wildcard fallback runs.
			name: "fallthrough to wildcard",
			body: `<xsl:try>
  <xsl:value-of select="1 idiv 0"/>
  <xsl:catch errors="err:XPTY0004">wrong</xsl:catch>
  <xsl:catch errors="*">fallback</xsl:catch>
</xsl:try>`,
			want: "fallback",
		},
		{
			// err:code is bound and usable inside the catch body.
			name: "error code variable",
			body: `<xsl:try>
  <xsl:value-of select="1 idiv 0"/>
  <xsl:catch>code=<xsl:value-of select="$err:code"/></xsl:catch>
</xsl:try>`,
			want: "code=err:FOAR0001",
		},
		{
			// @select form: success path appends the selected value.
			name: "select success",
			body: `<xsl:try select="n">
  <xsl:catch>nope</xsl:catch>
</xsl:try>`,
			want: "10",
		},
		{
			// @select form failing is caught.
			name: "select failure",
			body: `<xsl:try select="1 idiv 0">
  <xsl:catch>oops</xsl:catch>
</xsl:try>`,
			want: "oops",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sheet := hdr + `  <xsl:template match="/r">` + tc.body + `</xsl:template>
</xsl:stylesheet>`
			got := strings.TrimSpace(transform(t, sheet, src, nil))
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}
