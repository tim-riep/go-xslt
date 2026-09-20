package xpath

import "fmt"

// fn:transform($options as map(*)) as map(*) — F&O 3.1 §16.3.2
// (https://www.w3.org/TR/xpath-functions-31/#func-transform): "Invokes a
// transformation using a dynamically-loaded XSLT stylesheet."
//
// internal/xslt (this project's own XSLT engine) imports internal/xpath, so
// internal/xpath cannot import internal/xslt back without a cycle. The real
// implementation therefore lives in internal/xslt and registers itself here
// via TransformFunc — the same injected-hook pattern Context.SchemaTypes /
// Context.ResolveNamedFunction already use to let this package call back into
// the XSLT layer it cannot see.
//
// A call made FROM WITHIN a compiled stylesheet reaches a fuller
// implementation first: internal/xslt's own function dispatch (ctx.Funcs,
// consulted below) has access to that stylesheet's package registry
// (xsl:use-package) that a bare call — e.g. this function evaluated directly
// by a host with no enclosing stylesheet, such as the W3C QT3/FOTS
// conformance harness — does not. TransformFunc is the fallback for exactly
// that bare case.
var TransformFunc func(ctx *Context, opts *Map) (Object, error)

func init() {
	coreFuncs["transform"] = fnTransform
}

func fnTransform(ctx *Context, args []Object) (Object, error) {
	items := Items(arg(args, 0))
	if len(items) != 1 {
		return nil, fmt.Errorf("err:XPTY0004: transform() expects a single map")
	}
	opts, ok := items[0].(*Map)
	if !ok {
		return nil, fmt.Errorf("err:XPTY0004: transform() expects a map")
	}
	if ctx != nil && ctx.Funcs != nil {
		// Reuse the XSLT engine's own richer in-stylesheet implementation
		// unchanged (it knows the calling stylesheet's package registry,
		// among other things a bare call cannot supply) rather than
		// duplicating it.
		if v, ok, err := ctx.Funcs.ResolveFunc("", "transform", args, ctx); ok {
			return v, err
		}
	}
	if TransformFunc == nil {
		// err:FOXT0001 "No suitable XSLT processor available" — the first of
		// the Recommendation's own listed causes: "No XSLT processor is
		// available."
		return nil, fmt.Errorf("err:FOXT0001: no XSLT processor is available")
	}
	return TransformFunc(ctx, opts)
}
