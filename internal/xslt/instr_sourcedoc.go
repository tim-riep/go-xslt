package xslt

import (
	"fmt"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// This file implements xsl:source-document (XSLT 3.0).
//
// xsl:source-document reads the document identified by @href and evaluates its
// sequence-constructor body with that document node as the context item.
//
// @streamable="yes" takes the bounded-memory path in stream_exec.go when this
// executor can prove the body safe to run against an incrementally-read tree;
// otherwise the document is built in full, which a processor is always
// permitted to do for a streamable source-document. The fallback is total: it
// is taken before any output is produced, so a body that cannot stream behaves
// exactly as it did before streaming existed.
//
// All private helpers/types in this file are prefixed with "sd".

func init() {
	instrRegistry["source-document"] = sdCompile
}

type sdSourceDocument struct {
	el   *xmltree.Node
	href *avt
	body []instruction
	// val is the validation/type request — see litElement.val. nil unless one
	// was written EXPLICITLY here: §24.4 keeps the ambient
	// [xsl:]default-validation away from this instruction (vkSourceDoc).
	val *valRequest
}

func (*sdSourceDocument) instr() {}

// sdNeedsWholeDocument reports whether req asks for a validation episode that
// cannot be served by the incremental reader, so that this instruction must
// retrieve its document in full even though it was declared streamable.
//
// WHY THE BOUNDED-MEMORY GUARANTEE IS GIVEN UP HERE, DELIBERATELY. §19.10 tells
// a streaming processor that a guaranteed-streamable construct "must be
// processed using streaming", but it tells the non-streaming path something
// this engine cannot honour both ways at once: "the processor must evaluate the
// construct delivering the same results as if execution used streaming". A
// strict or lax episode (and a [xsl:]type request, which is an episode against
// one named type) assesses the document against the schema and annotates it —
// and internal/xsd assesses a complex type's content model over a node's whole
// Children slice, which by construction does not exist until the last record of
// that element has been read. Streaming the document anyway is not a weaker
// guarantee, it is a WRONG ANSWER: the body would see untyped nodes, atomize
// them as xs:untypedAtomic, and an invalid document would be reported valid —
// the suite ships bad-loans.xml precisely to pin that second point. Correct
// results with unbounded memory beat bounded memory with wrong results, so this
// one combination buffers and says so.
//
// The static half of the analysis is unaffected by this choice, which is why it
// is safe to make at run time: strmCheckStreamableSourceDocs decides XTSE3430
// from the BODY alone and never promises that streaming will actually be
// attempted, and strbTryStream already reports "not handled" — before any
// output is produced — for a resolver, document or body it cannot serve. This
// adds one more reason to that existing, total fallback rather than a new kind
// of decision.
//
// Narrow on purpose: "preserve" and "strip" are no-ops on a freshly retrieved
// document (applyPreserve has no vkSourceDoc case, and there are no annotations
// for stripAnnotations to remove), so they keep streaming exactly as before.
//
// As of this writing the guard changes no observable behaviour, and that was
// checked rather than assumed: strbTryStream was instrumented to announce every
// run it takes, and no stylesheet in the W3C suite — nor a hand-written one
// built for the purpose over the suite's own bad-loans.xml — reached it with a
// validation episode pending, because strbPlanFor independently declines those
// bodies today. The guard is kept anyway. It costs nothing, and it makes the
// invariant explicit at the one place that can break it: without it, the next
// widening of strbPlanFor would silently turn "validate then process" into
// "process unvalidated", which is a wrong ANSWER rather than a failure, and so
// would not announce itself.
func sdNeedsWholeDocument(req *valRequest) bool {
	if req == nil {
		return false
	}
	if req.typ != nil {
		return true
	}
	return req.mode == valStrict || req.mode == valLax
}

func sdCompile(c *compiler, el *xmltree.Node) (instruction, error) {
	n := &sdSourceDocument{el: el}
	h, ok := el.AttrLocal("href")
	if !ok {
		return nil, errAt(el, "xsl:source-document requires an href")
	}
	a, err := parseAVTFor(el, h)
	if err != nil {
		return nil, errAt(el, "xsl:source-document bad href %q: %v", h, err)
	}
	n.href = a
	body, err := c.compileSequence(childNodesForBody(el))
	if err != nil {
		return nil, err
	}
	n.body = body
	val, err := compileValidation(el, vkSourceDoc)
	if err != nil {
		return nil, err
	}
	n.val = val
	return n, nil
}

func (n *sdSourceDocument) exec(eng *engine, r rt, out *xmltree.Node) error {
	href, err := eng.evalAVT(n.href, n.el, r)
	if err != nil {
		return err
	}
	// @href is a URI REFERENCE, resolved against this element's own base URI
	// (its xml:base, if any, else the module it's written in) — not against
	// whatever directory the run started from (source-document/stream-004,
	// non-stream-004: xml:base="../../.." on xsl:source-document itself).
	if abs, aerr := xpath.ResolveURIRef(href, xpath.NodeBaseURI(n.el, "")); aerr == nil && abs != "" {
		href = abs
	}
	if eng.resolver == nil {
		return fmt.Errorf("err:FODC0002: cannot retrieve document %q", href)
	}
	streamable := false
	if v, ok := n.el.AttrLocal("streamable"); ok && isXSLTTrue(strings.TrimSpace(v)) {
		streamable = true
	}
	if streamable {
		// The DECLARATION is what §14.2.1/§14.2.2 turn on, so the flag covers
		// the buffered fallback below as well as the bounded-memory path.
		saved := eng.inStreamable
		eng.inStreamable = true
		defer func() { eng.inStreamable = saved }()
		// A validation EPISODE has to be skipped past here: the bounded-memory
		// path returns without ever reaching eng.applyValidation below, so
		// streaming a document the stylesheet asked to have validated would
		// hand the body untyped nodes and silently produce wrong answers (a
		// value-of over an xs:decimal element yields "400000", the untyped
		// same element "400000.0"). See sdNeedsWholeDocument.
		if !sdNeedsWholeDocument(n.val) {
			if handled, err := strbTryStream(eng, n, href, out); handled {
				return err
			}
		}
	}
	doc, ok := eng.resolver.ResolveDoc(href)
	if !ok {
		return fmt.Errorf("err:FODC0002: cannot retrieve document %q", href)
	}
	// §24.4: the retrieved document is validated as a document node before it
	// is processed. Applied ONLY when the stylesheet asked explicitly (n.val
	// is nil otherwise), which also keeps the annotation out of the resolver's
	// shared document cache for every stylesheet that did not request it.
	if err := eng.applySourceDocValidation(n.val, doc); err != nil {
		return err
	}
	// §4.3: input-type-annotations="strip" applies to every SOURCE TREE, and
	// §4.4's own definition of that term names "documents read using the
	// xsl:stream instruction" — the 2015 draft's name for what the REC calls
	// xsl:source-document — right alongside document()/fn:doc/fn:collection
	// results, whose stripping validateSourceDocument already performs via the
	// resolver. The same reasoning as there applies to the ORDER: validation
	// still RUNS (an invalid document is still reported, just above), and only
	// the annotations it earned are discarded, keeping "the input is checked"
	// and "the stylesheet does not rely on the types" separate.
	//
	// Scoped to an explicit validation request, since a document retrieved
	// with none carries no annotations to strip — and deliberately NOT hooked
	// into the generic applyValidation, which also serves nodes the stylesheet
	// itself CONSTRUCTS (xsl:copy-of, xsl:element, …); those are not source
	// trees and §4.3 must not reach them.
	if n.val != nil && eng.sheet != nil && eng.sheet.stripInputTypes {
		stripAnnotations(doc)
	}
	defer eng.restrictAccumulators(doc, n.el)()
	// A FRAGMENT IDENTIFIER selects a node WITHIN the retrieved document as the
	// context item, rather than the document node (docbook-004 points at
	// NEWS.xml#V1.79.1_Tools and processes just that section). The resolver
	// strips the fragment when locating the file, so it has to be applied here.
	ctxNode := doc
	if i := strings.IndexByte(href, '#'); i >= 0 {
		if frag := href[i+1:]; frag != "" {
			el := elementByID(doc, frag)
			if el == nil {
				return fmt.Errorf("err:FODC0002: document %q has no element with ID %q", href[:i], frag)
			}
			ctxNode = el
		}
	}
	// Reaching here means the document was built in full after all. It still
	// records that the stylesheet ASKED for streaming, so the rules conditional
	// on it apply either way (XTDE3362).
	if streamable {
		if eng.streamedRoots == nil {
			eng.streamedRoots = map[*xmltree.Node]bool{}
		}
		eng.streamedRoots[doc] = true
		defer delete(eng.streamedRoots, doc)
	}
	eng.pushScope()
	defer eng.popScope()
	return eng.execSequence(n.body, rt{node: ctxNode, pos: 1, size: 1}, out)
}
