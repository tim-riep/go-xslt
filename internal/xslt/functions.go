package xslt

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// xsltFuncs is the registry of XSLT context functions (no-prefix) added by
// instr_*.go via init(). Package-var literal so init() additions are safe.
// Returns (value, ok, err); ok=false means "not this function".
var xsltFuncs = map[string]func(eng *engine, args []xpath.Object, env *evalEnv) (xpath.Object, bool, error){}

// callFunc resolves XSLT-specific functions not handled by the core XPath
// library. It returns ok=false for unknown functions so the caller can report
// checkCapturedFocus implements XSLT 3.0 §10.3.6 for the two accumulator
// functions: a named function reference (or fn:function-lookup) saves the
// focus into the function item's closure, but "in the case where the context
// item is a node in a streamed input document, saving the node is not
// possible. In this case, therefore, the context is saved with an absent
// focus, so the call on F will fail with a dynamic error saying that there is
// no context item available" — XTDE3350.
//
// Scoped to a call that actually came THROUGH a function item
// (Context.ViaFunctionItem) and whose captured node is in a document the host
// declared streamed. A direct call is untouched, and so is a function item
// captured over an ordinary tree — accumulator-062 is exactly that case, and
// its catalog entry ("succeeds with early binding of context item") pins the
// early binding this must not disturb.
func (eng *engine) checkCapturedFocus(local string, ctx *xpath.Context) error {
	if ctx == nil || !ctx.ViaFunctionItem || len(eng.hostStreamedDocs) == 0 {
		return nil
	}
	if local != "accumulator-before" && local != "accumulator-after" {
		return nil
	}
	if ctx.Node == nil || !eng.hostStreamedDocs[rootOfNode(ctx.Node)] {
		return nil
	}
	return errAt(nil, "err:XTDE3350: %s() has no context item: the focus of a streamed node cannot be saved in a function item", local)
}

