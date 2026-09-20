package xslt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// strbAccSheet builds a stylesheet whose only source-document body is body,
// preceded by the given top-level declarations. use names the accumulators the
// source-document declares applicable: without @use-accumulators none is, and
// every read is XTDE3362 on BOTH paths (XSLT 3.0 §18.2).
func strbAccSheet(decls, body, streamable, use string) string {
	return `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"
                xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xsl:output method="text"/>
` + decls + `
  <xsl:template name="main">
    <xsl:source-document href="d.xml" streamable="` + streamable + `" use-accumulators="` + use + `">` + body + `</xsl:source-document>
  </xsl:template>
</xsl:stylesheet>`
}

// TestStreamAccumulators is the differential gate for incremental accumulator
// evaluation: a body that reads a streamable accumulator must both TAKE the
// streaming path and produce exactly what the same body produces over a fully
// materialized tree, where acc2walk computes the identical values in one eager
// pass.
func TestStreamAccumulators(t *testing.T) {
	dir := t.TempDir()
	strbWriteDoc(t, dir, "d.xml", 30)

	// A motionless rule over text nodes: the running sum of every <v>. XSLT 3.0
	// §18.2.8 requires exactly this of a streamable accumulator's rules, which
	// is what lets the driver drop each node as it completes.
	sum := `  <xsl:accumulator name="total" initial-value="0" streamable="yes">
    <xsl:accumulator-rule match="v/text()" select="$value + number(.)"/>
  </xsl:accumulator>`
	// A start/end pair, the shape the spec's own section-numbering example uses.
	depth := `  <xsl:accumulator name="depth" initial-value="0" streamable="yes">
    <xsl:accumulator-rule match="rec" phase="start" select="$value + 1"/>
    <xsl:accumulator-rule match="rec" phase="end" select="$value - 1"/>
  </xsl:accumulator>`

	cases := []struct {
		name       string
		decls      string
		body       string
		use        string
		wantStream bool
	}{
		{
			name:       "accumulator-after-per-record",
			decls:      sum,
			body:       `<xsl:for-each select="/data/rec"><xsl:value-of select="accumulator-after('total')"/>,</xsl:for-each>`,
			use:        "total",
			wantStream: true,
		},
		{
			name:       "accumulator-before-per-record",
			decls:      sum,
			body:       `<xsl:for-each select="/data/rec"><xsl:value-of select="accumulator-before('total')"/>,</xsl:for-each>`,
			use:        "total",
			wantStream: true,
		},
		{
			// A start/end rule pair: the value the body reads is the one left
			// after the record's own end-phase rule has fired.
			name:       "accumulator-start-and-end-phase",
			decls:      depth,
			body:       `<xsl:for-each select="/data/rec"><xsl:value-of select="accumulator-after('depth')"/>;</xsl:for-each>`,
			use:        "depth",
			wantStream: true,
		},
		{
			name:       "accumulator-inside-apply-templates-rule",
			decls:      sum,
			body:       `<xsl:apply-templates select="/data/rec"/>`,
			use:        "total",
			wantStream: true,
		},
		{
			name:       "accumulator-inside-iterate",
			decls:      sum,
			body:       `<xsl:iterate select="/data/rec"><xsl:value-of select="accumulator-after('total')"/>;</xsl:iterate>`,
			use:        "total",
			wantStream: true,
		},
		{
			name:       "accumulator-inside-grouping",
			decls:      sum,
			body:       `<xsl:for-each-group select="/data/rec" group-starting-with="rec[v = 1]">[<xsl:value-of select="accumulator-after('total')"/>]</xsl:for-each-group>`,
			use:        "total",
			wantStream: true,
		},
		{
			// A computed accumulator name cannot be resolved before the stream
			// starts, so there is no way to know which rules to run.
			name:       "computed-accumulator-name-falls-back",
			decls:      sum,
			body:       `<xsl:for-each select="/data/rec"><xsl:value-of select="accumulator-after(concat('to','tal'))"/>,</xsl:for-each>`,
			use:        "total",
			wantStream: false,
		},
		{
			// No accumulator is read, so the driver keeps its fast path (every
			// subtree off the record path skipped unbuilt) — the declaration
			// alone must not turn it off.
			name:       "declared-but-unread-accumulator-still-streams",
			decls:      sum,
			body:       `<xsl:for-each select="/data/rec"><xsl:value-of select="v"/>,</xsl:for-each>`,
			use:        "total",
			wantStream: true,
		},
	}

	rules := `
  <xsl:template match="rec">(<xsl:value-of select="accumulator-after('total')"/>)</xsl:template>`

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mk := func(streamable string) string {
				s := strbAccSheet(tc.decls, tc.body, streamable, tc.use)
				return strings.Replace(s, "</xsl:stylesheet>", rules+"\n</xsl:stylesheet>", 1)
			}
			got, streamed := strbRun1(t, mk("yes"), dir)
			want, alsoStreamed := strbRun1(t, mk("no"), dir)
			if alsoStreamed != 0 {
				t.Fatalf("streamable=\"no\" took the streaming path %d time(s)", alsoStreamed)
			}
			if tc.wantStream && streamed != 1 {
				t.Fatalf("expected the streaming path to run once, it ran %d time(s)", streamed)
			}
			if !tc.wantStream && streamed != 0 {
				t.Fatalf("expected full materialization, but the streaming path ran %d time(s)", streamed)
			}
			if got != want {
				t.Errorf("streamed output differs from materialized output\n got: %q\nwant: %q", got, want)
			}
			if strings.TrimRight(got, ",;") == "" {
				t.Errorf("accumulator produced nothing: %q", got)
			}
		})
	}
}

