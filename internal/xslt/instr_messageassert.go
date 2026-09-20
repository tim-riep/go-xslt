package xslt

import (
	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
	"strings"
)

// This file implements two related XSLT 3.0 instructions:
//
//   - xsl:message  — emit a (possibly terminating) message.
//   - xsl:assert   — fail with a message when a boolean test is false.
//
// Both produce their message content from a @select expression AND/OR a
// sequence-constructor body — unlike most select-or-content instructions,
// XSLT §12.2/§17.2 let the two combine: when @select is present its value is
// the initial content, and a non-empty sequence-constructor body supplies
// ADDITIONAL content appended after it (version-017: "message 1: " selected
// plus "A message" body content concatenate to one message). The combined
// content is recorded in eng.messages. All private helpers/types are
// prefixed with "msg".

func init() {
	instrRegistry["message"] = msgCompileMessage
	instrRegistry["assert"] = msgCompileAssert
}

// msgMessage is the compiled form of xsl:message.
type msgMessage struct {
	el        *xmltree.Node
	sel       *xpath.Parsed // @select (optional, combines with body — see above)
	body      []instruction // sequence constructor (additional content after @select, or the whole content when @select is absent)
	terminate *avt          // @terminate AVT, evaluates to "yes"/"no" (optional)
}

func (*msgMessage) instr() {}

// msgAssert is the compiled form of xsl:assert.
type msgAssert struct {
	el   *xmltree.Node
	test *xpath.Parsed // @test boolean (required)
	sel  *xpath.Parsed // @select (optional, combines with body — see above)
	body []instruction // sequence constructor (additional content after @select, or the whole content when @select is absent)
	// code is the EQName xsl:assert/@error-code names for the dynamic error a
	// failed assertion raises; "" means the default err:XTMM9001. It is
	// written in the "err:LOCAL"/"{uri}LOCAL" form the engine's error strings
	// and xsl:catch/@errors matching already use.
	code string
}

func (*msgAssert) instr() {}

func msgCompileMessage(c *compiler, el *xmltree.Node) (instruction, error) {
	m := &msgMessage{el: el}
	if s, ok := el.AttrLocal("select"); ok {
		p, err := parseXPathFor(el, s)
		if err != nil {
			return nil, errAt(el, "xsl:message bad select %q: %v", s, err)
		}
		m.sel = p
	}
	body, err := c.compileSequence(childNodesForBody(el))
	if err != nil {
		return nil, err
	}
	m.body = body
	if t, ok := el.AttrLocal("terminate"); ok {
		a, err := parseAVTFor(el, t)
		if err != nil {
			return nil, errAt(el, "xsl:message bad terminate %q: %v", t, err)
		}
		m.terminate = a
	}
	return m, nil
}

func msgCompileAssert(c *compiler, el *xmltree.Node) (instruction, error) {
	test, err := requireExpr(el, "test")
	if err != nil {
		return nil, err
	}
	a := &msgAssert{el: el, test: test}
	if ec, ok := el.AttrLocal("error-code"); ok && strings.TrimSpace(ec) != "" {
		qn := resolveQName(el, strings.TrimSpace(ec))
		a.code = "{" + qn.Space + "}" + qn.Local
	}
	if s, ok := el.AttrLocal("select"); ok {
		p, err := parseXPathFor(el, s)
		if err != nil {
			return nil, errAt(el, "xsl:assert bad select %q: %v", s, err)
		}
		a.sel = p
	}
	body, err := c.compileSequence(childNodesForBody(el))
	if err != nil {
		return nil, err
	}
	a.body = body
	return a, nil
}

// fragIsTextOnly reports whether frag's direct children are all text nodes —
// the common case for a plain-text message, where the OLD plain string-value
// behavior (no XML escaping/reparsing round-trip) must be preserved exactly.
func fragIsTextOnly(frag *xmltree.Node) bool {
	for _, c := range frag.Children {
		if c.Kind != xmltree.KindText {
			return false
		}
	}
	return true
}

