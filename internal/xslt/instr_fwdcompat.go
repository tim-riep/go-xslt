package xslt

import (
	"strconv"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// fwdCompatInstr is an otherwise-unrecognized XSLT instruction encountered
// under forwards-compatible processing (inForwardsCompatScope): a stylesheet
// declaring a version attribute greater than this processor implements may
// use element names it doesn't know, and per XSLT §3.4 that is a static
// no-op — at runtime the instruction instantiates its xsl:fallback children's
// content (all of them, concatenated), or raises a dynamic error if it has
// none (version-001/004/005/008/017).
type fwdCompatInstr struct {
	name        string
	hasFallback bool
	body        []instruction // concatenated content of every xsl:fallback child
	el          *xmltree.Node
}

func (*fwdCompatInstr) instr() {}

func (n *fwdCompatInstr) exec(eng *engine, r rt, out *xmltree.Node) error {
	if n.hasFallback {
		return eng.execSequence(n.body, r, out)
	}
	return errAt(n.el, "err:XTDE1425: unknown instruction xsl:%s has no xsl:fallback", n.name)
}

// compileForwardsCompat compiles an unrecognized xsl: element as a
// fwdCompatInstr, gathering the content of every direct xsl:fallback child.
func (c *compiler) compileForwardsCompat(el *xmltree.Node) (instruction, error) {
	n := &fwdCompatInstr{name: el.Name.Local, el: el}
	for _, ch := range elementChildren(el) {
		if ch.Name.Space != NS || ch.Name.Local != "fallback" {
			continue
		}
		n.hasFallback = true
		body, err := c.compileSequence(childNodesForBody(ch))
		if err != nil {
			return nil, err
		}
		n.body = append(n.body, body...)
	}
	return n, nil
}

// extInstr is a literal element whose namespace is an in-scope extension
// namespace (isExtensionElement) that this processor does not implement as an
// extension instruction — true of every extension instruction, since none are
// built in. At runtime it instantiates its xsl:fallback children's content
// (all of them, concatenated), or raises a dynamic error if it has none
// (XSLT §3.5 "Extension Instructions"; version-005/032).
type extInstr struct {
	name        string
	hasFallback bool
	body        []instruction // concatenated content of every xsl:fallback child
	el          *xmltree.Node
}

func (*extInstr) instr() {}

func (n *extInstr) exec(eng *engine, r rt, out *xmltree.Node) error {
	if n.hasFallback {
		return eng.execSequence(n.body, r, out)
	}
	return errAt(n.el, "err:XTDE1450: extension instruction %q is not implemented and has no xsl:fallback", n.name)
}

// compileExtensionElement compiles el (a literal element in an extension
// namespace) the way compileForwardsCompat compiles an unrecognized xsl:
// element: gathering the content of every direct xsl:fallback child.
func (c *compiler) compileExtensionElement(el *xmltree.Node) (instruction, error) {
	name := el.Name.Local
	if el.Name.Prefix != "" {
		name = el.Name.Prefix + ":" + name
	}
	n := &extInstr{name: name, el: el}
	for _, ch := range elementChildren(el) {
		if ch.Name.Space != NS || ch.Name.Local != "fallback" {
			continue
		}
		n.hasFallback = true
		body, err := c.compileSequence(childNodesForBody(ch))
		if err != nil {
			return nil, err
		}
		n.body = append(n.body, body...)
	}
	return n, nil
}

// inForwardsCompatScope reports whether el is processed under XSLT forwards-
// compatible mode: the nearest ancestor-or-self [xsl:]version attribute
// declares a version this processor doesn't support because it is GREATER
// than 3.0. Under that mode, an unrecognized xsl: instruction (XTSE1010) or an
// attribute a known one doesn't otherwise allow (XTSE0090) is not a static
// error — see fwdCompatInstr and the attribute check in validateXSLTElement.
func inForwardsCompatScope(el *xmltree.Node) bool {
	for cur := el; cur != nil; cur = cur.Parent {
		if cur.Kind != xmltree.KindElement {
			continue
		}
		var v string
		var ok bool
		if cur.Name.Space == NS {
			v, ok = cur.AttrLocal("version")
		} else {
			v, ok = cur.Attr(NS, "version")
		}
		if ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			return err == nil && f > 3.0
		}
	}
	return false
}

// inBackwardsCompatScope reports whether el runs under XSLT 1.0
// backwards-compatible processing: the nearest ancestor-or-self
// [xsl:]version attribute names a number less than 2.0. XSLT 3.0 §3.9.1
// backward-compatible mode is triggered by ANY value below the next version
// this processor implements above 1.0, not merely the literal value "1.0" —
// the "backwards" test-set's own -002/-004/-005/-037 pin 1.9, 0.5, -29 and
// 1.99999 all triggering it identically to "1.0". Only meaningful when
// backwardsCompatRun() claims the capability; callers gate on that (and on
// Stylesheet.hasBackwardsCompat, to skip this walk entirely for a module
// that could never trigger it) themselves.
func inBackwardsCompatScope(el *xmltree.Node) bool {
	for cur := el; cur != nil; cur = cur.Parent {
		if cur.Kind != xmltree.KindElement {
			continue
		}
		var v string
		var ok bool
		if cur.Name.Space == NS {
			v, ok = cur.AttrLocal("version")
		} else {
			v, ok = cur.Attr(NS, "version")
		}
		if ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			return err == nil && f < 2.0
		}
	}
	return false
}
