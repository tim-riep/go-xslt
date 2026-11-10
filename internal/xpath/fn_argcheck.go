package xpath

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// The function conversion rules (XPath 3.1 §3.1.5.2) for built-in functions.
//
// Every call of a built-in function — a static call, a named function
// reference, fn:function-lookup, a partial application, a dynamic call through
// a function item — passes through dispatchFunc, which looks the function up
// in the signature catalogue (fn_catalog.go) and runs its arguments through
// the rules BEFORE the implementation sees them, so an implementation can rely
// on its declared parameter types instead of re-checking them:
//
//	atomic parameter  atomize the argument; cast each xs:untypedAtomic to the
//	                  expected type (xs:double for xs:numeric; XPTY0117 for the
//	                  namespace-sensitive xs:QName/xs:NOTATION); numeric type
//	                  promotion and xs:anyURI → xs:string are accepted; any
//	                  other type, or a wrong item count, is XPTY0004.
//	other parameter   (node(), element(), map(*), array(*), function(*), …)
//	                  SequenceType matching, XPTY0004 on a mismatch.
//
// Outside the rules, nothing changes: an argument that already matches is
// handed over untouched (no copy, no allocation — the common case is decided
// by the type switch in argSpec.fast), the argument slice is copied only when
// a value is actually replaced, and XSLT 1.0 backwards-compatible processing
// (Context.BC10) keeps its own lenient conversions and skips the check
// entirely.
//
// Two conversions are VALIDITY checks only, not materialised: numeric
// promotion (an xs:integer where xs:double is declared is accepted as it is —
// every implementation reads numeric arguments through ToNumber/Float, and
// none returns its argument) and xs:anyURI/derived strings where xs:string is
// declared (read through Lexical). An untyped node whose atomized value is
// the xs:untypedAtomic of its string value is likewise passed as the node
// where the declared type is xs:string or xs:anyAtomicType (the cast is the
// identity), so string functions keep reading its string value directly.

// argKind says how an argSpec is checked.
type argKind uint8

const (
	akItem   argKind = iota // item() (any occurrence): only the item count
	akAtomic                // an atomic type (incl. xs:anyAtomicType, xs:numeric)
	akNode                  // node() (any occurrence)
	akOther                 // everything else: SequenceType matching
)

// atomTypeCount sizes the per-type acceptance table.
const atomTypeCount = int(XSerror) + 1

// argSpec is one declared parameter.
type argSpec struct {
	name string
	text string // the type as written, for messages
	st   *SeqType
	// declared is the type exactly as the catalogue writes it (st differs
	// only for a function-typed parameter, which is checked as function(*)).
	declared *SeqType
	kind     argKind
	occ      byte // 0 (exactly one), '?', '*', '+'
	atom     AtomType
	// ok[t] says a value of atomic type t satisfies an atomic parameter
	// without being converted (derivation, numeric promotion, anyURI →
	// string, untypedAtomic → string).
	ok                   [atomTypeCount]bool
	okStr, okDbl, okBool bool // the same for the untyped Go values string/float64/bool
	// stringy: the declared type is xs:string, xs:untypedAtomic or
	// xs:anyAtomicType, so a node that atomizes to xs:untypedAtomic needs no
	// cast and may be passed as the node itself.
	stringy bool
	// skip: item()* — nothing to check.
	skip bool
	// okMap, okArray, okFunc: the declared item type is the wildcard
	// map(*), array(*) or function(*), which a *Map, an *Array (function(*)
	// also takes both and a *Function) satisfies as it is.
	okMap, okArray, okFunc bool
}

// sigForm is the signature of one arity of a built-in function.
type sigForm struct {
	params []argSpec
	rest   *argSpec // variadic tail: applies to every argument beyond params
	ret    string
	retST  *SeqType // ret parsed; nil when it is not a SequenceType (fn:error's "none")
	// allSkip: every parameter is item()*, so there is nothing to check.
	allSkip bool
}

