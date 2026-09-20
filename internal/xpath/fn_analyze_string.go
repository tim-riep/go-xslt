package xpath

import (
	"fmt"
	"strconv"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

func init() {
	coreFuncs["analyze-string"] = fnAnalyzeString
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
	re, err := compileRegex(ToString(arg(a, 1)), flags)
	if err != nil {
		return nil, err
	}
	if re.MatchString("") {
		return nil, fmt.Errorf("err:FORX0003: the pattern matches a zero-length string")
	}
	root := xmltree.NewElement(asName("analyze-string-result"))
	doc := &xmltree.Node{Kind: xmltree.KindDocument}
	doc.Append(root)
	pos := 0
	for _, loc := range re.FindAllStringSubmatchIndex(input, -1) {
		if loc[0] > pos {
			nm := xmltree.NewElement(asName("non-match"))
			nm.Append(xmltree.NewText(input[pos:loc[0]]))
			root.Append(nm)
		}
		m := xmltree.NewElement(asName("match"))
		g := 1
		asGroups(m, input, loc, &g, loc[0], loc[1], true)
		root.Append(m)
		pos = loc[1]
	}
	if pos < len(input) {
		nm := xmltree.NewElement(asName("non-match"))
		nm.Append(xmltree.NewText(input[pos:]))
		root.Append(nm)
	}
	return NodeSet{root}, nil
}

// asGroups fills parent with input[from:to], wrapping every capturing group
// that lies inside that span in an fn:group element. *g is the next group
// number to consider; groups are numbered by opening parenthesis, so a group
// nested in another always follows it, and a group whose capture lies before
// pos (an earlier iteration of a repeated outer group) is skipped. An empty
// group sitting exactly at the span's end belongs to the match itself (top),
// never to the group that happens to end there.
func asGroups(parent *xmltree.Node, input string, loc []int, g *int, from, to int, top bool) {
	pos := from
	n := len(loc) / 2
	for *g < n {
		gs, ge := loc[2**g], loc[2**g+1]
		if gs < 0 || gs < pos {
			*g++
			continue
		}
		if ge > to || (gs == to && !top) {
			break
		}
		if gs > pos {
			parent.Append(xmltree.NewText(input[pos:gs]))
		}
		grp := xmltree.NewElement(asName("group"))
		grp.SetAttr(xmltree.Name{Local: "nr"}, strconv.Itoa(*g))
		*g++
		asGroups(grp, input, loc, g, gs, ge, false)
		parent.Append(grp)
		pos = ge
	}
	if to > pos {
		parent.Append(xmltree.NewText(input[pos:to]))
	}
}