// an "unknown function" error.
func (eng *engine) callFunc(prefix, local string, args []xpath.Object, env *evalEnv, ctx *xpath.Context) (xpath.Object, bool, error) {
	// User-defined xsl:function (always namespace-qualified).
	if prefix != "" {
		uri, ok := env.el.LookupPrefix(prefix)
		if !ok {
			// A braced EQName call Q{uri}local() for a namespace outside the
			// fn/math/map/array/xs set arrives here with prefix set to the
			// raw URI itself (the parser's bracedPrefixForURI passes an
			// unrecognized namespace straight through instead of a real
			// in-scope prefix token) — use it directly as the namespace,
			// rather than failing to resolve it as a prefix (function-0121:
			// both the xsl:function declaration and the call site name the
			// function via Q{http://app.com/}count-elements).
			uri = prefix
		}
		if fd, ok := eng.sheet.functions[funcKey(uri, local, len(args))]; ok {
			if err := eng.checkEvaluateVisibility(fd); err != nil {
				return nil, true, err
			}
			return eng.callUserFunc(fd, args, env)
		}
		if uri == xpathFunctionsNS {
			// XSLT's own "instruction" functions below (document, key,
			// current, generate-id, …) — normally called with no prefix —
			// may also be invoked with an explicit fn: prefix, since they are
			// formally members of the standard function namespace
			// (document-0301: document($u) is fn:document($u)).
			return eng.callFunc("", local, args, env, ctx)
		}
		return nil, false, nil
	}
	if eng.evaluateDepth > 0 && evNotAvailable[local] {
		return nil, true, errAt(nil, "err:XTDE3160: %s() is not available in the static context of xsl:evaluate", local)
	}
	// Registry-based XSLT context functions (current-group, current-merge-*,
	// accumulator-before/after, …) registered by instr_*.go via init().
	if fn, ok := xsltFuncs[local]; ok {
		if err := eng.checkCapturedFocus(local, ctx); err != nil {
			return nil, true, err
		}
		return fn(eng, args, env)
	}
	switch local {
	case "key":
		// The key name argument is a lexical QName resolved against the
		// in-scope namespaces at the key() call site, to its expanded (Clark)
		// form — matching how xsl:key's own @name is resolved at compile time
		// (compileKey), so a different prefix bound to the same namespace URI
		// still finds the key (key-013).
		keyLex := xpath.ToString(arg0(args))
		keyName := keyLex
		if env.el != nil {
			if prefix, _, ok := strings.Cut(keyLex, ":"); ok && prefix != "" && !strings.HasPrefix(keyLex, "Q{") {
				// A prefixed key name with no in-scope namespace declaration
				// for its prefix is XTDE1260 (error-1260f), not silently
				// treated as the no-namespace name.
				if _, boundOK := env.el.LookupPrefix(prefix); !boundOK {
					return nil, true, errAt(nil, "err:XTDE1260: no namespace declaration in scope for prefix %q in key name %q", prefix, keyLex)
				}
			}
			keyName = clarkName(resolveQName(env.el, keyLex))
		}
		// The key must be declared (XTDE1260). A composite key ties its
		// second argument's items together as ONE compound tuple to look up,
		// rather than searching for any one of them separately (key-096/097)
		// — every xsl:key declaration sharing a name must agree, so finding
		// composite="yes" on any one of them is enough.
		known := false
		composite := false
		collURI := ""
		for _, kd := range eng.sheet.keys {
			if kd.name == keyName {
				known = true
				if kd.composite {
					composite = true
				}
				if kd.collURI != "" {
					collURI = kd.collURI
				}
			}
		}
		if !known {
			return nil, true, errAt(nil, "err:XTDE1260: no xsl:key named %q", keyLex)
		}
		// The searched tree is the context node's document, or (third argument)
		// the tree containing $top — results are then scoped to $top's subtree.
		root := eng.doc
		haveCtxNode := ctx != nil && ctx.Node != nil
		if haveCtxNode {
			root = xdmRoot(ctx.Node)
		}
		var scope xpath.NodeSet
		if len(args) > 2 {
			if ns, ok := xpath.ToNodeSet(args[2]); ok && len(ns) > 0 {
				root = xdmRoot(ns[0])
				scope = ns
			}
		} else if !haveCtxNode {
			// The two-argument form searches the tree containing the context
			// node — with no context node at all, that is undefined (XTDE1270).
			return nil, true, errAt(nil, "err:XTDE1270: key() requires a context node in a document")
		}
		if root == nil {
			return nil, true, errAt(nil, "err:XTDE1270: key() requires a context node in a document")
		}
		if root.Kind != xmltree.KindDocument {
			return nil, true, errAt(nil, "err:XTDE1270: key() requires the searched tree's root to be a document node")
		}
		idx, kerr := eng.keyIndexOn(keyName, root)
		if kerr != nil {
			return nil, true, kerr
		}
		var values []string
		if composite {
			// The whole second-argument sequence is ONE tuple to look up
			// (mirrors CompositeKeyString's use in keyIndexOn exactly).
			values = []string{canonKeyString(xpath.CompositeKeyString(xpath.Items(argN(args, 1))), collURI)}
		} else if ns, ok := xpath.ToNodeSet(argN(args, 1)); ok {
			for _, n := range ns {
				values = append(values, canonKeyString(keyNodeString(n), collURI))
			}
		} else {
			// A typed atomic search value (e.g. xs:dateTime(...), xs:double(...))
			// must be canonicalized the same "eq"-equivalence-class way the index
			// itself is built (KeyString), not by lexical string form — otherwise
			// e.g. two dateTimes denoting the same instant with different
			// timezone offsets, or NaN, would (mis)match by accident of spelling
			// (key-069/070); collURI additionally re-canonicalizes string-family
			// keys per the key's @collation/@default-collation (collations-0105).
			for _, it := range xpath.Items(argN(args, 1)) {
				values = append(values, canonKeyString(xpath.KeyString(it), collURI))
			}
		}
		var out xpath.NodeSet
		for _, v := range values {
			out = append(out, idx[v]...)
		}
		if scope != nil {
			var scoped xpath.NodeSet
			for _, n := range out {
				for _, top := range scope {
					if nodeInSubtree(n, top) {
						scoped = append(scoped, n)
						break
					}
				}
			}
			out = scoped
		}
		return dedupNodes(out), true, nil

	case "regex-group":
		n := int(xpath.ToNumber(arg0(args)))
		if len(eng.regexGroups) > 0 {
			g := eng.regexGroups[len(eng.regexGroups)-1]
			if n >= 0 && n < len(g) {
				return g[n], true, nil
			}
		}
		return "", true, nil
	}
	switch local {
	case "current":
		if env.current == nil {
			return nil, true, errAt(nil, "err:XTDE1360: current() called when the context item is absent")
		}
		// An xsl:for-each/filter/analyze-string iteration over an ATOMIC
		// sequence models its item as a synthetic type-annotated text node
		// (so "." can still answer node questions) — current() must give
		// back the ORIGINAL ATOMIC VALUE there (XSLT 3.0's current() is
		// item()-valued, not node()-only), not the wrapper node itself,
		// which as a NodeSet is never a NUMBER for e.g. a $seq[current()]
		// positional predicate test (function-1013).
		if env.current.Kind == xmltree.KindText && xpath.IsTypeAnnotated(env.current) {
			if items, aerr := xpath.Atomize(xpath.NodeSet{env.current}); aerr == nil && len(items) == 1 {
				return items[0], true, nil
			}
		}
		return xpath.NodeSet{env.current}, true, nil

	case "generate-id":
		// Zero-arity generate-id() uses the CONTEXT node (".", which inside a
		// predicate is the node being tested), not current().
		node := env.current
		if ctx != nil && ctx.Node != nil {
			node = ctx.Node
		}
		if len(args) > 0 {
			if ns, ok := xpath.ToNodeSet(args[0]); ok {
				if len(ns) == 0 {
					return "", true, nil
				}
				node = ns[0]
			}
		}
		if node == nil {
			return "", true, nil
		}
		return "id" + strconv.Itoa(node.Order()), true, nil

	case "document":
		// document($uri-sequence[, $base-node]) — XSLT's legacy analog of
		// fn:doc, extended (2.0+) to accept a general sequence (XSLT 1.0
		// §12.1). A NODE item's string value resolves against THAT node's own
		// base-uri; a plain atomic item's resolves against the base-uri of
		// the first node in $base-node when given, else the static base URI
		// of the stylesheet element containing this call (ctx.BaseURI,
		// document-0107/0111/0203). The result never contains duplicate
		// nodes (document-0401).
		if len(args) == 0 {
			return xpath.NodeSet{}, true, nil
		}
		atomicBase := ctx.BaseURI
		haveBaseNode := false
		if len(args) > 1 {
			if ns, ok := xpath.ToNodeSet(args[1]); ok && len(ns) > 0 {
				atomicBase = xpath.NodeBaseURI(ns[0], ctx.BaseURI)
				haveBaseNode = true
			}
		}
		var out xpath.NodeSet
		for _, it := range xpath.Items(args[0]) {
			// ToString needs an Object, not a bare Item: xpath.Items(NodeSet)
			// yields raw *xmltree.Node items, which ToString's type switch
			// does not itself recognize (only its NodeSet/Sequence wrappers)
			// — wrapping via FromItems (as key()'s own argN(args,1) loop
			// above already does) is what makes a node argument stringify to
			// its string-value instead of silently going to "".
			uri := xpath.ToString(xpath.FromItems([]xpath.Item{it}))
			// A fragment identifier selects a node WITHIN the retrieved
			// document rather than the document node itself (XSLT §16.1): for
			// XML media types it names an element by its ID (id-001).
			frag := ""
			if h := strings.IndexByte(uri, '#'); h >= 0 {
				uri, frag = uri[:h], uri[h+1:]
			}
			base := atomicBase
			// An explicit $base-node governs EVERY item, including node items
			// that would otherwise resolve against their own base-uri
			// (XSLT 1.0 §12.1 — resolve-uri-010).
			if nd, ok := it.(*xmltree.Node); ok && !haveBaseNode {
				base = xpath.NodeBaseURI(nd, ctx.BaseURI)
			}
			if frag != "" && uri == "" && len(args) > 1 {
				// document('#id', $node): the empty URI reference denotes the
				// document $node belongs to, whose retrieval URI this engine
				// may not know (a source document supplied as text has none).
				if ns, ok := xpath.ToNodeSet(args[1]); ok && len(ns) > 0 {
					if root := rootOfNode(ns[0]); root != nil {
						if el := elementWithID(root, frag); el != nil {
							out = append(out, el)
						}
						continue
					}
				}
			}
			// XTDE1162: a $base-node was supplied but has NO base URI at all
			// (a parentless node built by an @as-typed variable), so a
			// relative href cannot be resolved. Without this the unresolved
			// relative reference would silently fall through to the resolver
			// and be resolved against the workspace directory instead
			// (error-1162b). Scoped to the explicit-$base-node case: the
			// ctx.BaseURI fallback legitimately stays empty for a caller that
			// compiled a stylesheet from a string with no location.
			if haveBaseNode && base == "" && uri != "" && !strings.Contains(uri, ":") {
				return nil, true, errAt(nil, "err:XTDE1162: the second argument of document() has no base URI, so %q cannot be resolved", uri)
			}
			resolved := uri
			if r, rerr := xpath.ResolveURIRef(uri, base); rerr == nil {
				resolved = r
			}
			// document(""): the tree of the stylesheet module containing this
			// call (copy-1203) — but ONLY when the effective (resolved) URI
			// actually denotes that module's own location. An xml:base
			// override in scope at the call site can redirect an EMPTY href
			// elsewhere entirely (base-uri-050: xml:base="baseuri023.xml" on
			// the enclosing template means document('') there names THAT
			// file, not this stylesheet), in which case it must fall through
			// to a real resolver lookup like any other href.
			if env.el != nil {
				if home := rootOfNode(env.el); home != nil && (resolved == "" || resolved == home.Base) {
					// Read as a SOURCE document: stripped per xsl:strip-space
					// (eng.homeDoc — where-populated-100).
					out = appendDocResult(out, eng.homeDoc(home), frag)
					continue
				}
			}
			if eng.resolver == nil {
				return nil, true, errAt(nil, "err:XTDE1460: document(%q) could not be retrieved", uri)
			}
			d, ok := eng.resolver.ResolveDoc(resolved)
			if !ok {
				return nil, true, errAt(nil, "err:XTDE1460: document(%q) could not be retrieved", uri)
			}
			out = appendDocResult(out, d, frag)
		}
		// Deduplicate by node IDENTITY, not dedupNodes/NodeSet.Unique's
		// document-order sort: Order() is a PER-DOCUMENT counter (assigned
		// independently by each separate xmltree.Parse), so comparing it
		// ACROSS the several unrelated documents document() can return is
		// meaningless — two document ROOTS routinely tie at Order()==0,
		// which leaves genuine repeats (document-0107's doc02.xml, loaded
		// once directly and once via $ea) non-adjacent after the sort and
		// so uncaught. The catalog's own comment concedes multi-document
		// order is "unpredictable" here, so first-occurrence order is fine.
		seen := make(map[*xmltree.Node]bool, len(out))
		uniq := out[:0]
		for _, d := range out {
			if !seen[d] {
				seen[d] = true
				uniq = append(uniq, d)
			}
		}
		return uniq, true, nil

	case "current-output-uri":
		// The absolute URI of the result document currently being written, or
		// the empty sequence when there is none: no host-supplied base output
		// URI (current-output-uri-013/015), temporary output state — an
		// xsl:function/variable/key/accumulator body (005/006/007), or a
		// context where the XSLT dynamic context is not available at all, such
		// as a match pattern or a dynamically-called function item (008/016/017).
		if ctx != nil && ctx.NoOutputURI {
			return xpath.Sequence{}, true, nil
		}
		uri := eng.currentOutputURI()
		if uri == "" {
			return xpath.Sequence{}, true, nil
		}
		return xpath.NewAnyURI(uri), true, nil

	case "copy-of", "snapshot":
		// Deep copy of the argument (context item when omitted): nodes get fresh
		// identity, atomic/map/array items pass through unchanged. fn:snapshot
		// additionally rebuilds the ANCESTOR SPINE (see snapshotNode).
		var in xpath.Object
		switch {
		case len(args) > 0:
			in = args[0]
		case ctx != nil && ctx.CtxItem != nil:
			in = xpath.FromItems([]xpath.Item{ctx.CtxItem})
		case ctx != nil && ctx.Node != nil:
			// The CONTEXT ITEM of the call site, not the template's current
			// node: "/doc/*/copy-of()" applies to each step's context node
			// (copy-of-001).
			in = xpath.NodeSet{ctx.Node}
		case env.current != nil:
			in = xpath.NodeSet{env.current}
		default:
			return nil, true, errAt(nil, "err:XPDY0002: fn:%s has no context item", local)
		}
		var items []xpath.Item
		for _, it := range xpath.Items(in) {
			nd, ok := it.(*xmltree.Node)
			if !ok {
				items = append(items, it)
				continue
			}
			// XTTE0950: copying an ATTRIBUTE with namespace-sensitive content
			// (a typed value of xs:QName/xs:NOTATION, or a type derived from
			// one) without its parent element loses the very bindings the
			// value's prefix resolves through. fn:copy-of preserves types, so
			// the rule's "validation='preserve'" precondition always holds
			// here (copy-of-009).
			if nd.Kind == xmltree.KindAttribute && nsSensitiveNode(nd) {
				return nil, true, errAt(nil,
					"err:XTTE0950: fn:%s cannot copy the attribute %s, whose content is namespace-sensitive, without its parent element",
					local, nd.Name.Local)
			}
			var cp *xmltree.Node
			if local == "snapshot" {
				cp = snapshotNode(nd)
			} else {
				cp = cloneNode(nd)
			}
			materializeInScopeNS(cp, nd)
			if local != "snapshot" {
				// A detached fn:copy-of copy is a tree of its own, and until it
				// is stamped it has none: every node in it keeps the zero order
				// and the zero tree id, so two unrelated copies compare EQUAL in
				// document order and all of them sort ahead of every real tree
				// (whose ids start at 1). That is what put the copies before the
				// variable's nodes in "$insertion union copy-of(...)". Stamping
				// it as a constructed tree puts it after every retrieved one
				// and, among constructed trees, in creation order — the ordering
				// xmltree's ephemeralTreeBit already defines. Marking the root
				// Ephemeral is what selects that half: the only other reader of
				// the flag, fn:document-uri, returns early for anything but a
				// document node, and a copied DOCUMENT node is already marked by
				// cloneNode.
				//
				// fn:snapshot is excluded because snapshotNode numbers the whole
				// snapshot — the copied node PLUS the ancestor spine it rebuilds
				// — from that spine's own root. Re-stamping from the returned
				// node would renumber its subtree alone and split one tree in
				// two, which snapshot-0112 catches by checking that every node
				// in a snapshot has a distinct generate-id().
				cp.Ephemeral = true
				xmltree.AssignOrder(cp)
			}
			// fn:copy-of and fn:snapshot both PRESERVE accumulator values
			// (XSLT 3.0 §18.2.4, accumulator-046/047).
			eng.recordAccOrigin(cp, nd)
			items = append(items, cp)
		}
		return xpath.FromItems(items), true, nil

	case "available-system-properties":
		return availableSystemProperties(), true, nil

	case "system-property":
		name := xpath.ToString(arg0(args))
		if err := checkQNameArg(env, name, "XTDE1390"); err != nil {
			return nil, true, err
		}
		var el *xmltree.Node
		if env != nil {
			el = env.el
		}
		uri, local := funcAvailableName(el, name)
		if uri == NS {
			return xsltSystemProperty(local), true, nil
		}
		return "", true, nil

	case "function-available":
		name := xpath.ToString(arg0(args))
		if err := checkQNameArg(env, name, "XTDE1400"); err != nil {
			return nil, true, err
		}
		hasArity := len(args) > 1
		arity := 0
		if hasArity {
			arity = int(xpath.ToNumber(args[1]))
		}
		return eng.funcAvailable(env, name, arity, hasArity), true, nil

	case "element-available":
		name := xpath.ToString(arg0(args))
		if err := checkQNameArg(env, name, "XTDE1440"); err != nil {
			return nil, true, err
		}
		var el *xmltree.Node
		if env != nil {
			el = env.el
		}
		uri, local := elementAvailableName(el, name)
		_, known := xsltElemSpecs[local]
		// xsl:import-schema is available exactly when this run claims
		// schema-awareness — which is what its own declRegistry entry
		// enforces, rejecting the declaration outright otherwise. Reporting
		// anything else here contradicts system-property('xsl:is-schema-aware')
		// in the same run (catalog-006 asserts the two agree: an XSLT element
		// may be unavailable only when that property says "no").
		if local == "import-schema" && uri == NS {
			return schemaAwareRun(), true, nil
		}
		return uri == NS && (known || elementAvailableExtra[local]), true, nil

	case "type-available":
		name := xpath.ToString(arg0(args))
		if err := checkQNameArg(env, name, "XTDE1450"); err != nil {
			return nil, true, err
		}
		var el *xmltree.Node
		if env != nil {
			el = env.el
		}
		uri, local := resolveAvailableName(el, name, "")
		return typeAvailable(el, uri, local), true, nil

	case "unparsed-entity-uri", "unparsed-entity-public-id":
		node := env.current
		if len(args) > 1 {
			if ns, ok := xpath.ToNodeSet(args[1]); ok && len(ns) > 0 {
				node = ns[0]
			}
		}
		if node == nil {
			return nil, true, errAt(nil, "err:XTDE1370: %s() requires a context node in a document", local)
		}
		root := xdmRoot(node)
		if root == nil || root.Kind != xmltree.KindDocument {
			return nil, true, errAt(nil, "err:XTDE1370: %s() requires the context node's tree to be rooted in a document node", local)
		}
		ent, ok := root.Unparsed[xpath.ToString(arg0(args))]
		if !ok {
			// No such unparsed entity: the zero-length string (never an error).
			if local == "unparsed-entity-uri" {
				return xpath.NewAnyURI(""), true, nil
			}
			return "", true, nil
		}
		if local == "unparsed-entity-public-id" {
			return ent.PublicID, true, nil
		}
		// The system identifier is resolved against the base URI of the
		// document in which the entity was declared, and the result is typed
		// xs:anyURI (unparsed-entity-11).
		uri := ent.SystemID
		if abs, err := xpath.ResolveURIRef(uri, xpath.NodeBaseURI(root, ctx.BaseURI)); err == nil && abs != "" {
			uri = abs
		}
		return xpath.NewAnyURI(uri), true, nil

	case "stream-available":
		// This engine never streams (documented design choice), but
		// fn:stream-available is a required function on every processor and
		// its contract is about the DOCUMENT, not about whether this
		// processor would use streaming: true when the resource can be
		// retrieved and begins as well-formed XML — a truncated document
		// (stream-available-004) or one with two top-level elements
		// (stream-available-005) still starts streamably, while a missing
		// file (001), a non-XML file (003) or one that is nothing but a DTD
		// (006) does not.
		if eng.resolver == nil {
			return false, true, nil
		}
		uri := xpath.ToString(arg0(args))
		if uri == "" {
			return false, true, nil
		}
		if env != nil && env.el != nil {
			if abs, err := xpath.ResolveURIRef(uri, xpath.NodeBaseURI(env.el, "")); err == nil && abs != "" {
				uri = abs
			}
		}
		text, ok := eng.resolver.ResolveText(uri)
		if !ok {
			return false, true, nil
		}
		return startsWithXMLElement(text), true, nil
	}
	return nil, false, nil
}

