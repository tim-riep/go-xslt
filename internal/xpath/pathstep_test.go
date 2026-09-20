package xpath

import (
	"testing"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// A function call may appear as a relative-path step (XPath 3.0
// StepExpr = PostfixExpr | AxisStep), e.g. .../xs:decimal(child).
func TestFunctionCallPathStep(t *testing.T) {
	doc, _ := xmltree.Parse(`<r><v>1.5</v><v>2.5</v><v>3</v></r>`)
	cases := []struct{ expr, want string }{
		// cast each <v> to decimal and sum
		{"sum(/r/v/xs:decimal(.))", "7"},
		// upper-case() as the final step, joined
		{"string-join(/r/v/upper-case(string(.)), ',')", "1.5,2.5,3"},
		// number() per node
		{"count(/r/v/number(.))", "3"},
	}
	for _, c := range cases {
		got := ToString(evalStr(t, c.expr, doc))
		if got != c.want {
			t.Errorf("%s => %q, want %q", c.expr, got, c.want)
		}
	}
}
