package xpath

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
)

// mkey is the op:same-key canonical form of a map key: a category plus a
// value form, so that equal-value keys of compatible types collapse (all
// string-family keys; all numeric keys with 1 == 1.0 and NaN == NaN) while
// keys of different categories stay distinct (a QName "abc" is not the
// string "abc"; the number 1.5 is not the string "1.5"). It used to be one
// string with the category as a prefix, which cost a concatenation per map
// operation on a string key; a string key's form is now its own string.
type mkey struct {
	cat byte // 'n' numeric, 'b' boolean, 's' string family, 'q' QName, 'd' duration, 'x' the rest
	s   string
}

// mapKey produces the canonical form of an atomic map key.
func mapKey(it Item) mkey {
	// Numeric keys compare in the EXACT value space: op:same-key converts a
	// double/float to xs:decimal without loss, so xs:double("1.1") is not the
	// same key as xs:decimal("1.1") (same-key-007, map-put-023, map-remove-016)
	// while 1, 1.0 and 1e0 are one key. The canonical form is the reduced
	// rational (big.Rat.SetFloat64 is exact).
	num := func(f float64) mkey {
		switch {
		case math.IsNaN(f):
			return mkey{'n', "NaN"} // every NaN key is the same key
		case math.IsInf(f, 1):
			return mkey{'n', "INF"}
		case math.IsInf(f, -1):
			return mkey{'n', "-INF"}
		}
		return mkey{'n', new(big.Rat).SetFloat64(f).RatString()}
	}
	switch v := it.(type) {
	case *Atomic:
		switch {
		case isIntegerType(v.T) && v.i != nil:
			return mkey{'n', v.i.String()}
		case v.T == XSdecimal && v.d != nil:
			return mkey{'n', v.d.RatString()}
		case v.IsNumeric():
			return num(v.Float())
		case v.T == XSboolean:
			if v.b {
				return mkey{'b', "1"}
			}
			return mkey{'b', "0"}
		case v.T == XSqname || v.T == XSnotation:
			return mkey{'q', "{" + v.qnv().Space + "}" + v.qnv().Local}
		case v.T == XSstring || v.T == XSanyURI || v.T == XSuntypedAtomic || isStringType(v.T):
			return mkey{'s', v.Lexical()}
		case isDurationType(v.T):
			// Durations are the same key by VALUE, across subtypes and lexical
			// forms (P1Y and P12M are one key; map-get-017).
			return mkey{'d', strconv.Itoa(v.durv().Months) + "/" + strconv.FormatFloat(v.durv().Secs, 'g', -1, 64)}
		case isDateTimeType(v.T):
			// Date/time keys are the same key when they are eq: the same
			// type family, the same timezone presence, and the same instant
			// (xs:time 17:00:00Z is xs:time 12:00:00-05:00 — same-key-027/028;
			// xs:dateTimeStamp is an xs:dateTime).
			t := v.T
			if t == XSdateTimeStamp {
				t = XSdateTime
			}
			if v.hasTZ {
				return mkey{'x', t.String() + ":Z:" + formatDateTime(t, v.tmv().UTC(), false)}
			}
			return mkey{'x', t.String() + "::" + formatDateTime(t, v.tmv(), false)}
		default:
			// binary: distinguish by type then lexical form.
			return mkey{'x', v.T.String() + ":" + v.Lexical()}
		}
	case float64:
		// A lookup key or number() result arrives as a bare Go float.
		return num(v)
	case bool:
		if v {
			return mkey{'b', "1"}
		}
		return mkey{'b', "0"}
	case string:
		return mkey{'s', v}
	default:
		return mkey{'s', itemString(it)}
	}
}

