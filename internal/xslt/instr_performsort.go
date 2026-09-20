package xslt

import (
	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// performSort implements xsl:perform-sort. The instruction sorts an input
// sequence according to its child xsl:sort keys and emits the sorted nodes.
//
// The input sequence is obtained from the optional @select attribute. When
// @select is absent the contained sequence-constructor body is executed into a
// temporary fragment and its children form the input node sequence. The sorted
// nodes are appended (deep-copied) to the result tree.
type performSort struct {
	sel   *xpath.Parsed // optional @select
	sorts []sortKey
	body  []instruction
	el    *xmltree.Node
}

func (*performSort) instr() {}

func init() {
	instrRegistry["perform-sort"] = psCompile
}

func psCompile(c *compiler, el *xmltree.Node) (instruction, error) {
	n := &performSort{el: el}
	if s, ok := el.AttrLocal("select"); ok {
		p, err := parseXPathFor(el, s)
		if err != nil {
			return nil, errAt(el, "bad select %q: %v", s, err)
		}
		n.sel = p
	}
	sorts, _, err := c.compileSortsAndParams(el)
	if err != nil {
		return nil, err
	}
	n.sorts = sorts
	// The body (sequence constructor) supplies the input when @select is absent.
	// It contains only non-sort children.
	body, err := c.compileSequence(nonSortChildren(el))
	if err != nil {
		return nil, err
	}
	n.body = body
	return n, nil
}

func (n *performSort) exec(eng *engine, r rt, out *xmltree.Node) error {
	nodes, err := psInput(n, eng, r)
	if err != nil {
		return err
	}
	if len(n.sorts) > 0 {
		nodes, err = eng.sortNodes(nodes, n.sorts, n.el, r)
		if err != nil {
			return err
		}
	}
	for _, node := range nodes {
		// A synthetic atomic-value wrapper (psInput) merges with its
		// neighbors like any other sequence-constructor atomic value, so
		// adjacent sorted strings get the usual single-space separator
		// (collations-0601: perform-sort over tokenize() results) instead of
		// running together as one word.
		if node.Kind == xmltree.KindText && node.Atomic {
			appendAtomicText(out, node.Value)
			continue
		}
		deepCopyInto(node, out)
	}
	return nil
}

// psInput resolves the input node sequence: either by evaluating @select (with
// atomic items modelled as synthetic text nodes, matching for-each semantics),
// or by executing the body into a temporary fragment and taking its children.
func psInput(n *performSort, eng *engine, r rt) ([]*xmltree.Node, error) {
	if n.sel != nil {
		v, err := eng.eval(n.sel, n.el, r)
		if err != nil {
			return nil, err
		}
		items := xpath.Items(v)
		nodes := make([]*xmltree.Node, len(items))
		for i, it := range items {
			if nd, ok := it.(*xmltree.Node); ok {
				nodes[i] = nd
				continue
			}
			nd := &xmltree.Node{Kind: xmltree.KindText, Value: xpath.ToString(xpath.FromItems([]xpath.Item{it})), Atomic: true}
			// Preserve the item's real atomic type (mirrors execForEach's
			// identical synthetic-node construction) so a sort key of "."
			// evaluated against this node — as xsl:sort's own @data-type
			// omitted-default inference does — still sees a genuinely
			// numeric/date/etc. value rather than a plain untyped string
			// (sort-062: xsl:perform-sort over a numeric xs:anyAtomicType*
			// sequence, no @data-type, must still sort numerically, not
			// lexicographically).
			if tag, ok := xpath.ItemAtomTypeTag(it); ok {
				nd.TypeAnno = tag
			}
			attachRealItem(nd, it)
			nodes[i] = nd
		}
		return nodes, nil
	}
	frag := &xmltree.Node{Kind: xmltree.KindDocument}
	if err := eng.execSequence(n.body, r, frag); err != nil {
		return nil, err
	}
	return frag.Children, nil
}