// builtinSig is every arity of one built-in function.
type builtinSig struct {
	ns, local string
	forms     []sigForm  // ascending arity; a variadic function has one form
	arities   []int      // arities[i] is the arity of forms[i]
	lo, hi    int        // arity range (hi == arityVariadic for fn:concat)
	byArity   []*sigForm // byArity[n] is the form of arity n (nil if none), for n up to the highest fixed arity
	// fn is the implementation registered for the function (nil for a
	// catalogued function the engine does not implement). Bound once, after
	// every init() has filled coreFuncs/mathFuncs/mapFuncs/arrayFuncs, so a
	// call finds the implementation and its signature with one lookup.
	fn coreFunc
}

// form returns the signature of arity n, or nil when the function has none.
func (s *builtinSig) form(n int) *sigForm {
	if n < len(s.byArity) {
		if f := s.byArity[n]; f != nil {
			return f
		}
	}
	if s.hi == arityVariadic && n >= s.lo {
		return &s.forms[len(s.forms)-1]
	}
	return nil
}

// hasArity reports whether the function is defined at arity n.
func (s *builtinSig) hasArity(n int) bool { return s.form(n) != nil }

// sigSet is the catalogue of one function namespace, keyed by local name.
type sigSet map[string]*builtinSig

var (
	catalogOnce                      sync.Once
	sigFn, sigMath, sigMap, sigArray sigSet
	catalogPrefixes                  = map[string]string{"fn": nsFn, "math": nsMath, "map": nsMap, "array": nsArray}
	hostCatalogPrefixes              = map[string]string{"fn": nsFn}
)

// ensureCatalog parses the built-in catalogue on first use (not at init, so a
// program that never calls a built-in pays nothing).
func ensureCatalog() {
	catalogOnce.Do(func() {
		sets, err := parseSigCatalog(builtinCatalogSrc, catalogPrefixes)
		if err != nil {
			panic("xpath: built-in function catalogue: " + err.Error())
		}
		sigFn, sigMath, sigMap, sigArray = sets[nsFn], sets[nsMath], sets[nsMap], sets[nsArray]
		for _, b := range []struct {
			set   sigSet
			funcs map[string]coreFunc
		}{{sigFn, coreFuncs}, {sigMath, mathFuncs}, {sigMap, mapFuncs}, {sigArray, arrayFuncs}} {
			for name, sig := range b.set {
				sig.fn = b.funcs[name]
			}
		}
	})
}

// sigSetFor returns the catalogue of a function namespace URI (nil for any
// other namespace).
func sigSetFor(ns string) sigSet {
	ensureCatalog()
	switch ns {
	case nsFn:
		return sigFn
	case nsMath:
		return sigMath
	case nsMap:
		return sigMap
	case nsArray:
		return sigArray
	}
	return nil
}

// builtinSigOf returns the catalogued signature of ns:local, or nil.
func builtinSigOf(ns, local string) *builtinSig {
	return sigSetFor(ns)[local]
}

// parseSigCatalog parses catalogue lines (see builtinCatalogSrc) into one
// sigSet per namespace URI; prefixes maps the line prefixes it accepts to
// their namespace.
func parseSigCatalog(src string, prefixes map[string]string) (map[string]sigSet, error) {
	out := map[string]sigSet{}
	for _, uri := range prefixes {
		out[uri] = sigSet{}
	}
	for ln, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		pfx, local, params, ret, err := splitSigLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %v", ln+1, err)
		}
		uri, ok := prefixes[pfx]
		if !ok {
			return nil, fmt.Errorf("line %d: unknown prefix %q", ln+1, pfx)
		}
		form := sigForm{ret: ret}
		form.retST, _ = sigParseSeqType(ret)
		for i, ptxt := range params {
			if ptxt == "..." {
				if i == 0 || i != len(params)-1 {
					return nil, fmt.Errorf("line %d: misplaced ...", ln+1)
				}
				last := form.params[len(form.params)-1]
				form.rest = &last
				continue
			}
			spec, err := parseArgSpec(ptxt)
			if err != nil {
				return nil, fmt.Errorf("line %d: %v", ln+1, err)
			}
			form.params = append(form.params, spec)
		}
		set := out[uri]
		sig := set[local]
		if sig == nil {
			sig = &builtinSig{ns: uri, local: local}
			set[local] = sig
		}
		arity := len(form.params)
		if form.rest != nil {
			sig.hi = arityVariadic
		}
		sig.forms = append(sig.forms, form)
		sig.arities = append(sig.arities, arity)
	}
	for _, set := range out {
		for _, sig := range set {
			// ascending arity
			idx := make([]int, len(sig.arities))
			for i := range idx {
				idx[i] = i
			}
			sort.Slice(idx, func(a, b int) bool { return sig.arities[idx[a]] < sig.arities[idx[b]] })
			forms := make([]sigForm, len(idx))
			ars := make([]int, len(idx))
			for i, j := range idx {
				forms[i], ars[i] = sig.forms[j], sig.arities[j]
			}
			sig.forms, sig.arities = forms, ars
			sig.lo = ars[0]
			if sig.hi != arityVariadic {
				sig.hi = ars[len(ars)-1]
			}
			top := ars[len(ars)-1]
			sig.byArity = make([]*sigForm, top+1)
			for i := range sig.forms {
				if sig.forms[i].rest == nil {
					sig.byArity[ars[i]] = &sig.forms[i]
				}
				// allSkip: every parameter is item()* (nothing to check)
				all := true
				for j := range sig.forms[i].params {
					if !sig.forms[i].params[j].skip {
						all = false
					}
				}
				sig.forms[i].allSkip = all && sig.forms[i].rest == nil
			}
		}
	}
	return out, nil
}

