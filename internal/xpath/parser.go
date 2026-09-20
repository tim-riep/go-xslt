package xpath

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// Parsed is a compiled XPath expression, ready to evaluate.
type Parsed struct {
	root Expr
	text string
}

// Text returns the original expression source.
func (p *Parsed) Text() string { return p.text }

// Parse compiles an XPath expression.
func Parse(src string) (*Parsed, error) {
	return parseCommon(src, false)
}

// ParseStaticTyping is Parse, but additionally enforces the narrow slice of
// XPath 3.1's OPTIONAL "Static Typing Feature" this engine can check without
// a general static type system: axis steps that are structurally provable,
// from axis+node-test shape alone, to always select nothing (err:XPST0005 —
// see static_axes.go). This is off by DEFAULT (plain Parse, used everywhere
// else in the engine — XSLT compilation included) because the feature is
// genuinely optional and its absence is not a conformance gap: per the spec,
// "if the Static Typing Feature is in effect and the static type... is
// empty-sequence(), a static error is raised" — outside that feature, the
// SAME construct (e.g. "@*/child::*", XSLT's axes-077/078) is expected to
// evaluate normally to an empty sequence, not fail to compile. It exists so a
// caller that wants to OPT IN for a specific expression (the QT3 harness,
// for a test-case/test-set declaring `dependency type="feature"
// value="staticTyping"`) can do so explicitly, without changing what every
// other caller of Parse sees.
func ParseStaticTyping(src string) (*Parsed, error) {
	return parseCommon(src, true)
}

func parseCommon(src string, staticTyping bool) (*Parsed, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, fmt.Errorf("xpath %q: %w", src, err)
	}
	p := &parser{toks: toks}
	e, err := p.parseExpr()
	if err != nil {
		return nil, fmt.Errorf("xpath %q: %w", src, err)
	}
	if p.cur().kind != tEOF {
		return nil, fmt.Errorf("xpath %q: unexpected trailing token %q", src, p.cur().text)
	}
	if staticTyping {
		if err := checkStaticEmptyPaths(e); err != nil {
			return nil, fmt.Errorf("xpath %q: %w", src, err)
		}
	}
	return &Parsed{root: e, text: src}, nil
}

// parseWithSchema is Parse with schema component names resolved through sn —
// see schema_names.go for why resolution happens here, at parse time, rather
// than when a node test is matched.
func parseWithSchema(src string, sn SchemaNameLookup) (*Parsed, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, fmt.Errorf("xpath %q: %w", src, err)
	}
	p := &parser{toks: toks, schema: sn}
	e, err := p.parseExpr()
	if err != nil {
		return nil, fmt.Errorf("xpath %q: %w", src, err)
	}
	if p.cur().kind != tEOF {
		return nil, fmt.Errorf("xpath %q: unexpected trailing token %q", src, p.cur().text)
	}
	return &Parsed{root: e, text: src}, nil
}

type parser struct {
	toks []token
	pos  int
	// schema is the host's schema component lookup, nil for every ordinary
	// (schema-unaware) parse. When set, a type name in a kind test and the
	// declaration named by schema-element()/schema-attribute() are resolved
	// as the test is built.
	schema SchemaNameLookup
	// schemaWithheld marks a parse whose host DELIBERATELY withheld the
	// in-scope schema components that do exist — xsl:evaluate without
	// schema-aware="yes" (XSLT 3.0 §10.4). An unresolvable user type name is
	// then a real error rather than the usual lenient degradation, because
	// the name may well denote a component the stylesheet has imported and
	// this expression is simply not entitled to see (XTDE3160).
	schemaWithheld bool
}

func (p *parser) cur() token        { return p.toks[p.pos] }
func (p *parser) next() token       { t := p.toks[p.pos]; p.pos++; return t }
func (p *parser) is(k tokKind) bool { return p.cur().kind == k }

func (p *parser) expect(k tokKind) (token, error) {
	if p.cur().kind != k {
		return token{}, fmt.Errorf("expected token %d, got %q", k, p.cur().text)
	}
	return p.next(), nil
}

// Expr := ExprSingle (',' ExprSingle)*  — a comma builds a sequence.
func (p *parser) parseExpr() (Expr, error) {
	first, err := p.parseExprSingle()
	if err != nil {
		return nil, err
	}
	if p.cur().kind != tComma {
		return first, nil
	}
	items := []Expr{first}
	for p.cur().kind == tComma {
		p.next()
		e, err := p.parseExprSingle()
		if err != nil {
			return nil, err
		}
		items = append(items, e)
	}
	return &SequenceExpr{Items: items}, nil
}

// ExprSingle := ForExpr | LetExpr | QuantifiedExpr | IfExpr | OrExpr
func (p *parser) parseExprSingle() (Expr, error) {
	switch {
	case p.isKeyword("for") && p.peekKind(1) == tDollar:
		return p.parseFor()
	case p.isKeyword("let") && p.peekKind(1) == tDollar:
		return p.parseLet()
	case (p.isKeyword("some") || p.isKeyword("every")) && p.peekKind(1) == tDollar:
		return p.parseQuantified()
	case p.isKeyword("if") && p.peekKind(1) == tLParen:
		return p.parseIf()
	}
	return p.parseOr()
}

func (p *parser) isKeyword(kw string) bool {
	return p.cur().kind == tName && p.cur().text == kw
}

