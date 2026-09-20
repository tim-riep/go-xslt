package xpath

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// evalFunc evaluates a function call. If any argument is an ArgumentPlaceholder
// "?", it produces a partially-applied function instead of calling.
func evalFunc(n *FuncCall, ctx *Context) (Object, error) {
	// A LEXICAL fn: prefix (this is the only place one is spelled by the
	// expression author; dynamic lookups synthesize the prefix from a URI)
	// must be bound by the host when it predeclares none (namespace-6202).
	if n.Prefix == "fn" && ctx != nil && ctx.NS != nil && ctx.HostPrefixesOnly {
		if _, ok := ctx.NS.ResolveNS("fn"); !ok {
			return nil, fmt.Errorf("err:XPST0081: prefix \"fn\" has no namespace binding")
		}
	}
	// Partial application?
	holes := 0
	for _, a := range n.Args {
		if _, ok := a.(*Placeholder); ok {
			holes++
		}
	}
	if holes > 0 {
		template := make([]Object, len(n.Args))
		isHole := make([]bool, len(n.Args))
		for i, a := range n.Args {
			if _, ok := a.(*Placeholder); ok {
				isHole[i] = true
				continue
			}
			v, err := evalExpr(a, ctx)
			if err != nil {
				return nil, err
			}
			template[i] = v
		}
		prefix, local := n.Prefix, n.Local
		return &Function{Arity: holes, Name: "", Call: func(supplied []Object) (Object, error) {
			filled := make([]Object, len(template))
			si := 0
			for i := range template {
				if isHole[i] {
					if si < len(supplied) {
						filled[i] = supplied[si]
						si++
					}
				} else {
					filled[i] = template[i]
				}
			}
			return dispatchFunc(prefix, local, filled, ctx)
		}}, nil
	}

	args := make([]Object, len(n.Args))
	for i, a := range n.Args {
		v, err := evalExpr(a, ctx)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	return dispatchFunc(n.Prefix, n.Local, args, ctx)
}

// dispatchFunc resolves and invokes a function by (prefix, local) with already
// evaluated arguments. Resolution order: type constructors (xs:), core fn:,
// math:, map:, array:, then the host FuncResolver (XSLT extension functions).
func dispatchFunc(prefix, local string, args []Object, ctx *Context) (Object, error) {
	// A non-lexical prefix that RESOLVES to the XMLSchema namespace names the
	// same constructors as the conventional "xs" (assert024's xsd:QName).
	if prefix != "" && prefix != "xs" && ctx != nil && ctx.NS != nil {
		if uri, ok := ctx.NS.ResolveNS(prefix); ok && uri == "http://www.w3.org/2001/XMLSchema" {
			prefix = "xs"
		}
	}
	// A prefix bound to one of the standard function namespaces names exactly
	// the same functions as the conventional prefix, however it is spelled
	// (evaluate-004 binds "xfn" to the F&O namespace and calls xfn:tokenize).
	// ONLY the switch below is canonicalized: the host resolver at the bottom
	// must still see the prefix as written, since it resolves that prefix in
	// the stylesheet's own namespace scope.
	sw := prefix
	if sw != "" && sw != "fn" && ctx != nil && ctx.NS != nil {
		if uri, ok := ctx.NS.ResolveNS(sw); ok {
			switch uri {
			case nsFn:
				sw = "fn"
			case nsMath:
				sw = "math"
			case nsMap:
				sw = "map"
			case nsArray:
				sw = "array"
			}
		}
	}
	switch sw {
	case "xs":
		// Atomic-type constructors take exactly one argument.
		if _, isType := AtomTypeByName(local); isType || local == "QName" {
			if len(args) != 1 {
				return nil, fmt.Errorf("err:XPST0017: xs:%s() requires exactly one argument", local)
			}
		}
		if len(args) == 1 {
			if items := Items(args[0]); len(items) > 1 {
				if _, isType := AtomTypeByName(local); isType || local == "QName" {
					return nil, fmt.Errorf("err:XPTY0004: %s() requires a single atomic value, got %d items", local, len(items))
				}
			}
		}
		if local == "QName" {
			return constructQName(ctx, firstArgOrEmpty(args))
		}
		if local == "anyURI" {
			// The constructor applies the XPath-level URI well-formedness
			// check that the shared CastTo (XSD-lenient) does not.
			items, err := Atomize(firstArgOrEmpty(args))
			if err != nil {
				return nil, err
			}
			if len(items) == 0 {
				return Sequence{}, nil
			}
			return castInContext(items[0], XSanyURI, ctx)
		}
		// double/float/gYear/gYearMonth go through castInContext too, for the
		// same reason anyURI does: the XSD-version-specific "+INF"/all-zero-
		// year checks live there, not in the shared (XSD-1.1-lenient) CastTo
		// (K2-SeqExprCast-231/232, xs-double-004, xs-float-004,
		// cbcl-cast-gYear-002/003, cbcl-cast-gYearMonth-003 — all use the
		// xs:T(...) constructor-function spelling, not "cast as").
		if t, ok := AtomTypeByName(local); ok && (t == XSdouble || t == XSfloat || t == XSgYear || t == XSgYearMonth) {
			items, err := Atomize(firstArgOrEmpty(args))
			if err != nil {
				return nil, err
			}
			if len(items) == 0 {
				return Sequence{}, nil
			}
			return castInContext(items[0], t, ctx)
		}
		if v, ok, err := CallConstructor(local, firstArgOrEmpty(args)); ok {
			return v, err
		}
		if v, ok, err := callListConstructor(local, args); ok {
			return v, err
		}
	case "", "fn":
		if fn, ok := coreFuncs[local]; ok {
			if err := checkStdArity(nsFn, local, len(args)); err != nil {
				return nil, err
			}
			return fn(ctx, args)
		}
	case "math":
		if fn, ok := mathFuncs[local]; ok {
			if err := checkStdArity(nsMath, local, len(args)); err != nil {
				return nil, err
			}
			return fn(ctx, args)
		}
	case "map":
		if fn, ok := mapFuncs[local]; ok {
			return fn(ctx, args)
		}
	case "array":
		if fn, ok := arrayFuncs[local]; ok {
			return fn(ctx, args)
		}
	}
	if ctx.Funcs != nil {
		v, ok, err := ctx.Funcs.ResolveFunc(prefix, local, args, ctx)
		if err != nil {
			return nil, err
		}
		if ok {
			return v, nil
		}
	}
	// The CONSTRUCTOR FUNCTION of an imported simple type. XPath 3.1 §3.14.4
	// puts one in the static context for every atomic type in the in-scope
	// schema types, so a stylesheet that imports a schema can write
	// my:hatsize('small') exactly as it writes xs:integer('3')
	// (import-schema-166/167/…, notation-0001..0004).
	//
	// Tried AFTER the host resolver, so a stylesheet function declared with
	// the same name still wins: a user-written xsl:function is the more
	// specific thing, and type-functions-0503 makes such a clash an error
	// rather than a silent shadowing in the other direction.
	if v, ok, err := schemaTypeConstructor(prefix, local, args, ctx); ok {
		return v, err
	}
	return nil, fmt.Errorf("unknown function %s()", qn(prefix, local))
}

// schemaTypeConstructor implements the constructor function of a user-defined
// simple type. ok is false when the name resolves to no imported simple type,
// in which case the caller keeps its own "unknown function" answer.
func schemaTypeConstructor(prefix, local string, args []Object, ctx *Context) (Object, bool, error) {
	sn := schemaLookupOf(ctx)
	if sn == nil {
		return nil, false, nil
	}
	lex := local
	if prefix != "" {
		lex = prefix + ":" + local
	}
	t, ok := sn.LookupSchemaType(lex)
	if !ok || t.Complex || t.Namespace == nsXS {
		return nil, false, nil
	}
	if len(args) != 1 {
		return nil, true, fmt.Errorf("err:XPST0017: %s() requires exactly one argument", qn(prefix, local))
	}
	items, err := Atomize(args[0])
	if err != nil {
		return nil, true, err
	}
	switch len(items) {
	case 0:
		// Every constructor's signature is ($arg as xs:anyAtomicType?) — the
		// empty sequence in, the empty sequence out.
		return Sequence{}, true, nil
	case 1:
	default:
		return nil, true, fmt.Errorf("err:XPTY0004: %s() requires a single atomic value, got %d items",
			qn(prefix, local), len(items))
	}
	v, err := castToNamedSchemaType(items[0], t, ctx)
	return v, true, err
}

// xsListItemType maps the three built-in XSD LIST types to their item type:
// xs:IDREFS / xs:NMTOKENS / xs:ENTITIES are constructor functions too
// (fn-function-lookup-523..528, function-literal-524..528).
func xsListItemType(local string) (AtomType, bool) {
	switch local {
	case "IDREFS":
		return XSidref, true
	case "NMTOKENS":
		return XSnmtoken, true
	case "ENTITIES":
		return XSentity, true
	}
	return 0, false
}

// callListConstructor implements the list-type constructors: the single
// argument's string value is split on XML whitespace and every token is cast
// to the item type; the list must not be empty (FORG0001).
func callListConstructor(local string, args []Object) (Object, bool, error) {
	itemT, ok := xsListItemType(local)
	if !ok {
		return nil, false, nil
	}
	if len(args) != 1 {
		return nil, true, fmt.Errorf("err:XPST0017: xs:%s() requires exactly one argument", local)
	}
	items, err := Atomize(args[0])
	if err != nil {
		return nil, true, err
	}
	if len(items) == 0 {
		return Sequence{}, true, nil
	}
	if len(items) > 1 {
		return nil, true, fmt.Errorf("err:XPTY0004: xs:%s() requires a single atomic value", local)
	}
	v, err := castToXSList(items[0], itemT)
	if err != nil {
		return nil, true, err
	}
	return v, true, nil
}

// castToXSList casts one atomic value to a built-in XSD LIST type whose member
// type is itemT: the value's string form is split on XML whitespace and every
// token cast to itemT. A list must have at least one member, so a blank or
// empty value is FORG0001 (castable-007: ” and ' ' are NOT castable as
// xs:NMTOKENS, while ' a b c ' is).
func castToXSList(it Item, itemT AtomType) (Object, error) {
	tokens := strings.Fields(itemString(it))
	if len(tokens) == 0 {
		return nil, fmt.Errorf("err:FORG0001: a list value needs at least one item")
	}
	out := make([]Item, 0, len(tokens))
	for _, tok := range tokens {
		v, err := CastTo(NewString(tok), itemT)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return FromItems(out), nil
}

// constructQName builds an xs:QName from a lexical string, resolving any prefix
// against the standard function-library prefixes and the in-scope namespaces.
func constructQName(ctx *Context, a Object) (Object, error) {
	items, err := Atomize(a)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return Sequence{}, nil
	}
	at, ok := items[0].(*Atomic)
	if !ok {
		return nil, fmt.Errorf("err:XPTY0004: xs:QName expects an atomic value")
	}
	if at.T == XSqname {
		return at, nil
	}
	lex := strings.TrimSpace(at.Lexical())
	prefix, local, ok := qnValidLexical(lex)
	if !ok {
		return nil, fmt.Errorf("err:FOCA0002: %q is not a valid lexical QName", lex)
	}
	space := ""
	if prefix != "" {
		if n, ok := nsForKnownPrefix(prefix); ok {
			space = n
		} else if ctx != nil && ctx.NS != nil {
			if n, ok := ctx.NS.ResolveNS(prefix); ok {
				space = n
			} else {
				return nil, fmt.Errorf("err:FONS0004: no namespace declared for prefix %q", prefix)
			}
		} else {
			return nil, fmt.Errorf("err:FONS0004: no namespace declared for prefix %q", prefix)
		}
	} else if ctx != nil {
		// An unprefixed lexical QName is in the default element/type
		// namespace (K2-SeqExprCast-201).
		space = ctx.DefaultElemNS
		if space == "" && ctx.NS != nil {
			space, _ = ctx.NS.ResolveNS("")
		}
	}
	return NewQName(xmltree.Name{Prefix: prefix, Local: local, Space: space}), nil
}

func firstArgOrEmpty(args []Object) Object {
	if len(args) > 0 {
		return args[0]
	}
	return Sequence{}
}

type coreFunc func(ctx *Context, args []Object) (Object, error)

func arg(args []Object, i int) Object {
	if i < len(args) {
		return args[i]
	}
	return nil
}

// coreFuncs is the fn: function registry. It is a package-var literal so that
// init() functions in sibling files (fn_*.go) can safely register additional
// functions into it before any code runs.
var coreFuncs = func() map[string]coreFunc {
	return map[string]coreFunc{
		// node-set
		"last": func(c *Context, a []Object) (Object, error) {
			if c.NoFocus {
				return nil, fmt.Errorf("err:XPDY0002: fn:last with no focus")
			}
			return NewInteger(int64(c.Size)), nil
		},
		"position": func(c *Context, a []Object) (Object, error) {
			if c.NoFocus {
				return nil, fmt.Errorf("err:XPDY0002: fn:position with no focus")
			}
			return NewInteger(int64(c.Pos)), nil
		},
		"count": func(c *Context, a []Object) (Object, error) {
			return NewInteger(int64(len(Items(arg(a, 0))))), nil
		},
		"local-name": fnLocalName,
		"name":       fnName,
		"namespace-uri": func(c *Context, a []Object) (Object, error) {
			// A non-node argument/context is XPTY0004; the result is xs:anyURI
			// (fn-namespace-uri-3/16/26).
			n, err := firstNodeOrContextErr(c, a)
			if err != nil {
				return nil, err
			}
			if n == nil {
				return NewAnyURI(""), nil
			}
			return NewAnyURI(n.Name.Space), nil
		},
		"id":              fnID,
		"idref":           fnIdref,
		"element-with-id": fnElementWithID,
		"root":            fnRoot,

		// string
		"string":           fnString,
		"concat":           fnConcat,
		"starts-with":      fnStartsWith,
		"ends-with":        fnEndsWith,
		"contains":         fnContains,
		"substring":        fnSubstring,
		"substring-before": fnSubstringBefore,
		"substring-after":  fnSubstringAfter,
		"string-length":    fnStringLength,
		"normalize-space":  fnNormalizeSpace,
		"translate":        fnTranslate,
		// xs:string? arguments: upper-case(12) is XPTY0004 (for-each-902,
		// array-for-each-007).
		"upper-case": func(c *Context, a []Object) (Object, error) {
			s, err := stringArgStrict("upper-case", a, 0, true)
			if err != nil {
				return nil, err
			}
			return upperCaseFull(s), nil
		},
		"lower-case": func(c *Context, a []Object) (Object, error) {
			s, err := stringArgStrict("lower-case", a, 0, true)
			if err != nil {
				return nil, err
			}
			return lowerCaseFull(s), nil
		},
		"matches":     fnMatches,
		"replace":     fnReplace,
		"string-join": fnStringJoin,

		// sequence
		"exists": func(c *Context, a []Object) (Object, error) {
			if ns, ok := ToNodeSet(arg(a, 0)); ok {
				return len(ns) > 0, nil
			}
			return arg(a, 0) != nil, nil
		},
		"empty": func(c *Context, a []Object) (Object, error) {
			if ns, ok := ToNodeSet(arg(a, 0)); ok {
				return len(ns) == 0, nil
			}
			return arg(a, 0) == nil, nil
		},
		"abs": fnAbsT,
		"tokenize": func(c *Context, a []Object) (Object, error) {
			input := ToString(arg(a, 0))
			if len(a) < 2 {
				// tokenize#1 splits on the four XML whitespace characters only
				// — a no-break space is content (fn-tokenize-51).
				return stringsToSeq(strings.FieldsFunc(input, isXMLSpaceRune)), nil
			}
			// fn:tokenize on a zero-length input returns the empty sequence
			// (not a single empty token, which is what re.Split would yield).
			if input == "" {
				return Sequence{}, nil
			}
			flags, err := flagsArg(a, 2)
			if err != nil {
				return nil, err
			}
			re, err := compileRegex(ToString(a[1]), flags)
			if err != nil {
				return nil, err
			}
			// A pattern that can match a zero-length string is an error for
			// fn:tokenize (fn-tokenize-1/36/37/38 — '.?', '^'/'$'/'^[\s]*$' m).
			if re.MatchString("") {
				return nil, fmt.Errorf("err:FORX0003: the pattern matches a zero-length string")
			}
			return stringsToSeq(re.Split(input, -1)), nil
		},
		"distinct-values": fnDistinctValues,
		"reverse":         fnReverse,
		"subsequence":     fnSubsequence,
		"index-of":        fnIndexOf,
		"head": func(c *Context, a []Object) (Object, error) {
			items := Items(arg(a, 0))
			if len(items) == 0 {
				return Sequence{}, nil
			}
			return FromItems(items[:1]), nil
		},
		"tail": func(c *Context, a []Object) (Object, error) {
			items := Items(arg(a, 0))
			if len(items) <= 1 {
				return Sequence{}, nil
			}
			return FromItems(items[1:]), nil
		},
		"min": func(c *Context, a []Object) (Object, error) { return fnMinMax(c, "min", a) },
		"max": func(c *Context, a []Object) (Object, error) { return fnMinMax(c, "max", a) },
		"avg": func(c *Context, a []Object) (Object, error) { return aggregate(arg(a, 0), "avg", nil, nil) },

		// higher-order functions
		"for-each":      fnForEach,
		"filter":        fnFilter,
		"fold-left":     fnFoldLeft,
		"fold-right":    fnFoldRight,
		"for-each-pair": fnForEachPair,
		"sort":          fnSort,

		// boolean
		"boolean": func(c *Context, a []Object) (Object, error) { return EffectiveBoolErr(arg(a, 0)) },
		"not": func(c *Context, a []Object) (Object, error) {
			b, err := EffectiveBoolErr(arg(a, 0))
			if err != nil {
				return nil, err
			}
			return !b, nil
		},
		"true":  func(c *Context, a []Object) (Object, error) { return true, nil },
		"false": func(c *Context, a []Object) (Object, error) { return false, nil },
		"lang":  fnLang,

		// number
		"number":  fnNumber,
		"sum":     fnSum,
		"floor":   fnFloorT,
		"ceiling": fnCeilingT,
		"round":   fnRoundT,
	}
}()

func argOrContext(c *Context, a []Object) Object {
	if len(a) > 0 {
		return a[0]
	}
	if c.Node != nil {
		return NodeSet{c.Node}
	}
	if c.CtxItem != nil {
		return FromItems([]Item{c.CtxItem})
	}
	return ""
}

// argOrContextErr is argOrContext for the functions whose omitted argument
// defaults to the context item: with no focus at all that is XPDY0002
// (fn-string-length-18, fn-number-3) instead of a silent "".
func argOrContextErr(c *Context, a []Object) (Object, error) {
	if len(a) == 0 && c.Node == nil && c.CtxItem == nil && c.NoFocus {
		return nil, fmt.Errorf("err:XPDY0002: the context item is absent")
	}
	return argOrContext(c, a), nil
}

// stringOrContextArg returns the string value of an optional xs:string?
// argument, or string(.) when it is omitted. A plural argument is XPTY0004
// (fn-string-length-19) and a function item cannot be atomized
// (fn-string-length-21).
//
// The two argument forms follow different rules, per fn:normalize-space's own
// F&O 3.1 definition: the ZERO-arg form implicitly takes string(.) (so ANY
// atomic type is fine — string() itself never fails on one), but the explicit
// one-arg form goes through the ordinary xs:string? function conversion
// rules, which only ever promote xs:anyURI or leave a string-family/
// xs:untypedAtomic value alone — an item of any OTHER atomic type (an
// xs:date, an xs:integer, …) matches no rule and is XPTY0004
// (fn-normalize-space-26, fn-string-length-25: normalize-space(.)/
// string-length(.) with the context item bound to a non-string atomic value).
func stringOrContextArg(c *Context, a []Object) (string, error) {
	o, err := argOrContextErr(c, a)
	if err != nil {
		return "", err
	}
	if len(a) == 0 {
		return ToString(o), nil
	}
	items, err := Atomize(o)
	if err != nil {
		return "", err
	}
	switch len(items) {
	case 0:
		return "", nil
	case 1:
		at, ok := items[0].(*Atomic)
		if !ok || !isComparableAsString(at) {
			return "", fmt.Errorf("err:XPTY0004: a single xs:string is required")
		}
		return at.Lexical(), nil
	}
	return "", fmt.Errorf("err:XPTY0004: a single string is required, got %d items", len(items))
}

// stringArgStrict returns the xs:string value of argument i under the function
// conversion rules: nodes atomize, xs:untypedAtomic and xs:anyURI convert, any
// other atomic type or a plural sequence is XPTY0004, and so is an empty
// sequence unless the parameter is optional (fn-translate3args-5/6/7,
// K2-TranslateFunc-1/2).
func stringArgStrict(fn string, a []Object, i int, optional bool) (string, error) {
	items, err := Atomize(arg(a, i))
	if err != nil {
		return "", err
	}
	switch {
	case len(items) == 0:
		if optional {
			return "", nil
		}
		return "", fmt.Errorf("err:XPTY0004: fn:%s argument %d must not be empty", fn, i+1)
	case len(items) > 1:
		return "", fmt.Errorf("err:XPTY0004: fn:%s argument %d must be a single string", fn, i+1)
	}
	at, ok := items[0].(*Atomic)
	if !ok || !isComparableAsString(at) {
		return "", fmt.Errorf("err:XPTY0004: fn:%s argument %d must be an xs:string", fn, i+1)
	}
	return at.Lexical(), nil
}

// fnNumber implements fn:number($arg as xs:anyAtomicType?) as xs:double: the
// argument (or the context item) is atomized and cast to xs:double; a value
// that cannot be cast — an empty sequence, an xs:anyURI or an xs:gYear as much
// as a non-numeric string — is NaN (K-NodeNumberFunc-13/15). The result is
// always an xs:double (fn-numberint1args-1: -2.147483648E9), a plural
// argument is XPTY0004 and an absent focus XPDY0002 (fn-number-3).
func fnNumber(c *Context, a []Object) (Object, error) {
	o, err := argOrContextErr(c, a)
	if err != nil {
		return nil, err
	}
	items, err := Atomize(o)
	if err != nil {
		return nil, err
	}
	if len(items) > 1 {
		return nil, fmt.Errorf("err:XPTY0004: fn:number requires at most one item, got %d", len(items))
	}
	if len(items) == 1 {
		at, ok := items[0].(*Atomic)
		if ok && (at.IsNumeric() || at.T == XSboolean || at.T == XSuntypedAtomic || isStringType(at.T)) {
			if d, err := CastTo(at, XSdouble); err == nil {
				return d, nil
			}
		}
	}
	return NewDouble(math.NaN()), nil
}

// firstNodeOrContextErr is the STRICT form: an explicit non-node argument or
// a non-node context item is XPTY0004 ((1 to 100)[local-name()],
// K-NodeRootFunc-2); an absent focus is XPDY0002.
func firstNodeOrContextErr(c *Context, a []Object) (*xmltree.Node, error) {
	if len(a) > 0 {
		items := Items(a[0])
		if len(items) == 0 {
			return nil, nil
		}
		nd, ok := items[0].(*xmltree.Node)
		if !ok {
			return nil, fmt.Errorf("err:XPTY0004: a node is required")
		}
		return nd, nil
	}
	if c.Node != nil {
		return c.Node, nil
	}
	if c.CtxItem != nil {
		return nil, fmt.Errorf("err:XPTY0004: the context item is not a node")
	}
	if c.NoFocus {
		return nil, fmt.Errorf("err:XPDY0002: the context item is absent")
	}
	return nil, nil
}

func fnLocalName(c *Context, a []Object) (Object, error) {
	n, err := firstNodeOrContextErr(c, a)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return "", nil
	}
	return n.Name.Local, nil
}

