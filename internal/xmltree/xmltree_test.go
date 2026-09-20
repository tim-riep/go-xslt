package xmltree

import "testing"

func TestParseAndStringValue(t *testing.T) {
	doc, err := Parse(`<catalog><book><title>Go</title></book></catalog>`)
	if err != nil {
		t.Fatal(err)
	}
	root := RootElement(doc)
	if root == nil || root.Name.Local != "catalog" {
		t.Fatalf("root = %#v", root)
	}
	if got := root.StringValue(); got != "Go" {
		t.Fatalf("string-value = %q, want %q", got, "Go")
	}
}

func TestParseNamespaces(t *testing.T) {
	doc, err := Parse(`<x:a xmlns:x="urn:x"><x:b>hi</x:b></x:a>`)
	if err != nil {
		t.Fatal(err)
	}
	root := RootElement(doc)
	if root.Name.Space != "urn:x" || root.Name.Local != "a" {
		t.Fatalf("name = %#v", root.Name)
	}
}

func TestDocumentOrder(t *testing.T) {
	doc, _ := Parse(`<a><b/><c/></a>`)
	root := RootElement(doc)
	b := root.Children[0]
	c := root.Children[1]
	if !(root.Order() < b.Order() && b.Order() < c.Order()) {
		t.Fatalf("order: a=%d b=%d c=%d", root.Order(), b.Order(), c.Order())
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	doc, _ := Parse(`<a id="1"><b>x</b></a>`)
	out := Serialize(doc, SerializeOptions{Method: "xml", OmitXMLDeclaration: true})
	want := `<a id="1"><b>x</b></a>`
	if out != want {
		t.Fatalf("serialize = %q, want %q", out, want)
	}
}

func TestSerializeLineCol(t *testing.T) {
	doc, _ := Parse("<a>\n  <b/>\n</a>")
	root := RootElement(doc)
	b := root.Children[1] // text "\n  " is [0]
	if b.Line != 2 {
		t.Fatalf("b.Line = %d, want 2", b.Line)
	}
}
