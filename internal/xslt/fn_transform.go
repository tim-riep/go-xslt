package xslt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// This file implements fn:transform($options as map(*)) as map(*) — F&O 3.1
// §16.3.2 (https://www.w3.org/TR/xpath-functions-31/#func-transform): "run a
// SECOND transformation from within a stylesheet (or from a static/bare XPath
// expression) and get its results back as a map."
//
// The returned map holds the principal result under the key "output" (unless
// base-output-uri names it otherwise) plus one entry per xsl:result-document
// secondary output, keyed by that document's absolute URI.
//
// Options implemented (Recommendation's own option table): stylesheet-
// location / stylesheet-node / stylesheet-text, stylesheet-base-uri,
// stylesheet-params, static-params, source-node, initial-match-selection,
// global-context-item, initial-template, initial-mode, initial-function,
// function-params, template-params, tunnel-params, delivery-format
// ("document" | "serialized" | "raw"), base-output-uri, serialization-params,
// post-process, xslt-version (type-checked only — this engine has one XSLT
// implementation and always runs the stylesheet's own declared version).
// package-name (+ package-version) is implemented for an in-stylesheet call,
// which has a package registry to search (xsl:use-package); package-location/
// package-node are NOT implemented (no package manager) and reported as
// FOXT0002, matching what a processor without them must say. requested-
// properties, vendor-options, cache, enable-messages/-trace/-assertions are
// accepted and, per the Recommendation's own "may ignore the request" leave,
// silently ignored — this is a single, fixed XSLT engine with nothing to
// negotiate.
//
// All private helpers in this file are prefixed with "xf".

func init() {
	xsltExtraArity["transform"] = [2]int{1, 1}
	xsltFuncs["transform"] = func(eng *engine, args []xpath.Object, env *evalEnv) (xpath.Object, bool, error) {
		opts, err := xfOptsArg(args)
		if err != nil {
			return nil, true, err
		}
		var el *xmltree.Node
		if env != nil {
			el = env.el
		}
		host := xfHost{
			baseURI:  xpath.NodeBaseURI(el, ""),
			baseDir:  eng.baseDir,
			resolver: eng.resolver,
			packages: eng.sheet.packages,
		}
		v, err := xfRun(opts, host)
		return v, true, err
	}
	// The bare/standalone entry point (no enclosing stylesheet — e.g. the
	// W3C QT3/FOTS conformance harness evaluates fn:transform directly as a
	// plain XPath expression): internal/xpath's own coreFuncs dispatch tries
	// ctx.Funcs first (reaching the fuller registration above, with package
	// access) and falls back to this hook only when there is no ctx.Funcs at
	// all — see internal/xpath/fn_transform.go.
	xpath.TransformFunc = func(ctx *xpath.Context, opts *xpath.Map) (xpath.Object, error) {
		host := xfHost{baseURI: ctx.BaseURI, resolver: ctx.Resolver}
		if hint, ok := ctx.Resolver.(xfBaseDirHint); ok {
			host.baseDir = hint.TransformBaseDir()
		}
		return xfRun(opts, host)
	}
}

// xfBaseDirHint is an OPTIONAL capability a host's xpath.ResourceResolver may
// implement (checked via a type assertion, exactly as xpath.CollectionResolver
// already extends xpath.ResourceResolver) to supply a real filesystem
// directory fn:transform can resolve a relative stylesheet-location/
// stylesheet-base-uri against when the calling expression's own static base
// URI is absent or not locally meaningful. This engine's own production
// resolver never needs it (its caller already has a real baseDir at hand —
// see the xsltFuncs registration above); only a bare host without an
// enclosing stylesheet, such as the QT3 conformance harness, does.
type xfBaseDirHint interface{ TransformBaseDir() string }