func fnName(c *Context, a []Object) (Object, error) {
	n, err := firstNodeOrContextErr(c, a)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return "", nil
	}
	if n.Name.Prefix != "" {
		// Namespace fixup (XSLT §11.7 "Conflicting Namespace Prefixes") can
		// let a later xsl:namespace instruction rebind an element's own
		// name-derived prefix to a different URI (namespace-2615): the
		// element keeps its namespace, but that prefix string no longer
		// names it. Only trust the stored prefix when it is not itself
		// bound, in scope, to some OTHER URI — an unbound prefix is the
		// normal case for a constructed element with no explicit xmlns
		// node, and must still report its compile-time prefix.
		if uri, ok := n.LookupPrefix(n.Name.Prefix); ok && uri != n.Name.Space {
			for p, u := range n.InScopeNamespaces() {
				if u == n.Name.Space {
					return p + ":" + n.Name.Local, nil
				}
			}
			return n.Name.Local, nil
		}
		return n.Name.Prefix + ":" + n.Name.Local, nil
	}
	return n.Name.Local, nil
}

func fnConcat(c *Context, a []Object) (Object, error) {
	var b strings.Builder
	for _, x := range a {
		// Each argument is xs:anyAtomicType?: a plural sequence is XPTY0004
		// (K2-ConcatFunc-1..3), a map or function item cannot be atomized
		// (fn-concat-18: FOTY0013), and an array atomizes to its members.
		items := Items(x)
		if len(items) > 1 {
			return nil, fmt.Errorf("err:XPTY0004: concat: argument is a sequence of %d items", len(items))
		}
		if len(items) == 1 {
			switch v := items[0].(type) {
			case *Array:
				atoms, err := Atomize(v)
				if err != nil {
					return nil, err
				}
				if len(atoms) > 1 {
					return nil, fmt.Errorf("err:XPTY0004: concat: argument atomizes to %d items", len(atoms))
				}
				if len(atoms) == 1 {
					b.WriteString(itemString(atoms[0]))
				}
				continue
			case *Map, *Function:
				return nil, fmt.Errorf("err:FOTY0013: concat: cannot atomize a %T", v)
			}
		}
		b.WriteString(ToString(x))
	}
	return b.String(), nil
}

