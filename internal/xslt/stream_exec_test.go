package xslt

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// strbWriteDoc writes a record-structured document into dir and returns its
// path and byte size.
func strbWriteDoc(t *testing.T, dir, name string, records int) (string, int64) {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?>` + "\n<data>\n")
	for i := 1; i <= records; i++ {
		fmt.Fprintf(&b, "  <rec id=\"r%d\"><v>%d</v><pad>%s</pad></rec>\n", i, i%7, strings.Repeat("x", 40))
		if b.Len() > 1<<20 {
			if _, err := f.WriteString(b.String()); err != nil {
				t.Fatalf("write: %v", err)
			}
			b.Reset()
		}
	}
	b.WriteString("</data>\n")
	if _, err := f.WriteString(b.String()); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return p, st.Size()
}

// strbRun1 compiles sheet and runs its named "main" template with baseDir,
// returning the principal output and how many source-documents actually took
// the streaming path.
func strbRun1(t *testing.T, sheet, baseDir string) (string, int64) {
	t.Helper()
	ss, err := CompileFrom(sheet, baseDir)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	before := strbStreamedRuns.Load()
	res, err := ss.TransformEntry("", Entry{Template: "main"}, baseDir)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	return res.Output, strbStreamedRuns.Load() - before
}

const strbSheetHdr = `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"
                xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xsl:output method="text"/>
  <xsl:template name="main">`

const strbSheetFtr = `  </xsl:template>
</xsl:stylesheet>`

// TestStreamMatchesFullMaterialization is the differential gate for the whole
// feature: for every body shape the executor claims to stream, the streamed run
// must produce byte-identical output to the same body run the old way. It also
// asserts which of the two paths was taken, so a body that silently stopped
// streaming (or started streaming when it should not) fails here rather than
// passing by accident.
func TestStreamMatchesFullMaterialization(t *testing.T) {
	dir := t.TempDir()
	strbWriteDoc(t, dir, "d.xml", 50)

	cases := []struct {
		name       string
		body       string
		wantStream bool
	}{
		{
			name:       "for-each-over-records",
			body:       `<xsl:for-each select="/data/rec"><xsl:value-of select="v"/>,</xsl:for-each>`,
			wantStream: true,
		},
		{
			name:       "for-each-with-filter-and-position",
			body:       `<xsl:for-each select="/data/rec"><xsl:if test="v = 3">[<xsl:value-of select="position()"/>:<xsl:value-of select="@id"/>]</xsl:if></xsl:for-each>`,
			wantStream: true,
		},
		{
			name:       "wildcard-step",
			body:       `<xsl:for-each select="*/rec"><xsl:value-of select="v"/></xsl:for-each>`,
			wantStream: true,
		},
		{
			name:       "literal-element-wrapper",
			body:       `<out><xsl:for-each select="/data/rec"><xsl:value-of select="v"/></xsl:for-each></out>`,
			wantStream: true,
		},
		{
			// A literal element INSIDE the record may read it — that is the
			// point — unlike the wrapper above, which is evaluated before
			// anything has been read.
			name:       "literal-element-inside-record-reads-it",
			body:       `<out><xsl:for-each select="/data/rec"><item id="{@id}" v="{v}"/></xsl:for-each></out>`,
			wantStream: true,
		},
		{
			// The classic streaming idiom (Saxon accepts this shape), but the
			// current classifier (streamability.go) is conservative about a
			// copy-of nested inside a for-each's body and rejects it — an
			// accepted, documented gap (see CLAUDE.md's streaming milestones:
			// "an early classifier will be over-conservative... never produces
			// wrong output — the correct direction to fail"), not a regression.
			// A bare top-level xsl:copy-of (below) is unaffected.
			name:       "copy-of-filtered-by-inner-if",
			body:       `<xsl:for-each select="/data/rec"><xsl:if test="v = 0"><xsl:copy-of select="."/></xsl:if></xsl:for-each>`,
			wantStream: false,
		},
		{
			name:       "top-level-copy-of",
			body:       `<xsl:copy-of select="/data/rec"/>`,
			wantStream: true,
		},
		{
			name: "iterate-running-total",
			body: `<xsl:iterate select="/data/rec">
        <xsl:param name="n" select="0"/>
        <xsl:next-iteration><xsl:with-param name="n" select="$n + number(v)"/></xsl:next-iteration>
        <xsl:on-completion>total=<xsl:value-of select="$n"/></xsl:on-completion>
      </xsl:iterate>`,
			wantStream: true,
		},
		{
			name: "iterate-with-break",
			body: `<xsl:iterate select="/data/rec">
        <xsl:choose>
          <xsl:when test="@id = 'r10'"><xsl:break>stopped</xsl:break></xsl:when>
          <xsl:otherwise><xsl:value-of select="v"/></xsl:otherwise>
        </xsl:choose>
      </xsl:iterate>`,
			wantStream: true,
		},
		{
			name:       "apply-templates-dispatch",
			body:       `<xsl:apply-templates select="/data/rec"/>`,
			wantStream: true,
		},
		{
			// Streamed apply-templates dispatches one record at a time, so
			// position() inside the rule would be 1 for every record where the
			// buffered path counts up. Rules in the mode are scanned for it.
			name:       "apply-templates-mode-using-position-falls-back",
			body:       `<xsl:apply-templates select="/data/rec" mode="p"/>`,
			wantStream: false,
		},
		// Shapes the classifier must REFUSE: each reads outside the record, or
		// needs the whole selection at once. They must still give the right
		// answer, via full materialization.
		{
			name:       "absolute-path-inside-body-falls-back",
			body:       `<xsl:for-each select="/data/rec"><xsl:value-of select="count(/data/rec)"/></xsl:for-each>`,
			wantStream: false,
		},
		{
			name:       "parent-axis-falls-back",
			body:       `<xsl:for-each select="/data/rec"><xsl:value-of select="../@id"/>.</xsl:for-each>`,
			wantStream: false,
		},
		{
			name:       "last-falls-back",
			body:       `<xsl:for-each select="/data/rec"><xsl:if test="position() = last()">L<xsl:value-of select="@id"/></xsl:if></xsl:for-each>`,
			wantStream: false,
		},
		{
			name:       "preceding-sibling-falls-back",
			body:       `<xsl:for-each select="/data/rec"><xsl:value-of select="count(preceding-sibling::rec)"/>,</xsl:for-each>`,
			wantStream: false,
		},
		{
			name:       "sort-falls-back",
			body:       `<xsl:for-each select="/data/rec"><xsl:sort select="v"/><xsl:value-of select="v"/></xsl:for-each>`,
			wantStream: false,
		},
		{
			name:       "descendant-path-falls-back",
			body:       `<xsl:for-each select="//rec"><xsl:value-of select="v"/></xsl:for-each>`,
			wantStream: false,
		},
		{
			// A BOOLEAN predicate on the last step filters records, which the
			// driver applies to each record once it is materialized. position()
			// must count only the records that survive the filter.
			name:       "boolean-predicate-in-record-path",
			body:       `<xsl:for-each select="/data/rec[v = 3]"><xsl:value-of select="position()"/>:<xsl:value-of select="@id"/>,</xsl:for-each>`,
			wantStream: false, // classifier-gated; see TestStreamShapesAheadOfTheClassifier
		},
		{
			name:       "existence-predicate-in-record-path",
			body:       `<xsl:for-each select="/data/rec[pad]"><xsl:value-of select="@id"/>,</xsl:for-each>`,
			wantStream: false, // classifier-gated; see TestStreamShapesAheadOfTheClassifier
		},
		{
			name:       "not-predicate-in-record-path",
			body:       `<xsl:for-each select="/data/rec[not(v = 0)]"><xsl:value-of select="v"/></xsl:for-each>`,
			wantStream: false, // classifier-gated; see TestStreamShapesAheadOfTheClassifier
		},
		{
			name:       "predicate-with-apply-templates",
			body:       `<xsl:apply-templates select="/data/rec[v = 1]"/>`,
			wantStream: false, // classifier-gated; see TestStreamShapesAheadOfTheClassifier
		},
		{
			// A NUMERIC predicate selects by position WITHIN THE PARENT, which a
			// reader seeing records one at a time across many parents does not
			// have.
			name:       "positional-predicate-in-record-path-falls-back",
			body:       `<xsl:for-each select="/data/rec[1]"><xsl:value-of select="@id"/></xsl:for-each>`,
			wantStream: false,
		},
		{
			name:       "position-predicate-in-record-path-falls-back",
			body:       `<xsl:for-each select="/data/rec[position() &lt; 4]"><xsl:value-of select="@id"/></xsl:for-each>`,
			wantStream: false,
		},
		{
			// A predicate on an INNER step decides whether an element ON THE
			// PATH qualifies, and that can depend on content arriving after the
			// records inside it have been handed over.
			name:       "predicate-on-inner-step-falls-back",
			body:       `<xsl:for-each select="/data[rec]/rec"><xsl:value-of select="@id"/></xsl:for-each>`,
			wantStream: false,
		},
		{
			// A predicate that reaches outside the record is rejected like any
			// other such expression.
			name:       "predicate-reading-outside-the-record-falls-back",
			body:       `<xsl:for-each select="/data/rec[../@id]"><xsl:value-of select="@id"/></xsl:for-each>`,
			wantStream: false,
		},
		{
			name:       "grouping-over-predicated-records",
			body:       `<xsl:for-each-group select="/data/rec[not(v = 5)]" group-ending-with="rec[v = 0]">(<xsl:value-of select="count(current-group())"/>)</xsl:for-each-group>`,
			wantStream: false, // classifier-gated; see TestStreamShapesAheadOfTheClassifier
		},
		{
			// group-by cannot close a group before the end of the stream (the
			// first and last item of a document may share a key), so it is not
			// streamable at all — XSLT 3.0 §19.8.4.19 allows only the two
			// boundary-driven modes below.
			name:       "for-each-group-falls-back",
			body:       `<xsl:for-each-group select="/data/rec" group-by="v"><xsl:value-of select="current-grouping-key()"/>=<xsl:value-of select="count(current-group())"/>;</xsl:for-each-group>`,
			wantStream: false,
		},
		{
			name:       "for-each-group-adjacent-falls-back",
			body:       `<xsl:for-each-group select="/data/rec" group-adjacent="v"><xsl:value-of select="count(current-group())"/>;</xsl:for-each-group>`,
			wantStream: false,
		},
		{
			name:       "group-starting-with",
			body:       `<xsl:for-each-group select="/data/rec" group-starting-with="rec[v = 1]">[<xsl:value-of select="position()"/>:<xsl:value-of select="count(current-group())"/>]</xsl:for-each-group>`,
			wantStream: true,
		},
		{
			name:       "group-ending-with",
			body:       `<xsl:for-each-group select="/data/rec" group-ending-with="rec[v = 0]">[<xsl:value-of select="string-join(current-group()/@id, '+')"/>]</xsl:for-each-group>`,
			wantStream: true,
		},
		{
			// Two SEPARATE reads of current-group() in one body: each one on
			// its own streams (the cases above), but the classifier in
			// streamability.go combines them into a body it will not approve.
			// An accepted, documented gap on that side of the seam — this
			// executor drives the shape correctly either way, and the fallback
			// keeps the answer right.
			name:       "group-with-two-current-group-reads-falls-back",
			body:       `<xsl:for-each-group select="/data/rec" group-starting-with="rec[v = 1]">[<xsl:value-of select="count(current-group())"/>:<xsl:value-of select="current-group()[1]/@id"/>]</xsl:for-each-group>`,
			wantStream: false,
		},
		{
			// The last group of a group-ending-with run is the one no record
			// closed — it must still be emitted at end of stream.
			name:       "group-ending-with-unterminated-tail",
			body:       `<xsl:for-each-group select="/data/rec" group-ending-with="rec[@id = 'r10']">(<xsl:value-of select="count(current-group())"/>)</xsl:for-each-group>`,
			wantStream: true,
		},
		{
			// A grouping whose body copies whole group members: the records
			// have been detached from the streamed tree by then, so this pins
			// that the group still holds them intact.
			name:       "group-starting-with-copying-members",
			body:       `<xsl:for-each-group select="/data/rec" group-starting-with="rec[v = 1]"><g><xsl:copy-of select="current-group()"/></g></xsl:for-each-group>`,
			wantStream: true,
		},
		{
			name:       "for-each-group-with-sort-falls-back",
			body:       `<xsl:for-each-group select="/data/rec" group-starting-with="rec[v = 1]"><xsl:sort select="count(current-group())"/>[<xsl:value-of select="count(current-group())"/>]</xsl:for-each-group>`,
			wantStream: false,
		},
		{
			// xsl:fork: each branch computes its own result over the same
			// stream, and the results concatenate in branch order — which per
			// record means running every branch into its own buffer.
			name: "fork-two-for-each-branches",
			body: `<xsl:fork>
        <xsl:sequence><xsl:for-each select="/data/rec">a<xsl:value-of select="@id"/></xsl:for-each></xsl:sequence>
        <xsl:sequence><xsl:for-each select="/data/rec">b<xsl:value-of select="v"/></xsl:for-each></xsl:sequence>
      </xsl:fork>`,
			wantStream: true,
		},
		{
			name: "fork-iterate-branch-aggregates",
			body: `<xsl:fork>
        <xsl:sequence><xsl:for-each select="/data/rec"><xsl:value-of select="@id"/>,</xsl:for-each></xsl:sequence>
        <xsl:sequence><xsl:iterate select="/data/rec">
          <xsl:param name="n" select="0"/>
          <xsl:next-iteration><xsl:with-param name="n" select="$n + number(v)"/></xsl:next-iteration>
          <xsl:on-completion>|total=<xsl:value-of select="$n"/></xsl:on-completion>
        </xsl:iterate></xsl:sequence>
      </xsl:fork>`,
			wantStream: true,
		},
		{
			// xsl:break in one branch must end THAT branch only (and skip its
			// own xsl:on-completion): in the buffered model each branch walks
			// the input independently, so the other branches still see the
			// whole document.
			name: "fork-break-in-one-branch-only",
			body: `<xsl:fork>
        <xsl:sequence><xsl:iterate select="/data/rec">
          <xsl:choose>
            <xsl:when test="@id = 'r5'"><xsl:break>cut</xsl:break></xsl:when>
            <xsl:otherwise><xsl:value-of select="@id"/></xsl:otherwise>
          </xsl:choose>
        </xsl:iterate></xsl:sequence>
        <xsl:sequence><xsl:for-each select="/data/rec">|<xsl:value-of select="v"/></xsl:for-each></xsl:sequence>
      </xsl:fork>`,
			wantStream: true,
		},
		{
			// Every branch breaking must still flush what each produced.
			name: "fork-all-branches-break",
			body: `<xsl:fork>
        <xsl:sequence><xsl:iterate select="/data/rec">
          <xsl:choose>
            <xsl:when test="@id = 'r3'"><xsl:break>A</xsl:break></xsl:when>
            <xsl:otherwise><xsl:value-of select="@id"/></xsl:otherwise>
          </xsl:choose>
        </xsl:iterate></xsl:sequence>
        <xsl:sequence><xsl:iterate select="/data/rec">
          <xsl:choose>
            <xsl:when test="@id = 'r2'"><xsl:break>B</xsl:break></xsl:when>
            <xsl:otherwise><xsl:value-of select="v"/></xsl:otherwise>
          </xsl:choose>
        </xsl:iterate></xsl:sequence>
      </xsl:fork>`,
			wantStream: true,
		},
		{
			// Two branches walking different record paths would need two
			// independent readers over one stream.
			name: "fork-with-differing-record-paths-falls-back",
			body: `<xsl:fork>
        <xsl:sequence><xsl:for-each select="/data/rec"><xsl:value-of select="@id"/></xsl:for-each></xsl:sequence>
        <xsl:sequence><xsl:for-each select="/data/rec/v"><xsl:value-of select="."/></xsl:for-each></xsl:sequence>
      </xsl:fork>`,
			wantStream: false,
		},
		{
			// A branch that wraps its consumer in a literal element would have
			// to emit the wrapper once while its content arrives record by
			// record — a different construction problem, so it falls back.
			name: "fork-branch-with-wrapper-falls-back",
			body: `<xsl:fork>
        <xsl:sequence><out><xsl:for-each select="/data/rec"><xsl:value-of select="@id"/></xsl:for-each></out></xsl:sequence>
        <xsl:sequence><xsl:for-each select="/data/rec"><xsl:value-of select="v"/></xsl:for-each></xsl:sequence>
      </xsl:fork>`,
			wantStream: false,
		},
		// A wrapper attribute and an xsl:iterate initial parameter are both
		// evaluated ONCE, before any record has been read, so anything they
		// read off the context item would differ between the two paths (the
		// streamed document node is still empty there). count(*) is 1
		// buffered and would be 0 streamed — so these must not stream.
		{
			name:       "wrapper-attribute-reading-context-falls-back",
			body:       `<out n="{count(*)}"><xsl:for-each select="/data/rec"><xsl:value-of select="v"/></xsl:for-each></out>`,
			wantStream: false,
		},
		{
			name: "iterate-param-reading-context-falls-back",
			body: `<xsl:iterate select="/data/rec">
        <xsl:param name="n" select="count(*)"/>
        <xsl:next-iteration><xsl:with-param name="n" select="$n + number(v)"/></xsl:next-iteration>
        <xsl:on-completion>total=<xsl:value-of select="$n"/></xsl:on-completion>
      </xsl:iterate>`,
			wantStream: false,
		},
		{
			name:       "two-consumers-fall-back",
			body:       `<xsl:for-each select="/data/rec"><xsl:value-of select="v"/></xsl:for-each><xsl:for-each select="/data/rec"><xsl:value-of select="@id"/></xsl:for-each>`,
			wantStream: false,
		},
	}

	rules := `
  <xsl:template match="rec">(<xsl:value-of select="@id"/>=<xsl:value-of select="v"/>)</xsl:template>
  <xsl:template match="rec" mode="p">[<xsl:value-of select="position()"/>]</xsl:template>`

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mk := func(streamable string) string {
				return strbSheetHdr +
					`<xsl:source-document href="d.xml" streamable="` + streamable + `">` +
					tc.body + `</xsl:source-document>` + "\n" + strbSheetFtr[:len(strbSheetFtr)-len("</xsl:stylesheet>")] +
					rules + "\n</xsl:stylesheet>"
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
		})
	}
}

// TestStreamGenerateIDDistinct pins the order-stamping requirement: without it
// every streamed node shares Order()==0 and fn:generate-id — which the spec
// classifies as motionless, so no streamability analysis would ever flag it —
// returns one id for the whole document.
func TestStreamGenerateIDDistinct(t *testing.T) {
	dir := t.TempDir()
	strbWriteDoc(t, dir, "d.xml", 6)
	sheet := strbSheetHdr + `<xsl:source-document href="d.xml" streamable="yes">
      <xsl:for-each select="/data/rec"><xsl:value-of select="generate-id()"/>,</xsl:for-each>
    </xsl:source-document>` + "\n" + strbSheetFtr
	out, streamed := strbRun1(t, sheet, dir)
	if streamed != 1 {
		t.Fatalf("expected the streaming path, ran %d time(s)", streamed)
	}
	ids := strings.Split(strings.TrimSuffix(out, ","), ",")
	if len(ids) != 6 {
		t.Fatalf("got %d ids, want 6 (%q)", len(ids), out)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" {
			t.Fatalf("empty generate-id in %q", out)
		}
		if seen[id] {
			t.Fatalf("generate-id repeated %q across streamed records: %q", id, out)
		}
		seen[id] = true
	}
}

// TestStreamKeyIsRefused checks the runtime guard directly: keyIndexOn
// (transform.go) must refuse a root this engine is streaming right now,
// rather than answer from whatever fraction of the document happens to be
// attached at that instant.
//
// This is a direct, white-box check of the guard rather than an end-to-end
// stylesheet, deliberately: this executor's OWN static defenses (the
// classifier in streamability.go, and strbConsumerGrounded's scan of every
// instruction and every candidate template body a streamed dispatch could
// reach) already catch a key() call written directly in the streamed body,
// inside a called stylesheet function, or inside a dispatched template's own
// body — before the runtime guard is ever needed. Reaching it through some
// OTHER indirect route a static scan cannot see (a global variable's
// initializer, a template's own match pattern) turned out to depend on
// unrelated engine behaviour this test has no business asserting on (e.g.
// global-variable evaluation timing, or how a dynamic error during pattern
// matching itself propagates) — exercising the guard directly is the precise
// way to check it without those confounds.
func TestStreamKeyIsRefused(t *testing.T) {
	root := &xmltree.Node{Kind: xmltree.KindDocument}
	eng := &engine{scratch: map[string]any{
		strbLiveKey: map[*xmltree.Node]bool{root: true},
	}}
	_, err := eng.keyIndexOn("byv", root)
	if err == nil {
		t.Fatal("expected key() over a streamed document to be refused, got no error")
	}
	if !strings.Contains(err.Error(), "XTSE3430") {
		t.Fatalf("expected XTSE3430, got %v", err)
	}

	// The same root, once no longer marked live (the run has ended), must
	// build normally — the guard must not outlive the streamed run it guards.
	eng2 := &engine{sheet: &Stylesheet{}}
	if _, err := eng2.keyIndexOn("byv", root); err != nil {
		t.Fatalf("key() over a non-streamed root should not be refused: %v", err)
	}
}

// ---------------------------------------------------------------------------
// the memory-bound proof
// ---------------------------------------------------------------------------

// strbPeakHeap runs fn while sampling the heap, returning the largest
// HeapAlloc observed during it. Sampling (rather than a before/after reading)
// is what makes it a PEAK: the non-streaming path frees its tree eventually,
// so only an observation taken while the tree is live distinguishes the two.
//
// The collector is tightened for the duration. HeapAlloc counts garbage that
// has not been collected yet, and at the default GOGC the heap is allowed to
// double before a cycle runs — so an allocation-heavy streamed run would
// report a "peak" made mostly of dead records, and one that moved with
// whatever the rest of the test binary had left on the heap. At GOGC=20 the
// reading tracks the LIVE heap, which is the quantity the claim is about.
func strbPeakHeap(fn func()) uint64 {
	prevGC := debug.SetGCPercent(20)
	defer debug.SetGCPercent(prevGC)
	runtime.GC()
	debug.FreeOSMemory()
	var peak uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			default:
			}
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak {
				peak = m.HeapAlloc
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	fn()
	close(stop)
	<-done
	return peak
}

// TestStreamIsMemoryBounded is the actual deliverable of the streaming
// executor, and the one thing the W3C conformance suite cannot check: its
// largest streaming fixture is under 10MB and it has no generators, so a
// processor that merely classified correctly and then materialized everything
// would score identically on it.
//
// The property under test is not "uses little memory" but "uses memory
// INDEPENDENT OF INPUT SIZE", so it is measured as a slope: the same
// aggregating stylesheet (output: one number, so nothing but the tree can grow
// with the input) runs over two documents FORTY times apart in size, and the
// peak heap must barely move between them. A fixed ceiling alone would be a
// weaker claim — it could be met by a constant that merely happens to exceed
// this particular document — and a narrow size range would not separate real
// retention from the megabyte or so of GC slack a sampled HeapAlloc always
// carries: over a 40x range, retention shows up as a 40x-larger gap while the
// slack stays where it is.
//
// The negative control runs the identical computation with streamable="no". It
// is what makes the measurement mean anything: it shows the methodology can
// tell the two paths apart, rather than passing whatever it is given. It uses
// the MIDDLE document, because buffering the largest one would cost gigabytes
// to demonstrate something the middle one already demonstrates.
func TestStreamIsMemoryBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a large document")
	}
	dir := t.TempDir()
	_, smallSize := strbWriteDoc(t, dir, "small.xml", 25_000)
	_, bigSize := strbWriteDoc(t, dir, "big.xml", 1_000_000)
	_, ctlSize := strbWriteDoc(t, dir, "control.xml", 250_000)

	sheet := func(file, streamable string) string {
		return strbSheetHdr + `<xsl:source-document href="` + file + `" streamable="` + streamable + `">
      <xsl:iterate select="/data/rec">
        <xsl:param name="n" select="0"/>
        <xsl:next-iteration><xsl:with-param name="n" select="$n + number(v)"/></xsl:next-iteration>
        <xsl:on-completion>total=<xsl:value-of select="$n"/></xsl:on-completion>
      </xsl:iterate>
    </xsl:source-document>` + "\n" + strbSheetFtr
	}

	measure := func(file, streamable string) (string, uint64) {
		var out string
		var ran int64
		peak := strbPeakHeap(func() { out, ran = strbRun1(t, sheet(file, streamable), dir) })
		wantStream := streamable == "yes"
		if (ran == 1) != wantStream {
			t.Fatalf("%s streamable=%q: streaming path ran %d time(s); the measurement would be meaningless",
				file, streamable, ran)
		}
		return out, peak
	}

	smallOut, smallPeak := measure("small.xml", "yes")
	bigOut, bigPeak := measure("big.xml", "yes")
	ctlStreamOut, ctlStreamPeak := measure("control.xml", "yes")
	ctlOut, ctlPeak := measure("control.xml", "no")

	if ctlStreamOut != ctlOut {
		t.Fatalf("streamed result %q differs from materialized result %q", ctlStreamOut, ctlOut)
	}
	if smallOut == "" || smallOut == bigOut {
		t.Fatalf("the two documents should give different totals, got %q and %q", smallOut, bigOut)
	}

	mb := func(v uint64) float64 { return float64(v) / (1 << 20) }
	t.Logf("streamed  %6.1f MB input -> peak heap %5.1f MB", mb(uint64(smallSize)), mb(smallPeak))
	t.Logf("streamed  %6.1f MB input -> peak heap %5.1f MB", mb(uint64(ctlSize)), mb(ctlStreamPeak))
	t.Logf("streamed  %6.1f MB input -> peak heap %5.1f MB   (%.0fx the input, %.1f%% of it)",
		mb(uint64(bigSize)), mb(bigPeak), float64(bigSize)/float64(bigPeak), 100*float64(bigPeak)/float64(bigSize))
	t.Logf("BUFFERED  %6.1f MB input -> peak heap %5.1f MB   (%.0fx the streamed peak at the same size, %.0f%% of the input)",
		mb(uint64(ctlSize)), mb(ctlPeak), float64(ctlPeak)/float64(ctlStreamPeak), 100*float64(ctlPeak)/float64(ctlSize))

	// The slope. Forty times the document may cost at most 5% of the extra
	// bytes: the streamed run holds the ancestor spine, one record, the
	// reader's 64KB buffer, the compiled stylesheet and a one-number result,
	// none of which grows with the document, so the true marginal cost is ~0
	// and the allowance is pure slack for GC timing and sampling jitter.
	var growth uint64
	if bigPeak > smallPeak {
		growth = bigPeak - smallPeak
	}
	if limit := uint64(bigSize-smallSize) / 20; growth > limit {
		t.Errorf("streamed peak heap grew by %d bytes for %d bytes of extra input (limit %d): memory is tracking the document",
			growth, bigSize-smallSize, limit)
	}
	// An absolute ceiling as well, so the slope test cannot be satisfied by two
	// equally enormous numbers. 32MB is far above the model (a few MB) and far
	// below what even a fraction of the parsed tree costs.
	if bigPeak > 32<<20 {
		t.Errorf("streamed peak heap %d exceeds the 32MB ceiling", bigPeak)
	}
	// Negative control: without streaming the heap must track the document.
	// Without this the assertions above could "pass" because the sampler never
	// observed anything at all.
	if ctlPeak <= uint64(ctlSize) {
		t.Errorf("negative control did not scale with the document: peak heap %d for a %d-byte input — the measurement cannot distinguish the two paths",
			ctlPeak, ctlSize)
	}
	if ctlPeak < 10*ctlStreamPeak {
		t.Errorf("streamed peak %d is not meaningfully below the buffered peak %d at the same size", ctlStreamPeak, ctlPeak)
	}
}

var _ atomic.Int64

// ---------------------------------------------------------------------------
// the lexical scanners, which are the primary safety defence
// ---------------------------------------------------------------------------

func TestStrbExprSafe(t *testing.T) {
	safe := []string{
		`v`, `@id`, `child::v`, `.`, `self::rec`, `v[@k = '1']`,
		`descendant::x`, `descendant-or-self::node()`, `attribute::id`,
		`count(v)`, `sum(item/price)`, `position()`, `string-join(v, ',')`,
		`$g + number(v)`, `generate-id()`, `namespace::*`,
		// the forbidden names appear here only inside literals or as ordinary
		// element names, which must not trip the scanner
		`v[. = 'key(x)']`, `key/value`, `root/branch`, `id`, `mykey(v)`,
		`current()`, `a/b/c`, `(v, @id)`, `if (v) then 1 else 2`,
	}
	for _, s := range safe {
		if !strbExprSafe(s) {
			t.Errorf("strbExprSafe(%q) = false, want true", s)
		}
	}
	unsafe := []string{
		`/data/rec`, `//rec`, `..`, `../@id`, `parent::data`,
		`ancestor::data`, `ancestor-or-self::x`, `preceding-sibling::rec`,
		`following-sibling::rec`, `preceding::x`, `following::x`,
		`last()`, `position() = last()`, `count(/data/rec)`,
		`key('k', v)`, `fn:key('k', v)`, `id('x')`, `root()`, `doc('a.xml')`,
		`document('a.xml')`, `collection()`, `snapshot(.)`,
		`accumulator-before('a')`, `current-group()`, `unparsed-entity-uri('e')`,
		`v[. = "x"]/..`, `(1, /a)`, `element-with-id('x')`,
	}
	for _, s := range unsafe {
		if strbExprSafe(s) {
			t.Errorf("strbExprSafe(%q) = true, want false", s)
		}
	}
}

