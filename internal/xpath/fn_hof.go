package xpath

import (
	"fmt"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

func init() {
	coreFuncs["function-arity"] = hofFunctionArity
	coreFuncs["function-name"] = hofFunctionName
	coreFuncs["function-lookup"] = hofFunctionLookup
	coreFuncs["apply"] = hofApply
}

// Standard function-library namespaces.
const (
	nsFn    = "http://www.w3.org/2005/xpath-functions"
	nsMath  = "http://www.w3.org/2005/xpath-functions/math"
	nsMap   = "http://www.w3.org/2005/xpath-functions/map"
	nsArray = "http://www.w3.org/2005/xpath-functions/array"
	nsXS    = "http://www.w3.org/2001/XMLSchema"
)

// dispatchTokenForNS maps a function namespace to the prefix token dispatchFunc
// keys on; ok=false for namespaces we don't host as a built-in library.
func dispatchTokenForNS(ns string) (string, bool) {
	switch ns {
	case nsFn, "":
		return "fn", true
	case nsMath:
		return "math", true
	case nsMap:
		return "map", true
	case nsArray:
		return "array", true
	case nsXS:
		return "xs", true
	}
	return "", false
}

// nsForKnownPrefix resolves the standard function-library prefixes.
func nsForKnownPrefix(p string) (string, bool) {
	switch p {
	case "", "fn":
		return nsFn, true
	case "math":
		return nsMath, true
	case "map":
		return nsMap, true
	case "array":
		return nsArray, true
	case "xs":
		return nsXS, true
	}
	return "", false
}

// prefixForNS gives a conventional prefix for a function namespace (so a QName's
// string value reads e.g. "fn:abs").
func prefixForNS(ns string) string {
	switch ns {
	case nsFn:
		return "fn"
	case nsMath:
		return "math"
	case nsMap:
		return "map"
	case nsArray:
		return "array"
	case nsXS:
		return "xs"
	}
	return ""
}

// registryHas reports whether (ns, local) names a built-in function/constructor.
func registryHas(ns, local string) bool {
	switch ns {
	case nsFn, "":
		_, ok := coreFuncs[local]
		return ok
	case nsMath:
		_, ok := mathFuncs[local]
		return ok
	case nsMap:
		_, ok := mapFuncs[local]
		return ok
	case nsArray:
		_, ok := arrayFuncs[local]
		return ok
	case nsXS:
		if _, ok := xsListItemType(local); ok {
			return true
		}
		_, ok := AtomTypeByName(local)
		return ok
	}
	return false
}

// lookupFunction resolves (ns, local, arity) to a callable function item, used by
// fn:function-lookup and named-function references. It returns ok=false when the
// name is not a known built-in. Arity is recorded on the item; we do not (yet)
// reject a known name with an out-of-range arity.
func lookupFunction(ctx *Context, ns, local string, arity int) (*Function, bool) {
	// A QName in no namespace names no built-in function. Callers that mean the
	// default function namespace pass nsFn explicitly.
	if ns == "" {
		return nil, false
	}
	token, ok := dispatchTokenForNS(ns)
	if !ok {
		return nil, false
	}
	// When the function is in the standard catalog, validate the arity: a valid
	// name at an out-of-range arity does not exist (returns ()), and a standard
	// function we do not implement still exists as a (stub) function item.
	if lo, hi, known := stdArity(ns, local); known {
		if stdArityOutside(ns, local, lo, hi, arity) {
			return nil, false
		}
		captured := ctx
		return sigApply(&Function{Arity: arity, NS: ns, Name: local, Call: func(args []Object) (Object, error) {
			if !registryHas(ns, local) {
				return nil, fmt.Errorf("err:FOXX0001: function %s#%d is not implemented", local, arity)
			}
			return dispatchFunc(token, local, args, captured)
		}}), true
	}
	if !registryHas(ns, local) {
		// A host (XSLT) may serve functions in the standard function namespace
		// that this package's catalog knows nothing about — current(), key(),
		// available-system-properties(), … . They are only reachable as
		// function ITEMS if the host declares them, which HostFuncCatalog does.
		if ctx != nil && ns == nsFn && ctx.Funcs != nil {
			if hc, ok := ctx.Funcs.(HostFuncCatalog); ok {
				if lo, hi, known := hc.HostFuncArity(ns, local); known && arity >= lo && arity <= hi {
					captured, hostLocal := ctx, local
					return &Function{Arity: arity, NS: ns, Name: local, Call: func(args []Object) (Object, error) {
						v, ok, err := captured.Funcs.ResolveFunc("", hostLocal, args, captured)
						if err != nil {
							return nil, err
						}
						if !ok {
							return nil, fmt.Errorf("unknown function %s()", hostLocal)
						}
						return v, nil
					}}, true
				}
			}
		}
		return nil, false
	}
	captured := ctx
	return sigApply(&Function{Arity: arity, NS: ns, Name: local, Call: func(args []Object) (Object, error) {
		return dispatchFunc(token, local, args, captured)
	}}), true
}

// HostFuncCatalog is the optional interface a FuncResolver may implement to
// declare which functions IN THE STANDARD FUNCTION NAMESPACE the host itself
// serves beyond this package's catalog (XSLT 3.0 adds current(), key(),
// available-system-properties(), …). Without it such a name is only callable
// directly; with it, fn:function-lookup and a named-function reference can
// also build a function item for it.
//
// Implementations must not have side effects: HostFuncArity is a pure lookup.
type HostFuncCatalog interface {
	// HostFuncArity reports the inclusive [lo,hi] arity range the host serves
	// for (uri, local), and whether it serves that name at all.
	HostFuncArity(uri, local string) (lo, hi int, ok bool)
}

// hofFunc returns the function(*) argument at args[i]: exactly ONE item
// (fn-function-arity-009, fn-function-name-009: a plural argument is
// XPTY0004) that is a function, or a map/array applied as one.
func hofFunc(args []Object, i int) (*Function, error) {
	items := Items(arg(args, i))
	if len(items) != 1 {
		return nil, fmt.Errorf("err:XPTY0004: expected a single function item, got %d items", len(items))
	}
	fn, ok := asFunc(items[0])
	if !ok {
		return nil, fmt.Errorf("err:XPTY0004: expected a function item")
	}
	return fn, nil
}

// function-arity($f as function(*)) as xs:integer
func hofFunctionArity(_ *Context, args []Object) (Object, error) {
	fn, err := hofFunc(args, 0)
	if err != nil {
		return nil, err
	}
	return NewInteger(int64(fn.Arity)), nil
}

// function-name($f as function(*)) as xs:QName?
//
// Returns the function's name as an xs:QName, or the empty sequence for an
// anonymous function (inline or partially-applied).
func hofFunctionName(_ *Context, args []Object) (Object, error) {
	fn, err := hofFunc(args, 0)
	if err != nil {
		return nil, err
	}
	if fn.Name == "" {
		return Sequence{}, nil
	}
	return NewQName(xmltree.Name{Local: fn.Name, Space: fn.NS, Prefix: prefixForNS(fn.NS)}), nil
}

// function-lookup($name as xs:QName, $arity as xs:integer) as function(*)?
//
// Resolves a built-in function by name and arity to a callable function item,
// or the empty sequence if there is no such function.
func hofFunctionLookup(ctx *Context, args []Object) (Object, error) {
	// $name (xs:QName) and $arity (xs:integer) are required singletons: an
	// empty or plural argument is a type error, not an empty result
	// (fn-function-lookup-707..710).
	nameItems := Items(arg(args, 0))
	if len(nameItems) != 1 {
		return nil, fmt.Errorf("err:XPTY0004: function-lookup expects exactly one xs:QName")
	}
	q, ok := nameItems[0].(*Atomic)
	if !ok || q.T != XSqname {
		return nil, fmt.Errorf("err:XPTY0004: function-lookup name must be an xs:QName")
	}
	if len(Items(arg(args, 1))) != 1 {
		return nil, fmt.Errorf("err:XPTY0004: function-lookup expects exactly one arity")
	}
	arity := int(ToNumber(arg(args, 1)))
	ns := q.qn.Space
	if ns == "" && q.qn.Prefix != "" {
		if n, ok := nsForKnownPrefix(q.qn.Prefix); ok {
			ns = n
		} else if ctx.NS != nil {
			if n, ok := ctx.NS.ResolveNS(q.qn.Prefix); ok {
				ns = n
			}
		}
	}
	// Note: an empty namespace URI is NOT defaulted to the fn namespace — a QName
	// in no namespace names no built-in function, so the lookup yields ().
	if fn, ok := lookupFunction(ctx, ns, q.qn.Local, arity); ok {
		return fn, nil
	}
	// The built-in F&O catalog has no match — give the host (the XSLT
	// engine, for a user-declared xsl:function) a chance, via
	// Context.ResolveNamedFunction's own doc comment.
	if ctx.ResolveNamedFunction != nil {
		if fn, ok := ctx.ResolveNamedFunction(ns, q.qn.Local, arity); ok {
			return fn, nil
		}
	}
	return Sequence{}, nil
}

// apply($f as function(*), $args as array(*)) as item()*
//
// Calls $f with the members of $args spread as its arguments.
func hofApply(_ *Context, args []Object) (Object, error) {
	fn, err := hofFunc(args, 0)
	if err != nil {
		return nil, err
	}
	arr, ok := firstItem(arg(args, 1)).(*Array)
	if !ok {
		return nil, fmt.Errorf("err:XPTY0004: apply expects an array as its second argument")
	}
	members := arr.Members()
	if len(members) != fn.Arity {
		return nil, fmt.Errorf("err:FOAP0001: arity mismatch: function takes %d arguments but array has %d members", fn.Arity, len(members))
	}
	return fn.Call(members)
}
