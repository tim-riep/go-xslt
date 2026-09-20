package xpath

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// rangeMaterializeLimit caps the size of an integer range ("a to b"). Our value
// model is eager, so a range larger than this would exhaust memory; stress tests
// use such ranges to probe streaming engines, which we are not.
const rangeMaterializeLimit = 10_000_000

// rangeOperand atomizes an operand of the `to` operator: () → absent (the range
// is empty — K-RangeExpr-5), a single xs:integer (an untypedAtomic is cast to
// one) → its value, and anything else — decimals and doubles included — is
// XPTY0004 (K-RangeExpr-33..36: 1.1 to 3). Integers beyond int64 clamp, which
// the length guard then turns into an error or an empty range.
func rangeOperand(ctx *Context, o Object) (int64, bool, error) {
	items, err := Atomize(o)
	if err != nil {
		return 0, false, err
	}
	if len(items) == 0 {
		return 0, false, nil
	}
	if len(items) > 1 {
		// XSLT's own backwards-compatible processing (ctx.BC10) restores the
		// function conversion rules' first-item cardinality relaxation here
		// too: "backwards-042: in range expressions (M to N), M or N can be
		// a sequence of integers, and the first-item rule holds".
		if ctx != nil && ctx.BC10 {
			items = items[:1]
		} else {
			return 0, false, fmt.Errorf("err:XPTY0004: range operand is a sequence of %d items", len(items))
		}
	}
	a := items[0].(*Atomic)
	if a.T == XSuntypedAtomic {
		if a, err = CastTo(a, XSinteger); err != nil {
			return 0, false, err
		}
	}
	if !isIntegerType(a.T) || a.i == nil {
		return 0, false, fmt.Errorf("err:XPTY0004: %s is not a valid range operand", a.T)
	}
	if !a.i.IsInt64() {
		if a.i.Sign() < 0 {
			return -1 << 63, true, nil
		}
		return 1<<63 - 1, true, nil
	}
	return a.i.Int64(), true, nil
}

// VarResolver resolves variable references.
type VarResolver interface {
	ResolveVar(prefix, local string) (Object, bool)
}

// NamespaceResolver maps a namespace prefix to its URI for the expression's
// static context.
type NamespaceResolver interface {
	ResolveNS(prefix string) (string, bool)
}

// FuncResolver resolves non-core (extension / XSLT) functions. Core XPath
// functions are handled internally and need not be provided.
type FuncResolver interface {
	ResolveFunc(prefix, local string, args []Object, ctx *Context) (Object, bool, error)
}

// Context is the evaluation context for an XPath expression.
type Context struct {
	Node    *xmltree.Node // context node
	Pos     int           // context position (1-based)
	Size    int           // context size
	CtxItem Item          // context item (non-node atomic, for simple map / "." over atomics)
	// NoOutputURI suppresses fn:current-output-uri(): it marks an evaluation
	// that is NOT writing a result document — a match/grouping PATTERN, or a
	// function item called dynamically — where XSLT 3.0 leaves the current
	// output URI absent (current-output-uri-008/016/017).
	NoOutputURI bool
	Vars        VarResolver
	NS          NamespaceResolver
	Funcs       FuncResolver
	// ResolveNamedFunction is an OPTIONAL host hook (set by the XSLT engine;
	// nil for every other caller, including plain XPath/XQuery use of this
	// package) that resolves a named function reference to a callable
	// function item OUTSIDE the built-in F&O catalog — specifically, a
	// user-declared XSLT xsl:function, which this package cannot see on its
	// own (internal/xpath does not, and must not, import internal/xslt).
	// Consulted by fn:function-lookup (function-lookup-001/002: an
	// xsl:function-declared function must be findable through the DYNAMIC
	// fn:function-lookup(name, arity) call, not just a static named-
	// function-reference at the call site) when the built-in catalog has no
	// match for the given (namespace, local, arity). Never consulted when
	// the name resolves into a reserved namespace (fn:/math:/map:/array:/
	// xs:) — isReservedNS already bars an xsl:function from ever living
	// there, so no ambiguity between a built-in and a user function can
	// arise.
	ResolveNamedFunction func(ns, local string, arity int) (*Function, bool)
	// ViaFunctionItem marks a context that was CAPTURED into a function item
	// by a named function reference, rather than one a direct call sees.
	// XSLT 3.0 §10.3.6 draws exactly this line for a context-dependent
	// function: "the returned function value includes in its closure a copy of
	// the static and dynamic context ... In the case where the context item is
	// a node in a streamed input document, saving the node is not possible.
	// In this case, therefore, the context is saved with an absent focus".
	// Only the host (the XSLT layer) knows which documents are streamed, so
	// the flag records the capture and the host decides what follows from it.
	ViaFunctionItem  bool
	Resolver         ResourceResolver         // resolves URIs for unparsed-text/doc/collection (may be nil)
	BaseURI          string                   // static base URI of the context document (may be "")
	HomeDoc          *xmltree.Node            // document root of the module containing the expression (XSLT only; nil otherwise) — doc('')/document('') return this directly rather than re-resolving through Resolver
	DecimalFormats   map[string]DecimalFormat // named xsl:decimal-format symbols for format-number (may be nil)
	DefaultElemNS    string                   // xpath-default-namespace: namespace for unprefixed element name tests
	DefaultCollation func(a, b string) int    // [xsl:]default-collation in scope (nil = codepoint collation)
	RootlessTree     bool                     // '/' errors unless the tree root is a document node (XSD assertion trees)
	NoFocus          bool                     // no focus at all: '.', position(), last() raise XPDY0002 (XSD simple-type assertions)
	// HostPrefixesOnly says the host (XSLT) predeclares no function-namespace
	// prefix: a "fn:" call must find fn bound in NS or it is XPST0081
	// (namespace-6202). Standalone XPath keeps fn predeclared.
	HostPrefixesOnly bool
	// PatternTop, when non-nil, switches the child and attribute axes to the
	// XSLT 3.0 PATTERN axes child-or-top / attribute-or-top: on the node named
	// here — the top of the tree the pattern is being matched against — those
	// axes ALSO select the context node itself, because a parentless node is
	// nobody's child yet a pattern such as "x/(a|b)" must still reach the
	// children of a parentless <x> (match-265..269/282). It is set ONLY by
	// pattern matching (matchExprPattern); ordinary XPath evaluation leaves it
	// nil and is completely unaffected.
	PatternTop *xmltree.Node
	// SchemaTypes is the optional injected hook a schema-aware caller supplies
	// for derivation-aware type-test/instance-of matching — see
	// SchemaTypeResolver. Nil (the default) means no schema-derivation
	// information is available at all.
	SchemaTypes SchemaTypeResolver
	// XMLVersion is the host's declared XML version ("1.0" is the default
	// when empty, else "1.1"). Currently consulted only by
	// fn:codepoints-to-string's FOCH0001 Char-production check
	// (K-CodepointToStringFunc-8/8a/11/11b/12/12b: the identical codepoint
	// is legal under 1.1's wider Char production — [#x1-#xD7FF] etc,
	// excluding only NUL and the surrogate/noncharacter blocks — but
	// illegal under 1.0's, which additionally excludes every C0 control
	// other than tab/LF/CR).
	XMLVersion string
	// XSDVersion is the host's declared XSD datatype version ("1.0" is the
	// default when empty, else "1.1"). Currently consulted only by the
	// xs:double/xs:float cast/constructor's "+INF" literal (K2-SeqExprCast-
	// 231/231a/232/232a, xs-double-004, xs-float-004): XSD 1.0's lexical
	// grammar for the special values is "INF, -INF and NaN" only, while XSD
	// 1.1 explicitly widens it to "INF, '+INF', -INF, and NaN" — the shared,
	// context-free CastTo stays permissive (used by the schema validator,
	// which enforces its own version-specific facets separately); only
	// castInContext (the XPath cast-expression path) checks this.
	XSDVersion string
	// BC10 marks XPath 1.0 backward-compatibility mode (XSLT's own
	// [xsl:]version="1.0", or the QT3 catalog's "xpath-1.0-compatibility"
	// feature). Currently consulted only by fn:generate-id: XPath 1.0's
	// conversion rules take the node-set's FIRST node in document order
	// where 2.0+ requires a true singleton, so generate-id(//*) — a
	// multi-node argument — succeeds instead of XPTY0004
	// (generate-id-018: it then equals generate-id(/*), the root element,
	// which sorts first).
	BC10 bool
	// DefaultLanguage is the host's declared default language (an
	// xs:language lexical value, e.g. "fr-CA") for fn:default-language().
	// Empty (the default) falls back to "en" — the QT3 catalog's own
	// dependency type="default-language" value="..." is a HOST
	// CONFIGURATION the harness sets per test case (default-language-006);
	// every other caller leaves this empty and keeps the old hardcoded
	// "en" behavior.
	DefaultLanguage string
	Now        time.Time   // host-supplied fixed "current instant" for a whole transformation/query (zero = fall back to time.Now() lazily) — see nowCache
	nowCache    *nowBox     // ONE current instant per evaluation (fn:current-*)
	locals      *localScope // for/let/quantified/inline-function bindings
	evalDepth   int         // recursion guard for evalExpr (prevents stack overflow)
}