// The substring-matching functions take an optional third $collation argument
// (strMatcherArg / strMatcher in fn_collation.go); nil is the codepoint
// collation, i.e. plain string matching.

// strPairArgs returns the two xs:string? operands of the contains family
// under the function conversion rules: a numeric or date operand is XPTY0004
// (for-each-pair-902: contains("ee", 12); filter-904: ends-with(date, 'e');
// hof-917: substring-before('Michael Kay', 2)) — UNLESS ctx.BC10 (XSLT's own
// backwards-compatible processing) is set, which takes the lenient
// package-wide ToString (first-item, no cardinality/type error) of each
// operand instead (backwards-045's own d1: contains($s, '2') with $s a
// 3-element node sequence).
func strPairArgs(fn string, a []Object, ctx *Context) (s, sub string, err error) {
	if ctx != nil && ctx.BC10 {
		return ToString(arg(a, 0)), ToString(arg(a, 1)), nil
	}
	if s, err = stringArgStrict(fn, a, 0, true); err != nil {
		return "", "", err
	}
	if sub, err = stringArgStrict(fn, a, 1, true); err != nil {
		return "", "", err
	}
	return s, sub, nil
}

func fnStartsWith(c *Context, a []Object) (Object, error) {
	m, err := strMatcherArg(c, a, 2)
	if err != nil {
		return nil, err
	}
	s, sub, err := strPairArgs("starts-with", a, c)
	if err != nil {
		return nil, err
	}
	return m.hasPrefix(s, sub), nil
}

