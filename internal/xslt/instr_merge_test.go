package xslt

import (
	"strings"
	"testing"
)

func TestMerge(t *testing.T) {
	cases := []struct {
		name  string
		sheet string
		src   string
		want  string
	}{
		{
			name: "two sources merged by key",
			// Two lists of items, merged by @id; each merged group emits the
			// id followed by all values sharing that id (across both sources).
			sheet: ident + `
  <xsl:output method="text"/>
  <xsl:template match="/data">
    <xsl:merge>
      <xsl:merge-source select="a/item">
        <xsl:merge-key select="@id"/>
      </xsl:merge-source>
      <xsl:merge-source select="b/item">
        <xsl:merge-key select="@id"/>
      </xsl:merge-source>
      <xsl:merge-action>
        <xsl:value-of select="current-merge-key()"/>
        <xsl:text>:</xsl:text>
        <xsl:for-each select="current-merge-group()">
          <xsl:value-of select="."/><xsl:text>,</xsl:text>
        </xsl:for-each>
        <xsl:text> </xsl:text>
      </xsl:merge-action>
    </xsl:merge>
  </xsl:template>
</xsl:stylesheet>`,
			src: `<data>
  <a><item id="1">A1</item><item id="2">A2</item></a>
  <b><item id="1">B1</item><item id="3">B3</item></b>
</data>`,
			// merged in key order 1,2,3; group for 1 contains A1 and B1.
			want: "1:A1,B1, 2:A2, 3:B3,",
		},
		{
			name: "single source ordered by key",
			sheet: ident + `
  <xsl:output method="text"/>
  <xsl:template match="/list">
    <xsl:merge>
      <xsl:merge-source select="n">
        <xsl:merge-key select="."/>
      </xsl:merge-source>
      <xsl:merge-action>
        <xsl:value-of select="current-merge-key()"/><xsl:text> </xsl:text>
      </xsl:merge-action>
    </xsl:merge>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<list><n>alpha</n><n>bravo</n><n>charlie</n></list>`,
			want: "alpha bravo charlie",
		},
		{
			name: "numeric data-type ordering",
			sheet: ident + `
  <xsl:output method="text"/>
  <xsl:template match="/list">
    <xsl:merge>
      <xsl:merge-source select="n">
        <xsl:merge-key select="." data-type="number"/>
      </xsl:merge-source>
      <xsl:merge-action>
        <xsl:value-of select="current-merge-key()"/><xsl:text> </xsl:text>
      </xsl:merge-action>
    </xsl:merge>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<list><n>2</n><n>10</n><n>30</n></list>`,
			want: "2 10 30",
		},
		{
			name: "descending order",
			sheet: ident + `
  <xsl:output method="text"/>
  <xsl:template match="/list">
    <xsl:merge>
      <xsl:merge-source select="n">
        <xsl:merge-key select="." data-type="number" order="descending"/>
      </xsl:merge-source>
      <xsl:merge-action>
        <xsl:value-of select="current-merge-key()"/><xsl:text> </xsl:text>
      </xsl:merge-action>
    </xsl:merge>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<list><n>3</n><n>2</n><n>1</n></list>`,
			want: "3 2 1",
		},
		{
			name: "group count via current-merge-group",
			sheet: ident + `
  <xsl:output method="text"/>
  <xsl:template match="/data">
    <xsl:merge>
      <xsl:merge-source select="a/item">
        <xsl:merge-key select="@k"/>
      </xsl:merge-source>
      <xsl:merge-source select="b/item">
        <xsl:merge-key select="@k"/>
      </xsl:merge-source>
      <xsl:merge-action>
        <xsl:value-of select="current-merge-key()"/>
        <xsl:text>=</xsl:text>
        <xsl:value-of select="count(current-merge-group())"/>
        <xsl:text> </xsl:text>
      </xsl:merge-action>
    </xsl:merge>
  </xsl:template>
</xsl:stylesheet>`,
			src: `<data>
  <a><item k="x">1</item><item k="y">2</item></a>
  <b><item k="x">3</item><item k="x">4</item></b>
</data>`,
			// key x: items from a(1) + b(2) = 3; key y: 1.
			want: "x=3 y=1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := strings.TrimSpace(transform(t, tc.sheet, tc.src, nil))
			got := strings.Join(strings.Fields(out), " ")
			want := strings.Join(strings.Fields(tc.want), " ")
			if got != want {
				t.Errorf("got %q want %q", got, want)
			}
		})
	}
}
