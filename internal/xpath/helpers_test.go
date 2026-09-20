package xpath

import (
	"testing"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// xpEval parses and evaluates expr against a minimal <r/> document.
func xpEval(t *testing.T, expr string) Object {
	t.Helper()
	p, err := Parse(expr)
	if err != nil {
		t.Fatalf("parse %q: %v", expr, err)
	}
	doc, _ := xmltree.Parse("<r/>")
	v, err := p.Eval(&Context{Node: doc, Pos: 1, Size: 1})
	if err != nil {
		t.Fatalf("eval %q: %v", expr, err)
	}
	return v
}

// xpStr evaluates expr (against <r/>) and returns ToString of the result.
func xpStr(t *testing.T, expr string) string {
	t.Helper()
	return ToString(xpEval(t, expr))
}

// xpStrCtx evaluates expr against the document parsed from xml and returns
// ToString of the result.
func xpStrCtx(t *testing.T, expr, xml string) string {
	t.Helper()
	p, err := Parse(expr)
	if err != nil {
		t.Fatalf("parse %q: %v", expr, err)
	}
	doc, err := xmltree.Parse(xml)
	if err != nil {
		t.Fatalf("parse xml: %v", err)
	}
	v, err := p.Eval(&Context{Node: doc, Pos: 1, Size: 1})
	if err != nil {
		t.Fatalf("eval %q: %v", expr, err)
	}
	return ToString(v)
}