func fnEndsWith(c *Context, a []Object) (Object, error) {
	m, err := strMatcherArg(c, a, 2)
	if err != nil {
		return nil, err
	}
	s, sub, err := strPairArgs("ends-with", a, c)
	if err != nil {
		return nil, err
	}
	return m.hasSuffix(s, sub), nil
}

func fnContains(c *Context, a []Object) (Object, error) {
	m, err := strMatcherArg(c, a, 2)
	if err != nil {
		return nil, err
	}
	s, sub, err := strPairArgs("contains", a, c)
	if err != nil {
		return nil, err
	}
	_, _, ok := m.find(s, sub)
	return ok, nil
}

func fnSubstringBefore(c *Context, a []Object) (Object, error) {
	m, err := strMatcherArg(c, a, 2)
	if err != nil {
		return nil, err
	}
	s, sep, err := strPairArgs("substring-before", a, c)
	if err != nil {
		return nil, err
	}
	if i, _, ok := m.find(s, sep); ok {
		return s[:i], nil
	}
	return "", nil
}

func fnSubstringAfter(c *Context, a []Object) (Object, error) {
	m, err := strMatcherArg(c, a, 2)
	if err != nil {
		return nil, err
	}
	s, sep, err := strPairArgs("substring-after", a, c)
	if err != nil {
		return nil, err
	}
	if _, j, ok := m.find(s, sep); ok {
		return s[j:], nil
	}
	return "", nil
}

// fnSubstring implements substring() with 1-based rounding semantics, operating
// on runes (Unicode code points).
func fnSubstring(c *Context, a []Object) (Object, error) {
	s := []rune(ToString(arg(a, 0)))
	start := roundHalfUp(ToNumber(arg(a, 1)))
	var end float64 = math.Inf(1)
	if len(a) >= 3 {
		end = start + roundHalfUp(ToNumber(arg(a, 2)))
	}
	// positions are 1-based; clamp
	from := int(math.Max(1, start)) - 1
	var to int
	if math.IsInf(end, 1) {
		to = len(s)
	} else {
		to = int(end) - 1
	}
	if from < 0 {
		from = 0
	}
	if to > len(s) {
		to = len(s)
	}
	if from >= to || from >= len(s) {
		return "", nil
	}
	return string(s[from:to]), nil
}

func fnStringLength(c *Context, a []Object) (Object, error) {
	s, err := stringOrContextArg(c, a)
	if err != nil {
		return nil, err
	}
	return NewInteger(int64(len([]rune(s)))), nil
}

func fnNormalizeSpace(c *Context, a []Object) (Object, error) {
	s, err := stringOrContextArg(c, a)
	if err != nil {
		return nil, err
	}
	return strings.Join(strings.Fields(s), " "), nil
}

func fnTranslate(c *Context, a []Object) (Object, error) {
	s, err := stringArgStrict("translate", a, 0, true)
	if err != nil {
		return nil, err
	}
	fromS, err := stringArgStrict("translate", a, 1, false)
	if err != nil {
		return nil, err
	}
	toS, err := stringArgStrict("translate", a, 2, false)
	if err != nil {
		return nil, err
	}
	from, to := []rune(fromS), []rune(toS)
	var b strings.Builder
	for _, r := range s {
		idx := indexRune(from, r)
		if idx < 0 {
			b.WriteRune(r)
		} else if idx < len(to) {
			b.WriteRune(to[idx])
		}
	}
	return b.String(), nil
}

func indexRune(rs []rune, r rune) int {
	for i, x := range rs {
		if x == r {
			return i
		}
	}
	return -1
}

func fnSum(c *Context, a []Object) (Object, error) {
	return aggregate(arg(a, 0), "sum", arg(a, 1), nil)
}

func roundHalfUp(f float64) float64 {
	if math.IsNaN(f) {
		return f
	}
	return math.Floor(f + 0.5)
}

func fnRoot(c *Context, a []Object) (Object, error) {
	n, err := firstNodeOrContextErr(c, a)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return NodeSet{}, nil
	}
	// effectiveRoot, not the raw Root(): a node extracted from an @as-typed
	// sequence collector is PARENTLESS in the XSLT data model, so the
	// throwaway NoAtomicMerge document that physically holds it is not its
	// root (snapshot-0103/0104: fn:root() of an orphan element/attribute/
	// comment built by an as="node()*" variable is the node itself).
	return NodeSet{effectiveRoot(n)}, nil
}

// idSearchRoot returns the document root to search for fn:id/fn:idref: the
// optional second-argument node's root, else the context node's root. The
// target must be a node — a non-node argument or context item is XPTY0004, an
// absent context is XPDY0002 (fn-idref-2/3/22).
func idSearchRoot(c *Context, a []Object) (*xmltree.Node, error) {
	if len(a) > 1 {
		n, ok := firstItem(arg(a, 1)).(*xmltree.Node)
		if !ok {
			return nil, fmt.Errorf("err:XPTY0004: fn:id/idref target must be a node")
		}
		return n.Root(), nil
	}
	if c.Node != nil {
		return c.Node.Root(), nil
	}
	if n, ok := c.CtxItem.(*xmltree.Node); ok {
		return n.Root(), nil
	}
	if c.CtxItem != nil {
		return nil, fmt.Errorf("err:XPTY0004: fn:id/idref context item is not a node")
	}
	return nil, fmt.Errorf("err:XPDY0002: fn:id/idref has no context node")
}

// idTokenSet collects the whitespace-separated candidate id values from every
// string in the first argument.
func idTokenSet(a []Object) map[string]bool {
	want := map[string]bool{}
	for _, it := range Items(arg(a, 0)) {
		for _, tok := range strings.Fields(itemString(it)) {
			want[tok] = true
		}
	}
	return want
}