// TestStreamNonStreamableAccumulatorRefused pins the rule that bounds this
// whole feature: a NON-streamable accumulator may not be read for a node in a
// document the stylesheet asked to have streamed (XSLT 3.0 §18.2, XTDE3362).
// The executor declines to stream such a body — so it must not answer from a
// partial tree — and the error then comes from the ordinary path, exactly as it
// did before incremental evaluation existed.
func TestStreamNonStreamableAccumulatorRefused(t *testing.T) {
	dir := t.TempDir()
	strbWriteDoc(t, dir, "d.xml", 5)
	decls := `  <xsl:accumulator name="total" initial-value="0">
    <xsl:accumulator-rule match="v/text()" select="$value + number(.)"/>
  </xsl:accumulator>`
	body := `<xsl:for-each select="/data/rec"><xsl:value-of select="accumulator-after('total')"/>,</xsl:for-each>`

	ss, err := CompileFrom(strbAccSheet(decls, body, "yes", "total"), dir)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	before := strbStreamedRuns.Load()
	if _, err = ss.TransformEntry("", Entry{Template: "main"}, dir); err == nil {
		t.Fatal("expected XTDE3362, got no error")
	}
	if !strings.Contains(err.Error(), "XTDE3362") {
		t.Fatalf("expected XTDE3362, got %v", err)
	}
	if n := strbStreamedRuns.Load() - before; n != 0 {
		t.Fatalf("the body must not stream at all, but the streaming path ran %d time(s)", n)
	}
}

