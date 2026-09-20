package xpath

import "testing"

func TestFnJSON(t *testing.T) {
	cases := []struct{ expr, want string }{
		// parse-json: scalar values are atomized via ToString.
		{`parse-json('"hello"')`, "hello"},
		{`parse-json('42')`, "42"},
		{`parse-json('true')`, "true"},
		{`parse-json('false')`, "false"},
		// null becomes the empty sequence.
		{`string(parse-json('null'))`, ""},
		// parse-json of an object: pull a key value out via map:get.
		{`map:get(parse-json('{"a":"x","b":"y"}'), 'a')`, "x"},
		// parse-json of an array: pull a member via array:get.
		{`array:get(parse-json('[1,2,3]'), 2)`, "2"},
		// parse-json parses a JSON string directly (json-doc now fetches a
		// resource via the host resolver — see fnJSJSONDoc).
		{`parse-json('"hi"')`, "hi"},
		{`map:get(parse-json('{"k":"v"}'), 'k')`, "v"},
		// serialize of an atomic value yields its string form.
		{`serialize('abc')`, "abc"},
		{`serialize(7)`, "7"},
		// empty input to parse-json / json-doc yields empty sequence.
		{`string(parse-json(()))`, ""},
		// xml-to-json round-trips a json-to-xml tree.
		{`xml-to-json(json-to-xml('{"a":1}'))`, `{"a":1}`},
		{`xml-to-json(json-to-xml('"s"'))`, `"s"`},
		{`xml-to-json(json-to-xml('true'))`, `true`},
		{`xml-to-json(json-to-xml('null'))`, `null`},
		{`xml-to-json(json-to-xml('[1,2]'))`, `[1,2]`},
	}
	for _, c := range cases {
		if got := xpStr(t, c.expr); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}
