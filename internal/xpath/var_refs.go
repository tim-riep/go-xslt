package xpath

// UsesVariable reports whether the parsed expression contains a reference to
// the variable named (prefix, local) that is NOT shadowed by an enclosing
// binding of the same name (a for/let/some/every clause or an inline-function
// parameter).
//
// XSLT uses this for XPST0008: a global variable is out of scope within its own
// declaration, so a $self reference anywhere in its select expression — even
// deep inside an inline function body that would only run later — is a STATIC
// error (variable-0118, higher-order-functions-070).
func (p *Parsed) UsesVariable(prefix, local string) bool {
	if p == nil {
		return false
	}
	return walkVarRefs(p.root, varName{prefix, local}, nil)
}

type varName struct{ prefix, local string }

// walkVarRefs reports whether want is referenced anywhere in e while not
// shadowed. bound is the stack of names bound by enclosing binders; it is only
// ever appended to, so a slice suffices.
//
// It mirrors walkFuncCalls' traversal (see its doc comment) and must cover the
// same Expr kinds: a kind missing here yields a false NEGATIVE (no error
// reported), never a false positive.
func walkVarRefs(e Expr, want varName, bound []varName) bool {
	if e == nil {
		return false
	}
	shadowed := func() bool {
		for _, b := range bound {
			if b == want {
				return true
			}
		}
		return false
	}
	any := func(es ...Expr) bool {
		for _, x := range es {
			if walkVarRefs(x, want, bound) {
				return true
			}
		}
		return false
	}
	switch v := e.(type) {
	case *VarRef:
		return !shadowed() && v.Prefix == want.prefix && v.Local == want.local
	case *FuncCall:
		return any(v.Args...)
	case *BinaryExpr:
		return any(v.L, v.R)
	case *UnaryExpr:
		return any(v.X)
	case *UnionExpr:
		return any(v.L, v.R)
	case *PathExpr:
		if any(v.Start) {
			return true
		}
		for _, s := range v.Steps {
			if s == nil {
				continue
			}
			if any(s.Preds...) || any(s.Postfix) {
				return true
			}
		}
	case *FilterExpr:
		return any(v.Primary) || any(v.Preds...)
	case *SequenceExpr:
		return any(v.Items...)
	case *RangeExpr:
		return any(v.From, v.To)
	case *ForExpr:
		// Each clause's sequence sees the bindings of the clauses BEFORE it,
		// so the name is added only after its own sequence is walked.
		inner := bound
		for _, b := range v.Binds {
			if walkVarRefs(b.Seq, want, inner) {
				return true
			}
			inner = append(append([]varName{}, inner...), varName{b.Prefix, b.Local})
		}
		return walkVarRefs(v.Body, want, inner)
	case *LetExpr:
		inner := bound
		for _, b := range v.Binds {
			if walkVarRefs(b.Seq, want, inner) {
				return true
			}
			inner = append(append([]varName{}, inner...), varName{b.Prefix, b.Local})
		}
		return walkVarRefs(v.Body, want, inner)
	case *QuantExpr:
		inner := bound
		for _, b := range v.Binds {
			if walkVarRefs(b.Seq, want, inner) {
				return true
			}
			inner = append(append([]varName{}, inner...), varName{b.Prefix, b.Local})
		}
		return walkVarRefs(v.Satisfies, want, inner)
	case *IfExpr:
		return any(v.Cond, v.Then, v.Else)
	case *MapExpr:
		for i := range v.Keys {
			if any(v.Keys[i]) {
				return true
			}
			if i < len(v.Vals) && any(v.Vals[i]) {
				return true
			}
		}
	case *ArrayExpr:
		return any(v.Items...)
	case *InlineFunc:
		inner := append([]varName{}, bound...)
		for _, pb := range v.Params {
			inner = append(inner, varName{pb.Prefix, pb.Local})
		}
		return walkVarRefs(v.Body, want, inner)
	case *LookupExpr:
		return any(v.Base, v.Key)
	case *DynCall:
		return any(v.Base) || any(v.Args...)
	case *CompareExpr:
		return any(v.L, v.R)
	case *StringConcatExpr:
		return any(v.Parts...)
	case *IntersectExceptExpr:
		return any(v.L, v.R)
	case *InstanceOfExpr:
		return any(v.X)
	case *TreatExpr:
		return any(v.X)
	case *CastExpr:
		return any(v.X)
	case *ArrowExpr:
		if any(v.Base) {
			return true
		}
		for _, s := range v.Steps {
			if any(s.Spec) || any(s.Args...) {
				return true
			}
		}
	case *SimpleMapExpr:
		return any(v.Steps...)
	}
	return false
}
