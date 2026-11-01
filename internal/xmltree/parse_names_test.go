package xmltree

import (
	"strings"
	"testing"
)

func TestParseFifthEditionNames(t *testing.T) {
	type tc struct{ name, src string }
	good := []tc{
		{"emoji element", "<\U0001F600/>"},
		{"bengali digit nested", "<a><৪/></a>"},
		{"astral attribute", "<a \U0001F600x='1'/>"},
		{"mixed 1.0 declaration", "<?xml version='1.0'?><r><ĳ ৪='v'/></r>"},
		{"1.1 declaration", "<?xml version='1.1'?><\U0001F600/>"},
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
	doc, err := Parse("<a><৪/></a>")
	if err != nil {
		t.Fatal(err)
	}
	if got := firstElement(doc).Children[0].Name.Local; got != "৪" {
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
		"<a￾/>", "<￾/>", "<1a/>", "<a ×='1'/>", "<̀a/>",
		"<a><́/></a>", "<a\U000F0000/>", "<a ̀x='1'/>",
		"<?xml version='1.1'?><̀a/>",
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
	_, err1 := Parse("<a￾/>")
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
