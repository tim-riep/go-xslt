package xslt

import (
	"errors"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// XSLT 3.0 xsl:iterate and its companion instructions xsl:next-iteration,
// xsl:break and xsl:on-completion.
//
// xsl:iterate iterates over the sequence given by @select. Leading xsl:param
// children declare iteration variables with initial values; the rest of the
// children form the body (an optional xsl:on-completion child runs once the
// sequence is exhausted). Within the body, xsl:next-iteration supplies the
// values of the iteration parameters for the next item, and xsl:break
// terminates the loop early (optionally producing a final result).
//
// next-iteration/break are implemented with sentinel error types returned from
// their exec methods; the iterate loop recovers them with errors.As so no
// shared engine state is required.

func init() {
	instrRegistry["iterate"] = itrCompileIterate
	instrRegistry["next-iteration"] = itrCompileNext
	instrRegistry["break"] = itrCompileBreak
	// xsl:on-completion is consumed by its parent xsl:iterate at compile time.
	// Should one appear standalone in a sequence constructor it is a no-op.
	instrRegistry["on-completion"] = func(c *compiler, el *xmltree.Node) (instruction, error) {
		return nil, nil
	}
}

// itrIterate is the compiled xsl:iterate instruction.
type itrIterate struct {
	sel          *xpath.Parsed
	params       []*VarDef     // leading xsl:param children (iteration variables)
	body         []instruction // body sequence constructor (minus params/on-completion)
	onCompletion []instruction // xsl:on-completion body (may be nil)
	el           *xmltree.Node
}

func (*itrIterate) instr() {}

// itrNextInstr is the compiled xsl:next-iteration instruction.
type itrNextInstr struct {
	params []*VarDef // xsl:with-param children
	el     *xmltree.Node
}

func (*itrNextInstr) instr() {}

// itrBreakInstr is the compiled xsl:break instruction.
type itrBreakInstr struct {
	sel  *xpath.Parsed // optional @select producing the final result
	body []instruction // optional body producing the final result
	el   *xmltree.Node
}

func (*itrBreakInstr) instr() {}

// itrNext is the sentinel signalling xsl:next-iteration. It carries the new
// values for the iteration parameters keyed by their clark name.
type itrNext struct {
	params map[string]xpath.Object
}

func (itrNext) Error() string { return "xsl:next-iteration" }

// itrBreak is the sentinel signalling xsl:break. The break's result, if any,
// has already been written to the output by the time this propagates.
type itrBreak struct{}

func (itrBreak) Error() string { return "xsl:break" }

func itrCompileIterate(c *compiler, el *xmltree.Node) (instruction, error) {
	sel, err := requireExpr(el, "select")
	if err != nil {
		return nil, err
	}
	it := &itrIterate{sel: sel, el: el}
	for _, ch := range elementChildren(el) {
		if ch.Name.Space == NS && ch.Name.Local == "param" {
			vd, err := c.compileVarDef(ch, true)
			if err != nil {
				return nil, err
			}
			it.params = append(it.params, vd)
			continue
		}
		if ch.Name.Space == NS && ch.Name.Local == "on-completion" {
			if v, ok := ch.AttrLocal("select"); ok {
				sel, err := parseXPathFor(ch, v)
				if err != nil {
					return nil, errAt(ch, "bad select %q: %v", v, err)
				}
				it.onCompletion = []instruction{&sequenceInstr{sel: sel, el: ch}}
				continue
			}
			ocBody, err := c.compileSequence(childNodesForBody(ch))
			if err != nil {
				return nil, err
			}
			it.onCompletion = ocBody
		}
	}
	if err := checkIterateTailPositions(itrBodyChildren(el), true); err != nil {
		return nil, err
	}
	body, err := c.compileSequence(itrBodyChildren(el))
	if err != nil {
		return nil, err
	}
	it.body = body
	return it, nil
}

// checkIterateTailPositions is XTSE3120: an xsl:break or xsl:next-iteration
// may appear only in TAIL POSITION within the sequence constructor forming
// the body of xsl:iterate — the last thing that executes along the path
// reaching it, not followed by any sibling instruction (error-3120a: an
// xsl:break nested in an xsl:if that is itself followed by more content is a
// STATIC error, regardless of what actually happens at runtime). nodes is
// one sequence-constructor SCOPE (the iterate body itself, or the body of an
// xsl:if/xsl:when/xsl:otherwise reached while recursing into it); parentTail
// is whether the CONTAINING construct (if any) is itself in tail position —
// a break at the tail of an xsl:if's own body is only truly tail position
// when the xsl:if itself is (error-3120a's exact shape: xsl:break is the
// tail of its enclosing xsl:if, but that xsl:if is followed by more content,
// so parentTail is false there and the break is correctly still rejected).
// This does not attempt full xsl:try/xsl:catch coverage — a narrower,
// well-scoped check for the shapes this engine's own tests exercise, not a
// claim of exhaustive XTSE3120 conformance.
func checkIterateTailPositions(nodes []*xmltree.Node, parentTail bool) error {
	var elems []*xmltree.Node
	for _, n := range nodes {
		if n.Kind != xmltree.KindElement {
			continue
		}
		// xsl:fallback is dead code here: xsl:iterate/xsl:choose/xsl:when/
		// xsl:otherwise are all CORE instructions this engine always
		// supports, so a sibling xsl:fallback's content never executes and
		// must not count as "something after" for tail-position purposes
		// (iterate-016/017: xsl:choose containing a tail xsl:next-iteration,
		// immediately followed by a sibling xsl:fallback, is still valid).
		if n.Name.Space == NS && n.Name.Local == "fallback" {
			continue
		}
		elems = append(elems, n)
	}
	for i, el := range elems {
		tail := parentTail && i == len(elems)-1
		if el.Name.Space != NS {
			continue
		}
		switch el.Name.Local {
		case "break", "next-iteration":
			if !tail {
				return errAt(el, "err:XTSE3120: xsl:%s is not in tail position within xsl:iterate", el.Name.Local)
			}
		case "if":
			if err := checkIterateTailPositions(childNodesForBody(el), tail); err != nil {
				return err
			}
		case "choose":
			for _, ch := range elementChildren(el) {
				if ch.Name.Space == NS && (ch.Name.Local == "when" || ch.Name.Local == "otherwise") {
					if err := checkIterateTailPositions(childNodesForBody(ch), tail); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// itrBodyChildren returns the body children of xsl:iterate: text + elements,
// excluding the leading xsl:param declarations and the xsl:on-completion child.
func itrBodyChildren(el *xmltree.Node) []*xmltree.Node {
	var out []*xmltree.Node
	for _, c := range el.Children {
		if c.Kind == xmltree.KindElement && c.Name.Space == NS {
			switch c.Name.Local {
			case "param", "on-completion":
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func itrCompileNext(c *compiler, el *xmltree.Node) (instruction, error) {
	n := &itrNextInstr{el: el}
	for _, ch := range elementChildren(el) {
		if ch.Name.Space == NS && ch.Name.Local == "with-param" {
			vd, err := c.compileVarDef(ch, false)
			if err != nil {
				return nil, err
			}
			n.params = append(n.params, vd)
		}
	}
	return n, nil
}

func itrCompileBreak(c *compiler, el *xmltree.Node) (instruction, error) {
	b := &itrBreakInstr{el: el}
	if sel, ok := el.AttrLocal("select"); ok {
		p, err := parseXPathFor(el, sel)
		if err != nil {
			return nil, errAt(el, "bad select %q: %v", sel, err)
		}
		b.sel = p
	}
	body, err := c.compileSequence(childNodesForBody(el))
	if err != nil {
		return nil, err
	}
	b.body = body
	return b, nil
}

func (n *itrIterate) exec(eng *engine, r rt, out *xmltree.Node) error {
	v, err := eng.eval(n.sel, n.el, r)
	if err != nil {
		return err
	}
	nodes := itrItemsToNodes(v)
	size := len(nodes)

	// Establish the initial iteration-parameter values once, evaluated in the
	// outer context (before any item is current).
	cur := make(map[string]xpath.Object, len(n.params))
	for _, p := range n.params {
		if p.requiredParam {
			// xsl:iterate's own xsl:param declares a purely local iteration
			// variable — there is no caller to supply a with-param override,
			// so an implicitly-required param (an @as with no select/content
			// whose declared type does not admit the empty-sequence default —
			// compileVarDef) can never be initialized: it is unconditionally
			// XTDE0700 (iterate-902).
			return errAt(p.el, "err:XTDE0700: required parameter $%s was not supplied", p.name.Local)
		}
		val, err := eng.evalVarDef(p, r)
		if err != nil {
			return err
		}
		cur[clark(p.name.Space, p.name.Local)] = val
	}

	for i, node := range nodes {
		ir := rt{node: node, pos: i + 1, size: size}
		eng.pushScope()
		for _, p := range n.params {
			eng.bindVar(p.name, cur[clark(p.name.Space, p.name.Local)])
		}
		err := eng.execSequence(n.body, ir, out)
		eng.popScope()
		if err == nil {
			continue
		}
		var nx itrNext
		if errors.As(err, &nx) {
			// Carry forward updated params; any param not mentioned keeps its
			// current value.
			for _, p := range n.params {
				key := clark(p.name.Space, p.name.Local)
				nv, ok := nx.params[key]
				if !ok {
					continue
				}
				// The value an xsl:with-param supplies is converted to the
				// iteration parameter's declared @as type under the function
				// conversion rules — in particular a result-tree fragment
				// supplied for as="xs:string" is ATOMIZED (iterate-042), not
				// carried forward as a node.
				if p.as != "" {
					if cv, cok := xpath.CoerceToDeclaredTypeCtx(p.as, nv, asTypeCtx(p.el)); cok {
						nv = cv
					}
				}
				cur[key] = nv
			}
			continue
		}
		var br itrBreak
		if errors.As(err, &br) {
			// Break's result was already written to out by its exec.
			return nil
		}
		return err
	}

	// Sequence exhausted normally: run xsl:on-completion (if present) with the
	// final iteration-parameter values in scope.
	if len(n.onCompletion) > 0 {
		eng.pushScope()
		for _, p := range n.params {
			eng.bindVar(p.name, cur[clark(p.name.Space, p.name.Local)])
		}
		// xsl:on-completion runs with NO context item — the sequence just
		// finished, it is not positioned "at" any particular member of it
		// (number-1004: a bare xsl:number inside xsl:on-completion, with no
		// context item to default to, is XTTE0990; "."/position()/last()
		// there are likewise XPDY0002) — unlike the loop body above, which
		// correctly runs WITH a focus (the current item, r unchanged).
		err := eng.execSequence(n.onCompletion, rt{noFocus: true}, out)
		eng.popScope()
		return err
	}
	return nil
}

func (n *itrNextInstr) exec(eng *engine, r rt, out *xmltree.Node) error {
	vals := make(map[string]xpath.Object, len(n.params))
	for _, p := range n.params {
		v, err := eng.evalVarDef(p, r)
		if err != nil {
			return err
		}
		vals[clark(p.name.Space, p.name.Local)] = v
	}
	return itrNext{params: vals}
}

func (n *itrBreakInstr) exec(eng *engine, r rt, out *xmltree.Node) error {
	if n.sel != nil {
		v, err := eng.eval(n.sel, n.el, r)
		if err != nil {
			return err
		}
		// §8.3: xsl:break/@select delivers the value of the expression as the
		// result of the whole xsl:iterate, exactly as xsl:sequence/@select
		// delivers one — which is why the sibling xsl:on-completion/@select is
		// COMPILED INTO a sequenceInstr (see itrCompile) rather than given its
		// own emitter. This branch used to flatten anything that was not a
		// node-set to its string value, so an xsl:iterate that broke early
		// with a map or array returned text: `as="map(*)"` on the enclosing
		// variable then raised XTTE0570 (si-iterate-037), while the identical
		// stylesheet running to completion worked. Share xsl:sequence's
		// emitter so both exits of the same instruction agree.
		if err := eng.checkSerializableItems(v, out); err != nil {
			return err
		}
		if err := emitSequenceValueRef(v, out); err != nil {
			return err
		}
	} else if len(n.body) > 0 {
		if err := eng.execSequence(n.body, r, out); err != nil {
			return err
		}
	}
	return itrBreak{}
}

// itrItemsToNodes converts an XPath sequence into nodes for iteration. Atomic
// items are modelled as synthetic text nodes so "." yields their value, mirroring
// xsl:for-each.
func itrItemsToNodes(v xpath.Object) []*xmltree.Node {
	items := xpath.Items(v)
	nodes := make([]*xmltree.Node, len(items))
	for i, it := range items {
		if nd, ok := it.(*xmltree.Node); ok {
			nodes[i] = nd
		} else {
			// Atomic: true, matching xsl:for-each's own identical wrapper
			// (execForEach) — without it, re-emitting "." elsewhere (e.g.
			// xsl:sequence select=".") silently loses the "adjacent atomics
			// join with a single space" treatment (simpleContentJoin's doc
			// comment; seqtor-007 is xsl:for-each's version of this same bug).
			nd := &xmltree.Node{Kind: xmltree.KindText, Atomic: true, Value: xpath.ToString(xpath.FromItems([]xpath.Item{it}))}
			if tag, ok := xpath.ItemAtomTypeTag(it); ok {
				nd.TypeAnno = tag
			}
			attachRealItem(nd, it)
			nodes[i] = nd
		}
	}
	return nodes
}