// maxEvalDepth bounds the static recursion of one expression evaluation. Real
// XPath nests only a few dozen deep; a pathological or cyclic AST that would
// otherwise overflow the goroutine stack (unrecoverable) errors out instead.
const maxEvalDepth = 3000

// ResourceResolver resolves a URI to external content for fn:unparsed-text,
// fn:doc and fn:collection. Relative URIs are resolved against Context.BaseURI by
// the caller before lookup. A nil resolver makes those functions report the
// resource as unavailable.
type ResourceResolver interface {
	// ResolveText returns the text content of a URI and whether it exists.
	ResolveText(uri string) (string, bool)
	// ResolveDoc returns the parsed document node for a URI and whether it exists.
	ResolveDoc(uri string) (*xmltree.Node, bool)
}

// CollectionResolver is the optional extension a ResourceResolver may also
// implement to answer fn:collection. The empty URI names the default
// collection. A resolver that does not implement it leaves every collection
// unavailable (and the default collection empty).
type CollectionResolver interface {
	// ResolveCollection returns the nodes of the collection named by uri
	// (already resolved against the static base URI) and whether such a
	// collection exists.
	ResolveCollection(uri string) ([]*xmltree.Node, bool)
}

// SchemaTypeResolver answers schema-type derivation questions a schema-aware
// caller supplies via Context.SchemaTypes — the injected hook that lets
// element(name,type)/instance-of/etc. do DERIVATION-aware matching ("is this
// node's type T, or a subtype of T") without internal/xpath importing
// internal/xsd (which would cycle: xmltree <- xpath <- xsd). Exact type-name
// IDENTITY needs no hook at all — xmltree.Node.SchemaType is compared
// directly. A nil resolver (the default: no schema imported, or a
// non-schema-aware caller) makes every derivation query answer false, which
// is the correct, conservative reading of "nothing is known to derive from
// anything" rather than an error.
type SchemaTypeResolver interface {
	// DerivesFrom reports whether got IS want, or derives from it by
	// extension/restriction (for a type) or substitution-group membership
	// (for an element declaration's type).
	DerivesFrom(got, want xmltree.SchemaTypeName) bool
}

// localScope is a chain of in-expression variable bindings (for/let/etc.).
type localScope struct {
	parent *localScope
	key    string
	val    Object
}

func (s *localScope) lookup(k string) (Object, bool) {
	for ; s != nil; s = s.parent {
		if s.key == k {
			return s.val, true
		}
	}
	return nil, false
}

// withLocal returns a child context with an additional local binding.
func withLocal(ctx *Context, key string, val Object) *Context {
	c := *ctx
	c.locals = &localScope{parent: ctx.locals, key: key, val: val}
	return &c
}

// varKey computes the lookup key for a variable reference (Clark notation).
func varKey(ctx *Context, prefix, local string) string {
	if prefix == "" {
		return local
	}
	uri := ""
	if bracedURI, ok := bracedVarPrefix(prefix); ok {
		// $Q{uri}local (splitVarName): the URI is explicit.
		uri = bracedURI
	} else if ctx.NS != nil {
		uri, _ = ctx.NS.ResolveNS(prefix)
	}
	if uri == "" {
		return local
	}
	return "{" + uri + "}" + local
}

// bracedVarPrefix recognises the "Q{uri}" pseudo-prefix splitVarName produces
// for a braced variable name, returning the URI.
func bracedVarPrefix(prefix string) (string, bool) {
	if strings.HasPrefix(prefix, "Q{") && strings.HasSuffix(prefix, "}") {
		return prefix[2 : len(prefix)-1], true
	}
	return "", false
}

// varPrefixKey normalises a variable's prefix for static (parse-time)
// comparison: the braced no-namespace form Q{} is the same as no prefix.
func varPrefixKey(prefix string) string {
	if uri, ok := bracedVarPrefix(prefix); ok {
		return uri
	}
	return prefix
}

func firstItem(o Object) Item {
	items := Items(o)
	if len(items) == 0 {
		return ""
	}
	return items[0]
}

// asPositionalInt reports whether a predicate value is a numeric (positional)
// predicate, returning its integer value. Atomizing first (rather than a raw
// type switch on o) recovers a singleton numeric even when it arrives as a
// *xmltree.Node — a for-each/filter/analyze-string context item over an
// atomic sequence is modelled as a synthetic, TypeAnno-stamped text node
// (see execForEach), so a variable bound to "." there and then used as a
// predicate ($expected[$n]) must still be recognized as positional, not
// silently fall through to an always-true effective-boolean-value test
// (format-date-en-013/014).
func asPositionalInt(o Object) (int, bool) {
	items := Items(o)
	if len(items) != 1 {
		return 0, false
	}
	// "If the value of the predicate expression is a singleton ATOMIC VALUE
	// of a numeric type ... otherwise the effective boolean value"
	// (XPath 3.1 §3.3.3). A NODE is not an atomic value, however its typed
	// value atomizes — which is the whole reason a[@b] is an existence test
	// and not a position test. Atomizing first (as this did) only ever agreed
	// with the spec by accident, because an unvalidated node atomizes to
	// xs:untypedAtomic, which is not numeric: the moment a schema annotates
	// @b as xs:integer, $d[self::node()] silently became "position() eq 8"
	// (import-schema-026/027/028). The same trap reaches a SynthCtx wrapper
	// node standing for an atomic for-each item.
	if _, isNode := items[0].(*xmltree.Node); isNode {
		return 0, false
	}
	// Bare Go scalars still reach here as items (the engine's XPath-1.0-era
	// representation of a number), so atomize the single non-node item rather
	// than requiring an *Atomic outright.
	a, err := toAtomic(items[0])
	if err != nil || !a.IsNumeric() {
		return 0, false
	}
	return positionalIndex(a.Float()), true
}

// positionalIndex maps a numeric predicate value to the position it selects.
// A non-integral value or NaN equals no position at all — (1, 2, 3)[1.1] is
// empty (K-FilterExpr-11/12) — so it maps to -1, which never matches.
func positionalIndex(f float64) int {
	if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) {
		return -1
	}
	return int(f)
}

// Eval evaluates the parsed expression in ctx.
func (p *Parsed) Eval(ctx *Context) (Object, error) {
	if err := checkStaticVarRefPrefixes(p.root, ctx); err != nil {
		return nil, err
	}
	return evalExpr(p.root, ctx)
}

// checkStaticVarRefPrefixes performs the ONE static, lexical check XPath 3.1's
// "expressions must not be rewritten in such a way as to create or remove
// static errors" requirement needs for a namespace-prefixed variable
// reference: resolving a QName's namespace prefix is a STATIC check,
// independent of whether the branch containing it is ever dynamically
// reached (errors-and-optimization-7: `if (true()) then 1 else let
// $unbound:var := 2 return $unbound:var` must raise XPST0081 even though the
// `else` branch referencing $unbound:var is never evaluated). Walking the
// WHOLE parsed tree unconditionally — rather than deferring to the lazy
// per-VarRef lookup evalExpr already does when a reference is actually
// reached — is what makes this static rather than dynamic. Deliberately
// narrow: only $prefix:name variable references are checked (not function
// calls, type names, or node-test prefixes, each already enforced by its own
// existing static check elsewhere), and only the prefix's NAMESPACE BINDING
// (not whether a variable of that name is actually declared — that really is
// dynamic, XPath having no static variable-declaration scope of its own for
// a bare expression).
func checkStaticVarRefPrefixes(root Expr, ctx *Context) error {
	var ns NamespaceResolver
	if ctx != nil {
		ns = ctx.NS
	}
	var staticErr error
	strmWalk(root, func(e Expr) bool {
		if staticErr != nil {
			return false
		}
		v, ok := e.(*VarRef)
		if !ok || v.Prefix == "" || strings.HasPrefix(v.Prefix, "Q{") {
			// An empty prefix needs no binding; a braced-EQName pseudo-prefix
			// ($Q{uri}local) already carries its namespace URI literally, with
			// no lexical prefix to resolve at all (see evalEnv.ResolveVar).
			return true
		}
		if ns == nil {
			staticErr = fmt.Errorf("err:XPST0081: prefix %q has no namespace binding", v.Prefix)
			return false
		}
		if _, ok := ns.ResolveNS(v.Prefix); !ok {
			staticErr = fmt.Errorf("err:XPST0081: prefix %q has no namespace binding", v.Prefix)
			return false
		}
		return true
	})
	return staticErr
}

