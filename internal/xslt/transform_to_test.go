package xslt

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

// countingSink is deliberately NOT a bytes.Buffer: the point of the measurement
// is that the output never has to exist anywhere, and a buffer would put it
// straight back on the heap.
type countingSink struct{ n int64 }

func (c *countingSink) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

// strmCopySheet is a stylesheet that streams a large document in and copies
// essentially all of it back out, wrapped in one element: input and output are
// both O(document), which is the shape that makes an input-only streaming
// guarantee only half real. It is written on one line because the guaranteed-
// streamability classifier is (correctly) strict about what a streamed body may
// contain, and indentation is not what this test is about.
func strmCopySheet(file, streamable string) string {
	return `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="xml" omit-xml-declaration="yes"/>
  <xsl:template match="/"><out><xsl:source-document href="` + file + `" streamable="` + streamable + `"><xsl:copy-of select="/data/rec"/></xsl:source-document></out></xsl:template>
</xsl:stylesheet>`
}

// strmRunTo runs sheet through the writer-based path and reports the bytes
// written, whether the INPUT streamed, and whether the OUTPUT was drained.
func strmRunTo(t *testing.T, sheet, baseDir string, allowChunked bool) (int64, int64, int64) {
	t.Helper()
	ss, err := CompileFrom(sheet, baseDir)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	inBefore, outBefore := strbStreamedRuns.Load(), strmChunkedRuns.Load()
	var sink countingSink
	if _, err := ss.TransformFullTo(&sink, "<_/>", nil, baseDir, allowChunked); err != nil {
		t.Fatalf("transform: %v", err)
	}
	return sink.n, strbStreamedRuns.Load() - inBefore, strmChunkedRuns.Load() - outBefore
}