// splitSigLine splits `prefix:local($a as T, …) as R`.
func splitSigLine(line string) (pfx, local string, params []string, ret string, err error) {
	open := strings.IndexByte(line, '(')
	if open < 0 {
		return "", "", nil, "", fmt.Errorf("no parameter list in %q", line)
	}
	name := line[:open]
	c := strings.IndexByte(name, ':')
	if c < 0 {
		return "", "", nil, "", fmt.Errorf("function name %q has no prefix", name)
	}
	pfx, local = name[:c], name[c+1:]
	depth, end := 0, -1
	for i := open; i < len(line) && end < 0; i++ {
		switch line[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
	}
	if end < 0 {
		return "", "", nil, "", fmt.Errorf("unbalanced parentheses in %q", line)
	}
	rest := strings.TrimSpace(line[end+1:])
	if !strings.HasPrefix(rest, "as ") {
		return "", "", nil, "", fmt.Errorf("no return type in %q", line)
	}
	ret = strings.TrimSpace(rest[3:])
	inner := line[open+1 : end]
	depth, start := 0, 0
	for i := 0; i <= len(inner); i++ {
		if i < len(inner) {
			switch inner[i] {
			case '(':
				depth++
				continue
			case ')':
				depth--
				continue
			case ',':
				if depth != 0 {
					continue
				}
			default:
				continue
			}
		}
		if p := strings.TrimSpace(inner[start:i]); p != "" {
			params = append(params, p)
		}
		start = i + 1
	}
	return pfx, local, params, ret, nil
}

// parseArgSpec parses `$name as SequenceType`.
func parseArgSpec(txt string) (argSpec, error) {
	var p argSpec
	if !strings.HasPrefix(txt, "$") {
		return p, fmt.Errorf("parameter %q does not start with $", txt)
	}
	name, typ, ok := strings.Cut(txt[1:], " as ")
	if !ok {
		return p, fmt.Errorf("parameter %q has no type", txt)
	}
	p.name, p.text = strings.TrimSpace(name), strings.TrimSpace(typ)
	st, ok := sigParseSeqType(p.text)
	if !ok {
		return p, fmt.Errorf("parameter %q: unparsable type", txt)
	}
	p.declared = st
	// A function-typed parameter is checked as function(*) (see
	// builtinCatalogSrc).
	if !st.Empty && st.Item.Kind == itFunction && !st.Item.Wild {
		st = &SeqType{Occur: st.Occur, Item: ItemType{Kind: itFunction, Wild: true}}
	}
	p.st, p.occ = st, st.Occur
	switch {
	case st.Empty:
		p.kind = akOther
	case st.Item.Kind == itItem:
		p.kind = akItem
		p.skip = st.Occur == '*'
	case st.Item.Kind == itAtomic && st.Item.SchemaType == nil:
		p.kind = akAtomic
		p.atom = st.Item.Atom
		for t := AtomType(0); int(t) < atomTypeCount; t++ {
			p.ok[t] = atomAcceptedAsIs(p.atom, t)
		}
		p.okStr, p.okDbl, p.okBool = p.ok[XSstring], p.ok[XSdouble], p.ok[XSboolean]
		p.stringy = p.atom == XSstring || p.atom == XSanyAtomicType || p.atom == XSuntypedAtomic
	case st.Item.Kind == itNode && st.Item.Node != nil && st.Item.Node.Kind == testNode:
		p.kind = akNode
	default:
		p.kind = akOther
		if st.Item.Wild || st.Item.Kind == itMap && st.Item.MapVal == nil || st.Item.Kind == itArray && st.Item.ArrayItem == nil {
			p.okMap = st.Item.Kind == itMap || st.Item.Kind == itFunction
			p.okArray = st.Item.Kind == itArray || st.Item.Kind == itFunction
			p.okFunc = st.Item.Kind == itFunction
		}
	}
	return p, nil
}

// atomAcceptedAsIs reports whether a value of atomic type t satisfies a
// parameter declared as target without conversion.
func atomAcceptedAsIs(target, t AtomType) bool {
	switch target {
	case XSanyAtomicType:
		return t != XSnumeric && t != XSerror
	case XSuntypedAtomic:
		return t == XSuntypedAtomic
	case XSstring:
		// xs:anyURI promotes to xs:string, xs:untypedAtomic casts to it
		// unchanged.
		return isStringType(t) || t == XSanyURI || t == XSuntypedAtomic
	case XSdouble, XSnumeric:
		// xs:float and xs:decimal (so xs:integer) promote to xs:double;
		// xs:numeric is any of the three.
		return isNumericType(t)
	case XSfloat:
		return isNumericType(t) && t != XSdouble
	}
	return t != XSnumeric && t != XSerror && atomDerivesFrom(t, target)
}

// --- the check ---------------------------------------------------------------

// callBuiltin calls the built-in function local of the namespace whose
// catalogue is set (ns is that namespace's URI, reg its implementation
// registry): it enforces the arity, applies the function conversion rules to
// args and runs the implementation. ok is false when the namespace has no such
// function. A registered function with no catalogue entry (a gap
// TestBuiltinCatalogComplete exists to prevent) is checked for arity only.
func callBuiltin(set sigSet, reg map[string]coreFunc, ns, local string, args []Object, ctx *Context) (v Object, ok bool, err error) {
	if sig := set[local]; sig != nil && sig.fn != nil {
		args, err = checkSig(sig, ns, local, args, ctx)
		if err != nil {
			return nil, true, err
		}
		v, err = sig.fn(ctx, args)
		return v, true, err
	}
	fn, found := reg[local]
	if !found {
		return nil, false, nil
	}
	if err = checkStdArity(ns, local, len(args)); err != nil {
		return nil, true, err
	}
	v, err = fn(ctx, args)
	return v, true, err
}

// checkSig enforces the arity of sig and applies the conversion rules to args.
func checkSig(sig *builtinSig, ns, local string, args []Object, ctx *Context) ([]Object, error) {
	f := sig.form(len(args))
	if f == nil {
		return nil, fmt.Errorf("err:XPST0017: %s() does not accept %d arguments", local, len(args))
	}
	if f.allSkip || len(args) == 0 || (ctx != nil && ctx.BC10) {
		return args, nil
	}
	return f.convert(local, args, ctx)
}

// convert applies the conversion rules to each argument.
func (f *sigForm) convert(fname string, args []Object, ctx *Context) ([]Object, error) {
	var out []Object
	for i, a := range args {
		p := f.spec(i)
		if p == nil || p.skip || p.fast(a) {
			continue
		}
		v, changed, err := p.convert(fname, i, a, ctx)
		if err != nil {
			return nil, err
		}
		if changed {
			if out == nil {
				// The caller's slice may be shared (an array's members, a
				// partial application's template): never write into it.
				out = append(make([]Object, 0, len(args)), args...)
			}
			out[i] = v
		}
	}
	if out != nil {
		return out, nil
	}
	return args, nil
}

func (f *sigForm) spec(i int) *argSpec {
	if i < len(f.params) {
		return &f.params[i]
	}
	return f.rest
}

// fast reports whether a already satisfies the parameter, without conversion
// and without allocating — the case of nearly every call.
func (p *argSpec) fast(a Object) bool {
	// One item, by far the most common shape: any occurrence indicator takes it.
	switch v := a.(type) {
	case *Atomic:
		return p.kind == akItem || p.kind == akAtomic && p.ok[v.T]
	case string:
		return p.kind == akItem || p.kind == akAtomic && p.okStr
	case float64:
		return p.kind == akItem || p.kind == akAtomic && p.okDbl
	case bool:
		return p.kind == akItem || p.kind == akAtomic && p.okBool
	case *xmltree.Node:
		switch p.kind {
		case akItem:
			return true
		case akAtomic:
			return p.stringy && v.Untyped()
		case akNode:
			return v.RealItem() == nil
		}
		return false
	case *Map:
		return p.kind == akItem || p.kind == akOther && p.okMap
	case *Array:
		return p.kind == akItem || p.kind == akOther && p.okArray
	case *Function:
		return p.kind == akItem || p.kind == akOther && p.okFunc
	case NodeSet:
		if !p.countOK(len(v)) {
			return false
		}
		switch p.kind {
		case akItem:
			return true
		case akAtomic:
			if !p.stringy {
				return len(v) == 0
			}
			for _, nd := range v {
				if !plainNode(nd) {
					return false
				}
			}
			return true
		case akNode:
			for _, nd := range v {
				if nd == nil || nd.RealItem() != nil {
					return false
				}
			}
			return true
		}
		return len(v) == 0
	case Sequence:
		if !p.countOK(len(v)) {
			return false
		}
		if p.kind == akItem {
			return true
		}
		for _, it := range v {
			if !p.itemFast(it) {
				return false
			}
		}
		return true
	case nil:
		return p.countOK(0)
	}
	return false
}

// countOK reports whether n items satisfy the occurrence indicator.
func (p *argSpec) countOK(n int) bool {
	switch p.occ {
	case 0:
		return n == 1
	case '?':
		return n <= 1
	case '+':
		return n >= 1
	}
	return true
}

// itemFast reports whether one item satisfies the parameter's item type as it
// is (the occurrence indicator is checked by the caller).
func (p *argSpec) itemFast(it Item) bool {
	switch v := it.(type) {
	case *Atomic:
		return p.kind == akAtomic && p.ok[v.T]
	case string:
		return p.kind == akAtomic && p.okStr
	case float64:
		return p.kind == akAtomic && p.okDbl
	case bool:
		return p.kind == akAtomic && p.okBool
	case *xmltree.Node:
		switch p.kind {
		case akAtomic:
			return p.stringy && v.Untyped()
		case akNode:
			return v.RealItem() == nil
		}
	case *Map:
		return p.kind == akOther && p.okMap
	case *Array:
		return p.kind == akOther && p.okArray
	case *Function:
		return p.kind == akOther && p.okFunc
	}
	return false
}

// plainNode reports whether nd is an ordinary node with no schema annotation,
// whose typed value is its string value as xs:untypedAtomic (xs:string for a
// comment, processing instruction or namespace node) — the nodes a string
// parameter takes as they are.
func plainNode(nd *xmltree.Node) bool {
	return nd != nil && nd.Untyped()
}

// convert is the general case: atomize, cast and promote, then match the
// declared type. changed reports that the returned value replaces a.
func (p *argSpec) convert(fname string, idx int, a Object, ctx *Context) (v Object, changed bool, err error) {
	switch p.kind {
	case akItem:
		if err := p.checkCount(fname, idx, a, itemCount(a)); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	case akNode, akOther:
		if MatchesSeqTypeCtx(p.st, a, ctx) {
			return nil, false, nil
		}
		return nil, false, p.mismatch(fname, idx, a)
	}
	// One item that is an atomic value or an ordinary node (the usual shape
	// of a call): convert it without building a sequence.
	if itemCount(a) == 1 {
		switch x := itemAt(a, 0).(type) {
		case *Atomic:
			c, ch, err := p.convertAtom(fname, idx, a, x, ctx)
			if err != nil || !ch {
				return nil, false, err
			}
			return c, true, nil
		case *xmltree.Node:
			if plainNode(x) && x.Kind != xmltree.KindComment && x.Kind != xmltree.KindPI && x.Kind != xmltree.KindNamespace {
				// Its typed value is the xs:untypedAtomic of its string value.
				if p.atom == XSdouble || p.atom == XSnumeric {
					if f, ok := simpleDouble(x.StringValue()); ok {
						return NewDouble(f), true, nil
					}
				}
				c, ch, err := p.convertAtom(fname, idx, a, NewUntyped(x.StringValue()), ctx)
				if err != nil || !ch {
					return nil, false, err
				}
				return c, true, nil
			}
			if !plainNode(x) {
				// A type-annotated node: its one typed value, converted as
				// the general loop below would, without the sequences it
				// builds. Anything else (a list type, no typed value, an
				// error) takes the general way, which reports it as before.
				if at, n, err := atomizeOne(x); err == nil && n == 1 {
					c, _, err := p.convertAtom(fname, idx, a, at.(*Atomic), ctx)
					if err != nil {
						return nil, false, err
					}
					return c, true, nil
				}
			}
		}
	}
	items := Items(a)
	out := make([]Item, 0, len(items))
	for _, it := range items {
		switch x := it.(type) {
		case *Atomic:
			c, ch, err := p.convertAtom(fname, idx, a, x, ctx)
			if err != nil {
				return nil, false, err
			}
			out = append(out, c)
			changed = changed || ch
			continue
		case string, float64, bool:
			t, _ := itemAtomType(x)
			if !p.ok[t] {
				return nil, false, p.mismatch(fname, idx, a)
			}
			out = append(out, x)
			continue
		case *xmltree.Node:
			if p.stringy && plainNode(x) {
				out = append(out, x)
				continue
			}
		case *Map, *Function:
			return nil, false, fmt.Errorf("err:FOTY0013: %s(): argument %d ($%s) cannot be atomized: a %s has no typed value",
				fname, idx+1, p.name, itemKind(it))
		}
		// A node or an array: its atomized items.
		atoms, err := Atomize(it)
		if err != nil {
			return nil, false, err
		}
		changed = true
		for _, ai := range atoms {
			x, ok := ai.(*Atomic)
			if !ok {
				return nil, false, p.mismatch(fname, idx, a)
			}
			c, _, err := p.convertAtom(fname, idx, a, x, ctx)
			if err != nil {
				return nil, false, err
			}
			out = append(out, c)
		}
	}
	if err := p.checkCount(fname, idx, a, len(out)); err != nil {
		return nil, false, err
	}
	if !changed {
		return nil, false, nil
	}
	return FromItems(out), true, nil
}

// convertAtom applies the rules to one atomic item.
func (p *argSpec) convertAtom(fname string, idx int, arg Object, x *Atomic, ctx *Context) (Item, bool, error) {
	if p.ok[x.T] {
		return x, false, nil
	}
	if x.T != XSuntypedAtomic {
		return nil, false, p.mismatch(fname, idx, arg)
	}
	// An xs:untypedAtomic is cast to the expected type.
	target := p.atom
	switch target {
	case XSqname, XSnotation:
		return nil, false, fmt.Errorf("err:XPTY0117: %s(): argument %d ($%s): an xs:untypedAtomic cannot be cast to the namespace-sensitive %s",
			fname, idx+1, p.name, target)
	case XSnumeric:
		target = XSdouble
	}
	if target == XSdouble {
		if f, ok := simpleDouble(x.Lexical()); ok {
			return NewDouble(f), true, nil
		}
	}
	c, err := castInContext(x, target, ctx)
	if err != nil {
		return nil, false, err
	}
	ca, ok := c.(*Atomic)
	if !ok {
		return nil, false, p.mismatch(fname, idx, arg)
	}
	return ca, true, nil
}

// simpleDouble parses the plain decimal notations ("12", "-3.5", ".5", "7.")
// that make up nearly all numeric text, exactly as casting the string to
// xs:double would, without building the intermediate values. Anything else
// (an exponent, INF, NaN, other digits, a malformed number) is not handled
// here and takes the general cast.
func simpleDouble(s string) (float64, bool) {
	s = strings.Trim(s, " \t\r\n")
	if s == "" || len(s) > 40 {
		return 0, false
	}
	i, digits, dot := 0, 0, false
	if s[0] == '+' || s[0] == '-' {
		i++
	}
	for ; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			digits++
		case c == '.' && !dot:
			dot = true
		default:
			return 0, false
		}
	}
	if digits == 0 {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// checkCount reports XPTY0004 when n items break the occurrence indicator.
func (p *argSpec) checkCount(fname string, idx int, a Object, n int) error {
	switch p.occ {
	case 0:
		if n == 1 {
			return nil
		}
	case '?':
		if n <= 1 {
			return nil
		}
	case '+':
		if n >= 1 {
			return nil
		}
	default:
		return nil
	}
	return p.mismatch(fname, idx, a)
}

// mismatch is the XPTY0004 for argument idx.
func (p *argSpec) mismatch(fname string, idx int, a Object) error {
	return fmt.Errorf("err:XPTY0004: %s(): argument %d ($%s) must be %s, got %s",
		fname, idx+1, p.name, p.text, describeArg(a))
}

// describeArg words an argument for an error message.
func describeArg(a Object) string {
	switch n := itemCount(a); n {
	case 0:
		return "the empty sequence"
	case 1:
		return "a " + itemKind(itemAt(a, 0))
	default:
		return fmt.Sprintf("a sequence of %d items starting with a %s", n, itemKind(itemAt(a, 0)))
	}
}

// itemKind names an item's type for messages.
func itemKind(it Item) string {
	switch v := it.(type) {
	case *Atomic:
		return v.T.String()
	case string:
		return "xs:string"
	case float64:
		return "xs:double"
	case bool:
		return "xs:boolean"
	case *xmltree.Node:
		return "node"
	case *Map:
		return "map"
	case *Array:
		return "array"
	case *Function:
		return "function item"
	}
	return fmt.Sprintf("%T", it)
}

// --- host catalogues ---------------------------------------------------------

// SigCatalog is a set of function signatures supplied by a host for the
// functions it implements itself (the XSLT additions to the fn: namespace:
// key, document, current-group, …), checked by the same function conversion
// rules as the built-ins.
type SigCatalog struct {
	set sigSet
}

// NewSigCatalog parses signature lines in the notation of builtinCatalogSrc,
// all in the fn: namespace.
func NewSigCatalog(src string) (*SigCatalog, error) {
	sets, err := parseSigCatalog(src, hostCatalogPrefixes)
	if err != nil {
		return nil, err
	}
	return &SigCatalog{set: sets[nsFn]}, nil
}

// Has reports whether local is catalogued, and whether it has arity n.
func (c *SigCatalog) Has(local string, n int) (known, arityOK bool) {
	if c == nil {
		return false, false
	}
	s := c.set[local]
	if s == nil {
		return false, false
	}
	return true, s.hasArity(n)
}

// Names lists the catalogued function names, sorted.
func (c *SigCatalog) Names() []string {
	var out []string
	for n := range c.set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ArityRanges returns the [min,max] arity of every catalogued function.
func (c *SigCatalog) ArityRanges() map[string][2]int {
	out := make(map[string][2]int, len(c.set))
	for n, s := range c.set {
		out[n] = [2]int{s.lo, s.hi}
	}
	return out
}

// Arities lists the arities catalogued for local.
func (c *SigCatalog) Arities(local string) []int {
	if s := c.set[local]; s != nil {
		return append([]int(nil), s.arities...)
	}
	return nil
}

// Check applies the function conversion rules to args for the catalogued
// function local, returning the arguments to pass on. A function or arity not
// in the catalogue is passed through unchecked (the host reports its own
// arity errors), as is any call under XSLT 1.0 compatibility processing.
func (c *SigCatalog) Check(local string, args []Object, ctx *Context) ([]Object, error) {
	if c == nil || len(args) == 0 || (ctx != nil && ctx.BC10) {
		return args, nil
	}
	s := c.set[local]
	if s == nil {
		return args, nil
	}
	f := s.form(len(args))
	if f == nil {
		return args, nil
	}
	return f.convert(local, args, ctx)
}
