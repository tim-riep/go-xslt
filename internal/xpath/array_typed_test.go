package xpath

import "testing"

// TestArrayMemberTypeMemo: an array built by Append remembers, on the claim
// its lineage shares, how many leading members passed an array(T) test;
// the memo must never admit a member that does not match, for a longer or
// a shorter array of the lineage, or after a different T was asked.
func TestArrayMemberTypeMemo(t *testing.T) {
	// The array(T) test is reached through the context-taking matcher (the
	// no-context shortcut answers only for atomic and item() types).
	matches := func(as string, v Object) bool {
		st := declaredType(as, nil)
		if st == nil {
			t.Fatalf("cannot parse %q", as)
		}
		return MatchesSeqTypeCtx(st, v, nil)
	}
	a := NewArray(nil)
	for i := 0; i < 50; i++ {
		a = a.Append(NewString("s"))
	}
	if !matches("array(xs:string)", a) {
		t.Fatal("50 strings: want array(xs:string)")
	}
	if a.claim.typedN != 50 || a.claim.typedAtom != XSstring {
		t.Fatalf("memo = (%d, %v), want (50, xs:string)", a.claim.typedN, a.claim.typedAtom)
	}
	longer := a.Append(NewString("t")).Append(NewInteger(1))
	if matches("array(xs:string)", longer) {
		t.Fatal("an appended integer must fail array(xs:string)")
	}
	if !matches("array(xs:anyAtomicType)", longer) {
		t.Fatal("want array(xs:anyAtomicType)")
	}
	// The shorter array of the same lineage still matches, whatever the
	// memo now says.
	if !matches("array(xs:string)", a) {
		t.Fatal("50 strings after a failed check of a longer array: want array(xs:string)")
	}
	if matches("array(xs:integer)", a) {
		t.Fatal("strings are not array(xs:integer)")
	}
	// A sequence member, an empty member and a node member are not one
	// atomic item.
	b := NewArray(nil).Append(NewString("x")).Append(Sequence{})
	if matches("array(xs:string)", b) {
		t.Fatal("an empty member must fail array(xs:string)")
	}
	if !matches("array(xs:string?)", b) {
		t.Fatal("array(xs:string?) admits an empty member")
	}
	c := NewArray(nil).Append(Sequence{NewString("x"), NewString("y")})
	if matches("array(xs:string)", c) {
		t.Fatal("a two-item member must fail array(xs:string)")
	}
	// Without a claim (not built by Append) the plain loop decides.
	d := NewArray([]Object{NewString("x"), NewInteger(2)})
	if matches("array(xs:string)", d) || !matches("array(xs:anyAtomicType)", d) {
		t.Fatal("plain array: wrong verdicts")
	}
}