// xfHost carries everything fn:transform needs from its caller: the static
// base URI of the call site, a resolver for URIs (the SAME xpath.
// ResourceResolver fn:doc/fn:unparsed-text already use — no second resolver
// mechanism), a filesystem directory fallback, and — only for an in-
// stylesheet call — the calling stylesheet's own package registry.
type xfHost struct {
	baseURI  string
	baseDir  string
	resolver xpath.ResourceResolver
	packages []PackageSource
}

// xfOptsArg validates the single map(*) argument shape shared by both call
// paths (the in-stylesheet xsltFuncs registration takes args directly; the
// standalone path is already validated once in internal/xpath's fnTransform,
// but the in-stylesheet path bypasses that, so it is repeated here).
func xfOptsArg(args []xpath.Object) (*xpath.Map, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("err:XPTY0004: transform() expects a single map")
	}
	items := xpath.Items(args[0])
	if len(items) != 1 {
		return nil, fmt.Errorf("err:XPTY0004: transform() expects a single map")
	}
	opts, ok := items[0].(*xpath.Map)
	if !ok {
		return nil, fmt.Errorf("err:XPTY0004: transform() expects a map")
	}
	return opts, nil
}

// xfRun is the shared implementation, usable both at run time (from within a
// compiled stylesheet, or from a static expression — a static variable may
// call fn:transform to decide what the enclosing stylesheet should contain,
// transform-004) and from a bare/standalone XPath expression.
func xfRun(opts *xpath.Map, host xfHost) (xpath.Object, error) {
	nStyle := 0
	for _, k := range [...]string{"stylesheet-location", "stylesheet-node", "stylesheet-text"} {
		if xfHas(opts, k) {
			nStyle++
		}
	}
	if xfHas(opts, "package-name") {
		nStyle++
	}
	if nStyle > 1 {
		return nil, fmt.Errorf("err:FOXT0002: transform(): the stylesheet/package options are mutually exclusive")
	}
	if xfHas(opts, "package-location") || xfHas(opts, "package-node") {
		// Only package-NAME invocation is supported: locating a package by
		// URI or taking it from a node needs a package manager this
		// processor does not have.
		return nil, fmt.Errorf("err:FOXT0002: transform(): package-location/package-node invocation is not supported by this processor")
	}
	if xfHas(opts, "initial-template") && xfHas(opts, "initial-mode") {
		return nil, fmt.Errorf("err:FOXT0002: transform(): initial-template and initial-mode are mutually exclusive")
	}
	if xfHas(opts, "source-node") && xfHas(opts, "initial-match-selection") {
		return nil, fmt.Errorf("err:FOXT0002: transform(): source-node and initial-match-selection are mutually exclusive")
	}
	if xfHas(opts, "xslt-version") {
		v := opts.Get(xpath.NewString("xslt-version"))
		items := xpath.Items(v)
		if len(items) != 1 || !xfIsNumeric(items[0]) {
			return nil, fmt.Errorf("err:XPTY0004: transform(): xslt-version must be a single numeric value")
		}
	}
	if xfHas(opts, "delivery-format") {
		switch xfString(opts, "delivery-format") {
		case "document", "serialized", "raw":
		default:
			return nil, fmt.Errorf("err:FOXT0002: transform(): invalid delivery-format %q", xfString(opts, "delivery-format"))
		}
	}
	if err := xfCheckRequestedProperties(opts); err != nil {
		return nil, err
	}

	// --- locate and compile the stylesheet -----------------------------------
	var src, selfPath string
	packageEntry := false
	switch {
	case xfHas(opts, "package-name"):
		// Package-based invocation: resolve the named package (and optional
		// package-version range) against the SAME host-supplied registry the
		// calling stylesheet was compiled with (transform-005/006/007). Not
		// reachable outside a stylesheet: a bare call has no registry.
		name := xfString(opts, "package-name")
		ver := "*"
		if xfHas(opts, "package-version") {
			ver = xfString(opts, "package-version")
		}
		srcPkg, err := resolvePackageIn(host.packages, name, ver)
		if err != nil {
			return nil, fmt.Errorf("err:FOXT0002: transform(): cannot locate package %q: %v", name, err)
		}
		b, rerr := os.ReadFile(srcPkg.Path)
		if rerr != nil {
			return nil, fmt.Errorf("err:FOXT0002: transform(): cannot retrieve package %q", name)
		}
		src, selfPath, packageEntry = string(b), srcPkg.Path, true
	case xfHas(opts, "stylesheet-location"):
		loc := xfString(opts, "stylesheet-location")
		uri := xpath.ResolveAgainstBase(host.baseURI, loc)
		text, ok := xfFetchText(uri, host)
		if !ok {
			return nil, fmt.Errorf("err:FOXT0002: transform(): cannot retrieve stylesheet %q", loc)
		}
		src, selfPath = text, xfFinalizeSelfPath(uri, host)
	case xfHas(opts, "stylesheet-text"):
		src = xfString(opts, "stylesheet-text")
		// No inherent base at all for bare stylesheet text: only an explicit
		// stylesheet-base-uri gives it one (fn-transform-18/22); with
		// neither, selfPath stays "" — genuinely unresolved, so a relative
		// xsl:include inside it fails naturally (fn-transform-err-9) instead
		// of guessing a host directory it was never told about.
		if xfHas(opts, "stylesheet-base-uri") {
			selfPath = xfFinalizeSelfPath(xpath.ResolveAgainstBase(host.baseURI, xfString(opts, "stylesheet-base-uri")), host)
		}
	case xfHas(opts, "stylesheet-node"):
		nodes, _ := xpath.ToNodeSet(opts.Get(xpath.NewString("stylesheet-node")))
		if len(nodes) == 0 {
			return nil, fmt.Errorf("err:FOXT0002: transform(): stylesheet-node is empty")
		}
		src = xmltree.Serialize(nodes[0], xmltree.SerializeOptions{Method: "xml", OmitXMLDeclaration: true})
		// The node's OWN base URI is the default for stylesheet-base-uri
		// (F&O 3.1's stylesheet-node row); an explicit stylesheet-base-uri
		// option is honored only when the node has none of its own — "it is
		// implementation-defined whether this parameter has any effect"
		// otherwise, and preferring a node's real, already-known location is
		// the more precise choice (fn-transform-19/41 accept either answer).
		switch nodeBase := xpath.NodeBaseURI(nodes[0], ""); {
		case nodeBase != "":
			selfPath = xfFinalizeSelfPath(nodeBase, host)
		case xfHas(opts, "stylesheet-base-uri"):
			selfPath = xfFinalizeSelfPath(xpath.ResolveAgainstBase(host.baseURI, xfString(opts, "stylesheet-base-uri")), host)
		}
	default:
		return nil, fmt.Errorf("err:FOXT0002: transform(): no stylesheet supplied")
	}

	statics := map[string]string{}
	if xfHas(opts, "static-params") {
		m, ok := xfMap(opts, "static-params")
		if !ok {
			return nil, fmt.Errorf("err:XPTY0004: transform(): static-params must be a map")
		}
		for _, k := range m.Keys() {
			statics[xfQNameKey(k)] = xfLiteralOf(m.Get(k))
		}
	}
	ss, err := CompileAtWithPackages(src, selfPath, statics, host.packages)
	if err != nil {
		return nil, err
	}

	// --- build the entry point -----------------------------------------------
	e := Entry{RequirePublicEntry: packageEntry}
	runDir := filepath.Dir(selfPath)
	if runDir == "." || runDir == "" {
		runDir = host.baseDir
	}
	if xfHas(opts, "initial-template") {
		e.Template = xfFirstKey(opts, "initial-template")
	}
	if xfHas(opts, "initial-mode") {
		e.Mode = xfFirstKey(opts, "initial-mode")
	}
	if xfHas(opts, "initial-function") {
		e.Function = xfFirstKey(opts, "initial-function")
		if arr, ok := xfArray(opts, "function-params"); ok {
			for _, m := range arr.Members() {
				e.FuncArgs = append(e.FuncArgs, xfLiteralOf(m))
			}
		}
	}
	if xfHas(opts, "stylesheet-params") {
		m, ok := xfMap(opts, "stylesheet-params")
		if !ok {
			return nil, fmt.Errorf("err:XPTY0004: transform(): stylesheet-params must be a map")
		}
		e.Params = map[string]string{}
		e.ParamSelects = map[string]string{}
		for _, k := range m.Keys() {
			if !xfIsQName(k) {
				return nil, fmt.Errorf("err:FOXT0002: transform(): stylesheet-params keys must be xs:QName")
			}
			name := xfQNameKey(k)
			e.Params[name] = xpath.ToString(m.Get(k))
			e.ParamSelects[name] = xfLiteralOf(m.Get(k))
		}
	}
	for _, spec := range [...]struct {
		key    string
		tunnel bool
	}{{"template-params", false}, {"tunnel-params", true}} {
		if m, ok := xfMap(opts, spec.key); ok {
			for _, k := range m.Keys() {
				e.TemplateParams = append(e.TemplateParams, EntryParam{
					Name: xfQNameKey(k), Select: xfLiteralOf(m.Get(k)), Tunnel: spec.tunnel,
				})
			}
		}
	}
	// The initial context item / match selection. A node travels as a real
	// node (InitialItems); anything else is handed over as an expression.
	// F&O 3.1's source-node row: the global context item defaults to the
	// ROOT of the tree containing the supplied node (fn-transform-82b), not
	// the node itself — computed here, distinctly from the match selection,
	// so an explicit global-context-item option can still override it
	// (fn-transform-82c/82d).
	var globalItem xpath.Item
	switch {
	case xfHas(opts, "source-node"):
		its := xpath.Items(opts.Get(xpath.NewString("source-node")))
		e.InitialItems = its
		if len(its) == 1 {
			if nd, ok := its[0].(*xmltree.Node); ok {
				globalItem = rootOfNode(nd)
			} else {
				globalItem = its[0]
			}
		}
	case xfHas(opts, "initial-match-selection"):
		e.InitialItems = xpath.Items(opts.Get(xpath.NewString("initial-match-selection")))
	}
	if xfHas(opts, "global-context-item") {
		its := xpath.Items(opts.Get(xpath.NewString("global-context-item")))
		if len(its) == 1 {
			globalItem = its[0]
		}
	}
	e.GlobalContextItem = globalItem

	if xfHas(opts, "base-output-uri") {
		e.BaseOutputURI = xpath.ResolveAgainstBase(host.baseURI, xfString(opts, "base-output-uri"))
	}

	res, err := ss.TransformEntry("", e, runDir)
	if err != nil {
		return nil, err
	}
	// XSLT 3.0 §5.7.1 "sequence normalization" merges adjacent text nodes
	// produced by constructing complex content into one — e.g. three
	// consecutive xsl:value-of instructions with nothing between them make
	// ONE text node, not three (fn-transform-54..58 read it back via
	// "//out/text() = '…'", which only a single merged node can equal). The
	// engine's own node-construction path does not merge these — Node.Append
	// deliberately never does, by design, for its OTHER callers — so it is
	// done here instead, once, on fn:transform's own returned trees only.
	xfMergeAdjacentText(res.Root)
	for _, sd := range res.Secondary {
		xfMergeAdjacentText(sd.Root)
	}

	// --- assemble the result map ---------------------------------------------
	format := "document"
	if xfHas(opts, "delivery-format") {
		format = xfString(opts, "delivery-format")
	}
	var serParams *xpath.Map
	if m, ok := xfMap(opts, "serialization-params"); ok {
		serParams = m
	}
	primaryKey := "output"
	if e.BaseOutputURI != "" {
		primaryKey = e.BaseOutputURI
	}
	var postProcess *xpath.Function
	if xfHas(opts, "post-process") {
		items := xpath.Items(opts.Get(xpath.NewString("post-process")))
		if len(items) == 1 {
			if fn, ok := items[0].(*xpath.Function); ok {
				postProcess = fn
			}
		}
	}
	out := xpath.NewMap()
	// "Empty/absent principal result document" (the W3C catalog's own bug
	// 30209 note, cited on nearly every multi-result-document test case):
	// when a template constructs NOTHING at the top level — every one of its
	// xsl:result-document instructions redirected all the content — there is
	// no principal result at all, and the returned map must have no entry
	// for it (fn-transform-13a/33/37/38/43/44 all assert
	// not(map:contains($result, "output"))). A real raw/typed result
	// (fn-transform-62/84) always leaves something in resultRoot, so this
	// only ever suppresses the genuinely-empty case.
	principalAbsent := res.Root == nil || len(res.Root.Children) == 0
	if !principalAbsent {
		primary, perr := xfDeliver(ss, res.Root, res.Output, res.Method, res.Value, format, serParams)
		if perr != nil {
			return nil, perr
		}
		if postProcess != nil {
			if primary, err = postProcess.Call([]xpath.Object{xpath.NewString(primaryKey), primary}); err != nil {
				return nil, err
			}
		}
		out.Put(xpath.NewString(primaryKey), primary)
	}
	for _, sd := range res.Secondary {
		v, derr := xfDeliver(ss, sd.Root, sd.Content, sd.Method, nil, format, serParams)
		if derr != nil {
			return nil, derr
		}
		// The map key is "the URI of the document, as an absolute URI" (F&O
		// 3.1 §16.3.2) — SecondaryDoc.Href is deliberately the RAW,
		// UNRESOLVED xsl:result-document/@href (its own doc comment: "the
		// key the harness and the XTDE1490 duplicate check use"), so it must
		// be resolved here rather than used as-is (fn-transform-13a/33/37/
		// 38/43/44 all key off the resolved absolute form). An already-
		// absolute href resolves to itself regardless of base; a relative
		// one with no usable base is passed through unresolved as the best
		// available answer (matches resolveOutputURI's own fallback).
		key := sd.Href
		if abs, aerr := xpath.ResolveURIRef(sd.Href, e.BaseOutputURI); aerr == nil && abs != "" {
			key = abs
		}
		if postProcess != nil {
			if v, err = postProcess.Call([]xpath.Object{xpath.NewString(key), v}); err != nil {
				return nil, err
			}
		}
		out.Put(xpath.NewString(key), v)
	}
	return out, nil
}