// TestStreamAccumulatorSeesSkippedSubtrees pins the one thing incremental
// evaluation must not get wrong: an accumulator sees EVERY node, including the
// subtrees the driver would otherwise skip without building. A rule that fires
// only outside the record path is the sharpest form of the check — if the
// driver still skipped, the accumulator would read 0 where the buffered run
// reads the real count.
func TestStreamAccumulatorSeesSkippedSubtrees(t *testing.T) {
	dir := t.TempDir()
	src := `<?xml version="1.0"?>
<data>
  <meta><tag/><tag/><tag/></meta>
  <rec id="r1"><v>1</v></rec>
  <other><tag/><tag/></other>
  <rec id="r2"><v>2</v></rec>
</data>`
	if err := os.WriteFile(filepath.Join(dir, "d.xml"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	decls := `  <xsl:accumulator name="tags" initial-value="0" streamable="yes">
    <xsl:accumulator-rule match="tag" select="$value + 1"/>
  </xsl:accumulator>`
	body := `<xsl:for-each select="/data/rec"><xsl:value-of select="@id"/>=<xsl:value-of select="accumulator-before('tags')"/>;</xsl:for-each>`

	got, streamed := strbRun1(t, strbAccSheet(decls, body, "yes", "tags"), dir)
	want, alsoStreamed := strbRun1(t, strbAccSheet(decls, body, "no", "tags"), dir)
	if streamed != 1 || alsoStreamed != 0 {
		t.Fatalf("streaming path ran %d time(s) for streamable=yes and %d for streamable=no", streamed, alsoStreamed)
	}
	if got != want {
		t.Fatalf("streamed %q, materialized %q", got, want)
	}
	if got != "r1=3;r2=5;" {
		t.Fatalf("got %q, want %q — the accumulator did not see the subtrees off the record path", got, "r1=3;r2=5;")
	}
}

// TestStreamAccumulatorIsMemoryBounded checks that incremental evaluation kept
// the property the executor exists for. Two things could have made memory track
// the document and must not: the per-node value cache (an entry is released
// when its node is dropped) and the traversal that now builds the subtrees it
// used to skip (each node is dropped as it completes).
func TestStreamAccumulatorIsMemoryBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a large document")
	}
	dir := t.TempDir()
	_, smallSize := strbWriteDoc(t, dir, "small.xml", 20_000)
	_, bigSize := strbWriteDoc(t, dir, "big.xml", 400_000)

	decls := `  <xsl:accumulator name="total" initial-value="0" streamable="yes">
    <xsl:accumulator-rule match="v/text()" select="$value + number(.)"/>
  </xsl:accumulator>`
	// One number of output, read from the accumulator at every record and
	// carried forward, so nothing but the tree and the value cache could grow.
	body := `<xsl:iterate select="/data/rec">
      <xsl:param name="n" select="0"/>
      <xsl:next-iteration><xsl:with-param name="n" select="accumulator-after('total')"/></xsl:next-iteration>
      <xsl:on-completion>total=<xsl:value-of select="$n"/></xsl:on-completion>
    </xsl:iterate>`
	measure := func(file string) (string, uint64) {
		sheet := strings.Replace(strbAccSheet(decls, body, "yes", "total"), `href="d.xml"`, `href="`+file+`"`, 1)
		var out string
		var ran int64
		peak := strbPeakHeap(func() { out, ran = strbRun1(t, sheet, dir) })
		if ran != 1 {
			t.Fatalf("%s: streaming path ran %d time(s); the measurement would be meaningless", file, ran)
		}
		return out, peak
	}

	smallOut, smallPeak := measure("small.xml")
	bigOut, bigPeak := measure("big.xml")
	if smallOut == bigOut {
		t.Fatalf("the two documents should give different totals, got %q and %q", smallOut, bigOut)
	}
	mb := func(v uint64) float64 { return float64(v) / (1 << 20) }
	t.Logf("accumulating %6.1f MB input -> peak heap %5.1f MB", mb(uint64(smallSize)), mb(smallPeak))
	t.Logf("accumulating %6.1f MB input -> peak heap %5.1f MB", mb(uint64(bigSize)), mb(bigPeak))

	var growth uint64
	if bigPeak > smallPeak {
		growth = bigPeak - smallPeak
	}
	if limit := uint64(bigSize-smallSize) / 20; growth > limit {
		t.Errorf("peak heap grew by %d bytes for %d bytes of extra input (limit %d): the accumulator cache or the walk is retaining the document",
			growth, bigSize-smallSize, limit)
	}
	if bigPeak > 32<<20 {
		t.Errorf("peak heap %d exceeds the 32MB ceiling", bigPeak)
	}
}