func (p *parser) parseBindings(useAssign bool) ([]VarBind, error) {
	var binds []VarBind
	for {
		if _, err := p.expect(tDollar); err != nil {
			return nil, err
		}
		name, err := p.expect(tName)
		if err != nil {
			return nil, err
		}
		prefix, local := splitVarName(name.text)
		if useAssign {
			if _, err := p.expect(tAssign); err != nil {
				return nil, err
			}
		} else {
			if !p.isKeyword("in") {
				return nil, fmt.Errorf("expected 'in', got %q", p.cur().text)
			}
			p.next()
		}
		seq, err := p.parseExprSingle()
		if err != nil {
			return nil, err
		}
		binds = append(binds, VarBind{Prefix: prefix, Local: local, Seq: seq})
		if p.cur().kind != tComma {
			break
		}
		p.next()
	}
	return binds, nil
}

func (p *parser) parseFor() (Expr, error) {
	p.next() // 'for'
	binds, err := p.parseBindings(false)
	if err != nil {
		return nil, err
	}
	if !p.isKeyword("return") {
		return nil, fmt.Errorf("expected 'return' in for expression")
	}
	p.next()
	body, err := p.parseExprSingle()
	if err != nil {
		return nil, err
	}
	return &ForExpr{Binds: binds, Body: body}, nil
}

func (p *parser) parseLet() (Expr, error) {
	p.next() // 'let'
	binds, err := p.parseBindings(true)
	if err != nil {
		return nil, err
	}
	if !p.isKeyword("return") {
		return nil, fmt.Errorf("expected 'return' in let expression")
	}
	p.next()
	body, err := p.parseExprSingle()
	if err != nil {
		return nil, err
	}
	return &LetExpr{Binds: binds, Body: body}, nil
}

func (p *parser) parseQuantified() (Expr, error) {
	every := p.cur().text == "every"
	p.next() // some|every
	binds, err := p.parseBindings(false)
	if err != nil {
		return nil, err
	}
	if !p.isKeyword("satisfies") {
		return nil, fmt.Errorf("expected 'satisfies'")
	}
	p.next()
	test, err := p.parseExprSingle()
	if err != nil {
		return nil, err
	}
	return &QuantExpr{Every: every, Binds: binds, Satisfies: test}, nil
}

func (p *parser) parseIf() (Expr, error) {
	p.next() // 'if'
	if _, err := p.expect(tLParen); err != nil {
		return nil, err
	}
	cond, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tRParen); err != nil {
		return nil, err
	}
	if !p.isKeyword("then") {
		return nil, fmt.Errorf("expected 'then'")
	}
	p.next()
	thenE, err := p.parseExprSingle()
	if err != nil {
		return nil, err
	}
	if !p.isKeyword("else") {
		return nil, fmt.Errorf("expected 'else'")
	}
	p.next()
	elseE, err := p.parseExprSingle()
	if err != nil {
		return nil, err
	}
	return &IfExpr{Cond: cond, Then: thenE, Else: elseE}, nil
}

func (p *parser) parseOr() (Expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.cur().kind == tOp && p.cur().text == "or" {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Op: "or", L: left, R: right}
	}
	return left, nil
}

func (p *parser) parseAnd() (Expr, error) {
	left, err := p.parseComparison()
	if err != nil {
		return nil, err
	}
	for p.cur().kind == tOp && p.cur().text == "and" {
		p.next()
		right, err := p.parseComparison()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Op: "and", L: left, R: right}
	}
	return left, nil
}

// parseComparison[18] is non-chaining: at most one comparison operator.
func (p *parser) parseComparison() (Expr, error) {
	left, err := p.parseStringConcat()
	if err != nil {
		return nil, err
	}
	var kind compKind
	var op string
	switch {
	case p.cur().kind == tEq:
		kind, op = compGeneral, "="
	case p.cur().kind == tNeq:
		kind, op = compGeneral, "!="
	case p.cur().kind == tLt:
		kind, op = compGeneral, "<"
	case p.cur().kind == tLe:
		kind, op = compGeneral, "<="
	case p.cur().kind == tGt:
		kind, op = compGeneral, ">"
	case p.cur().kind == tGe:
		kind, op = compGeneral, ">="
	case p.cur().kind == tNodelt:
		kind, op = compNode, "<<"
	case p.cur().kind == tNodegt:
		kind, op = compNode, ">>"
	case p.cur().kind == tOp && isValueComp(p.cur().text):
		kind, op = compValue, p.cur().text
	case p.cur().kind == tOp && p.cur().text == "is":
		kind, op = compNode, "is"
	default:
		return left, nil
	}
	p.next()
	right, err := p.parseStringConcat()
	if err != nil {
		return nil, err
	}
	return &CompareExpr{Kind: kind, Op: op, L: left, R: right}, nil
}

func isValueComp(s string) bool {
	switch s {
	case "eq", "ne", "lt", "le", "gt", "ge":
		return true
	}
	return false
}

func (p *parser) parseStringConcat() (Expr, error) {
	left, err := p.parseRange()
	if err != nil {
		return nil, err
	}
	if p.cur().kind != tConcat {
		return left, nil
	}
	parts := []Expr{left}
	for p.cur().kind == tConcat {
		p.next()
		r, err := p.parseRange()
		if err != nil {
			return nil, err
		}
		parts = append(parts, r)
	}
	return &StringConcatExpr{Parts: parts}, nil
}

func (p *parser) parseRange() (Expr, error) {
	left, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	if p.cur().kind == tOp && p.cur().text == "to" {
		p.next()
		right, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		return &RangeExpr{From: left, To: right}, nil
	}
	return left, nil
}

func (p *parser) parseAdditive() (Expr, error) {
	left, err := p.parseMultiplicative()
	if err != nil {
		return nil, err
	}
	for p.cur().kind == tPlus || p.cur().kind == tMinus {
		op := p.next().text
		right, err := p.parseMultiplicative()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Op: op, L: left, R: right}
	}
	return left, nil
}