// xfIsNumeric reports whether it is a single numeric atomic value (F&O 3.1's
// xslt-version option is typed xs:decimal — a plain string is a type error,
// fn-transform-err-4).
func xfIsNumeric(it xpath.Item) bool {
	a, err := xpath.AtomicFromItem(it)
	return err == nil && a.IsNumeric()
}

// xfIsQName reports whether an item is an xs:QName (stylesheet-params keys
// must be, fn-transform-err-18).
func xfIsQName(it xpath.Item) bool {
	a, err := xpath.AtomicFromItem(it)
	if err != nil {
		return false
	}
	_, ok := a.QNameValue()
	return ok
}

// xfFinalizeSelfPath resolves an already-non-empty, possibly-relative
// selfPath value (from stylesheet-location or an explicit stylesheet-base-uri
// option — never called for "nothing was supplied at all") against the
// host's own directory when it is not already absolute or a real URI —
// the QT3/FOTS environment's directory when no static base URI is in scope
// at the calling expression either (fn-transform-22/err-9a).
func xfFinalizeSelfPath(selfPath string, host xfHost) string {
	// A "file:"/"file://" URI is always a real local path underneath —
	// stripped down to one here so filepath.Dir/Join (used both below and by
	// the compiler's own further xsl:include/import resolution) operate on
	// an actual OS path rather than a string with a literal "file:" still
	// embedded partway through it (fn-transform-23/24: $include's own base
	// URI, xpath.NodeBaseURI, is exactly such a URI).
	selfPath = xfPathOf(selfPath)
	if selfPath == "" || xfLooksAbsolute(selfPath) {
		return selfPath
	}
	if host.baseDir != "" {
		return filepath.Join(host.baseDir, selfPath)
	}
	return selfPath
}

