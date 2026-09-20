package xslt

import (
	"errors"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// tcErrNS is the namespace for the XSLT/XQuery error variables (err:code etc.).
const tcErrNS = "http://www.w3.org/2005/xqt-errors"

// tryCatch implements xsl:try with one or more xsl:catch children. The try body
// (either @select or the element body, excluding xsl:catch children) is run into
// a temporary fragment; if it fails, a matching xsl:catch is selected and its
// body is run with the err: error variables bound in scope.
type tryCatch struct {
	sel     *xpath.Parsed // @select (optional; else use body)
	body    []instruction // try body (children minus xsl:catch)
	catches []*tcCatch
	el      *xmltree.Node
	// rollback reflects @rollback-output (default yes). With "no" the
	// processor is allowed to refuse recovery once output has been written —
	// XTDE3530 (try-034), while a failure before any output is still
	// recoverable (try-033).
	rollback bool
}

// tcCatch is a compiled xsl:catch child.
type tcCatch struct {
	codes []xmltree.Name // resolved error codes from @errors; empty means '*'/all
	all   bool           // true if @errors absent or contains '*'
	sel   *xpath.Parsed  // @select — the catch value, used INSTEAD of the body
	body  []instruction
	el    *xmltree.Node
}

func (*tryCatch) instr() {}

// tcNoop is the compiled form of a standalone xsl:catch (handled by xsl:try).
type tcNoop struct{}

func (*tcNoop) instr() {}

func (*tcNoop) exec(eng *engine, r rt, out *xmltree.Node) error { return nil }

func init() {
	instrRegistry["try"] = func(c *compiler, el *xmltree.Node) (instruction, error) {
		t := &tryCatch{el: el, rollback: true}
		if v, ok := el.AttrLocal("rollback-output"); ok {
			t.rollback = isXSLTTrue(strings.TrimSpace(v))
		}
		if sel, ok := el.AttrLocal("select"); ok {
			p, err := parseXPathFor(el, sel)
			if err != nil {
				return nil, errAt(el, "bad select %q: %v", sel, err)
			}
			t.sel = p
		}
		// Body = children that are not xsl:catch. With @select present the body
		// must be empty, but we tolerate ignoring it either way.
		var bodyNodes []*xmltree.Node
		for _, ch := range el.Children {
			if ch.Kind == xmltree.KindElement && ch.Name.Space == NS && ch.Name.Local == "catch" {
				cat, err := tcCompileCatch(c, ch)
				if err != nil {
					return nil, err
				}
				t.catches = append(t.catches, cat)
				continue
			}
			bodyNodes = append(bodyNodes, ch)
		}
		if t.sel == nil {
			body, err := c.compileSequence(bodyNodes)
			if err != nil {
				return nil, err
			}
			t.body = body
		}
		if len(t.catches) == 0 {
			return nil, errAt(el, "xsl:try requires at least one xsl:catch child")
		}
		return t, nil
	}
	// xsl:catch on its own is a no-op; xsl:try handles its children.
	instrRegistry["catch"] = func(c *compiler, el *xmltree.Node) (instruction, error) {
		return &tcNoop{}, nil
	}
}

func tcCompileCatch(c *compiler, el *xmltree.Node) (*tcCatch, error) {
	cat := &tcCatch{el: el}
	if errs, ok := el.AttrLocal("errors"); ok {
		for _, tok := range strings.Fields(errs) {
			if tok == "*" {
				cat.all = true
				continue
			}
			cat.codes = append(cat.codes, tcParseNameTest(el, tok))
		}
		if len(cat.codes) == 0 && !cat.all {
			cat.all = true
		}
	} else {
		cat.all = true
	}
	if sel, ok := el.AttrLocal("select"); ok {
		p, err := parseXPathFor(el, sel)
		if err != nil {
			return nil, errAt(el, "bad xsl:catch select %q: %v", sel, err)
		}
		cat.sel = p
		return cat, nil
	}
	body, err := c.compileSequence(childNodesForBody(el))
	if err != nil {
		return nil, err
	}
	cat.body = body
	return cat, nil
}

// tcParseNameTest resolves one token of xsl:catch/@errors, which is a
// NameTest: "err:FOAR0001", "Q{uri}local", "prefix:*", "*:local" or "*".
// An UNPREFIXED name is in NO namespace — xpath-default-namespace does not
// apply to it (try-036) — so it never matches a standard error code.
// Space/Local "*" stand for "any".
func tcParseNameTest(el *xmltree.Node, tok string) xmltree.Name {
	if strings.HasPrefix(tok, "*:") {
		return xmltree.Name{Space: "*", Local: tok[2:]}
	}
	if strings.HasSuffix(tok, ":*") {
		uri, _ := el.LookupPrefix(tok[:len(tok)-2])
		return xmltree.Name{Space: uri, Local: "*"}
	}
	if uri, local, ok := bracedEQName(tok); ok {
		if local == "*" {
			return xmltree.Name{Space: uri, Local: "*"}
		}
		return xmltree.Name{Space: uri, Local: local}
	}
	return resolveQName(el, tok)
}

func (t *tryCatch) exec(eng *engine, r rt, out *xmltree.Node) error {
	// Run the try part into a temporary fragment so partial output is discarded
	// on failure. Variables declared in the try body are NOT in scope in
	// xsl:catch (try-011/039/040), so the body gets its own scope.
	// The scratch fragment must carry the DESTINATION's sequence flags: a try
	// body feeding an @as-typed collector is building a discrete sequence, not
	// element content, so adjacent atomic items must not be space-merged
	// (NoAtomicMerge) and a document node must survive as one item rather than
	// flattening into its children (KeepDocItems). Dropping them here made
	// <xsl:variable as="document-node()?"><xsl:try>... yield elements
	// (catalog-006), space-joined strings (catalog-007), and an xsl:function
	// whose whole body is an xsl:try return xs:untypedAtomic "false" — whose
	// effective boolean value is TRUE — instead of the xs:boolean false() the
	// body produced (higher-order-functions-067). With an ordinary element/RTF
	// destination both flags are false and behaviour is unchanged.
	frag := &xmltree.Node{Kind: xmltree.KindDocument,
		NoAtomicMerge: out.NoAtomicMerge, KeepDocItems: out.KeepDocItems}
	var tryErr error
	savedErrEl := eng.errEl
	eng.errEl = nil
	if t.sel != nil {
		obj, err := eng.eval(t.sel, t.el, r)
		if err != nil {
			tryErr = err
		} else {
			// emitSequenceValue (the same path xsl:catch below already uses)
			// preserves each item's type annotation and carried identity;
			// tcAppendObject stringified everything.
			emitSequenceValue(obj, frag)
		}
	} else {
		eng.pushScope()
		tryErr = eng.execSequence(t.body, r, frag)
		eng.popScope()
	}

	if tryErr == nil || isControlSignal(tryErr) {
		// A control-flow SENTINEL (xsl:break / xsl:next-iteration, which
		// propagate as error values) is not a failure: the try body completed
		// normally up to that point, so its output must be flushed and the
		// signal passed on untouched — never offered to xsl:catch, and never
		// subject to the rollback-output check (iterate-035 builds <pos> and
		// then breaks, and expects the <pos> to survive).
		eng.errEl = savedErrEl
		for _, ch := range frag.Children {
			out.Append(ch)
		}
		return tryErr
	}
	errEl := eng.errEl
	eng.errEl = savedErrEl

	// rollback-output="no" with output already written: recovery is refused.
	if !t.rollback && len(frag.Children) > 0 {
		return errAt(t.el, "err:XTDE3530: xsl:try rollback-output=\"no\" cannot recover after output has been written: %v", tryErr)
	}

	code, value := tcErrorParts(tryErr)
	cat := t.matchCatch(code)
	if cat == nil {
		// No matching catch: propagate the original error.
		return tryErr
	}

	eng.pushScope()
	defer eng.popScope()
	eng.bindVar(xmltree.Name{Space: tcErrNS, Local: "code", Prefix: "err"}, xpath.NewQName(code))
	eng.bindVar(xmltree.Name{Space: tcErrNS, Local: "description", Prefix: "err"}, xpath.NewString(tryErr.Error()))
	eng.bindVar(xmltree.Name{Space: tcErrNS, Local: "value", Prefix: "err"}, value)
	module, line, col := tcErrorLocation(tryErr, errEl, t.el)
	eng.bindVar(xmltree.Name{Space: tcErrNS, Local: "module", Prefix: "err"}, module)
	eng.bindVar(xmltree.Name{Space: tcErrNS, Local: "line-number", Prefix: "err"}, line)
	eng.bindVar(xmltree.Name{Space: tcErrNS, Local: "column-number", Prefix: "err"}, col)
	if cat.sel != nil {
		v, err := eng.eval(cat.sel, cat.el, r)
		if err != nil {
			return err
		}
		return emitSequenceValue(v, out)
	}
	return eng.execSequence(cat.body, r, out)
}

// tcErrorParts splits a dynamic error into the expanded error QName and the
// error object ($err:value). A structured *xpath.Error carries both directly;
// any other error's code is recovered from its "err:CODE:" message prefix and
// is by definition in the standard error namespace.
func tcErrorParts(err error) (xmltree.Name, xpath.Object) {
	var xe *xpath.Error
	if errors.As(err, &xe) {
		v := xe.Value
		if v == nil {
			v = xpath.Sequence{}
		}
		return xe.Code, v
	}
	code := tcExtractCode(err.Error())
	if code == "" {
		code = "FOER0000"
	}
	return xmltree.Name{Space: tcErrNS, Local: code, Prefix: "err"}, xpath.Sequence{}
}

// tcErrorLocation reports $err:module / $err:line-number / $err:column-number.
// errEl is the stylesheet element whose expression evaluation failed (recorded
// by eng.eval); a *CompileError's own Line/Col wins when it has them. The
// module is the static base URI of whichever element is used. All three are
// the empty sequence when nothing is known, which is what the spec allows and
// what the tests accept.
func tcErrorLocation(err error, errEl, tryEl *xmltree.Node) (xpath.Object, xpath.Object, xpath.Object) {
	line, col := 0, 0
	if ce, ok := err.(*CompileError); ok {
		line, col = ce.Line, ce.Col
	}
	el := errEl
	if el == nil {
		el = tryEl
	}
	if line == 0 && el != nil {
		line, col = el.Line, el.Col
	}
	var module xpath.Object = xpath.Sequence{}
	if el != nil {
		if base := xpath.NodeBaseURI(el, ""); base != "" {
			module = xpath.NewString(base)
		}
	}
	var lineO, colO xpath.Object = xpath.Sequence{}, xpath.Sequence{}
	if line > 0 {
		lineO = xpath.NewInteger(int64(line))
	}
	if col > 0 {
		colO = xpath.NewInteger(int64(col))
	}
	return module, lineO, colO
}

// matchCatch returns the first xsl:catch whose @errors match the given error
// code; a catch with '*' or no @errors matches any error.
func (t *tryCatch) matchCatch(code xmltree.Name) *tcCatch {
	for _, cat := range t.catches {
		if cat.all {
			return cat
		}
		for _, c := range cat.codes {
			if (c.Space == "*" || c.Space == code.Space) && (c.Local == "*" || c.Local == code.Local) {
				return cat
			}
		}
	}
	return nil
}

// tcExtractCode extracts an error code from a message of the form
// "...err:CODE:...". It returns the CODE token, or "" if none is present.
func tcExtractCode(msg string) string {
	const marker = "err:"
	i := strings.Index(msg, marker)
	if i < 0 {
		return ""
	}
	rest := msg[i+len(marker):]
	j := strings.IndexByte(rest, ':')
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// isControlSignal reports whether err is one of the XSLT control-flow
// sentinels that travel as error values (xsl:break, xsl:next-iteration) rather
// than a real dynamic error.
func isControlSignal(err error) bool {
	switch err.(type) {
	case itrBreak, *itrBreak, itrNext, *itrNext:
		return true
	}
	return false
}