func (p *parser) parseMultiplicative() (Expr, error) {
	left, err := p.parseUnion()
	if err != nil {
		return nil, err
	}
	// A '*' right after a complete operand is multiplication even when the
	// lexer classed it as a wildcard star — after the '?' occurrence
	// indicator of a SequenceType no wildcard can follow
	// (K-SeqExprTreat-14: 3 treat as xs:integer ? * 3).
	for (p.cur().kind == tOp && (p.cur().text == "*" || p.cur().text == "div" || p.cur().text == "mod" || p.cur().text == "idiv")) || p.cur().kind == tStar {
		op := p.next().text
		right, err := p.parseUnion()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Op: op, L: left, R: right}
	}
	return left, nil
}

func (p *parser) parseUnion() (Expr, error) {
	left, err := p.parseIntersectExcept()
	if err != nil {
		return nil, err
	}
	for p.cur().kind == tPipe || (p.cur().kind == tOp && p.cur().text == "union") {
		p.next()
		right, err := p.parseIntersectExcept()
		if err != nil {
			return nil, err
		}
		left = &UnionExpr{L: left, R: right}
	}
	return left, nil
}

func (p *parser) parseIntersectExcept() (Expr, error) {
	left, err := p.parseInstanceof()
	if err != nil {
		return nil, err
	}
	for p.cur().kind == tOp && (p.cur().text == "intersect" || p.cur().text == "except") {
		op := p.next().text
		right, err := p.parseInstanceof()
		if err != nil {
			return nil, err
		}
		left = &IntersectExceptExpr{Op: op, L: left, R: right}
	}
	return left, nil
}

func (p *parser) parseInstanceof() (Expr, error) {
	left, err := p.parseTreat()
	if err != nil {
		return nil, err
	}
	if p.cur().kind == tOp && p.cur().text == "instance" {
		p.next()
		if !p.isKeyword("of") {
			return nil, fmt.Errorf("expected 'of' after 'instance'")
		}
		p.next()
		st, err := p.parseSequenceType()
		if err != nil {
			return nil, err
		}
		return &InstanceOfExpr{X: left, Type: st}, nil
	}
	return left, nil
}

func (p *parser) parseTreat() (Expr, error) {
	left, err := p.parseCastable()
	if err != nil {
		return nil, err
	}
	if p.cur().kind == tOp && p.cur().text == "treat" {
		p.next()
		if !p.isKeyword("as") {
			return nil, fmt.Errorf("expected 'as' after 'treat'")
		}
		p.next()
		st, err := p.parseSequenceType()
		if err != nil {
			return nil, err
		}
		return &TreatExpr{X: left, Type: st}, nil
	}
	return left, nil
}

func (p *parser) parseCastable() (Expr, error) {
	left, err := p.parseCast()
	if err != nil {
		return nil, err
	}
	if p.cur().kind == tOp && p.cur().text == "castable" {
		p.next()
		if !p.isKeyword("as") {
			return nil, fmt.Errorf("expected 'as' after 'castable'")
		}
		p.next()
		st, err := p.parseSingleType()
		if err != nil {
			return nil, err
		}
		return &CastExpr{X: left, Type: st, Castable: true}, nil
	}
	return left, nil
}

func (p *parser) parseCast() (Expr, error) {
	left, err := p.parseArrow()
	if err != nil {
		return nil, err
	}
	if p.cur().kind == tOp && p.cur().text == "cast" {
		p.next()
		if !p.isKeyword("as") {
			return nil, fmt.Errorf("expected 'as' after 'cast'")
		}
		p.next()
		st, err := p.parseSingleType()
		if err != nil {
			return nil, err
		}
		return &CastExpr{X: left, Type: st}, nil
	}
	return left, nil
}

func (p *parser) parseArrow() (Expr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	if p.cur().kind != tArrow {
		return left, nil
	}
	ar := &ArrowExpr{Base: left}
	for p.cur().kind == tArrow {
		p.next()
		var step ArrowStep
		switch p.cur().kind {
		case tName:
			name := p.next().text
			if uri, local, ok := parseBracedName(name); ok {
				step.Pre, step.Name = bracedPrefixForURI(uri), local
			} else {
				step.Pre, step.Name = splitQName(name)
			}
		case tDollar:
			p.next()
			vn, err := p.expect(tName)
			if err != nil {
				return nil, err
			}
			pre, loc := splitVarName(vn.text)
			step.Spec = &VarRef{Prefix: pre, Local: loc}
		case tLParen:
			p.next()
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(tRParen); err != nil {
				return nil, err
			}
			step.Spec = e
		default:
			return nil, fmt.Errorf("expected function specifier after '=>'")
		}
		args, err := p.parseArgList()
		if err != nil {
			return nil, err
		}
		step.Args = args
		ar.Steps = append(ar.Steps, step)
	}
	return ar, nil
}

func (p *parser) parseUnary() (Expr, error) {
	neg, signed := false, false
	for p.cur().kind == tMinus || p.cur().kind == tPlus {
		if p.cur().kind == tMinus {
			neg = !neg
		}
		signed = true
		p.next()
	}
	x, err := p.parseSimpleMap()
	if err != nil {
		return nil, err
	}
	if signed {
		return &UnaryExpr{X: x, Plus: !neg}, nil
	}
	return x, nil
}