// msgContent builds the message's content from a @select expression (if
// present) followed by a body sequence constructor (if present); see the file
// comment for how the two combine. A plain-text result (the overwhelming
// common case) is returned as-is via StringValue, matching prior behavior
// exactly; content that includes actual nodes (message-0202, version-017
// messages 2/3) is serialized to XML markup so assert-message's structural
// assertions can parse and query it like any other constructed content.
func msgContent(eng *engine, el *xmltree.Node, sel *xpath.Parsed, body []instruction, r rt) (string, error) {
	frag := &xmltree.Node{Kind: xmltree.KindDocument}
	if sel != nil {
		v, err := eng.eval(sel, el, r)
		if err != nil {
			return "", err
		}
		if err := emitSequenceValue(v, frag); err != nil {
			return "", err
		}
	}
	if len(body) > 0 {
		if err := eng.execSequence(body, r, frag); err != nil {
			return "", err
		}
	}
	if len(frag.Attrs) > 0 {
		// Constructing simple content (XSLT 3.0 §5.7.2): an attribute node in
		// the result sequence is atomized like any other item — Serialize
		// never looks at a document node's own Attrs, so fold them in as
		// trailing atomic text before either return path below
		// (namespace-2602-style edge case).
		for _, a := range frag.Attrs {
			appendAtomicText(frag, a.Value)
		}
		frag.Attrs = nil
	}
	if fragIsTextOnly(frag) {
		return frag.StringValue(), nil
	}
	return xmltree.Serialize(frag, xmltree.SerializeOptions{Method: "xml", OmitXMLDeclaration: true}), nil
}

// msgContentNodes builds the same content msgContent does but hands back the
// constructed fragment's items instead of their string form, for the
// $err:value an xsl:message terminate="yes" makes available to xsl:catch
// (message-0501).
func msgContentNodes(eng *engine, el *xmltree.Node, sel *xpath.Parsed, body []instruction, r rt) xpath.Object {
	frag := &xmltree.Node{Kind: xmltree.KindDocument, NoAtomicMerge: true, KeepDocItems: true}
	if sel != nil {
		if v, err := eng.eval(sel, el, r); err == nil {
			_ = emitSequenceValue(v, frag)
		}
	}
	if len(body) > 0 {
		if err := eng.execSequence(body, r, frag); err != nil {
			return xpath.Sequence{}
		}
	}
	return fragAsSequence(frag)
}

func (n *msgMessage) exec(eng *engine, r rt, out *xmltree.Node) error {
	msg, err := msgContent(eng, n.el, n.sel, n.body, r)
	if err != nil {
		// A dynamic error while building the message CONTENT does not fail
		// the transformation: xsl:message is a diagnostic aside, and XSLT 3.0
		// §12.2 leaves the effect of such an error implementation-defined
		// (message-0404 divides by zero inside the message and still expects
		// the result tree). The message is simply not emitted — unless
		// @terminate says to stop, which is handled below.
		if n.terminate == nil {
			return nil
		}
		msg = err.Error()
	}
	eng.messages = append(eng.messages, msg)
	if n.terminate != nil {
		t, err := eng.evalAVT(n.terminate, n.el, r)
		if err != nil {
			return err
		}
		switch strings.TrimSpace(t) {
		case "yes", "true", "1":
			// XSLT 3.0 §12.2: terminate="yes" raises the dynamic error
			// XTMM9000, which xsl:try can catch like any other — $err:code
			// must be err:XTMM9000, $err:description the message, and
			// $err:value the message CONTENT as a node sequence
			// (message-0501).
			return xpath.NewError(
				xmltree.Name{Space: tcErrNS, Local: "XTMM9000", Prefix: "err"},
				"xsl:message terminated: "+msg,
				msgContentNodes(eng, n.el, n.sel, n.body, r))
		case "no", "false", "0":
		default:
			// An AVT in a position where only a fixed set of values is
			// permitted must produce one of them (error-0030a: terminate
			// ="{$x}" with $x = 'bananas').
			return errAt(n.el, "err:XTDE0030: %q is not a permitted value for xsl:message/@terminate", t)
		}
	}
	return nil
}

func (n *msgAssert) exec(eng *engine, r rt, out *xmltree.Node) error {
	v, err := eng.eval(n.test, n.el, r)
	if err != nil {
		return err
	}
	if xpath.ToBool(v) {
		return nil
	}
	// Assertion failed: this is a dynamic error. Record the message and abort.
	msg, err := msgContent(eng, n.el, n.sel, n.body, r)
	if err != nil {
		return err
	}
	eng.messages = append(eng.messages, msg)
	// A failed assertion raises a DYNAMIC ERROR whose code is XTMM9001 (or
	// xsl:assert/@error-code), so xsl:try/xsl:catch errors="*:XTMM9001" can
	// catch it (assert-005/010).
	if n.code != "" {
		return errAt(n.el, "err:%s: xsl:assert failed: %s", n.code, msg)
	}
	return errAt(n.el, "err:XTMM9001: xsl:assert failed: %s", msg)
}
