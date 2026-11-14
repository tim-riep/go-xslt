package xmltree

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// This file implements a PULL-style incremental reader over an io.Reader. It
// builds ordinary *Node values — indistinguishable in kind and shape from the
// ones Parse produces, so the XPath evaluator and the XSLT engine work on them
// unmodified — but hands them to the caller one at a time, letting the caller
// keep the ancestor spine plus a single record live and DISCARD everything
// else. Peak memory is then O(depth + largest record) rather than O(document).
// The tokens come from parse.go's own tokenizer, run over a window of the
// input (stream_tok.go); a node's strings are substrings of that window, so
// a live node keeps its window alive — the same bound, since a window is
// read once and dropped records drop theirs.
//
// It deliberately does NOT reuse parseRaw: every one of that function's
// pre-passes (entity expansion, attribute-whitespace normalization, line
// offsets, the DTD tables) reads the WHOLE source string first, which is
// exactly the property a streamed document cannot have. The parts that matter
// for a streamed document are reimplemented incrementally here; the parts that
// cannot be (external parsed entities, XML 1.1 leniency, UTF-16-without-BOM)
// make the reader refuse the document with ErrStreamUnsupported so the caller
// can fall back to a full parse.
//
// NOT reproduced for streamed input, by design:
//   - Line/Col. They feed diagnostics only, and tracking them would mean
//     keeping the byte offsets of a document we are explicitly not holding.
//   - entity base URIs (applyEntityBases): they exist only for external parsed
//     entities, which this reader rejects outright.
//
// All exported names here are prefixed "Stream".

// ErrStreamUnsupported reports a document this reader will not stream. It is
// not a parse failure: the caller is expected to fall back to Parse, which
// handles every one of these cases in full.
var ErrStreamUnsupported = errors.New("xmltree: document cannot be streamed")

// StreamEventKind classifies what Advance just produced.
type StreamEventKind int

const (
	// StreamEOF: the document is exhausted.
	StreamEOF StreamEventKind = iota
	// StreamEnter: an element's start tag. The node carries its name,
	// namespace nodes and attributes, and is already linked into its parent,
	// but has no children yet.
	StreamEnter
	// StreamExit: an element's end tag. The node's subtree is now complete.
	StreamExit
	// StreamLeaf: a completed text, comment or processing-instruction node.
	// Adjacent character data coalesces into one text node exactly as in a
	// full parse, so a second chunk landing in an already-reported text node
	// produces NO further event.
	StreamLeaf
)

// StreamEvent is one step of the incremental walk.
type StreamEvent struct {
	Kind StreamEventKind
	Node *Node
	// Depth counts elements: the document node is 0, the outermost element 1.
	Depth int
}

// StreamReader incrementally builds a document tree from an io.Reader.
//
// The caller drives it with Advance and decides what to keep: Drop detaches a
// completed node from its parent, and SkipSubtree refuses an entire element
// before any of it is built. Neither is done automatically — a caller that
// drops nothing simply ends up with the whole document, exactly as Parse would
// have produced it.
type StreamReader struct {
	tok *streamTokenizer
	// textBytes counts the character data and attribute values read so far,
	// once the DOCTYPE has declared entities: their references are expanded
	// by the tokenizer, so this is how the reader bounds the amplification
	// (expansionLimit of the input read).
	textBytes int
	doc       *Node
	stack     []*Node
	nsStack   []map[string]string
	rawNames  []xml.Name

	// order/tree stamp nodes as they are created, in the same sequence
	// assignOrder would have used for a completed tree. See stamp.
	order int
	tree  uint64

	// DTD-derived tables, populated from the internal subset when the DOCTYPE
	// directive is tokenized (always before the root element, so entity
	// references can never outrun them).
	idKinds      map[string]map[string]uint8
	attrDefaults map[string]map[string]attlistDefault
	elemOnly     map[string]bool

	// nameIntern dedupes element Local/Prefix strings across the stream, the
	// same reasoning as parseRaw's identical table in parse.go.
	nameIntern map[string]string
	// wsRuns shares the whitespace-only text runs (the indentation between
	// elements, most of a pretty-printed document's text nodes), as
	// parseRaw's internText does, so each costs a node and not also a copy
	// of the decoder's buffer. Capped at streamMaxWSRuns distinct runs: a
	// document is not holding this reader's memory hostage with ever new
	// whitespace.
	wsRuns map[string]string

	// incomplete records the nodes Drop has taken children away from. Only
	// live ancestors accumulate here (an entry is removed when the node is
	// itself dropped), so the map is bounded by the spine, not by the
	// document. See Incomplete for what it is for.
	incomplete map[*Node]bool

	// primed holds the first event, read during construction so that a DOCTYPE
	// refusal reaches the caller before it has acted on anything.
	primed   StreamEvent
	hasPrime bool
}