func (p *parser) parseSimpleMap() (Expr, error) {
	left, err := p.parsePath()
	if err != nil {
		return nil, err
	}
	if p.cur().kind != tBang {
		return left, nil
	}
	steps := []Expr{left}
	for p.cur().kind == tBang {
		p.next()
		r, err := p.parsePath()
		if err != nil {
			return nil, err
		}
		steps = append(steps, r)
	}
	return &SimpleMapExpr{Steps: steps}, nil
}

// parsePath handles both location paths and filter-expression-rooted paths.
func (p *parser) parsePath() (Expr, error) {
	if p.startsWithPrimary() {
		filter, err := p.parseFilter()
		if err != nil {
			return nil, err
		}
		// Optional trailing relative path.
		if p.cur().kind == tSlash || p.cur().kind == tSlashSlash {
			path := &PathExpr{Start: filter}
			if err := p.parseRelativeInto(path); err != nil {
				return nil, err
			}
			return path, nil
		}
		return filter, nil
	}
	return p.parseLocationPath()
}

func (p *parser) parseLocationPath() (Expr, error) {
	path := &PathExpr{}
	switch p.cur().kind {
	case tSlash:
		p.next()
		path.Absolute = true
		// A leading "/" is followed by a RelativePathExpr whenever the next
		// token can start one — an axis step, or a primary such as a literal
		// or array constructor (PathExpr-24: /42, PathExpr-17: /[bid, bid]).
		if p.startsStep() || p.startsWithPrimary() {
			if err := p.parseSteps(path); err != nil {
				return nil, err
			}
		}
	case tSlashSlash:
		p.next()
		path.Absolute = true
		path.Steps = append(path.Steps, descendantOrSelfStep())
		if err := p.parseSteps(path); err != nil {
			return nil, err
		}
	default:
		if err := p.parseSteps(path); err != nil {
			return nil, err
		}
	}
	return path, nil
}

// parseRelativeInto consumes '/'/'//' separators and following steps into path.
func (p *parser) parseRelativeInto(path *PathExpr) error {
	for {
		switch p.cur().kind {
		case tSlash:
			p.next()
			step, err := p.parseStep()
			if err != nil {
				return err
			}
			path.Steps = append(path.Steps, step)
		case tSlashSlash:
			p.next()
			path.Steps = append(path.Steps, descendantOrSelfStep())
			step, err := p.parseStep()
			if err != nil {
				return err
			}
			path.Steps = append(path.Steps, step)
		default:
			return nil
		}
	}
}

func (p *parser) parseSteps(path *PathExpr) error {
	step, err := p.parseStep()
	if err != nil {
		return err
	}
	path.Steps = append(path.Steps, step)
	return p.parseRelativeInto(path)
}

func descendantOrSelfStep() *Step {
	return &Step{Axis: "descendant-or-self", Test: NodeTest{Kind: testNode}}
}

func (p *parser) parseStep() (*Step, error) {
	// XPath 3.1 §3.3.5: ".." is an AbbrevReverseStep, and an AxisStep is a
	// step FOLLOWED BY A PREDICATE LIST — so "..[@x]" is ordinary syntax, not
	// a trailing token (sf-current-100's match="AUTHOR[..[@CAT = current()/../@CAT]]").
	// "." reaches here only inside a path ("a/.[b]"); on its own it is a
	// primary expression and parseFilter already applies its predicates.
	var abbrev *Step
	switch p.cur().kind {
	case tDot:
		p.next()
		abbrev = &Step{Axis: "self", Test: NodeTest{Kind: testNode}}
	case tDotDot:
		p.next()
		abbrev = &Step{Axis: "parent", Test: NodeTest{Kind: testNode}}
	}
	if abbrev != nil {
		for p.cur().kind == tLBracket {
			pred, err := p.parsePredicate()
			if err != nil {
				return nil, err
			}
			abbrev.Preds = append(abbrev.Preds, pred)
		}
		return abbrev, nil
	}

	// A PostfixExpr step (e.g. a function call xs:decimal(.) or $f(.) used as a
	// relative-path step — XPath 3.0 StepExpr = PostfixExpr | AxisStep).
	if p.startsWithPrimary() {
		e, err := p.parseFilter()
		if err != nil {
			return nil, err
		}
		return &Step{Postfix: e}, nil
	}

	step := &Step{Axis: "child"}
	explicitAxis := true
	if p.cur().kind == tAt {
		p.next()
		step.Axis = "attribute"
	} else if p.cur().kind == tName && p.peekKind(1) == tColonCol {
		step.Axis = p.next().text
		p.next() // '::'
		if !knownAxis(step.Axis) {
			return nil, fmt.Errorf("err:XPST0003: unknown axis %q", step.Axis)
		}
	} else {
		explicitAxis = false
	}
	step.AxisExplicit = explicitAxis

	test, err := p.parseNodeTest()
	if err != nil {
		return nil, err
	}
	step.Test = test
	// XPath 3.0 §3.3.5 abbreviated syntax: with no axis, an attribute()/
	// schema-attribute() test selects on the attribute axis and a
	// namespace-node() test on the namespace axis (Axes113: /*/namespace-node()).
	if !explicitAxis {
		switch test.Kind {
		case testAttribute, testSchemaAttr:
			step.Axis = "attribute"
		case testNamespace:
			step.Axis = "namespace"
		}
	}

	for p.cur().kind == tLBracket {
		pred, err := p.parsePredicate()
		if err != nil {
			return nil, err
		}
		step.Preds = append(step.Preds, pred)
	}
	return step, nil
}

func (p *parser) isStarTok() bool {
	return p.cur().kind == tStar || (p.cur().kind == tOp && p.cur().text == "*")
}