func evalExpr(e Expr, ctx *Context) (Object, error) {
	if ctx != nil {
		ctx.evalDepth++
		if ctx.evalDepth > maxEvalDepth {
			ctx.evalDepth--
			return nil, fmt.Errorf("err:XPDY0001: expression nesting too deep")
		}
		defer func() { ctx.evalDepth-- }()
	}
	switch n := e.(type) {
	case *LiteralExpr:
		return n.Val, nil
	case *VarRef:
		if v, ok := ctx.locals.lookup(varKey(ctx, n.Prefix, n.Local)); ok {
			return v, nil
		}
		if ctx.Vars != nil {
			if v, ok := ctx.Vars.ResolveVar(n.Prefix, n.Local); ok {
				return v, nil
			}
		}
		return nil, fmt.Errorf("undefined variable $%s", qn(n.Prefix, n.Local))
	case *FuncCall:
		return evalFunc(n, ctx)
	case *UnaryExpr:
		v, err := evalExpr(n.X, ctx)
		if err != nil {
			return nil, err
		}
		return unaryArith(ctx, v, !n.Plus)
	case *BinaryExpr:
		return evalBinary(n, ctx)
	case *UnionExpr:
		l, err := evalExpr(n.L, ctx)
		if err != nil {
			return nil, err
		}
		r, err := evalExpr(n.R, ctx)
		if err != nil {
			return nil, err
		}
		ls, err := nodeSetOperand(l, "union")
		if err != nil {
			return nil, err
		}
		rs, err := nodeSetOperand(r, "union")
		if err != nil {
			return nil, err
		}
		return NodeSet(append(append(NodeSet{}, ls...), rs...)).normalize(), nil
	case *FilterExpr:
		return evalFilter(n, ctx)
	case *PathExpr:
		return evalPath(n, ctx)
	case *ContextItemExpr:
		if ctx.CtxItem != nil {
			return FromItems([]Item{ctx.CtxItem}), nil
		}
		if ctx.Node != nil {
			// A SynthCtx text node is not a real node: it is the host's
			// stand-in for an ATOMIC context item (xsl:for-each /
			// xsl:apply-templates / xsl:analyze-string over a non-node
			// sequence). When the host recorded the item's original type
			// (TypeAnno), "." must yield that ATOMIC VALUE, not the stand-in
			// node — otherwise "." instance of xs:integer, ". gt 0" and
			// friends see a node and silently answer the wrong thing
			// (match-127..135, match-240*).
			if ctx.Node.SynthCtx {
				if a, ok := nodeTypedValue(ctx.Node); ok {
					return FromItems([]Item{a}), nil
				}
			}
			return NodeSet{ctx.Node}, nil
		}
		if ctx.NoFocus {
			return nil, fmt.Errorf("err:XPDY0002: the context item is absent")
		}
		return Sequence{}, nil
	case *CompareExpr:
		return evalCompareExpr(n, ctx)
	case *StringConcatExpr:
		var b strings.Builder
		for _, part := range n.Parts {
			v, err := evalExpr(part, ctx)
			if err != nil {
				return nil, err
			}
			// Each operand is xs:anyAtomicType?: atomization rejects a
			// function item (op-concat-18, FOTY0013) and more than one
			// item is XPTY0004.
			items, err := Atomize(v)
			if err != nil {
				return nil, err
			}
			if len(items) > 1 {
				return nil, fmt.Errorf("err:XPTY0004: '||' operand is a sequence of %d items", len(items))
			}
			if len(items) == 1 {
				b.WriteString(itemString(items[0]))
			}
		}
		return NewString(b.String()), nil
	case *IntersectExceptExpr:
		return evalIntersectExcept(n, ctx)
	case *InstanceOfExpr:
		if err := checkBareTypes(n.Type, ctx); err != nil {
			return nil, err
		}
		v, err := evalExpr(n.X, ctx)
		if err != nil {
			return nil, err
		}
		return MatchesSeqTypeCtx(n.Type, v, ctx), nil
	case *TreatExpr:
		if err := checkBareTypes(n.Type, ctx); err != nil {
			return nil, err
		}
		v, err := evalExpr(n.X, ctx)
		if err != nil {
			return nil, err
		}
		if !MatchesSeqTypeCtx(n.Type, v, ctx) {
			return nil, fmt.Errorf("err:XPDY0050: value does not match treated type")
		}
		return v, nil
	case *CastExpr:
		return evalCast(n, ctx)
	case *ArrowExpr:
		return evalArrow(n, ctx)
	case *SimpleMapExpr:
		return evalSimpleMap(n, ctx)
	case *NamedFuncRef:
		ref := n
		ns := ref.URI
		if ns == "" {
			var ok bool
			ns, ok = nsForKnownPrefix(ref.Prefix)
			if !ok && ctx.NS != nil {
				ns, _ = ctx.NS.ResolveNS(ref.Prefix)
			}
		}
		if fn, ok := lookupFunction(ctx, ns, ref.Local, ref.Arity); ok {
			return fn, nil
		}
		// A host-declared function (an xsl:function) referenced by name: take
		// the host's own function item, which carries its DECLARED signature,
		// rather than falling through to the untyped name-dispatch wrapper
		// below (higher-order-functions-032/033/034 test local:f#2 against
		// typed function tests).
		if ctx != nil && ctx.ResolveNamedFunction != nil && ns != "" {
			if fn, ok := ctx.ResolveNamedFunction(ns, ref.Local, ref.Arity); ok {
				return fn, nil
			}
		}
		// A KNOWN standard function referenced at an arity it does not have is
		// a static error (fn:unparsed-text#0 / #3 — XPST0017), not a deferred
		// dynamic failure.
		if lo, hi, known := stdArity(ns, ref.Local); known {
			if stdArityOutside(ns, ref.Local, lo, hi, ref.Arity) {
				return nil, fmt.Errorf("err:XPST0017: %s#%d does not exist", ref.Local, ref.Arity)
			}
		} else if ns == nsFn && ctx.Funcs == nil {
			// An fn: name that is neither implemented nor catalogued does not
			// exist (environment-variable-003/004: fn:environment-variables#0).
			// With a host resolver the name may still be a host-supplied
			// function (XSLT's current#0, key#2 …), so only a pure XPath
			// context can reject it statically.
			return nil, fmt.Errorf("err:XPST0017: function fn:%s#%d does not exist", ref.Local, ref.Arity)
		}
		// Fall back to name dispatch (host/user functions not in our registries).
		prefix := ref.Prefix
		if prefix == "" && ref.URI != "" {
			prefix = bracedPrefixForURI(ref.URI)
		}
		if prefix == "" && ref.Local == "current" && ref.Arity == 0 {
			// current() relies on the LEXICAL context of its own direct call
			// site (the innermost enclosing expression's initial focus); it
			// has no meaning called indirectly through a dereferenced
			// function item, so referencing it as current#0 and invoking it
			// dynamically is always a dynamic error (XTDE1360), regardless of
			// whether a context item happens to be available at the call
			// site.
			return sigApply(&Function{Arity: 0, Name: "current", NS: ns, Call: func(args []Object) (Object, error) {
				return nil, fmt.Errorf("err:XTDE1360: current() may not be called via a dynamic function reference")
			}}), nil
		}
		if ref.Local == "current-output-uri" && ref.Arity == 0 && (ns == nsFn || ns == "") {
			// Like current#0 above, fn:current-output-uri has no meaning when
			// reached through a function item: there is no result document
			// being written at the point of a dynamic call, so it yields the
			// empty sequence (current-output-uri-016, W3C bug 30411).
			return sigApply(&Function{Arity: 0, Name: "current-output-uri", NS: ns, Call: func(args []Object) (Object, error) {
				return Sequence{}, nil
			}}), nil
		}
		if prefix == "" && ref.Arity == 0 {
			// Same rule as current#0 above for the grouping functions: the
			// current group and current grouping key are parts of the XSLT
			// dynamic context that a function item does not capture, so a call
			// made THROUGH such an item never has them — even when the call
			// happens to sit inside some other xsl:for-each-group
			// (for-each-group-090 invokes an outer group's current-group#0
			// from inside an inner grouping and must still fail).
			switch ref.Local {
			case "current-group":
				return sigApply(&Function{Arity: 0, Name: ref.Local, NS: ns, Call: func(args []Object) (Object, error) {
					return nil, fmt.Errorf("err:XTDE1061: current-group() may not be called via a dynamic function reference")
				}}), nil
			case "current-grouping-key":
				return sigApply(&Function{Arity: 0, Name: ref.Local, NS: ns, Call: func(args []Object) (Object, error) {
					return nil, fmt.Errorf("err:XTDE1071: current-grouping-key() may not be called via a dynamic function reference")
				}}), nil
			}
		}
		if prefix == "" && ref.Local == "regex-group" && ref.Arity == 1 {
			// XSLT 3.0 §5.3.4: the captured substrings belong to the dynamic
			// context of the xsl:analyze-string that is being evaluated, and a
			// dynamic function call has none — so regex-group() reached
			// through a function item sees no captured substrings and returns
			// a zero-length string, rather than the groups of whatever
			// xsl:analyze-string happens to enclose the CALL (regex-090/091:
			// $g(2) inside a nested analyze-string is "", not "222").
			return sigApply(&Function{Arity: 1, Name: "regex-group", NS: ns, Call: func(args []Object) (Object, error) {
				return "", nil
			}}), nil
		}
		// The captured context is marked so a host function that depends on
		// the focus can tell it was reached THROUGH a function item — see
		// Context.ViaFunctionItem and XSLT 3.0 §10.3.6.
		captured := ctx
		if captured != nil && !captured.ViaFunctionItem {
			c := *captured
			c.ViaFunctionItem = true
			captured = &c
		}
		return sigApply(&Function{Arity: ref.Arity, Name: ref.Local, NS: ns, Call: func(args []Object) (Object, error) {
			return dispatchFunc(prefix, ref.Local, args, captured)
		}}), nil
	case *Placeholder:
		return nil, fmt.Errorf("argument placeholder '?' outside a function call")
	case *SequenceExpr:
		var items []Item
		for _, it := range n.Items {
			v, err := evalExpr(it, ctx)
			if err != nil {
				return nil, err
			}
			items = append(items, Items(v)...)
		}
		return FromItems(items), nil
	case *RangeExpr:
		fromV, err := evalExpr(n.From, ctx)
		if err != nil {
			return nil, err
		}
		toV, err := evalExpr(n.To, ctx)
		if err != nil {
			return nil, err
		}
		from, fromOK, err := rangeOperand(ctx, fromV)
		if err != nil {
			return nil, err
		}
		to, toOK, err := rangeOperand(ctx, toV)
		if err != nil {
			return nil, err
		}
		if !fromOK || !toOK || from > to {
			return Sequence{}, nil
		}
		// Guard against pathological ranges (e.g. "65 to 65536*65536") that would
		// materialise billions of integers and OOM. We model sequences eagerly, so
		// cap the length; any consumer of a range this large is degenerate. (A
		// negative difference here is int64 overflow — a range beyond 2^63.)
		span := to - from
		if span < 0 || span >= rangeMaterializeLimit {
			return nil, fmt.Errorf("err:FOAR0002: integer range too large (%d to %d)", from, to)
		}
		items := make([]Item, 0, span+1)
		for k := int64(0); k <= span; k++ { // by offset: from+k never overflows past to
			items = append(items, NewInteger(from+k))
		}
		return FromItems(items), nil
	case *IfExpr:
		cond, err := evalExpr(n.Cond, ctx)
		if err != nil {
			return nil, err
		}
		cb, err := EffectiveBoolErr(cond)
		if err != nil {
			return nil, err
		}
		if cb {
			return evalExpr(n.Then, ctx)
		}
		return evalExpr(n.Else, ctx)
	case *ForExpr:
		return evalFor(n.Binds, 0, ctx, n.Body)
	case *LetExpr:
		cur := ctx
		for _, b := range n.Binds {
			v, err := evalExpr(b.Seq, cur)
			if err != nil {
				return nil, err
			}
			cur = withLocal(cur, varKey(cur, b.Prefix, b.Local), v)
		}
		return evalExpr(n.Body, cur)
	case *QuantExpr:
		ok, err := evalQuant(n.Every, n.Binds, 0, ctx, n.Satisfies)
		return ok, err
	case *MapExpr:
		m := NewMap()
		for i := range n.Keys {
			kv, err := evalExpr(n.Keys[i], ctx)
			if err != nil {
				return nil, err
			}
			vv, err := evalExpr(n.Vals[i], ctx)
			if err != nil {
				return nil, err
			}
			// A map key must be EXACTLY ONE atomic value (MapConstructor-023:
			// a filtered range yielding 0 or 2+ items is XPTY0004).
			kitems, err := Atomize(kv)
			if err != nil {
				return nil, err
			}
			if len(kitems) != 1 {
				return nil, fmt.Errorf("err:XPTY0004: a map key must be a single atomic value, got %d items", len(kitems))
			}
			if m.Contains(kitems[0]) {
				return nil, fmt.Errorf("err:XQDY0137: duplicate key in map constructor")
			}
			m.Put(kitems[0], vv)
		}
		return m, nil
	case *ArrayExpr:
		return evalArray(n, ctx)
	case *InlineFunc:
		for _, pt := range n.ParamTypes {
			if err := checkBareTypes(pt, ctx); err != nil {
				return nil, err
			}
		}
		if err := checkBareTypes(n.RetType, ctx); err != nil {
			return nil, err
		}
		return evalInlineFunc(n, ctx), nil
	case *LookupExpr:
		return evalLookup(n, ctx)
	case *DynCall:
		baseV, err := evalExpr(n.Base, ctx)
		if err != nil {
			return nil, err
		}
		args := make([]Object, len(n.Args))
		isHole := make([]bool, len(n.Args))
		holes := 0
		for i, a := range n.Args {
			if _, ok := a.(*Placeholder); ok {
				isHole[i] = true
				holes++
				continue
			}
			v, err := evalExpr(a, ctx)
			if err != nil {
				return nil, err
			}
			args[i] = v
		}
		if holes == 0 {
			// The callee is exactly one item (xqhof12: a sequence of four
			// functions applied to an argument is XPTY0004).
			if bi := Items(baseV); len(bi) != 1 {
				return nil, fmt.Errorf("err:XPTY0004: dynamic function call on %d items", len(bi))
			}
			return callItemAsFunction(firstItem(baseV), args)
		}
		// Partial application of a dynamic call: $f(?, 3) — the same shape as
		// evalFunc's named-call case (fn-function-lookup-612).
		base := firstItem(baseV)
		if fn, ok := base.(*Function); ok && fn.Arity != len(n.Args) {
			return nil, fmt.Errorf("err:XPTY0004: function of arity %d called with %d arguments", fn.Arity, len(n.Args))
		}
		return &Function{Arity: holes, Call: func(supplied []Object) (Object, error) {
			filled := make([]Object, len(args))
			si := 0
			for i := range args {
				if isHole[i] {
					if si < len(supplied) {
						filled[i] = supplied[si]
						si++
					}
				} else {
					filled[i] = args[i]
				}
			}
			return callItemAsFunction(base, filled)
		}}, nil
	}
	return nil, fmt.Errorf("cannot evaluate expression %T", e)
}

