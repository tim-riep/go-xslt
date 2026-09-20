package xpath

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// fn_qname.go implements the XPath/XQuery 3.1 "qname" family of functions:
//
//	fn:QName($uri as xs:string?, $qname as xs:string) as xs:QName
//	fn:prefix-from-QName($arg as xs:QName?) as xs:NCName?
//	fn:local-name-from-QName($arg as xs:QName?) as xs:NCName?
//	fn:namespace-uri-from-QName($arg as xs:QName?) as xs:anyURI
//	fn:namespace-uri-for-prefix($prefix as xs:string?, $element as element()) as xs:anyURI?
//	fn:in-scope-prefixes($element as element()) as xs:string*
//	fn:resolve-QName($qname as xs:string?, $element as element()) as xs:QName?
//
// All private helpers in this file are prefixed "qn" to avoid clashes with
// other concurrently-written sibling files.

func init() {
	coreFuncs["QName"] = qnFnQName
	coreFuncs["prefix-from-QName"] = qnFnPrefixFromQName
	coreFuncs["local-name-from-QName"] = qnFnLocalNameFromQName
	coreFuncs["namespace-uri-from-QName"] = qnFnNamespaceURIFromQName
	coreFuncs["namespace-uri-for-prefix"] = qnFnNamespaceURIForPrefix
	coreFuncs["in-scope-prefixes"] = qnFnInScopePrefixes
	coreFuncs["resolve-QName"] = qnFnResolveQName
}