func (p *parser) parseNodeTest() (NodeTest, error) {
	switch {
	case p.isStarTok():
		p.next()
		// "*:local"
		if p.cur().kind == tColon {
			p.next()
			ln, err := p.expect(tName)
			if err != nil {
				return NodeTest{}, err
			}
			return NodeTest{Kind: testName, WildNS: true, Local: ln.text}, nil
		}
		return NodeTest{Kind: testName, AnyName: true}, nil

	case p.cur().kind == tName:
		name := p.cur().text
		// KindTest?
		if isKindTestName(name) && p.peekKind(1) == tLParen {
			return p.parseKindTest()
		}
		p.next()
		// Q{uri}local / Q{uri}*
		if uri, local, ok := parseBracedName(name); ok {
			if local == "*" || p.isStarTok() {
				if local == "" {
					p.next()
				}
				return NodeTest{Kind: testName, URI: uri, Braced: true, AnyName: true}, nil
			}
			return NodeTest{Kind: testName, URI: uri, Braced: true, Local: local}, nil
		}
		// "prefix:*"  (lexed as a name ending in ':' followed by '*')
		if strings.HasSuffix(name, ":") && p.isStarTok() {
			p.next()
			return NodeTest{Kind: testName, Prefix: strings.TrimSuffix(name, ":"), AnyName: true}, nil
		}
		prefix, local := splitQName(name)
		if local == "*" {
			return NodeTest{Kind: testName, Prefix: prefix, AnyName: true}, nil
		}
		return NodeTest{Kind: testName, Prefix: prefix, Local: local}, nil
	}
	return NodeTest{}, fmt.Errorf("expected node test, got %q", p.cur().text)
}

func isKindTestName(name string) bool {
	switch name {
	case "node", "text", "comment", "processing-instruction", "element",
		"attribute", "document-node", "namespace-node", "schema-element",
		"schema-attribute":
		return true
	}
	return false
}

// parseKindTest parses a KindTest (current token is the kind keyword, next is "(").
func (p *parser) parseKindTest() (NodeTest, error) {
	name := p.next().text
	if _, err := p.expect(tLParen); err != nil {
		return NodeTest{}, err
	}
	nt := NodeTest{}
	switch name {
	case "node":
		nt.Kind = testNode
	case "text":
		nt.Kind = testText
	case "comment":
		nt.Kind = testComment
	case "namespace-node":
		nt.Kind = testNamespace
	case "processing-instruction":
		nt.Kind = testPI
		if p.cur().kind == tLiteral || p.cur().kind == tName {
			isName := p.cur().kind == tName
			nt.PITarget = p.next().text
			// The unquoted form of the target is an NCName — a PI target can
			// never contain a colon (XML Namespaces §6), so a prefixed name
			// here is a syntax error, not a test that simply never matches
			// (error-0340c).
			if isName && strings.Contains(nt.PITarget, ":") {
				return NodeTest{}, fmt.Errorf("err:XPST0003: %q is not a valid processing-instruction target", nt.PITarget)
			}
		}
	case "element":
		nt.Kind = testElement
		if !p.isRParen() {
			if err := p.parseElemAttrTestBody(&nt); err != nil {
				return NodeTest{}, err
			}
		}
	case "attribute":
		nt.Kind = testAttribute
		if !p.isRParen() {
			if err := p.parseElemAttrTestBody(&nt); err != nil {
				return NodeTest{}, err
			}
		}
	case "schema-element", "schema-attribute":
		// With no schema in scope no element/attribute declaration exists, so
		// a schema-element()/schema-attribute() test names an undeclared
		// component (XPST0008) — K2-NodeTest-8/9, K2-NameTest-37/38. A
		// schema-aware parse instead resolves the declaration and, only when
		// it is genuinely absent from the imported components, raises the
		// same static error.
		if err := p.parseSchemaCompTest(&nt, name); err != nil {
			return NodeTest{}, err
		}
	case "document-node":
		nt.Kind = testDocument
		if !p.isRParen() {
			// A document-node() test's argument may ONLY be element() or
			// schema-element() (XPST0003) — K2-NodeTest-16/17.
			if p.cur().kind != tName || (p.cur().text != "element" && p.cur().text != "schema-element") {
				return NodeTest{}, fmt.Errorf("err:XPST0003: document-node() takes an element test only")
			}
			inner, err := p.parseKindTest()
			if err != nil {
				return NodeTest{}, err
			}
			nt.Inner = &inner
		}
	}
	if _, err := p.expect(tRParen); err != nil {
		return NodeTest{}, err
	}
	return nt, nil
}

func (p *parser) isRParen() bool { return p.cur().kind == tRParen }

// parseElemAttrTestBody parses (NameOrWildcard ("," TypeName "?"?)?).
func (p *parser) parseElemAttrTestBody(nt *NodeTest) error {
	if p.isStarTok() {
		p.next()
		nt.AnyName = true
	} else if p.cur().kind == tName {
		name := p.next().text
		if uri, local, ok := parseBracedName(name); ok {
			// element(Q{uri}local): the URI is explicit.
			nt.URI, nt.Braced, nt.Local = uri, true, local
		} else {
			prefix, local := splitQName(name)
			nt.Prefix, nt.Local = prefix, local
		}
	}
	if p.cur().kind == tComma {
		p.next()
		if p.cur().kind == tName {
			nt.TypeName = p.next().text
			// A user-defined type is resolved to its identity HERE, once —
			// see schema_names.go. A name the lookup does not know (every
			// name at all when no schema is imported) leaves SchemaType nil
			// and is judged by the built-in-only reading in kindTestTypeOK,
			// which answers false for anything it cannot account for.
			if p.schema != nil {
				if st, ok := p.schema.LookupSchemaType(nt.TypeName); ok {
					nt.SchemaType = &st
				}
			}
		}
		if p.cur().kind == tQuestion {
			p.next()
			nt.Nillable = true
		}
	}
	return nil
}

