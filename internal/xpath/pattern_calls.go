package xpath

import "fmt"

// UsesFunction reports whether the pattern's expression tree contains an
// unprefixed call to the named function anywhere, including inside nested
// predicates. XSLT uses this for XTSE1060/XTSE1070: current-group() and
// current-grouping-key() have no defined value in the context a pattern is
// evaluated in, so referencing them inside any match pattern is a static
// error, regardless of whether the pattern ever actually matches a node.
func (p *Pattern) UsesFunction(name string) bool {
	if p == nil {
		return false
	}
	for _, alt := range p.alts {
		if exprUsesFunc(alt, name) {
			return true
		}
	}
	for _, alt := range p.exprAlts {
		if exprUsesFunc(alt, name) {
			return true
		}
	}
	return false
}

// exprUsesFunc reports whether the expression tree contains an unprefixed call
// to name.
func exprUsesFunc(e Expr, name string) bool {
	return walkFuncCalls(e, func(c *FuncCall) bool { return c.Prefix == "" && c.Local == name })
}

// walkFuncCalls visits every function call in an XPath expression tree, stopping
// at (and reporting) the first one for which visit returns true. It covers every
// Expr node kind that can carry a sub-expression.
func walkFuncCalls(e Expr, visit func(*FuncCall) bool) bool {
	if e == nil {
		return false
	}
	switch v := e.(type) {
	case *FuncCall:
		if visit(v) {
			return true
		}
		for _, a := range v.Args {
			if walkFuncCalls(a, visit) {
				return true
			}
		}
	case *BinaryExpr:
		return walkFuncCalls(v.L, visit) || walkFuncCalls(v.R, visit)
	case *UnaryExpr:
		return walkFuncCalls(v.X, visit)
	case *UnionExpr:
		return walkFuncCalls(v.L, visit) || walkFuncCalls(v.R, visit)
	case *PathExpr:
		if walkFuncCalls(v.Start, visit) {
			return true
		}
		for _, s := range v.Steps {
			if s == nil {
				continue
			}
			for _, pr := range s.Preds {
				if walkFuncCalls(pr, visit) {
					return true
				}
			}
			if walkFuncCalls(s.Postfix, visit) {
				return true
			}
		}
	case *FilterExpr:
		if walkFuncCalls(v.Primary, visit) {
			return true
		}
		for _, pr := range v.Preds {
			if walkFuncCalls(pr, visit) {
				return true
			}
		}
	case *SequenceExpr:
		for _, it := range v.Items {
			if walkFuncCalls(it, visit) {
				return true
			}
		}
	case *RangeExpr:
		return walkFuncCalls(v.From, visit) || walkFuncCalls(v.To, visit)
	case *ForExpr:
		for _, b := range v.Binds {
			if walkFuncCalls(b.Seq, visit) {
				return true
			}
		}
		return walkFuncCalls(v.Body, visit)
	case *LetExpr:
		for _, b := range v.Binds {
			if walkFuncCalls(b.Seq, visit) {
				return true
			}
		}
		return walkFuncCalls(v.Body, visit)
	case *QuantExpr:
		for _, b := range v.Binds {
			if walkFuncCalls(b.Seq, visit) {
				return true
			}
		}
		return walkFuncCalls(v.Satisfies, visit)
	case *IfExpr:
		return walkFuncCalls(v.Cond, visit) || walkFuncCalls(v.Then, visit) || walkFuncCalls(v.Else, visit)
	case *MapExpr:
		for i := range v.Keys {
			if walkFuncCalls(v.Keys[i], visit) {
				return true
			}
			if i < len(v.Vals) && walkFuncCalls(v.Vals[i], visit) {
				return true
			}
		}
	case *ArrayExpr:
		for _, it := range v.Items {
			if walkFuncCalls(it, visit) {
				return true
			}
		}
	case *InlineFunc:
		return walkFuncCalls(v.Body, visit)
	case *LookupExpr:
		return walkFuncCalls(v.Base, visit) || walkFuncCalls(v.Key, visit)
	case *DynCall:
		if walkFuncCalls(v.Base, visit) {
			return true
		}
		for _, a := range v.Args {
			if walkFuncCalls(a, visit) {
				return true
			}
		}
	case *CompareExpr:
		return walkFuncCalls(v.L, visit) || walkFuncCalls(v.R, visit)
	case *StringConcatExpr:
		for _, pt := range v.Parts {
			if walkFuncCalls(pt, visit) {
				return true
			}
		}
	case *IntersectExceptExpr:
		return walkFuncCalls(v.L, visit) || walkFuncCalls(v.R, visit)
	case *InstanceOfExpr:
		return walkFuncCalls(v.X, visit)
	case *TreatExpr:
		return walkFuncCalls(v.X, visit)
	case *CastExpr:
		return walkFuncCalls(v.X, visit)
	case *ArrowExpr:
		if walkFuncCalls(v.Base, visit) {
			return true
		}
		for _, s := range v.Steps {
			if walkFuncCalls(s.Spec, visit) {
				return true
			}
			for _, a := range s.Args {
				if walkFuncCalls(a, visit) {
					return true
				}
			}
		}
	case *SimpleMapExpr:
		for _, s := range v.Steps {
			if walkFuncCalls(s, visit) {
				return true
			}
		}
	}
	return false
}