// TestChunkedOutputIsMemoryBounded is the deliverable of the output side, and
// the exact counterpart of TestStreamIsMemoryBounded: that one proves the INPUT
// does not have to be held, this one proves the OUTPUT does not either.
//
// The stylesheet is chosen so nothing else can hide the effect — it copies each
// record straight through, so the result is as big as the document and a run
// that retained it would be indistinguishable from one that retained the input.
// As in the input-side test the property is a SLOPE, not a ceiling: the same
// stylesheet runs over two documents forty times apart in size and the peak
// heap must barely move, which a merely-generous constant could not fake.
//
// Two negative controls, because there are two things that could be retaining
// memory and only one of them is this round's work:
//
//   - allowChunked=false: the input still streams and the output still goes
//     straight to the writer, but the result TREE is built in full first. This
//     is the measurement that isolates the output side.
//   - TransformFull: the ordinary string-returning path, which additionally
//     materializes the serialized result.
//
// Both must be far above the drained run at the same document size, or the
// methodology is not measuring anything.
func TestChunkedOutputIsMemoryBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a large document")
	}
	dir := t.TempDir()
	_, smallSize := strbWriteDoc(t, dir, "small.xml", 15_000)
	_, bigSize := strbWriteDoc(t, dir, "big.xml", 600_000)
	_, ctlSize := strbWriteDoc(t, dir, "control.xml", 150_000)

	measure := func(file string, allowChunked bool) (int64, uint64) {
		var written, inRuns, outRuns int64
		peak := strbPeakHeap(func() {
			written, inRuns, outRuns = strmRunTo(t, strmCopySheet(file, "yes"), dir, allowChunked)
		})
		if inRuns != 1 {
			t.Fatalf("%s: the input took the streaming path %d time(s); the measurement would be meaningless", file, inRuns)
		}
		if allowChunked && outRuns != 1 {
			t.Fatalf("%s: the output was NOT drained incrementally; the measurement would be meaningless", file)
		}
		if !allowChunked && outRuns != 0 {
			t.Fatalf("%s: the output was drained although the control asked for the buffered path", file)
		}
		return written, peak
	}

	smallOut, smallPeak := measure("small.xml", true)
	bigOut, bigPeak := measure("big.xml", true)
	ctlDrainOut, ctlDrainPeak := measure("control.xml", true)
	ctlTreeOut, ctlTreePeak := measure("control.xml", false)

	if ctlDrainOut != ctlTreeOut {
		t.Fatalf("drained run wrote %d bytes, buffered run wrote %d", ctlDrainOut, ctlTreeOut)
	}
	if smallOut == 0 || smallOut == bigOut {
		t.Fatalf("the two documents should give different output sizes, got %d and %d", smallOut, bigOut)
	}
	if bigOut < bigSize/2 {
		t.Fatalf("output %d is not comparable to the %d-byte input; the test is not exercising a large output", bigOut, bigSize)
	}

	// The string-returning path, for the record: it holds the tree AND the
	// serialized result.
	strPeak := strbPeakHeap(func() {
		ss, err := CompileFrom(strmCopySheet("control.xml", "yes"), dir)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		rr, err := ss.TransformFull("<_/>", nil, dir)
		if err != nil {
			t.Fatalf("transform: %v", err)
		}
		if int64(len(rr.Output)) != ctlTreeOut {
			t.Fatalf("string path produced %d bytes, writer path %d", len(rr.Output), ctlTreeOut)
		}
	})

	mb := func(v uint64) float64 { return float64(v) / (1 << 20) }
	t.Logf("drained   %6.1f MB in -> %6.1f MB out -> peak heap %5.1f MB", mb(uint64(smallSize)), mb(uint64(smallOut)), mb(smallPeak))
	t.Logf("drained   %6.1f MB in -> %6.1f MB out -> peak heap %5.1f MB", mb(uint64(ctlSize)), mb(uint64(ctlDrainOut)), mb(ctlDrainPeak))
	t.Logf("drained   %6.1f MB in -> %6.1f MB out -> peak heap %5.1f MB   (%.0fx the output, %.1f%% of it)",
		mb(uint64(bigSize)), mb(uint64(bigOut)), mb(bigPeak), float64(bigOut)/float64(bigPeak), 100*float64(bigPeak)/float64(bigOut))
	t.Logf("CONTROL result tree kept, written at the end: %6.1f MB out -> peak heap %5.1f MB   (%.0fx the drained peak at the same size)",
		mb(uint64(ctlTreeOut)), mb(ctlTreePeak), float64(ctlTreePeak)/float64(ctlDrainPeak))
	t.Logf("CONTROL string-returning TransformFull:       %6.1f MB out -> peak heap %5.1f MB   (%.0fx the drained peak at the same size)",
		mb(uint64(ctlTreeOut)), mb(strPeak), float64(strPeak)/float64(ctlDrainPeak))

	// The slope: forty times the document (and forty times the output) may cost
	// at most 10% of the extra bytes. The drained run holds the ancestor spine,
	// one record, one flush interval's worth of pending output, the reader's and
	// writer's fixed buffers and the compiled stylesheet — none of which grows,
	// so the true marginal cost is ~0 and the allowance is entirely slack. It is
	// looser than the input-side test's 5% for a real reason: this stylesheet
	// copies every record, so it CHURNS a result's worth of short-lived nodes,
	// and a sampled HeapAlloc therefore reads the collector's headroom rather
	// than anything retained — which is also why the middle measurement can come
	// out below the smallest one. Ten percent still leaves a tenfold margin
	// against genuine retention, which would show up at ~100%.
	var growth uint64
	if bigPeak > smallPeak {
		growth = bigPeak - smallPeak
	}
	if limit := uint64(bigSize-smallSize) / 10; growth > limit {
		t.Errorf("drained peak heap grew by %d bytes for %d bytes of extra input/output (limit %d): memory is tracking the document",
			growth, bigSize-smallSize, limit)
	}
	// An absolute ceiling as well, so the slope test cannot be satisfied by two
	// equally enormous numbers.
	if bigPeak > 32<<20 {
		t.Errorf("drained peak heap %d exceeds the 32MB ceiling", bigPeak)
	}
	if bigPeak > uint64(bigOut)/4 {
		t.Errorf("drained peak heap %d is not a small fraction of the %d bytes it wrote", bigPeak, bigOut)
	}
	// Negative controls: both must scale with the result the drained run refused
	// to keep. Without these the assertions above could "pass" on a sampler that
	// never observed anything.
	if ctlTreePeak <= uint64(ctlTreeOut) {
		t.Errorf("buffered-tree control did not scale with the output: peak heap %d for %d bytes of output", ctlTreePeak, ctlTreeOut)
	}
	if ctlTreePeak < 10*ctlDrainPeak {
		t.Errorf("drained peak %d is not meaningfully below the buffered-tree peak %d at the same size", ctlDrainPeak, ctlTreePeak)
	}
	if strPeak < ctlTreePeak {
		t.Errorf("the string-returning path (%d) should cost at least as much as keeping the tree alone (%d)", strPeak, ctlTreePeak)
	}
}

