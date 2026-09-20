package xpath

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

func init() {
	coreFuncs["parse-xml"] = fnParseXML
	coreFuncs["parse-xml-fragment"] = fnParseXMLFragment
	coreFuncs["unparsed-text"] = fnUnparsedText
	coreFuncs["unparsed-text-lines"] = fnUnparsedTextLines
	coreFuncs["unparsed-text-available"] = fnUnparsedTextAvailable
	coreFuncs["doc"] = fnDoc
	coreFuncs["collection"] = fnCollection
	coreFuncs["uri-collection"] = fnURICollection
	coreFuncs["doc-available"] = fnDocAvailable
	// The engine exposes no environment variables (a conforming choice); the
	// FOTS tests are all guarded by "empty($all) or …".
	coreFuncs["environment-variable"] = func(c *Context, a []Object) (Object, error) {
		items := Items(arg(a, 0))
		if len(items) != 1 || !qnStringy(items[0]) {
			return nil, fmt.Errorf("err:XPTY0004: environment-variable expects a single xs:string")
		}
		return Sequence{}, nil
	}
	coreFuncs["available-environment-variables"] = func(c *Context, a []Object) (Object, error) { return Sequence{}, nil }
}

// ResolveAgainstBase is the exported form of resolveAgainstBase, for a host
// (internal/xslt's fn:transform bridge) that needs the identical
// fn:doc/unparsed-text URI-resolution rule outside this package.
func ResolveAgainstBase(base, ref string) string { return resolveAgainstBase(base, ref) }