// qnSplit splits a lexical QName "prefix:local" into its parts; the prefix is
// empty when the lexical form is unprefixed.
func qnSplit(s string) (prefix, local string) {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

// qnIsEmpty reports whether an optional argument was supplied as the empty
// sequence (or omitted entirely).
func qnIsEmpty(o Object) bool {
	if o == nil {
		return true
	}
	switch v := o.(type) {
	case Sequence:
		return len(v) == 0
	case NodeSet:
		return len(v) == 0
	}
	return false
}

// qnFirstItem returns the first item of an Object, or nil if empty.
func qnFirstItem(o Object) Item {
	its := Items(o)
	if len(its) == 0 {
		return nil
	}
	return its[0]
}

// qnAsQName extracts the QName *Atomic from an Object. The XDM type system
// guarantees a QName-typed value here; we accept an *Atomic with T==XSqname.
// Other atomics are tolerated by parsing their lexical form (prefix/local
// only, with no namespace URI available — a documented limitation).
func qnAsQName(o Object) (*Atomic, error) {
	its := Items(o)
	// $arg is declared xs:QName? — an OPTIONAL SINGLETON. A multi-item
	// sequence (LocalNameFromQNameFunc010/NamespaceURIFromQNameFunc010:
	// /root/elemQN selecting several elements) is a cardinality violation,
	// XPTY0004, not "just take the first one".
	if len(its) > 1 {
		return nil, fmt.Errorf("err:XPTY0004: argument is not a singleton xs:QName?")
	}
	if len(its) == 0 {
		return nil, nil
	}
	it := its[0]
	if a, ok := it.(*Atomic); ok {
		if a.T == XSqname {
			return a, nil
		}
		// Tolerate a string-ish atomic carrying a lexical QName: prefix/local
		// are recovered, but the namespace URI is unknown. Any other atomic
		// type (xs:integer, xs:time) is XPTY0004 (LocalNameFromQNameFunc016/
		// 017, NamespaceURIFromQNameFunc016/017, fn-prefix-from-qname-2).
		if !isComparableAsString(a) {
			return nil, fmt.Errorf("err:XPTY0004: %s is not an xs:QName", a.T)
		}
		pre, loc := qnSplit(a.Lexical())
		return NewQName(xmltree.Name{Prefix: pre, Local: loc}), nil
	}
	if nd, ok := it.(*xmltree.Node); ok {
		// The function conversion rules ATOMIZE a node argument before the
		// xs:QName? signature is applied, and a schema-validated QName-typed
		// node atomizes to a real xs:QName — prefix resolved against its own
		// namespaces (toAtomic, cast.go). Without this a typed node reaches
		// these functions un-atomized and is rejected outright
		// (type-functions-0501). Only a genuine QName is accepted, so an
		// untyped node keeps the XPTY0004 it has always had.
		if av, err := toAtomic(nd); err == nil && av != nil && av.T == XSqname {
			return av, nil
		}
	}
	return nil, fmt.Errorf("err:XPTY0004: argument is not an xs:QName")
}

// qnElement extracts the element node operand used by the namespace-context
// functions. The argument is required to be a single element node.
func qnElement(o Object) (*xmltree.Node, error) {
	ns, ok := ToNodeSet(o)
	if !ok || len(ns) == 0 {
		return nil, fmt.Errorf("err:XPTY0004: expected an element node")
	}
	n := ns.first()
	if n == nil || n.Kind != xmltree.KindElement {
		return nil, fmt.Errorf("err:XPTY0004: expected an element node")
	}
	return n, nil
}

// qnFnQName implements fn:QName($paramURI, $paramQName).
//
// The lexical QName is split into prefix/local; the namespace URI is taken
// from $paramURI. If a prefix is present, a non-empty URI is required
// (err:FOCA0002).
func qnFnQName(c *Context, a []Object) (Object, error) {
	uriArg := arg(a, 0)
	if it := firstItem(uriArg); it != nil && !qnStringy(it) {
		return nil, fmt.Errorf("err:XPTY0004: fn:QName namespace argument must be a string")
	}
	uri := ""
	if !qnIsEmpty(uriArg) {
		uri = ToString(uriArg)
	}
	lexItem := firstItem(arg(a, 1))
	if lexItem == nil || !qnStringy(lexItem) {
		return nil, fmt.Errorf("err:XPTY0004: fn:QName lexical argument must be a string")
	}
	lexical := strings.TrimSpace(ToString(arg(a, 1)))
	pre, loc, ok := qnValidLexical(lexical)
	if !ok {
		return nil, fmt.Errorf("err:FOCA0002: invalid lexical QName %q", lexical)
	}
	if pre != "" && uri == "" {
		return nil, fmt.Errorf("err:FOCA0002: prefix %q has no namespace URI", pre)
	}
	return NewQName(xmltree.Name{Prefix: pre, Local: loc, Space: uri}), nil
}

// qnStringy reports whether an item is usable as an xs:string argument: a bare
// Go string, or an xs:string/xs:untypedAtomic/xs:anyURI atomic. Numeric,
// boolean, QName and other atomics are rejected (ExpandedQNameConstructFunc015/016).
func qnStringy(it Item) bool {
	switch v := it.(type) {
	case string:
		return true
	case *Atomic:
		return v.T == XSstring || v.T == XSuntypedAtomic || v.T == XSanyURI
	}
	return false
}

// qnValidLexical validates a lexical QName (NCName or NCName ':' NCName) and
// returns its prefix and local parts.
func qnValidLexical(s string) (pre, loc string, ok bool) {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		pre, loc = s[:i], s[i+1:]
		if !reNCNameLex.MatchString(pre) || !reNCNameLex.MatchString(loc) {
			return "", "", false
		}
		return pre, loc, true
	}
	if !reNCNameLex.MatchString(s) {
		return "", "", false
	}
	return "", s, true
}

// qnFnPrefixFromQName implements fn:prefix-from-QName: returns the prefix as an
// xs:NCName, or the empty sequence when the QName has no prefix.
func qnFnPrefixFromQName(c *Context, a []Object) (Object, error) {
	q, err := qnAsQName(arg(a, 0))
	if err != nil {
		return nil, err
	}
	if q == nil {
		return Sequence{}, nil
	}
	pre, _ := qnSplit(q.Lexical())
	if pre == "" {
		return Sequence{}, nil
	}
	return qnNCName(pre), nil
}

// qnFnLocalNameFromQName implements fn:local-name-from-QName.
func qnFnLocalNameFromQName(c *Context, a []Object) (Object, error) {
	q, err := qnAsQName(arg(a, 0))
	if err != nil {
		return nil, err
	}
	if q == nil {
		return Sequence{}, nil
	}
	_, loc := qnSplit(q.Lexical())
	return qnNCName(loc), nil
}

// qnFnNamespaceURIFromQName implements fn:namespace-uri-from-QName: returns the
// namespace URI as xs:anyURI (the zero-length anyURI when the QName is in no
// namespace), or the empty sequence for an empty input.
func qnFnNamespaceURIFromQName(c *Context, a []Object) (Object, error) {
	q, err := qnAsQName(arg(a, 0))
	if err != nil {
		return nil, err
	}
	if q == nil {
		return Sequence{}, nil
	}
	return NewAnyURI(qnURIOf(q)), nil
}