// TestChunkedOutputMatchesBuffered is the differential gate for the incremental
// serializer: for every shape it is allowed to drain, the bytes it writes must
// be exactly the bytes the ordinary Serialize path produces. It also asserts
// which path was taken, so a case that silently stopped being drained fails
// here rather than passing by agreeing with itself.
func TestChunkedOutputMatchesBuffered(t *testing.T) {
	src := func() string {
		var b strings.Builder
		b.WriteString(`<root xmlns:s="urn:s" a="1">`)
		for i := 0; i < 40; i++ {
			fmt.Fprintf(&b, `<rec id="r%d" s:k="%d"><v>%d</v><pad>a&amp;b&lt;c "q" %s</pad><s:x/></rec>`, i, i, i%7, strings.Repeat("z", 10))
		}
		b.WriteString(`<!--tail--><?pi go?>trailing text</root>`)
		return b.String()
	}()

	body := `<xsl:for-each select="root/rec"><xsl:copy-of select="."/></xsl:for-each>`
	cases := []struct{ name, out, tmpl string }{
		{"plain", `<xsl:output method="xml"/>`, `<out>` + body + `</out>`},
		{"no-decl", `<xsl:output method="xml" omit-xml-declaration="yes"/>`, `<out>` + body + `</out>`},
		{"bom", `<xsl:output method="xml" byte-order-mark="yes"/>`, `<out>` + body + `</out>`},
		{"nested-wrappers", `<xsl:output method="xml"/>`, `<a><b><c>` + body + `</c></b></a>`},
		{"top-level-sequence", `<xsl:output method="xml" omit-xml-declaration="yes"/>`, body},
		{"ns-on-wrapper", `<xsl:output method="xml"/>`,
			`<w:out xmlns:w="urn:w" xmlns:q="urn:q" q:z="1">` + body + `</w:out>`},
		{"cdata", `<xsl:output method="xml" cdata-section-elements="pad"/>`, `<out>` + body + `</out>`},
		{"charmap", `<xsl:output method="xml" use-character-maps="m"/><xsl:character-map name="m"><xsl:output-character character="z" string="[Z]"/></xsl:character-map>`,
			`<out>` + body + `</out>`},
		{"latin1", `<xsl:output method="xml" encoding="iso-8859-1"/>`, `<out><t>caf&#233;&#x1F600;</t>` + body + `</out>`},
		{"xml11", `<xsl:output method="xml" version="1.1" undeclare-prefixes="yes"/>`, `<out>` + body + `</out>`},
		{"nfc", `<xsl:output method="xml" normalization-form="NFC"/>`, `<out><t>a&#x30A;</t>` + body + `</out>`},
		{"doe", `<xsl:output method="xml"/>`,
			`<out><xsl:for-each select="root/rec"><r><xsl:value-of select="v" disable-output-escaping="yes"/></r></xsl:for-each></out>`},
		{"empty-elements", `<xsl:output method="xml"/>`,
			`<out><xsl:for-each select="root/rec"><e/><f a="1"/></xsl:for-each></out>`},
		{"comments-pis", `<xsl:output method="xml"/>`,
			`<out><xsl:for-each select="root/rec"><xsl:comment>c</xsl:comment><xsl:processing-instruction name="p">d</xsl:processing-instruction><x/></xsl:for-each></out>`},
		{"text", `<xsl:output method="text"/>`,
			`<xsl:for-each select="root/rec"><xsl:value-of select="@id"/>,<xsl:value-of select="v"/>&#10;</xsl:for-each>`},
		{"text-wrapped", `<xsl:output method="text"/>`, `<out>` + body + `</out>`},
		{"copy-namespaces-no", `<xsl:output method="xml"/>`,
			`<out xmlns:w="urn:w"><xsl:for-each select="root/rec"><xsl:copy-of select="." copy-namespaces="no"/></xsl:for-each></out>`},
		{"attribute-instr", `<xsl:output method="xml"/>`,
			`<out><xsl:for-each select="root/rec"><r><xsl:attribute name="n"><xsl:value-of select="@id"/></xsl:attribute><xsl:value-of select="v"/></r></xsl:for-each></out>`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sheet := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  ` + tc.out + `
  <xsl:template match="/">` + tc.tmpl + `</xsl:template>
</xsl:stylesheet>`
			ss, err := CompileFrom(sheet, "")
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			want, err := ss.TransformFull(src, nil, "")
			if err != nil {
				t.Fatalf("buffered transform: %v", err)
			}
			before := strmChunkedRuns.Load()
			var got strings.Builder
			if _, err := ss.TransformFullTo(&got, src, nil, "", true); err != nil {
				t.Fatalf("writer transform: %v", err)
			}
			if strmChunkedRuns.Load()-before != 1 {
				t.Fatalf("the output was not drained incrementally; this case proves nothing")
			}
			if got.String() != want.Output {
				t.Errorf("drained output differs from buffered\n want %q\n  got %q", want.Output, got.String())
			}
		})
	}
}

// TestChunkedOutputDeclines checks the opt-outs: an output definition whose
// serialization cannot be decided before the result exists must fall back to
// building the tree, and must still write the same bytes.
func TestChunkedOutputDeclines(t *testing.T) {
	src := `<root><rec><v>1</v></rec><rec><v>2</v></rec></root>`
	cases := []struct{ name, out string }{
		{"no-method", ``},
		{"indent", `<xsl:output method="xml" indent="yes"/>`},
		{"doctype", `<xsl:output method="xml" doctype-system="x.dtd"/>`},
		{"standalone", `<xsl:output method="xml" standalone="yes"/>`},
		{"html", `<xsl:output method="html"/>`},
		{"xhtml", `<xsl:output method="xhtml"/>`},
		{"item-separator", `<xsl:output method="xml" item-separator="|"/>`},
		{"text-charmap", `<xsl:output method="text" use-character-maps="m"/><xsl:character-map name="m"><xsl:output-character character="1" string="[1]"/></xsl:character-map>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sheet := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  ` + tc.out + `
  <xsl:template match="/"><out><xsl:for-each select="root/rec"><r><xsl:value-of select="v"/></r></xsl:for-each></out></xsl:template>
</xsl:stylesheet>`
			ss, err := CompileFrom(sheet, "")
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			want, err := ss.TransformFull(src, nil, "")
			if err != nil {
				t.Fatalf("buffered transform: %v", err)
			}
			before := strmChunkedRuns.Load()
			var got strings.Builder
			if _, err := ss.TransformFullTo(&got, src, nil, "", true); err != nil {
				t.Fatalf("writer transform: %v", err)
			}
			if n := strmChunkedRuns.Load() - before; n != 0 {
				t.Errorf("expected the buffered fall back, but the output was drained (%d)", n)
			}
			if got.String() != want.Output {
				t.Errorf("writer output differs from buffered\n want %q\n  got %q", want.Output, got.String())
			}
		})
	}
}

// TestChunkedOutputKeepsSerializationErrors: the one serialization check that
// reads the whole result (SERE0006, an XML-1.0-illegal control character) has to
// survive the tree being written and released underneath it — that is what the
// ChunkWriter's Validate hook is for.
func TestChunkedOutputKeepsSerializationErrors(t *testing.T) {
	// codepoints-to-string(1) itself now raises FOCH0001 for a C0 control
	// (correct: XML 1.0's Char production excludes it, the same rule
	// SERE0006 enforces below — so the illegal character can no longer be
	// smuggled in through ordinary XPath string construction). An externally
	// supplied top-level parameter is untouched by that check (it arrives as
	// plain xs:untypedAtomic text, exactly like an attribute value would),
	// so it is used here instead purely as a vehicle to get the illegal
	// character into the result tree — the test is about the ChunkWriter's
	// Validate hook, not about codepoints-to-string.
	sheet := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="xml"/>
  <xsl:param name="p"/>
  <xsl:template match="/"><out><a/><b><xsl:value-of select="$p"/></b></out></xsl:template>
</xsl:stylesheet>`
	ss, err := CompileFrom(sheet, "")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	params := map[string]string{"p": "x\x01y"}
	if _, err := ss.TransformFull("<_/>", params, ""); err == nil || !strings.Contains(err.Error(), "SERE0006") {
		t.Fatalf("buffered path: err = %v, want SERE0006", err)
	}
	var got strings.Builder
	if _, err := ss.TransformFullTo(&got, "<_/>", params, "", true); err == nil || !strings.Contains(err.Error(), "SERE0006") {
		t.Errorf("drained path: err = %v, want SERE0006", err)
	}
}

var _ io.Writer = (*countingSink)(nil)