// xfLooksAbsolute reports whether s is a real filesystem-absolute path or
// carries its own URI scheme (e.g. "http://…", "file://…") — in either case
// it needs no further resolution against a base directory.
func xfLooksAbsolute(s string) bool {
	if filepath.IsAbs(s) {
		return true
	}
	if i := strings.IndexByte(s, ':'); i > 0 {
		scheme := s[:i]
		for _, c := range scheme {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.') {
				return false
			}
		}
		return true
	}
	return false
}

// xfFetchText retrieves the text at an already-resolved uri: first via the
// SAME xpath.ResourceResolver fn:doc/fn:unparsed-text use (which, for this
// engine's own production resolver, already resolves a relative reference
// against its baseDir — and for the QT3 harness's resolver, additionally
// knows the environment's declared synthetic URIs), falling back to a direct
// filesystem read of a real absolute path the resolver does not recognize.
func xfFetchText(uri string, host xfHost) (string, bool) {
	if host.resolver != nil {
		if s, ok := host.resolver.ResolveText(uri); ok {
			return s, true
		}
	}
	p := xfPathOf(uri)
	if filepath.IsAbs(p) {
		if b, err := os.ReadFile(p); err == nil {
			return string(b), true
		}
	}
	return "", false
}

// xfCheckRequestedProperties honors the "requested-properties" option (F&O
// 3.1 §16.3.2, keyed by the xsl:system-property QNames the request names) for
// the small set of properties this engine's characteristics are genuinely
// FIXED for, rather than silently ignoring a request this one, single XSLT
// implementation cannot actually vary per call. Per the Recommendation's own
// text this is a deliberate choice ("it may be appropriate to ignore the
// request... in other cases it may be more appropriate to raise an error
// [err:FOXT0001]") — this engine chooses to raise it whenever the request
// asks for a capability toggle it has no way to honor, rather than quietly
// running as if the request had never been made. Every other requested
// property (vendor/product-name, is-schema-aware, xpath-version, …) is left
// alone: this engine's actual behavior already happens to satisfy every such
// request the W3C catalog exercises, or the request is for a capability
// (e.g. a specific vendor extension) that already fails naturally with a
// real error if unmet.
func xfCheckRequestedProperties(opts *xpath.Map) error {
	m, ok := xfMap(opts, "requested-properties")
	if !ok {
		return nil
	}
	for _, k := range m.Keys() {
		a, err := xpath.AtomicFromItem(k)
		if err != nil || a.T != xpath.XSqname {
			continue
		}
		qn, _ := a.QNameValue()
		if qn.Space != NS {
			continue // only xsl:-namespaced properties are ones this engine has a fixed answer for
		}
		requested := xpath.ToBool(m.Get(k))
		var have bool
		switch qn.Local {
		case "supports-backwards-compatibility":
			have = true // XSLT 1.0 backwards-compatibility mode is always honored
		case "supports-namespace-axis":
			have = true // the namespace:: axis is always implemented
		case "supports-streaming":
			have = false // fn:transform does not itself enforce/guarantee bounded-memory execution
		default:
			continue
		}
		if requested != have {
			return fmt.Errorf("err:FOXT0001: transform(): requested-properties xsl:%s=%v is not available", qn.Local, requested)
		}
	}
	return nil
}