// NewStreamReader starts an incremental parse of r. It returns
// ErrStreamUnsupported for a document whose correct handling needs the whole
// source at once (see the file comment); the caller must then fall back to a
// full parse.
func NewStreamReader(r io.Reader) (*StreamReader, error) {
	return newStreamReaderChunk(r, 0)
}

// newStreamReaderChunk is NewStreamReader reading chunk bytes at a time (0:
// the default); tests use a tiny chunk to put a window boundary inside
// every kind of token.
func newStreamReaderChunk(r io.Reader, chunk int) (*StreamReader, error) {
	src, err := streamDecode(r)
	if err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(src, 64<<10)
	// The prolog is the one place a streamed document can declare something
	// this reader cannot honour. Peeking it costs nothing (it is already in
	// the buffer) and keeps the refusal at construction time, where the caller
	// can still fall back cheaply. The XML declaration's encoding is read
	// here as well: the tokenizer works on a window, which it cannot re-read
	// through a charset decoder the way a whole source is, so the input is
	// decoded ahead of it (encoding/xml did the same through CharsetReader).
	var in io.Reader = br
	if head, _ := br.Peek(4096); len(head) > 0 {
		if declaresXML11(string(head)) {
			return nil, fmt.Errorf("%w: XML 1.1 needs the lenient whole-source pre-passes", ErrStreamUnsupported)
		}
		if enc := declaredEncoding(string(head)); enc != "" {
			dr, err := charsetReader(enc, br)
			if err != nil {
				return nil, fmt.Errorf("xml: opening charset %q: %w", enc, err)
			}
			if dr != nil {
				in = dr
			}
		}
	}
	doc := &Node{Kind: KindDocument}
	s := &StreamReader{
		tok:        newStreamTokenizer(in, chunk),
		doc:        doc,
		stack:      []*Node{doc},
		nsStack:    []map[string]string{{"xml": "http://www.w3.org/XML/1998/namespace", "": ""}},
		tree:       atomic.AddUint64(&treeSeq, 1),
		nameIntern: make(map[string]string),
	}
	s.stamp(doc)
	// Priming. The DOCTYPE is the other place a document can turn out to be
	// unstreamable, and it is only seen once tokenizing starts. Reading the
	// first event here means EVERY ErrStreamUnsupported arrives from this
	// constructor, so a caller can fall back to a full parse without having
	// already acted on a stream it then has to abandon.
	ev, err := s.advance()
	if err != nil {
		return nil, err
	}
	s.primed, s.hasPrime = ev, true
	return s, nil
}

// declaredEncoding is the encoding the XML declaration at the start of head
// names, "" when there is none (or the declaration is cut off).
func declaredEncoding(head string) string {
	head = strings.TrimLeft(head, "\uFEFF \t\r\n")
	if !strings.HasPrefix(head, "<?xml") {
		return ""
	}
	end := strings.Index(head, "?>")
	if end < 0 {
		return ""
	}
	return procInstParam("encoding", head[:end])
}