// startsWithXMLElement reports whether s, after its XML prolog (BOM,
// whitespace, XML declaration, processing instructions, comments and a
// DOCTYPE declaration with any internal subset), begins with an element start
// tag. It deliberately inspects only the prolog: whether the REST of the
// document is well-formed is exactly what a streaming processor cannot know
// up front, and fn:stream-available must not pretend otherwise.
func startsWithXMLElement(s string) bool {
	s = strings.TrimPrefix(s, "\ufeff")
	i := 0
	for {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
			i++
		}
		if i >= len(s) || s[i] != '<' {
			return false
		}
		switch {
		case strings.HasPrefix(s[i:], "<?"):
			j := strings.Index(s[i:], "?>")
			if j < 0 {
				return false
			}
			i += j + 2
		case strings.HasPrefix(s[i:], "<!--"):
			j := strings.Index(s[i+4:], "-->")
			if j < 0 {
				return false
			}
			i += 4 + j + 3
		case strings.HasPrefix(s[i:], "<!DOCTYPE"):
			j := skipDoctype(s, i)
			if j < 0 {
				return false
			}
			i = j
		default:
			// An element start tag: "<" followed by a name start character.
			if i+1 >= len(s) {
				return false
			}
			c := s[i+1]
			return c == '_' || c == ':' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
		}
	}
}

// skipDoctype returns the index just past the DOCTYPE declaration beginning at
// s[i], including any bracketed internal subset, or -1 if it is unterminated.
func skipDoctype(s string, i int) int {
	depth := 0
	for ; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
		case '>':
			if depth <= 0 {
				return i + 1
			}
		}
	}
	return -1
}

// checkQNameArg validates a QName-valued function argument (lexical form and
// prefix binding), raising the given dynamic error code.
func checkQNameArg(env *evalEnv, name, code string) error {
	if !isLexicalQName(name) {
		return errAt(nil, "err:%s: %q is not a valid QName", code, name)
	}
	if env != nil && env.el != nil {
		if err := checkPrefixBound(env.el, name); err != nil {
			return errAt(nil, "err:%s: unbound prefix in %q", code, name)
		}
	}
	return nil
}