// KeyString renders a single item's canonical "eq"-equivalence-class string,
// used to index/look up xsl:key and key() values (XSLT 3.0 17.1.2). A node's
// own use-value is never atomized through here (its caller uses
// StringValue() directly, matching this function's plain-lexical default) —
// an untyped, string-like value must stay compatible with that raw form, so
// only two categories get a distinguishing tag instead of the plain lexical
// form:
//   - numeric values compare in the exact value space, not by spelling
//     (xs:double("10.0") and xs:double("10.00") are the same key), NaN is
//     never eq to anything — not even another NaN, so unlike mapKey's
//     op:same-key (which intentionally treats every NaN as one map key) each
//     NaN here gets a unique, never-matching key (key-070) — AND a number
//     must never collide with an unrelated same-spelled explicit string (a
//     use of string-length(.) indexing the number 4 must not be found by
//     key('k','4'), only by key('k',4) — key-081), so the numeric value gets
//     its own "n:" category tag.
//   - date/time values: eq compares by instant, not by the original lexical
//     timezone spelling (two equivalent offsets, e.g. -05:00 and its UTC
//     form, are eq) — key-069.
func KeyString(it Item) string {
	// Route every representation (a bare Go scalar, an *Atomic, OR a
	// synthetic text node stamped with ItemAtomTypeTag — a for-each/filter/
	// analyze-string context item over an atomic sequence, e.g. the integers
	// of "97 to 105" in key-073/074/075) through the SAME typed conversion
	// toAtomic already gives fn:data(): only that recovers the item's real
	// type (XSinteger, say) instead of falling through to its bare lexical
	// text with no numeric/date-time category at all.
	a, err := toAtomic(it)
	if err != nil {
		return itemString(it)
	}
	switch {
	case a.IsNumeric():
		return numericKeyString(a)
	case isDateTimeType(a.T):
		t := a.T
		if t == XSdateTimeStamp {
			t = XSdateTime
		}
		if a.hasTZ {
			return "x:" + t.String() + ":Z:" + formatDateTime(t, a.tmv().UTC(), false)
		}
		return "x:" + t.String() + "::" + formatDateTime(t, a.tmv(), false)
	case a.T == XSqname || a.T == XSnotation:
		// A QName/NOTATION value IS its expanded name (XSD 1.0 Part 2
		// §3.2.18/§3.2.19), and "eq" compares exactly that — the prefix is
		// lexical spelling only. Three attributes written one:mp3, first:mp3
		// and (default-namespaced) mp3 are therefore ONE key value, which is
		// the whole point of notation-0305. The "q:" tag keeps such a value
		// from colliding with an unrelated string that happens to be spelled
		// the same way, exactly as "n:" does for numerics above.
		return "q:{" + a.qnv().Space + "}" + a.qnv().Local
	default:
		return a.Lexical()
	}
}

// numericKeyString renders a numeric key value's "n:"-tagged canonical form.
// Deliberately NOT mapKey's exact-value-space rational form: xsl:key/key()
// compare like "eq", which for two DIFFERENT numeric subtypes promotes the
// narrower one up to the wider one's precision (decimal cast to double, in
// particular, so number(q)=3.7 (a double) and the literal 3.7 (a decimal)
// must be one key even though their EXACT binary/decimal values differ —
// key-004). a.Lexical() already IS that shared canonical form: XPath's
// number-to-string rule renders every numeric subtype's value through the
// same "shortest round-trip" text (integer 1, decimal 1.0 and double 1.0 all
// render "1"; a double and a decimal holding the same 3.7 both render
// "3.7"), so reusing it (unlike mapKey, which intentionally keeps xs:double
// and xs:decimal apart) gives exactly the cross-subtype unification "eq"
// needs. The "n:" tag only keeps a number from colliding with an unrelated,
// identically-spelled explicit string (key-081). NaN is the one exception:
// "eq" never considers NaN equal to anything, not even another NaN, so
// (unlike mapKey, which treats every NaN as one map key) each NaN here gets
// a unique, never-matching key instead of the shared literal text "NaN".
func numericKeyString(a *Atomic) string {
	if (a.T == XSdouble || a.T == XSfloat) && math.IsNaN(a.f) {
		return fmt.Sprintf("nan:%p", new(byte))
	}
	return "n:" + a.Lexical()
}

// CompositeKeyString joins a fixed-order tuple of items into the single
// canonical key string for an xsl:key/@composite="yes" declaration (key-096/
// 097): unlike the default (composite="no") multi-value key, where a
// use-sequence's items each index the node SEPARATELY, a composite key's
// items together form ONE compound key — so items are joined (each rendered
// via KeyString, length-prefixed so no arity/content combination can collide
// with another) rather than indexed individually.
func CompositeKeyString(items []Item) string {
	b := strconv.Itoa(len(items))
	for _, it := range items {
		s := KeyString(it)
		b += ":" + strconv.Itoa(len(s)) + ":" + s
	}
	return b
}

// Array is an XPath 3.1 array: a sequence of members, each an Object (sequence).
//
// Arrays made by Append share a backing array: appending to the newest one
// writes the slot after its members in place (an array never reads past its
// own length), so building an array one array:append at a time is linear,
// not quadratic. claim records how far the shared backing array has been
// handed out (see mapLog); nil means the members are the caller's slice of
// NewArray, which Append never writes into.
type Array struct {
	members []Object
	claim   *mapLog
}