// streamDecode strips a UTF-8 BOM / transcodes a UTF-16 one, the streaming
// counterpart of decodeBOM: a 0xFF/0xFE first byte kills encoding/xml before
// CharsetReader can ever fire, so it has to be handled ahead of the decoder —
// but, unlike decodeBOM, without reading the document into a string first.
func streamDecode(r io.Reader) (io.Reader, error) {
	br := bufio.NewReaderSize(r, 4<<10)
	head, err := br.Peek(3)
	if err != nil && err != io.EOF && !errors.Is(err, bufio.ErrBufferFull) {
		return nil, err
	}
	switch {
	case bytes.HasPrefix(head, []byte{0xEF, 0xBB, 0xBF}):
		_, _ = br.Discard(3)
		return br, nil
	case bytes.HasPrefix(head, []byte{0xFF, 0xFE}):
		return transform.NewReader(br, unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM).NewDecoder()), nil
	case bytes.HasPrefix(head, []byte{0xFE, 0xFF}):
		return transform.NewReader(br, unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM).NewDecoder()), nil
	}
	return br, nil
}

// Doc returns the document node. It is live: its subtree grows as Advance runs
// and shrinks as the caller drops what it no longer needs.
func (s *StreamReader) Doc() *Node { return s.doc }

// Depth is the current element depth (0 = at document level).
func (s *StreamReader) Depth() int { return len(s.stack) - 1 }

// stamp assigns the next document-order index and this document's tree stamp.
//
// This is not an optimization. A node left at order 0 ties with every other
// such node, which silently breaks three unrelated things at once:
// fn:generate-id (its id is literally "d"+root pointer+"n"+Order, so every
// node of the document would share one id), the XSLT accumulator value cache
// (keyed on Order — every node would collide), and docOrderBefore's structural
// fallback (an O(children) identity scan that, over a tree whose already-
// processed siblings have been dropped, cannot find the node at all).
func (s *StreamReader) stamp(n *Node) {
	SetOrder(n, s.order, s.tree)
	s.order++
}

// Advance reads forward until the next event, or EOF.
func (s *StreamReader) Advance() (StreamEvent, error) {
	if s.hasPrime {
		s.hasPrime = false
		return s.primed, nil
	}
	return s.advance()
}

func (s *StreamReader) advance() (StreamEvent, error) {
	for {
		kind, err := s.tok.next()
		if err == io.EOF {
			if len(s.rawNames) != 0 {
				return StreamEvent{}, errors.New("unexpected end of document: unclosed elements")
			}
			return StreamEvent{Kind: StreamEOF}, nil
		}
		if err != nil {
			return StreamEvent{}, err
		}
		// The tokenizer reports names as written (the prefix in Space), as
		// parseRaw has them: the namespace frame stack below does the
		// resolution, and the end tags are matched here.
		tz := &s.tok.tz
		switch kind {
		case tokStart:
			t := xml.StartElement{Name: tz.name, Attr: tz.attrs}
			if err := s.countAttrs(t.Attr); err != nil {
				return StreamEvent{}, err
			}
			// A streamed document is XML 1.0 (1.1 is ErrStreamUnsupported), so
			// a prefix may not be undeclared — parseRaw's identical check.
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" && a.Value == "" {
					return StreamEvent{}, fmt.Errorf("namespace constraint No Prefix Undeclaring: xmlns:%s may not be empty in an XML 1.0 document", a.Name.Local)
				}
			}
			if len(s.rawNames) >= MaxDepth {
				return StreamEvent{}, errTooDeep
			}
			el := s.startElement(t)
			return StreamEvent{Kind: StreamEnter, Node: el, Depth: len(s.stack) - 1}, nil

		case tokEnd:
			el, err := s.endElement(xml.EndElement{Name: tz.name})
			if err != nil {
				return StreamEvent{}, err
			}
			return StreamEvent{Kind: StreamExit, Node: el, Depth: len(s.stack)}, nil

		case tokText:
			text := s.text()
			if err := s.countText(len(text)); err != nil {
				return StreamEvent{}, err
			}
			if n := s.charData(text); n != nil {
				return StreamEvent{Kind: StreamLeaf, Node: n, Depth: len(s.stack) - 1}, nil
			}

		case tokComment:
			n := &Node{Kind: KindComment, Value: tz.str}
			s.stamp(n)
			s.top().Append(n)
			return StreamEvent{Kind: StreamLeaf, Node: n, Depth: len(s.stack) - 1}, nil

		case tokPI:
			if tz.target == "xml" {
				continue // the XML declaration is not a PI node
			}
			n := &Node{Kind: KindPI, Name: Name{Local: tz.target}, Value: tz.str}
			s.stamp(n)
			s.top().Append(n)
			return StreamEvent{Kind: StreamLeaf, Node: n, Depth: len(s.stack) - 1}, nil

		case tokDirective:
			if err := s.directive(xml.Directive(tz.data)); err != nil {
				return StreamEvent{}, err
			}
		}
	}
}