func xfHas(m *xpath.Map, key string) bool { return m.Contains(xpath.NewString(key)) }

// xfFirstKey renders the single item of option key as a component name.
func xfFirstKey(m *xpath.Map, key string) string {
	its := xpath.Items(m.Get(xpath.NewString(key)))
	if len(its) == 0 {
		return ""
	}
	return xfQNameKey(its[0])
}

func xfString(m *xpath.Map, key string) string {
	return xpath.ToString(m.Get(xpath.NewString(key)))
}

func xfMap(m *xpath.Map, key string) (*xpath.Map, bool) {
	its := xpath.Items(m.Get(xpath.NewString(key)))
	if len(its) != 1 {
		return nil, false
	}
	mm, ok := its[0].(*xpath.Map)
	return mm, ok
}

func xfArray(m *xpath.Map, key string) (*xpath.Array, bool) {
	its := xpath.Items(m.Get(xpath.NewString(key)))
	if len(its) != 1 {
		return nil, false
	}
	a, ok := its[0].(*xpath.Array)
	return a, ok
}

// xfQNameKey renders an option value that names a component: an xs:QName
// becomes its "{uri}local" Clark form (the engine's entry-point lookups accept
// that spelling and it carries the namespace the caller resolved), anything
// else its string value.
func xfQNameKey(it xpath.Item) string {
	if a, err := xpath.AtomicFromItem(it); err == nil {
		if qn, ok := a.QNameValue(); ok {
			return clark(qn.Space, qn.Local)
		}
	}
	return xpath.ToString(xpath.FromItems([]xpath.Item{it}))
}

