package xslt

import (
	"strings"
	"testing"
)

func TestIterate(t *testing.T) {
	const hdr = `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
`
	cases := []struct {
		name string
		body string
		src  string
		want string
	}{
		{
			name: "running-sum-with-on-completion",
			body: `<xsl:template match="/list">
    <xsl:iterate select="n">
      <xsl:param name="sum" select="0"/>
      <xsl:next-iteration>
        <xsl:with-param name="sum" select="$sum + ."/>
      </xsl:next-iteration>
      <xsl:on-completion>total=<xsl:value-of select="$sum"/></xsl:on-completion>
    </xsl:iterate>
  </xsl:template>`,
			src:  `<list><n>1</n><n>2</n><n>3</n><n>4</n></list>`,
			want: "total=10",
		},
		{
			name: "emit-each-item",
			body: `<xsl:template match="/list">
    <xsl:iterate select="n">
      <xsl:value-of select="."/><xsl:text>,</xsl:text>
    </xsl:iterate>
  </xsl:template>`,
			src:  `<list><n>a</n><n>b</n><n>c</n></list>`,
			want: "a,b,c,",
		},
		{
			name: "break-stops-on-condition",
			body: `<xsl:template match="/list">
    <xsl:iterate select="n">
      <xsl:choose>
        <xsl:when test=". = 'stop'"><xsl:break/></xsl:when>
        <xsl:otherwise><xsl:value-of select="."/></xsl:otherwise>
      </xsl:choose>
    </xsl:iterate>
  </xsl:template>`,
			src:  `<list><n>x</n><n>y</n><n>stop</n><n>z</n></list>`,
			want: "xy",
		},
		{
			name: "break-with-result-body",
			body: `<xsl:template match="/list">
    <xsl:iterate select="n">
      <xsl:param name="seen" select="0"/>
      <xsl:choose>
        <xsl:when test=". &gt; 2">
          <xsl:break>found after <xsl:value-of select="$seen"/></xsl:break>
        </xsl:when>
        <xsl:otherwise>
          <xsl:next-iteration>
            <xsl:with-param name="seen" select="$seen + 1"/>
          </xsl:next-iteration>
        </xsl:otherwise>
      </xsl:choose>
    </xsl:iterate>
  </xsl:template>`,
			src:  `<list><n>1</n><n>2</n><n>3</n><n>4</n></list>`,
			want: "found after 2",
		},
		{
			name: "on-completion-runs-when-no-break",
			body: `<xsl:template match="/list">
    <xsl:iterate select="n">
      <xsl:param name="count" select="0"/>
      <xsl:next-iteration>
        <xsl:with-param name="count" select="$count + 1"/>
      </xsl:next-iteration>
      <xsl:on-completion>done:<xsl:value-of select="$count"/></xsl:on-completion>
    </xsl:iterate>
  </xsl:template>`,
			src:  `<list><n>a</n><n>b</n></list>`,
			want: "done:2",
		},
		{
			name: "atomic-sequence",
			body: `<xsl:template match="/r">
    <xsl:iterate select="1 to 3">
      <xsl:value-of select="."/>
    </xsl:iterate>
  </xsl:template>`,
			src:  `<r/>`,
			want: "123",
		},
		{
			name: "multiple-params-carry-unmentioned",
			body: `<xsl:template match="/list">
    <xsl:iterate select="n">
      <xsl:param name="a" select="'A'"/>
      <xsl:param name="b" select="0"/>
      <xsl:value-of select="$a"/><xsl:value-of select="$b"/><xsl:text>;</xsl:text>
      <xsl:next-iteration>
        <xsl:with-param name="b" select="$b + 1"/>
      </xsl:next-iteration>
    </xsl:iterate>
  </xsl:template>`,
			src:  `<list><n>x</n><n>y</n><n>z</n></list>`,
			want: "A0;A1;A2;",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sheet := hdr + tc.body + "\n</xsl:stylesheet>"
			out := strings.TrimSpace(transform(t, sheet, tc.src, nil))
			out = strings.Join(strings.Fields(out), " ")
			want := strings.Join(strings.Fields(tc.want), " ")
			if out != want {
				t.Errorf("got %q want %q", out, want)
			}
		})
	}
}
