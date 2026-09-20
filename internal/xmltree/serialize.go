package xmltree

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// outWriter is what the serializer writes through. strings.Builder (Serialize)
// and bufio.Writer (WriteTo, ChunkWriter) both satisfy it; neither reports an
// error mid-walk — a Builder cannot fail and a bufio.Writer latches its first
// error and turns every later write into a no-op — so the walk itself stays
// error-free and the single error check happens at Flush.
type outWriter interface {
	io.Writer
	WriteString(string) (int, error)
}

// SerializeOptions controls output serialization.
type SerializeOptions struct {
	Method             string // "xml" | "html" | "text"
	Indent             bool
	OmitXMLDeclaration bool
	Encoding           string
	CharacterMap       map[rune]string // xsl:character-map: mapped chars emit their replacement raw
	// SeparateItems + ItemSeparator implement the serialization
	// "item-separator" parameter: the TOP-LEVEL nodes of the result are
	// serialized with ItemSeparator written between each adjacent pair,
	// instead of being run together. The caller must have kept those items
	// discrete while building the tree (xslt sets NoAtomicMerge on the result
	// root when an item-separator is in force) — a merged text run is one item
	// here, exactly as sequence normalization would have produced.
	SeparateItems bool
	ItemSeparator string
	// Standalone ("yes"/"no") adds the standalone pseudo-attribute to the XML
	// declaration; "" leaves it out.
	Standalone string
	// CDATAElements lists the elements whose text children are written as
	// CDATA sections (cdata-section-elements).
	CDATAElements []Name
	// SuppressIndentation lists the elements whose content is never indented
	// even when Indent is set (suppress-indentation).
	SuppressIndentation []Name
	// HTMLVersion is the html method's html-version; 5 (or more) emits the
	// <!DOCTYPE HTML> declaration.
	HTMLVersion string
	// IncludeContentType adds a Content-Type <meta> as the first child of an
	// html/xhtml head element (include-content-type; spec default is on).
	IncludeContentType bool
	// MediaType is the html/xhtml methods' media-type serialization parameter,
	// used in the generated Content-Type meta's content value
	// ("<media-type>; charset=<encoding>"); "" defaults to "text/html".
	MediaType string
	// DoctypePublic/DoctypeSystem (xml/xhtml methods): when DoctypeSystem is
	// set, a <!DOCTYPE> declaration is written immediately before the first
	// element (output-0234) — PUBLIC form when DoctypePublic is also set,
	// SYSTEM form otherwise. A DoctypePublic with no DoctypeSystem is not
	// well-formed XML on its own and is ignored.
	DoctypePublic string
	DoctypeSystem string
	// NoEscapeURIAttributes disables the html/xhtml methods' default
	// percent-encoding of non-ASCII bytes in well-known URI-valued attributes
	// (escape-uri-attributes="no"/"false"/"0"). The spec default is escaping
	// ON, so the zero value (false) keeps that default.
	NoEscapeURIAttributes bool
	// ByteOrderMark prepends a literal U+FEFF to the serialized output
	// (byte-order-mark="yes"), for every method.
	ByteOrderMark bool
	// NormalizationForm is xsl:output/@normalization-form ("" / "none" means
	// no normalization; "NFC"/"NFD"/"NFKC"/"NFKD" normalize text and
	// attribute content — but not a character map's own replacement strings,
	// which are immune (character-map-025/028): mapChars normalizes each
	// unmapped run before escaping it, leaving mapped substitutions raw).
	NormalizationForm string
	// XMLVersion is the xml/xhtml methods' "version" serialization parameter
	// ("" = 1.0). It selects the version written in the XML declaration and,
	// at 1.1, turns C0/C1 control characters into numeric character
	// references (they are legal in an XML 1.1 document only in that form —
	// xml-version-007..010/018).
	XMLVersion string
	// UndeclarePrefixes is the xml method's "undeclare-prefixes" parameter:
	// at XML 1.1 a namespace node explicitly undeclaring a prefix is
	// serialized as xmlns:p="" instead of being dropped (xml-version-026).
	UndeclarePrefixes bool
}

// xmlVersion11 reports whether the xml/xhtml output version is 1.1.
func (s *serializer) xmlVersion11() bool {
	return strings.TrimSpace(s.opts.XMLVersion) == "1.1" &&
		(s.opts.Method == "" || s.opts.Method == "xml" || s.opts.Method == "xhtml")
}

// isXMLControlChar reports whether r is a control character that an XML 1.1
// serializer must write as a numeric character reference: the C0 range minus
// tab/LF/CR, plus DEL and the C1 range.
func isXMLControlChar(r rune) bool {
	return (r >= 0x1 && r <= 0x1F && r != 0x9 && r != 0xA && r != 0xD) ||
		(r >= 0x7F && r <= 0x9F)
}