// text is the character data of the tokText token just read: a substring of
// the input where it stands there as written, else (a reference or a CR in
// it) a copy of the tokenizer's buffer, which the next token reuses.
func (s *StreamReader) text() string {
	if s.tok.tz.raw {
		return s.tok.tz.str
	}
	return string(s.tok.tz.textBytes())
}

// countText adds n bytes of character data to textBytes once the DOCTYPE has
// declared entities, and fails when entity expansion has made the text
// outgrow expansionLimit of the input read so far; countAttrs does the same
// for a start tag's attribute values.
// streamEntityMax is the longest entity value the streaming reader expands
// itself: at most 8 times the 3 bytes of the shortest reference (&e;), the
// factor expansionLimit allows.
const streamEntityMax = 24

func (s *StreamReader) countText(n int) error {
	if s.tok.tz.entity == nil {
		return nil
	}
	s.textBytes += n
	if limit := expansionLimit(s.tok.offset()); s.textBytes > limit {
		return errEntityExpansion(limit)
	}
	return nil
}

func (s *StreamReader) countAttrs(attrs []xml.Attr) error {
	if s.tok.tz.entity == nil {
		return nil
	}
	n := 0
	for _, a := range attrs {
		n += len(a.Value)
	}
	return s.countText(n)
}

func (s *StreamReader) top() *Node { return s.stack[len(s.stack)-1] }

// directive handles the DOCTYPE, the only directive that carries information
// the tree needs. Everything it reads comes from the INTERNAL subset: an
// external subset or an external general entity would have to be fetched and
// spliced in textually (expandExternalEntities), which is precisely the
// whole-source rewrite streaming cannot do — such a document is refused.
func (s *StreamReader) directive(t xml.Directive) error {
	d := string(t)
	if !strings.HasPrefix(strings.TrimLeft(d, " \t\r\n"), "DOCTYPE") {
		return nil
	}
	src := "<!" + d + ">"
	if _, ok := externalDTDSystemID(src); ok {
		return fmt.Errorf("%w: DOCTYPE names an external subset", ErrStreamUnsupported)
	}
	if sub, ok := internalSubset(src); ok && streamHasExternalEntity(sub) {
		return fmt.Errorf("%w: internal subset declares an external entity", ErrStreamUnsupported)
	}
	// baseDir "" throughout: every one of these helpers uses it only to reach
	// an external file, which the two checks above have already excluded.
	ents, err := documentEntities(src, "", nil)
	if err != nil {
		return err
	}
	// The tokenizer builds a whole run of text, every reference in it
	// substituted, before this reader sees it, so countText alone cannot keep
	// a long entity value referenced many times from being built. Such a
	// document goes to the full parser, which bounds the expansion before
	// building anything; short values (character names like &nbsp;) amplify
	// no more than the full parser's own limit allows.
	for _, v := range ents {
		if len(v) > streamEntityMax {
			return fmt.Errorf("%w: an entity value longer than %d bytes", ErrStreamUnsupported, streamEntityMax)
		}
	}
	if len(ents) > 0 {
		s.tok.tz.entity = ents
	}
	s.idKinds = idAttrKinds(src)
	s.attrDefaults = attlistDefaults(src)
	s.elemOnly = elementOnlyDecls(src, "", nil)
	if u := unparsedEntityDecls(src, "", nil); len(u) > 0 {
		s.doc.SetUnparsed(u)
	}
	return nil
}

