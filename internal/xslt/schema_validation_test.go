package xslt

import (
	"strings"
	"testing"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// The corner these tests exist for: §24.4.1.1's "preserve" is FOUR different
// rules wearing one keyword, and two of them look interchangeable but are
// exact opposites.
//
//	xsl:copy    of an element -> xs:anyType   (the annotation is DROPPED)
//	xsl:copy-of of an element -> unchanged    (the annotation is KEPT)
//
// The spec's own reason for the asymmetry is in the text: xsl:copy "does not
// copy the content of the element, [so] it would be wrong to assume that the
// type is unchanged", whereas xsl:copy-of copies the content and so may keep
// it. Getting these backwards produces a result that is structurally identical
// and type-annotated wrongly — invisible to every serialization-comparing
// conformance assertion, and visible only to an element(N,T) test. Hence a
// direct, differential unit test rather than reliance on the suite.

// annotatedTree builds <e my:a="x"><c/></e> with a distinct, non-built-in
// annotation on each of the three nodes, so any rule that drops, keeps or
// rewrites one is individually observable.
func annotatedTree() (root, child, attr *xmltree.Node) {
	root = xmltree.NewElement(xmltree.Name{Local: "e"})
	root.SchemaType = &xmltree.SchemaTypeName{Namespace: "urn:t", Local: "RootType", Complex: true}
	attr = root.SetAttr(xmltree.Name{Local: "a"}, "x")
	attr.SchemaType = &xmltree.SchemaTypeName{Namespace: "urn:t", Local: "AttrType"}
	child = xmltree.NewElement(xmltree.Name{Local: "c"})
	child.SchemaType = &xmltree.SchemaTypeName{Namespace: "urn:t", Local: "ChildType", Complex: true}
	root.Append(child)
	return root, child, attr
}

func typeName(n *xmltree.Node) string {
	if n == nil || n.SchemaType == nil {
		return "<none>"
	}
	return n.SchemaType.Namespace + "#" + n.SchemaType.Local
}

func TestPreserveIsPerInstruction(t *testing.T) {
	const (
		none   = "<none>"
		anyT   = xsdNS + "#anyType"
		rootT  = "urn:t#RootType"
		childT = "urn:t#ChildType"
		attrT  = "urn:t#AttrType"
	)

	for _, tc := range []struct {
		name                string
		kind                valKind
		wantRoot, wantChild string
		wantAttr            string
		why                 string
	}{
		{
			name: "xsl:element and literal result elements",
			kind: vkElement, wantRoot: anyT, wantChild: childT, wantAttr: attrT,
			why: `"the new element has a type annotation of xs:anyType, and the type annotations of contained nodes are retained unchanged"`,
		},
		{
			name: "xsl:copy of an element",
			kind: vkCopy, wantRoot: anyT, wantChild: childT, wantAttr: attrT,
			why: `"the copied element will have a type annotation of xs:anyType ... but any contained nodes will have their type annotations retained"`,
		},
		{
			name: "xsl:copy-of",
			kind: vkCopyOf, wantRoot: rootT, wantChild: childT, wantAttr: attrT,
			why: `"all the nodes that are copied will retain their type annotations unchanged" — the OPPOSITE of xsl:copy for the copied element itself`,
		},
		{
			name: "document nodes",
			kind: vkDocument, wantRoot: rootT, wantChild: childT, wantAttr: attrT,
			why: `validation="preserve" on a document node "does not request validation ... all element and attribute nodes within the tree ... retain their type annotations"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, child, attr := annotatedTree()
			applyPreserve(tc.kind, root)
			if got := typeName(root); got != tc.wantRoot {
				t.Errorf("copied element: got %s, want %s\n  §24.4.1.1: %s", got, tc.wantRoot, tc.why)
			}
			if got := typeName(child); got != tc.wantChild {
				t.Errorf("contained element: got %s, want %s\n  §24.4.1.1: %s", got, tc.wantChild, tc.why)
			}
			if got := typeName(attr); got != tc.wantAttr {
				t.Errorf("contained attribute: got %s, want %s\n  §24.4.1.1: %s", got, tc.wantAttr, tc.why)
			}
		})
	}

	// The two kinds whose "preserve" is defined on a node that is not an
	// element at all.
	t.Run("xsl:attribute is strip", func(t *testing.T) {
		a := xmltree.NewAttribute(xmltree.Name{Local: "a"}, "x")
		a.SchemaType = &xmltree.SchemaTypeName{Namespace: "urn:t", Local: "AttrType"}
		applyPreserve(vkAttribute, a)
		if got := typeName(a); got != none {
			t.Errorf("xsl:attribute under preserve: got %s, want %s\n  §24.4.1.1: "+
				`"the effect is exactly the same as specifying validation='strip'"`, got, none)
		}
	})
	t.Run("xsl:copy of an attribute retains", func(t *testing.T) {
		a := xmltree.NewAttribute(xmltree.Name{Local: "a"}, "x")
		a.SchemaType = &xmltree.SchemaTypeName{Namespace: "urn:t", Local: "AttrType"}
		applyPreserve(vkCopy, a)
		if got := typeName(a); got != attrT {
			t.Errorf("xsl:copy of an attribute under preserve: got %s, want %s\n  §24.4.1.1: "+
				`"the copied attribute will retain its type annotation"`, got, attrT)
		}
	})
}

// TestCopyAndCopyOfDisagreeOnPreserve states the asymmetry as ONE assertion,
// so a change that silently unifies the two (the easy mistake — they differ by
// a single spec sentence) fails here with the reason attached, rather than
// only showing up as a conformance-count drift.
func TestCopyAndCopyOfDisagreeOnPreserve(t *testing.T) {
	byCopy, _, _ := annotatedTree()
	byCopyOf, _, _ := annotatedTree()
	applyPreserve(vkCopy, byCopy)
	applyPreserve(vkCopyOf, byCopyOf)
	if typeName(byCopy) == typeName(byCopyOf) {
		t.Fatalf("xsl:copy and xsl:copy-of gave the SAME annotation (%s) for a copied element under "+
			"validation=\"preserve\"; §24.4.1.1 requires them to differ: copy drops to xs:anyType, "+
			"copy-of retains", typeName(byCopy))
	}
	if typeName(byCopy) != xsdNS+"#anyType" {
		t.Errorf("xsl:copy: got %s, want xs:anyType", typeName(byCopy))
	}
	if typeName(byCopyOf) != "urn:t#RootType" {
		t.Errorf("xsl:copy-of: got %s, want the original urn:t#RootType", typeName(byCopyOf))
	}
}

func TestStripClearsWholeSubtree(t *testing.T) {
	root, child, attr := annotatedTree()
	child.TypeAnno, root.TypeAnno, attr.TypeAnno = 7, 8, 9
	stripAnnotations(root)
	for _, n := range []*xmltree.Node{root, child, attr} {
		if n.SchemaType != nil || n.TypeAnno != 0 {
			t.Errorf("strip left %s annotated (SchemaType=%v TypeAnno=%d); §24.4.1.1 requires "+
				"xs:untyped / xs:untypedAtomic throughout, which this engine spells as no annotation",
				n.Name.Local, n.SchemaType, n.TypeAnno)
		}
	}
}

// markUnassessed is what separates "assessed, and turned out to be nothing in
// particular" (xs:anyType) from "stripped" (xs:untyped). Both are spelled by
// internal/xsd as an absent annotation on the way out, so the distinction only
// exists because this pass puts it back.
func TestMarkUnassessedGivesAnyTypeToElementsOnly(t *testing.T) {
	root := xmltree.NewElement(xmltree.Name{Local: "e"})
	attr := root.SetAttr(xmltree.Name{Local: "a"}, "x")
	kept := xmltree.NewElement(xmltree.Name{Local: "typed"})
	kept.SchemaType = &xmltree.SchemaTypeName{Namespace: "urn:t", Local: "Kept"}
	bare := xmltree.NewElement(xmltree.Name{Local: "bare"})
	root.Append(kept)
	root.Append(bare)

	markUnassessed(root)

	if typeName(root) != xsdNS+"#anyType" || typeName(bare) != xsdNS+"#anyType" {
		t.Errorf("unassessed elements: root=%s bare=%s, want xs:anyType for both — §24.4.1.1: "+
			`"If no validation is performed for a node ... the node is annotated as xs:anyType"`,
			typeName(root), typeName(bare))
	}
	if typeName(kept) != "urn:t#Kept" {
		t.Errorf("an element that WAS assessed must keep its own type, got %s", typeName(kept))
	}
	if attr.SchemaType != nil {
		t.Errorf("an unassessed attribute is xs:untypedAtomic (no annotation), got %s", typeName(attr))
	}
	// The sentinel must be the literal name internal/xpath's schemaTypeMatches
	// compares against, or element(*, xs:anyType) silently stops matching.
	if *root.SchemaType != anyTypeName {
		t.Errorf("the anyType sentinel must equal anyTypeName exactly, got %+v", *root.SchemaType)
	}
	if !anyTypeName.Complex {
		t.Error("xs:anyType is a COMPLEX type; a false Complex flag makes it compare unequal to a resolved xs:anyType")
	}
}

// --- compile-time -----------------------------------------------------------

func elemAt(t *testing.T, src, local string) *xmltree.Node {
	t.Helper()
	doc, err := xmltree.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var found *xmltree.Node
	var walk func(n *xmltree.Node)
	walk = func(n *xmltree.Node) {
		if found != nil {
			return
		}
		if n.Kind == xmltree.KindElement && n.Name.Local == local {
			found = n
			return
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(doc)
	if found == nil {
		t.Fatalf("no element %q in %s", local, src)
	}
	return found
}

const valSheet = `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"
  xmlns:xs="http://www.w3.org/2001/XMLSchema" default-validation="preserve">
  <xsl:template name="main">
    <inherits/>
    <explicit xsl:validation="strip"/>
    <typed xsl:type="xs:untyped"/>
    <xsl:copy validation="lax"/>
    <both xsl:validation="strict" xsl:type="xs:string"/>
    <xsl:source-document href="x.xml"/>
  </xsl:template>
</xsl:stylesheet>`

// The whole feature is gated on ONE predicate. If compileValidation ever
// returns a non-nil request at the default claim, every constructed node in
// every non-schema-aware transformation starts taking the strip walk — so this
// is the test that keeps the default 7,790-case baseline honest.
func TestCompileValidationInertWhenNotSchemaAware(t *testing.T) {
	SetSchemaAware(false)
	for _, local := range []string{"inherits", "explicit", "typed", "both"} {
		req, err := compileValidation(elemAt(t, valSheet, local), vkElement)
		if err != nil {
			t.Errorf("%s: unexpected error at the default claim: %v", local, err)
		}
		if req != nil {
			t.Errorf("%s: got a request (%+v) at the default claim; it must be nil so the runtime hook is a single nil check", local, req)
		}
	}
}

func TestCompileValidationSchemaAware(t *testing.T) {
	SetSchemaAware(true)
	defer SetSchemaAware(false)

	t.Run("inherits default-validation from the nearest declaring ancestor", func(t *testing.T) {
		req, err := compileValidation(elemAt(t, valSheet, "inherits"), vkElement)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if req == nil || req.mode != valPreserve {
			t.Fatalf("got %+v, want mode=preserve inherited from xsl:stylesheet/@default-validation", req)
		}
	})

	t.Run("an explicit xsl:validation wins over the ambient default", func(t *testing.T) {
		req, err := compileValidation(elemAt(t, valSheet, "explicit"), vkElement)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if req == nil || req.mode != valStrip {
			t.Fatalf("got %+v, want mode=strip", req)
		}
	})

	t.Run("xs:untyped is admissible as a type even though it is not a type definition", func(t *testing.T) {
		req, err := compileValidation(elemAt(t, valSheet, "typed"), vkElement)
		if err != nil {
			t.Fatalf("xsl:type=\"xs:untyped\" must be accepted (§24.4.1.2 defines it by reference to strip): %v", err)
		}
		if req == nil || req.typ == nil || req.typ.Local != "untyped" {
			t.Fatalf("got %+v, want a request carrying xs:untyped", req)
		}
	})

	t.Run("XTSE1505 for both attributes at once", func(t *testing.T) {
		_, err := compileValidation(elemAt(t, valSheet, "both"), vkElement)
		if err == nil || !strings.Contains(err.Error(), "XTSE1505") {
			t.Fatalf("got %v, want XTSE1505 — §24.4: the two attributes are mutually exclusive", err)
		}
	})

	t.Run("xsl:source-document takes no ambient default", func(t *testing.T) {
		// §24.4: default-validation "has no effect on the xsl:stream and
		// xsl:merge-source elements, which perform no validation unless
		// explicitly requested" — even though this stylesheet declares
		// default-validation="preserve", which every other construct inherits.
		req, err := compileValidation(elemAt(t, valSheet, "source-document"), vkSourceDoc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if req != nil {
			t.Fatalf("got %+v, want no request at all", req)
		}
	})
}

// XTSE1530 is the static half of the "a complex type cannot validate an
// attribute" rule; XTTE1535 is its dynamic twin for xsl:copy/xsl:copy-of,
// where which items are being copied is not known until it runs.
func TestAttributeTypeMustBeSimple(t *testing.T) {
	SetSchemaAware(true)
	defer SetSchemaAware(false)

	const sheet = `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"
	  xmlns:xs="http://www.w3.org/2001/XMLSchema">
	  <xsl:template name="main"><xsl:attribute name="a" type="xs:anyType"/></xsl:template>
	</xsl:stylesheet>`
	_, err := compileValidation(elemAt(t, sheet, "attribute"), vkAttribute)
	if err == nil || !strings.Contains(err.Error(), "XTSE1530") {
		t.Fatalf("got %v, want XTSE1530 — §24.4.1.2: the type attribute of xsl:attribute must not refer to a complex type", err)
	}
	// The SAME name on an element-constructing instruction is perfectly legal.
	if _, err := compileValidation(elemAt(t, sheet, "attribute"), vkElement); err != nil {
		t.Fatalf("xs:anyType is only forbidden for xsl:attribute, got %v", err)
	}
}

func TestValidationTargetOfDocumentNode(t *testing.T) {
	mk := func(children ...*xmltree.Node) *xmltree.Node {
		d := &xmltree.Node{Kind: xmltree.KindDocument}
		for _, c := range children {
			d.Append(c)
		}
		return d
	}
	el := func(name string) *xmltree.Node { return xmltree.NewElement(xmltree.Name{Local: name}) }

	// §24.4.2: "the children of the document node comprise exactly one element
	// node, no text nodes, and zero or more comment and processing instruction
	// nodes, in any order" — anything else is XTTE1550, reported as a nil
	// target.
	only := el("root")
	if got := validationTarget(mk(&xmltree.Node{Kind: xmltree.KindComment}, only)); got != only {
		t.Errorf("one element plus a comment: got %v, want the element itself", got)
	}
	if got := validationTarget(mk(el("a"), el("b"))); got != nil {
		t.Errorf("two element children must be XTTE1550 (nil target), got %v", got)
	}
	if got := validationTarget(mk(el("a"), xmltree.NewText("hello"))); got != nil {
		t.Errorf("a text child must be XTTE1550 (nil target), got %v", got)
	}
	// An empty text node is what a branch producing nothing leaves behind, and
	// is not the "text node" the rule is about.
	if got := validationTarget(mk(only, xmltree.NewText(""))); got != only {
		t.Errorf("an empty text child must not trip XTTE1550, got %v", got)
	}
	// Kinds that carry no annotation at all are simply not targets — the case
	// xsl:copy-of hits constantly, since its @select routinely mixes kinds.
	for _, n := range []*xmltree.Node{
		xmltree.NewText("t"),
		{Kind: xmltree.KindComment},
		{Kind: xmltree.KindPI},
	} {
		if got := validationTarget(n); got != nil {
			t.Errorf("kind %v must not be a validation target, got %v", n.Kind, got)
		}
	}
}