// isIDAttr reports whether an attribute is of type ID: declared ID in the DTD,
// annotated xs:ID by schema validation, the reserved xml:id, or (as a lenient
// fallback for schema-less documents) an unprefixed attribute literally named
// "id".
func isIDAttr(at *xmltree.Node) bool {
	return at.IDKind == xmltree.IDKindID || isIDTyped(at) ||
		(at.Name.Space == "http://www.w3.org/XML/1998/namespace" && at.Name.Local == "id") ||
		(at.Name.Space == "" && at.Name.Local == "id")
}

// isIDTyped reports whether schema validation annotated this node with xs:ID
// (or a type derived from it). XSD gives an ID value to an ELEMENT as readily
// as to an attribute — `<xs:element name="id-elem-only" type="xs:ID"/>` — and
// only a type annotation can say so, since nothing in the node's name or the
// DTD marks it (match-212's my:id attribute is namespaced, so even the lenient
// "an attribute called id" fallback above cannot see it).
//
// Inert without an annotation, which is every node in a non-schema-aware run.
func isIDTyped(n *xmltree.Node) bool {
	at, ok := nodeAnnotationType(n)
	return ok && (at == XSid || atomDerivesFrom(at, XSid))
}

// isIDElem reports whether an ELEMENT carries the XDM [is-id] property — the
// element-node counterpart of isIDAttr.
//
// Reading IDKind as well as the annotation is what makes it survive
// input-type-annotations="strip": XSLT 3.0 §4.4 strips the TYPE ANNOTATIONS
// from a source tree but leaves the is-id and is-idref properties alone
// ("the is-id and is-idref properties of element and attribute nodes are
// unaffected"), which is precisely what strip-type-annotations-021 asserts —
// data(id-elem[1]) instance of xs:ID is false there, and id('id1') must still
// find that element. IDKind is xmltree's own carrier for that property and is
// already written back by schema validation (xsd/bridge.go's idKindFor), so
// an element whose simple content is xs:ID-derived keeps it.
func isIDElem(n *xmltree.Node) bool {
	return n.IDKind == xmltree.IDKindID || isIDTyped(n)
}

// fnID implements fn:id. fnElementWithID implements fn:element-with-id. They
// differ in EXACTLY one case, which is the one schema-awareness introduces
// (F&O 3.1 §14.5.1/§14.5.2): when the ID value is the typed value of an
// ELEMENT rather than of an attribute, fn:id returns that element, while
// fn:element-with-id returns the element it IDENTIFIES — its parent.
// match-056 (`match="id('C')"` + `xsl:copy-of select=".."`) and match-054
// (`match="element-with-id('C', $x)"` + `xsl:copy-of select="."`) assert the
// two halves of that distinction against the same shape of document.
func fnID(c *Context, a []Object) (Object, error) { return idLookup(c, a, false) }

func fnElementWithID(c *Context, a []Object) (Object, error) { return idLookup(c, a, true) }

func idLookup(c *Context, a []Object, identified bool) (Object, error) {
	root, err := idSearchRoot(c, a)
	if err != nil {
		return nil, err
	}
	want := idTokenSet(a)
	var out NodeSet
	var walk func(n *xmltree.Node)
	walk = func(n *xmltree.Node) {
		for _, at := range n.Attrs {
			// xs:ID (and so xml:id) has the "collapse" whitespace facet: its
			// value for identity purposes is leading/trailing-trimmed (and any
			// internal whitespace run collapsed), not the raw attribute text —
			// key-076: xml:id="id3 " (a trailing space) must still be found by
			// id(' id3').
			if isIDAttr(at) && want[strings.TrimSpace(at.Value)] {
				out = append(out, n)
				break
			}
		}
		// An ID-VALUED element: the ID belongs to n itself, and the element
		// the ID identifies is n's parent.
		if n.Kind == xmltree.KindElement && isIDElem(n) && want[strings.TrimSpace(n.StringValue())] {
			if !identified {
				out = append(out, n)
			} else if n.Parent != nil && n.Parent.Kind == xmltree.KindElement {
				out = append(out, n.Parent)
			}
		}
		for _, ch := range n.Children {
			walk(ch)
		}
	}
	walk(root)
	return out.normalize(), nil
}

// fnIdref implements fn:idref: the IDREF/IDREFS-typed attribute nodes whose
// token list contains one of the candidate id values (DTD-typed documents).
func fnIdref(c *Context, a []Object) (Object, error) {
	root, err := idSearchRoot(c, a)
	if err != nil {
		return nil, err
	}
	// Unlike fn:id, fn:idref does NOT split its argument into whitespace-
	// separated tokens: each supplied string is one candidate value, compared
	// against the individual IDREF tokens of the nodes searched. A candidate
	// that is not itself a valid xs:IDREF (e.g. one containing a space) can
	// therefore never match anything (id-041).
	want := map[string]bool{}
	for _, it := range Items(arg(a, 0)) {
		want[itemString(it)] = true
	}
	var out NodeSet
	var walk func(n *xmltree.Node)
	walk = func(n *xmltree.Node) {
		for _, at := range n.Attrs {
			if at.IDKind != xmltree.IDKindIDREF && at.IDKind != xmltree.IDKindIDREFS {
				continue
			}
			for _, tok := range strings.Fields(at.Value) {
				if want[tok] {
					out = append(out, at)
					break
				}
			}
		}
		for _, ch := range n.Children {
			walk(ch)
		}
	}
	walk(root)
	return out.normalize(), nil
}

// fnMatches implements matches(input, pattern [, flags]).
func fnMatches(c *Context, a []Object) (Object, error) {
	// $pattern is a required (non-optional) xs:string: an empty or
	// multi-item sequence there is a cardinality violation, not "no
	// pattern" (K-MatchesFunc-1: matches("input", ())).
	pattern, err := stringArgStrict("matches", a, 1, false)
	if err != nil {
		return nil, err
	}
	flags, err := flagsArg(a, 2)
	if err != nil {
		return nil, err
	}
	xsdVersion := ""
	if c != nil {
		xsdVersion = c.XSDVersion
	}
	input := ToString(arg(a, 0))
	re, err := compileRegexForXSDVersion(pattern, flags, xsdVersion)
	if err != nil && isInvalidRepeatCountErr(err) {
		// A bounded repeat quantifier {n}/{n,m} whose count is larger than
		// Go's RE2 engine will even compile (cbcl-matches-038:
		// 'a{2147483647}') can never be satisfied by THIS input anyway: no
		// input longer than its own length + 1 could ever supply that many
		// repetitions of a single-character atom, so clamping every such
		// huge bound down to len(input)+1 is a provably-equivalent rewrite
		// for this call — not a behavior change — while staying well under
		// RE2's own repeat-count ceiling (no allocation/iteration
		// proportional to the original, astronomically large count).
		clamped := clampRegexRepeats(pattern, len([]rune(input))+1)
		if re2, err2 := compileRegexForXSDVersion(clamped, flags, xsdVersion); err2 == nil {
			re, err = re2, nil
		}
	}
	if err != nil {
		return nil, err
	}
	matchSubject := input
	if strings.Contains(flags, "m") && strings.HasSuffix(matchSubject, "\n") {
		// F&O 3.1 §5.6.2's 'm' flag gives '^'/'$' a narrower meaning at the
		// SUBJECT STRING'S OWN trailing newline than Go's native (?m) does:
		// '^' matches "the position immediately after a newline character
		// other than a newline that appears as the last character in the
		// string", and '$' matches "the position immediately before a
		// newline character, and the end of the entire string if there is
		// no newline character at the end of the string" — so neither
		// anchor may ever match at position len(input) when input ends in
		// a newline, while Go's (?m) always allows both there regardless
		// (fn-matches-26: "abcd\ndefg\n" against "^$" must be false, not
		// true). Dropping exactly the one trailing newline before matching
		// removes precisely that one over-permitted position — every other
		// position both engines already agree on (verified by hand against
		// the spec text for several trailing-newline shapes, including
		// fn-matches-27/28's siblings) — and stays sound for a
		// BOOLEAN-only caller: fn:matches never reports offsets or
		// substrings, so the dropped character can never leak into a
		// result the way it would for fn:tokenize/fn:replace/
		// fn:analyze-string (deliberately NOT touched here).
		matchSubject = matchSubject[:len(matchSubject)-1]
	}
	return re.MatchString(matchSubject), nil
}

