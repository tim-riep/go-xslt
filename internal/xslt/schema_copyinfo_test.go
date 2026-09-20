package xslt

import (
	"testing"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// The type-model fields (xmltree.Node.TypeAnno, .IDKind) used to be dropped by
// every node-rebuild path in the engine, because each one constructs fresh
// nodes field by field and nobody listed them: a DTD-typed ID attribute came
// out of an xsl:copy-of as an ordinary attribute, so fn:id could not find it in
// the copy. These pin the round trip through all four rebuild paths. Nothing in
// the engine produces a real schema annotation yet, so the fields are set
// synthetically — which is exactly the point: the copy must be faithful
// regardless of who set them.
func typedFixture() (root, kid, attr, text *xmltree.Node) {
	root = xmltree.NewElement(xmltree.Name{Local: "root"})
	root.TypeAnno = int32(xpath.XSstring)

	kid = xmltree.NewElement(xmltree.Name{Local: "kid"})
	kid.TypeAnno = int32(xpath.XSinteger)
	root.Append(kid)

	attr = kid.SetAttr(xmltree.Name{Local: "id"}, "a1")
	attr.IDKind = xmltree.IDKindID
	attr.TypeAnno = int32(xpath.XSstring)

	text = xmltree.NewText("42")
	text.TypeAnno = int32(xpath.XSinteger)
	kid.Append(text)
	return
}

func checkTypeInfo(t *testing.T, what string, got, want *xmltree.Node) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: no node", what)
	}
	if got.TypeAnno != want.TypeAnno {
		t.Errorf("%s: TypeAnno = %d, want %d", what, got.TypeAnno, want.TypeAnno)
	}
	if got.IDKind != want.IDKind {
		t.Errorf("%s: IDKind = %d, want %d", what, got.IDKind, want.IDKind)
	}
}

func TestCloneNodeKeepsTypeInfo(t *testing.T) {
	root, kid, attr, text := typedFixture()

	cp := cloneNode(root)
	checkTypeInfo(t, "cloned element", cp, root)
	cpKid := cp.Children[0]
	checkTypeInfo(t, "cloned child element", cpKid, kid)
	checkTypeInfo(t, "cloned attribute", cpKid.Attrs[0], attr)
	checkTypeInfo(t, "cloned text", cpKid.Children[0], text)

	// A standalone attribute item (fn:copy-of over an attribute node).
	checkTypeInfo(t, "cloned standalone attribute", cloneNode(attr), attr)
}

func TestDeepCopyIntoKeepsTypeInfo(t *testing.T) {
	root, kid, attr, text := typedFixture()

	out := &xmltree.Node{Kind: xmltree.KindDocument}
	deepCopyInto(root, out)
	cp := out.Children[0]
	checkTypeInfo(t, "copied element", cp, root)
	cpKid := cp.Children[0]
	checkTypeInfo(t, "copied child element", cpKid, kid)
	checkTypeInfo(t, "copied attribute", cpKid.Attrs[0], attr)
	checkTypeInfo(t, "copied text", cpKid.Children[0], text)

	// An attribute copied as its own sequence item goes through appendAttrItem,
	// a different rebuild than the element's own attribute loop above.
	seq := &xmltree.Node{Kind: xmltree.KindDocument, NoAtomicMerge: true}
	deepCopyInto(attr, seq)
	checkTypeInfo(t, "copied attribute item", seq.Attrs[0], attr)
}

func TestSnapshotNodeKeepsTypeInfo(t *testing.T) {
	root, kid, attr, _ := typedFixture()
	doc := &xmltree.Node{Kind: xmltree.KindDocument}
	doc.Append(root)

	// Snapshotting an element rebuilds its ANCESTORS as well, by a path of its
	// own (not cloneNode) — both halves have to keep the fields.
	snap := snapshotNode(kid)
	checkTypeInfo(t, "snapshot element", snap, kid)
	checkTypeInfo(t, "snapshot attribute", snap.Attrs[0], attr)
	checkTypeInfo(t, "snapshot ancestor element", snap.Parent, root)

	// Snapshotting the attribute itself returns the attribute node as it lives
	// on the rebuilt parent, which is a third rebuild again.
	checkTypeInfo(t, "snapshot of an attribute", snapshotNode(attr), attr)
}

func TestSetAttrReturnsTheNodeItSet(t *testing.T) {
	el := xmltree.NewElement(xmltree.Name{Local: "e"})
	a := el.SetAttr(xmltree.Name{Local: "x"}, "1")
	if a != el.Attrs[0] {
		t.Fatalf("SetAttr returned %p, want the node it appended %p", a, el.Attrs[0])
	}
	// Replacing a value must hand back the SAME node, not a fresh one, or a
	// caller carrying type info over would stamp a node nobody can see.
	if b := el.SetAttr(xmltree.Name{Local: "x"}, "2"); b != a {
		t.Fatalf("SetAttr on replace returned a different node")
	}
}
