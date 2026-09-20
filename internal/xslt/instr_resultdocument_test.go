package xslt

import (
	"testing"
)

// rdRun compiles and transforms, returning both the principal output and the
// secondary documents produced by xsl:result-document.
func rdRun(t *testing.T, sheet, src string) (string, []SecondaryDoc) {
	t.Helper()
	ss, err := Compile(sheet)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	res, err := ss.TransformFull(src, nil, "")
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	return res.Output, res.Secondary
}

func TestResultDocumentMainOutputUnaffected(t *testing.T) {
	// The body of xsl:result-document must NOT appear in the principal output.
	cases := []struct {
		name  string
		sheet string
		src   string
		want  string
	}{
		{
			name: "content goes only to secondary",
			sheet: `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="xml" omit-xml-declaration="yes"/>
  <xsl:template match="/r">
    <main>
      <xsl:result-document href="side.xml">
        <secondary>hidden</secondary>
      </xsl:result-document>
      <kept/>
    </main>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<r/>`,
			want: `<main><kept/></main>`,
		},
		{
			name: "text output untouched by result-document",
			sheet: `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/r">main<xsl:result-document href="o.xml"><x/></xsl:result-document>done</xsl:template>
</xsl:stylesheet>`,
			src:  `<r/>`,
			want: `maindone`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := transform(t, tc.sheet, tc.src, nil)
			if got != tc.want {
				t.Errorf("main output: got %q want %q", got, tc.want)
			}
		})
	}
}

func TestResultDocumentSecondary(t *testing.T) {
	cases := []struct {
		name       string
		sheet      string
		src        string
		wantHref   string
		wantMethod string
		want       string
	}{
		{
			// A secondary document's own serialization defaults follow the
			// spec (an XML declaration is included unless explicitly
			// omitted) — not the principal xsl:output's @method="text",
			// which doesn't apply to it at all.
			name: "default method is xml with declaration included",
			sheet: `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/r">
    <xsl:result-document href="out.xml">
      <doc><v>1</v></doc>
    </xsl:result-document>
  </xsl:template>
</xsl:stylesheet>`,
			src:        `<r/>`,
			wantHref:   "out.xml",
			wantMethod: "xml",
			want:       `<?xml version="1.0" encoding="UTF-8"?><doc><v>1</v></doc>`,
		},
		{
			name: "explicit text method serializes string value only",
			sheet: `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/r">
    <xsl:result-document href="out.txt" method="text">hello <x>world</x></xsl:result-document>
  </xsl:template>
</xsl:stylesheet>`,
			src:        `<r/>`,
			wantHref:   "out.txt",
			wantMethod: "text",
			want:       `hello world`,
		},
		{
			name: "href is an AVT",
			sheet: `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/r">
    <xsl:result-document href="{@name}.xml"><n/></xsl:result-document>
  </xsl:template>
</xsl:stylesheet>`,
			src:        `<r name="part1"/>`,
			wantHref:   "part1.xml",
			wantMethod: "xml",
			want:       `<?xml version="1.0" encoding="UTF-8"?><n/>`,
		},
		{
			name: "method is an AVT",
			sheet: `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/r">
    <xsl:result-document href="x" method="{@m}">data</xsl:result-document>
  </xsl:template>
</xsl:stylesheet>`,
			src:        `<r m="text"/>`,
			wantHref:   "x",
			wantMethod: "text",
			want:       `data`,
		},
		{
			name: "value-of inside body computes content",
			sheet: `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/r">
    <xsl:result-document href="c.xml">
      <total><xsl:value-of select="sum(item)"/></total>
    </xsl:result-document>
  </xsl:template>
</xsl:stylesheet>`,
			src:        `<r><item>2</item><item>3</item></r>`,
			wantHref:   "c.xml",
			wantMethod: "xml",
			want:       `<?xml version="1.0" encoding="UTF-8"?><total>5</total>`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, sec := rdRun(t, tc.sheet, tc.src)
			if len(sec) != 1 {
				t.Fatalf("got %d secondary docs, want 1", len(sec))
			}
			d := sec[0]
			if d.Href != tc.wantHref {
				t.Errorf("href: got %q want %q", d.Href, tc.wantHref)
			}
			if d.Method != tc.wantMethod {
				t.Errorf("method: got %q want %q", d.Method, tc.wantMethod)
			}
			if d.Content != tc.want {
				t.Errorf("content: got %q want %q", d.Content, tc.want)
			}
		})
	}
}

func TestResultDocumentMultiple(t *testing.T) {
	sheet := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/r">
    <xsl:for-each select="p">
      <xsl:result-document href="{@id}.xml" method="text"><xsl:value-of select="."/></xsl:result-document>
    </xsl:for-each>
  </xsl:template>
</xsl:stylesheet>`
	src := `<r><p id="a">A</p><p id="b">B</p></r>`
	_, sec := rdRun(t, sheet, src)
	if len(sec) != 2 {
		t.Fatalf("got %d secondary docs, want 2", len(sec))
	}
	type kv struct{ h, c string }
	got := []kv{{sec[0].Href, sec[0].Content}, {sec[1].Href, sec[1].Content}}
	want := []kv{{"a.xml", "A"}, {"b.xml", "B"}}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("doc %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}