// streamHasExternalEntity reports whether an internal subset declares a general
// entity by external identifier (<!ENTITY x SYSTEM "..."> / PUBLIC). Such an
// entity's replacement text may be markup, which encoding/xml can never supply
// — a full parse splices it in textually instead.
func streamHasExternalEntity(sub string) bool {
	for i := 0; ; {
		j := strings.Index(sub[i:], "<!ENTITY")
		if j < 0 {
			return false
		}
		p := i + j + len("<!ENTITY")
		i = p
		end := strings.IndexByte(sub[p:], '>')
		if end < 0 {
			return true // malformed: refuse rather than guess
		}
		decl := sub[p : p+end]
		if strings.Contains(decl, "SYSTEM") || strings.Contains(decl, "PUBLIC") {
			return true
		}
		i = p + end
	}
}

func (s *StreamReader) startElement(t xml.StartElement) *Node {
	// Intern the tag's own name before anything retains it: one string per
	// distinct name, copied out of the tokenizer's window (see
	// streamTokenizer), where the element and rawNames would otherwise keep
	// the window alive.
	t.Name.Local = internCopy(s.nameIntern, t.Name.Local)
	t.Name.Space = internCopy(s.nameIntern, t.Name.Space)
	// Lazy-clone, same reasoning as the buffered parser's identical gate in
	// parse.go: most elements declare no namespace of their own, so reuse the
	// parent's frame reference until a decl actually forces a copy.
	frame := s.nsStack[len(s.nsStack)-1]
	cloned := false
	var decls []*Node
	if d := s.attrDefaults[t.Name.Local]; len(d) > 0 {
		t.Attr = withNSDefaults(t.Attr, d) // DTD-defaulted xmlns[:p] declarations
	}
	for _, a := range t.Attr {
		local, isNS := "", false
		switch {
		case a.Name.Space == "xmlns":
			local, isNS = a.Name.Local, true
		case a.Name.Space == "" && a.Name.Local == "xmlns":
			local, isNS = "", true
		}
		if !isNS {
			continue
		}
		if !cloned {
			frame = cloneNS(frame)
			cloned = true
		}
		// Copied out of the window: the frame lives as long as the element
		// is open, which for the document element is the whole document.
		uri := strings.Clone(a.Value)
		frame[local] = uri
		decls = append(decls, &Node{Kind: KindNamespace, Name: Name{Local: internCopy(s.nameIntern, local)}, Value: uri})
	}
	// The element and its attribute nodes are one allocation: an attribute
	// keeps its element alive through Parent anyway, so nothing is retained
	// longer than before, and a record of a few attributes costs one
	// allocation instead of one per node plus the regrowth of Attrs.
	nAttrs := len(t.Attr) - len(decls)
	slab, attrs := streamElementSlab(nAttrs)
	el := &slab[0]
	el.Kind, el.Name = KindElement, resolveRawName(frame, t.Name, false)
	el.SetNSDecls(decls)
	// Stamping order is element, then its namespace nodes, then its
	// attributes, then (as they arrive) its children — exactly assignOrder's
	// traversal, so a streamed tree numbers identically to a parsed one.
	s.stamp(el)
	for _, d := range decls {
		d.Parent = el
		s.stamp(d)
	}
	el.Attrs = attrs
	for _, a := range t.Attr {
		if a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns") {
			continue
		}
		slab = slab[1:]
		an := &slab[0]
		an.Kind = KindAttribute
		an.Name = resolveRawName(frame, a.Name, true)
		an.Value = streamNormalizeAttrValue(a.Value)
		if m := s.idKinds[t.Name.Local]; m != nil {
			if k := m[a.Name.Local]; k != 0 {
				an.SetIDKind(k)
			}
		}
		normalizeIDAttr(an)
		an.Parent = el
		s.stamp(an)
		el.Attrs = append(el.Attrs, an)
	}
	if defaults := s.attrDefaults[t.Name.Local]; len(defaults) > 0 {
		for _, name := range defaultNames(defaults) {
			d := defaults[name]
			if isNSDecl(name) {
				continue
			}
			present := false
			for _, a := range el.Attrs {
				if a.Name.Space == "" && a.Name.Local == name {
					present = true
					break
				}
			}
			if present {
				continue
			}
			an := &Node{Kind: KindAttribute, Name: Name{Local: name}, Value: d.value, Parent: el}
			s.stamp(an)
			el.Attrs = append(el.Attrs, an)
		}
	}
	s.appendChild(s.top(), el)
	s.stack = append(s.stack, el)
	s.nsStack = append(s.nsStack, frame)
	s.rawNames = append(s.rawNames, t.Name)
	return el
}

