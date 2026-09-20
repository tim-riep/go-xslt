package xslt

import (
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// Bounded-memory OUTPUT for a transformation, the counterpart of the streamed
// INPUT in stream_exec.go.
//
// Two levels, and which one a run gets is decided entirely here:
//
//  1. Always: the serialized result goes straight to the caller's io.Writer
//     instead of being accumulated into one string. That removes a full copy of
//     the output (plus the Builder's growth spikes) from peak memory, but the
//     result TREE is still built in full first, so peak is still O(result).
//
//  2. When the output definition permits it (chunkedOptions), the result tree
//     is additionally DRAINED while it is built: xmltree.ChunkWriter writes out
//     and detaches every subtree that can no longer change. Combined with a
//     streamed xsl:source-document, whose records are dropped as they are
//     processed, peak memory then depends on neither the input nor the output
//     size — only on the largest record and the deepest open element.
//
// Level 2 is deliberately conservative: anything whose serialization needs the
// whole result present keeps level 1, which is byte-for-byte the ordinary path.

// strmChunkedRuns counts the runs whose principal result was actually drained
// while it was built. The fall back to building the tree in full is invisible
// in the output — which is exactly why a test asserting bounded memory needs a
// way to tell that it measured the path it meant to (the same reason
// strbStreamedRuns exists for the input side).
var strmChunkedRuns atomic.Int64

// TransformFullTo runs the stylesheet and writes the principal result to w,
// returning the run's messages and secondary outputs with an empty Output (the
// content went to w). allowChunked is the CALLER's half of the eligibility
// test: the caller must have established that the stylesheet cannot redirect
// its principal output mid-run (an empty-href xsl:result-document rewrites the
// output definition after the fact, which is impossible to honour once bytes
// are out). This function applies the other half, over the output definition.
func (ss *Stylesheet) TransformFullTo(w io.Writer, srcXML string, params map[string]string, baseDir string, allowChunked bool) (*RunResult, error) {
	tgt := &outTarget{w: w}
	if allowChunked {
		if so, ok := ss.chunkedOptions(); ok {
			cw := xmltree.NewChunkWriter(w, so)
			// checkSerializationErrors' SERE0006 scan cannot run at the end any
			// more — by then the tree has been written and released — so the
			// same check travels with the writer, node by node. Its condition is
			// the same one there: the xml method below version 1.1.
			if so.Method == "xml" && strings.TrimSpace(so.XMLVersion) != "1.1" {
				cw.Validate = func(n *xmltree.Node) error {
					if c, bad := firstIllegalXML10Char(n); bad {
						return fmt.Errorf("err:SERE0006: character #x%X cannot be serialized as XML 1.0", c)
					}
					return nil
				}
			}
			tgt.sink = cw
			strmChunkedRuns.Add(1)
		}
	}
	eng, root, err := ss.transformInto(srcXML, params, baseDir, tgt)
	if err != nil {
		return nil, err
	}
	return ss.finishRun(eng, root)
}

// chunkedOptions returns the serialization options for an incrementally
// drained run, and whether this stylesheet's output definition qualifies at
// all.
//
// The whole point is that these options must be known BEFORE the run: the
// prologue is written as soon as the first content is flushed. So the method
// has to be stated explicitly (an auto-detected one is read off the finished
// result tree), nothing may still override it (a named xsl:output reached
// through xsl:result-document, ruled out by the caller), and the serialization
// itself must not depend on content that has not arrived yet — which is what
// xmltree.CanChunk decides.
func (ss *Stylesheet) chunkedOptions() (xmltree.SerializeOptions, bool) {
	cfg := ss.output
	if cfg.Method == "" || outputIsRaw(cfg) {
		return xmltree.SerializeOptions{}, false
	}
	// include-content-type is false for every method CanChunk accepts (it is an
	// html/xhtml parameter), so the flag it needs from finishRun is settled.
	so := ss.serializeOptions(cfg, cfg.Method, false)
	if !xmltree.CanChunk(so) {
		return xmltree.SerializeOptions{}, false
	}
	return so, true
}
