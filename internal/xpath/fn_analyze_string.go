package xpath

import (
	"fmt"
	"strconv"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

func init() {
	coreFuncs["analyze-string"] = fnAnalyzeString
}

// asTyped marks an element of the analyze-string result tree with the type the
// fn:analyze-string-result schema (F&O 3.1 §5.6.5) gives it. All of them are
// declared with anonymous complex types, so none is xs:untyped; the closest
// named type for them is xs:anyType.
func asTyped(el *xmltree.Node) *xmltree.Node {
	el.SetSchemaType(&xmltree.SchemaTypeName{Namespace: nsXS, Local: "anyType", Complex: true})
	return el
}

// asName builds a name in the fn namespace for the analyze-string result tree.
func asName(local string) xmltree.Name {
	return xmltree.Name{Space: jsXFNS, Local: local, Prefix: "fn"}
}

// fnAnalyzeString implements fn:analyze-string($input, $pattern[, $flags]): an
// fn:analyze-string-result element whose fn:match / fn:non-match children
// partition $input in order, each match carrying its participating capturing
// groups as (nested) fn:group elements with an nr attribute. An empty $input
// yields an empty result element (analyzeString-002a/022/023).
func fnAnalyzeString(c *Context, a []Object) (Object, error) {
	input := ""
	if !jsIsEmpty(arg(a, 0)) {
		input = ToString(arg(a, 0))
	}
	flags := ""
	if len(a) > 2 {
		flags = ToString(arg(a, 2))
	}
	re, matchesEmpty, err := compileRegexEmpty(ToString(arg(a, 1)), flags)
	if err != nil {
		return nil, err
	}
	if matchesEmpty {
		return nil, fmt.Errorf("err:FORX0003: the pattern matches a zero-length string")
	}
	parents := asGroupParents(ToString(arg(a, 1)), flags)
	root := asTyped(xmltree.NewElement(asName("analyze-string-result")))
	doc := &xmltree.Node{Kind: xmltree.KindDocument}
	doc.Append(root)
	pos := 0
	for _, loc := range re.FindAllStringSubmatchIndex(input, -1) {
		if loc[0] > pos {
			nm := asTyped(xmltree.NewElement(asName("non-match")))
			nm.Append(xmltree.NewText(input[pos:loc[0]]))
			root.Append(nm)
		}
		m := asTyped(xmltree.NewElement(asName("match")))
		g := 1
		asGroups(m, input, loc, parents, &g, 0, loc[0], loc[1])
		root.Append(m)
		pos = loc[1]
	}
	if pos < len(input) {
		nm := asTyped(xmltree.NewElement(asName("non-match")))
		nm.Append(xmltree.NewText(input[pos:]))
		root.Append(nm)
	}
	return NodeSet{root}, nil
}

// asGroups fills parent (capturing group cur, 0 for the match itself) with
// input[from:to], wrapping every capturing group nested directly in cur that
// took part in the match in an fn:group element. *g is the next group number
// to consider; groups are numbered by opening parenthesis, so the groups
// nested in cur follow it directly and the first one that is not ends cur. A
// group whose capture lies outside the span (an earlier iteration of a
// repeated outer group) is skipped. Nesting comes from the pattern
// (parents), not from the positions: (a)(b?) and (a(b?)) capture the same
// spans of "a", but only in the second is the empty group 2 inside group 1.
func asGroups(parent *xmltree.Node, input string, loc, parents []int, g *int, cur, from, to int) {
	pos := from
	n := len(loc) / 2
	for *g < n && asWithin(parents, *g, cur) {
		gs, ge := loc[2**g], loc[2**g+1]
		if gs < 0 || gs < pos || ge > to {
			*g++
			continue
		}
		if gs > pos {
			parent.Append(xmltree.NewText(input[pos:gs]))
		}
		grp := asTyped(xmltree.NewElement(asName("group")))
		grp.SetAttr(xmltree.Name{Local: "nr"}, strconv.Itoa(*g))
		for _, at := range grp.Attrs {
			at.SetTypeAnno(int32(XSpositiveInteger)) // nr is xs:positiveInteger in the schema
		}
		self := *g
		*g++
		asGroups(grp, input, loc, parents, g, self, gs, ge)
		parent.Append(grp)
		pos = ge
	}
	if to > pos {
		parent.Append(xmltree.NewText(input[pos:to]))
	}
}

// asWithin reports whether capturing group g lies inside group cur (0: the
// whole match, which contains every group).
func asWithin(parents []int, g, cur int) bool {
	for g > 0 {
		if g == cur {
			return true
		}
		if g >= len(parents) {
			return false
		}
		g = parents[g]
	}
	return cur == 0
}

// asGroupParents returns, for every capturing group of an XPath regex, the
// number of the capturing group it is nested in (0 for none), read off the
// pattern's parentheses: an escaped character and a character class (which
// may nest through subtraction, [a-z-[aeiou]]) hold none, and (?:...) is a
// group that captures nothing. With the q flag nothing is a group.
func asGroupParents(pattern, flags string) []int {
	parents := []int{0}
	for _, f := range flags {
		if f == 'q' {
			return parents
		}
	}
	var open []int // enclosing groups, innermost last; 0 = non-capturing
	class := 0
	rs := []rune(pattern)
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; {
		case r == '\\':
			i++
		case r == '[':
			class++
		case r == ']' && class > 0:
			class--
		case class > 0:
		case r == '(':
			if i+2 < len(rs) && rs[i+1] == '?' && rs[i+2] == ':' {
				open = append(open, 0)
				i += 2
				continue
			}
			enclosing := 0
			for j := len(open) - 1; j >= 0; j-- {
				if open[j] != 0 {
					enclosing = open[j]
					break
				}
			}
			parents = append(parents, enclosing)
			open = append(open, len(parents)-1)
		case r == ')' && len(open) > 0:
			open = open[:len(open)-1]
		}
	}
	return parents
}