// isInvalidRepeatCountErr reports whether err is Go's regexp/syntax error for
// a {n}/{n,m} quantifier whose count exceeds RE2's own compiled-program size
// limit — the specific, narrow failure clampRegexRepeats exists to recover
// from, not a stand-in for "any regex compile error".
func isInvalidRepeatCountErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "invalid repeat count")
}

// regexRepeatBound matches a bounded repeat quantifier {n} or {n,m} (with an
// optional open upper bound {n,}). Used only by clampRegexRepeats, itself
// only reached after the ORIGINAL pattern already failed to compile because
// RE2 rejected an unreasonably large count outright.
var regexRepeatBound = regexp.MustCompile(`\{(\d+)(,(\d*))?\}`)

// clampRegexRepeats rewrites every {n}/{n,m} bound greater than maxReps down
// to maxReps. Sound specifically as a same-call fallback: maxReps is the
// caller's own input length (in runes) plus one, and no repeat count larger
// than that can ever be satisfied by that same input, so the rewrite cannot
// change the match outcome for THIS call, only make the pattern compilable.
func clampRegexRepeats(pattern string, maxReps int) string {
	return regexRepeatBound.ReplaceAllStringFunc(pattern, func(m string) string {
		sub := regexRepeatBound.FindStringSubmatch(m)
		n, err := strconv.Atoi(sub[1])
		if err != nil {
			return m
		}
		if n > maxReps {
			n = maxReps
		}
		if sub[2] == "" {
			return fmt.Sprintf("{%d}", n)
		}
		if sub[3] == "" {
			return fmt.Sprintf("{%d,}", n)
		}
		mx, err := strconv.Atoi(sub[3])
		if err != nil {
			return m
		}
		if mx > maxReps {
			mx = maxReps
		}
		if mx < n {
			mx = n
		}
		return fmt.Sprintf("{%d,%d}", n, mx)
	})
}

// fnReplace implements replace(input, pattern, replacement [, flags]).
func fnReplace(c *Context, a []Object) (Object, error) {
	// pattern and replacement must each be a single string — () is XPTY0004
	// (K-ReplaceFunc-2/3).
	if len(Items(arg(a, 1))) == 0 || len(Items(arg(a, 2))) == 0 {
		return nil, fmt.Errorf("err:XPTY0004: fn:replace pattern and replacement are required")
	}
	replStr := ToString(arg(a, 2))
	flags, err := flagsArg(a, 3)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(flags, "q") && !validReplacement(replStr) {
		return nil, fmt.Errorf("err:FORX0004: invalid replacement string %q", replStr)
	}
	if strings.Contains(flags, "q") {
		// The 'q' flag makes BOTH pattern and replacement literal, so this is
		// a plain string replacement (fn-replace-49..53). Case-insensitivity
		// still applies if 'i' is also set.
		pat := ToString(arg(a, 1))
		in := ToString(arg(a, 0))
		if pat == "" {
			return nil, fmt.Errorf("err:FORX0003: the pattern matches a zero-length string")
		}
		if strings.Contains(flags, "i") {
			re, err := compileRegex(pat, strings.ReplaceAll(flags, "q", ""))
			if err != nil {
				return nil, err
			}
			return re.ReplaceAllString(in, strings.ReplaceAll(strings.ReplaceAll(replStr, "\\", "\\\\"), "$", "$$")), nil
		}
		return strings.ReplaceAll(in, pat, replStr), nil
	}
	re, err := compileRegex(ToString(arg(a, 1)), flags)
	if err != nil {
		return nil, err
	}
	// A pattern that matches a zero-length string is FORX0003 for fn:replace,
	// exactly as for fn:tokenize (fn-replace-6 '.*?', cbcl-fn-replace-002 '').
	if re.MatchString("") {
		return nil, fmt.Errorf("err:FORX0003: the pattern matches a zero-length string")
	}
	repl := xpathReplToGo(replStr, re.NumSubexp())
	return re.ReplaceAllString(ToString(arg(a, 0)), repl), nil
}

// fnStringJoin implements string-join(seq [, sep]).
func fnStringJoin(c *Context, a []Object) (Object, error) {
	sep := ""
	if len(a) >= 2 {
		if c != nil && c.BC10 {
			// XSLT 1.0 backwards-compatible processing: the separator takes
			// the lenient, first-item ToString of whatever was supplied —
			// including a multi-node argument (xpath-compat-0303's own
			// "choosing first of multiple nodes and being cast to string")
			// — instead of requiring a true singleton xs:string.
			sep = ToString(arg(a, 1))
		} else {
			// $separator is a REQUIRED xs:string: () is XPTY0004 (K-StringJoinFunc-7).
			s, err := stringArgStrict("string-join", a, 1, false)
			if err != nil {
				return nil, err
			}
			sep = s
		}
	}
	// $arg is declared xs:anyAtomicType*, so the function conversion rule
	// ATOMIZES it (F&O 3.1 §5.4.1). For every ordinary node that is the same
	// string it already had; for a LIST-typed one it is the difference between
	// one item and one per token, which is what the separator then joins
	// (import-schema-029/030: a list of four QNames joined with "," must give
	// "red,green,hat:blue,hat:pink", not the raw space-separated text).
	items, err := Atomize(arg(a, 0))
	if err != nil {
		return nil, err
	}
	return seqAsString(items, sep), nil
}

// flagsArg returns the caller-supplied $flags value when the argument at
// position i was actually given (omitted entirely is the empty-flags
// default, ""). Every $flags parameter across matches/replace/tokenize is
// declared as a REQUIRED xs:string in the arity overload where it exists at
// all, so passing an empty or multi-item sequence there — as opposed to
// simply omitting the argument — is a cardinality violation, not a silent
// "no flags" (K-MatchesFunc-3: matches("input","pattern",())).
func flagsArg(a []Object, i int) (string, error) {
	if i >= len(a) {
		return "", nil
	}
	items, err := Atomize(a[i])
	if err != nil {
		return "", err
	}
	if len(items) != 1 {
		return "", fmt.Errorf("err:XPTY0004: the flags argument must be a single xs:string")
	}
	at, ok := items[0].(*Atomic)
	if !ok || !isComparableAsString(at) {
		return "", fmt.Errorf("err:XPTY0004: the flags argument must be an xs:string")
	}
	return at.Lexical(), nil
}

// CompileRegex compiles an XPath/XSD regex with optional flags into a Go RE2
// regexp. It stays lenient about a bare '{'/'}' so the XSD 1.0 pattern-facet
// validator keeps accepting them literally.
func CompileRegex(pattern, flags string) (*regexp.Regexp, error) {
	re, err := compileRegexOpts(pattern, flags, false, false, GrammarXPath)
	if err != nil {
		return nil, err
	}
	rx, ok := re.(*regexp.Regexp)
	if !ok {
		return nil, fmt.Errorf("err:FORX0002: back-references are not allowed in this pattern")
	}
	return rx, nil
}

// CompileRegexStrict is the XPath-3.1 regex entry point for the host's
// matches/replace/tokenize/analyze-string (xsl:analyze-string): a bare
// '{' or '}' is a grammar error there (FORX0002 — analyze-string-097).
func CompileRegexStrict(pattern, flags string) (Regex, error) {
	return compileRegexOpts(pattern, flags, true, true, GrammarXPath)
}

// compileRegex is the XPath-function entry point (matches, replace, tokenize,
// analyze-string): the XPath 3.1 regex grammar is XSD 1.1's, where a bare
// '{' or '}' is not a NormalChar, so those are rejected here. The exported
// CompileRegex stays lenient: XSD 1.0 patterns may still use them literally.
func compileRegex(pattern, flags string) (Regex, error) {
	return compileRegexOpts(pattern, flags, true, true, GrammarXPath)
}

// compileRegexForXSDVersion is compileRegex, but selecting the STRICT XSD 1.0
// character-class grammar instead of this engine's default XSD-1.1-lenient
// one when the caller has explicitly declared xsd-version 1.0 in force
// (re00056/re00086/re00102a: a chained range like [a-d-b-c] is FORX0002
// under 1.0, but legal under 1.1's rewritten charGroupPart production — see
// RegexGrammar's own doc comment for why the engine defaults to the lenient
// 1.1 reading absent this signal).
func compileRegexForXSDVersion(pattern, flags, xsdVersion string) (Regex, error) {
	g := GrammarXPath
	if xsdVersion == "1.0" {
		g = GrammarXSD10
	}
	return compileRegexOpts(pattern, flags, true, true, g)
}

