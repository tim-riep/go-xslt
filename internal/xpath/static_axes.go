package xpath

import "fmt"

// This file implements a narrow, purely STRUCTURAL static-emptiness check for
// axis steps (XPath 3.1's optional "Static Typing Feature" raises err:XPST0005
// when an expression's static type is provably empty-sequence() — QT3's
// prod-AxisStep.static-typing test set, ST-Axes001..015). We do not implement
// general XPath static typing (that stays out of scope — see CLAUDE.md's
// schema-awareness notes on static_typing/staticTyping), only the much
// narrower slice of it these tests need: an axis step can sometimes be proven
// to select nothing at all using ONLY the axis+node-test SHAPE of the
// preceding step(s) — no schema, no dynamic data. E.g. attribute:: can never
// produce anything from a document node (only elements carry attributes);
// self::foo can never match a node that a previous step already pinned to a
// different element name; descendant-or-self:: from an attribute or text
// node (leaf kinds with no children, and whose own kind can never satisfy an
// element-principal-kind test) is empty both ways at once.
//
// This is deliberately conservative: every axis NOT explicitly reasoned about
// below (ancestor, ancestor-or-self, following, preceding, following-sibling,
// preceding-sibling, namespace) and every node-test shape more complex than a
// plain unprefixed name (a wildcard, a prefixed/braced name, a schema-aware
// type test) degrades to "unknown — never claim emptiness". The goal is zero
// false positives against the wider corpus, even at the cost of under-firing
// on cases a fuller static-type system could also catch.

// stepNodeKind is a coarse node-kind classification used only for this
// static-emptiness reasoning (distinct from testKind, which classifies a
// NodeTest's own grammar shape).
type stepNodeKind int

const (
	skAny stepNodeKind = iota // unknown / could be anything — never proves emptiness
	skDocument
	skElement
	skAttribute
	skText
	skComment
	skPI
	skNamespace
)

// stepShape is what we statically know about the node(s) a step could
// produce: a node kind, and — only when unambiguous (a plain unprefixed,
// non-wildcard name) — its exact name. parentless additionally marks a
// value that is KNOWN, independent of its kind, to never have a parent —
// fn:parse-xml-fragment's own result, a freshly parsed, detached fragment
// (parse-xml-fragment-022-st): unlike the document-node case (kind alone
// already implies parentless), a fragment can be an element, text, etc., so
// this needs its own flag rather than piggybacking on kind.
type stepShape struct {
	kind       stepNodeKind
	name       string
	nameKnown  bool
	parentless bool
}

// principalKind is the XPath "principal node kind" for name-test matching:
// attribute:: matches attribute names, namespace:: matches namespace names,
// every other axis matches element names.
func principalKind(axis string) stepNodeKind {
	switch axis {
	case "attribute":
		return skAttribute
	case "namespace":
		return skNamespace
	default:
		return skElement
	}
}

// hasChildrenKind reports whether a node of this kind can ever have children
// (only document and element nodes can; every other kind is a leaf).
func hasChildrenKind(k stepNodeKind) bool {
	return k == skDocument || k == skElement
}

// kindCompatible reports whether two (possibly "any") kinds could describe
// the same node.
func kindCompatible(a, b stepNodeKind) bool {
	return a == skAny || b == skAny || a == b
}

// testShapeOf returns the static kind/name a step's own axis+test targets,
// independent of whatever feeds it. Anything this analysis does not have a
// precise, safe answer for (schema-aware tests, KindTests with an inner
// element/attribute type) degrades to skAny (element/attribute KindTests
// still fix the KIND, just not the name).
func testShapeOf(axis string, t *NodeTest) (kind stepNodeKind, name string, nameKnown bool) {
	switch t.Kind {
	case testName:
		kind = principalKind(axis)
		if !t.AnyName && !t.WildNS && t.Prefix == "" && t.URI == "" && t.Local != "" {
			name, nameKnown = t.Local, true
		}
	case testNode:
		kind = skAny
	case testText:
		kind = skText
	case testComment:
		kind = skComment
	case testPI:
		kind = skPI
	case testElement:
		kind = skElement
	case testAttribute:
		kind = skAttribute
	case testDocument:
		kind = skDocument
	case testNamespace:
		kind = skNamespace
	default:
		// testSchemaElement / testSchemaAttr / anything unrecognized: no safe
		// kind assumption.
		kind = skAny
	}
	return
}

