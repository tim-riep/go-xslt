package xsd

import (
	"sort"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// xs:override (XSD 1.1 §4.2.5), implemented as the spec's override
// transformation (the suite ships the same transform as
// saxonData/Override/override-transform.xsl): the overridden document is
// rewritten — matching top-level components replaced, xs:include rewritten to
// an xs:override carrying the override's children, nested xs:override merged
// (replace matching, keep the rest, APPEND unmatched), xs:redefine children
// replaced in place, xs:import kept verbatim — and the rewritten document is
// then collected as usual. Unmatched override children are DISCARDED at the
// schema level (over026: a reference to a discarded component is an error).

// overrideComponents returns the component children of an xs:override element
// (the xs-namespace element children carrying @name; annotations excluded).
func overrideComponents(ov *xmltree.Node) []*xmltree.Node {
	var out []*xmltree.Node
	for _, ch := range ov.Children {
		if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS {
			continue
		}
		if _, ok := ch.AttrLocal("name"); ok && ch.Name.Local != "annotation" {
			out = append(out, ch)
		}
	}
	return out
}

// checkOverrideUniqueness enforces src-override: the {kind, name} identities
// of one override's children must be distinct (over021).
func checkOverrideUniqueness(ov *xmltree.Node) error {
	seen := map[string]bool{}
	for _, ch := range overrideComponents(ov) {
		n, _ := ch.AttrLocal("name")
		key := ch.Name.Local + " " + applyWhiteSpace("collapse", n)
		if seen[key] {
			return invalidf("src-override", "duplicate %s %q in one xs:override", ch.Name.Local, n)
		}
		seen[key] = true
	}
	return nil
}

// cloneSchemaTree deep-copies a node (attributes, namespace nodes, children)
// with correct parent pointers.
func cloneSchemaTree(n *xmltree.Node, parent *xmltree.Node) *xmltree.Node {
	cp := *n
	cp.Parent = parent
	cp.Attrs = make([]*xmltree.Node, len(n.Attrs))
	for i, a := range n.Attrs {
		ac := *a
		ac.Parent = &cp
		cp.Attrs[i] = &ac
	}
	cp.NS = make([]*xmltree.Node, len(n.NS))
	for i, ns := range n.NS {
		nc := *ns
		nc.Parent = &cp
		cp.NS[i] = &nc
	}
	cp.Children = make([]*xmltree.Node, len(n.Children))
	for i, ch := range n.Children {
		cp.Children[i] = cloneSchemaTree(ch, &cp)
	}
	return &cp
}

// transplantClone clones src for insertion under parent in ANOTHER document,
// materializing src's in-scope namespace bindings onto the clone so QName
// attribute values keep resolving with the OVERRIDING document's prefixes
// (and never fall through to the host document's).
func transplantClone(src *xmltree.Node, parent *xmltree.Node) *xmltree.Node {
	cp := cloneSchemaTree(src, parent)
	have := map[string]bool{}
	for _, ns := range cp.NS {
		have[ns.Name.Local] = true
	}
	inscope := src.InScopeNamespaces()
	if _, ok := inscope[""]; !ok {
		inscope[""] = "" // pin "no default namespace" against the host's default
	}
	for pfx, uri := range inscope {
		if have[pfx] {
			continue
		}
		cp.NS = append(cp.NS, &xmltree.Node{
			Kind: xmltree.KindNamespace,
			Name: xmltree.Name{Local: pfx}, Value: uri, Parent: cp,
		})
	}
	return cp
}

// ovMatch finds the override component matching n by {kind, name} — the same
// element name and a whitespace-collapsed-equal @name. (The reference
// transform compares QName(tns, @name); the tns halves are already forced
// equal-or-chameleon by directiveTnsOK.)
func ovMatch(comps []*xmltree.Node, n *xmltree.Node) *xmltree.Node {
	if n.Kind != xmltree.KindElement || n.Name.Space != xsNS {
		return nil
	}
	name, ok := n.AttrLocal("name")
	if !ok {
		return nil
	}
	name = applyWhiteSpace("collapse", name)
	for _, c := range comps {
		if c.Name.Local != n.Name.Local {
			continue
		}
		cn, _ := c.AttrLocal("name")
		if applyWhiteSpace("collapse", cn) == name {
			return c
		}
	}
	return nil
}

// applyOverride returns the §4.2.5-transformed deep copy of target (an
// xs:schema element) under override element ov.
func applyOverride(target, ov *xmltree.Node) *xmltree.Node {
	comps := overrideComponents(ov)
	cp := cloneSchemaTree(target, target.Parent)
	newKids := make([]*xmltree.Node, 0, len(cp.Children))
	for _, ch := range cp.Children {
		if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS {
			newKids = append(newKids, ch)
			continue
		}
		switch ch.Name.Local {
		case "import":
			newKids = append(newKids, ch) // verbatim
		case "include":
			// include → override carrying ALL of ov's children (over020's
			// chameleon chain relies on this rewriting).
			nov := &xmltree.Node{Kind: xmltree.KindElement,
				Name: xmltree.Name{Space: xsNS, Local: "override"}, Parent: cp}
			if loc, ok := ch.AttrLocal("schemaLocation"); ok {
				nov.Attrs = append(nov.Attrs, &xmltree.Node{Kind: xmltree.KindAttribute,
					Name: xmltree.Name{Local: "schemaLocation"}, Value: loc, Parent: nov})
			}
			for _, oc := range ov.Children {
				if oc.Kind == xmltree.KindElement {
					nov.Children = append(nov.Children, transplantClone(oc, nov))
				}
			}
			newKids = append(newKids, nov)
		case "override":
			// Merge: replace matching children, keep the rest, then APPEND the
			// ov components matching none of the ORIGINAL children (over009;
			// over009temp.xsd is the hand-computed expected output).
			nov := &xmltree.Node{Kind: xmltree.KindElement,
				Name: xmltree.Name{Space: xsNS, Local: "override"}, Parent: cp}
			if loc, ok := ch.AttrLocal("schemaLocation"); ok {
				nov.Attrs = append(nov.Attrs, &xmltree.Node{Kind: xmltree.KindAttribute,
					Name: xmltree.Name{Local: "schemaLocation"}, Value: loc, Parent: nov})
			}
			for _, sub := range ch.Children {
				if m := ovMatch(comps, sub); m != nil {
					nov.Children = append(nov.Children, transplantClone(m, nov))
				} else {
					sub.Parent = nov
					nov.Children = append(nov.Children, sub)
				}
			}
			for _, c := range comps {
				if ovMatch(overrideComponents(ch), c) == nil {
					nov.Children = append(nov.Children, transplantClone(c, nov))
				}
			}
			newKids = append(newKids, nov)
		case "redefine":
			// Children replaced in place; nothing appended.
			for i, sub := range ch.Children {
				if m := ovMatch(comps, sub); m != nil {
					ch.Children[i] = transplantClone(m, ch)
				}
			}
			newKids = append(newKids, ch)
		default:
			if m := ovMatch(comps, ch); m != nil {
				newKids = append(newKids, transplantClone(m, cp))
			} else {
				newKids = append(newKids, ch)
			}
		}
	}
	cp.Children = newKids
	return cp
}

// ovSignature is an order-insensitive canonical fingerprint of an override's
// element children, used to key the collection dedup: the same document
// reached under the SAME effective override collects once, while a different
// override of the same path is a fresh collection (over022's duplicate), and
// the merge fixpoint in override cycles terminates (over023/over024).
func ovSignature(ov *xmltree.Node) string {
	var parts []string
	for _, ch := range ov.Children {
		if ch.Kind != xmltree.KindElement {
			continue
		}
		var b strings.Builder
		sigNode(&b, ch)
		parts = append(parts, b.String())
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x02")
}

func sigNode(b *strings.Builder, n *xmltree.Node) {
	switch n.Kind {
	case xmltree.KindText:
		if v := strings.TrimSpace(n.Value); v != "" {
			b.WriteString("t:")
			b.WriteString(v)
			b.WriteByte('\x01')
		}
	case xmltree.KindElement:
		b.WriteString("e:")
		b.WriteString(n.Name.Space)
		b.WriteByte('|')
		b.WriteString(n.Name.Local)
		attrs := make([]string, 0, len(n.Attrs))
		for _, a := range n.Attrs {
			attrs = append(attrs, a.Name.Space+"|"+a.Name.Local+"="+a.Value)
		}
		sort.Strings(attrs)
		for _, a := range attrs {
			b.WriteByte('\x01')
			b.WriteString(a)
		}
		b.WriteByte('(')
		for _, ch := range n.Children {
			sigNode(b, ch)
		}
		b.WriteByte(')')
	}
}
