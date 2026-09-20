package xslt

import (
	"strings"
	"testing"
)

const msgHeader = `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
`

// TestMessageAssertSuccess covers non-terminating, non-failing cases where the
// transform completes and produces output. xsl:message and a passing
// xsl:assert must not affect the result tree.
func TestMessageAssertSuccess(t *testing.T) {
	cases := []struct {
		name string
		body string
		src  string
		want string
		msgs []string // expected recorded messages, in order
	}{
		{
			name: "message-from-select",
			body: `<xsl:message select="'hi there'"/>OK`,
			src:  `<r/>`,
			want: "OK",
			msgs: []string{"hi there"},
		},
		{
			name: "message-from-body",
			body: `<xsl:message>note: <xsl:value-of select="/r/@n"/></xsl:message>OK`,
			src:  `<r n="42"/>`,
			want: "OK",
			msgs: []string{"note: 42"},
		},
		{
			name: "message-terminate-no",
			body: `<xsl:message terminate="no" select="'keep going'"/>DONE`,
			src:  `<r/>`,
			want: "DONE",
			msgs: []string{"keep going"},
		},
		{
			name: "assert-true-noop",
			body: `<xsl:assert test="1 = 1" select="'should not appear'"/>PASS`,
			src:  `<r/>`,
			want: "PASS",
			msgs: nil,
		},
		{
			name: "assert-true-from-context",
			body: `<xsl:assert test="/r/@n = '5'">bad</xsl:assert>GOOD`,
			src:  `<r n="5"/>`,
			want: "GOOD",
			msgs: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sheet := msgHeader + `  <xsl:template match="/r">` + tc.body + `</xsl:template>
</xsl:stylesheet>`
			// Plain output check via the shared helper.
			got := strings.TrimSpace(transform(t, sheet, tc.src, nil))
			if got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
			// Inspect recorded messages via TransformFull.
			ss, err := Compile(sheet)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			res, err := ss.TransformFull(tc.src, nil, "")
			if err != nil {
				t.Fatalf("transform: %v", err)
			}
			if len(res.Messages) != len(tc.msgs) {
				t.Fatalf("messages = %v, want %v", res.Messages, tc.msgs)
			}
			for i, m := range tc.msgs {
				if res.Messages[i] != m {
					t.Fatalf("message[%d] = %q, want %q", i, res.Messages[i], m)
				}
			}
		})
	}
}

// TestMessageAssertError covers cases that must abort the transform with an
// error: a terminating xsl:message and a failing xsl:assert.
func TestMessageAssertError(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		src     string
		wantSub string // substring expected in the error message
	}{
		{
			name:    "message-terminate-yes",
			body:    `<xsl:message terminate="yes" select="'stop now'"/>UNREACHED`,
			src:     `<r/>`,
			wantSub: "xsl:message terminated: stop now",
		},
		{
			name:    "assert-false-select",
			body:    `<xsl:assert test="1 = 2" select="'math broke'"/>UNREACHED`,
			src:     `<r/>`,
			wantSub: "xsl:assert failed: math broke",
		},
		{
			name:    "assert-false-body",
			body:    `<xsl:assert test="/r/@n = 'x'">got <xsl:value-of select="/r/@n"/></xsl:assert>UNREACHED`,
			src:     `<r n="y"/>`,
			wantSub: "xsl:assert failed: got y",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sheet := msgHeader + `  <xsl:template match="/r">` + tc.body + `</xsl:template>
</xsl:stylesheet>`
			ss, err := Compile(sheet)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			_, _, err = ss.Transform(tc.src, nil)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}