// xfLiteralOf renders a parameter VALUE as an XPath expression the callee can
// re-evaluate. Strings are quoted; everything else uses its lexical form,
// which is exact for the numeric/boolean values these options carry.
//
// A map/array entry's value may be a bare Go scalar (string/float64/bool)
// rather than an *Atomic — xpath.Map's own fast-path representation — so
// the type check goes through xpath.AtomicFromItem rather than a direct type
// assertion (fn-transform-50/51/52 silently produced an UNQUOTED "Hello
// World" as an XPath expression otherwise, "unexpected trailing token").
func xfLiteralOf(v xpath.Object) string {
	its := xpath.Items(v)
	if len(its) == 1 {
		if a, err := xpath.AtomicFromItem(its[0]); err == nil && a.IsStringFamily() {
			return "'" + strings.ReplaceAll(xpath.ToString(v), "'", "''") + "'"
		}
	}
	s := xpath.ToString(v)
	if s == "" {
		return "()"
	}
	return s
}

// xfPathOf maps a file: URI (or a plain path) to a filesystem path.
func xfPathOf(uri string) string {
	u := strings.TrimSpace(uri)
	if strings.HasPrefix(u, "file://") {
		return strings.TrimPrefix(u, "file://")
	}
	if strings.HasPrefix(u, "file:") {
		return strings.TrimPrefix(u, "file:")
	}
	return u
}