// stepResult computes, given one axis step and the shape of whatever feeds
// it, whether the step is PROVABLY empty by structure alone, and if not, the
// best-effort shape of what it could produce (degrading to skAny when this
// analysis cannot say anything precise, which is always safe: it just means
// later steps also see skAny and cannot prove anything further either).
func stepResult(axis string, t *NodeTest, in stepShape) (out stepShape, alwaysEmpty bool) {
	tk, tn, tnk := testShapeOf(axis, t)
	switch axis {
	case "self":
		if in.kind == skAny {
			return stepShape{kind: tk, name: tn, nameKnown: tnk, parentless: in.parentless}, false
		}
		if !kindCompatible(tk, in.kind) {
			return stepShape{}, true
		}
		if tnk && in.nameKnown && tn != in.name {
			return stepShape{}, true
		}
		name, nameKnown := in.name, in.nameKnown
		if tnk {
			name, nameKnown = tn, true
		}
		return stepShape{kind: in.kind, name: name, nameKnown: nameKnown, parentless: in.parentless}, false
	case "child", "descendant":
		if in.kind != skAny && !hasChildrenKind(in.kind) {
			return stepShape{}, true
		}
		return stepShape{kind: tk, name: tn, nameKnown: tnk}, false
	case "descendant-or-self":
		selfOK := in.kind == skAny || (kindCompatible(tk, in.kind) && (!tnk || !in.nameKnown || tn == in.name))
		descOK := in.kind == skAny || hasChildrenKind(in.kind)
		switch {
		case !selfOK && !descOK:
			return stepShape{}, true
		case descOK && !selfOK:
			return stepShape{kind: tk, name: tn, nameKnown: tnk}, false
		case selfOK && !descOK:
			name, nameKnown := in.name, in.nameKnown
			if tnk {
				name, nameKnown = tn, true
			}
			return stepShape{kind: in.kind, name: name, nameKnown: nameKnown, parentless: in.parentless}, false
		default:
			return stepShape{kind: tk, name: tn, nameKnown: tnk}, false
		}
	case "parent":
		if in.kind == skDocument || in.parentless {
			return stepShape{}, true
		}
		return stepShape{kind: tk, name: tn, nameKnown: tnk}, false
	case "attribute":
		if in.kind != skAny && in.kind != skElement {
			return stepShape{}, true
		}
		return stepShape{kind: skAttribute, name: tn, nameKnown: tnk}, false
	default:
		// ancestor, ancestor-or-self, following, preceding, following-sibling,
		// preceding-sibling, namespace, or anything else: deliberately not
		// reasoned about.
		return stepShape{kind: skAny}, false
	}
}

// pathStaticEmptyCheck reports err:XPST0005 when p's OWN step chain (not
// counting nested predicates/postfix expressions, which are checked
// separately by the caller) can be proven, by structure alone, to always
// select nothing.
// isParentlessConstructor reports whether e is a call to fn:parse-xml-fragment
// — the one function whose result this analysis can name outright as always
// parentless, regardless of what node kind(s) the fragment turns out to
// contain (parse-xml-fragment-022-st).
func isParentlessConstructor(e Expr) bool {
	call, ok := e.(*FuncCall)
	if !ok {
		return false
	}
	return (call.Prefix == "" || call.Prefix == "fn") && call.Local == "parse-xml-fragment"
}

func pathStaticEmptyCheck(p *PathExpr) error {
	if p == nil || len(p.Steps) == 0 {
		return nil
	}
	cur := stepShape{kind: skAny}
	if p.Start == nil && p.Absolute {
		// A bare rooted path ("/..." or "//...", no leading primary
		// expression) starts at exactly the document node.
		cur = stepShape{kind: skDocument}
	} else if isParentlessConstructor(p.Start) {
		cur = stepShape{kind: skAny, parentless: true}
	}
	for _, s := range p.Steps {
		if s == nil {
			continue
		}
		if s.Postfix != nil {
			// A dynamic postfix step (a function call used as a path step)
			// is opaque to this analysis, EXCEPT the one narrow case this
			// engine can name outright: fn:parse-xml-fragment's own result
			// is, by construction, a freshly parsed and never-attached
			// fragment (parse-xml-fragment-022-st: "result is parentless").
			if isParentlessConstructor(s.Postfix) {
				cur = stepShape{kind: skAny, parentless: true}
			} else {
				cur = stepShape{kind: skAny}
			}
			continue
		}
		out, empty := stepResult(s.Axis, &s.Test, cur)
		if empty {
			return fmt.Errorf("err:XPST0005: the %s:: axis can never select anything here", s.Axis)
		}
		cur = out
	}
	return nil
}

