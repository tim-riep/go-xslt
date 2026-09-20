package xpath

import (
	"fmt"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// Pattern is a compiled XSLT match pattern: one or more alternatives separated
// by '|'. Match tests whether a node is selected by the pattern.
type Pattern struct {
	alts     []*PathExpr
	exprAlts []Expr // XSLT 3.0 expression-based patterns ((a|b)[p], doc(), id(), $var, except/intersect)
	src      string
}

// Src returns the original pattern text.
func (p *Pattern) Src() string { return p.src }

// ParsePattern compiles an XSLT match pattern.
func ParsePattern(src string) (*Pattern, error) { return parsePatternWithSchema(src, nil) }

// parsePatternWithSchema is ParsePattern with schema component names resolved
// through sn (nil for an ordinary parse) — see schema_names.go.
func parsePatternWithSchema(src string, sn SchemaNameLookup) (*Pattern, error) {
	pat := &Pattern{src: src}
	alts := patternAlternatives(src)
	// Pattern ::= PredicatePattern | UnionExprP, so the PredicatePattern form
	// (". PredicateList") may only be the WHOLE pattern: it is not a
	// UnionExprP operand and cannot be parenthesized. Both are ordinary XPath
	// expressions, hence legal further down, and both are XTSE0340 here
	// (match-060/129 union one with a node test or with each other; match-239
	// wraps one in parentheses).
	for _, alt := range alts {
		// The parenthesis test looks at the ORIGINAL source: patternAlternatives
		// strips a wrapping pair of parentheses, so by the time an alternative
		// is in hand "(.[p])" is indistinguishable from ".[p]".
		if len(alts) == 1 && !patternStartsWithParen(src) {
			continue
		}
		if altIsPredicatePattern(alt) {
			return nil, fmt.Errorf("err:XTSE0340: a predicate pattern (%q) must be the entire pattern, not a union operand or parenthesized, in %q", strings.TrimSpace(alt), src)
		}
	}
	for _, alt := range alts {
		toks, err := lex(alt)
		if err != nil {
			return nil, err
		}
		pr := &parser{toks: toks, schema: sn}
		e, err := pr.parsePath()
		if err != nil || pr.cur().kind != tEOF {
			// Not a plain path pattern: try the XSLT 3.0 expression-pattern
			// forms ((a|b)[p], doc(...)/..., id(...), key(...), $var, union/
			// except/intersect) as a full expression.
			if ex, exErr := parseExprPattern(alt, sn); exErr == nil {
				pat.exprAlts = append(pat.exprAlts, ex)
				continue
			}
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("unexpected token %q in pattern", pr.cur().text)
		}
		pe, ok := e.(*PathExpr)
		if !ok {
			// "." (optionally with predicates) is a valid pattern matching the
			// context node, i.e. any node satisfying the predicates. Represent it
			// as a self::node() step.
			if cpe, okc := contextItemPattern(e); okc {
				pe, ok = cpe, true
			}
		}
		if !ok {
			if ex, exErr := parseExprPattern(alt, sn); exErr == nil {
				pat.exprAlts = append(pat.exprAlts, ex)
				continue
			}
			// A bare name parses to PathExpr already; anything else is unsupported.
			return nil, fmt.Errorf("unsupported pattern %q", alt)
		}
		if err := checkPatternSteps(pe, alt); err != nil {
			return nil, err
		}
		if hasInnerPostfixStep(pe) || hasExplicitDescendantStep(pe) {
			// A parenthesized sub-pattern after "/" (e.g. "x/(a|b)" or
			// "x[@id='1']/(descendant::a except child::a)") is not something
			// the step-by-step path matcher can walk backwards; evaluate the
			// whole alternative as an expression pattern instead.
			if ex, exErr := parseExprPattern(alt, sn); exErr == nil {
				pat.exprAlts = append(pat.exprAlts, ex)
				continue
			}
		}
		pat.alts = append(pat.alts, pe)
	}
	if len(pat.alts) == 0 && len(pat.exprAlts) == 0 {
		return nil, fmt.Errorf("empty pattern")
	}
	return pat, nil
}

// patternStartsWithParen reports whether the alternative's first SIGNIFICANT
// character is an opening parenthesis. Leading whitespace and XPath comments
// are skipped, so a pattern that merely opens with a "(:...:)" comment — which
// the suite writes a lot of (match-132/246a) — is not mistaken for a
// parenthesized one.
func patternStartsWithParen(s string) bool {
	for i := 0; i < len(s); {
		switch {
		case s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r':
			i++
		case strings.HasPrefix(s[i:], "(:"):
			depth := 1
			for i += 2; i < len(s) && depth > 0; {
				switch {
				case strings.HasPrefix(s[i:], "(:"):
					depth, i = depth+1, i+2
				case strings.HasPrefix(s[i:], ":)"):
					depth, i = depth-1, i+2
				default:
					i++
				}
			}
		default:
			return s[i] == '('
		}
	}
	return false
}

// altIsPredicatePattern reports whether one pattern alternative is the
// PredicatePattern form: the context item "." followed by zero or more
// predicates. Parentheses are transparent in this AST, so "(.[p])" answers
// true here too — which is exactly what the caller's parenthesis check needs.
func altIsPredicatePattern(alt string) bool {
	toks, err := lex(alt)
	if err != nil {
		return false
	}
	pr := &parser{toks: toks}
	e, err := pr.parseExpr()
	if err != nil || pr.cur().kind != tEOF {
		return false
	}
	// "(E)" parses to a one-item SequenceExpr; unwrap so the parenthesized
	// form is recognized as the same pattern shape as the bare one.
	for {
		se, ok := e.(*SequenceExpr)
		if !ok || len(se.Items) != 1 {
			break
		}
		e = se.Items[0]
	}
	_, ok := contextItemPattern(e)
	return ok
}

// parseExprPattern parses a pattern alternative as a full XPath expression
// (the XSLT 3.0 expression-based pattern forms).
func parseExprPattern(alt string, sn SchemaNameLookup) (Expr, error) {
	toks, err := lex(alt)
	if err != nil {
		return nil, err
	}
	pr := &parser{toks: toks, schema: sn}
	e, err := pr.parseExpr()
	if err != nil {
		return nil, err
	}
	if pr.cur().kind != tEOF {
		return nil, fmt.Errorf("unexpected token %q in pattern", pr.cur().text)
	}
	if err := checkExprPatternShape(e, alt); err != nil {
		return nil, err
	}
	return e, nil
}

// checkExprPatternShape rejects expressions that parse fine as XPath but are
// not in the XSLT pattern grammar at all. The grammar admits unions,
// intersect/except, predicated paths and the restricted set of "rooted"
// primaries (a parenthesized pattern, a variable reference, doc()/id()/key()/
// root(), the context item) — never arithmetic, comparison, sequence
// construction or a bare literal (error-0340d: xsl:number count="2+2").
func checkExprPatternShape(e Expr, src string) error {
	switch v := e.(type) {
	case *UnionExpr:
		if err := checkExprPatternShape(v.L, src); err != nil {
			return err
		}
		return checkExprPatternShape(v.R, src)
	case *IntersectExceptExpr:
		if err := checkExprPatternShape(v.L, src); err != nil {
			return err
		}
		return checkExprPatternShape(v.R, src)
	case *BinaryExpr, *UnaryExpr, *CompareExpr, *StringConcatExpr,
		*RangeExpr, *SequenceExpr, *ForExpr, *LetExpr, *QuantExpr, *IfExpr,
		*InstanceOfExpr, *TreatExpr, *CastExpr, *InlineFunc, *MapExpr,
		*ArrayExpr, *SimpleMapExpr, *ArrowExpr, *LiteralExpr, *NamedFuncRef:
		return fmt.Errorf("err:XTSE0340: %q is not a valid pattern", src)
	}
	return nil
}

// patternStepAxes are the only axes the Pattern grammar's PatternAxis
// production admits for a step itself (§6.3): child, attribute (incl. the
// "@" shorthand), self, descendant, descendant-or-self, and namespace.
// Reverse axes (parent, ancestor, ancestor-or-self, following, following-
// sibling, preceding, preceding-sibling) are not pattern syntax AT ALL — not
// even as a pattern's last step — though they remain ordinary XPath inside a
// step's PREDICATE, which this check never inspects (st.Preds is untouched:
// current-001/match-041/match246/mode-1425/number-1301/predicate-054/
// streamable-122/key-097 all use a reverse axis only inside a "[...]" and
// stay valid). version-023a: forwards-compatibility mode (a future version=
// value) relaxes checks tied to unsupported FEATURES, not this base grammar
// restriction — XTSE0340 still fires.
var patternStepAxes = map[string]bool{
	"child": true, "attribute": true, "self": true,
	"descendant": true, "descendant-or-self": true, "namespace": true,
}

// checkPatternSteps rejects path patterns whose name tests are not valid names
// (e.g. "name/1223" — legal as an XPath 3.1 expression, illegal as a pattern),
// or whose step uses an axis outside the Pattern grammar's allowed set.
func checkPatternSteps(pe *PathExpr, src string) error {
	for _, st := range pe.Steps {
		// The pattern grammar admits only function calls, variables and
		// parenthesized unions as non-axis steps: a literal or an array/map
		// constructor after "/" is an expression, not a pattern
		// (XSLT match-068: "/[doc]" is XTSE0340).
		switch st.Postfix.(type) {
		case *LiteralExpr, *ArrayExpr, *MapExpr, *InlineFunc:
			return fmt.Errorf("err:XTSE0340: a %T is not a valid pattern step in %q", st.Postfix, src)
		}
		if st.Postfix == nil && st.Axis != "" && !patternStepAxes[st.Axis] {
			return fmt.Errorf("err:XTSE0340: axis %q is not a valid pattern step axis in %q", st.Axis, src)
		}
		if st.Test.Kind != testName || st.Test.AnyName || st.Test.WildNS || st.Test.Local == "" {
			continue
		}
		r := rune(st.Test.Local[0])
		if r >= '0' && r <= '9' {
			return fmt.Errorf("err:XTSE0340: %q is not a valid name test in pattern %q", st.Test.Local, src)
		}
	}
	return nil
}

// hasExplicitDescendantStep reports whether a MULTI-step path pattern spells a
// descendant / descendant-or-self axis out explicitly (as opposed to the "//"
// connector the step-by-step matcher already understands). Such a pattern —
// "doc/descendant::foo", "chapter/descendant::foo[1]" — cannot be walked
// backwards one parent at a time, because the step spans an unbounded number
// of levels and its predicates are positional over the whole axis; evaluating
// the alternative as an expression pattern from each anchor gets it right
// (match-075/235/237/238).
func hasExplicitDescendantStep(pe *PathExpr) bool {
	if len(pe.Steps) < 2 {
		return false
	}
	for _, st := range pe.Steps {
		if isConnectorStep(st) || st.Postfix != nil {
			continue
		}
		if st.Axis == "descendant" || st.Axis == "descendant-or-self" {
			return true
		}
	}
	return false
}

// hasInnerPostfixStep reports whether a path pattern has a non-leading step
// that is a postfix (parenthesized) expression rather than an axis step.
func hasInnerPostfixStep(pe *PathExpr) bool {
	for i, st := range pe.Steps {
		if i > 0 && st.Postfix != nil {
			return true
		}
	}
	return false
}

// contextItemPattern recognises "." and ".[pred]…" — a context-item expression,
// optionally wrapped in a FilterExpr of predicates — and returns an equivalent
// self::node() path step (which matches any node) carrying those predicates.
func contextItemPattern(e Expr) (*PathExpr, bool) {
	var preds []Expr
	// ".[a][b]" may parse as nested FilterExprs; unwrap until the primary is
	// the context item, collecting the predicates outermost-last so they stay
	// in source order (match-131/132/240: a multi-predicate predicate pattern
	// is a pattern, not an "expression pattern" whose result would be the
	// ATOMIC context item and so could never be identical to the node being
	// matched).
	for {
		fe, ok := e.(*FilterExpr)
		if !ok {
			break
		}
		preds = append(append([]Expr{}, fe.Preds...), preds...)
		e = fe.Primary
	}
	if _, ok := e.(*ContextItemExpr); !ok {
		return nil, false
	}
	return &PathExpr{predPattern: len(preds) > 0, Steps: []*Step{{
		Axis:  "self",
		Test:  NodeTest{Kind: testNode},
		Preds: preds,
	}}}, true
}

// patternAlternatives splits a pattern into its top-level union alternatives,
// also unwrapping an alternative that is WHOLLY parenthesized: XSLT 3.0 allows
// "(doc|cod)" as a pattern, and it is exactly equivalent to the union
// "doc|cod" — same alternatives, each with its OWN default priority, not the
// 0.5 an expression pattern would get (match-082a/b/c).
func patternAlternatives(src string) []string {
	var out []string
	for _, alt := range splitTopLevelUnion(src) {
		if inner, ok := stripOuterParens(alt); ok {
			out = append(out, patternAlternatives(inner)...)
			continue
		}
		out = append(out, alt)
	}
	return out
}

// stripOuterParens removes one layer of parentheses wrapping the WHOLE
// expression. "(:" starts an XPath comment, never a parenthesized expression,
// so it is left alone.
func stripOuterParens(s string) (string, bool) {
	t := strings.TrimSpace(s)
	if len(t) < 3 || t[0] != '(' || t[len(t)-1] != ')' || t[1] == ':' {
		return s, false
	}
	depth := 0
	for i := 0; i < len(t); i++ {
		switch t[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 && i != len(t)-1 {
				return s, false // the opening paren closes early
			}
		}
	}
	if depth != 0 {
		return s, false
	}
	return strings.TrimSpace(t[1 : len(t)-1]), true
}

// splitTopLevelUnion splits a pattern on '|' that are not inside brackets.
func splitTopLevelUnion(s string) []string {
	var parts []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[', '(':
			depth++
		case ']', ')':
			depth--
		case '|':
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	parts = append(parts, strings.TrimSpace(s[start:]))
	return parts
}

// reverseNodes flips a node-set in place.
func reverseNodes(ns NodeSet) {
	for i, j := 0, len(ns)-1; i < j; i, j = i+1, j-1 {
		ns[i], ns[j] = ns[j], ns[i]
	}
}

// PatternAlternatives splits a match pattern's source text into its top-level
// union alternatives. XSLT §6.4 treats a union pattern as a SET of template
// rules — "a template rule with match='A|B' is equivalent to two template
// rules, one with match='A' and one with match='B'" — so each alternative
// carries its own default priority and is reached separately by
// xsl:next-match. A pattern with no top-level '|' yields a single entry.
func PatternAlternatives(src string) []string { return patternAlternatives(src) }

// Match reports whether node n is selected by the pattern, using ctx for
// variable/namespace/function resolution within predicates.
func (p *Pattern) Match(n *xmltree.Node, ctx *Context) (bool, error) {
	for _, alt := range p.alts {
		ok, err := matchPath(alt, n, ctx)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	for _, ex := range p.exprAlts {
		ok, err := matchExprPattern(ex, n, ctx)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// matchExprPattern implements the XSLT 3.0 rule for expression-based patterns:
// the node matches if evaluating the expression with SOME ancestor-or-self as
// the context item selects it. Most evaluation errors count as non-matches
// (deliberately lenient — e.g. a malformed id()/key() argument on some
// ancestor should not abort the whole match attempt), EXCEPT a circular
// xsl:key definition (XTDE0640), which must always propagate as a genuine
// non-recoverable dynamic error (error-0640a).
func matchExprPattern(ex Expr, n *xmltree.Node, ctx *Context) (bool, error) {
	// The anchor node ranges over n's ancestor-or-self chain UP TO the top of
	// the tree n really belongs to. effectiveRoot stops at a NoAtomicMerge
	// sequence collector: in the XSLT data model an element produced by an
	// @as-typed sequence constructor is PARENTLESS, so that scratch document
	// node is not an anchor (match-275: "descendant::a except child::a" must
	// not match the outer <a> — it only does if the collector root, where
	// child::a selects nothing, is allowed to be the anchor).
	top := effectiveRoot(n)
	for a := n; a != nil; a = a.Parent {
		sub := &Context{SchemaTypes: ctx.SchemaTypes, Node: a, CtxItem: a.RealItem, Pos: 1, Size: 1, Vars: ctx.Vars, NS: ctx.NS, Funcs: ctx.Funcs,
			Resolver: ctx.Resolver, BaseURI: ctx.BaseURI, DefaultElemNS: ctx.DefaultElemNS, DefaultCollation: ctx.DefaultCollation, locals: ctx.locals,
			PatternTop: top}
		v, err := evalExpr(ex, sub)
		if err != nil {
			if strings.Contains(err.Error(), "XTDE0640") {
				return false, err
			}
			continue
		}
		for _, it := range Items(v) {
			if m, ok := it.(*xmltree.Node); ok && m == n {
				return true, nil
			}
		}
		if a == top {
			break
		}
	}
	return false, nil
}

func matchPath(pe *PathExpr, n *xmltree.Node, ctx *Context) (bool, error) {
	if len(pe.Steps) == 0 {
		if pe.Start != nil {
			// A pattern that is wholly a leading primary expression, with no
			// trailing path (e.g. match="key('k1', $x)" or match="$var"): n
			// matches iff it is itself one of the items Start evaluates to.
			return nodeInPatternStart(pe, n, ctx)
		}
		// "/" matches the document/root node.
		return pe.Absolute && n.Kind == xmltree.KindDocument, nil
	}
	return matchStepRec(pe, len(pe.Steps)-1, n, ctx)
}

// nodeInPatternStart reports whether target is one of the nodes produced by
// evaluating pe.Start (the pattern's leading primary expression — typically
// id(...)/key(...)/doc(...), a variable reference, or a parenthesized union)
// in ctx. Non-node items in the result never match (a pattern only ever
// selects nodes).
func nodeInPatternStart(pe *PathExpr, target *xmltree.Node, ctx *Context) (bool, error) {
	v, err := evalExpr(pe.Start, ctx)
	if err != nil {
		return false, err
	}
	for _, it := range Items(v) {
		if m, ok := it.(*xmltree.Node); ok && m == target {
			return true, nil
		}
	}
	return false, nil
}

func matchStepRec(pe *PathExpr, i int, n *xmltree.Node, ctx *Context) (bool, error) {
	step := pe.Steps[i]
	ok, err := stepMatches(step, n, ctx)
	if err != nil || !ok {
		return false, err
	}
	if i == 0 {
		if pe.Absolute {
			// "/bbb" needs a REAL document parent: an element sitting in a
			// discrete-sequence collector (a document node with NoAtomicMerge,
			// the same tree boundary effectiveRoot draws) is parentless as
			// far as the data model is concerned (sequence-0124).
			return n.Parent != nil && n.Parent.Kind == xmltree.KindDocument && !n.Parent.NoAtomicMerge, nil
		}
		if pe.Start != nil {
			// A single '/' separates the leading primary expression from the
			// path steps (e.g. match="key('k1', $x)/Age"): n's parent must be
			// one of the nodes Start evaluates to (key-035/key-033).
			if n.Parent == nil {
				return false, nil
			}
			return nodeInPatternStart(pe, n.Parent, ctx)
		}
		return true, nil
	}
	prev := pe.Steps[i-1]
	if isConnectorStep(prev) {
		if i-2 < 0 {
			if pe.Start != nil {
				// '//' separates Start from the path steps (e.g.
				// match="key('k1', $x)//*"): some ancestor-or-self of n's
				// parent must be one of the nodes Start evaluates to.
				for anc := n.Parent; anc != nil; anc = anc.Parent {
					ok, err := nodeInPatternStart(pe, anc, ctx)
					if err != nil {
						return false, err
					}
					if ok {
						return true, nil
					}
				}
				return false, nil
			}
			// leading '//' — matches at any depth, but ONLY inside a tree
			// rooted at a document node: "//a" cannot match a node whose
			// tree is a parentless element (match-215).
			return effectiveRoot(n).Kind == xmltree.KindDocument, nil
		}
		for anc := n.Parent; anc != nil; anc = anc.Parent {
			ok, err := matchStepRec(pe, i-2, anc, ctx)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	}
	if n.Parent == nil {
		return false, nil
	}
	return matchStepRec(pe, i-1, n.Parent, ctx)
}

func isConnectorStep(s *Step) bool {
	return s.Axis == "descendant-or-self" && s.Test.Kind == testNode && len(s.Preds) == 0
}

func stepMatches(step *Step, n *xmltree.Node, ctx *Context) (bool, error) {
	// XSLT §5.5.3 "The Meaning of a Pattern": a document-node() node test with
	// NO explicit axis is evaluated on the SELF axis, not child — that is what
	// makes the pattern "document-node()" match a document node at all, while
	// "child::document-node()" keeps the child axis and so matches nothing
	// (match-048 vs match-088/root-0101).
	axis := step.Axis
	if axis == "child" && !step.AxisExplicit && step.Test.Kind == testDocument {
		axis = "self"
	}
	// A pattern step's axis constrains the KIND of node it can ever select,
	// independently of its node test: attribute:: (i.e. attribute-or-top)
	// reaches only attribute nodes, namespace:: only namespace nodes, and
	// child:: (i.e. child-or-top — the children, or the context node itself
	// when that is a parentless element/text/comment/PI) reaches neither of
	// those nor a document node. matchNameTest already enforces the principal
	// node kind for NAME tests; kind tests need it too, so that e.g.
	// "attribute::element()" or "attribute::text()" match nothing at all
	// rather than matching every element/text node in the tree
	// (match-103/110/111/112/113), and so that "node()"/"child::node()" never
	// catches the document node or an attribute (key-078: xsl:key's eager
	// index build tests every node in the tree directly, attributes
	// included). self:: is deliberately left unconstrained — "." (compiled to
	// self::node() by contextItemPattern) still has to match a document-node
	// ancestor when xsl:number's level="multiple"/"any" walks up through one
	// via count="." (number-0110) — and matchTest itself stays fully
	// unrestricted for general expression evaluation / sequence-type checking
	// (conflict-resolution-0101/0106/0107/0112/0201).
	switch axis {
	case "attribute":
		if n.Kind != xmltree.KindAttribute {
			return false, nil
		}
	case "namespace":
		if n.Kind != xmltree.KindNamespace {
			return false, nil
		}
	case "child", "descendant", "descendant-or-self":
		if n.Kind == xmltree.KindAttribute || n.Kind == xmltree.KindNamespace ||
			n.Kind == xmltree.KindDocument {
			return false, nil
		}
	}
	ok, err := matchTest(step.Test, axis, n, ctx)
	if err != nil || !ok {
		return false, err
	}
	if len(step.Preds) == 0 {
		return true, nil
	}
	// Candidate list: the axis siblings passing the node test (n's proximity
	// position is its index here).
	var cands []*xmltree.Node
	if n.Parent == nil {
		cands = []*xmltree.Node{n}
	} else {
		for _, s := range axisNodes(step.Axis, n.Parent) {
			ok, err := matchTest(step.Test, step.Axis, s, ctx)
			if err != nil {
				return false, err
			}
			if ok {
				cands = append(cands, s)
			}
		}
	}
	pos := 1
	for i, s := range cands {
		if s == n {
			pos = i + 1
			break
		}
	}
	// Fast path: evaluate the predicates on n only. Correct as long as every
	// result is a boolean AND there is only one predicate; a numeric
	// (positional) result needs the sequential filtering semantics below
	// (foo[@a='c'][2] = second foo whose @a is 'c'), and so does a SECOND (or
	// later) predicate in general — position()/last() inside it are relative
	// to the survivors of the earlier predicate(s), not to the original
	// candidate list, even when the predicate's own value is boolean rather
	// than a bare integer (match-022/023: "[(position() mod 2)=1][position()
	// > 3]" — the second predicate's position() ranges over the six nodes
	// the first one kept, not all of cands).
	fastOK := len(step.Preds) <= 1
	for i := 0; fastOK && i < len(step.Preds); i++ {
		pred := step.Preds[i]
		sub := &Context{SchemaTypes: ctx.SchemaTypes, Node: n, CtxItem: n.RealItem, Pos: pos, Size: len(cands), Vars: ctx.Vars, NS: ctx.NS, Funcs: ctx.Funcs, DefaultElemNS: ctx.DefaultElemNS, DefaultCollation: ctx.DefaultCollation, locals: ctx.locals}
		v, err := evalExpr(pred, sub)
		if err != nil {
			return false, err
		}
		if _, isNum := asPositionalInt(v); isNum {
			fastOK = false
			break
		}
		if !ToBool(v) {
			return false, nil
		}
	}
	if fastOK {
		return true, nil
	}
	// Slow path: filter the candidates predicate by predicate.
	for _, pred := range step.Preds {
		kept := cands[:0:0]
		size := len(cands)
		for i, s := range cands {
			sub := &Context{SchemaTypes: ctx.SchemaTypes, Node: s, CtxItem: s.RealItem, Pos: i + 1, Size: size, Vars: ctx.Vars, NS: ctx.NS, Funcs: ctx.Funcs, DefaultElemNS: ctx.DefaultElemNS, DefaultCollation: ctx.DefaultCollation, locals: ctx.locals}
			v, err := evalExpr(pred, sub)
			if err != nil {
				return false, err
			}
			if num, ok := asPositionalInt(v); ok {
				if num == i+1 {
					kept = append(kept, s)
				}
				continue
			}
			if ToBool(v) {
				kept = append(kept, s)
			}
		}
		cands = kept
	}
	for _, s := range cands {
		if s == n {
			return true, nil
		}
	}
	return false, nil
}

// DefaultPriority returns the XSLT default priority of the pattern (the maximum
// over its alternatives).
func (p *Pattern) DefaultPriority() float64 {
	best := -1.0
	first := true
	for _, alt := range p.alts {
		pr := pathDefaultPriority(alt)
		if first || pr > best {
			best, first = pr, false
		}
	}
	if len(p.exprAlts) > 0 && (first || best < 0.5) {
		// Expression-based patterns default to priority 0.5.
		best, first = 0.5, false
	}
	if first {
		return 0
	}
	return best
}

func pathDefaultPriority(pe *PathExpr) float64 {
	if pe.predPattern {
		// XSLT 3.0: a PredicatePattern (".[...]", one or more predicates) has
		// default priority 1 — higher than the generic 0.5 every other
		// "complex" pattern gets, and independent of how many predicates it
		// carries (match-131: it must rank between explicit priorities
		// 0.999999 and 1.0000001; match-132: one and two predicates tie).
		return 1
	}
	if pe.Start != nil {
		// A pattern with a leading primary expression (IdKeyPattern, $var,
		// parenthesized union, ...), with or without trailing path steps,
		// takes the same default priority as any other expression-based
		// pattern (0.5).
		return 0.5
	}
	if len(pe.Steps) == 0 {
		// XSLT §6.4: 'If the pattern has the form /, then the priority is
		// -0.5' (XSLT 1.0 gave it 0.5; 2.0 onwards does not — and this suite
		// tests the 2.0 value directly, conflict-resolution-1601).
		return -0.5
	}
	realSteps := 0
	for _, s := range pe.Steps {
		if isConnectorStep(s) {
			continue
		}
		realSteps++
	}
	last := pe.Steps[len(pe.Steps)-1]
	if realSteps > 1 || pe.Absolute || len(last.Preds) > 0 {
		return 0.5
	}
	return nodeTestDefaultPriority(last.Test)
}

// nodeTestDefaultPriority implements the XSLT §6.4 default-priority table for
// a single node test (a pattern that is one step with no predicates).
func nodeTestDefaultPriority(t NodeTest) float64 {
	switch t.Kind {
	case testName:
		// A wildcard that NAMES a namespace — prefix:*, *:local, or the braced
		// form Q{uri}* — has priority -0.25; only the bare "*" (any name in any
		// namespace) has -0.5. The braced case needs Braced, not Prefix: a
		// Q{uri}* test carries no prefix at all yet is just as specific as
		// prefix:* (match-261, W3C bug 30375).
		if t.AnyName && t.Prefix == "" && !t.WildNS && !t.Braced {
			return -0.5
		}
		if t.AnyName || t.WildNS {
			return -0.25 // prefix:* / Q{uri}* / *:local
		}
		return 0
	case testPI:
		if t.PITarget != "" {
			return 0
		}
		return -0.5
	case testElement, testAttribute:
		// element()/element(*)/attribute()/attribute(*) = -0.5 (equivalent to
		// * / @*); element(E)/attribute(A)/element(*,T)/attribute(*,T) = 0;
		// element(E,T)/attribute(A,T) = 0.25 (name AND type).
		named := !t.AnyName && t.Local != ""
		typed := t.TypeName != ""
		switch {
		case named && typed:
			return 0.25
		case named || typed:
			return 0
		}
		return -0.5
	case testSchemaElement, testSchemaAttr:
		return 0.25
	case testDocument:
		// document-node() = -0.5; document-node(element(...)) takes the
		// priority of the contained element test.
		if t.Inner != nil {
			return nodeTestDefaultPriority(*t.Inner)
		}
		return -0.5
	default: // node(), text(), comment(), namespace-node()
		return -0.5
	}
}