// callItemAsFunction invokes an item used in function-call position: a function
// value, or a map/array (each callable with exactly one argument — key lookup
// for a map, 1-based index for an array). Used by dynamic calls and by the
// arrow operator's expression target (ArrowPostfix-020..023).
func callItemAsFunction(base Item, args []Object) (Object, error) {
	switch b := base.(type) {
	case *Function:
		// A dynamic call must supply exactly the function's arity
		// (xqhof7: concat#3("one", "two"); hof-916: a 2-hole partial
		// application called with one argument — XPTY0004).
		if len(args) != b.Arity {
			return nil, fmt.Errorf("err:XPTY0004: function of arity %d called with %d arguments", b.Arity, len(args))
		}
		return b.Call(args)
	case *Array:
		if len(args) != 1 {
			return nil, fmt.Errorf("err:XPTY0004: array invocation requires exactly one argument")
		}
		// The position is exactly one xs:integer (Lookup-211: [1,2,3](1.1)).
		i, err := sqIntegerArg("array", args[0])
		if err != nil {
			return nil, err
		}
		if i < 1 || i > b.Size() {
			return nil, fmt.Errorf("err:FOAY0001: array index %d out of bounds (1..%d)", i, b.Size())
		}
		return b.Get(i), nil
	case *Map:
		if len(args) != 1 {
			return nil, fmt.Errorf("err:XPTY0004: map invocation requires exactly one argument")
		}
		// The key argument is xs:anyAtomicType: exactly one atomic value
		// (map-call-901/902: an empty or plural key is XPTY0004).
		keys, err := Atomize(args[0])
		if err != nil {
			return nil, err
		}
		if len(keys) != 1 {
			return nil, fmt.Errorf("err:XPTY0004: map invocation requires exactly one atomic key, got %d items", len(keys))
		}
		return b.Get(keys[0]), nil
	}
	return nil, fmt.Errorf("err:XPTY0004: value is not a function")
}

