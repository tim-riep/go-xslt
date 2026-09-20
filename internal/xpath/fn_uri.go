package xpath

import (
	"fmt"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

func init() {
	coreFuncs["encode-for-uri"] = fnURIEncodeForURI
	coreFuncs["iri-to-uri"] = fnURIIriToURI
	coreFuncs["escape-html-uri"] = fnURIEscapeHTMLURI
	coreFuncs["resolve-uri"] = fnURIResolveURI
}

// uriIsEmpty reports whether the argument is an empty sequence.
func uriIsEmpty(o Object) bool {
	if o == nil {
		return true
	}
	return len(Items(o)) == 0
}

// uriArgString returns the string value of argument i, defaulting to "" when
// the argument is absent or an empty sequence.
func uriArgString(args []Object, i int) string {
	o := arg(args, i)
	if uriIsEmpty(o) {
		return ""
	}
	return ToString(o)
}

// uriUnreserved reports whether b is an RFC 3986 unreserved character that must
// not be percent-encoded by fn:encode-for-uri.
func uriUnreserved(b byte) bool {
	switch {
	case b >= 'A' && b <= 'Z':
		return true
	case b >= 'a' && b <= 'z':
		return true
	case b >= '0' && b <= '9':
		return true
	case b == '-' || b == '_' || b == '.' || b == '~':
		return true
	}
	return false
}

// fnURIEncodeForURI implements fn:encode-for-uri($s as xs:string?) as xs:string.
// It percent-encodes every character except the RFC 3986 unreserved set.
func fnURIEncodeForURI(c *Context, args []Object) (Object, error) {
	// xs:string?: a numeric argument is XPTY0004 (fn-encode-for-uri1args-6).
	s, err := stringArgStrict("encode-for-uri", args, 0, true)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if uriUnreserved(ch) {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return NewString(b.String()), nil
}

// fnURIIriToURI implements fn:iri-to-uri($s as xs:string?) as xs:string.
// It percent-encodes the bytes of $s that are not allowed in a URI, while
// leaving already-present URI delimiters and reserved characters intact.
func fnURIIriToURI(c *Context, args []Object) (Object, error) {
	// xs:string?: a numeric or plural argument is XPTY0004
	// (fn-iri-to-uri1args-5, K2-IRIToURIfunc-3/4).
	s, err := stringArgStrict("iri-to-uri", args, 0, true)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if uriIriAllowed(ch) {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return NewString(b.String()), nil
}

// uriIriAllowed reports whether b may appear literally in the result of
// fn:iri-to-uri. This is the unreserved set plus the reserved/delimiter
// characters that make up valid URI syntax.
func uriIriAllowed(b byte) bool {
	if uriUnreserved(b) {
		return true
	}
	switch b {
	// reserved gen-delims and sub-delims plus '%' (kept so existing escapes
	// are not double-encoded) and the writing-of-URI characters.
	case '!', '#', '$', '&', '\'', '(', ')', '*', '+', ',', '-', '.', '/',
		':', ';', '=', '?', '@', '_', '~', '%', '[', ']':
		return true
	}
	return false
}

// fnURIEscapeHTMLURI implements fn:escape-html-uri($s as xs:string?) as
// xs:string. It percent-encodes every byte whose code point falls outside the
// printable ASCII range 32..126 inclusive.
func fnURIEscapeHTMLURI(c *Context, args []Object) (Object, error) {
	// xs:string?: a numeric argument is XPTY0004 (fn-escape-html-uri1args-5).
	s, err := stringArgStrict("escape-html-uri", args, 0, true)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch >= 32 && ch <= 126 {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return NewString(b.String()), nil
}

// fnURIResolveURI implements fn:resolve-uri($relative as xs:string?, $base as
// xs:string?) as xs:anyURI?. With one argument $base defaults to the static
// base URI, which is unavailable here, so the relative reference is returned
// as-is. If $relative is the empty sequence the result is the empty sequence.
func fnURIResolveURI(c *Context, args []Object) (Object, error) {
	rel := arg(args, 0)
	if uriIsEmpty(rel) {
		return Sequence{}, nil
	}
	relStr := ToString(rel)

	var baseStr string
	if len(args) >= 2 && !uriIsEmpty(arg(args, 1)) {
		baseStr = ToString(arg(args, 1))
	} else {
		// Single-argument form: resolve against the static base URI.
		baseStr = c.BaseURI
	}
	if baseStr == "" {
		// An absolute $relative needs no base; otherwise none is available.
		if r := splitURIRef(relStr); r.hasScheme && uriRefValid(r) {
			return NewAnyURI(relStr), nil
		}
		return Sequence{}, nil
	}
	resolved, err := ResolveURIRef(relStr, baseStr)
	if err != nil {
		return nil, err
	}
	return NewAnyURI(resolved), nil
}

// uriRef is a URI reference split into its five RFC 3986 components (appendix
// B). The has* flags distinguish an absent component from an empty one.
type uriRef struct {
	scheme, authority, path, query, fragment       string
	hasScheme, hasAuthority, hasQuery, hasFragment bool
}

// splitURIRef splits s per the RFC 3986 appendix B regular expression
// ^(([^:/?#]+):)?(//([^/?#]*))?([^?#]*)(\?([^#]*))?(#(.*))? — a purely
// lexical split that never normalizes or percent-encodes anything.
func splitURIRef(s string) uriRef {
	var r uriRef
	rest := s
	if i := strings.IndexAny(rest, ":/?#"); i > 0 && rest[i] == ':' {
		r.scheme, r.hasScheme = rest[:i], true
		rest = rest[i+1:]
	}
	if strings.HasPrefix(rest, "//") {
		rest = rest[2:]
		i := strings.IndexAny(rest, "/?#")
		if i < 0 {
			i = len(rest)
		}
		r.authority, r.hasAuthority = rest[:i], true
		rest = rest[i:]
	}
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		r.fragment, r.hasFragment = rest[i+1:], true
		rest = rest[:i]
	}
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		r.query, r.hasQuery = rest[i+1:], true
		rest = rest[:i]
	}
	r.path = rest
	return r
}

// String recomposes the reference (RFC 3986 §5.3).
func (r uriRef) String() string {
	var b strings.Builder
	if r.hasScheme {
		b.WriteString(r.scheme)
		b.WriteByte(':')
	}
	if r.hasAuthority {
		b.WriteString("//")
		b.WriteString(r.authority)
	}
	b.WriteString(r.path)
	if r.hasQuery {
		b.WriteByte('?')
		b.WriteString(r.query)
	}
	if r.hasFragment {
		b.WriteByte('#')
		b.WriteString(r.fragment)
	}
	return b.String()
}

// uriRefValid applies the RFC 3986 checks that make a reference *invalid*
// (FORG0002) without rejecting the LEIRI-style characters (spaces, non-ASCII)
// the spec lets an implementation accept: a well-formed scheme, complete
// percent-escapes ("http:%%" — fn-resolve-uri-4), and no colon in the first
// segment of a relative path (":" — fn-resolve-uri-3).
func uriRefValid(r uriRef) bool {
	if r.hasScheme {
		if !uriSchemeOK(r.scheme) {
			return false
		}
	} else if seg, _, _ := strings.Cut(r.path, "/"); strings.Contains(seg, ":") {
		return false
	}
	// A second "#" can only land inside the (first) fragment, and "#" is not
	// in the fragment's character repertoire — "##some.uri" is not a URI
	// reference at all (resolve-uri-018).
	if strings.Contains(r.fragment, "#") {
		return false
	}
	for _, part := range []string{r.authority, r.path, r.query, r.fragment} {
		for i := 0; i < len(part); i++ {
			if part[i] == '%' {
				if i+2 >= len(part) || !isHexDigit(part[i+1]) || !isHexDigit(part[i+2]) {
					return false
				}
			}
		}
	}
	return true
}

func uriSchemeOK(s string) bool {
	if s == "" || !isASCIILetter(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !isASCIILetter(c) && !(c >= '0' && c <= '9') && c != '+' && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

func isASCIILetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// removeDotSegments implements RFC 3986 §5.2.4.
func removeDotSegments(path string) string {
	var out []string
	in := path
	for in != "" {
		switch {
		case strings.HasPrefix(in, "../"):
			in = in[3:]
		case strings.HasPrefix(in, "./"):
			in = in[2:]
		case strings.HasPrefix(in, "/./"):
			in = in[2:]
		case in == "/.":
			in = "/"
		case strings.HasPrefix(in, "/../"):
			in = in[3:]
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		case in == "/..":
			in = "/"
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		case in == "." || in == "..":
			in = ""
		default:
			// move the first path segment (including its leading "/") to out
			start := 0
			if in[0] == '/' {
				start = 1
			}
			end := strings.IndexByte(in[start:], '/')
			if end < 0 {
				end = len(in)
			} else {
				end += start
			}
			out = append(out, in[:end])
			in = in[end:]
		}
	}
	return strings.Join(out, "")
}

// ResolveURIRef resolves a (possibly relative) URI reference against a base URI
// per RFC 3986 §5.2 (strict), on the raw strings: no case normalization
// (fn-resolve-uri-9), no percent-encoding of non-ASCII or space
// (fn-resolve-uri-30/32). An absolute reference is returned as-is; otherwise
// the base must be a valid absolute URI without a fragment (FORG0002:
// fn-resolve-uri-24/26).
func ResolveURIRef(rel, base string) (string, error) {
	r := splitURIRef(rel)
	if !uriRefValid(r) {
		return "", fmt.Errorf("err:FORG0002: invalid relative URI %q", rel)
	}
	if r.hasScheme {
		r.path = removeDotSegments(r.path)
		return r.String(), nil
	}
	b := splitURIRef(base)
	if !b.hasScheme || b.hasFragment || !uriRefValid(b) {
		return "", fmt.Errorf("err:FORG0002: invalid base URI %q", base)
	}
	var t uriRef
	if r.hasAuthority {
		t.authority, t.hasAuthority = r.authority, true
		t.path = removeDotSegments(r.path)
		t.query, t.hasQuery = r.query, r.hasQuery
	} else {
		if r.path == "" {
			t.path = b.path
			if r.hasQuery {
				t.query, t.hasQuery = r.query, true
			} else {
				t.query, t.hasQuery = b.query, b.hasQuery
			}
		} else {
			if strings.HasPrefix(r.path, "/") {
				t.path = removeDotSegments(r.path)
			} else {
				// merge (§5.2.3)
				var merged string
				if b.hasAuthority && b.path == "" {
					merged = "/" + r.path
				} else if i := strings.LastIndexByte(b.path, '/'); i >= 0 {
					merged = b.path[:i+1] + r.path
				} else {
					merged = r.path
				}
				t.path = removeDotSegments(merged)
			}
			t.query, t.hasQuery = r.query, r.hasQuery
		}
		t.authority, t.hasAuthority = b.authority, b.hasAuthority
	}
	t.scheme, t.hasScheme = b.scheme, true
	t.fragment, t.hasFragment = r.fragment, r.hasFragment
	return t.String(), nil
}

// baseURIParent returns n's parent for XML Base resolution purposes, treating
// a synthetic NoAtomicMerge sequence collector (the throwaway document node
// evalVarDef/callUserFunc build to gather an @as-typed variable/param/
// function-result body before type-checking, in internal/xslt) as
// transparent: per the XDM model such a collector never really existed, so a
// node attached only to one counts as parentless.
func baseURIParent(n *xmltree.Node) *xmltree.Node {
	p := n.Parent
	if p != nil && p.Kind == xmltree.KindDocument && p.NoAtomicMerge {
		return nil
	}
	return p
}

// effectiveRoot is like (*xmltree.Node).Root() but stops climbing at the same
// synthetic-collector boundary baseURIParent does: a node extracted from an
// @as-typed variable/param/function/xsl:sequence body still carries a raw
// .Parent link back to the throwaway NoAtomicMerge document node that
// gathered it (xmltree has no separate "detach" step), but per the XDM model
// that collector never existed — the item is a genuinely PARENTLESS,
// standalone tree. Used for '/' 's XPDY0050 check (sequence-0135/0136: a
// for-each over such an @as="element()" value must see ITSELF as the root,
// not the scratch document, so "/" correctly errors instead of silently
// resolving to that transient wrapper).
func effectiveRoot(n *xmltree.Node) *xmltree.Node {
	cur := n
	for {
		p := cur.Parent
		if p == nil || (p.Kind == xmltree.KindDocument && p.NoAtomicMerge) {
			return cur
		}
		cur = p
	}
}

// NodeBaseURI computes the base URI of a node per XML Base: the xml:base
// attributes of its ancestor-or-self elements, innermost resolved against
// outermost, all against the fallback (document/static base) — except that a
// node carrying an intrinsic Base (xmltree.Node.Base: set on the root of a
// freshly constructed temporary tree, or retained through an xsl:copy/
// xsl:copy-of that was not re-embedded in further complex content) uses that
// value directly and stops climbing right there.
func NodeBaseURI(n *xmltree.Node, fallback string) string {
	if n == nil {
		return fallback
	}
	switch n.Kind {
	case xmltree.KindAttribute, xmltree.KindText, xmltree.KindComment, xmltree.KindPI:
		// XDM §6.5.3: the base-uri of these node kinds is always that of
		// their parent; with no (real) parent, it is the empty sequence
		// (base-uri-015: a from-scratch attribute/text/comment node).
		if baseURIParent(n) == nil {
			return ""
		}
	}
	var chain []string
	for cur := n; cur != nil; cur = baseURIParent(cur) {
		if cur.Base != "" {
			fallback = cur.Base
			break
		}
		if cur.Kind == xmltree.KindElement {
			if v, ok := cur.Attr("http://www.w3.org/XML/1998/namespace", "base"); ok {
				chain = append(chain, v)
			}
		}
		// A node spliced in from an external parsed entity resolves against
		// that entity's location — AFTER its own xml:base has been collected
		// above, because an xml:base inside an entity is relative to the
		// entity, not to the document that referenced it (base-uri-051). It
		// applies to a processing instruction from an entity too
		// (resolve-uri-021), hence the check sits outside the element-only
		// branch. Node.Base is checked before the xml:base instead (at the top
		// of the loop): that one is an already-computed base URI, not a base to
		// resolve against.
		if cur.EntityBase != "" {
			fallback = cur.EntityBase
			break
		}
	}
	base := fallback
	for i := len(chain) - 1; i >= 0; i-- {
		if base == "" {
			base = chain[i]
			continue
		}
		if r, err := ResolveURIRef(chain[i], base); err == nil {
			base = r
		}
	}
	return base
}
