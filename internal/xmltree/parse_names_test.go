package xmltree

import (
	"strings"
	"testing"
)

func TestParseFifthEditionNames(t *testing.T) {
	type tc struct{ name, src string }
	good := []tc{
		{"emoji element", "<\U0001F600/>"},
		{"bengali digit nested", "<a><\u09EA/></a>"},
		{"astral attribute", "<a \U0001F600x='1'/>"},
		{"mixed 1.0 declaration", "<?xml version='1.0'?><r><\u0133 \u09EA='v'/></r>"},
		{"1.1 declaration", "<?xml version='1.1'?><\U0001F600/>"},
		{"prefix and namespace declaration", "<\U0001F600:a xmlns:\U0001F600=\"u\"><!-- c \U0001F600 --><?p \U0001F600?><\u09EA b\U0001F600=\"1\"/></\U0001F600:a>"},
		{"prefixed attribute", "<r xmlns:\u0133=\"u\" \u0133:\u09EA=\"1\"/>"},
		{"name char that cannot start a name", "<a\u203Fb x\u203F=\"1\"/>"},
		{"many distinct", "<r><\U0001F600/><\U0001F601/><\U0001F602/><\U0001F603/><\U0001F604/><\U0001F605/><\U0001F606/><\U0001F607/></r>"},
	}
	for _, c := range good {
		for _, parse := range []struct {
			n string
			f func(string) (*Node, error)
		}{{"Parse", Parse}, {"ParseLenient11", ParseLenient11}} {
			doc, err := parse.f(c.src)
			if err != nil {
				t.Errorf("%s %s: %v", parse.n, c.name, err)
				continue
			}
			// The serialization carries the names back unchanged.
			root := firstElement(doc)
			out := Serialize(doc, SerializeOptions{Method: "xml", OmitXMLDeclaration: true})
			if !IsName(root.Name.Local) || strings.Contains(out, "Zq5e") {
				t.Errorf("%s %s: name %q, serialized %q", parse.n, c.name, root.Name.Local, out)
			}
			if again, err := Parse(out); err != nil {
				t.Errorf("%s %s: serialization %q does not parse back: %v", parse.n, c.name, out, err)
			} else if Serialize(again, SerializeOptions{Method: "xml", OmitXMLDeclaration: true}) != out {
				t.Errorf("%s %s: round trip changed %q", parse.n, c.name, out)
			}
		}
	}
	doc, err := Parse("<a><\u09EA/></a>")
	if err != nil {
		t.Fatal(err)
	}
	if got := firstElement(doc).Children[0].Name.Local; got != "\u09EA" {
		t.Errorf("child name = %q", got)
	}
	// Content that merely contains the character is untouched.
	doc, err = Parse("<\U0001F600>\U0001F600 text</\U0001F600>")
	if err != nil {
		t.Fatal(err)
	}
	if got := firstElement(doc).StringValue(); got != "\U0001F600 text" {
		t.Errorf("text = %q", got)
	}
}

func TestParseKeepsIllegalNameErrors(t *testing.T) {
	for _, src := range []string{
		"<a\uFFFE/>", "<\uFFFE/>", "<1a/>", "<a \u00D7='1'/>", "<\u0300a/>",
		"<a><\u0301/></a>", "<a\U000F0000/>", "<a \u0300x='1'/>",
		"<?xml version='1.1'?><\u0300a/>",
	} {
		if _, err := Parse(src); err == nil {
			t.Errorf("Parse(%q) succeeded", src)
		}
		if _, err := ParseLenient11(src); err == nil {
			t.Errorf("ParseLenient11(%q) succeeded", src)
		}
	}
}

// A document that parses at once never reaches the repair; one that cannot be
// repaired reports the decoder's own error.
func TestParseRepairErrorIsDecoders(t *testing.T) {
	_, err1 := Parse("<a\uFFFE/>")
	if err1 == nil || !strings.Contains(err1.Error(), "invalid XML name") {
		t.Fatalf("err = %v", err1)
	}
}

func firstElement(doc *Node) *Node {
	for _, c := range doc.Children {
		if c.Kind == KindElement {
			return c
		}
	}
	panic("no element")
}

// The prefix of an element or attribute and the name of a namespace
// declaration are names too: they come back from the repair as the original
// characters, in the tree and in the serialization.
func TestParseRepairRestoresPrefixes(t *testing.T) {
	src := "<\U0001F600:a xmlns:\U0001F600=\"u\" xmlns:\u0133=\"v\" \u0133:x=\"1\"><!-- c \U0001F600 --><?p \U0001F600?><\u09EA b\U0001F600=\"1\"/></\U0001F600:a>"
	for _, parse := range []func(string) (*Node, error){Parse, ParseLenient11} {
		doc, err := parse(src)
		if err != nil {
			t.Fatal(err)
		}
		root := firstElement(doc)
		if root.Name.Prefix != "\U0001F600" || root.Name.Local != "a" || root.Name.Space != "u" {
			t.Errorf("root name = %+v", root.Name)
		}
		var decls []string
		for _, ns := range root.NSDecls() {
			decls = append(decls, ns.Name.Local+"="+ns.Value)
		}
		if got := strings.Join(decls, " "); got != "\U0001F600=u \u0133=v" {
			t.Errorf("namespace declarations = %q", got)
		}
		if len(root.Attrs) < 1 || root.Attrs[0].Name.Prefix != "\u0133" || root.Attrs[0].Name.Space != "v" {
			t.Errorf("attributes = %+v", root.Attrs)
		}
		if uri, ok := root.LookupPrefix("\U0001F600"); !ok || uri != "u" {
			t.Errorf("LookupPrefix = %q, %v", uri, ok)
		}
		out := Serialize(doc, SerializeOptions{Method: "xml", OmitXMLDeclaration: true})
		if strings.Contains(out, "Zq5e") || !strings.HasPrefix(out, "<\U0001F600:a ") || !strings.Contains(out, "xmlns:\U0001F600=\"u\"") {
			t.Errorf("serialized %q", out)
		}
	}
	// A combining mark cannot open a prefix. (Not tested: the tokenizer's own
	// laxness, which accepts it after "xmlns:" like encoding/xml, stays.)
	for _, bad := range []string{"<\u0300a:b xmlns:\u0300a=\"u\"/>", "<\u0300a:b xmlns:\U0001F600=\"u\"/>"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) succeeded", bad)
		}
	}
}