func evalFor(binds []VarBind, i int, ctx *Context, body Expr) (Object, error) {
	if i == len(binds) {
		return evalExpr(body, ctx)
	}
	seqV, err := evalExpr(binds[i].Seq, ctx)
	if err != nil {
		return nil, err
	}
	key := varKey(ctx, binds[i].Prefix, binds[i].Local)
	var out []Item
	for _, it := range Items(seqV) {
		child := withLocal(ctx, key, FromItems([]Item{it}))
		r, err := evalFor(binds, i+1, child, body)
		if err != nil {
			return nil, err
		}
		out = append(out, Items(r)...)
	}
	return FromItems(out), nil
}

func evalQuant(every bool, binds []VarBind, i int, ctx *Context, test Expr) (bool, error) {
	if i == len(binds) {
		v, err := evalExpr(test, ctx)
		if err != nil {
			return false, err
		}
		return EffectiveBoolErr(v)
	}
	seqV, err := evalExpr(binds[i].Seq, ctx)
	if err != nil {
		return false, err
	}
	key := varKey(ctx, binds[i].Prefix, binds[i].Local)
	for _, it := range Items(seqV) {
		child := withLocal(ctx, key, FromItems([]Item{it}))
		ok, err := evalQuant(every, binds, i+1, child, test)
		if err != nil {
			return false, err
		}
		if every && !ok {
			return false, nil
		}
		if !every && ok {
			return true, nil
		}
	}
	return every, nil
}

func evalArray(n *ArrayExpr, ctx *Context) (Object, error) {
	if n.Curly {
		var members []Object
		if len(n.Items) > 0 {
			v, err := evalExpr(n.Items[0], ctx)
			if err != nil {
				return nil, err
			}
			for _, it := range Items(v) {
				members = append(members, FromItems([]Item{it}))
			}
		}
		return NewArray(members), nil
	}
	members := make([]Object, len(n.Items))
	for i, e := range n.Items {
		v, err := evalExpr(e, ctx)
		if err != nil {
			return nil, err
		}
		members[i] = v
	}
	return NewArray(members), nil
}

func evalInlineFunc(n *InlineFunc, ctx *Context) *Function {
	params := n.Params
	ptypes := n.ParamTypes
	rtype := n.RetType
	body := n.Body
	captured := ctx
	return &Function{
		Arity:  len(params),
		Name:   "", // inline functions are anonymous (function-name returns ())
		Typed:  true,
		Params: ptypes,
		Ret:    rtype,
		Call: func(args []Object) (Object, error) {
			// XPath 3.1 §3.1.7: the focus is ABSENT inside an inline
			// function body — '.' there is XPDY0002 (inline-fn-005), not
			// the caller's context item.
			focusless := *captured
			focusless.Node, focusless.CtxItem, focusless.Pos, focusless.Size, focusless.NoFocus = nil, nil, 0, 0, true
			// The current output URI is likewise absent inside a function
			// invoked dynamically (current-output-uri-017).
			focusless.NoOutputURI = true
			c := &focusless
			for i, p := range params {
				var av Object = Sequence{}
				if i < len(args) {
					av = args[i]
				}
				// A declared parameter type is enforced under the function
				// conversion rules — atomization + numeric promotion +
				// untypedAtomic coercion (inline-function-5: an integer arg
				// converts to the declared xs:double). Only a genuine
				// mismatch is XPTY0004 (ArrayTest-075/079).
				if i < len(ptypes) && ptypes[i] != nil {
					// The DEFINING context, so a declared parameter type
					// naming an imported schema component can still ask the
					// derivation question that decides whether the argument
					// matches (castable-005: `function($a as s:dateOrDateTime)`
					// called with an xs:date, where s:dateOrDateTime is a
					// union of xs:date and xs:dateTime — unanswerable without
					// Context.SchemaTypes).
					cv, ok := convertForParamCtx(ptypes[i], av, captured)
					if !ok {
						return nil, fmt.Errorf("err:XPTY0004: argument %d does not match the declared parameter type", i+1)
					}
					av = cv
				}
				c = withLocal(c, varKey(captured, p.Prefix, p.Local), av)
			}
			r, err := evalExpr(body, c)
			if err != nil {
				return nil, err
			}
			// The declared return type is checked under the function
			// conversion rules — e.g. an empty body against "as xs:integer"
			// is a type error (FunctionCall-048).
			if rtype != nil {
				cv, ok := convertForParamCtx(rtype, r, captured)
				if !ok {
					return nil, fmt.Errorf("err:XPTY0004: result does not match the declared return type")
				}
				r = cv
			}
			return r, nil
		},
	}
}

func evalLookup(n *LookupExpr, ctx *Context) (Object, error) {
	baseV, err := evalExpr(n.Base, ctx)
	if err != nil {
		return nil, err
	}
	// The key specifier may yield a sequence of keys ("?(1 to 2)"); each key is
	// applied to every map/array in the base sequence.
	var keys []Item
	if !n.Wild {
		kv, err := evalExpr(n.Key, ctx)
		if err != nil {
			return nil, err
		}
		keys = Items(kv)
	}
	var out []Item
	for _, it := range Items(baseV) {
		switch v := it.(type) {
		case *Map:
			if n.Wild {
				for _, k := range v.Keys() {
					out = append(out, Items(v.Get(k))...)
				}
			} else {
				for _, k := range keys {
					out = append(out, Items(v.Get(k))...)
				}
			}
		case *Array:
			if n.Wild {
				for _, m := range v.Members() {
					out = append(out, Items(m)...)
				}
			} else {
				for _, k := range keys {
					// An array lookup key must be an xs:integer value —
					// ?1.0 (decimal) is XPTY0004 (Lookup-018/019).
					if a, ok := k.(*Atomic); ok && !isIntegerType(a.T) && a.T != XSuntypedAtomic {
						return nil, fmt.Errorf("err:XPTY0004: array lookup key must be an integer, got %s", a.T)
					}
					if f, ok := k.(float64); ok && f != float64(int64(f)) {
						return nil, fmt.Errorf("err:XPTY0004: array lookup key must be an integer")
					}
					idx := int(itemNumber(k))
					if idx < 1 || idx > v.Size() {
						return nil, fmt.Errorf("err:FOAY0001: array index %d out of bounds [1,%d]", idx, v.Size())
					}
					out = append(out, Items(v.Get(idx))...)
				}
			}
		default:
			// Lookup requires a map or array on the left; anything else (node,
			// atomic, function) is a type error.
			return nil, fmt.Errorf("err:XPTY0004: lookup operator '?' requires a map or array, got %T", it)
		}
	}
	return FromItems(out), nil
}

func evalFilter(n *FilterExpr, ctx *Context) (Object, error) {
	v, err := evalExpr(n.Primary, ctx)
	if err != nil {
		return nil, err
	}
	if ns, ok := v.(NodeSet); ok {
		for _, pred := range n.Preds {
			ns, err = applyPredicate(ns, pred, ctx, true)
			if err != nil {
				return nil, err
			}
		}
		return ns, nil
	}
	// General sequence: filter items, modelling the context item as a node
	// (atomics are wrapped in a synthetic text node so "." works).
	items := Items(v)
	for _, pred := range n.Preds {
		items, err = applyPredicateToItems(items, pred, ctx)
		if err != nil {
			return nil, err
		}
	}
	return FromItems(items), nil
}

