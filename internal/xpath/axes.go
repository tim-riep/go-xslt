package xpath

import (
	"fmt"
	"time"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

func isReverseAxis(axis string) bool {
	switch axis {
	case "ancestor", "ancestor-or-self", "preceding", "preceding-sibling", "parent":
		return true
	}
	return false
}

// axisNodes returns the nodes on the given axis from node n, in axis order
// (document order for forward axes, reverse document order for reverse axes).
func axisNodes(axis string, n *xmltree.Node) NodeSet {
	switch axis {
	case "child":
		return append(NodeSet{}, n.Children...)
	case "self":
		return NodeSet{n}
	case "parent":
		if p := baseURIParent(n); p != nil {
			return NodeSet{p}
		}
		return nil
	case "attribute":
		return append(NodeSet{}, n.Attrs...)
	case "namespace":
		return namespaceNodes(n, nil)
	case "descendant":
		var out NodeSet
		collectDescendants(n, &out)
		return out
	case "descendant-or-self":
		out := NodeSet{n}
		collectDescendants(n, &out)
		return out
	case "ancestor":
		var out NodeSet
		for p := baseURIParent(n); p != nil; p = baseURIParent(p) {
			out = append(out, p)
		}
		return out
	case "ancestor-or-self":
		out := NodeSet{n}
		for p := baseURIParent(n); p != nil; p = baseURIParent(p) {
			out = append(out, p)
		}
		return out
	case "following-sibling":
		return siblings(n, true)
	case "preceding-sibling":
		return siblings(n, false)
	case "following":
		return followingNodes(n)
	case "preceding":
		return precedingNodes(n)
	}
	return nil
}

const xmlNamespaceURI = "http://www.w3.org/XML/1998/namespace"

// namespaceNodes returns the namespace axis of n per XDM: one namespace node
// for every IN-SCOPE binding (the innermost declaration of a prefix wins, an
// xmlns=""/xmlns:p="" undeclaration removes it) plus the implicit xml binding
// — not merely the declarations on n (Axes118: 6 on the auction root,
// fn-innermost-017: 69 over the whole tree). A binding declared on n itself is
// n's own NS node; an inherited one gets a node whose parent is n, so the
// namespace nodes of two elements are distinct (Axes122). The optional cache
// (per evaluation, see ctxNSScope) makes repeated evaluation on the same
// element return the same nodes, which is what identity and union dedup need.
func namespaceNodes(n *xmltree.Node, cache map[*xmltree.Node][]*xmltree.Node) NodeSet {
	if n.Kind != xmltree.KindElement {
		return nil
	}
	if cache != nil {
		if v, ok := cache[n]; ok {
			return append(NodeSet{}, v...)
		}
	}
	seen := map[string]bool{}
	var out NodeSet
	for cur := n; cur != nil && cur.Kind == xmltree.KindElement; cur = cur.Parent {
		for _, ns := range cur.NS {
			if seen[ns.Name.Local] {
				continue
			}
			seen[ns.Name.Local] = true
			if ns.Value == "" {
				continue // undeclaration: no namespace node
			}
			if cur == n {
				out = append(out, ns)
			} else {
				out = append(out, n.NewInheritedNamespace(ns.Name.Local, ns.Value))
			}
		}
		// inherit-namespaces="no" (xmltree.Node.NSBarrier): this element's
		// in-scope namespaces are its own declarations only.
		if cur.NSBarrier {
			break
		}
	}
	if !seen["xml"] {
		out = append(out, n.NewInheritedNamespace("xml", xmlNamespaceURI))
	}
	if cache != nil {
		cache[n] = out
	}
	return append(NodeSet{}, out...)
}

// ctxNSScope returns the evaluation's namespace-node cache (creating the
// per-evaluation box on demand, exactly as ctxNow does).
func ctxNSScope(c *Context) map[*xmltree.Node][]*xmltree.Node {
	if c == nil {
		return nil
	}
	if c.nowCache == nil {
		c.nowCache = &nowBox{t: time.Now().UTC()}
	}
	if c.nowCache.nsScope == nil {
		c.nowCache.nsScope = map[*xmltree.Node][]*xmltree.Node{}
	}
	return c.nowCache.nsScope
}

func collectDescendants(n *xmltree.Node, out *NodeSet) {
	for _, c := range n.Children {
		*out = append(*out, c)
		collectDescendants(c, out)
	}
}

func siblings(n *xmltree.Node, following bool) NodeSet {
	p := baseURIParent(n)
	if p == nil {
		return nil
	}
	sibs := p.Children
	idx := -1
	for i, c := range sibs {
		if c == n {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	var out NodeSet
	if following {
		for _, c := range sibs[idx+1:] {
			out = append(out, c)
		}
	} else {
		// reverse document order
		for i := idx - 1; i >= 0; i-- {
			out = append(out, sibs[i])
		}
	}
	return out
}

// docWalk returns the tree containing n in document order (the root plus every
// descendant; attribute and namespace nodes, which neither axis below selects,
// are omitted) together with n's position in it. An attribute or namespace
// node is ANCHORED at its parent element: everything after that element in the
// walk — its own children included — genuinely follows the attribute in
// document order, and everything before it precedes it.
//
// The position is found by IDENTITY rather than through Node.Order(), which is
// assigned only when a tree is parsed: a tree constructed at run time (an RTF,
// an xsl:copy-of result, a stylesheet function's result) has Order() == 0
// everywhere, which made both axes below select nothing at all (copy-4305).
//
// The root is found via effectiveRoot, not (*xmltree.Node).Root(): a node
// built as one of several top-level items of an @as-typed sequence body
// (xsl:variable/param/function/xsl:sequence, e.g. "as=\"node()*\"") is one of
// several INDEPENDENT roots of a "forest" sharing a throwaway NoAtomicMerge
// collector as a raw .Parent link — following/preceding must never cross from
// one such item into another (sequence-0113/0125: a Saxon-TinyTree-forest
// style test, explicitly checking axes across several parentless top-level
// nodes sharing no ancestor).
func docWalk(n *xmltree.Node) (NodeSet, int) {
	anchor := n
	if n.Kind == xmltree.KindAttribute || n.Kind == xmltree.KindNamespace {
		if n.Parent == nil {
			return nil, -1
		}
		anchor = n.Parent
	}
	all := NodeSet{effectiveRoot(anchor)}
	collectDescendants(all[0], &all)
	for i, c := range all {
		if c == anchor {
			return all, i
		}
	}
	return all, -1
}

func followingNodes(n *xmltree.Node) NodeSet {
	all, at := docWalk(n)
	if at < 0 {
		return nil
	}
	var out NodeSet
	for _, c := range all[at+1:] {
		// following:: excludes ancestors AND descendants of the context node
		// (fn-innermost-049: descendants were wrongly included).
		if !isAncestorOf(c, n) && !isAncestorOf(n, c) {
			out = append(out, c)
		}
	}
	return out
}

func precedingNodes(n *xmltree.Node) NodeSet {
	all, at := docWalk(n)
	if at < 0 {
		return nil
	}
	var out NodeSet
	for i := at - 1; i >= 0; i-- {
		if c := all[i]; !isAncestorOf(c, n) {
			out = append(out, c)
		}
	}
	return out
}

func isAncestorOf(a, n *xmltree.Node) bool {
	for p := baseURIParent(n); p != nil; p = baseURIParent(p) {
		if p == a {
			return true
		}
	}
	return false
}

// matchTest reports whether node c satisfies the node test on the given axis.
func matchTest(t NodeTest, axis string, c *xmltree.Node, ctx *Context) (bool, error) {
	switch t.Kind {
	case testNode:
		return true, nil
	case testText:
		return c.Kind == xmltree.KindText, nil
	case testComment:
		return c.Kind == xmltree.KindComment, nil
	case testPI:
		if c.Kind != xmltree.KindPI {
			return false, nil
		}
		return t.PITarget == "" || c.Name.Local == t.PITarget, nil
	case testName:
		ok, err := matchNameTest(t, axis, c, ctx)
		if err != nil || !t.TypedElem {
			return ok, err
		}
		// xsl:mode/@typed="strict"/"lax": this name test is interpreted as
		// schema-element(N) — see Pattern.TypedModeRewrite.
		if !ok {
			if c.Kind != xmltree.KindElement ||
				!substitutesFor(c.Name, xmltree.Name{Space: t.URI, Local: t.Local}, ctx) {
				return false, nil
			}
		}
		if t.SchemaType == nil {
			return true, nil
		}
		return schemaTypeMatches(*t.SchemaType, c, ctx), nil
	case testElement, testSchemaElement:
		if c.Kind != xmltree.KindElement {
			return false, nil
		}
		ok, err := kindTestNameOK(t, c, ctx)
		if err != nil {
			return false, err
		}
		if !ok {
			// schema-element(N) also matches every member of the substitution
			// group headed by N (XPath 3.1 §2.5.5.5) — element(N) does not.
			if t.Kind != testSchemaElement ||
				!substitutesFor(c.Name, xmltree.Name{Space: t.URI, Local: t.Local}, ctx) {
				return false, nil
			}
		}
		// XPath 3.1 §2.5.5.3 clause 3: element(N, T) does NOT match a nilled
		// element; only element(N, T?) does. The clause is tied to the
		// TypeName — element(N) on its own says nothing about nilling — and
		// belongs to ElementTest alone: a SchemaElementTest is governed
		// instead by its DECLARATION's nillable property (§2.5.5.4), and an
		// xsi:nil on a non-nillable declaration never validates in the first
		// place, so a nilled node reaching here always had a nillable one.
		if t.Kind == testElement && !t.TypedElem && t.TypeName != "" && !t.Nillable && c.Nilled {
			return false, nil
		}
		if t.Kind == testSchemaElement {
			return schemaElementTypeOK(t, c, ctx), nil
		}
		return kindTestTypeOK(t, c, ctx), nil
	case testAttribute, testSchemaAttr:
		if c.Kind != xmltree.KindAttribute {
			return false, nil
		}
		ok, err := kindTestNameOK(t, c, ctx)
		if err != nil || !ok {
			return ok, err
		}
		return kindTestTypeOK(t, c, ctx), nil
	case testDocument:
		if c.Kind != xmltree.KindDocument {
			return false, nil
		}
		if t.Inner != nil {
			// document-node(element(x)) matches "a document node that
			// contains exactly ONE element node, optionally accompanied by
			// one or more comment and processing instruction nodes, if E is
			// an ElementTest ... that matches this element node" (XPath 3.1
			// §2.5.5.1). Two element children — or a text child — is not
			// such a document node however well the first element matches
			// (import-schema-055 builds $u from two xsl:copy-of of the same
			// element and requires document-node(schema-element(address)) to
			// be FALSE for it).
			var elem *xmltree.Node
			for _, ch := range c.Children {
				switch ch.Kind {
				case xmltree.KindElement:
					if elem != nil {
						return false, nil
					}
					elem = ch
				case xmltree.KindText:
					if ch.Value != "" {
						return false, nil
					}
				}
			}
			if elem == nil {
				return false, nil
			}
			return matchTest(*t.Inner, "child", elem, ctx)
		}
		return true, nil
	case testNamespace:
		return c.Kind == xmltree.KindNamespace, nil
	}
	return false, nil
}

// kindTestNameOK checks the optional name of an element(...)/attribute(...)
// kind test against the node.
func kindTestNameOK(t NodeTest, c *xmltree.Node, ctx *Context) (bool, error) {
	if t.AnyName || t.Local == "" {
		return true, nil
	}
	uri := t.URI
	if t.Prefix != "" {
		u, err := resolvePrefix(ctx, t.Prefix)
		if err != nil {
			return false, err
		}
		uri = u
	} else if !t.Braced && c.Kind == xmltree.KindElement {
		// Unprefixed element-test names use the default element namespace
		// (an explicit Q{}local does not).
		uri = ctx.DefaultElemNS
	}
	return c.Name.Local == t.Local && c.Name.Space == uri, nil
}

// schemaElementTypeOK is XPath 3.1 §2.5.5.4's third matching condition for a
// SchemaElementTest: derives-from(AT, ET), where AT is the CANDIDATE's type
// annotation and ET the type declared by the element declaration whose name
// the CANDIDATE bears — not the one the test names. See
// SchemaDeclTypeResolver for why that distinction is load-bearing and why the
// lookup cannot happen at parse time.
//
// Falls back to the parse-time type baked into the test (the ElementName's own
// declared type) whenever the host installs no declaration resolver, which is
// exactly the pre-existing behaviour for any caller that does not implement
// the optional seam.
func schemaElementTypeOK(t NodeTest, c *xmltree.Node, ctx *Context) bool {
	et, ok := schemaElemDeclType(c.Name, ctx)
	if !ok {
		return kindTestTypeOK(t, c, ctx)
	}
	return schemaTypeMatches(et, c, ctx)
}

// kindTestTypeOK checks the optional type annotation of an
// element(name, type)/attribute(name, type) kind test against the node. A name
// the host RESOLVED against an imported schema takes the first branch below;
// everything else takes the BUILT-IN-ONLY reading, which is all a node without
// a schema type can ever satisfy — no validation has run, so a constructed or
// parsed node is untyped (elements: xs:untyped; attributes: xs:untypedAtomic)
// and TypeAnno is set only on the rare specially-annotated tree (XSD assertion
// detached clones, an atomic-sequence-item-as-node for a for-each context).
//
// The TypeName is matched as a full expanded QName: a PREFIXED or braced name
// that resolves anywhere but the XML Schema namespace names no type this
// processor knows, and must not match merely because its local part happens to
// read "string" or "untyped". Only the UNPREFIXED form still matches by local
// part — the same deliberate leniency atomTypeForEQName documents, and harmless
// while no user-defined type exists to collide with a built-in's name.
func kindTestTypeOK(t NodeTest, c *xmltree.Node, ctx *Context) bool {
	// A name RESOLVED against an imported schema (element(N,T) naming a
	// user-defined type; the declared type of a schema-element()/
	// schema-attribute() declaration) is settled by identity + derivation on
	// the node's own annotation — never by the built-in reading below, which
	// knows nothing of user types. See schema_names.go.
	if t.SchemaType != nil {
		if schemaTypeMatches(*t.SchemaType, c, ctx) {
			return true
		}
		// A BUILT-IN name resolves through the schema too (every schema
		// contains the built-ins), and then takes the identity/derivation
		// route — which knows nothing of xs:untyped, the annotation an
		// unvalidated node actually carries. Fall through to the built-in
		// reading below for such a name, which is the only one that can
		// answer for it (import-schema-052/053/054/076: element(*,
		// xs:untyped) / element(*, xs:anyType) over an unvalidated RTF).
		//
		// Deliberately MONOTONE — tried second, never instead — so it can
		// only ever turn a false into a true. Skipping the schema route at
		// PARSE time instead was measured at +4/−10 and is not taken.
		if t.SchemaType.Namespace != nsXS || t.TypeName == "" {
			return false
		}
		// …but only for a node the schema graph cannot speak about. A node
		// validated against a USER-DEFINED type already has a complete,
		// authoritative derivation answer above; the built-in reading below
		// is a lossy approximation that consults TypeAnno, which for a LIST
		// or UNION type deliberately holds the ITEM/ACCEPTING-MEMBER
		// primitive rather than the node's annotation (XDM 3.1 §3.3.1.1:
		// a union-validated node's [type-name] is "the declared union type",
		// while §3.3.1.2's typed value is "an instance of ... the [member
		// type definition] if T is a union type" — two different facts, one
		// field each). Letting the fallback read it as the annotation makes
		// attribute(my:string-int-union, xs:string) match an attribute whose
		// annotation is the union my:string-int-type purely because the
		// member that accepted "hello" happened to be xs:string (match-211).
		if c != nil && c.SchemaType != nil && c.SchemaType.Namespace != nsXS {
			return false
		}
	}
	if t.TypeName == "" {
		return true
	}
	local, ok := builtinTypeLocalName(t.TypeName, ctx)
	if !ok {
		return false
	}
	switch local {
	case "anyType", "anySimpleType":
		// The root of the complex/simple type hierarchy: every element (for
		// anyType) or every value (anySimpleType, which subsumes anyType for
		// this purpose) matches unconditionally.
		return true
	case "untyped":
		// Only a valid annotation for an ELEMENT (type-0203) — and only for
		// one nothing has validated. A validated element with COMPLEX content
		// legitimately has TypeAnno 0 (complex content has no typed value to
		// atomize) while carrying its real type in SchemaType, so the
		// annotation test has to consult both or it reports every validated
		// element as untyped (import-schema-076).
		return c.Kind == xmltree.KindElement && !IsTypeAnnotated(c) && c.SchemaType == nil
	case "untypedAtomic":
		// The default annotation for an ATTRIBUTE's typed value. For an
		// ELEMENT it is normally impossible (type-0203: element(*,
		// xs:untypedAtomic) must be false for an ordinary untyped element) —
		// with one exception, [xsl:]type="xs:untypedAtomic", which §24.4.1.2
		// gives an element exactly that annotation (validation-0108).
		if c.Kind != xmltree.KindAttribute {
			return c.SchemaType != nil && c.SchemaType.Namespace == nsXS &&
				c.SchemaType.Local == "untypedAtomic"
		}
		// A LIST- or UNION-typed attribute has no TypeAnno of its own (its
		// typed value is a sequence, which one AtomType cannot hold), so
		// "unannotated" alone does not mean untypedAtomic — a schema type
		// must be absent too (strip-type-annotations-014). The one schema
		// type that DOES mean untypedAtomic is xs:untypedAtomic itself, which
		// [xsl:]type="xs:untypedAtomic" stamps on the node it validates
		// (§24.4.1.2) exactly as it does for an element — validation-0108
		// asserts the attribute half of that alongside the element half.
		if c.SchemaType != nil {
			return c.SchemaType.Namespace == nsXS && c.SchemaType.Local == "untypedAtomic"
		}
		at, annotated := nodeAnnotationType(c)
		return !annotated || at == XSuntypedAtomic
	case "anyAtomicType":
		// Every attribute's typed value derives from xs:anyAtomicType — never
		// a valid ELEMENT type annotation.
		return c.Kind == xmltree.KindAttribute
	default:
		// A specific named (schema-validated) type: this engine has no
		// schema validation of general document content, so a node is
		// annotated with a specific type only via the rare mechanisms noted
		// above. Conservative false rather than a false positive when it
		// isn't one of those.
		anno, annotated := nodeAnnotationType(c)
		if !annotated {
			return false
		}
		// The three built-in LIST types have no AtomType of their own — a
		// list's typed value is a sequence — so they are recognised by the
		// pair of facts a list-annotated node actually carries: ListTyped,
		// plus the ITEM type's primitive in TypeAnno (see xmltree's own
		// ListTyped doc comment). Without this, attribute(*, xs:NMTOKENS)
		// could never match an attribute validated against xs:NMTOKENS
		// (import-schema-020), since AtomTypeByName knows only atomic names.
		if item, isList := xsListItemType(local); isList {
			return c.ListTyped && anno == item
		}
		if c.ListTyped {
			// An ATOMIC type name never matches a list-annotated node:
			// TypeAnno holds the item type there, not the node's own.
			return false
		}
		at, ok := AtomTypeByName(local)
		return ok && (anno == at || atomDerivesFrom(anno, at))
	}
}

// builtinTypeLocalName resolves a kind test's TypeName to the local part of a
// built-in XML Schema type, reporting false when the name provably denotes
// something else. A braced name carries its URI outright; a prefixed one
// resolves through the static context (the conventional xs prefix and the
// standard prefixes are always bound, matching itemTypePrefixErr, and an
// UNBOUND prefix names nothing at all).
func builtinTypeLocalName(name string, ctx *Context) (string, bool) {
	if uri, local, braced := parseBracedName(name); braced {
		return local, uri == nsXS
	}
	p := lexicalTypePrefix(name)
	if p == "" {
		return localOfEQName(name), true
	}
	uri, ok := nsForKnownPrefix(p) // includes the conventional xs
	if !ok {
		if ctx == nil || ctx.NS == nil {
			return "", false
		}
		if uri, ok = ctx.NS.ResolveNS(p); !ok {
			return "", false
		}
	}
	return localOfEQName(name), uri == nsXS
}

func matchNameTest(t NodeTest, axis string, c *xmltree.Node, ctx *Context) (bool, error) {
	// principal node type per axis
	switch axis {
	case "attribute":
		if c.Kind != xmltree.KindAttribute {
			return false, nil
		}
	case "namespace":
		if c.Kind != xmltree.KindNamespace {
			return false, nil
		}
	default:
		if c.Kind != xmltree.KindElement {
			return false, nil
		}
	}
	if t.AnyName && t.Prefix == "" {
		return true, nil
	}
	// "*:local" — wildcard namespace, specific local name (any namespace, incl. none).
	if t.WildNS {
		return c.Name.Local == t.Local, nil
	}
	// Q{uri}local / Q{uri}*: the URI is explicit — no prefix resolution and no
	// default element namespace, even for Q{}local (eqname-014/026/027).
	if t.Braced {
		return c.Name.Space == t.URI && (t.AnyName || c.Name.Local == t.Local), nil
	}
	uri, err := resolvePrefix(ctx, t.Prefix)
	if err != nil {
		return false, err
	}
	// xpath-default-namespace: an unprefixed name test on an element axis
	// matches names in the default element namespace.
	if t.Prefix == "" && uri == "" && axis != "attribute" && axis != "namespace" {
		uri = ctx.DefaultElemNS
	}
	if t.AnyName {
		return c.Name.Space == uri, nil
	}
	return c.Name.Space == uri && c.Name.Local == t.Local, nil
}

func resolvePrefix(ctx *Context, prefix string) (string, error) {
	if prefix == "" {
		return "", nil
	}
	if ctx.NS != nil {
		if uri, ok := ctx.NS.ResolveNS(prefix); ok {
			return uri, nil
		}
		// A namespace resolver exists but the prefix is unbound (XPST0081).
		// The "xml" prefix is implicitly bound.
		if prefix == "xml" {
			return "http://www.w3.org/XML/1998/namespace", nil
		}
		return "", fmt.Errorf("err:XPST0081: prefix %q has no namespace binding", prefix)
	}
	return "", nil
}

// InScopeNamespaceNodes returns the namespace nodes on an element's namespace
// axis — the exported form of the axis itself, for a host that must enumerate
// every node of a tree (the XSLT engine's xsl:key index, whose match pattern
// may be namespace-node() — key-087/090).
func InScopeNamespaceNodes(n *xmltree.Node) []*xmltree.Node {
	return namespaceNodes(n, nil)
}