// checkRegexBraces rejects an unescaped '{' or '}' outside a character class
// that is not a {n,m} quantifier or a \p{…}/\P{…} property (FORX0002 —
// XSLT30 analyze-string-097's '(\{[^}]+})', which RE2 would read literally).
func checkRegexBraces(p string) error {
	rs := []rune(p)
	depth := 0
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\\':
			i++
			if i < len(rs) && (rs[i] == 'p' || rs[i] == 'P') {
				for i < len(rs) && rs[i] != '}' {
					i++
				}
			}
		case '[':
			depth++
		case ']':
			if depth > 0 {
				depth--
			}
		case '{':
			if depth > 0 {
				continue
			}
			j, digits := i+1, 0
			for j < len(rs) && rs[j] >= '0' && rs[j] <= '9' {
				j, digits = j+1, digits+1
			}
			if j < len(rs) && rs[j] == ',' {
				for j++; j < len(rs) && rs[j] >= '0' && rs[j] <= '9'; j++ {
				}
			}
			if digits == 0 || j >= len(rs) || rs[j] != '}' {
				return fmt.Errorf("err:FORX0002: invalid regular expression %q: unescaped '{'", p)
			}
			i = j
		case '}':
			if depth == 0 {
				return fmt.Errorf("err:FORX0002: invalid regular expression %q: unescaped '}'", p)
			}
		}
	}
	return nil
}

// compileRegexOpts compiles an XPath/XSD regex with optional flags into a Go
// RE2 regexp. Supported flags: i (case-insensitive), s (dot-all), m
// (multiline), x (whitespace-insensitive), q (literal). strictBraces applies
// checkRegexBraces.
func compileRegexOpts(pattern, flags string, strictBraces, allowBackref bool, grammar RegexGrammar) (Regex, error) {
	var goFlags string
	literal := false
	ignoreWS := false
	for _, f := range flags {
		switch f {
		case 'i', 's', 'm':
			goFlags += string(f)
		case 'x':
			ignoreWS = true
		case 'q':
			literal = true
		default:
			return nil, fmt.Errorf("err:FORX0001: invalid regex flag %q", f)
		}
	}
	if literal {
		pattern = regexp.QuoteMeta(pattern)
	} else {
		if ignoreWS {
			// F&O 3.1 §5.6.2's own worked example: the 'x' flag strips every
			// whitespace CHARACTER from the pattern TEXT before anything else
			// looks at it (character classes excepted) — including one
			// immediately after a backslash, so "hello\ sworld" becomes
			// "hello\sworld" (a \s escape), not a protected literal space.
			// This must happen before brace-checking, grammar-checking, AND
			// escape parsing all see the pattern, so it is applied to
			// `pattern` itself, once, here — not re-derived separately by
			// each downstream check.
			pattern = stripRegexWSOutsideClasses(pattern)
		}
		if strictBraces {
			if err := checkRegexBraces(pattern); err != nil {
				return nil, err
			}
			// The full F&O 3.1 §5.6.1 grammar check (shared with the XSD
			// pattern-facet path — see regex_grammar.go). RE2 silently accepts
			// a range of shapes the grammar forbids: chained ranges
			// ([a-d-b-c]), a nested '[' or an empty class ([^[a-b]], a[]]b),
			// a subtraction with no base ([-[e-g]) or not in final position,
			// and a ']' outside a class (a]) — all FORX0002.
			if err := ValidateRegexGrammar(pattern, grammar); err != nil {
				return nil, fmt.Errorf("err:FORX0002: %v", err)
			}
		}
		pattern = expandClassSubtraction(pattern)
		var err error
		var sawBackref bool
		pattern, sawBackref, err = xsdRegexToGo(pattern, ignoreWS, strings.Contains(goFlags, "s"), strings.Contains(goFlags, "i"), allowBackref)
		if err != nil {
			return nil, err
		}
		if sawBackref {
			// RE2 has no back-references at all; this pattern gets the
			// backtracking matcher instead (see regex_backref.go).
			return compileBackref(pattern, goFlags)
		}
	}
	if goFlags != "" {
		pattern = "(?" + goFlags + ")" + pattern
	}
	return regexp.Compile(pattern)
}

// xsdRegexToGo translates the XSD-regex-specific constructs our RE2 engine does
// not understand natively: the multi-character escapes \i \I \c \C, and (under
// the 'x' flag) whitespace removal that respects character classes and escapes.
// Constructs RE2 cannot express at all (back-references, class subtraction) are
// left for regexp.Compile to reject. Outside dot-all mode the XSD/XPath '.'
// excludes BOTH #xA and #xD (RE2's excludes only #xA), so it becomes [^\n\r]
// (fn-replace-43).
//
// caseInsensitive marks the 'i' flag: F&O §5.6.1.1 says the category escapes
// \p{…}/\P{…} match exactly what they match without the flag, whereas RE2's
// (?i) extends \p{Lu} to lowercase letters — so outside a class they are
// wrapped in a case-sensitive group (caselessmatch14/15).
func xsdRegexToGo(p string, ignoreWS, dotAll, caseInsensitive, allowBackref bool) (string, bool, error) {
	sawBackref := false
	iClass := NameStartClass // XSD \i — XML NameStartChar
	cClass := NameCharClass  // XSD \c — XML NameChar
	var b strings.Builder
	inClass := false
	rs := []rune(p)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '\\' && i+1 < len(rs) {
			n := rs[i+1]
			switch n {
			case 'i', 'I', 'c', 'C':
				cls := iClass
				if n == 'c' || n == 'C' {
					cls = cClass
				}
				neg := n == 'I' || n == 'C'
				if inClass {
					if neg {
						// \\I / \\C inside a POSITIVE class are legal XSD
						// (App.F charClassEsc includes MultiCharEsc): they
						// contribute the complement-of-Name(Start)Char set
						// (reG18-21, xv100notc/noti, addB058).
						cb := NameStartComplement
						if n == 'C' {
							cb = NameCharComplement
						}
						b.WriteString(cb)
					} else {
						b.WriteString(cls)
					}
				} else if neg {
					b.WriteString("[^" + cls + "]")
				} else {
					b.WriteString("[" + cls + "]")
				}
				i++
				continue
			case 'd', 'D', 'w', 'W', 's', 'S':
				// XSD multi-character escapes use Unicode semantics, unlike Go RE2's
				// ASCII \d/\w/\s: \d=\p{Nd}, \w=[^\p{P}\p{Z}\p{C}], \s=[ \t\n\r].
				var body string
				// \d/\s and \W are positive sets; \D/\S and \w (= complement of
				// punctuation/separator/other) are negated.
				neg := n == 'D' || n == 'S' || n == 'w'
				switch n {
				case 'd', 'D':
					body = `\p{Nd}`
				case 'w', 'W':
					body = `\p{P}\p{Z}\p{C}` // \w is the complement of this set
				case 's', 'S':
					body = `\x{20}\t\n\r`
				}
				switch {
				case inClass && !neg:
					b.WriteString(body)
				case inClass && n == 'D':
					b.WriteString(`\P{Nd}`)
				case inClass && n == 'w':
					// \w = ¬(P∪Z∪C) = exactly L∪M∪N∪S — no underscore (reZ002)
					b.WriteString(`\p{L}\p{M}\p{N}\p{S}`)
				case inClass && n == 'S':
					// \S = ¬{space, tab, nl, cr}, emitted as explicit ranges (reG25)
					b.WriteString(`\x00-\x08\x0B\x0C\x0E-\x1F\x{21}-\x{10FFFF}`)
				case neg:
					b.WriteString("[^" + body + "]")
				default:
					b.WriteString("[" + body + "]")
				}
				i++
				continue
			case 'p', 'P':
				// \p{...}/\P{...}: XSD Unicode block names (\p{IsBasicLatin}) are
				// unknown to RE2, so translate them to explicit ranges; categories
				// and scripts (\p{L}, \p{Greek}) pass through.
				if i+2 < len(rs) && rs[i+2] == '{' {
					j := i + 3
					for j < len(rs) && rs[j] != '}' {
						j++
					}
					if j >= len(rs) {
						return "", false, fmt.Errorf("err:FORX0002: unterminated \\%c{", n)
					}
					name := string(rs[i+3 : j])
					neg := n == 'P'
					// Cn (unassigned) has no RE2 category: emit the complement
					// of every ASSIGNED category (surrogates cannot occur as
					// runes, Cs needs no mention) — reJ80's \p{Cn}* is valid
					// XSD. Inside a class the union form is inexpressible;
					// leave that to the compile stage.
					if name == "Cn" && !inClass {
						const assigned = `\p{L}\p{M}\p{N}\p{P}\p{S}\p{Z}\p{Cc}\p{Cf}\p{Co}`
						if neg {
							b.WriteString("[" + assigned + "]")
						} else {
							b.WriteString("[^" + assigned + "]")
						}
						i = j
						continue
					}
					if strings.HasPrefix(name, "Is") {
						if inClass && neg {
							// \P{IsBlock} inside a class: RE2 can't negate here, so
							// emit the block's complement as explicit ranges.
							cbody, ok := xsdBlockComplementBody(name[2:])
							if !ok {
								return "", false, fmt.Errorf("err:FORX0002: unknown Unicode block %q", name)
							}
							b.WriteString(cbody)
							i = j
							continue
						}
						body, ok := xsdBlockClassBody(name[2:])
						if !ok {
							return "", false, fmt.Errorf("err:FORX0002: unknown Unicode block %q", name)
						}
						b.WriteString(blockClass(body, neg, inClass))
						i = j
						continue
					}
					if caseInsensitive && !inClass {
						b.WriteString("(?-i:" + string(rs[i:j+1]) + ")") // category/script, case-exact
					} else {
						b.WriteString(string(rs[i : j+1])) // category/script → RE2
					}
					i = j
					continue
				}
				b.WriteRune(r)
				b.WriteRune(n)
				i++
				continue
			default:
				// XSD regex permits only a fixed set of letter escapes; others
				// (\b, \B word boundaries; \x, \z, \a, …) are invalid even though
				// RE2 would accept some of them.
				if (n >= 'a' && n <= 'z' || n >= 'A' && n <= 'Z') && !strings.ContainsRune("nrtdDsSwW", n) {
					return "", false, fmt.Errorf("err:FORX0002: invalid escape \\%c", n)
				}
				// A backslash before a digit is neither an octal escape nor a
				// back-reference in the XSD/XPath grammar — both are absent — so
				// RE2's octal interpretation must not leak through (re00615-626,
				// re00785, re00820, re00997).
				if n >= '0' && n <= '9' {
					// A back-reference is legal in the XPath F&O grammar (but
					// never inside a character class, and never \0). The RE2
					// path still rejects it — only the XPath entry points pass
					// allowBackref, and the pattern then goes to the
					// backtracking matcher in regex_backref.go.
					if !allowBackref || inClass || n == '0' {
						return "", false, fmt.Errorf("err:FORX0002: back-reference/octal escape \\%c is not allowed", n)
					}
					sawBackref = true
					b.WriteRune(r)
					b.WriteRune(n)
					i++
					continue
				}
				b.WriteRune(r)
				b.WriteRune(n)
				i++
				continue
			}
		}
		switch r {
		case '.':
			if !inClass && !dotAll {
				b.WriteString(`[^\n\r]`)
				continue
			}
		case '{':
			// A '{' outside a class must open a well-formed quantifier
			// ({n}, {n,}, {n,m}); it is not a normal Char, so RE2's
			// treat-as-literal fallback must not leak through (re00032,
			// re00521, re00567-569).
			if !inClass && !validQuantifierAt(rs, i) {
				return "", false, fmt.Errorf("err:FORX0002: invalid quantifier")
			}
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '(':
			// XSD allows only "(" and "(?:"; inline-flag groups "(?i)", named
			// groups "(?<n>", and look-around "(?=" are not part of the grammar.
			if !inClass && i+1 < len(rs) && rs[i+1] == '?' {
				if i+2 >= len(rs) || rs[i+2] != ':' {
					return "", false, fmt.Errorf("err:FORX0002: extended group (?…) is not allowed in XSD regular expressions")
				}
			}
		case ' ', '\t', '\n', '\r':
			if ignoreWS && !inClass {
				continue // 'x' flag: ignore whitespace outside classes
			}
		}
		b.WriteRune(r)
	}
	return b.String(), sawBackref, nil
}