func applyPredicateToItems(items []Item, pred Expr, ctx *Context) ([]Item, error) {
	size := len(items)
	var out []Item
	for i, it := range items {
		pos := i + 1
		sub := &Context{SchemaTypes: ctx.SchemaTypes, Pos: pos, Size: size, Vars: ctx.Vars, NS: ctx.NS, Funcs: ctx.Funcs, Resolver: ctx.Resolver, BaseURI: ctx.BaseURI, DecimalFormats: ctx.DecimalFormats, DefaultElemNS: ctx.DefaultElemNS, DefaultCollation: ctx.DefaultCollation, RootlessTree: ctx.RootlessTree, nowCache: ctx.nowCache, locals: ctx.locals, BC10: ctx.BC10}
		switch it.(type) {
		case *xmltree.Node:
			sub.Node = it.(*xmltree.Node)
		default:
			// A non-node context item stays ITSELF — the old synthetic text
			// node made atomics answer node questions (root()/local-name()
			// silently succeeded — K-NodeRootFunc-2, K2-NameTest) and broke
			// predicate lookups over maps/arrays (Lookup-001).
			sub.CtxItem = it
		}
		v, err := evalExpr(pred, sub)
		if err != nil {
			return nil, err
		}
		if num, ok := asPositionalInt(v); ok {
			if num == pos {
				out = append(out, it)
			}
			continue
		}
		vb, err := EffectiveBoolErr(v)
		if err != nil {
			return nil, err // FORG0006: a predicate value with no EBV (predicate-056)
		}
		if vb {
			out = append(out, it)
		}
	}
	return out, nil
}

func evalPath(n *PathExpr, ctx *Context) (Object, error) {
	// XPST0081 is STATIC: a name-test prefix with no binding is an error
	// regardless of whether any node is selected (K2-NameTest-11/41/42, an
	// empty input must still raise it). "xmlns" is never a legal name-test
	// prefix (K2-NameTest-43..46).
	for _, step := range n.Steps {
		p := step.Test.Prefix
		if step.Test.Braced && step.Test.URI == "http://www.w3.org/2000/xmlns/" {
			// The xmlns namespace can never be the namespace of a name test
			// (eqname-910: XQST0070).
			return nil, fmt.Errorf("err:XQST0070: the xmlns namespace URI cannot be used in a name")
		}
		if p == "" || step.Test.URI != "" {
			continue
		}
		if p == "xmlns" {
			return nil, fmt.Errorf("err:XPST0081: xmlns is not a valid namespace prefix")
		}
		if _, err := resolvePrefix(ctx, p); err != nil {
			return nil, err
		}
	}
	var current NodeSet
	if n.Start != nil {
		v, err := evalExpr(n.Start, ctx)
		if err != nil {
			return nil, err
		}
		ns, ok := ToNodeSet(v)
		if !ok {
			return nil, fmt.Errorf("path step applied to non-node-set")
		}
		current = ns
	} else if n.Absolute {
		cn := ctx.Node
		if cn == nil {
			if nd, ok := ctx.CtxItem.(*xmltree.Node); ok {
				cn = nd
			} else if ctx.CtxItem != nil {
				return nil, fmt.Errorf("err:XPTY0020: the context item for a path expression is not a node")
			} else if ctx.NoFocus {
				// '/' with no focus at all is XPDY0002 (K2-Axes-45).
				return nil, fmt.Errorf("err:XPDY0002: the context item is absent for '/'")
			}
		}
		if cn != nil && cn.SynthCtx {
			// The context item is an ATOMIC value modelled as a text node
			// (xsl:for-each / xsl:analyze-string over a non-node sequence):
			// a path expression rooted at it has no tree to navigate
			// (error-XPTY0020a/b).
			return nil, fmt.Errorf("err:XPTY0020: the context item for a path expression is not a node")
		}
		if cn != nil {
			root := cn.Root()
			if ctx.RootlessTree {
				// effectiveRoot (not the raw Root()) so a node extracted from
				// an @as-typed variable/param/function/xsl:sequence body is
				// judged by where its REAL content ends — a throwaway
				// NoAtomicMerge scratch collector further up doesn't count
				// as a document root any more than XSD's already-fully-
				// detached (Parent==nil) assertion-tree copies do.
				root = effectiveRoot(cn)
				if root.Kind != xmltree.KindDocument {
					// XPDY0050: '/' requires a tree rooted at a document node.
					return nil, fmt.Errorf("XPDY0050: the tree has no document root")
				}
			}
			current = NodeSet{root}
		}
	} else {
		if ctx.Node != nil {
			if ctx.Node.SynthCtx {
				return nil, fmt.Errorf("err:XPTY0020: the context item for an axis step is not a node")
			}
			current = NodeSet{ctx.Node}
		} else if nd, ok := ctx.CtxItem.(*xmltree.Node); ok {
			// A node focus set only through CtxItem (simple map "!").
			current = NodeSet{nd}
		} else if ctx.CtxItem != nil {
			// An axis step with an atomic (or function) context item is
			// XPTY0020 (K2-Axes-38/39: 123[..], 1[element()]).
			return nil, fmt.Errorf("err:XPTY0020: the context item for an axis step is not a node")
		} else if ctx.CtxItem == nil {
			// A relative path with no context item at all is XPDY0002
			// (format-integer-019 in the "empty" environment), not empty.
			return nil, fmt.Errorf("err:XPDY0002: the context item is absent")
		}
	}

	for i, step := range n.Steps {
		if step.Postfix != nil {
			// PostfixExpr step (function call etc.): evaluate per context node.
			var items []Item
			size := len(current)
			for idx, cn := range current {
				sub := &Context{SchemaTypes: ctx.SchemaTypes, Node: cn, Pos: idx + 1, Size: size, CtxItem: cn, Vars: ctx.Vars, NS: ctx.NS, Funcs: ctx.Funcs, Resolver: ctx.Resolver, BaseURI: ctx.BaseURI, DecimalFormats: ctx.DecimalFormats, DefaultElemNS: ctx.DefaultElemNS, DefaultCollation: ctx.DefaultCollation, RootlessTree: ctx.RootlessTree, nowCache: ctx.nowCache, locals: ctx.locals, BC10: ctx.BC10}
				v, err := evalExpr(step.Postfix, sub)
				if err != nil {
					return nil, err
				}
				items = append(items, Items(v)...)
			}
			// XPath 3.1 §3.3.3: when E1/E2 yields NODES the result is returned
			// in document order with duplicates eliminated. That applies to a
			// postfix (non-axis) right operand just as much as to an axis step
			// — "./(//c[@id='33'], //c[@id='11'])" is ordered by the tree, not
			// by the way the sequence was written (bug-5802). A postfix step
			// that is not the right operand of "/" at all (the first step of a
			// relative path) keeps the order its own expression produced, and is
			// not itself subject to the last-step mixed node/atomic check.
			isPathStep := i > 0 || n.Absolute || n.Start != nil
			if i == len(n.Steps)-1 {
				if isPathStep {
					return pathStepResult(items)
				}
				return FromItems(items), nil
			}
			result := FromItems(items)
			// Only nodes can be navigated further.
			ns, ok := ToNodeSet(result)
			if !ok {
				return nil, fmt.Errorf("path step after a non-node value")
			}
			current = ns.normalize()
			continue
		}
		var next NodeSet
		for _, cn := range current {
			matched, err := evalStep(step, cn, ctx)
			if err != nil {
				return nil, err
			}
			next = append(next, matched...)
		}
		current = next.normalize()
	}
	return current, nil
}

