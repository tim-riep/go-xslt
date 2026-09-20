package xpath

import "testing"

func TestMaMapArrayFuncs(t *testing.T) {
	cases := []struct{ expr, want string }{
		// map:find — collect matching values into an array.
		{`array:size(map:find([map{'x':1}, map{'x':2}, map{'y':3}], 'x'))`, "2"},
		{`array:get(map:find([map{'x':1}, map{'x':2}], 'x'), 2)`, "2"},

		// array:subarray
		{`array:get(array:subarray([1,2,3,4], 2), 1)`, "2"},
		{`array:size(array:subarray([1,2,3,4], 2, 2))`, "2"},
		{`array:get(array:subarray([1,2,3,4], 2, 2), 2)`, "3"},

		// array:remove
		{`array:size(array:remove([1,2,3], 2))`, "2"},
		{`array:get(array:remove([1,2,3], 2), 2)`, "3"},
		{`array:size(array:remove([1,2,3,4], (1,3)))`, "2"},

		// array:insert-before
		{`array:size(array:insert-before([1,2,3], 2, 9))`, "4"},
		{`array:get(array:insert-before([1,2,3], 2, 9), 2)`, "9"},
		{`array:get(array:insert-before([1,2,3], 4, 9), 4)`, "9"},

		// array:head / array:tail
		{`array:head(['a','b','c'])`, "a"},
		{`array:size(array:tail(['a','b','c']))`, "2"},
		{`array:get(array:tail(['a','b','c']), 1)`, "b"},

		// array:reverse
		{`array:get(array:reverse([1,2,3]), 1)`, "3"},
		{`string-join(array:flatten(array:reverse([1,2,3])), ',')`, "3,2,1"},

		// array:filter
		{`array:size(array:filter([1,2,3,4], function($x) { $x mod 2 = 0 }))`, "2"},
		{`array:get(array:filter([1,2,3,4], function($x) { $x mod 2 = 0 }), 2)`, "4"},

		// array:fold-left / fold-right
		{`array:fold-left([1,2,3,4], 0, function($a,$b) { $a + $b })`, "10"},
		{`array:fold-right([1,2,3,4], 0, function($a,$b) { $a + $b })`, "10"},
		{`array:fold-left(['a','b','c'], '', function($a,$b) { concat($a,$b) })`, "abc"},

		// array:for-each-pair
		{`array:size(array:for-each-pair([1,2,3], [10,20,30], function($a,$b) { $a + $b }))`, "3"},
		{`array:get(array:for-each-pair([1,2,3], [10,20,30], function($a,$b) { $a + $b }), 2)`, "22"},
		{`array:get(array:for-each-pair([1,2], [10,20,30], function($a,$b) { $a + $b }), 2)`, "22"},

		// array:sort
		{`array:get(array:sort([3,1,2]), 1)`, "1"},
		{`string-join(array:flatten(array:sort([3,1,2])), ',')`, "1,2,3"},
		{`array:get(array:sort([3,1,2], (), function($x) { -$x }), 1)`, "3"},
	}
	for _, c := range cases {
		if got := xpStr(t, c.expr); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}
