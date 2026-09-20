package xpath

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

func init() {
	coreFuncs["has-children"] = fnHasChildren
	coreFuncs["innermost"] = fnInnermost
	coreFuncs["outermost"] = fnOutermost
	coreFuncs["path"] = fnPath
}

// ndNodeArg returns the node argument at index i, defaulting to the context
// node when the argument is absent.
func ndNodeArg(c *Context, a []Object, i int) *xmltree.Node {
	if i < len(a) {
		ns, _ := ToNodeSet(a[i])
		return ns.first()
	}
	return c.Node
}

// ndNodes flattens an Object argument into the list of *xmltree.Node items it
// contains, preserving order. Non-node items are skipped.
func ndNodes(o Object) []*xmltree.Node {
	var out []*xmltree.Node
	for _, it := range Items(o) {
		if n, ok := it.(*xmltree.Node); ok {
			out = append(out, n)
		}
	}
	return out
}

// ndSortDocOrder sorts nodes into document order, removing duplicates by
// identity.
func ndSortDocOrder(nodes []*xmltree.Node) []*xmltree.Node {
	seen := map[*xmltree.Node]bool{}
	uniq := make([]*xmltree.Node, 0, len(nodes))
	for _, n := range nodes {
		if !seen[n] {
			seen[n] = true
			uniq = append(uniq, n)
		}
	}
	// The same ordering every node-set operation uses (fn-innermost-054
	// deep-equals innermost() against an "except" over two documents).
	sort.SliceStable(uniq, func(i, j int) bool { return docOrderBefore(uniq[i], uniq[j]) })
	return uniq
}

// ndHasAncestorIn reports whether any proper ancestor of n is present in set.
func ndHasAncestorIn(n *xmltree.Node, set map[*xmltree.Node]bool) bool {
	for cur := n.Parent; cur != nil; cur = cur.Parent {
		if set[cur] {
			return true
		}
	}
	return false
}

// fnHasChildren implements fn:has-children: true when the node has at least one
// child node.
func fnHasChildren(c *Context, a []Object) (Object, error) {
	if len(a) > 0 {
		items := Items(a[0])
		if len(items) == 0 {
			return NewBool(false), nil
		}
		if _, ok := items[0].(*xmltree.Node); !ok || len(items) > 1 {
			return nil, fmt.Errorf("err:XPTY0004: fn:has-children requires a single node")
		}
	} else if c.Node == nil {
		return nil, fmt.Errorf("err:XPDY0002: fn:has-children has no context node")
	}
	n := ndNodeArg(c, a, 0)
	if n == nil {
		return NewBool(false), nil
	}
	return NewBool(len(n.Children) > 0), nil
}

// fnInnermost implements fn:innermost: the nodes in the input that have no
// descendant in the input set — i.e. nodes with no ancestor in the set are
// dropped only when they themselves contain another input node. Equivalently,
// keep nodes that are not ancestors of any other node in the input.
// requireAllNodes reports XPTY0004 if any item in o is not a node (fn:innermost
// and fn:outermost are declared over node()*).
func requireAllNodes(o Object, fn string) error {
	for _, it := range Items(o) {
		if _, ok := it.(*xmltree.Node); !ok {
			return fmt.Errorf("err:XPTY0004: fn:%s expects node()*, got %T", fn, it)
		}
	}
	return nil
}

func fnInnermost(c *Context, a []Object) (Object, error) {
	if err := requireAllNodes(arg(a, 0), "innermost"); err != nil {
		return nil, err
	}
	nodes := ndSortDocOrder(ndNodes(arg(a, 0)))
	var out NodeSet
	for _, n := range nodes {
		// n is innermost if no other input node is a descendant of n.
		isAncestor := false
		self := map[*xmltree.Node]bool{n: true}
		for _, m := range nodes {
			if m == n {
				continue
			}
			if ndHasAncestorIn(m, self) {
				isAncestor = true
				break
			}
		}
		if !isAncestor {
			out = append(out, n)
		}
	}
	return out, nil
}

// fnOutermost implements fn:outermost: the nodes in the input that have no
// ancestor in the input set (keep the topmost nodes, drop those nested inside
// another input node).
func fnOutermost(c *Context, a []Object) (Object, error) {
	if err := requireAllNodes(arg(a, 0), "outermost"); err != nil {
		return nil, err
	}
	nodes := ndSortDocOrder(ndNodes(arg(a, 0)))
	set := map[*xmltree.Node]bool{}
	for _, n := range nodes {
		set[n] = true
	}
	var out NodeSet
	for _, n := range nodes {
		if !ndHasAncestorIn(n, set) {
			out = append(out, n)
		}
	}
	return out, nil
}