// xfMergeAdjacentText merges consecutive plain-text element/document children
// into one, matching XSLT 3.0 §5.7.1's sequence-normalization rule for
// constructed complex content ("adjacent text nodes are merged into a single
// text node") — see the call site's own comment for why this runs here
// rather than in the engine's general node-construction path. Nodes that are
// not plain constructed text — an atomic-sequence item (Atomic, which gets
// its own space-separator handling elsewhere) or a non-string RealItem
// carrier (a map/array/function riding through the tree) — are left alone.
func xfMergeAdjacentText(n *xmltree.Node) {
	if n == nil || len(n.Children) == 0 {
		return
	}
	plainText := func(c *xmltree.Node) bool {
		return c.Kind == xmltree.KindText && !c.Atomic && !c.SynthCtx && c.RealItem == nil
	}
	merged := make([]*xmltree.Node, 0, len(n.Children))
	for _, c := range n.Children {
		if plainText(c) && len(merged) > 0 && plainText(merged[len(merged)-1]) {
			merged[len(merged)-1].Value += c.Value
			continue
		}
		merged = append(merged, c)
	}
	n.Children = merged
	for _, c := range n.Children {
		xfMergeAdjacentText(c)
	}
}

// xfDeliver shapes one output document per the delivery-format option:
// "serialized" hands back the serialized string (re-serialized with
// serialization-params overrides when given), "raw" the result sequence
// without its document wrapper (rawValue, when non-nil, is the ACTUAL
// sequence an initial-function/template's typed result produced, preferred
// over reconstructing it from the collector tree — see RunResult.Value), and
// "document" (the default) the document node.
func xfDeliver(ss *Stylesheet, root *xmltree.Node, serialized, method string, rawValue xpath.Object, format string, serParams *xpath.Map) (xpath.Object, error) {
	switch format {
	case "serialized":
		if serParams != nil {
			if root == nil {
				return xpath.NewString(""), nil
			}
			return xfSerialize(ss, root, method, serParams)
		}
		return xpath.NewString(serialized), nil
	case "raw":
		if rawValue != nil {
			return rawValue, nil
		}
		if root == nil {
			return xpath.Sequence{}, nil
		}
		return fragAsSequence(root), nil
	default:
		if root == nil {
			return xpath.Sequence{}, nil
		}
		return xpath.NodeSet{root}, nil
	}
}