func arg0(args []xpath.Object) xpath.Object {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

func argN(args []xpath.Object, i int) xpath.Object {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func dedupNodes(ns xpath.NodeSet) xpath.NodeSet { return ns.Unique() }

// callUserFunc invokes a compiled xsl:function. Arguments are bound positionally
// to the declared params; the body is evaluated to a value (a leading
// xsl:sequence/xsl:value-of yields its raw value, otherwise the body's text).
func (eng *engine) callUserFunc(fd *FuncDef, args []xpath.Object, env *evalEnv) (xpath.Object, bool, error) {
	if fd.visibility == "abstract" {
		// XTDE3052: an abstract component of a used package declares a
		// signature but no implementation — a using package must override it
		// before it can ever actually be CALLED (use-package-295: the
		// override never happens, and the call is reached only through
		// another function, so this can only be caught here, dynamically,
		// not by the static XTSE3080/checkAbstractRefs check, which covers
		// only a direct xsl:call-template reference in the SAME module).
		return nil, true, errAt(fd.el, "err:XTDE3052: function %s#%d is abstract and has no implementation", clarkName(fd.name), len(fd.params))
	}
	eng.depth++
	defer func() { eng.depth-- }()
	if eng.depth > maxTemplateDepth {
		return nil, true, errAt(fd.el, "err:XTDE0040: function recursion too deep (possible non-terminating stylesheet)")
	}
	eng.funcDepth++
	defer func() { eng.funcDepth-- }()
	// A function declared new-each-time="no" / cache="yes" is a pure function
	// of its arguments, so each distinct argument tuple is evaluated once and
	// the RESULT ITSELF is reused — which is what makes repeated calls deliver
	// the same node identities (function-1025/1026) and what makes a naively
	// exponential recursion finish at all (function-1031's fib(92)).
	memoKey, memoOK := "", false
	if fd.memoize {
		if memoKey, memoOK = funcMemoKey(fd, args); memoOK {
			if hit, ok := eng.funcMemo[memoKey]; ok {
				return hit.val, hit.confident, nil
			}
		}
	}
	eng.pushScope()
	defer eng.popScope()
	// Calling a stylesheet function establishes a fresh, EMPTY tunnel
	// parameter set: unlike xsl:call-template/apply-templates (which forward
	// the ambient tunnel set unless a NEW one is supplied), a function
	// invocation is fully isolated from whatever tunnel context happens to
	// be active at its call site (tunnel-0113: an apply-templates further
	// out supplies tunnel param "par"="abc", but a nested xsl:call-template
	// inside a function body — with no with-param of its own — must see the
	// CALLED template's own default "123", not "abc" leaking through).
	prevTunnel := eng.tunnel
	eng.tunnel = nil
	defer func() { eng.tunnel = prevTunnel }()
	for i, p := range fd.params {
		var v xpath.Object = ""
		if i < len(args) {
			v = args[i]
		}
		// Coerce the argument to the parameter's declared @as type under the
		// function-conversion rules (e.g. xs:anyURI → xs:string, numeric
		// promotion) — function-1015. A supplied value the conversion
		// rules cannot reconcile with the declared type is XTTE0790
		// (function-1019: 'banana' passed where xs:integer is declared).
		if p.as != "" {
			cv, ok := xpath.CoerceToDeclaredTypeCtx(p.as, v, asTypeCtx(p.el))
			if !ok {
				return nil, true, errAt(p.el, "err:XTTE0790: supplied value of parameter $%s does not match declared type %q", p.name.Local, p.as)
			}
			v = cv
		}
		eng.bindVar(p.name, v)
	}
	// A stylesheet function call establishes a context with NO context item
	// (unless a future xsl:context-item/use="required" feature says
	// otherwise) — it does not inherit the caller's context node
	// (error-XPDY0002c/d, error-1270a/b: key()/"."/count(//*) used inside a
	// function body with no supplied context is XPDY0002).
	r := rt{noFocus: true}
	// XSLT §15.2: the set of captured substrings is part of the dynamic
	// context and is reset to an EMPTY sequence when a stylesheet function is
	// evaluated, so regex-group() inside a function called from an
	// xsl:matching-substring returns "" rather than the caller's groups
	// (analyze-string-034/076/077).
	savedGroups := eng.regexGroups
	eng.regexGroups = nil
	// XSLT 2.0 erratum XT.E19: the CURRENT MODE is likewise not part of a
	// stylesheet function's dynamic context — mode="#current" inside a
	// function body means the unnamed mode, not the mode the caller happened
	// to be running in (mode-1401).
	savedMode := eng.curMode
	eng.curMode = ""
	result, confident, err := eng.evalFuncBody(fd, r)
	eng.curMode = savedMode
	eng.regexGroups = savedGroups
	if err != nil {
		return nil, true, err
	}
	if fd.as != "" {
		// If the as attribute of xsl:function is specified, the sequence
		// constructor's result is converted to the required type using the
		// function conversion rules; it is a type error (XTTE0780) if this
		// conversion fails (as-0113/0136/0146).
		//
		// That enforcement is only raised when the value came from the
		// single-instruction fast path below (a direct, unambiguous XPath
		// value — confident=true): the general multi-instruction path built
		// via xsl:copy/tree construction can under-report a node's true
		// shape (e.g. xsl:copy of a document node flattens its children
		// instead of yielding one wrapped document-node item — copy-4802's
		// recursive identity-transform helper relies on that), so a mismatch
		// there stays best-effort/lenient, matching every other @as site in
		// this engine (VarDef, template params, …) rather than raising a
		// false positive.
		cv, ok := xpath.CoerceToDeclaredTypeCtx(fd.as, result, asTypeCtx(fd.el))
		if ok {
			result = cv
		} else if confident {
			return nil, true, errAt(fd.el, "err:XTTE0780: result of function %s does not match declared return type %q", fd.name.Local, fd.as)
		}
	}
	if memoOK {
		if eng.funcMemo == nil {
			eng.funcMemo = map[string]memoResult{}
		}
		eng.funcMemo[memoKey] = memoResult{val: result, confident: true}
	}
	return result, true, nil
}

// evalFuncBody computes the raw (pre-@as-conversion) value of an xsl:function
// body: a leading xsl:sequence/xsl:value-of yields its raw value directly
// (confident=true — a direct, unambiguous XPath value), otherwise the body's
// constructed content (confident=false — tree construction can under-report
// a result's true shape, e.g. document-node flattening in xsl:copy).
func (eng *engine) evalFuncBody(fd *FuncDef, r rt) (result xpath.Object, confident bool, err error) {
	// An xsl:function body runs in TEMPORARY OUTPUT STATE: it is not writing a
	// result document, so fn:current-output-uri() is the empty sequence there
	// (current-output-uri-005/008). The general path below increments this too;
	// the counter is a depth, so the overlap is harmless — but the fast paths
	// that return early were previously not covered at all.
	eng.tempOutputDepth++
	defer func() { eng.tempOutputDepth-- }()
	if len(fd.body) == 1 {
		switch b := fd.body[0].(type) {
		case *sequenceInstr:
			if b.sel != nil { // body (content) form falls through to general handling
				v, err := eng.eval(b.sel, b.el, r)
				return v, true, err
			}
		case *valueOf:
			if b.sel == nil { // body (content) form falls through to general handling
				break
			}
			s, err := eng.evalString(b.sel, b.el, r)
			if err != nil {
				return nil, true, err
			}
			if fd.as == "" {
				return s, true, nil
			}
			if !xpath.SeqTypeWantsNodes(fd.as) {
				// xsl:value-of constructs a TEXT NODE, whose atomized value is
				// xs:untypedAtomic — not the concrete xs:string a bare Go
				// string is otherwise treated as (itemAtomType) — so a
				// non-node-kind declared type (item(), an atomic type, …)
				// still gets the untypedAtomic->target cast (function
				// conversion rules) instead of an XSstring mismatch (as-0146:
				// as="xs:nonPositiveInteger" over <xsl:value-of
				// select="xs:nonPositiveInteger(...)"/>).
				return xpath.NewUntyped(s), true, nil
			}
			// A node-kind target (text()/node()) needs an actual text node —
			// xsl:value-of always constructs exactly one (avt-1205: as="text()*").
			return xpath.NodeSet{textNode(s, b.doe)}, true, nil
		case *evEvaluate:
			// A function body that is a single xsl:evaluate returns the
			// dynamic expression's SEQUENCE (evaluate-028/029).
			v, err := b.value(eng, r)
			return v, true, err
		case *performSort:
			// xsl:perform-sort's result is a SEQUENCE, and a function whose
			// whole body is one such instruction returns exactly that — not
			// the flat string the general tree-construction path below would
			// make of a purely atomic result (namespace-2401: a sort helper
			// over in-scope-prefixes() feeding xsl:value-of/@separator).
			nodes, err := psInput(b, eng, r)
			if err != nil {
				return nil, true, err
			}
			if len(b.sorts) > 0 {
				if nodes, err = eng.sortNodes(nodes, b.sorts, b.el, r); err != nil {
					return nil, true, err
				}
			}
			items := make([]xpath.Item, 0, len(nodes))
			for _, nd := range nodes {
				if nd.Kind == xmltree.KindText && nd.Atomic {
					// A synthetic wrapper stands in for an atomic item; hand
					// back the atomic value it represents.
					if at, aerr := xpath.Atomize(xpath.NodeSet{nd}); aerr == nil && len(at) == 1 {
						items = append(items, at[0])
						continue
					}
				}
				items = append(items, nd)
			}
			return xpath.FromItems(items), true, nil
		case *wherePopulated:
			// xsl:where-populated's result is well-defined and precisely
			// computed by its own exec (itemPopulated's doc comment) —
			// unlike the general multi-instruction path below, it never
			// under-reports the true shape of its result, so a function
			// whose whole body is one xsl:where-populated is just as
			// "confident" as the xsl:sequence/xsl:perform-sort cases above:
			// an empty result here genuinely IS the empty sequence, and
			// @as="element()" (etc) enforcement in callUserFunc must see it
			// (where-populated-coco-102: XTTE0780 when the wrapped element
			// this drops as unpopulated was the function's only possible
			// result).
			frag := &xmltree.Node{Kind: xmltree.KindDocument, NoAtomicMerge: true, KeepDocItems: true}
			if err := b.exec(eng, r, frag); err != nil {
				return nil, true, err
			}
			return fragAsSequence(frag), true, nil
		}
	}
	// General case: an xsl:function body is a sequence constructor. When it
	// constructs real markup (elements, comments, PIs, attributes, namespace
	// nodes) the function's value must be that SEQUENCE, preserving node
	// identity, so a caller can path-step off the result (copy-3702: a body
	// of xsl:copy-of followed by a conditional xsl:sequence must return the
	// copied element(s), not their concatenated string-value).
	//
	// But every atomic-valued sequence constructor item (xsl:value-of,
	// xsl:sequence select="'x'", a literal expand-text {..}) is ALSO built by
	// appending a synthetic text node — execution has no other way to place
	// an atomic value into a tree — and once several such items are baked
	// into frag.Children as separate nodes, re-embedding them elsewhere via
	// deepCopyInto (a NODE copy) permanently loses the "adjacent atomics get
	// a space separator" rule that appendAtomicText applies only to genuine
	// atomic items (seqtor-024/030/031/033: several sequence/text items,
	// none of them real markup, must still join with the customary spaces).
	// So: only switch to the sequence form when frag actually contains
	// structure beyond plain text — otherwise keep the flat string-value
	// this always returned, which is exactly the pre-existing, correct
	// behavior for a purely atomic-valued function body. This construction
	// is unaffected by @as (a multi-instruction body's own item/merge
	// semantics come first); @as is enforced afterward, in callUserFunc, by
	// converting whatever value this produces.
	// XSLT 3.0 "temporary output state" (5.7.1): evaluating an xsl:function's
	// body forbids xsl:result-document (err:XTDE1480 — result-document-1142).
	// KeepDocItems: true so a document-node item constructed in the body
	// (e.g. xsl:copy-of over document(...)) stays one document-node() item
	// instead of flattening into its children (as-0136: my:func2()
	// as="document-node()*" tested with "instance of document-node()").
	//
	// wantsNodes gates NoAtomicMerge: a node-kind @as (text()/node()) still
	// needs the OLD eager space-merge-during-construction behavior, since its
	// own branch below reads frag.StringValue() directly (avt-1205,
	// static-032, copy-4802 — see that branch's comment). Every other case
	// keeps each instruction's atomic-valued contribution a separate item
	// during construction (mirroring sequenceFromBody, used for an
	// @as-typed xsl:variable/param/template body — sequence-0101: three
	// xsl:text items must atomize to 3 distinct items, not merge into one).
	// A caller re-embedding a multi-item result via ITS OWN xsl:sequence
	// re-applies the identical adjacent-atomics-get-a-space rule at ITS
	// construction site, so a purely-atomic recursive function whose result
	// is immediately re-embedded (seqtor-031/033) still serializes with
	// exactly the same spacing as when this function merged eagerly — the
	// merge is just deferred to wherever the sequence actually needs to
	// become text, instead of baked in prematurely and losing discreteness
	// along the way (function-0701: a declared as="xs:integer*" body mixing
	// a leading non-content xsl:variable with a multi-item xsl:sequence tail
	// must not silently collapse to one joined string).
	wantsNodes := fd.as != "" && xpath.SeqTypeWantsNodes(fd.as)
	frag := &xmltree.Node{Kind: xmltree.KindDocument, KeepDocItems: true, NoAtomicMerge: !wantsNodes}
	eng.tempOutputDepth++
	err = eng.execSequence(fd.body, r, frag)
	eng.tempOutputDepth--
	if err != nil {
		return nil, false, err
	}
	if fragHasMarkup(frag) {
		return fnUntypeAtomics(fragAsSequence(frag)), false, nil
	}
	if len(frag.Children) == 0 {
		// A body that constructed NOTHING (e.g. xsl:copy-of over an empty
		// select, with no further content) is the empty SEQUENCE — zero
		// items — not a one-item empty string (copy-3702: this exact case
		// is what a recursive helper call returns on its terminating branch,
		// and a caller re-embedding it via xsl:sequence must see zero items
		// contributed, not a spurious empty text node).
		return xpath.Sequence{}, false, nil
	}
	if fd.as == "" && len(frag.Children) == 1 &&
		frag.Children[0].Kind == xmltree.KindText && !frag.Children[0].Atomic {
		// A body that is exactly one literal text node or text value template
		// CONSTRUCTS a text node, and with no declared @as the function's
		// result is the sequence the body constructed — so it must stay a node
		// (cvt-030 asserts f:sum(1,2) instance of text()), not be flattened to
		// the Go string below. The !Atomic guard is load-bearing: an atomic
		// value produced by xsl:value-of / xsl:sequence is modelled as a
		// SYNTHETIC text node (Node.Atomic), and those keep the long-standing
		// flat-string treatment.
		return xpath.NodeSet{frag.Children[0]}, false, nil
	}
	if wantsNodes {
		// A node-kind declared type (text()/node()) needs an actual text
		// NODE, not a bare string — but still merging every text-producing
		// instruction's contribution the same way frag.StringValue() below
		// always has (avt-1205: as="text()*" over <xsl:text/>[<xsl:value-of/
		// >]<xsl:text/> must read back as "[N]", not just its first
		// constituent node's value; static-032: a leaf xsl:value-of inside
		// xsl:choose under as="text()"). This is still not "confident": a
		// leaf of copy-4802's recursive identity-transform helper can
		// legitimately need >1 item here (its xsl:copy of a document node
		// flattens instead of yielding one wrapped document-node item).
		return xpath.NodeSet{textNode(frag.StringValue(), false)}, false, nil
	}
	if len(frag.Children) > 1 {
		// Genuinely multiple discrete atomic items (NoAtomicMerge kept them
		// apart during construction) — return the actual sequence, each item
		// independently typed/atomizable, not a single joined string.
		return fragAsSequence(frag), false, nil
	}
	if ch := frag.Children[0]; xpath.IsTypeAnnotated(ch) || ch.RealItem != nil {
		if it, ok := xpath.TypedTextItem(ch); ok {
			// An atomic item the construction stamped with its original type:
			// hand back the VALUE, not the text node standing in for it. An
			// xsl:function body of xsl:try select="false()" must return the
			// xs:boolean false — as a node it would have an effective boolean
			// value of TRUE (higher-order-functions-067).
			return xpath.FromItems([]xpath.Item{it}), false, nil
		}
		// ONE item, but one whose real XDM identity the construction recorded
		// (a type annotation, or a carried map/array/function): flattening it
		// to a string would discard that. A body of xsl:try select="false()"
		// must return the xs:boolean false, not the xs:untypedAtomic "false"
		// whose effective boolean value is TRUE
		// (higher-order-functions-067). Untyped text content — the ordinary
		// case — still takes the string path below.
		return fragAsSequence(frag), false, nil
	}
	s := frag.StringValue()
	if fd.as != "" {
		// This flat string is RTF/text CONTENT (no real markup), whose XDM
		// type is xs:untypedAtomic — not the concrete xs:string a bare Go
		// string is otherwise treated as (itemAtomType) — so a declared
		// non-node return type still gets the untypedAtomic->target cast
		// (function conversion rules), not a spurious XSstring mismatch
		// (function-1001: a recursive xsl:choose/xsl:sequence body declared
		// as="xs:integer").
		return xpath.NewUntyped(s), false, nil
	}
	return s, false, nil
}

// fragHasMarkup reports whether frag's constructed content includes anything
// beyond plain text nodes: attributes, namespace nodes, or an
// element/comment/PI child — i.e. real markup whose node identity a caller
// might need, as opposed to a sequence of purely atomic-valued items.
func fragHasMarkup(frag *xmltree.Node) bool {
	if len(frag.Attrs) > 0 || len(frag.NS) > 0 {
		return true
	}
	for _, c := range frag.Children {
		if c.Kind != xmltree.KindText {
			return true
		}
		if _, ok := c.RealItem.(*xmltree.Node); ok {
			// A node xsl:sequence contributed by reference (see
			// emitSequenceValueOpts) is markup, whatever its kind: the
			// result must go through fragAsSequence, which hands back the
			// node itself (function-1201's nine sorted @author attributes).
			return true
		}
	}
	return false
}

// xsltExtraArity catalogs the [min,max] arity of the XSLT-defined functions
// in the standard function namespace that are not part of the core F&O
// catalog tracked by package xpath (current, document, key, …), for
// fn:function-available's arity-qualified form.
var xsltExtraArity = map[string][2]int{
	"current":                     {0, 0},
	"document":                    {1, 2},
	"key":                         {2, 3},
	"current-output-uri":          {0, 0},
	"copy-of":                     {0, 1},
	"snapshot":                    {0, 1},
	"system-property":             {1, 1},
	"function-available":          {1, 2},
	"element-available":           {1, 1},
	"type-available":              {1, 1},
	"current-group":               {0, 0},
	"current-grouping-key":        {0, 0},
	"current-merge-group":         {0, 1},
	"current-merge-key":           {0, 0},
	"regex-group":                 {1, 1},
	"stream-available":            {1, 1},
	"accumulator-before":          {1, 1},
	"accumulator-after":           {1, 1},
	"unparsed-entity-uri":         {1, 2},
	"unparsed-entity-public-id":   {1, 2},
	"available-system-properties": {0, 0},
}

// elementAvailableExtra names XSLT elements this processor implements that
// have no entry in xsltElemSpecs (whose purpose is STATIC validation, which
// these do not currently get) but which fn:element-available must still report
// as available — catalog-006 scans every stylesheet in the suite and asserts
// element-available() is true for every XSLT element they use.
var elementAvailableExtra = map[string]bool{
	"merge-source": true,
}

// xsltDynamicCallable lists the XSLT-defined functions in the standard function
// namespace that may be obtained as a FUNCTION ITEM (fn:function-lookup, a
// named-function reference) and called dynamically. It is deliberately a small
// allowlist rather than all of xsltExtraArity: most XSLT-defined functions read
// the XSLT focus (current(), current-group(), regex-group(), accumulator-*, …)
// and calling those other than directly is itself a dynamic error — XTDE1360
// and friends (error-1360b) — which the generic named-function fallback in
// xpath already reports correctly.
var xsltDynamicCallable = map[string]bool{
	"available-system-properties": true,
	// fn:system-property reads only the STATIC context (the stylesheet element
	// whose namespace bindings resolve its QName argument), not the XSLT
	// focus, so the rationale above for keeping this list short does not apply
	// to it — it is callable dynamically (system-property-024).
	"system-property": true,
}

// HostFuncArity implements xpath.HostFuncCatalog: it declares the XSLT-defined
// additions to the standard function namespace that fn:function-lookup and a
// named-function reference may build a function item for
// (available-system-properties-001/002).
func (e *evalEnv) HostFuncArity(uri, local string) (int, int, bool) {
	if uri != xpathFunctionsNS || !xsltDynamicCallable[local] {
		return 0, 0, false
	}
	if r, ok := xsltExtraArity[local]; ok {
		return r[0], r[1], true
	}
	return 0, 0, false
}

// xsltSystemPropertyNames lists every system property this processor reports a
// value for, in the order fn:available-system-properties returns them. It MUST
// stay in step with xsltSystemProperty: available-system-properties-014..028
// check that system-property() gives a value for each name listed here.
var xsltSystemPropertyNames = []string{
	"version", "vendor", "vendor-url", "product-name", "product-version",
	"is-schema-aware", "supports-serialization",
	"supports-backwards-compatibility", "supports-namespace-axis",
	"supports-streaming", "supports-dynamic-evaluation",
	"supports-higher-order-functions", "xpath-version", "xsd-version",
}

// availableSystemProperties builds the fn:available-system-properties() result:
// one xs:QName per property, all in the XSLT namespace.
func availableSystemProperties() xpath.Object {
	seq := make(xpath.Sequence, 0, len(xsltSystemPropertyNames))
	for _, n := range xsltSystemPropertyNames {
		seq = append(seq, xpath.NewQName(xmltree.Name{Space: NS, Local: n, Prefix: "xsl"}))
	}
	return seq
}

// funcAvailableName resolves the lexical/EQName $name argument of
// fn:function-available (and the static-context use-when analog) to its
// expanded (uri, local) form: a braced Q{uri}local carries its own
// namespace; a prefixed name resolves through el's in-scope namespaces (a
// prefix rebound to some OTHER uri therefore does not match the real
// function namespace, function-available-1006); an unprefixed name defaults
// to the standard function namespace, matching how an unprefixed function
// call itself resolves (function-available-0801/1015).
func funcAvailableName(el *xmltree.Node, name string) (uri, local string) {
	return resolveAvailableName(el, name, xpathFunctionsNS)
}

// elementAvailableName is funcAvailableName's analog for fn:element-available:
// an unprefixed name defaults to the XSLT namespace instead of the standard
// function namespace (function-0302: element-available('get-a-life') is
// false, but so would a bare 'value-of' be true — a10/$zls||'value-of').
func elementAvailableName(el *xmltree.Node, name string) (uri, local string) {
	return resolveAvailableName(el, name, NS)
}

// resolveAvailableName is the shared QName-resolution logic behind
// fn:function-available/fn:element-available's $name argument: a braced
// Q{uri}local carries its own namespace (including an explicitly EMPTY one,
// Q{}local — function-0303's a10); a prefixed name resolves through el's
// in-scope namespaces (a prefix rebound to some OTHER uri does not match);
// an unprefixed name defaults to defaultNS.
func resolveAvailableName(el *xmltree.Node, name, defaultNS string) (uri, local string) {
	if u, l, ok := bracedEQName(name); ok {
		return u, l
	}
	prefix := ""
	if i := strings.IndexByte(name, ':'); i >= 0 {
		prefix, local = name[:i], name[i+1:]
	} else {
		local = name
	}
	if prefix == "" {
		return defaultNS, local
	}
	if el != nil {
		if u, ok := el.LookupPrefix(prefix); ok {
			return u, local
		}
	}
	return "", local
}

// stdFuncAvailable answers fn:function-available for a resolved (uri, local)
// name against the XSLT-extras catalog and package xpath's F&O/map/array/xs
// catalog — no user-defined xsl:function lookup (callers with a compiled
// stylesheet check eng.sheet.functions first).
func stdFuncAvailable(uri, local string, arity int, hasArity bool) bool {
	if uri == xpathFunctionsNS {
		if r, ok := xsltExtraArity[local]; ok {
			return !hasArity || (arity >= r[0] && arity <= r[1])
		}
	}
	if uri == xsNS {
		// Abstract XSD types have no constructor function, even though
		// AtomTypeByName recognizes them for casting/instance-of purposes
		// (function-available-1016).
		switch local {
		case "anyType", "anySimpleType", "anyAtomicType", "NOTATION":
			return false
		}
	}
	if !hasArity {
		_, _, known := xpath.StdFuncArity(uri, local)
		return known
	}
	return xpath.StdArityOK(uri, local, arity)
}

// funcAvailable implements fn:function-available($name) and
// fn:function-available($name, $arity) (XSLT 3.0 §18.1.1): whether a
// function of that name — user-declared xsl:function (each arity an
// independent overload, function-1002), or standard — can be called, at any
// arity or (if $arity is given) at exactly that arity.
func (eng *engine) funcAvailable(env *evalEnv, name string, arity int, hasArity bool) bool {
	var el *xmltree.Node
	if env != nil {
		el = env.el
	}
	uri, local := funcAvailableName(el, name)
	if hasArity {
		if _, ok := eng.sheet.functions[funcKey(uri, local, arity)]; ok {
			return true
		}
	} else {
		want := clark(uri, local)
		for _, fd := range eng.sheet.functions {
			if clarkName(fd.name) == want {
				return true
			}
		}
	}
	return stdFuncAvailable(uri, local, arity, hasArity)
}

// formatNumber implements a useful subset of format-number(): #, 0, decimal
// separator, grouping separator (','), and a literal prefix/suffix. Subpatterns
// (positive;negative) are not yet supported.
func formatNumber(value float64, pattern string) string {
	// Split optional prefix/suffix around the number pattern.
	start, end := 0, len(pattern)
	for start < len(pattern) && !isPatternChar(pattern[start]) {
		start++
	}
	for end > start && !isPatternChar(pattern[end-1]) {
		end--
	}
	prefix := pattern[:start]
	suffix := pattern[end:]
	core := pattern[start:end]

	intPart, fracPart := core, ""
	if i := strings.IndexByte(core, '.'); i >= 0 {
		intPart, fracPart = core[:i], core[i+1:]
	}

	minFrac := strings.Count(fracPart, "0")
	maxFrac := len(strings.ReplaceAll(fracPart, ",", ""))
	grouped := strings.Contains(intPart, ",")

	neg := value < 0
	if neg {
		value = -value
	}
	// Round to maxFrac digits.
	s := strconv.FormatFloat(value, 'f', maxFrauxClamp(maxFrac), 64)
	ip, fp := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		ip, fp = s[:i], s[i+1:]
	}
	// Trim/pad fraction to [minFrac, maxFrac].
	for len(fp) > minFrac && strings.HasSuffix(fp, "0") {
		fp = fp[:len(fp)-1]
	}
	for len(fp) < minFrac {
		fp += "0"
	}
	if grouped {
		ip = group(ip)
	}
	var b strings.Builder
	b.WriteString(prefix)
	if neg {
		b.WriteByte('-')
	}
	b.WriteString(ip)
	if fp != "" {
		b.WriteByte('.')
		b.WriteString(fp)
	}
	b.WriteString(suffix)
	return b.String()
}

func maxFrauxClamp(n int) int {
	if n < 0 {
		return 0
	}
	if n > 15 {
		return 15
	}
	return n
}

func isPatternChar(c byte) bool {
	return c == '#' || c == '0' || c == '.' || c == ','
}

func group(intPart string) string {
	n := len(intPart)
	if n <= 3 {
		return intPart
	}
	var b strings.Builder
	rem := n % 3
	if rem > 0 {
		b.WriteString(intPart[:rem])
		if n > rem {
			b.WriteByte(',')
		}
	}
	for i := rem; i < n; i += 3 {
		b.WriteString(intPart[i : i+3])
		if i+3 < n {
			b.WriteByte(',')
		}
	}
	return b.String()
}

var _ = fmt.Sprintf

// nodeInSubtree reports whether n is top or a descendant of top (attributes
// count via their owner element).
func nodeInSubtree(n, top *xmltree.Node) bool {
	for cur := n; cur != nil; cur = cur.Parent {
		if cur == top {
			return true
		}
	}
	return false
}

// appendDocResult adds a document() result to out: the document node itself,
// or — when the URI carried a fragment identifier — the element that fragment
// identifies. An unresolvable fragment contributes nothing (it is not an
// error: id-001's document('#non-existent', /)).
func appendDocResult(out xpath.NodeSet, d *xmltree.Node, frag string) xpath.NodeSet {
	if frag == "" {
		return append(out, d)
	}
	if el := elementWithID(d, frag); el != nil {
		return append(out, el)
	}
	return out
}

// elementWithID finds the element in a tree whose ID-typed attribute (a DTD
// ATTLIST ID, or xml:id, or — for schema-less documents — one literally named
// "id") has the given value.
func elementWithID(n *xmltree.Node, id string) *xmltree.Node {
	if n.Kind == xmltree.KindElement {
		for _, a := range n.Attrs {
			if a.Value != id {
				continue
			}
			if a.IDKind == xmltree.IDKindID ||
				(a.Name.Space == xmlURI && a.Name.Local == "id") ||
				(a.Name.Space == "" && a.Name.Local == "id") {
				return n
			}
		}
	}
	for _, ch := range n.Children {
		if m := elementWithID(ch, id); m != nil {
			return m
		}
	}
	return nil
}

// typeAvailable implements fn:type-available: true exactly for the names in the
// in-scope schema components.
//
// XSLT 3.0 §3.15 makes EVERY processor — basic or schema-aware — carry all of
// XSD part 2's built-in types, so the schema-awareness claim does not narrow
// this set; that is a change from XSLT 2.0 §3.13, where a basic processor had
// only the primitives (minus xs:NOTATION) plus xs:integer, and it is why the
// case pinning the 2.0 rule (type-available-0148: xs:int/xs:NCName/xs:ID/...
// false) is declared spec="XSLT20" exactly rather than "XSLT20+". What the
// claim adds is the other half of §3.15: the user-defined types an
// xsl:import-schema brings in, consulted last — after every built-in has been
// tried — through the components in scope for el's own module.
//
// xs:dateTimeStamp is XSD 1.1-only and this processor reports XSD 1.1
// (type-available-0151a).
func typeAvailable(el *xmltree.Node, uri, local string) bool {
	if uri != "http://www.w3.org/2001/XMLSchema" {
		if is := schemaForElement(el); is != nil {
			_, ok := is.sch.TypeByName(uri, local)
			return ok
		}
		return false
	}
	switch local {
	case "anyType", "untyped", "anySimpleType", "anyAtomicType":
		return true
	case "dateTimeStamp", "error":
		// Both XSD 1.1 additions are implemented (XSdateTimeStamp; the
		// xs:error constructor), and XSD 1.1 is what this processor
		// declares (type-available-0151a).
		return true
	}
	if _, ok := xpath.AtomTypeByName(local); ok {
		return true
	}
	// The built-in LIST types are types too, and this processor implements
	// them (xpath's xsListItemType, xsd's builtinList) — but AtomTypeByName
	// knows only atomic types.
	//
	// Gated on the schema-awareness claim because the suite pins both answers:
	// type-available-0147 (XSLT20+, feature schema_aware) wants true, while
	// type-available-0148 — spec="XSLT20" exactly, with schema_aware
	// explicitly NOT satisfied, one of the 31 canary cases — wants false,
	// XSLT 2.0 §3.13 giving a basic processor only a short list of types
	// where §3.15 gives a schema-aware one all of XSD Part 2.
	if schemaAwareRun() {
		switch local {
		case "NMTOKENS", "IDREFS", "ENTITIES":
			return true
		}
	}
	return false
}

// xsltSystemProperty returns the value of a standard XSLT system property
// (XSLT 3.0 §18.1.1), reflecting this engine's actual capabilities:
// backwards-compatibility, schema-awareness and streaming all report what the
// HOST says it is presenting (SetBackwardsCompatible / SetSchemaAware /
// SetStreamingClaim); everything else in the XSLT 3.0 / XPath 3.1 feature set
// (HOF, dynamic evaluation via xsl:evaluate, serialization, the namespace::
// axis) is implemented. An unknown name is the zero-length string.
func xsltSystemProperty(local string) string {
	switch local {
	case "version":
		return "3.0"
	case "vendor", "product-name":
		return "go-xslt"
	case "vendor-url":
		return "https://github.com/"
	case "product-version":
		return "1.0"
	case "is-schema-aware":
		// What the HOST says it is presenting — see SetSchemaAware
		// (schema_property.go). Default "no", which is also what the engine
		// can back up today.
		if schemaAwareRun() {
			return "yes"
		}
		return "no"
	case "supports-serialization", "supports-namespace-axis",
		"supports-dynamic-evaluation", "supports-higher-order-functions":
		return "yes"
	case "supports-backwards-compatibility":
		// What the HOST says it is presenting — see SetBackwardsCompatible
		// (backcompat_property.go). Default "no" (system-property-019a);
		// "yes" once a run claims it (system-property-019).
		if backwardsCompatRun() {
			return "yes"
		}
		return "no"
	case "supports-streaming":
		// "yes" unless the HOST says it is presenting a non-streaming
		// processor — see SetStreamingClaim (stream_property.go) for why this
		// is not tied to strmEnforce.
		if strmClaim.Load() {
			return "yes"
		}
		return "no"
	case "xpath-version":
		return "3.1"
	case "xsd-version":
		return "1.0"
	}
	return ""
}

// xdmRoot is rootOfNode restricted to the XDM tree a node actually belongs to.
// An @as-typed variable/function body collects its items under a synthetic
// document node (NoAtomicMerge) that is an implementation artifact, not part
// of any XDM tree: a parentless element constructed there has ITSELF as the
// root of its tree, so unparsed-entity-uri() over it is XTDE1370/1380
// (error-1370b/1380b) rather than a lookup in a non-existent document.
func xdmRoot(n *xmltree.Node) *xmltree.Node {
	for n != nil && n.Parent != nil {
		if n.Parent.Kind == xmltree.KindDocument && n.Parent.NoAtomicMerge {
			return n
		}
		n = n.Parent
	}
	return n
}

// snapshotNode implements fn:snapshot for a single node: a deep copy of the
// node (attributes, namespaces, descendants) that is ALSO re-attached under
// shallow copies of its ancestors — each ancestor copied with its own
// attributes and namespace declarations but none of its other children. The
// result therefore keeps a usable ancestor/root axis, which is exactly what
// distinguishes fn:snapshot from fn:copy-of (snapshot-0101b: snapshot(/*) has
// a parent; snapshot-0108: the snapshot of a grandchild has 2 ancestors).
//
// The walk stops at the node's effective root: a scratch NoAtomicMerge
// document collector holding an @as-typed sequence is not part of the tree in
// the XSLT data model, so it never appears in the spine.
// materializeInScopeNS writes onto an element COPY the namespace bindings it
// used to inherit from ancestors it no longer has. In XDM every element carries
// a namespace node per IN-SCOPE binding, not just per declaration, so a copy
// that keeps only the source's own xmlns attributes has silently lost part of
// the node it claims to be a copy of (sf-copy-of-021, sf-snapshot-0321: the
// copied gml:description must still report all 8 prefixes of the CityGML root).
// The node's own declarations win, and a prefix rebound to "" by the source is
// not resurrected.
func materializeInScopeNS(cp, src *xmltree.Node) {
	if cp == nil || src == nil || cp.Kind != xmltree.KindElement || src.Kind != xmltree.KindElement {
		return
	}
	// Only the copy's OWN declarations count as present. fn:snapshot rebuilds
	// the ancestor spine, so most bindings are also reachable through it — but
	// the copy is a value that can be emitted on its own (xsl:sequence copies
	// the node, not its ancestors), and then only what it declares itself
	// survives (sf-snapshot-0321).
	have := map[string]string{}
	for _, ns := range cp.NS {
		have[ns.Name.Local] = ns.Value
	}
	want := src.InScopeNamespaces()
	prefixes := make([]string, 0, len(want))
	for prefix := range want {
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes) // a map's order would make the output non-deterministic
	for _, prefix := range prefixes {
		uri := want[prefix]
		if uri == "" || have[prefix] == uri {
			continue
		}
		if _, bound := have[prefix]; bound {
			continue // the copy rebinds this prefix itself; that binding wins
		}
		cp.NS = append(cp.NS, &xmltree.Node{Kind: xmltree.KindNamespace, Name: xmltree.Name{Local: prefix}, Value: uri, Parent: cp})
	}
}

func snapshotNode(n *xmltree.Node) *xmltree.Node {
	var anc []*xmltree.Node
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Kind == xmltree.KindDocument && p.NoAtomicMerge {
			break
		}
		anc = append(anc, p)
		if p.Kind != xmltree.KindElement && p.Kind != xmltree.KindDocument {
			break
		}
	}
	// anc is innermost-first; build outermost-first. snapRoot keeps the
	// OUTERMOST copy so the finished snapshot tree can be numbered in document
	// order: every node in it is freshly built (cloneNode/NewElement/SetAttr),
	// so without that they all share index 0 and generate-id() — which is
	// (tree identity, document order) — reports the same id for every node in
	// the snapshot (snapshot-0112 checks exactly that they are all distinct).
	var parent, snapRoot *xmltree.Node
	for i := len(anc) - 1; i >= 0; i-- {
		a := anc[i]
		var c *xmltree.Node
		switch a.Kind {
		case xmltree.KindDocument:
			c = &xmltree.Node{Kind: xmltree.KindDocument, Base: xpath.NodeBaseURI(a, ""), Ephemeral: true}
			mergeUnparsed(c, a)
		case xmltree.KindElement:
			c = xmltree.NewElement(a.Name)
			xmltree.CopyTypeInfo(c, a)
			for _, ns := range a.NS {
				c.NS = append(c.NS, &xmltree.Node{Kind: xmltree.KindNamespace, Name: ns.Name, Value: ns.Value, Parent: c})
			}
			for _, at := range a.Attrs {
				xmltree.CopyTypeInfo(c.SetAttr(at.Name, at.Value), at)
			}
			c.Base = a.Base
		default:
			continue
		}
		if parent != nil {
			parent.Append(c)
		}
		parent = c
		if snapRoot == nil {
			snapRoot = c
		}
	}
	copyN := cloneNode(n)
	if parent == nil {
		xmltree.AssignOrder(copyN)
		return copyN
	}
	defer xmltree.AssignOrder(snapRoot)
	switch copyN.Kind {
	case xmltree.KindAttribute:
		// copyN is cloneNode's copy, so it already carries the source's type
		// information; the node that ends up IN the snapshot is the one SetAttr
		// owns, which has to carry it too.
		xmltree.CopyTypeInfo(parent.SetAttr(copyN.Name, copyN.Value), copyN)
		for _, at := range parent.Attrs {
			if at.Name == copyN.Name {
				return at
			}
		}
		return copyN
	case xmltree.KindNamespace:
		// The copied parent already carries its own namespace declarations;
		// a prefix among them is that node, not a second declaration
		// (snapshot-0102a deep-equals the snapshot's root against the spec's
		// own graft-based implementation, which declares each prefix once).
		for _, ns := range parent.NS {
			if ns.Name.Local == copyN.Name.Local {
				return ns
			}
		}
		nsn := &xmltree.Node{Kind: xmltree.KindNamespace, Name: copyN.Name, Value: copyN.Value, Parent: parent}
		parent.NS = append(parent.NS, nsn)
		return nsn
	default:
		parent.Append(copyN)
		return copyN
	}
}