// NewArray builds an array from members.
func NewArray(members []Object) *Array { return &Array{members: members} }

// Size returns the number of members.
func (a *Array) Size() int { return len(a.members) }

// Get returns member i (1-based); empty sequence if out of range.
func (a *Array) Get(i int) Object {
	if i < 1 || i > len(a.members) {
		return Sequence{}
	}
	return a.members[i-1]
}

// Members returns the underlying members slice. Its capacity is its
// length, so appending to it never writes into a shared backing array.
func (a *Array) Members() []Object { return a.members[:len(a.members):len(a.members)] }

// Append returns a new array: a's members followed by m.
func (a *Array) Append(m Object) *Array {
	n := len(a.members)
	if c := a.claim; c != nil && cap(a.members) > n {
		c.mu.Lock()
		if c.n == n {
			c.n++
			ms := append(a.members, m) // within capacity: the slot no other array sees
			c.mu.Unlock()
			return &Array{members: ms, claim: c}
		}
		c.mu.Unlock()
	}
	ms := make([]Object, n+1, n+1+n/2+4)
	copy(ms, a.members)
	ms[n] = m
	return &Array{members: ms, claim: &mapLog{n: n + 1}}
}

// membersAreAtomic reports whether every member is exactly one atomic item
// of type at (or a type derived from it): array(xs:string) and the like,
// the member test of such an array type. An array built by Append shares
// its backing array and claim with the arrays it grew from, and a slot once
// written never changes, so the claim remembers how many leading members
// already passed this test for which type: the next test of a longer array
// of the same lineage checks only the new members. An accumulating
// array(T) parameter is checked on every xsl:iterate step, which made a
// run of n steps test n*n/2 members.
func (a *Array) membersAreAtomic(at AtomType, ctx *Context) bool {
	n := len(a.members)
	from := 0
	c := a.claim
	if c != nil {
		c.mu.Lock()
		if c.typedAtom == at {
			// A shorter array of the lineage is a prefix of the verified
			// members too.
			from = min(c.typedN, n)
		}
		c.mu.Unlock()
	}
	for i := from; i < n; i++ {
		if !memberIsAtomic(a.members[i], at, ctx) {
			return false
		}
	}
	if c != nil && n > 0 {
		c.mu.Lock()
		if c.typedAtom != at || c.typedN < n {
			c.typedAtom, c.typedN = at, n
		}
		c.mu.Unlock()
	}
	return true
}

// memberIsAtomic reports whether o is exactly one atomic item of type at.
func memberIsAtomic(o Object, at AtomType, ctx *Context) bool {
	var item Item
	switch v := o.(type) {
	case nil:
		return false
	case Sequence:
		if len(v) != 1 {
			return false
		}
		item = v[0]
	case NodeSet:
		if len(v) != 1 {
			return false
		}
		item = Items(v)[0]
	default:
		item = v
	}
	t, ok := itemAtomType(item)
	return ok && (at == XSanyAtomicType || atomDerivesFrom(t, at))
}

// Flatten replaces arrays in an item sequence by their members, recursively
// (XDM flattening, used by atomization and XSLT content construction).
func Flatten(items []Item) []Item {
	flat := true
	for _, it := range items {
		if _, ok := it.(*Array); ok {
			flat = false
			break
		}
	}
	if flat {
		return items
	}
	out := make([]Item, 0, len(items))
	for _, it := range items {
		if a, ok := it.(*Array); ok {
			for _, m := range a.members {
				out = append(out, Flatten(Items(m))...)
			}
			continue
		}
		out = append(out, it)
	}
	return out
}

// Function is a function item (inline function or a named-function reference).
type Function struct {
	Arity int
	Name  string // local name for named functions; "" for anonymous (inline/partial)
	NS    string // namespace URI of the function name ("" if anonymous)
	// Call invokes the function with the given argument values.
	Call func(args []Object) (Object, error)
	// Typed marks a function whose declared signature is known (an inline
	// function expression): Params[i] / Ret are the declared SequenceTypes, a
	// nil entry meaning item()*. Typed function tests then use the XPath 3.1
	// subtype-itemtype judgement instead of arity-only matching.
	Typed  bool
	Params []*SeqType
	Ret    *SeqType
}
