package xmltree

import (
	"errors"
	"strings"
	"testing"
)

// streamAll drives a reader to EOF, keeping everything, and returns the
// document it built.
func streamAll(t *testing.T, src string) *Node {
	t.Helper()
	s, err := NewStreamReader(strings.NewReader(src))
	if err != nil {
		t.Fatalf("NewStreamReader: %v", err)
	}
	for {
		ev, err := s.Advance()
		if err != nil {
			t.Fatalf("Advance: %v", err)
		}
		if ev.Kind == StreamEOF {
			return s.Doc()
		}
	}
}

// TestStreamReaderMatchesParse is the reader's correctness gate: a document
// streamed WITHOUT dropping anything must be indistinguishable from the same
// document parsed in one go — same serialization, and the same document-order
// numbering, since half the engine keys off Node.Order().
func TestStreamReaderMatchesParse(t *testing.T) {
	docs := []string{
		`<a><b x="1">t</b><c/></a>`,
		`<?xml version="1.0"?><root xmlns="urn:d" xmlns:p="urn:p"><p:x a="1" p:b="2"/><y/>tail</root>`,
		`<a>  <b/> text <!--c--><?pi data?><![CDATA[<raw>]]> </a>`,
		"<a>\n  <b/>\n</a>\n",
		`<!DOCTYPE a [<!ENTITY e "EXP">]><a>&e;<b n="&e;"/></a>`,
		`<!DOCTYPE a [<!ELEMENT a (b)><!ATTLIST b id ID #IMPLIED k CDATA "dflt">]><a><b id="i1"/></a>`,
		"\xEF\xBB\xBF<a>bom</a>", // a UTF-8 byte-order mark is an encoding signature, not content
	}
	opts := SerializeOptions{Method: "xml", OmitXMLDeclaration: true}
	for _, src := range docs {
		want, err := Parse(src)
		if err != nil {
			t.Fatalf("Parse(%q): %v", src, err)
		}
		got := streamAll(t, src)
		if gs, ws := Serialize(got, opts), Serialize(want, opts); gs != ws {
			t.Errorf("stream(%q):\n got %q\nwant %q", src, gs, ws)
			continue
		}
		if err := sameOrdering(got, want); err != nil {
			t.Errorf("stream(%q): %v", src, err)
		}
	}
}

// sameOrdering walks both trees in step, checking that every node carries the
// same document-order index and that indices are strictly increasing.
func sameOrdering(got, want *Node) error {
	var g, w []*Node
	collect(got, &g)
	collect(want, &w)
	if len(g) != len(w) {
		return errors.New("node counts differ")
	}
	last := -1
	for i := range g {
		if g[i].Order() != w[i].Order() {
			return errors.New("order index differs at node " + g[i].Name.Local)
		}
		if g[i].Order() <= last {
			return errors.New("order is not strictly increasing at " + g[i].Name.Local)
		}
		last = g[i].Order()
		if g[i].Tree() == 0 {
			return errors.New("tree stamp missing at " + g[i].Name.Local)
		}
	}
	return nil
}

func collect(n *Node, out *[]*Node) {
	*out = append(*out, n)
	for _, ns := range n.NS {
		*out = append(*out, ns)
	}
	for _, a := range n.Attrs {
		*out = append(*out, a)
	}
	for _, c := range n.Children {
		collect(c, out)
	}
}

// TestStreamReaderAttrNormalization: a literal newline inside an attribute
// value is a space (the XML normalization step encoding/xml skips).
func TestStreamReaderAttrNormalization(t *testing.T) {
	doc := streamAll(t, "<a k=\"one\ntwo\tthree\"/>")
	el := RootElement(doc)
	if v, _ := el.AttrLocal("k"); v != "one two three" {
		t.Errorf("got %q, want %q", v, "one two three")
	}
}

// TestStreamReaderDropAndIncomplete: dropping a processed record detaches it
// and marks the parent, which is the only record left that the parent's child
// list no longer describes the document.
func TestStreamReaderDropAndIncomplete(t *testing.T) {
	s, err := NewStreamReader(strings.NewReader(`<d><r>1</r><r>2</r><r>3</r></d>`))
	if err != nil {
		t.Fatalf("NewStreamReader: %v", err)
	}
	var root *Node
	var values []string
	for {
		ev, err := s.Advance()
		if err != nil {
			t.Fatalf("Advance: %v", err)
		}
		if ev.Kind == StreamEOF {
			break
		}
		if ev.Kind == StreamEnter && ev.Depth == 1 {
			root = ev.Node
		}
		if ev.Kind == StreamExit && ev.Depth == 2 {
			values = append(values, ev.Node.StringValue())
			if n := len(root.Children); n != 1 {
				t.Fatalf("expected exactly one live record under the root, got %d", n)
			}
			s.Drop(ev.Node)
		}
	}
	if strings.Join(values, ",") != "1,2,3" {
		t.Errorf("got %v, want 1,2,3", values)
	}
	if !s.Incomplete(root) {
		t.Error("the root should be marked incomplete after its children were dropped")
	}
	if s.Incomplete(s.Doc()) {
		t.Error("the document node lost no children and should not be marked incomplete")
	}
}

// TestStreamReaderSkipSubtree: an off-path subtree must be passed over without
// being built at all — dropping it afterwards would mean materializing it
// first, which is the whole thing streaming exists to avoid.
func TestStreamReaderSkipSubtree(t *testing.T) {
	s, err := NewStreamReader(strings.NewReader(`<d><skip><deep><x/></deep>text</skip><keep>K</keep></d>`))
	if err != nil {
		t.Fatalf("NewStreamReader: %v", err)
	}
	var kept []string
	for {
		ev, err := s.Advance()
		if err != nil {
			t.Fatalf("Advance: %v", err)
		}
		if ev.Kind == StreamEOF {
			break
		}
		if ev.Kind == StreamEnter && ev.Node.Name.Local == "skip" {
			if err := s.SkipSubtree(); err != nil {
				t.Fatalf("SkipSubtree: %v", err)
			}
			continue
		}
		if ev.Kind == StreamExit && ev.Node.Name.Local == "keep" {
			kept = append(kept, ev.Node.StringValue())
		}
	}
	if strings.Join(kept, ",") != "K" {
		t.Errorf("got %v, want [K]", kept)
	}
}

// TestStreamReaderRefusals: documents whose correct handling needs the whole
// source at once are refused, not mis-parsed — the caller falls back to Parse.
func TestStreamReaderRefusals(t *testing.T) {
	cases := map[string]string{
		"external subset": `<!DOCTYPE a SYSTEM "a.dtd"><a/>`,
		"external entity": `<!DOCTYPE a [<!ENTITY e SYSTEM "e.xml">]><a>&e;</a>`,
		"xml 1.1":         `<?xml version="1.1"?><a/>`,
	}
	for name, src := range cases {
		// Every refusal must arrive from the constructor (the reader primes
		// itself past the DOCTYPE), so a caller can fall back before it has
		// acted on anything.
		if _, err := NewStreamReader(strings.NewReader(src)); !errors.Is(err, ErrStreamUnsupported) {
			t.Errorf("%s: got %v, want ErrStreamUnsupported", name, err)
		}
	}
}