// escapeControls replaces every isXMLControlChar rune with its decimal
// numeric character reference.
func escapeControls(v string) string {
	has := false
	for _, r := range v {
		if isXMLControlChar(r) {
			has = true
			break
		}
	}
	if !has {
		return v
	}
	var b strings.Builder
	for _, r := range v {
		if isXMLControlChar(r) {
			fmt.Fprintf(&b, "&#%d;", r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// nameIn reports whether n's expanded name is in the list.
func nameIn(list []Name, n Name) bool {
	for _, x := range list {
		if x.Local == n.Local && x.Space == n.Space {
			return true
		}
	}
	return false
}

// htmlVersion5 reports whether an html-version value denotes HTML 5 or later.
func htmlVersion5(v string) bool {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	return err == nil && f >= 5
}

// quoteLiteral wraps a DOCTYPE public/system literal in double quotes, or
// single quotes if the value itself contains a double quote (output-0311: a
// system literal of ABC"DEF cannot use "..." without breaking out early —
// the XML grammar allows either delimiter, so switch to '...' instead).
func quoteLiteral(s string) string {
	if strings.Contains(s, `"`) {
		return `'` + s + `'`
	}
	return `"` + s + `"`
}

// doctypeDecl builds the <!DOCTYPE ...> declaration text for the xml/xhtml
// output methods' doctype-public/doctype-system serialization parameters.
func doctypeDecl(rootName, public, system string) string {
	if public != "" {
		return `<!DOCTYPE ` + rootName + ` PUBLIC ` + quoteLiteral(public) + ` ` + quoteLiteral(system) + `>`
	}
	return `<!DOCTYPE ` + rootName + ` SYSTEM ` + quoteLiteral(system) + `>`
}

// htmlFamilyNS are the three namespaces HTML 5 treats as "in the document" and
// that the xhtml output method at html-version 5 writes without a prefix (see
// the prefix-normalization block in serializer.element).
var htmlFamilyNS = map[string]bool{
	"http://www.w3.org/1999/xhtml":       true,
	"http://www.w3.org/2000/svg":         true,
	"http://www.w3.org/1998/Math/MathML": true,
}

var htmlVoidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
	// HTML4-only void elements, dropped from HTML5's own void list but still
	// void-empty under this serializer's (HTML5-agnostic, html-version
	// unset) default treatment (output-0116/0116a/0116b, result-document-
	// 0278/0280/0282/0283).
	"basefont": true, "frame": true, "isindex": true,
}

// htmlBooleanAttrs are the HTML4-recognized boolean attributes (attribute-0701):
// the html output method serializes one of these as a bare `name` rather than
// `name="value"` when its value equals the attribute's own name (case-
// insensitively) — per XSLT's HTML output method rules, §16.2.
var htmlBooleanAttrs = map[string]bool{
	"checked": true, "compact": true, "declare": true, "defer": true,
	"disabled": true, "ismap": true, "multiple": true, "noresize": true,
	"noshade": true, "nowrap": true, "readonly": true, "selected": true,
}

// htmlURIAttrs is the well-known table of HTML attributes whose value is a
// URI: the html/xhtml output methods percent-escape non-ASCII (and other
// unsafe) bytes in these — but only these — attribute values by default
// (escape-uri-attributes, XSLT §21.2; attribute-0301: <a href> is escaped,
// <input value>/<textarea> content is not). Keyed by lower-cased element then
// attribute local name.
var htmlURIAttrs = map[string]map[string]bool{
	"a":          {"href": true},
	"applet":     {"codebase": true},
	"area":       {"href": true},
	"base":       {"href": true},
	"blockquote": {"cite": true},
	"body":       {"background": true},
	"button":     {"formaction": true},
	"del":        {"cite": true},
	"form":       {"action": true},
	"frame":      {"longdesc": true, "src": true},
	"head":       {"profile": true},
	"iframe":     {"longdesc": true, "src": true},
	"img":        {"longdesc": true, "src": true, "usemap": true, "lowsrc": true, "dynsrc": true},
	"input":      {"src": true, "usemap": true, "formaction": true},
	"ins":        {"cite": true},
	"link":       {"href": true},
	"object":     {"classid": true, "codebase": true, "data": true, "usemap": true},
	"q":          {"cite": true},
	"script":     {"src": true},
	"audio":      {"src": true},
	"video":      {"src": true, "poster": true},
	"source":     {"src": true},
	"track":      {"src": true},
	"table":      {"background": true},
	"tbody":      {"background": true},
	"td":         {"background": true},
	"tfoot":      {"background": true},
	"th":         {"background": true},
	"thead":      {"background": true},
	"tr":         {"background": true},
	"xmp":        {"href": true},
}

// escapeHTMLURIAttr percent-encodes every byte of s outside the printable
// ASCII range 32..126 inclusive — the same algorithm as fn:escape-html-uri —
// leaving an already-percent-escaped "%XX" sequence untouched (each of its
// three bytes is itself printable ASCII). s is first NFC-normalized
// (output-0101/0164: a decomposed "å" (a + combining ring) must percent-encode
// to the same %C3%A5 as its precomposed form, not %61%CC%8A).
func escapeHTMLURIAttr(s string) string {
	s = norm.NFC.String(s)
	needsEscape := false
	for i := 0; i < len(s); i++ {
		if s[i] < 32 || s[i] > 126 {
			needsEscape = true
			break
		}
	}
	if !needsEscape {
		return s
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
	return b.String()
}

// Serialize renders a node (document, element, or synthetic result-tree root)
// to a string according to opts.
func Serialize(n *Node, opts SerializeOptions) string {
	var b strings.Builder
	writeDoc(&b, n, opts)
	return b.String()
}

// WriteTo serializes n to w exactly as Serialize does, but incrementally \u2014
// nothing larger than the write buffer is ever held. It is the entry point for
// a caller that streams a large result somewhere (a file, a socket) instead of
// wanting it back as one string. The only difference from Serialize is that a
// write failure is reported rather than impossible.
func WriteTo(w io.Writer, n *Node, opts SerializeOptions) error {
	bw := bufio.NewWriterSize(w, chunkBufSize)
	writeDoc(bw, n, opts)
	return bw.Flush()
}

// chunkBufSize is the write-buffer size for WriteTo and ChunkWriter: big
// enough that per-node writes amortize away, small enough to stay a constant
// next to the tree being drained.
const chunkBufSize = 32 << 10

// xmlNS is permanently bound to the "xml" prefix: pre-seeding it in a
// serializer's scope maps means resolvePrefix treats it as already in scope
// instead of emitting xmlns:xml="..." the first time an xml:lang/xml:space
// attribute is serialized (output-0104).
const xmlNS = "http://www.w3.org/XML/1998/namespace"

func newSerializer(out outWriter, opts SerializeOptions) *serializer {
	return &serializer{b: out, opts: opts,
		declared: map[string]string{xmlNS: "xml"},
		prefixes: map[string]string{"xml": xmlNS}}
}

// writeXMLDeclaration writes the XML declaration the xml/xhtml/adaptive
// methods prepend unless omit-xml-declaration is set. The adaptive method
// serializes a node exactly as the xml method does, declaration included
// (output-0721).
func writeXMLDeclaration(out outWriter, opts SerializeOptions) {
	if opts.OmitXMLDeclaration {
		return
	}
	if opts.Method != "xml" && opts.Method != "xhtml" && opts.Method != "adaptive" {
		return
	}
	enc := opts.Encoding
	if enc == "" {
		enc = "UTF-8"
	}
	xmlVer := "1.0"
	if strings.TrimSpace(opts.XMLVersion) == "1.1" {
		xmlVer = "1.1"
	}
	decl := `<?xml version="` + xmlVer + `" encoding="` + enc + `"`
	if opts.Standalone != "" {
		decl += ` standalone="` + opts.Standalone + `"`
	}
	out.WriteString(decl + "?>")
}

// writeDoc is Serialize's body, writing through out instead of returning a
// string.
func writeDoc(out outWriter, n *Node, opts SerializeOptions) {
	if opts.Method == "" {
		opts.Method = "xml"
	}

	if opts.Method == "text" {
		// The character map and @normalization-form apply to the WHOLE text
		// result at once (BOM included, as it always has been), so they need
		// it buffered; without either, every piece goes straight out.
		dst, post := out, len(opts.CharacterMap) > 0 || opts.NormalizationForm != ""
		var buf strings.Builder
		if post {
			dst = &buf
		}
		if opts.ByteOrderMark {
			dst.WriteString("\ufeff")
		}
		if opts.SeparateItems {
			for i, c := range topLevel(n) {
				if i > 0 {
					dst.WriteString(opts.ItemSeparator)
				}
				writeTextValue(dst, c)
			}
		} else {
			writeTextValue(dst, n)
		}
		if post {
			out.WriteString((&serializer{opts: opts}).mapChars(buf.String(), nil))
		}
		return
	}

	if opts.ByteOrderMark {
		out.WriteString("\ufeff")
	}
	writeXMLDeclaration(out, opts)
	s := newSerializer(out, opts)
	children := topLevel(n)
	needDoctype := (opts.Method == "xml" || opts.Method == "xhtml") && opts.DoctypeSystem != ""
	// html-version >= 5 (html or xhtml method) auto-generates <!DOCTYPE html>
	// immediately before the root element — but only when that root element is
	// actually html: named "html" (output-0213: a non-html root gets no
	// DOCTYPE), and for the xhtml method only when it's genuinely in the XHTML
	// namespace (output-0214/0215: an "alien" namespace — even one still
	// locally called html/z:html — gets no DOCTYPE either). Only an explicit
	// doctype-system/-public takes priority over this. The keyword echoes the
	// root element's own name/case (output-0208/0209/0210 use lower/upper/
	// mixed-case <html> and expect the DOCTYPE to match).
	needHTML5Doctype := !needDoctype && (opts.Method == "html" || opts.Method == "xhtml") && htmlVersion5(opts.HTMLVersion)
	for ci, c := range children {
		if opts.SeparateItems && ci > 0 {
			out.WriteString(opts.ItemSeparator)
		}
		if needDoctype && c.Kind == KindElement {
			out.WriteString(doctypeDecl(c.Name.Local, opts.DoctypePublic, opts.DoctypeSystem))
			needDoctype = false
		}
		if needHTML5Doctype && c.Kind == KindElement {
			isHTMLRoot := strings.EqualFold(c.Name.Local, "html") &&
				(opts.Method == "html" || c.Name.Space == "http://www.w3.org/1999/xhtml")
			if isHTMLRoot {
				out.WriteString("<!DOCTYPE " + c.Name.Local + ">")
			}
			needHTML5Doctype = false
		}
		s.node(c, 0)
	}
}

// topLevel returns the nodes to serialize: children of a document/result root,
// or the node itself if it is an element.
func topLevel(n *Node) []*Node {
	if n.Kind == KindDocument || (n.Kind == KindElement && n.Name.Local == "" && n.Name.Space == "") {
		return n.Children
	}
	return []*Node{n}
}

type serializer struct {
	b        outWriter
	opts     SerializeOptions
	declared map[string]string // uri -> prefix currently in scope
	prefixes map[string]string // prefix -> uri currently in scope
	suppress bool              // inside a suppress-indentation element
}

func (s *serializer) node(n *Node, depth int) {
	if nd, ok := n.RealItem.(*Node); ok {
		n = nd // a node carried by reference (see RealItem)
	}
	switch n.Kind {
	case KindElement:
		s.element(n, depth)
	case KindText:
		// disable-output-escaping skips markup escaping, but the HTML/XHTML
		// no-break-space mapping is a character encoding choice that still applies.
		var v string
		if !n.Raw && n.Parent != nil && nameIn(s.opts.CDATAElements, n.Parent.Name) {
			// cdata-section-elements: the text goes out unescaped inside a
			// CDATA section (never through the character map — output-0115c),
			// split around any "]]>" it contains and, per @normalization-form
			// and the output encoding's repertoire, around any character a
			// CDATA section cannot carry literally either (output-0115b/d/e).
			s.writeCDATA(n.Value)
			return
		}
		// The html output method (not xhtml) treats script/style content as
		// CDATA: markup characters are never escaped there — any escaping
		// needed (e.g. a literal "</script>") is the stylesheet author's own
		// responsibility (output-0154/0159).
		rawScriptStyle := s.opts.Method == "html" && n.Parent != nil && n.Parent.Name.Space == "" &&
			(strings.EqualFold(n.Parent.Name.Local, "script") || strings.EqualFold(n.Parent.Name.Local, "style"))
		if n.Raw || rawScriptStyle {
			v = s.mapChars(n.Value, nil)
		} else if s.opts.Method == "html" {
			v = s.mapChars(n.Value, escapeText)
		} else {
			v = s.mapChars(n.Value, escapeTextXML)
		}
		v = s.escapeRepertoire(v)
		if s.xmlVersion11() {
			v = escapeControls(v)
		}
		if s.opts.Method == "html" || s.opts.Method == "xhtml" {
			v = strings.ReplaceAll(v, "\u00a0", "&nbsp;")
			v = escapeC1(v)
		}
		s.b.WriteString(v)
	case KindComment:
		s.b.WriteString("<!--" + n.Value + "-->")
	case KindPI:
		s.b.WriteString("<?" + n.Name.Local + " " + n.Value + "?>")
	case KindDocument:
		for _, c := range n.Children {
			s.node(c, depth)
		}
	}
}

// nsSave records the previous state of the serializer's namespace maps so a
// subtree's declarations can be undone when the element closes.
type nsSave struct {
	uri, prevPfx string
	hadURI       bool
	pfx, prevURI string
	hadPfx       bool
}

// declare binds pfx->uri for the current subtree, returning the declaration
// attribute text.
func (s *serializer) declare(pfx, uri string, saves *[]nsSave) string {
	sv := nsSave{uri: uri, pfx: pfx}
	sv.prevPfx, sv.hadURI = s.declared[uri]
	sv.prevURI, sv.hadPfx = s.prefixes[pfx]
	*saves = append(*saves, sv)
	s.declared[uri] = pfx
	s.prefixes[pfx] = uri
	if pfx == "" {
		return ` xmlns="` + escapeAttr(uri) + `"`
	}
	return ` xmlns:` + pfx + `="` + escapeAttr(uri) + `"`
}

// resolvePrefix picks the serialization prefix for a name in a namespace,
// declaring the binding if needed. Conflicting preferred prefixes get a
// numeric suffix (ns -> ns_0).
func (s *serializer) resolvePrefix(name Name, attr bool, saves *[]nsSave, decls *strings.Builder) string {
	uri := name.Space
	if uri == "" {
		return ""
	}
	// The declared[uri] cache remembers the FIRST prefix uri was bound to,
	// but a later sibling/descendant can shadow that binding (e.g. rebind the
	// default namespace to "" or to a different URI) without ever un-caching
	// it here — so the cached prefix must still be the AMBIENT one before
	// it's reused, or a namespace once redeclared away from a URI would
	// never be redeclared back to it for a later element that needs it
	// (namespace-3132: default ns "http://x/" on the grandparent, "" on the
	// parent, "http://x/" again on the child).
	// A node keeps the prefix its name was created with (XDM names carry
	// one; namespace fixup only ever ADDS the binding it needs): a copied
	// qri:QuoteRef stays qri:QuoteRef even under a parent that binds the
	// same URI as its default namespace (copy-4901/5201), so the node's own
	// prefix wins over a cached default-prefix binding to that URI.
	if name.Prefix != "" {
		if cur, ok := s.prefixes[name.Prefix]; ok && cur == uri {
			return name.Prefix
		}
	}
	if pfx, ok := s.declared[uri]; ok && !(attr && pfx == "") && s.prefixes[pfx] == uri && !(pfx == "" && name.Prefix != "") {
		return pfx
	}
	pfx := name.Prefix
	if pfx == "" && attr {
		pfx = "ns" // attributes in a namespace always need a prefix
	}
	// The default (empty) prefix has no numbered fallback: unlike a named
	// prefix, it can never collide with another binding's MEANING — it is
	// simply reasserted to a new URI at this scope (that is what "shadowing"
	// the default namespace is). Only a genuinely named prefix bound to a
	// conflicting URI (or an attribute forced off the empty prefix above)
	// needs the ns_0/ns_1 numbering.
	if pfx != "" {
		if cur, taken := s.prefixes[pfx]; taken && cur != uri {
			base := pfx
			for i := 0; ; i++ {
				cand := fmt.Sprintf("%s_%d", base, i)
				if cur, taken := s.prefixes[cand]; !taken || cur == uri {
					pfx = cand
					break
				}
			}
		}
	}
	decls.WriteString(s.declare(pfx, uri, saves))
	return pfx
}

// elemOpen is what startTag hands back so the element can be closed later.
// serializer.element closes it immediately; ChunkWriter keeps it on a stack and
// closes it once the element's last child has arrived, possibly many flushes
// later — which is the whole reason the start tag is written by its own method.
type elemOpen struct {
	qname string
	meta  string
	saves []nsSave
	// indent is whether the END tag gets its own indented line.
	indent bool
	// suppress records that this element turned suppress-indentation ON, so
	// the closer knows to turn it back off.
	suppress bool
}

// startTag writes an element's leading indentation and its start tag up to
// (but not including) the ">" — namespace declarations and attributes
// included. Everything after that depends on the children, which the caller
// owns.
func (s *serializer) startTag(n *Node, depth int) elemOpen {
	html := s.opts.Method == "html"
	xhtml := s.opts.Method == "xhtml"
	htmlish := html || xhtml
	// An element NAMED in suppress-indentation must itself lose its own
	// closing tag's indentation too, not just its children's — s.suppress
	// only turns on further below, after `indent` used to be computed, so
	// the element that TRIGGERS suppression was, wrongly, still judged by
	// its PARENT's (not-yet-suppressed) state (fn-transform-66: <my:b> has
	// an element child and was still indenting its own </my:b>).
	triggersSuppress := !s.suppress && nameIn(s.opts.SuppressIndentation, n.Name)
	indent := s.opts.Indent && !s.suppress && !triggersSuppress && hasElementChildren(n)

	if s.opts.Indent && !s.suppress && depth > 0 {
		s.b.WriteString("\n" + strings.Repeat("  ", depth))
	}
	suppressed := false
	if triggersSuppress {
		s.suppress = true
		suppressed = true
	}
	// include-content-type: an HTML/XHTML head gets the Content-Type meta
	// first. For the xhtml method this only applies to a genuinely
	// XHTML-namespaced head (output-0214/0215: an "alien" namespace root gets
	// no meta, matching the DOCTYPE rule above).
	meta := ""
	isHeadNS := html || n.Name.Space == "http://www.w3.org/1999/xhtml"
	headWantsContentType := htmlish && s.opts.IncludeContentType && strings.EqualFold(n.Name.Local, "head") && isHeadNS
	if headWantsContentType {
		hasExisting := false
		for _, c := range n.Children {
			if c.Kind == KindElement && isContentTypeMeta(c) {
				hasExisting = true
				break
			}
		}
		if !hasExisting {
			meta = s.contentTypeMeta()
		}
	}
	// A pre-existing <meta http-equiv="Content-Type"> child of such a head
	// (output-0143/0144/0157/0158) isn't duplicated above; instead its own
	// "content" attribute is (re)written with the correct value below.
	fixContentTypeAttr := htmlish && s.opts.IncludeContentType && n.Parent != nil &&
		strings.EqualFold(n.Parent.Name.Local, "head") &&
		(html || n.Parent.Name.Space == "http://www.w3.org/1999/xhtml") &&
		isContentTypeMeta(n)

	var saves []nsSave
	var decls strings.Builder

	// XHTML 5 "prefix normalization" (Serialization 3.1 §7.2): under the xhtml
	// output method at html-version 5, an element in the XHTML, SVG or MathML
	// namespace is written with NO prefix — the namespace becomes the default
	// namespace at that point — and every namespace NODE binding a prefix to
	// one of those three namespaces is removed from the output
	// (output-0211/0221/0225/0226 all assert no "h:" / "s:" / "m:" survives).
	// The html output method at html-version 5 normalizes the same way: the
	// suite treats its results as definitive where the spec's HTML wording is
	// unclear (output-0602a's own description; 0602b/0603a likewise).
	normalizePfx := htmlish && htmlVersion5(s.opts.HTMLVersion)

	// Explicitly-declared namespace nodes (e.g. from xsl:namespace or copied
	// namespaces) own their prefixes; declare them first.
	for _, ns := range n.NS {
		if normalizePfx && ns.Name.Local != "" && htmlFamilyNS[ns.Value] {
			continue // prefixed binding for XHTML/SVG/MathML: removed
		}
		if ns.Value == "" {
			// A namespace UNDECLARATION (xmlns:p=""). Only XML 1.1 can
			// express one, and only when undeclare-prefixes is on; at 1.0 it
			// is silently dropped, which is what the serialization spec
			// prescribes (xml-version-027/028 vs 026/035).
			if ns.Name.Local != "" && s.opts.UndeclarePrefixes && s.xmlVersion11() {
				if cur, ok := s.prefixes[ns.Name.Local]; ok && cur != "" {
					decls.WriteString(s.declare(ns.Name.Local, "", &saves))
				}
			}
			continue
		}
		if ns.Name.Local == "xml" {
			// The xml: prefix is permanently bound to the XML namespace and
			// must never be declared explicitly (output-0104).
			continue
		}
		if cur, ok := s.prefixes[ns.Name.Local]; ok && cur == ns.Value {
			continue // already in scope
		}
		decls.WriteString(s.declare(ns.Name.Local, ns.Value, &saves))
	}

	// inherit-namespaces="no" (NSBarrier): this element does not inherit its
	// ancestors' namespace declarations, so a DEFAULT namespace that is in
	// scope in the serialized output but not in this element's own scope has
	// to be undeclared right here. Leaving it to resurface on the first
	// no-namespace descendant (which is where the "a no-namespace element
	// undeclares the default" rule below would otherwise catch it) puts the
	// xmlns="" in the wrong place and, worse, leaves the default prefix in
	// scope on this element — visible to in-scope-prefixes() when the result
	// is read back (namespace-0913/0914). Only the default namespace is
	// handled: undeclaring a PREFIXED namespace needs XML 1.1 syntax, which
	// the serializer emits only under undeclare-prefixes.
	//
	// It is skipped whenever the element's own NAME will settle the default
	// prefix anyway: a no-namespace name undeclares it just below, and a name
	// written without a prefix binds it to that element's namespace — emitting
	// xmlns="" first would then produce two xmlns attributes on one element
	// (element-0306).
	if n.NSBarrier && n.Name.Space != "" && n.Name.Prefix != "" {
		ownDefault := ""
		for _, ns := range n.NS {
			if ns.Name.Local == "" {
				ownDefault = ns.Value
			}
		}
		if ownDefault == "" {
			if cur, ok := s.prefixes[""]; ok && cur != "" {
				decls.WriteString(s.declare("", "", &saves))
			}
		}
	}

	// Element name.
	var qname string
	if n.Name.Space == "" {
		qname = n.Name.Local
		// A default namespace in scope must be undeclared for a no-namespace
		// element.
		if cur, ok := s.prefixes[""]; ok && cur != "" {
			decls.WriteString(s.declare("", "", &saves))
		}
	} else if normalizePfx && htmlFamilyNS[n.Name.Space] {
		// Prefix normalization: the name is written bare and its namespace
		// becomes the default one here (declared only when it is not already
		// the default in scope, so a run of sibling XHTML elements declares it
		// just once, on their common ancestor).
		qname = n.Name.Local
		if cur, ok := s.prefixes[""]; !ok || cur != n.Name.Space {
			decls.WriteString(s.declare("", n.Name.Space, &saves))
		}
	} else {
		pfx := s.resolvePrefix(n.Name, false, &saves, &decls)
		if pfx == "" {
			qname = n.Name.Local
		} else {
			qname = pfx + ":" + n.Name.Local
		}
	}

	// Attributes (namespaced attributes may add declarations).
	attrEsc := escapeAttr
	if !html {
		attrEsc = escapeAttrXML
	}
	var attrs strings.Builder
	for _, a := range n.Attrs {
		if fixContentTypeAttr && a.Name.Space == "" && strings.EqualFold(a.Name.Local, "content") {
			// Dropped so the correct value can be appended once below, instead
			// of keeping whatever (possibly stale/bogus) value was there.
			continue
		}
		aq := a.Name.Local
		if a.Name.Space != "" {
			pfx := s.resolvePrefix(a.Name, true, &saves, &decls)
			aq = pfx + ":" + a.Name.Local
		}
		if html && a.Name.Space == "" && htmlBooleanAttrs[strings.ToLower(a.Name.Local)] &&
			strings.EqualFold(a.Value, a.Name.Local) {
			// HTML boolean attribute (attribute-0701): serialize as a bare
			// name, not name="value".
			attrs.WriteString(" " + aq)
			continue
		}
		val := a.Value
		isURIAttr := htmlish && !s.opts.NoEscapeURIAttributes && a.Name.Space == "" &&
			htmlURIAttrs[strings.ToLower(n.Name.Local)][strings.ToLower(a.Name.Local)]
		var out string
		if isURIAttr {
			// A URI-valued HTML/XHTML attribute is percent-escaped instead of
			// going through the character map (character-map-009: a character
			// map must not rewrite characters inside an already-escaped URI).
			out = attrEsc(escapeHTMLURIAttr(val))
		} else {
			out = s.mapChars(val, attrEsc)
		}
		out = s.escapeRepertoire(out)
		if s.xmlVersion11() {
			out = escapeControls(out)
		}
		if htmlish {
			out = escapeC1(out)
		}
		attrs.WriteString(" " + aq + `="` + out + `"`)
	}
	if fixContentTypeAttr {
		attrs.WriteString(` content="` + s.mapChars(s.contentTypeValue(), escapeAttr) + `"`)
	}

	s.b.WriteString("<" + qname + decls.String() + attrs.String())
	return elemOpen{qname: qname, meta: meta, saves: saves, indent: indent, suppress: suppressed}
}

// closeTag writes an element's end tag and unwinds the scope startTag opened.
func (s *serializer) closeTag(o elemOpen, depth int) {
	if o.indent {
		s.b.WriteString("\n" + strings.Repeat("  ", depth))
	}
	s.b.WriteString("</" + o.qname + ">")
	s.undeclare(o.saves)
	if o.suppress {
		s.suppress = false
	}
}

func (s *serializer) element(n *Node, depth int) {
	o := s.startTag(n, depth)

	if len(n.Children) == 0 {
		htmlish := s.opts.Method == "html" || s.opts.Method == "xhtml"
		switch {
		case o.meta != "":
			s.b.WriteString(">" + o.meta + "</" + o.qname + ">")
		case htmlish && htmlVoidElements[strings.ToLower(n.Name.Local)]:
			// HTML void element: <br>; XHTML keeps the XML self-close, with the
			// HTML4-compatibility space before the slash: <br />
			// (output-0107/0116/0132..0135).
			if s.opts.Method == "xhtml" {
				s.b.WriteString(" />")
			} else {
				s.b.WriteString(">")
			}
		case htmlish:
			// Non-void empty element must use a separate end tag (<p></p>).
			s.b.WriteString("></" + o.qname + ">")
		default:
			s.b.WriteString("/>")
		}
		s.undeclare(o.saves)
		if o.suppress {
			s.suppress = false
		}
		return
	}

	s.b.WriteString(">" + o.meta)
	for _, c := range n.Children {
		s.node(c, depth+1)
	}
	s.closeTag(o, depth)
}

// isContentTypeMeta reports whether n is an html/xhtml <meta
// http-equiv="Content-Type"> element (the one include-content-type manages),
// regardless of what other attributes — even a bogus one — it also carries.
func isContentTypeMeta(n *Node) bool {
	if !strings.EqualFold(n.Name.Local, "meta") {
		return false
	}
	for _, a := range n.Attrs {
		if a.Name.Space == "" && strings.EqualFold(a.Name.Local, "http-equiv") &&
			strings.EqualFold(strings.TrimSpace(a.Value), "content-type") {
			return true
		}
	}
	return false
}

// contentTypeValue is the include-content-type meta's content value:
// "<media-type>; charset=<encoding>", defaulting to "text/html; charset=UTF-8".
func (s *serializer) contentTypeValue() string {
	mt := s.opts.MediaType
	if mt == "" {
		mt = "text/html"
	}
	enc := s.opts.Encoding
	if enc == "" {
		enc = "UTF-8"
	}
	return mt + "; charset=" + enc
}

// contentTypeMeta builds the auto-generated Content-Type <meta> for the
// html/xhtml methods' include-content-type serialization parameter,
// self-closed for xhtml (XML syntax) and left as a bare start tag for html.
func (s *serializer) contentTypeMeta() string {
	tag := `<meta http-equiv="Content-Type" content="` + s.mapChars(s.contentTypeValue(), escapeAttr) + `"`
	if s.opts.Method == "xhtml" {
		return tag + " />"
	}
	return tag + ">"
}

func (s *serializer) undeclare(saves []nsSave) {
	// Restore in reverse order so nested re-declarations unwind correctly.
	for i := len(saves) - 1; i >= 0; i-- {
		sv := saves[i]
		if sv.hadURI {
			s.declared[sv.uri] = sv.prevPfx
		} else {
			delete(s.declared, sv.uri)
		}
		if sv.hadPfx {
			s.prefixes[sv.pfx] = sv.prevURI
		} else {
			delete(s.prefixes, sv.pfx)
		}
	}
}

func hasElementChildren(n *Node) bool {
	for _, c := range n.Children {
		if c.Kind == KindElement {
			return true
		}
	}
	return false
}

func writeTextValue(b outWriter, n *Node) {
	switch n.Kind {
	case KindText:
		b.WriteString(n.Value)
	case KindElement, KindDocument:
		for _, c := range n.Children {
			writeTextValue(b, c)
		}
	}
}

// mapChars applies the character map to v: mapped characters emit their
// replacement string raw; the runs between them go through esc (nil = raw),
// after Unicode normalization (opts.NormalizationForm) — normalization never
// touches a character map's own raw replacement text.
func (s *serializer) mapChars(v string, esc func(string) string) string {
	if esc == nil {
		esc = func(x string) string { return x }
	}
	normEsc := func(x string) string { return esc(s.normalize(x)) }
	cmap := s.opts.CharacterMap
	if len(cmap) == 0 {
		return normEsc(v)
	}
	var b, run strings.Builder
	flush := func() {
		if run.Len() > 0 {
			b.WriteString(normEsc(run.String()))
			run.Reset()
		}
	}
	for _, r := range v {
		if rep, ok := cmap[r]; ok {
			flush()
			b.WriteString(rep)
		} else {
			run.WriteRune(r)
		}
	}
	flush()
	return b.String()
}

// writeCDATA writes v as one or more CDATA sections, splitting around any
// "]]>" it contains — and, per the declared output encoding's repertoire,
// around any character a CDATA section cannot carry literally (a character
// reference has no meaning inside "<![CDATA[...]]>", so such a character
// must break out into raw text between two CDATA sections instead;
// output-0115b/d/e). Character maps never apply inside a CDATA section
// (output-0115c); normalization (@normalization-form) does.
func (s *serializer) writeCDATA(v string) {
	v = s.normalize(v)
	limit := encodingLimit(s.opts.Encoding)
	if limit == 0 {
		s.b.WriteString("<![CDATA[" + strings.ReplaceAll(v, "]]>", "]]]]><![CDATA[>") + "]]>")
		return
	}
	var run strings.Builder
	flush := func() {
		if run.Len() > 0 {
			s.b.WriteString("<![CDATA[" + strings.ReplaceAll(run.String(), "]]>", "]]]]><![CDATA[>") + "]]>")
			run.Reset()
		}
	}
	for _, r := range v {
		if r > limit {
			flush()
			fmt.Fprintf(s.b, "&#%d;", r)
		} else {
			run.WriteRune(r)
		}
	}
	flush()
}

// normalize applies opts.NormalizationForm (if any) to v.
func (s *serializer) normalize(v string) string {
	switch s.opts.NormalizationForm {
	case "NFC":
		return norm.NFC.String(v)
	case "NFD":
		return norm.NFD.String(v)
	case "NFKC":
		return norm.NFKC.String(v)
	case "NFKD":
		return norm.NFKD.String(v)
	}
	return v
}

// encodingLimit returns the highest Unicode code point representable
// literally by the named output encoding, or 0 if the repertoire isn't
// restricted (UTF-8/UTF-16 family, or any encoding this serializer doesn't
// specifically recognize — it always emits UTF-8 bytes regardless of the
// declared @encoding, so an unrecognized name is treated as unrestricted
// rather than guessed at).
func encodingLimit(enc string) rune {
	switch strings.ToUpper(strings.TrimSpace(enc)) {
	case "ISO-8859-1", "ISO8859-1", "LATIN1", "LATIN-1", "L1":
		return 0xFF
	case "US-ASCII", "ASCII":
		return 0x7F
	}
	return 0
}

// escapeRepertoire replaces any character outside the declared output
// encoding's repertoire with a decimal numeric character reference
// (character-map-010: encoding="iso-8859-1" can't carry a supplementary-plane
// character literally, so it must fall back to &#100000;-style escaping).
func (s *serializer) escapeRepertoire(v string) string {
	limit := encodingLimit(s.opts.Encoding)
	if limit == 0 {
		return v
	}
	hasOut := false
	for _, r := range v {
		if r > limit {
			hasOut = true
			break
		}
	}
	if !hasOut {
		return v
	}
	var b strings.Builder
	for _, r := range v {
		if r > limit {
			fmt.Fprintf(&b, "&#%d;", r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escapeC1 replaces each C1 control character (U+0080-U+009F) with its
// decimal numeric character reference. The html/xhtml output methods must
// not emit these raw — a raw C1 byte is exactly the kind of thing that gets
// silently mis-decoded by legacy single-byte encodings/tools consuming HTML
// (output-0102e/0103b/0103c/0103e).
func escapeC1(s string) string {
	hasC1 := false
	for _, r := range s {
		if r >= 0x80 && r <= 0x9F {
			hasC1 = true
			break
		}
	}
	if !hasC1 {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if r >= 0x80 && r <= 0x9F {
			fmt.Fprintf(&b, "&#%d;", r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func escapeText(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// escapeTextXML is escapeText plus a character reference for CR: under the
// xml output method a literal CR in text would be line-end-normalized away
// on re-parse (Serialization 3.1 §6.1.4; xml-version-002/020 round-trip
// &#13; through a text node).
func escapeTextXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\r", "&#13;")
	return r.Replace(s)
}

// escapeAttr escapes an html-method attribute value. The quote is a numeric
// reference (&#34;) rather than the named &quot; entity — matching the
// reference processor these tests were written against (output-0102c/0103c
// both require &#34;/&#x22;, never &quot;, and nothing in the suite expects
// &quot; either, so this is a safe, not just test-fitted, choice).
func escapeAttr(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;")
	return r.Replace(s)
}

// escapeAttrXML is escapeAttr plus tab/LF/CR character references
// (attribute-1101): under XML(/XHTML) rules a literal one of these in an
// attribute value would attribute-value-normalize to an ordinary space on
// re-parse, silently losing which whitespace character was actually there.
// The html output method has no such normalization step, so it keeps using
// plain escapeAttr.
func escapeAttrXML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;",
		"\t", "&#9;", "\n", "&#10;", "\r", "&#13;",
	)
	return r.Replace(s)
}

// ---------------------------------------------------------------------------
// incremental (bounded-memory) serialization
// ---------------------------------------------------------------------------

// ChunkWriter serializes a result tree WHILE IT IS STILL BEING BUILT, writing
// out every subtree that is provably finished and detaching it from the tree,
// so a transformation that produces a huge result never holds more than the
// part still under construction.
//
// THE INVARIANT IT RESTS ON. The XSLT engine builds a result tree strictly
// depth-first, and appends a constructed element to its parent BEFORE filling
// it (see execLitElement). So at any moment the only nodes that can still grow
// are those on the RIGHTMOST PATH — root, its last child, that node's last
// child, and so on. Everything to the left of that path is final. Sync walks
// the rightmost path, emits and drops each level's non-last children, and
// commits a start tag for each level it descends through.
//
// WHEN IT MAY BE CALLED. Only at a QUIESCENT point — between two instructions,
// never in the middle of one. Detaching a written subtree renumbers its former
// siblings, and an instruction that has appended several nodes and is about to
// revisit them by index (execCopyOf's namespace fix-up is the one that does)
// would then read the wrong ones. Its caller, not this type, owns that timing.
//
// WHY COMMITTING A START TAG IS SAFE. Descending into an element means writing
// its start tag, which fixes its name, namespaces and attributes forever. Sync
// therefore descends only into an element that ALREADY HAS A CHILD: XSLT
// forbids adding an attribute or namespace node to an element after content has
// been added to it (XTDE0410), so an element with children can no longer change
// its start tag. An element that is still empty is simply left alone until its
// first child arrives.
//
// WHAT IT DOES NOT DO. It handles the xml and text output methods only, and
// only for option sets whose serialization does not depend on the whole result
// being present (see CanChunk). Everything else keeps using Serialize/WriteTo,
// which are unaffected by any of this.
type ChunkWriter struct {
	w    *bufio.Writer
	s    *serializer
	opts SerializeOptions
	root *Node
	// text is the text output method, which writes no markup at all: there is
	// nothing to commit and nothing to close, only string values to emit.
	text  bool
	began bool
	// open is the chain of elements whose start tag has been written, outermost
	// first. It is exactly the committed prefix of the rightmost path.
	open  []chunkOpen
	err   error
	since int
	// Every is how many Tick calls may pass before the next flush. It trades
	// flush overhead against retained nodes; zero means the default.
	Every int
	// Validate, when set, is called with each node about to be written. It lets
	// the caller apply the whole-result serialization checks (SERE0006) that it
	// can no longer run at the end, because by then the tree is gone. A node may
	// be presented more than once (an element at commit time and each of its
	// children later), so the check must be idempotent.
	Validate func(*Node) error
}

type chunkOpen struct {
	n     *Node
	o     elemOpen
	depth int
}

// defaultChunkEvery is how many Ticks trigger a flush. Small enough that
// retention stays flat, large enough that the rightmost-path walk amortizes to
// nothing.
const defaultChunkEvery = 64

// CanChunk reports whether opts can be served incrementally. Everything it
// rejects needs the complete result before the first byte can be written: an
// item separator and indentation both depend on what comes next, a doctype or
// standalone declaration on the top-level element count, and the html/xhtml
// methods on children an element does not have yet (the Content-Type <meta>
// scan). A character map or @normalization-form is fine for xml (both apply per
// text node) but not for text, where they apply to the whole result at once.
func CanChunk(opts SerializeOptions) bool {
	switch opts.Method {
	case "xml":
	case "text":
		if len(opts.CharacterMap) > 0 || opts.NormalizationForm != "" {
			return false
		}
	default:
		return false
	}
	return !opts.Indent && !opts.SeparateItems &&
		opts.Standalone == "" && opts.DoctypeSystem == "" && opts.DoctypePublic == ""
}

// NewChunkWriter returns a ChunkWriter feeding w. opts must satisfy CanChunk.
func NewChunkWriter(w io.Writer, opts SerializeOptions) *ChunkWriter {
	if opts.Method == "" {
		opts.Method = "xml"
	}
	bw := bufio.NewWriterSize(w, chunkBufSize)
	c := &ChunkWriter{w: bw, opts: opts, text: opts.Method == "text"}
	if !c.text {
		c.s = newSerializer(bw, opts)
	}
	return c
}

// Attach makes root the tree this writer drains.
func (c *ChunkWriter) Attach(root *Node) { c.root = root }

// Tick is the caller's "this is a quiescent point" signal, to be called as
// often as convenient — it is a counter test, and only every Every-th call does
// any work, so it is cheap enough to sit on a hot dispatch path. See the
// ChunkWriter doc comment for what "quiescent" has to mean.
func (c *ChunkWriter) Tick() {
	c.since++
	every := c.Every
	if every <= 0 {
		every = defaultChunkEvery
	}
	if c.since >= every {
		c.since = 0
		c.Sync()
	}
}

// begin writes the one-off prologue (BOM, XML declaration).
func (c *ChunkWriter) begin() {
	if c.began {
		return
	}
	c.began = true
	if c.opts.ByteOrderMark {
		c.w.WriteString("\ufeff")
	}
	if !c.text {
		writeXMLDeclaration(c.w, c.opts)
	}
}

// emit writes one finished node and reports whether to keep going.
func (c *ChunkWriter) emit(n *Node, depth int) bool {
	if c.Validate != nil {
		if err := c.Validate(n); err != nil {
			c.err = err
			return false
		}
	}
	if c.text {
		writeTextValue(c.w, n)
		return true
	}
	c.s.node(n, depth)
	return true
}

// commit writes the start tag of an element the walk is descending into and
// pushes it on the open chain. Under the text method there is no tag to write,
// but the element still has to be tracked so its children are emitted in order.
func (c *ChunkWriter) commit(n *Node, depth int) bool {
	var o elemOpen
	if !c.text {
		if c.Validate != nil {
			if err := c.Validate(n); err != nil {
				c.err = err
				return false
			}
		}
		o = c.s.startTag(n, depth)
		c.s.b.WriteString(">" + o.meta)
	}
	c.open = append(c.open, chunkOpen{n: n, o: o, depth: depth})
	return true
}

// closeFrom finishes every open element from the innermost one down to index i:
// whatever is still attached to it is emitted, then its end tag is written and
// it is detached from its own parent. Inner elements close first, so by the
// time an outer one is reached its already-closed child is gone from Children
// and the remaining ones are exactly its unwritten content, still in order.
func (c *ChunkWriter) closeFrom(i int) {
	for k := len(c.open) - 1; k >= i; k-- {
		oe := c.open[k]
		for _, ch := range oe.n.Children {
			if !c.emit(ch, oe.depth+1) {
				c.open = c.open[:i]
				return
			}
		}
		oe.n.Children = nil
		if !c.text {
			c.s.closeTag(oe.o, oe.depth)
		}
		detachChild(oe.n)
	}
	c.open = c.open[:i]
}

// detachChild removes n from its parent's child list, which is what actually
// releases a written-out subtree.
func detachChild(n *Node) {
	p := n.Parent
	if p == nil {
		return
	}
	for i, ch := range p.Children {
		if ch == n {
			p.Children = append(p.Children[:i], p.Children[i+1:]...)
			return
		}
	}
}

// Sync writes out and drops everything in the attached tree that can no longer
// change. It is safe to call at any point during construction.
func (c *ChunkWriter) Sync() {
	if c.err != nil || c.root == nil {
		return
	}
	c.begin()

	// Descend the already-committed chain. An open element that has acquired a
	// FOLLOWING SIBLING has in fact finished (nothing can be added to a node
	// the engine has already moved past), so the chain unwinds from there.
	cur, depth := c.root, 0
	for i := 0; i < len(c.open); i++ {
		oe := c.open[i]
		if len(cur.Children) == 0 || cur.Children[0] != oe.n {
			c.err = errors.New("xmltree: incremental serializer lost track of its open element")
			return
		}
		if len(cur.Children) > 1 {
			c.closeFrom(i)
			break
		}
		cur, depth = oe.n, depth+1
	}

	for {
		kids := cur.Children
		if len(kids) == 0 {
			break
		}
		for j := 0; j < len(kids)-1; j++ {
			if !c.emit(kids[j], depth) {
				return
			}
		}
		last := kids[len(kids)-1]
		cur.Children = append(cur.Children[:0], last)
		if last.Kind != KindElement || len(last.Children) == 0 {
			break
		}
		if !c.commit(last, depth) {
			return
		}
		cur, depth = last, depth+1
	}
}

// Finish writes whatever is left, closes every open element and flushes the
// underlying writer. The attached tree is empty afterwards.
func (c *ChunkWriter) Finish() error {
	if c.err == nil && c.root != nil {
		c.begin()
		c.closeFrom(0)
		if c.err == nil {
			for _, ch := range c.root.Children {
				if !c.emit(ch, 0) {
					break
				}
			}
			c.root.Children = nil
		}
	}
	if err := c.w.Flush(); err != nil && c.err == nil {
		c.err = err
	}
	c.root = nil
	return c.err
}

// sortedKeys is a small helper used by tests/debug.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var _ = sortedKeys