// Element records with one to three attributes, the common shapes, come
// from one allocation holding the attribute pointer list as well; the
// pointers stay in the element's own allocation, which its attributes keep
// alive through Parent anyway, so nothing lives longer than before.
type streamElem1 struct {
	nodes [2]Node
	ptrs  [1]*Node
}

type streamElem2 struct {
	nodes [3]Node
	ptrs  [2]*Node
}

type streamElem3 struct {
	nodes [4]Node
	ptrs  [3]*Node
}

// streamElementSlab allocates an element node followed by nAttrs attribute
// nodes, and the empty attribute list to append them to (nil for none).
func streamElementSlab(nAttrs int) (nodes []Node, attrs []*Node) {
	switch nAttrs {
	case 0:
		return make([]Node, 1), nil
	case 1:
		e := &streamElem1{}
		return e.nodes[:], e.ptrs[:0]
	case 2:
		e := &streamElem2{}
		return e.nodes[:], e.ptrs[:0]
	case 3:
		e := &streamElem3{}
		return e.nodes[:], e.ptrs[:0]
	}
	return make([]Node, 1+nAttrs), make([]*Node, 0, nAttrs)
}

func (s *StreamReader) endElement(t xml.EndElement) (*Node, error) {
	if len(s.rawNames) == 0 {
		return nil, errors.New("unexpected end element </" + rawQName(t.Name) + ">")
	}
	if open := s.rawNames[len(s.rawNames)-1]; open != t.Name {
		return nil, errors.New("element <" + rawQName(open) + "> closed by </" + rawQName(t.Name) + ">")
	}
	el := s.top()
	s.pop()
	return el, nil
}

func (s *StreamReader) pop() {
	s.rawNames = s.rawNames[:len(s.rawNames)-1]
	s.stack = s.stack[:len(s.stack)-1]
	s.nsStack = s.nsStack[:len(s.nsStack)-1]
}

// charData appends character data, returning the node to report — or nil when
// the data was discarded (prolog/epilog or element-content whitespace) or
// coalesced into a text node already reported. The three rules mirror
// parseRaw's CharData case exactly.
func (s *StreamReader) charData(t string) *Node {
	top := s.top()
	if top == s.doc && strings.Trim(t, " \t\r\n") == "" {
		return nil
	}
	if top.Kind == KindElement && s.elemOnly[top.Name.Local] && strings.Trim(t, " \t\r\n") == "" {
		return nil
	}
	if k := len(top.Children); k > 0 && top.Children[k-1].Kind == KindText && !top.Children[k-1].Atomic() {
		top.Children[k-1].Value += t
		return nil
	}
	n := &Node{Kind: KindText, Value: s.textString(t)}
	s.stamp(n)
	s.appendChild(top, n)
	return n
}

// appendChild is Node.Append with the growth a streamed record is built by:
// an element's first child gets a list of one (most elements are a leaf
// holding one text node), the second a list of four, then doubling — so
// an element of three or four children costs two list allocations, not
// three (1, 2, 4), for the same bytes.
func (s *StreamReader) appendChild(parent, child *Node) {
	child.Parent = parent
	if n := len(parent.Children); n == cap(parent.Children) {
		grown := make([]*Node, n, max(1, 4*n))
		copy(grown, parent.Children)
		parent.Children = grown
	}
	parent.Children = append(parent.Children, child)
}

// streamMaxWSRuns bounds StreamReader.wsRuns.
const streamMaxWSRuns = 256