// validQuantifierAt reports whether rs[i:] (rs[i]=='{') is a well-formed XSD
// quantifier: '{' QuantExact ( ',' QuantMax? )? '}', where QuantExact is one or
// more digits.
func validQuantifierAt(rs []rune, i int) bool {
	j := i + 1
	start := j
	for j < len(rs) && rs[j] >= '0' && rs[j] <= '9' {
		j++
	}
	if j == start {
		return false // no lower bound
	}
	if j < len(rs) && rs[j] == ',' {
		j++
		for j < len(rs) && rs[j] >= '0' && rs[j] <= '9' {
			j++
		}
	}
	return j < len(rs) && rs[j] == '}'
}

// xpathReplToGo converts XPath replacement syntax ($N, \$, \\) to Go's ${N}.
// ngroups is the pattern's capturing-group count: when several digits follow
// the '$', the longest prefix that names an existing group is the reference
// and the remaining digits are literal ("$1520" with 15 groups is group 15
// followed by "20"; "$17" with 16 groups is group 1 followed by "7" —
// fn-replace-41/42). A single digit beyond the group count expands to "".
func xpathReplToGo(s string, ngroups int) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if i+1 < len(s) && s[i+1] == '$' {
				b.WriteString("$$") // \$ is a literal dollar (fn-replace-38)
				i++
				continue
			}
			if i+1 < len(s) && s[i+1] == '\\' {
				b.WriteByte('\\')
				i++
				continue
			}
			b.WriteByte('\\')
		case '$':
			j := i + 1
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if j > i+1 {
				digits := s[i+1 : j]
				k := len(digits)
				for k > 1 {
					if n, err := strconv.Atoi(digits[:k]); err == nil && n <= ngroups {
						break
					}
					k--
				}
				b.WriteString("${" + digits[:k] + "}")
				i += k
				continue
			}
			b.WriteString("$$")
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func fnLang(c *Context, a []Object) (Object, error) {
	want := strings.ToLower(ToString(arg(a, 0)))
	// The node is the explicit second argument, else the context item — which
	// must be a node (fn-lang-15/22 error on a non-node context).
	var node *xmltree.Node
	if len(a) > 1 {
		n, ok := firstItem(arg(a, 1)).(*xmltree.Node)
		if !ok {
			return nil, fmt.Errorf("err:XPTY0004: fn:lang second argument must be a node")
		}
		node = n
	} else if c.Node != nil {
		node = c.Node
	} else if n, ok := c.CtxItem.(*xmltree.Node); ok {
		node = n
	} else if c.CtxItem != nil {
		return nil, fmt.Errorf("err:XPTY0004: fn:lang context item is not a node")
	} else {
		return nil, fmt.Errorf("err:XPDY0002: fn:lang has no context node")
	}
	for n := node; n != nil; n = n.Parent {
		if v, ok := n.Attr("http://www.w3.org/XML/1998/namespace", "lang"); ok {
			lv := strings.ToLower(v)
			return lv == want || strings.HasPrefix(lv, want+"-"), nil
		}
	}
	return false, nil
}

// fnString implements fn:string: FOTY0014 for a map/array/function argument,
// XPDY0002 when there is no context item to default to.
func fnString(c *Context, a []Object) (Object, error) {
	if len(a) == 0 {
		if c.Node == nil && c.CtxItem == nil {
			return nil, fmt.Errorf("err:XPDY0002: fn:string() has no context item")
		}
		return ToString(argOrContext(c, a)), nil
	}
	switch firstItem(a[0]).(type) {
	case *Map, *Array, *Function:
		return nil, fmt.Errorf("err:FOTY0014: a map/array/function has no string value")
	}
	// $arg is item()?: a sequence of two or more items is XPTY0004
	// (K-StringFunc-6, fo-test-fn-string-004).
	if n := len(Items(a[0])); n > 1 {
		return nil, fmt.Errorf("err:XPTY0004: fn:string requires at most one item, got %d", n)
	}
	return ToString(a[0]), nil
}

// validReplacement reports whether an fn:replace replacement string is
// well-formed: '\' and '$' must each be followed by an allowed character
// (FORX0004).
func validReplacement(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if i+1 >= len(s) || (s[i+1] != '\\' && s[i+1] != '$') {
				return false
			}
			i++
		case '$':
			if i+1 >= len(s) || s[i+1] < '0' || s[i+1] > '9' {
				return false
			}
		}
	}
	return true
}