// memoResult is one cached xsl:function result (see FuncDef.memoize).
type memoResult struct {
	val       xpath.Object
	confident bool
}

// funcMemoKey builds the cache key for one call of a memoizable function: the
// declaration's identity plus every argument's typed value. It reports ok=false
// — meaning "do not memoize this call" — when any argument carries a node, map,
// array or function item: those have IDENTITY rather than just a value, so two
// calls with equal-looking arguments are not necessarily the same call
// (function-1032 distinguishes input nodes by identity).
func funcMemoKey(fd *FuncDef, args []xpath.Object) (string, bool) {
	var b strings.Builder
	fmt.Fprintf(&b, "%p", fd)
	for _, a := range args {
		b.WriteByte(0x1e)
		for _, it := range xpath.Items(a) {
			switch it.(type) {
			case *xmltree.Node, *xpath.Map, *xpath.Array, *xpath.Function:
				return "", false
			}
			tag, ok := xpath.ItemAtomTypeTag(it)
			if ok && xpath.AtomType(tag) == xpath.XSqname {
				// A QName's key string is its lexical form, which drops the
				// namespace URI — two DIFFERENT QNames sharing a local name
				// would collide (function-1034's x:qn-qn over x:alpha in two
				// namespaces). Do not memoize such a call.
				return "", false
			}
			if ok {
				fmt.Fprintf(&b, "%d:", tag)
			}
			b.WriteString(xpath.KeyString(it))
			b.WriteByte(0x1f)
		}
	}
	return b.String(), true
}

// fnUntypeAtomics restores the real atomic value of every item a construction
// recorded as a type-annotated text node. A body that ALSO built markup returns
// a node set, so without this an atomic item keeps travelling as a node — and a
// node's effective boolean value is unconditionally true, which hides the
// FORG0006 a mixed atomic-then-node sequence owes (sf-boolean-120/sf-not-120).
// The single-item and multi-item no-markup paths below already do this; only
// the markup path did not.
func fnUntypeAtomics(o xpath.Object) xpath.Object {
	items := xpath.Items(o)
	out := make([]xpath.Item, 0, len(items))
	changed := false
	for _, it := range items {
		if n, ok := it.(*xmltree.Node); ok {
			if a, ok := xpath.TypedTextItem(n); ok {
				out = append(out, a)
				changed = true
				continue
			}
		}
		out = append(out, it)
	}
	if !changed {
		return o
	}
	return xpath.FromItems(out)
}