// qnFnNamespaceURIForPrefix implements fn:namespace-uri-for-prefix: looks up the
// namespace URI bound to $prefix in the in-scope namespaces of $element.
// Returns the empty sequence when the prefix is not bound.
func qnFnNamespaceURIForPrefix(c *Context, a []Object) (Object, error) {
	prefix := ""
	if !qnIsEmpty(arg(a, 0)) {
		prefix = ToString(arg(a, 0))
	}
	elem, err := qnElement(arg(a, 1))
	if err != nil {
		return nil, err
	}
	uri, ok := elem.LookupPrefix(prefix)
	if !ok {
		// The default (no-prefix) binding may simply be "no namespace".
		if prefix == "" {
			return Sequence{}, nil
		}
		return Sequence{}, nil
	}
	return NewAnyURI(uri), nil
}

// qnFnInScopePrefixes implements fn:in-scope-prefixes: returns the prefixes of
// all in-scope namespaces of $element, as xs:string*. The default-namespace
// binding is reported with the zero-length-string prefix; "xml" is always in
// scope.
func qnFnInScopePrefixes(c *Context, a []Object) (Object, error) {
	elem, err := qnElement(arg(a, 0))
	if err != nil {
		return nil, err
	}
	bindings := elem.InScopeNamespaces()
	prefixes := make([]string, 0, len(bindings)+1)
	for p, uri := range bindings {
		if uri == "" {
			continue // xmlns="" undeclaration: no binding in scope (fn-in-scope-prefixes-26)
		}
		prefixes = append(prefixes, p)
	}
	prefixes = append(prefixes, "xml")
	prefixes = qnDedup(prefixes)
	sort.Strings(prefixes)
	items := make([]Item, len(prefixes))
	for i, p := range prefixes {
		items[i] = NewString(p)
	}
	return FromItems(items), nil
}

// qnFnResolveQName implements fn:resolve-QName($qname, $element): resolves the
// lexical QName $qname against the in-scope namespaces of $element. Returns the
// empty sequence when $qname is the empty sequence.
func qnFnResolveQName(c *Context, a []Object) (Object, error) {
	if qnIsEmpty(arg(a, 0)) {
		return Sequence{}, nil
	}
	lexical := strings.TrimSpace(ToString(arg(a, 0)))
	elem, err := qnElement(arg(a, 1))
	if err != nil {
		return nil, err
	}
	pre, loc := qnSplit(lexical)
	// The prefix (if any) and local part must each be a valid NCName — in
	// particular neither may itself contain a ':' (a lexical QName has AT
	// MOST one colon: type-0156's "pre:thi:ng" splits at the first colon to
	// local="thi:ng", which fails this exactly because it still has one) and
	// neither may start with a character outside the NCName start class
	// (type-0155's "pre:+thing" local="+thing").
	if loc == "" || !reNCNameLex.MatchString(loc) || (pre != "" && !reNCNameLex.MatchString(pre)) {
		return nil, fmt.Errorf("err:FOCA0002: invalid lexical QName %q", lexical)
	}
	uri := ""
	if pre == "" {
		// Default-element namespace, if any.
		if u, ok := elem.LookupPrefix(""); ok {
			uri = u
		}
	} else {
		u, ok := elem.LookupPrefix(pre)
		if !ok {
			return nil, fmt.Errorf("err:FONS0004: no namespace bound to prefix %q", pre)
		}
		uri = u
	}
	return NewQName(xmltree.Name{Prefix: pre, Local: loc, Space: uri}), nil
}

// qnNCName returns an NCName-typed atomic. xs:NCName is a derived string type;
// the registry exposes XSncname, so prefer it when available, falling back to
// xs:string.
func qnNCName(s string) *Atomic {
	if t, ok := AtomTypeByName("xs:NCName"); ok {
		return &Atomic{T: t, s: s}
	}
	return NewString(s)
}

// qnURIOf returns the namespace URI carried by a QName atomic. Only QName-typed
// atomics retain their namespace URI; a lexical-only QName yields "".
func qnURIOf(q *Atomic) string {
	if q != nil && q.T == XSqname {
		return q.qn.Space
	}
	return ""
}

// qnDedup removes duplicate strings, preserving first-seen order.
func qnDedup(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
