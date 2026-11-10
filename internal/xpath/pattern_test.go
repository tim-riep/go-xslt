package xpath

import "testing"

func TestPatternElementsOnly(t *testing.T) {
	cases := map[string]bool{
		"order":                         true,
		"orders/order":                  true,
		"a//b":                          true,
		"*":                             true,
		"p:item":                        true,
		"*:item":                        true,
		"Q{urn:x}item":                  true,
		"item[@sku]":                    true,
		"item[position() = 1]":          true,
		"element()":                     true,
		"element(order)":                true,
		"//order":                       true,
		"/orders/order":                 true,
		"order | item":                  true,
		"@id":                           false,
		"order/@id":                     false,
		"text()":                        false,
		"node()":                        false,
		"order/node()":                  false,
		"comment()":                     false,
		"processing-instruction()":      false,
		"/":                             false,
		".":                             false,
		"self::order":                   false,
		"namespace-node()":              false,
		"document-node()":               false,
		"document-node(element(order))": false,
		"order | @id":                   false,
		".[. instance of xs:integer]":   false,
		"id('x')":                       false,
		"key('k', 'v')/order":           false, // a rooted pattern is judged conservatively
	}
	for src, want := range cases {
		p, err := ParsePattern(src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		if got := p.ElementsOnly(); got != want {
			t.Errorf("ElementsOnly(%q) = %v, want %v", src, got, want)
		}
	}
}