func TestStrbFocusFree(t *testing.T) {
	free := []string{`0`, `()`, `$g`, `$a + $b`, `'lit'`, `count($g)`, `1 to 3`}
	for _, s := range free {
		if !strbFocusFree(s) {
			t.Errorf("strbFocusFree(%q) = false, want true", s)
		}
	}
	bound := []string{
		`.`, `v`, `true`, `count(*)`, `count(v)`, `count(node())`, `@id`,
		`position()`, `string()`, `/data`, `0.5`, `current()`, `xs:integer(1)`,
	}
	for _, s := range bound {
		if strbFocusFree(s) {
			t.Errorf("strbFocusFree(%q) = true, want false", s)
		}
	}
}

func TestStrbParsePath(t *testing.T) {
	// A stylesheet element with no namespace declarations and no
	// xpath-default-namespace: every step lands in no namespace.
	el := &xmltree.Node{Kind: xmltree.KindElement}
	ok := map[string]int{`/data/rec`: 2, `data/rec`: 2, `rec`: 1, `*/rec`: 2, `./a/b/c`: 3, `child::a/b`: 2,
		// A boolean predicate on the LAST step filters records; the steps are
		// unchanged by it.
		`/data/rec[v='x']`: 2, `/data/rec[not(v)]`: 2, `rec[@id]`: 1}
	for src, want := range ok {
		got, valid := strbParsePath(src, el)
		if !valid {
			t.Errorf("strbParsePath(%q) rejected, want %d steps", src, want)
			continue
		}
		if len(got) != want {
			t.Errorf("strbParsePath(%q) gave %d steps, want %d", src, len(got), want)
		}
	}
	bad := []string{`//rec`, ``, `/`, `..`,
		`ancestor::x`, `/data/@id`, `a|b`, `doc('x')/rec`, `a/*/b/`,
		// positional predicates, a predicate on an inner step, and one that
		// reads outside the record
		`/data/rec[1]`, `/data/rec[position() lt 3]`, `/data[rec]/rec`,
		`/data/rec[../@id]`, `/data/rec[last()]`, `/data/rec[$n]`}
	for _, src := range bad {
		if _, valid := strbParsePath(src, el); valid {
			t.Errorf("strbParsePath(%q) accepted, want rejected", src)
		}
	}
}