// resolveAgainstBase resolves a possibly-relative URI reference against the
// static base URI, matching the resolution fn:unparsed-text/doc apply.
func resolveAgainstBase(base, ref string) string {
	if base == "" {
		return ref
	}
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

// reEncodingName matches the XML EncName production; an encoding argument that
// fails it is FOUT1190 (fn-unparsed-text-036: "123").
var reEncodingName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]*$`)

func validEncodingName(s string) bool { return reEncodingName.MatchString(s) }

// hrefArg checks the $href argument of the resource functions ($href as
// xs:string?): the empty sequence is reported as absent, anything but a single
// string-family value is XPTY0004 (doc-available(xs:integer(2)),
// fn-unparsed-text-available-008).
func hrefArg(o Object, fn string) (string, bool, error) {
	items, err := Atomize(o)
	if err != nil {
		return "", false, err
	}
	if len(items) == 0 {
		return "", false, nil
	}
	if len(items) > 1 || !qnStringy(items[0]) {
		return "", false, fmt.Errorf("err:XPTY0004: fn:%s expects an xs:string? href", fn)
	}
	return itemString(items[0]), true, nil
}

// encodingArg checks the $encoding argument ($encoding as xs:string — NOT
// optional: fn-unparsed-text-available-012) and returns it; the caller decides
// whether a value that is not an EncName is FOUT1190 or "unavailable".
func encodingArg(a []Object, fn string) (enc string, present bool, err error) {
	if len(a) < 2 {
		return "", false, nil
	}
	items, err := Atomize(a[1])
	if err != nil {
		return "", false, err
	}
	if len(items) != 1 || !qnStringy(items[0]) {
		return "", false, fmt.Errorf("err:XPTY0004: fn:%s expects a single xs:string encoding", fn)
	}
	return itemString(items[0]), true, nil
}

// retrieveText fetches $href through the resolver and checks the result is
// text: undecodable bytes (not UTF-8 under the assumed encoding) or a
// non-XML character are FOUT1200 (fn-unparsed-text-037/038/039).
func retrieveText(c *Context, uri string) (string, error) {
	if c.Resolver == nil {
		return "", fmt.Errorf("err:FOUT1170: cannot retrieve unparsed text %q", uri)
	}
	s, ok := c.Resolver.ResolveText(uri)
	if !ok {
		return "", fmt.Errorf("err:FOUT1170: cannot retrieve unparsed text %q", uri)
	}
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("err:FOUT1200: resource %q cannot be decoded as text", uri)
	}
	// F&O 3.1: FOUT1190 covers octets that cannot be decoded with the chosen
	// encoding AND the case where the resulting characters are not permitted
	// XML characters; FOUT1200 is only "no encoding given and none can be
	// inferred". A NUL in the resource is squarely FOUT1190, which is the code
	// unparsed-text-lines-004's xsl:catch selects on.
	if !allValidXMLChars(s) {
		return "", fmt.Errorf("err:FOUT1190: resource %q contains characters that are not permitted in XML", uri)
	}
	return s, nil
}

func fnUnparsedText(c *Context, a []Object) (Object, error) {
	href, ok, err := hrefArg(arg(a, 0), "unparsed-text")
	if err != nil {
		return nil, err
	}
	if enc, present, err := encodingArg(a, "unparsed-text"); err != nil {
		return nil, err
	} else if present && !validEncodingName(enc) {
		return nil, fmt.Errorf("err:FOUT1190: invalid encoding name %q", enc)
	}
	if !ok {
		return Sequence{}, nil
	}
	uri := resolveAgainstBase(c.BaseURI, href)
	s, err := retrieveText(c, uri)
	if err != nil {
		return nil, err
	}
	return NewString(s), nil
}

// fnUnparsedTextLines splits the retrieved text into lines (on \r\n, \r or \n),
// without a trailing empty line for a terminating newline.
func fnUnparsedTextLines(c *Context, a []Object) (Object, error) {
	href, ok, err := hrefArg(arg(a, 0), "unparsed-text-lines")
	if err != nil {
		return nil, err
	}
	if enc, present, err := encodingArg(a, "unparsed-text-lines"); err != nil {
		return nil, err
	} else if present && !validEncodingName(enc) {
		return nil, fmt.Errorf("err:FOUT1190: invalid encoding name %q", enc)
	}
	if !ok {
		return Sequence{}, nil
	}
	uri := resolveAgainstBase(c.BaseURI, href)
	s, err := retrieveText(c, uri)
	if err != nil {
		return nil, err
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return Sequence{}, nil
	}
	var items []Item
	for _, line := range strings.Split(s, "\n") {
		items = append(items, NewString(line))
	}
	return FromItems(items), nil
}

func fnUnparsedTextAvailable(c *Context, a []Object) (Object, error) {
	// Argument TYPE errors are still raised (fn-unparsed-text-available-008/
	// 010/012); it is only the retrieval outcome that is folded into false.
	href, ok, err := hrefArg(arg(a, 0), "unparsed-text-available")
	if err != nil {
		return nil, err
	}
	enc, present, err := encodingArg(a, "unparsed-text-available")
	if err != nil {
		return nil, err
	}
	if !ok {
		return NewBool(false), nil
	}
	// A bad encoding name, an unretrievable resource, or content that is not
	// valid text (bad UTF-8 or a non-XML character) all make it false — never
	// an error (fn-unparsed-text-available-035/037/038).
	if present && !validEncodingName(enc) {
		return NewBool(false), nil
	}
	uri := resolveAgainstBase(c.BaseURI, href)
	if _, err := retrieveText(c, uri); err != nil {
		return NewBool(false), nil
	}
	return NewBool(true), nil
}

// allValidXMLChars reports whether every rune of s is a legal XML character.
func allValidXMLChars(s string) bool {
	for _, r := range s {
		if !isValidXMLChar(r) {
			return false
		}
	}
	return true
}

func fnDoc(c *Context, a []Object) (Object, error) {
	href, ok, err := hrefArg(arg(a, 0), "doc")
	if err != nil {
		return nil, err
	}
	if !ok {
		return Sequence{}, nil
	}
	uri := resolveAgainstBase(c.BaseURI, href)
	if c.HomeDoc != nil && (uri == "" || uri == c.HomeDoc.Base) {
		// doc(''): the tree of the stylesheet module containing this call
		// (document-0302), matching the legacy document('') special case —
		// but only when the effective (resolved) URI actually denotes that
		// module's own location; an xml:base override in scope at the call
		// site can redirect an EMPTY href elsewhere entirely (base-uri-050),
		// in which case this falls through to a real resolver lookup below.
		return NodeSet{c.HomeDoc}, nil
	}
	if c.Resolver != nil {
		if d, ok := c.Resolver.ResolveDoc(uri); ok {
			return NodeSet{d}, nil
		}
	}
	return nil, fmt.Errorf("err:FODC0002: cannot retrieve document %q", uri)
}

func fnDocAvailable(c *Context, a []Object) (Object, error) {
	href, ok, err := hrefArg(arg(a, 0), "doc-available")
	if err != nil {
		return nil, err
	}
	if !ok {
		return NewBool(false), nil
	}
	uri := resolveAgainstBase(c.BaseURI, href)
	if c.Resolver != nil {
		if _, ok := c.Resolver.ResolveDoc(uri); ok {
			return NewBool(true), nil
		}
	}
	return NewBool(false), nil
}

// fnParseXML implements fn:parse-xml: a well-formed document parsed to a
// document node (FODC0006 otherwise).
func fnParseXML(c *Context, a []Object) (Object, error) {
	in := arg(a, 0)
	if len(Items(in)) == 0 {
		return Sequence{}, nil
	}
	doc, err := xmltree.Parse(ToString(in))
	if err != nil {
		return nil, fmt.Errorf("err:FODC0006: not a well-formed document: %v", err)
	}
	if err := checkBoundPrefixes(doc); err != nil {
		return nil, err
	}
	// fn:parse-xml's result has no document-uri property (XDM 3.1 §6.2.1) —
	// it was never retrieved from anywhere, unlike an fn:doc() result. Marked
	// Ephemeral so fn:document-uri's own "fall back to the calling
	// expression's static base URI" default (for a host resolver that never
	// populates Node.Base itself) does not misattribute this constructed
	// document to wherever the CALLER happens to live (parse-xml-011).
	doc.Ephemeral = true
	return NodeSet{doc}, nil
}

// checkBoundPrefixes rejects elements/attributes whose prefix never resolved
// (encoding/xml tolerates <p:a/> with p unbound; parse-xml must not —
// parse-xml-015, FODC0006).
func checkBoundPrefixes(n *xmltree.Node) error {
	if n.Kind == xmltree.KindElement {
		// encoding/xml leaves an UNRESOLVED prefix verbatim in Space with an
		// empty Prefix; a real default-namespace element has the in-scope
		// default equal to its Space.
		if n.Name.Prefix == "" && n.Name.Space != "" {
			if def, _ := n.LookupPrefix(""); def != n.Name.Space {
				return fmt.Errorf("err:FODC0006: unbound namespace prefix %q", n.Name.Space)
			}
		}
		for _, a := range n.Attrs {
			if a.Name.Prefix == "" && a.Name.Space != "" &&
				a.Name.Space != "http://www.w3.org/XML/1998/namespace" &&
				a.Name.Space != "http://www.w3.org/2000/xmlns/" && a.Name.Space != "xmlns" {
				if uri, ok := n.LookupPrefix(a.Name.Space); !ok || uri == "" {
					// attribute Space holding a bound PREFIX name resolves;
					// otherwise it is an unbound prefix
					bound := false
					for cur := n; cur != nil; cur = cur.Parent {
						for _, ns := range cur.NS {
							if ns.Value == a.Name.Space {
								bound = true
							}
						}
					}
					if !bound {
						return fmt.Errorf("err:FODC0006: unbound namespace prefix %q", a.Name.Space)
					}
				}
			}
		}
	}
	for _, ch := range n.Children {
		if err := checkBoundPrefixes(ch); err != nil {
			return err
		}
	}
	return nil
}

// fnParseXMLFragment implements fn:parse-xml-fragment: external general
// parsed entity rules — multiple roots and bare text are fine, a full XML
// declaration with standalone is not.
func fnParseXMLFragment(c *Context, a []Object) (Object, error) {
	in := arg(a, 0)
	if len(Items(in)) == 0 {
		return Sequence{}, nil
	}
	s := ToString(in)
	// A leading text declaration is allowed, but per the XML TextDecl grammar
	// it MUST carry an encoding and MUST NOT carry a standalone pseudo-attr;
	// strip a valid one, reject an invalid one (parse-xml-fragment-014/016).
	if strings.HasPrefix(s, "<?xml") {
		if end := strings.Index(s, "?>"); end >= 0 {
			decl := s[:end]
			if strings.Contains(decl, "standalone") || !strings.Contains(decl, "encoding") {
				return nil, fmt.Errorf("err:FODC0006: a fragment text declaration must have an encoding and no standalone")
			}
			s = s[end+2:]
		}
	}
	// A DOCTYPE declaration is not permitted in a fragment (parse-xml-fragment-020).
	if strings.Contains(s, "<!DOCTYPE") {
		return nil, fmt.Errorf("err:FODC0006: a fragment must not contain a DOCTYPE declaration")
	}
	// Wrap the fragment in a synthetic root so multiple top-level items and
	// bare text parse; the wrapper is stripped afterwards. Its name must be a
	// valid element name (a NUL-based name is rejected by the XML parser).
	doc, err := xmltree.Parse("<parse-xml-fragment-root>" + s + "</parse-xml-fragment-root>")
	if err != nil {
		return nil, fmt.Errorf("err:FODC0006: not a well-formed fragment: %v", err)
	}
	root := xmltree.RootElement(doc)
	frag := &xmltree.Node{Kind: xmltree.KindDocument}
	if root != nil {
		if err := checkBoundPrefixes(root); err != nil {
			return nil, err
		}
		for _, ch := range root.Children {
			ch.Parent = frag
			frag.Children = append(frag.Children, ch)
		}
	}
	return NodeSet{frag}, nil
}

// collectionURI resolves the optional $uri argument of fn:collection /
// fn:uri-collection: the empty sequence (or an omitted argument) names the
// DEFAULT collection, reported as the empty URI.
func collectionURI(c *Context, a []Object, fn string) (string, error) {
	if len(a) == 0 {
		return "", nil
	}
	href, ok, err := hrefArg(arg(a, 0), fn)
	if err != nil || !ok {
		return "", err
	}
	return resolveAgainstBase(c.BaseURI, href), nil
}

// fnCollection implements fn:collection: the sequence of nodes in the
// collection named by $uri, or in the default collection when $uri is absent
// or the empty sequence. A host supplies collections through a resolver that
// also implements CollectionResolver; with none, the default collection is
// empty (a conforming choice) and any named collection is FODC0002.
func fnCollection(c *Context, a []Object) (Object, error) {
	uri, err := collectionURI(c, a, "collection")
	if err != nil {
		return nil, err
	}
	if cr, ok := c.Resolver.(CollectionResolver); ok {
		if nodes, ok := cr.ResolveCollection(uri); ok {
			out := make(NodeSet, 0, len(nodes))
			out = append(out, nodes...)
			return out, nil
		}
	}
	return nil, fmt.Errorf("err:FODC0002: cannot retrieve collection %q", uri)
}

// fnURICollection implements fn:uri-collection: the document URIs of the
// nodes a collection contains.
func fnURICollection(c *Context, a []Object) (Object, error) {
	v, err := fnCollection(c, a)
	if err != nil {
		return nil, err
	}
	var items []Item
	for _, it := range Items(v) {
		if nd, ok := it.(*xmltree.Node); ok {
			items = append(items, NewAnyURI(nd.Base))
		}
	}
	return FromItems(items), nil
}