// evalStep produces the nodes selected by step from context node cn.
func evalStep(step *Step, cn *xmltree.Node, ctx *Context) (NodeSet, error) {
	var candidates NodeSet
	if step.Axis == "namespace" {
		candidates = namespaceNodes(cn, ctxNSScope(ctx))
	} else {
		candidates = axisNodes(step.Axis, cn)
	}
	if ctx != nil && ctx.PatternTop != nil && cn == ctx.PatternTop &&
		(step.Axis == "child" || step.Axis == "attribute") {
		// child-or-top / attribute-or-top (see Context.PatternTop).
		candidates = append(NodeSet{cn}, candidates...)
	}
	// node-test filter
	var matched NodeSet
	for _, c := range candidates {
		ok, err := matchTest(step.Test, step.Axis, c, ctx)
		if err != nil {
			return nil, err
		}
		if ok {
			matched = append(matched, c)
		}
	}
	// predicates
	reverse := isReverseAxis(step.Axis)
	for _, pred := range step.Preds {
		var err error
		matched, err = applyPredicateOrdered(matched, pred, ctx, reverse)
		if err != nil {
			return nil, err
		}
	}
	if reverse {
		// A reverse axis delivers its nodes in REVERSE document order — which
		// is what the predicates' proximity positions above are based on — but
		// the STEP's result is a node-set, and a path expression's result is in
		// document order. The caller restores that with normalize(), which
		// sorts on Node.Order() — an index only a PARSED tree carries, so in a
		// tree constructed at run time (an RTF, an xsl:copy-of result, a
		// stylesheet function's result) the sort is a no-op and the reversal
		// would survive. Undo it here instead (copy-4305:
		// "$copied/d/ancestor-or-self::*" must read c, d — not d, c).
		reverseNodes(matched)
	}
	return matched, nil
}

// applyPredicate applies a predicate to a node-set already in document order.
// It must NOT re-normalize (sort+dedupe) here: its one caller, evalFilter,
// hands it whatever NodeSet-typed value the FilterExpr's primary evaluated
// to — for a genuine path/union expression that value is already normalized
// by evalPath/UnionExpr's own construction, but for a general sequence
// constructor or variable reference (e.g. "(root(), /, /doc)[3]",
// "$var as='node()+'[3]") FromItems ALSO returns a NodeSet whenever every
// item happens to be a node, even though such a sequence deliberately
// preserves construction order and duplicate node identities (the comma
// operator is order/duplicate-preserving, unlike a location path) — a
// re-normalize here silently collapsed two equal-identity nodes into one,
// shifting every later item's position (as-1304: (root(), /, /doc)[3] must
// still see 3 positions, not 2).
func applyPredicate(ns NodeSet, pred Expr, ctx *Context, _ bool) (NodeSet, error) {
	return applyPredicateOrdered(ns, pred, ctx, false)
}

// applyPredicateOrdered applies a predicate to nodes already in AXIS order:
// axisNodes returns a reverse axis nearest-first, so the context position is
// the list index for forward and reverse axes alike (bang-13:
// preceding-sibling::*[1] is the nearest sibling; flipping the position a
// second time selected the document-order first one). The reverse flag is
// kept for callers that pass a document-ordered set.
func applyPredicateOrdered(ns NodeSet, pred Expr, ctx *Context, _ bool) (NodeSet, error) {
	size := len(ns)
	var out NodeSet
	for i, node := range ns {
		pos := i + 1
		sub := &Context{SchemaTypes: ctx.SchemaTypes, Node: node, Pos: pos, Size: size, Vars: ctx.Vars, NS: ctx.NS, Funcs: ctx.Funcs, Resolver: ctx.Resolver, BaseURI: ctx.BaseURI, DecimalFormats: ctx.DecimalFormats, DefaultElemNS: ctx.DefaultElemNS, DefaultCollation: ctx.DefaultCollation, RootlessTree: ctx.RootlessTree, nowCache: ctx.nowCache, locals: ctx.locals, BC10: ctx.BC10}
		v, err := evalExpr(pred, sub)
		if err != nil {
			return nil, err
		}
		if num, ok := asPositionalInt(v); ok {
			if num == pos {
				out = append(out, node)
			}
			continue
		}
		vb, err := EffectiveBoolErr(v)
		if err != nil {
			return nil, err
		}
		if vb {
			out = append(out, node)
		}
	}
	return out, nil
}

func evalBinary(n *BinaryExpr, ctx *Context) (Object, error) {
	switch n.Op {
	case "or":
		l, err := evalExpr(n.L, ctx)
		if err != nil {
			return nil, err
		}
		lb, err := EffectiveBoolErr(l)
		if err != nil {
			return nil, err
		}
		if lb {
			return true, nil
		}
		r, err := evalExpr(n.R, ctx)
		if err != nil {
			return nil, err
		}
		rb, err := EffectiveBoolErr(r)
		if err != nil {
			return nil, err
		}
		return rb, nil
	case "and":
		l, err := evalExpr(n.L, ctx)
		if err != nil {
			return nil, err
		}
		lb, err := EffectiveBoolErr(l)
		if err != nil {
			return nil, err
		}
		if !lb {
			return false, nil
		}
		r, err := evalExpr(n.R, ctx)
		if err != nil {
			return nil, err
		}
		rb, err := EffectiveBoolErr(r)
		if err != nil {
			return nil, err
		}
		return rb, nil
	}

	l, err := evalExpr(n.L, ctx)
	if err != nil {
		return nil, err
	}
	r, err := evalExpr(n.R, ctx)
	if err != nil {
		return nil, err
	}

	switch n.Op {
	case "+", "-", "*", "div", "mod", "idiv":
		return evalArith(ctx, n.Op, l, r)
	}
	return nil, fmt.Errorf("unknown operator %q", n.Op)
}

// nodeSetOperand converts an operand of union/intersect/except to a node set.
// The operators take node()* only: any atomic, map, array or function item is
// XPTY0004 ((1, 2, 3) union (1, 2, 3), 1 intersect 2 — K2-SeqUnion-5/46/47,
// K2-SeqIntersect-1/43/44, K2-SeqExcept-1).
func nodeSetOperand(o Object, op string) (NodeSet, error) {
	if o == nil {
		return nil, nil
	}
	ns, ok := ToNodeSet(o)
	if !ok {
		return nil, fmt.Errorf("err:XPTY0004: operands of %s must be nodes", op)
	}
	return ns, nil
}

func evalIntersectExcept(n *IntersectExceptExpr, ctx *Context) (Object, error) {
	l, err := evalExpr(n.L, ctx)
	if err != nil {
		return nil, err
	}
	r, err := evalExpr(n.R, ctx)
	if err != nil {
		return nil, err
	}
	ln, err := nodeSetOperand(l, n.Op)
	if err != nil {
		return nil, err
	}
	rn, err := nodeSetOperand(r, n.Op)
	if err != nil {
		return nil, err
	}
	inR := map[*xmltree.Node]bool{}
	for _, x := range rn {
		inR[x] = true
	}
	var out NodeSet
	for _, x := range ln {
		if n.Op == "intersect" && inR[x] {
			out = append(out, x)
		} else if n.Op == "except" && !inR[x] {
			out = append(out, x)
		}
	}
	return out.normalize(), nil
}

func evalCast(n *CastExpr, ctx *Context) (Object, error) {
	v, err := evalExpr(n.X, ctx)
	if err != nil {
		return nil, err
	}
	items, err := Atomize(v)
	if err != nil {
		return nil, err
	}
	target := n.Type.Item.Atom
	allowEmpty := n.Type.Occur == '?'
	// A USER-DEFINED simple type target goes to the schema, which owns the
	// facets that decide validity (import-schema-021..025, notation-0002).
	if st := n.Type.Item.SchemaType; st != nil {
		if len(items) == 0 {
			if n.Castable {
				return allowEmpty, nil
			}
			if allowEmpty {
				return Sequence{}, nil
			}
			return nil, fmt.Errorf("err:XPTY0004: cannot cast empty sequence")
		}
		if len(items) != 1 {
			if n.Castable {
				return false, nil
			}
			return nil, fmt.Errorf("err:XPTY0004: cast operand is not a singleton")
		}
		v, err := castToNamedSchemaType(items[0], *st, ctx)
		if n.Castable {
			return err == nil, nil
		}
		return v, err
	}
	// Abstract types are never legal cast/castable targets — a STATIC error
	// regardless of the operand ('x' castable as xs:anyAtomicType,
	// () castable as xs:NOTATION — XPST0080).
	if target == XSnotation || target == XSanyAtomicType {
		return nil, fmt.Errorf("err:XPST0080: %s is not a valid cast target", target)
	}
	if err := checkBareTypes(n.Type, ctx); err != nil {
		return nil, err
	}
	if n.Castable {
		if len(items) == 0 {
			return allowEmpty, nil
		}
		if len(items) != 1 {
			return false, nil
		}
		if n.Type.List {
			_, err := castToXSList(items[0], target)
			return err == nil, nil
		}
		_, err := castInContext(items[0], target, ctx)
		return err == nil, nil
	}
	if len(items) == 0 {
		if allowEmpty {
			return Sequence{}, nil
		}
		return nil, fmt.Errorf("err:XPTY0004: cannot cast empty sequence")
	}
	if len(items) != 1 {
		return nil, fmt.Errorf("err:XPTY0004: cast operand is not a singleton")
	}
	if n.Type.List {
		return castToXSList(items[0], target)
	}
	return castInContext(items[0], target, ctx)
}

