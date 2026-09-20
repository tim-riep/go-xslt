package xslt

import "github.com/tim-riep/go-xslt/internal/xmltree"

// nextMatch implements xsl:next-match and xsl:apply-imports. When importsOnly is
// set (apply-imports), only candidate rules with strictly lower import
// precedence than the current rule are considered.
type nextMatch struct {
	params      []*VarDef
	el          *xmltree.Node
	importsOnly bool
}

func (*nextMatch) instr() {}

func init() {
	instrRegistry["next-match"] = func(c *compiler, el *xmltree.Node) (instruction, error) {
		_, params, err := c.compileSortsAndParams(el)
		if err != nil {
			return nil, err
		}
		return &nextMatch{params: params, el: el}, nil
	}
	instrRegistry["apply-imports"] = func(c *compiler, el *xmltree.Node) (instruction, error) {
		_, params, err := c.compileSortsAndParams(el)
		if err != nil {
			return nil, err
		}
		return &nextMatch{params: params, el: el, importsOnly: true}, nil
	}
}

func (n *nextMatch) exec(eng *engine, r rt, out *xmltree.Node) error {
	if len(eng.applyStack) == 0 {
		return errAt(n.el, "err:XTDE0560: xsl:%s used where the current template rule is absent", instrName(n))
	}
	f := eng.applyStack[len(eng.applyStack)-1]
	start := f.idx + 1
	if n.importsOnly {
		// XSLT §6.7: the rules xsl:apply-imports considers are those that were
		// imported, directly or indirectly, INTO THE MODULE containing the
		// current template rule — not simply every rule of lower precedence
		// anywhere in the stylesheet. cur.importLo..cur.importPrec is exactly
		// that module's import closure; a rule below importLo belongs to a
		// sibling import branch and is out of reach (import-1601: apply-imports
		// inside a module with no imports of its own falls straight through to
		// the built-in rule).
		cur := f.cands[f.idx]
		for start < len(f.cands) &&
			(f.cands[start].importPrec >= cur.importPrec || f.cands[start].importPrec < cur.importLo) {
			start++
		}
	}
	if start >= len(f.cands) {
		return eng.builtinTemplate(f.node, f.mode, out, n.params, r)
	}
	tunnelIn, err := eng.tunnelFrom(n.params, r)
	if err != nil {
		return err
	}
	return eng.invokeRule(f.cands, start, f.node, f.mode, r, out, n.params, tunnelIn, r)
}

func instrName(n *nextMatch) string {
	if n.importsOnly {
		return "apply-imports"
	}
	return "next-match"
}