// parseSchemaCompTest parses the argument of schema-element(N) /
// schema-attribute(N) and resolves N against the imported schema components.
// The test carries the declaration's EXPANDED name (so prefix resolution is
// settled here, not repeated per node) and its declared type.
func (p *parser) parseSchemaCompTest(nt *NodeTest, name string) error {
	if p.schema == nil {
		return fmt.Errorf("err:XPST0008: %s is not schema-declared", name)
	}
	if p.cur().kind != tName {
		return fmt.Errorf("err:XPST0003: %s requires an element/attribute declaration name", name)
	}
	lexical := p.next().text
	var (
		decl xmltree.Name
		typ  xmltree.SchemaTypeName
		ok   bool
	)
	if name == "schema-element" {
		nt.Kind = testSchemaElement
		decl, typ, ok = p.schema.LookupSchemaElement(lexical)
	} else {
		nt.Kind = testSchemaAttr
		decl, typ, ok = p.schema.LookupSchemaAttribute(lexical)
	}
	if !ok {
		return fmt.Errorf("err:XPST0008: %s(%s) names no declaration in the imported schema components", name, lexical)
	}
	// Braced records that URI is authoritative even when empty — a no-namespace
	// declaration must not silently pick up the default element namespace.
	nt.URI, nt.Braced, nt.Local = decl.Space, true, decl.Local
	nt.SchemaType = &typ
	return nil
}

// parseBracedName splits a Q{uri}local token into (uri, local).
func parseBracedName(s string) (uri, local string, ok bool) {
	if len(s) < 2 || s[0] != 'Q' || s[1] != '{' {
		return "", "", false
	}
	end := indexByteFrom(s, '}', 2)
	if end < 0 {
		return "", "", false
	}
	// The URI in Q{...} has leading/trailing whitespace stripped and
	// internal whitespace collapsed per the BracedURILiteral rules
	// (eqname-020/021: Q{ ...math } pi() resolves to math:pi()).
	return strings.Join(strings.Fields(s[2:end]), " "), s[end+1:], true
}

func (p *parser) parsePredicate() (Expr, error) {
	if _, err := p.expect(tLBracket); err != nil {
		return nil, err
	}
	e, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tRBracket); err != nil {
		return nil, err
	}
	return e, nil
}

// parseFilter := PrimaryExpr (Predicate | Lookup | ArgumentList)*
func (p *parser) parseFilter() (Expr, error) {
	var prim Expr
	if p.cur().kind == tQuestion {
		// UnaryLookup "?key" — operates on the context item (".?key").
		prim = &ContextItemExpr{}
	} else {
		var err error
		prim, err = p.parsePrimary()
		if err != nil {
			return nil, err
		}
	}
	for {
		switch p.cur().kind {
		case tLBracket:
			pred, err := p.parsePredicate()
			if err != nil {
				return nil, err
			}
			prim = &FilterExpr{Primary: prim, Preds: []Expr{pred}}
		case tQuestion:
			p.next()
			lk := &LookupExpr{Base: prim}
			switch p.cur().kind {
			case tStar:
				p.next()
				lk.Wild = true
			case tName:
				// The NCName key specifier must be an NCName — a prefixed QName
				// or a Q{uri}local literal is a grammar error (Lookup-156/157).
				name := p.cur().text
				if strings.ContainsAny(name, ":{}") {
					return nil, fmt.Errorf("err:XPST0003: lookup key must be an NCName, got %q", name)
				}
				lk.Key = &LiteralExpr{Val: p.next().text}
			case tNumber:
				// KeySpecifier admits only an IntegerLiteral — ?1.0 / ?2.2 is
				// a grammar error (Lookup-018, UnaryLookup-018/047).
				if strings.ContainsAny(p.cur().text, ".eE") {
					return nil, fmt.Errorf("err:XPST0003: lookup key must be an integer literal, got %q", p.cur().text)
				}
				f, _ := strconv.ParseFloat(p.next().text, 64)
				lk.Key = &LiteralExpr{Val: f}
			case tLParen:
				p.next()
				// A ParenthesizedExpr key specifier may be empty — "?()" looks
				// up an empty key sequence, yielding () (Lookup-025/046/123/146).
				if p.cur().kind == tRParen {
					p.next()
					lk.Key = &SequenceExpr{}
					prim = lk
					continue
				}
				e, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				if _, err := p.expect(tRParen); err != nil {
					return nil, err
				}
				lk.Key = e
			default:
				return nil, fmt.Errorf("expected lookup key after '?', got %q", p.cur().text)
			}
			prim = lk
		case tLParen:
			args, err := p.parseArgList()
			if err != nil {
				return nil, err
			}
			prim = &DynCall{Base: prim, Args: args}
		default:
			return prim, nil
		}
	}
}

// parseArgList parses a parenthesized, comma-separated ExprSingle list.
func (p *parser) parseArgList() ([]Expr, error) {
	if _, err := p.expect(tLParen); err != nil {
		return nil, err
	}
	var args []Expr
	if p.cur().kind != tRParen {
		for {
			// ArgumentPlaceholder "?" (partial application)
			if p.cur().kind == tQuestion && (p.peekKind(1) == tComma || p.peekKind(1) == tRParen) {
				p.next()
				args = append(args, &Placeholder{})
			} else {
				a, err := p.parseExprSingle()
				if err != nil {
					return nil, err
				}
				args = append(args, a)
			}
			if p.cur().kind != tComma {
				break
			}
			p.next()
		}
	}
	if _, err := p.expect(tRParen); err != nil {
		return nil, err
	}
	return args, nil
}