// castInContext is CastTo for the XPath cast/castable expression and the xs:T()
// constructors — the places that have a static context. A string cast to
// xs:QName resolves its prefix against the in-scope namespaces (K2-SeqExprCast-1,
// FONS0004 when unbound), and xs:anyURI additionally requires a well-formed
// URI (the shared CastTo stays XSD-1.1-lenient for the schema validator).
func castInContext(it Item, target AtomType, ctx *Context) (Object, error) {
	if target == XSqname {
		if a, ok := it.(*Atomic); ok && a.T != XSqname && a.T != XSuntypedAtomic && a.T != XSanyURI && (isStringType(a.T) || a.T == XSstring) {
			return constructQName(ctx, a)
		}
		// xs:untypedAtomic → xs:QName: XQuery/XPath 1.0 disallowed this
		// unconditionally, but XPath/XQuery 3.0 (bug 16059) permits it —
		// resolved exactly like a plain string source (constructQName), the
		// one difference being the error code on an ill-formed lexical
		// value: FORG0001 (a general CAST error, K-SeqExprCast-422a), not
		// fn:QName's own FOCA0002 — so this does its own qnValidLexical
		// check rather than sharing constructQName's (K-SeqExprCast-71b:
		// "ncname" with no prefix succeeds; -71a/422/423 pin the PRE-3.0
		// unconditional rejection, but are version-gated to XP20/XQ10 only,
		// which this XP31 engine never satisfies).
		if a, ok := it.(*Atomic); ok && a.T == XSuntypedAtomic {
			lex := strings.TrimSpace(a.Lexical())
			prefix, local, lok := qnValidLexical(lex)
			if !lok {
				return nil, fmt.Errorf("err:FORG0001: %q is not a valid lexical QName", lex)
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
				space = ctx.DefaultElemNS
				if space == "" && ctx.NS != nil {
					space, _ = ctx.NS.ResolveNS("")
				}
			}
			return NewQName(xmltree.Name{Prefix: prefix, Local: local, Space: space}), nil
		}
	}
	if (target == XSdouble || target == XSfloat) && (ctx == nil || ctx.XSDVersion != "1.1") {
		if a, ok := it.(*Atomic); ok && !a.IsNumeric() && a.T != XSboolean {
			if strings.TrimSpace(a.Lexical()) == "+INF" {
				return nil, fmt.Errorf("err:FORG0001: %q is not a valid %s literal under XSD 1.0", "+INF", target)
			}
		}
	}
	// A year of all zeros ("0000", "-0000", "0000-05", …) is prohibited by
	// XSD 1.0's gYear/gYearMonth lexical grammar note ("'0000' is
	// prohibited"); XSD 1.1 changed this so '0000' denotes 1 BCE
	// (cbcl-cast-gYear-002/003, cbcl-cast-gYearMonth-003 want FORG0001 under
	// the default/1.0 identity; cbcl-cast-gYear-003a's XSD-1.1 sibling
	// accepts the identical "-0000" as equal to gYear('0000'), which the
	// shared, version-agnostic CastTo below already computes correctly).
	if (target == XSgYear || target == XSgYearMonth) && (ctx == nil || ctx.XSDVersion != "1.1") {
		if a, ok := it.(*Atomic); ok {
			lex := strings.TrimSpace(a.Lexical())
			var yr string
			if target == XSgYear {
				if m := reGYear.FindStringSubmatch(lex); m != nil {
					yr = m[1]
				}
			} else if m := reGYearMo.FindStringSubmatch(lex); m != nil {
				yr = m[1]
			}
			if yr != "" && strings.Trim(yr, "-0") == "" {
				return nil, fmt.Errorf("err:FORG0001: %q is not a valid %s literal under XSD 1.0 ('0000' is prohibited)", a.Lexical(), target)
			}
		}
	}
	r, err := CastTo(it, target)
	if err != nil {
		return nil, err
	}
	if target == XSanyURI && !anyURIWellFormed(r.Lexical()) {
		return nil, fmt.Errorf("err:FORG0001: %q is not a valid xs:anyURI", r.Lexical())
	}
	return r, nil
}

func evalArrow(n *ArrowExpr, ctx *Context) (Object, error) {
	cur, err := evalExpr(n.Base, ctx)
	if err != nil {
		return nil, err
	}
	for _, step := range n.Steps {
		args := make([]Object, 0, len(step.Args)+1)
		args = append(args, cur)
		isHole := []bool{false}
		holes := 0
		for _, a := range step.Args {
			if _, ok := a.(*Placeholder); ok {
				args = append(args, nil)
				isHole = append(isHole, true)
				holes++
				continue
			}
			av, err := evalExpr(a, ctx)
			if err != nil {
				return nil, err
			}
			args = append(args, av)
			isHole = append(isHole, false)
		}
		var call func(filled []Object) (Object, error)
		if step.Name != "" {
			pre, name := step.Pre, step.Name
			call = func(filled []Object) (Object, error) { return dispatchFunc(pre, name, filled, ctx) }
		} else {
			spec, err := evalExpr(step.Spec, ctx)
			if err != nil {
				return nil, err
			}
			fi := firstItem(spec)
			call = func(filled []Object) (Object, error) { return callItemAsFunction(fi, filled) }
		}
		if holes == 0 {
			if cur, err = call(args); err != nil {
				return nil, err
			}
			continue
		}
		// An arrow step with '?' arguments is a partial application whose
		// first bound argument is the arrow operand: "$" => concat(?) is
		// concat("$", ?) (ArrowPostfix-108).
		template, holeAt := args, isHole
		cur = &Function{Arity: holes, Call: func(supplied []Object) (Object, error) {
			filled := make([]Object, len(template))
			si := 0
			for i := range template {
				if holeAt[i] {
					if si < len(supplied) {
						filled[i] = supplied[si]
						si++
					}
				} else {
					filled[i] = template[i]
				}
			}
			return call(filled)
		}}
	}
	return cur, nil
}

func evalSimpleMap(n *SimpleMapExpr, ctx *Context) (Object, error) {
	cur, err := evalExpr(n.Steps[0], ctx)
	if err != nil {
		return nil, err
	}
	for _, step := range n.Steps[1:] {
		items := Items(cur)
		var out []Item
		for i, it := range items {
			sub := &Context{SchemaTypes: ctx.SchemaTypes, Pos: i + 1, Size: len(items), CtxItem: it, Vars: ctx.Vars, NS: ctx.NS, Funcs: ctx.Funcs, Resolver: ctx.Resolver, BaseURI: ctx.BaseURI, DecimalFormats: ctx.DecimalFormats, DefaultElemNS: ctx.DefaultElemNS, DefaultCollation: ctx.DefaultCollation, RootlessTree: ctx.RootlessTree, nowCache: ctx.nowCache, locals: ctx.locals, BC10: ctx.BC10}
			if nd, ok := it.(*xmltree.Node); ok {
				sub.Node = nd
			}
			r, err := evalExpr(step, sub)
			if err != nil {
				return nil, err
			}
			out = append(out, Items(r)...)
		}
		cur = FromItems(out)
	}
	return cur, nil
}

func qn(prefix, local string) string {
	if prefix == "" {
		return local
	}
	return prefix + ":" + local
}

// pathStepResult applies the XPath rule for the result of a path expression
// (XPath 3.1 §3.3): every item must be a node or every item must be a
// non-node (XPTY0018 otherwise — expression-0932/0933), and a node result is
// returned in document order with duplicates removed (expression-0907: a
// parenthesized last step may deliver its nodes in any order).
func pathStepResult(items []Item) (Object, error) {
	nodes, others := 0, 0
	for _, it := range items {
		if _, ok := it.(*xmltree.Node); ok {
			nodes++
		} else {
			others++
		}
	}
	if nodes > 0 && others > 0 {
		return nil, fmt.Errorf("err:XPTY0018: the result of the last step of a path expression mixes nodes and atomic values")
	}
	if nodes == 0 {
		return FromItems(items), nil
	}
	ns := make(NodeSet, 0, len(items))
	for _, it := range items {
		ns = append(ns, it.(*xmltree.Node))
	}
	return ns.normalize(), nil
}