// textString is t, or the shared copy of it when t is a short
// whitespace-only run (see wsRuns): one string per distinct run, instead of
// a text node per run keeping its own window of the input alive.
func (s *StreamReader) textString(t string) string {
	if len(t) == 0 || len(t) > maxInternedWS {
		return t
	}
	for i := 0; i < len(t); i++ {
		if c := t[i]; c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			return t
		}
	}
	if v, ok := s.wsRuns[t]; ok {
		return v
	}
	if len(s.wsRuns) < streamMaxWSRuns {
		if s.wsRuns == nil {
			s.wsRuns = make(map[string]string)
		}
		v := strings.Clone(t)
		s.wsRuns[v] = v
		return v
	}
	return t
}

// internCopy is intern (parse.go) for strings cut from a tokenizer window:
// the kept string is a copy, so the table never holds a window.
func internCopy(m map[string]string, s string) string {
	if v, ok := m[s]; ok {
		return v
	}
	s = strings.Clone(s)
	m[s] = s
	return s
}

// streamNormalizeAttrValue applies the XML attribute-value normalization step
// encoding/xml skips: a literal tab, LF or CR in a quoted attribute value is a
// space.
//
// A full parse does this on the RAW source (normalizeAttrWhitespace), before
// the decoder resolves references, which is what lets it keep a &#10; as a
// real newline while folding a literal one. Here the decoder has already
// resolved the reference and the two are indistinguishable, so a
// character-reference newline in a streamed attribute folds to a space as
// well. Accepted: the alternative — not normalizing at all — is wrong for the
// overwhelmingly more common literal case.
func streamNormalizeAttrValue(v string) string {
	if !strings.ContainsAny(v, "\t\n\r") {
		return v
	}
	b := []byte(v)
	for i, c := range b {
		if c == '\t' || c == '\n' || c == '\r' {
			b[i] = ' '
		}
	}
	return string(b)
}

// Drop detaches a completed node from its parent so the subtree becomes
// garbage. The parent is recorded as INCOMPLETE (see Incomplete): its
// string-value, its child axis and every document-order walk through it now
// describe less than the document really contained.
func (s *StreamReader) Drop(n *Node) {
	p := n.Parent
	if p == nil {
		return
	}
	for i, c := range p.Children {
		if c == n {
			p.Children = append(p.Children[:i], p.Children[i+1:]...)
			break
		}
	}
	if s.incomplete == nil {
		s.incomplete = map[*Node]bool{}
	}
	s.incomplete[p] = true
	// A dropped node is unreachable, so its own entry (if it ever had one) is
	// dead weight — removing it keeps the map bounded by the live spine
	// instead of growing once per record.
	delete(s.incomplete, n)
}

// SkipSubtree discards the element the last StreamEnter reported, and
// everything inside it, WITHOUT building any of it. This is the only way to
// pass over an irrelevant subtree in bounded memory: dropping it after the
// fact would mean materializing it first.
func (s *StreamReader) SkipSubtree() error {
	if len(s.stack) < 2 {
		return errors.New("SkipSubtree: no element is open")
	}
	el := s.top()
	nested := 0
	for {
		kind, err := s.tok.next()
		if err != nil {
			if err == io.EOF {
				return errors.New("unexpected end of document: unclosed elements")
			}
			return err
		}
		tz := &s.tok.tz
		switch kind {
		case tokStart:
			if err := s.countAttrs(tz.attrs); err != nil {
				return err
			}
			nested++
		case tokText:
			if err := s.countText(len(s.text())); err != nil {
				return err
			}
		case tokEnd:
			if nested > 0 {
				nested--
				continue
			}
			if open := s.rawNames[len(s.rawNames)-1]; open != tz.name {
				return errors.New("element <" + rawQName(open) + "> closed by </" + rawQName(tz.name) + ">")
			}
			s.pop()
			s.Drop(el)
			return nil
		}
	}
}

// Incomplete reports whether Drop has taken children away from n. Every read
// that depends on n having ALL its children — its string-value, its child /
// descendant / following / preceding axes, a document-order walk crossing it —
// is truncated for such a node, and truncation is silent: siblings() simply
// fails to find a dropped node and returns nil, docWalk collects whatever is
// still attached. A caller that can reach a node from a streamed document by a
// route it did not itself control must consult this and raise an error rather
// than hand back a quietly wrong answer.
func (s *StreamReader) Incomplete(n *Node) bool { return s.incomplete[n] }