// ndSiblingPosition returns the 1-based position of n among its same-kind,
// same-name siblings (used to build the predicate in fn:path).
func ndSiblingPosition(n *xmltree.Node) int {
	if n.Parent == nil {
		return 1
	}
	pos := 0
	for _, sib := range ndChildNodes(n.Parent) {
		if ndSameStep(sib, n) {
			pos++
		}
		if sib == n {
			return pos
		}
	}
	return pos
}

// ndChildNodes returns the child nodes of n that participate in the path
// (elements, text, comments, PIs in document order).
func ndChildNodes(n *xmltree.Node) []*xmltree.Node {
	return n.Children
}

// ndSameStep reports whether two sibling nodes belong to the same path step
// group (same kind and, for named kinds, same expanded name).
func ndSameStep(a, b *xmltree.Node) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case xmltree.KindElement:
		return a.Name.Local == b.Name.Local && a.Name.Space == b.Name.Space
	case xmltree.KindPI:
		return a.Name.Local == b.Name.Local
	default:
		return true
	}
}

// ndStepLabel returns the path step label for a single node, e.g.
// "Q{ns}local", "text()", "comment()", or "processing-instruction(target)".
func ndStepLabel(n *xmltree.Node) string {
	switch n.Kind {
	case xmltree.KindElement:
		return "Q{" + n.Name.Space + "}" + n.Name.Local
	case xmltree.KindAttribute:
		if n.Name.Space == "" {
			return "@" + n.Name.Local // no-namespace attributes are written @local (path005)
		}
		return "@Q{" + n.Name.Space + "}" + n.Name.Local
	case xmltree.KindText:
		return "text()"
	case xmltree.KindComment:
		return "comment()"
	case xmltree.KindPI:
		return "processing-instruction(" + n.Name.Local + ")"
	case xmltree.KindNamespace:
		if n.Name.Local == "" {
			// The default-namespace node has no name (path013).
			return `namespace::*[Q{http://www.w3.org/2005/xpath-functions}local-name()=""]`
		}
		return "namespace::" + n.Name.Local
	default:
		return ""
	}
}

// fnPath implements fn:path: a canonical path expression locating the node from
// the root of its tree. The document node yields "/". Attributes are addressed
// with "@". Positional predicates ([n]) are always emitted for element/text/
// comment/PI steps.
func fnPath(c *Context, a []Object) (Object, error) {
	n := ndNodeArg(c, a, 0)
	if n == nil {
		return Sequence{}, nil
	}
	if n.Kind == xmltree.KindDocument {
		return NewString("/"), nil
	}
	// A tree whose root is NOT a document node cannot be addressed from "/":
	// F&O 3.1 names its root with a call to fn:root() instead, and the root
	// node itself contributes no step (accumulator-088 asks for the path of a
	// parentless element, which is exactly the bare fn:root() call;
	// accessor-058..064 take fn:path of such a parentless element and its
	// descendants). effectiveRoot / baseURIParent, not the raw Root()/Parent:
	// a node extracted from an @as-typed collector is PARENTLESS in the XDM
	// model even though it still carries a physical link to the throwaway
	// document that gathered it.
	root := effectiveRoot(n)
	rooted := root.Kind == xmltree.KindDocument
	var steps []string
	for cur := n; cur != nil && cur.Kind != xmltree.KindDocument; cur = baseURIParent(cur) {
		if !rooted && cur == root {
			break
		}
		label := ndStepLabel(cur)
		if cur.Kind == xmltree.KindAttribute || cur.Kind == xmltree.KindNamespace {
			steps = append(steps, label)
			continue
		}
		steps = append(steps, label+"["+strconv.Itoa(ndSiblingPosition(cur))+"]")
	}
	// steps were collected leaf-to-root; reverse them.
	for i, j := 0, len(steps)-1; i < j; i, j = i+1, j-1 {
		steps[i], steps[j] = steps[j], steps[i]
	}
	if rooted {
		return NewString("/" + strings.Join(steps, "/")), nil
	}
	const rootCall = "Q{http://www.w3.org/2005/xpath-functions}root()"
	if len(steps) == 0 {
		return NewString(rootCall), nil
	}
	return NewString(rootCall + "/" + strings.Join(steps, "/")), nil
}