// checkStaticEmptyPaths walks an entire expression tree looking for every
// PathExpr (including ones nested inside function arguments, predicates,
// FLWOR bodies, etc.) and reports the first one whose own step chain is
// provably empty. It mirrors walkFuncCalls's traversal (pattern_calls.go)
// node-kind for node-kind, so keep the two in sync if the grammar grows a new
// Expr kind that can carry a nested PathExpr.
func checkStaticEmptyPaths(e Expr) error {
	if e == nil {
		return nil
	}
	switch v := e.(type) {
	case *FuncCall:
		for _, a := range v.Args {
			if err := checkStaticEmptyPaths(a); err != nil {
				return err
			}
		}
	case *BinaryExpr:
		if err := checkStaticEmptyPaths(v.L); err != nil {
			return err
		}
		return checkStaticEmptyPaths(v.R)
	case *UnaryExpr:
		return checkStaticEmptyPaths(v.X)
	case *UnionExpr:
		if err := checkStaticEmptyPaths(v.L); err != nil {
			return err
		}
		return checkStaticEmptyPaths(v.R)
	case *PathExpr:
		if err := pathStaticEmptyCheck(v); err != nil {
			return err
		}
		if err := checkStaticEmptyPaths(v.Start); err != nil {
			return err
		}
		for _, s := range v.Steps {
			if s == nil {
				continue
			}
			for _, pr := range s.Preds {
				if err := checkStaticEmptyPaths(pr); err != nil {
					return err
				}
			}
			if err := checkStaticEmptyPaths(s.Postfix); err != nil {
				return err
			}
		}
	case *FilterExpr:
		if err := checkStaticEmptyPaths(v.Primary); err != nil {
			return err
		}
		for _, pr := range v.Preds {
			if err := checkStaticEmptyPaths(pr); err != nil {
				return err
			}
		}
	case *SequenceExpr:
		for _, it := range v.Items {
			if err := checkStaticEmptyPaths(it); err != nil {
				return err
			}
		}
	case *RangeExpr:
		if err := checkStaticEmptyPaths(v.From); err != nil {
			return err
		}
		return checkStaticEmptyPaths(v.To)
	case *ForExpr:
		for _, b := range v.Binds {
			if err := checkStaticEmptyPaths(b.Seq); err != nil {
				return err
			}
		}
		return checkStaticEmptyPaths(v.Body)
	case *LetExpr:
		for _, b := range v.Binds {
			if err := checkStaticEmptyPaths(b.Seq); err != nil {
				return err
			}
		}
		return checkStaticEmptyPaths(v.Body)
	case *QuantExpr:
		for _, b := range v.Binds {
			if err := checkStaticEmptyPaths(b.Seq); err != nil {
				return err
			}
		}
		return checkStaticEmptyPaths(v.Satisfies)
	case *IfExpr:
		if err := checkStaticEmptyPaths(v.Cond); err != nil {
			return err
		}
		if err := checkStaticEmptyPaths(v.Then); err != nil {
			return err
		}
		return checkStaticEmptyPaths(v.Else)
	case *MapExpr:
		for i := range v.Keys {
			if err := checkStaticEmptyPaths(v.Keys[i]); err != nil {
				return err
			}
			if i < len(v.Vals) {
				if err := checkStaticEmptyPaths(v.Vals[i]); err != nil {
					return err
				}
			}
		}
	case *ArrayExpr:
		for _, it := range v.Items {
			if err := checkStaticEmptyPaths(it); err != nil {
				return err
			}
		}
	case *InlineFunc:
		return checkStaticEmptyPaths(v.Body)
	case *LookupExpr:
		if err := checkStaticEmptyPaths(v.Base); err != nil {
			return err
		}
		return checkStaticEmptyPaths(v.Key)
	case *DynCall:
		if err := checkStaticEmptyPaths(v.Base); err != nil {
			return err
		}
		for _, a := range v.Args {
			if err := checkStaticEmptyPaths(a); err != nil {
				return err
			}
		}
	case *CompareExpr:
		if err := checkStaticEmptyPaths(v.L); err != nil {
			return err
		}
		return checkStaticEmptyPaths(v.R)
	case *StringConcatExpr:
		for _, pt := range v.Parts {
			if err := checkStaticEmptyPaths(pt); err != nil {
				return err
			}
		}
	case *IntersectExceptExpr:
		if err := checkStaticEmptyPaths(v.L); err != nil {
			return err
		}
		return checkStaticEmptyPaths(v.R)
	case *InstanceOfExpr:
		return checkStaticEmptyPaths(v.X)
	case *TreatExpr:
		return checkStaticEmptyPaths(v.X)
	case *CastExpr:
		return checkStaticEmptyPaths(v.X)
	case *ArrowExpr:
		if err := checkStaticEmptyPaths(v.Base); err != nil {
			return err
		}
		for _, s := range v.Steps {
			if err := checkStaticEmptyPaths(s.Spec); err != nil {
				return err
			}
			for _, a := range s.Args {
				if err := checkStaticEmptyPaths(a); err != nil {
					return err
				}
			}
		}
	case *SimpleMapExpr:
		for _, s := range v.Steps {
			if err := checkStaticEmptyPaths(s); err != nil {
				return err
			}
		}
	}
	return nil
}