// parseMapConstructor parses: map { key : value (, ...)? }
func (p *parser) parseMapConstructor() (Expr, error) {
	p.next() // 'map'
	if _, err := p.expect(tLBrace); err != nil {
		return nil, err
	}
	m := &MapExpr{}
	if p.cur().kind != tRBrace {
		for {
			k, err := p.parseExprSingle()
			if err != nil {
				return nil, err
			}
			if p.cur().kind != tColon {
				// ':' is lexed within names; map keys use a literal ':' token.
				return nil, fmt.Errorf("expected ':' in map entry, got %q", p.cur().text)
			}
			p.next()
			v, err := p.parseExprSingle()
			if err != nil {
				return nil, err
			}
			m.Keys = append(m.Keys, k)
			m.Vals = append(m.Vals, v)
			if p.cur().kind != tComma {
				break
			}
			p.next()
		}
	}
	if _, err := p.expect(tRBrace); err != nil {
		return nil, err
	}
	return m, nil
}

// parseSquareArray parses: [ expr (, expr)? ]
func (p *parser) parseSquareArray() (Expr, error) {
	p.next() // '['
	a := &ArrayExpr{}
	if p.cur().kind != tRBracket {
		for {
			e, err := p.parseExprSingle()
			if err != nil {
				return nil, err
			}
			a.Items = append(a.Items, e)
			if p.cur().kind != tComma {
				break
			}
			p.next()
		}
	}
	if _, err := p.expect(tRBracket); err != nil {
		return nil, err
	}
	return a, nil
}

// parseCurlyArray parses: array { expr } (members are the flattened sequence)
func (p *parser) parseCurlyArray() (Expr, error) {
	p.next() // 'array'
	if _, err := p.expect(tLBrace); err != nil {
		return nil, err
	}
	a := &ArrayExpr{Curly: true}
	if p.cur().kind != tRBrace {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		a.Items = append(a.Items, e)
	}
	if _, err := p.expect(tRBrace); err != nil {
		return nil, err
	}
	return a, nil
}

// parseInlineFunction parses: function ( $p (, $p)? ) { body }
func (p *parser) parseInlineFunction() (Expr, error) {
	p.next() // 'function'
	if _, err := p.expect(tLParen); err != nil {
		return nil, err
	}
	fn := &InlineFunc{}
	if p.cur().kind != tRParen {
		for {
			if _, err := p.expect(tDollar); err != nil {
				return nil, err
			}
			name, err := p.expect(tName)
			if err != nil {
				return nil, err
			}
			// optional "as type" — capture it for call-time arg checking.
			var pt *SeqType
			if p.isKeyword("as") {
				p.next()
				st, err := p.parseSequenceType()
				if err != nil {
					return nil, err
				}
				pt = st
			}
			prefix, local := splitVarName(name.text)
			// Two parameters may not share an (expanded) name — $a twice, or
			// $Q{}a beside $a (inline-function-12a/15, eqname-913: XQST0039).
			for _, prev := range fn.Params {
				if prev.Local == local && varPrefixKey(prev.Prefix) == varPrefixKey(prefix) {
					return nil, fmt.Errorf("err:XQST0039: duplicate parameter name $%s", local)
				}
			}
			fn.Params = append(fn.Params, VarBind{Prefix: prefix, Local: local})
			fn.ParamTypes = append(fn.ParamTypes, pt)
			if p.cur().kind != tComma {
				break
			}
			p.next()
		}
	}
	if _, err := p.expect(tRParen); err != nil {
		return nil, err
	}
	// optional "as returnType" — captured for result checking at call time.
	if p.isKeyword("as") {
		p.next()
		rt, err := p.parseSequenceType()
		if err != nil {
			return nil, err
		}
		fn.RetType = rt
	}
	if _, err := p.expect(tLBrace); err != nil {
		return nil, err
	}
	// The body is an EnclosedExpr, whose Expr is optional: an empty body
	// {} (or one holding only a comment) yields the empty sequence
	// (FunctionCall-045/046/047, inline-fn-007).
	if p.cur().kind == tRBrace {
		p.next()
		fn.Body = &SequenceExpr{}
		return fn, nil
	}
	body, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tRBrace); err != nil {
		return nil, err
	}
	fn.Body = body
	return fn, nil
}