// patternOuterFunctions is the closed set of functions XSLT's Pattern grammar
// admits as the leading primary of a path pattern (XSLT 2.0 IdKeyPattern,
// XSLT 3.0 FunctionCallP/OuterFunctionName). Anything else there does not match
// the Pattern production.
var patternOuterFunctions = map[string]bool{
	"doc": true, "id": true, "element-with-id": true, "key": true, "root": true,
}

// CheckGrammar reports XTSE0340 when the pattern, though a legal XPath
// expression, does not match XSLT's Pattern production. Two restrictions are
// checked here, both concerning function calls:
//
//   - a function call may only be the LEADING primary of a path pattern, never
//     a later step (match-080: "/key('k', 42)//a");
//   - the function called there must be one of the OuterFunctionNames
//     (match-077: "copy-of($x)//a") and each of its arguments must be a literal
//     or a variable reference (match-079: "key('k', 40+2)//a").
//
// Predicates are deliberately not walked: a PredicateList in a pattern may
// contain an arbitrary XPath expression.
func (p *Pattern) CheckGrammar() error {
	if p == nil {
		return nil
	}
	for _, alt := range p.alts {
		if err := checkPatternOuterCall(alt.Start); err != nil {
			return err
		}
		for _, s := range alt.Steps {
			if s == nil || s.Postfix == nil {
				continue
			}
			if _, isCall := s.Postfix.(*FuncCall); isCall {
				return fmt.Errorf("err:XTSE0340: a function call may only appear at the start of a pattern")
			}
		}
	}
	for _, ex := range p.exprAlts {
		if _, isCall := ex.(*FuncCall); isCall {
			if err := checkPatternOuterCall(ex); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkPatternOuterCall(e Expr) error {
	call, ok := e.(*FuncCall)
	if !ok {
		return nil
	}
	if call.Prefix != "" && call.Prefix != "fn" {
		return fmt.Errorf("err:XTSE0340: %s:%s is not allowed at the start of a pattern", call.Prefix, call.Local)
	}
	if !patternOuterFunctions[call.Local] {
		return fmt.Errorf("err:XTSE0340: %s() is not allowed at the start of a pattern", call.Local)
	}
	for _, a := range call.Args {
		switch a.(type) {
		case *LiteralExpr, *VarRef, *ContextItemExpr:
		default:
			return fmt.Errorf("err:XTSE0340: arguments of %s() in a pattern must be literals or variable references", call.Local)
		}
	}
	return nil
}

// PatternCall names one function call appearing anywhere in a pattern,
// including inside its predicates.
type PatternCall struct {
	Prefix string
	Local  string
	Arity  int
}

// FunctionCalls returns every function call the pattern makes. The XSLT
// compiler uses it to check statically that each one is actually available
// (XPST0017) — a pattern's predicate is never evaluated eagerly, so an
// undeclared function there would otherwise go unnoticed.
func (p *Pattern) FunctionCalls() []PatternCall {
	if p == nil {
		return nil
	}
	var out []PatternCall
	collect := func(c *FuncCall) bool {
		out = append(out, PatternCall{Prefix: c.Prefix, Local: c.Local, Arity: len(c.Args)})
		return false // keep walking
	}
	for _, alt := range p.alts {
		walkFuncCalls(alt, collect)
	}
	for _, alt := range p.exprAlts {
		walkFuncCalls(alt, collect)
	}
	return out
}
