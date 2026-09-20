package xpath

import (
	"testing"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// Local bindings (for / let / some / every / inline-function params) must stay
// in scope inside EVERY nested evaluation: node-set predicates, atomic-sequence
// predicates, postfix-expression path steps, simple-map (!) right operands, and
// nested/chained predicates. This guards the class of bug where a sub-context
// was built without propagating ctx.locals.
func TestLocalVarScope(t *testing.T) {
	doc, _ := xmltree.Parse(`<r><cur>EUR</cur><amt c="EUR">10</amt><amt c="USD">20</amt><amt c="EUR">5</amt></r>`)
	cases := []struct {
		name, expr, want string
	}{
		// --- node-set predicate [ ... ] (the original EN16931 failure shape) ---
		{"quantified-in-predicate",
			"every $c in /r/cur satisfies count(/r/amt[@c = $c]) = 2", "true"},
		{"some-in-predicate",
			"some $c in /r/cur satisfies /r/amt[@c = $c] = '10'", "true"},
		{"for-in-predicate",
			"sum(for $c in /r/cur return /r/amt[@c = $c]/xs:decimal(.))", "15"},
		{"let-in-predicate",
			"let $k := 'USD' return /r/amt[@c = $k]/xs:decimal(.)", "20"},

		// --- atomic-sequence predicate (applyPredicateToItems) ---
		{"let-in-atomic-predicate",
			"let $n := 3 return (1 to 5)[. = $n]", "3"},
		{"for-in-atomic-predicate",
			"string-join(for $n in (2, 4) return (1 to 5)[. = $n], ',')", "2,4"},

		// --- postfix-expression path step using a local var ---
		{"let-in-postfix-step",
			"let $k := '!' return string-join(/r/amt/concat(@c, $k), ',')", "EUR!,USD!,EUR!"},

		// --- simple map (!) right operand using a local var ---
		{"let-in-simple-map",
			"let $k := 10 return string-join((1, 2, 3) ! string(. + $k), ',')", "11,12,13"},

		// --- nested / chained predicates keep the outer var ---
		{"for-in-chained-predicate",
			"string-join(for $i in (1, 2) return /r/amt[@c = 'EUR'][$i]/xs:decimal(.), ',')", "10,5"},

		// --- inline-function captured var used inside a predicate ---
		{"inline-capture-in-predicate",
			"let $k := 'EUR' return string-join(for-each(/r/amt, function($a) { $a[@c = $k]/string(.) }), ',')", "10,5"},
	}
	for _, c := range cases {
		got := ToString(evalStr(t, c.expr, doc))
		if got != c.want {
			t.Errorf("%s: %s => %q, want %q", c.name, c.expr, got, c.want)
		}
	}
}