func (p *parser) parsePrimary() (Expr, error) {
	// Map / array / inline-function constructors.
	if p.cur().kind == tName {
		switch p.cur().text {
		case "map":
			if p.peekKind(1) == tLBrace {
				return p.parseMapConstructor()
			}
		case "array":
			if p.peekKind(1) == tLBrace {
				return p.parseCurlyArray()
			}
		case "function":
			if p.peekKind(1) == tLParen {
				return p.parseInlineFunction()
			}
		}
	}
	if p.cur().kind == tLBracket {
		return p.parseSquareArray()
	}

	switch p.cur().kind {
	case tDot:
		p.next()
		return &ContextItemExpr{}, nil
	case tLParen:
		p.next()
		if p.cur().kind == tRParen {
			p.next()
			return &SequenceExpr{}, nil
		}
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tRParen); err != nil {
			return nil, err
		}
		return e, nil
	case tLiteral:
		return &LiteralExpr{Val: p.next().text}, nil
	case tNumber:
		return &LiteralExpr{Val: numericLiteral(p.next().text)}, nil
	case tDollar:
		p.next()
		name, err := p.expect(tName)
		if err != nil {
			return nil, err
		}
		prefix, local := splitVarName(name.text)
		return &VarRef{Prefix: prefix, Local: local}, nil
	case tName:
		name := p.next().text
		// NamedFunctionRef: EQName "#" IntegerLiteral
		if p.cur().kind == tHash {
			p.next()
			arity, err := p.expect(tNumber)
			if err != nil {
				return nil, err
			}
			n, err := strconv.Atoi(arity.text)
			if err != nil {
				// An arity beyond the integer range names no function
				// (fn-function-arity-017: concat#2^128 is FOAR0002).
				return nil, fmt.Errorf("err:FOAR0002: function arity %s is out of range", arity.text)
			}
			if uri, local, ok := parseBracedName(name); ok {
				return &NamedFuncRef{URI: uri, Local: local, Arity: n}, nil
			}
			prefix, local := splitQName(name)
			if prefix == "" && reservedFunctionNames[local] {
				return nil, fmt.Errorf("err:XPST0017: %q is a reserved function name", local)
			}
			return &NamedFuncRef{Prefix: prefix, Local: local, Arity: n}, nil
		}
		args, err := p.parseArgList()
		if err != nil {
			return nil, err
		}
		if uri, local, ok := parseBracedName(name); ok {
			return &FuncCall{Prefix: bracedPrefixForURI(uri), Local: local, Args: args}, nil
		}
		prefix, local := splitQName(name)
		return &FuncCall{Prefix: prefix, Local: local, Args: args}, nil
	}
	return nil, fmt.Errorf("expected primary expression, got %q", p.cur().text)
}

// numericLiteral classifies a numeric literal token into a typed atomic.
func numericLiteral(txt string) *Atomic {
	if strings.ContainsAny(txt, "eE") {
		f, _ := strconv.ParseFloat(txt, 64)
		return NewDouble(f)
	}
	if strings.Contains(txt, ".") {
		if a, ok := NewDecimalFromString(txt); ok {
			return a
		}
		f, _ := strconv.ParseFloat(txt, 64)
		return NewDouble(f)
	}
	z, ok := new(big.Int).SetString(txt, 10)
	if !ok {
		f, _ := strconv.ParseFloat(txt, 64)
		return NewDouble(f)
	}
	return NewIntegerBig(z)
}

// startsWithPrimary reports whether the upcoming tokens begin a primary
// expression (filter), as opposed to a location-path step.
func (p *parser) startsWithPrimary() bool {
	switch p.cur().kind {
	case tDollar, tLiteral, tNumber, tLParen, tLBracket, tDot:
		return true
	case tQuestion: // UnaryLookup "?key" == ".?key"
		return true
	case tName:
		// NamedFunctionRef EQName#int.
		if p.peekKind(1) == tHash {
			return true
		}
		// map{…} / array{…|[…]} / function(…){…} constructors.
		switch p.cur().text {
		case "map":
			return p.peekKind(1) == tLBrace
		case "array":
			return p.peekKind(1) == tLBrace
		case "function":
			return p.peekKind(1) == tLParen
		}
		// A function call: Name immediately followed by '(', and the name is
		// not a KindTest.
		if p.peekKind(1) == tLParen {
			if isKindTestName(p.cur().text) {
				return false // KindTest in a step
			}
			return true
		}
		return false
	}
	return false
}

func (p *parser) startsStep() bool {
	switch p.cur().kind {
	case tName, tStar, tAt, tDot, tDotDot:
		return true
	}
	return false
}

func (p *parser) peekKind(n int) tokKind {
	if p.pos+n < len(p.toks) {
		return p.toks[p.pos+n].kind
	}
	return tEOF
}

func splitQName(s string) (prefix, local string) {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

// splitVarName splits a variable name token. A braced EQName $Q{uri}local is
// carried as the pseudo-prefix "Q{uri}" (URI whitespace already collapsed by
// parseBracedName) so that varKey binds it by expanded name — $Q{ urn:x }v and
// $Q{urn:x}v are the same variable, and $Q{}v is plain $v (eqname-024/025/
// 032/033).
func splitVarName(s string) (prefix, local string) {
	if uri, l, ok := parseBracedName(s); ok {
		return "Q{" + uri + "}", l
	}
	return splitQName(s)
}

// reservedFunctionNames cannot be used as an unprefixed function name (XPST0017).
var reservedFunctionNames = map[string]bool{
	"array": true, "attribute": true, "comment": true, "document-node": true,
	"element": true, "empty-sequence": true, "function": true, "if": true,
	"item": true, "map": true, "namespace-node": true, "node": true,
	"processing-instruction": true, "schema-attribute": true,
	"schema-element": true, "switch": true, "text": true, "typeswitch": true,
}

// bracedPrefixForURI maps a Q{uri}local function namespace to the prefix token
// dispatchFunc keys on (fn/math/map/array/xs); unknown URIs pass through.
func bracedPrefixForURI(uri string) string {
	if tok, ok := dispatchTokenForNS(uri); ok {
		return tok
	}
	return uri
}

// knownAxis reports whether name is one of the 13 XPath axes (an unknown or
// misspelled axis is a static error — K2-Axes-29 preceding-or-ancestor,
// K2-Axes-77 preceeding).
func knownAxis(name string) bool {
	switch name {
	case "child", "descendant", "attribute", "self", "descendant-or-self",
		"following-sibling", "following", "namespace", "parent", "ancestor",
		"preceding-sibling", "preceding", "ancestor-or-self":
		return true
	}
	return false
}