// xfSerialize re-serializes root using the stylesheet's own xsl:output
// declaration as a base, with serialization-params overriding/augmenting it
// exactly as F&O 3.1 §16.3.2 specifies ("the same rules that apply to a map
// supplied as the second argument of fn:serialize... overrides or augments
// the value specified in the unnamed xsl:output declaration").
func xfSerialize(ss *Stylesheet, root *xmltree.Node, method string, params *xpath.Map) (xpath.Object, error) {
	cfg := ss.output
	for _, k := range params.Keys() {
		a, err := xpath.AtomicFromItem(k)
		if err != nil || a.T != xpath.XSstring {
			continue // an xs:QName (or other) key is implementation-defined; ignored, matching fn:serialize
		}
		name := xpath.ToString(k)
		v := params.Get(k)
		switch name {
		case "method":
			if s := xpath.ToString(v); s != "" {
				method, cfg.Method = s, s
			}
		case "indent":
			cfg.Indent = xpath.ToBool(v)
		case "omit-xml-declaration":
			cfg.OmitXMLDeclaration = xpath.ToBool(v)
		case "encoding":
			cfg.Encoding = xpath.ToString(v)
		case "standalone":
			cfg.Standalone = xpath.ToString(v)
		case "cdata-section-elements":
			cfg.CDATASectionElements = append(cfg.CDATASectionElements, xfQNameList(v)...)
		case "suppress-indentation":
			cfg.SuppressIndentation = append(cfg.SuppressIndentation, xfQNameList(v)...)
		case "use-character-maps":
			if cm, ok := firstItemMap(v); ok {
				if cfg.InlineCharMap == nil {
					cfg.InlineCharMap = map[rune]string{}
				}
				for _, ck := range cm.Keys() {
					r := []rune(xpath.ToString(xpath.FromItems([]xpath.Item{ck})))
					if len(r) == 1 {
						cfg.InlineCharMap[r[0]] = xpath.ToString(cm.Get(ck))
					}
				}
			}
		case "html-version":
			cfg.HTMLVersion = xpath.ToString(v)
		}
	}
	includeContentType := !cfg.NoContentType && (method == "html" || method == "xhtml")
	so := ss.serializeOptions(cfg, method, includeContentType)
	return xpath.NewString(xmltree.Serialize(root, so)), nil
}

// firstItemMap returns the single map item of a sequence, if that is what it is.
func firstItemMap(o xpath.Object) (*xpath.Map, bool) {
	its := xpath.Items(o)
	if len(its) != 1 {
		return nil, false
	}
	m, ok := its[0].(*xpath.Map)
	return m, ok
}

// xfQNameList collects a sequence of xs:QName items into xmltree.Names
// (cdata-section-elements/suppress-indentation serialization-params).
func xfQNameList(o xpath.Object) []xmltree.Name {
	var out []xmltree.Name
	for _, it := range xpath.Items(o) {
		if a, err := xpath.AtomicFromItem(it); err == nil {
			if qn, ok := a.QNameValue(); ok {
				out = append(out, xmltree.Name{Space: qn.Space, Local: qn.Local})
			}
		}
	}
	return out
}
