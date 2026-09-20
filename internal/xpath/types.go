package xpath

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
)

// Map is an XPath 3.1 map: an ordered set of key/value entries. Keys are
// atomic items compared by the op:same-key rule (a value-and-category
// canonical form). Values are arbitrary Objects (sequences).
type Map struct {
	keys   []Item            // insertion order
	values map[string]Object // keyed by atomic string form
	raw    map[string]Item   // original key item (for map:keys)
}

// NewMap returns an empty map.
func NewMap() *Map {
	return &Map{values: map[string]Object{}, raw: map[string]Item{}}
}

// mapKey produces the op:same-key canonical form of an atomic key: a category
// prefix plus a value form, so that equal-value keys of compatible types
// collapse (all string-family keys; all numeric keys with 1 == 1.0 and
// NaN == NaN) while keys of different categories stay distinct (a QName "abc"
// is not the string "abc"; the number 1.5 is not the string "1.5").
func mapKey(it Item) string {
	// Numeric keys compare in the EXACT value space: op:same-key converts a
	// double/float to xs:decimal without loss, so xs:double("1.1") is not the
	// same key as xs:decimal("1.1") (same-key-007, map-put-023, map-remove-016)
	// while 1, 1.0 and 1e0 are one key. The canonical form is the reduced
	// rational (big.Rat.SetFloat64 is exact).
	num := func(f float64) string {
		switch {
		case math.IsNaN(f):
			return "n:NaN" // every NaN key is the same key
		case math.IsInf(f, 1):
			return "n:INF"
		case math.IsInf(f, -1):
			return "n:-INF"
		}
		return "n:" + new(big.Rat).SetFloat64(f).RatString()
	}
	switch v := it.(type) {
	case *Atomic:
		switch {
		case isIntegerType(v.T) && v.i != nil:
			return "n:" + v.i.String()
		case v.T == XSdecimal && v.d != nil:
			return "n:" + v.d.RatString()
		case v.IsNumeric():
			return num(v.Float())
		case v.T == XSboolean:
			if v.b {
				return "b:1"
			}
			return "b:0"
		case v.T == XSqname || v.T == XSnotation:
			return "q:{" + v.qn.Space + "}" + v.qn.Local
		case v.T == XSstring || v.T == XSanyURI || v.T == XSuntypedAtomic || isStringType(v.T):
			return "s:" + v.Lexical()
		case isDurationType(v.T):
			// Durations are the same key by VALUE, across subtypes and lexical
			// forms (P1Y and P12M are one key; map-get-017).
			return "dur:" + strconv.Itoa(v.dur.Months) + "/" + strconv.FormatFloat(v.dur.Secs, 'g', -1, 64)
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
				return "x:" + t.String() + ":Z:" + formatDateTime(t, v.tm.UTC(), false)
			}
			return "x:" + t.String() + "::" + formatDateTime(t, v.tm, false)
		default:
			// binary: distinguish by type then lexical form.
			return "x:" + v.T.String() + ":" + v.Lexical()
		}
	case float64:
		// A lookup key or number() result arrives as a bare Go float.
		return num(v)
	case bool:
		if v {
			return "b:1"
		}
		return "b:0"
	case string:
		return "s:" + v
	default:
		return "s:" + itemString(it)
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
			return "x:" + t.String() + ":Z:" + formatDateTime(t, a.tm.UTC(), false)
		}
		return "x:" + t.String() + "::" + formatDateTime(t, a.tm, false)
	case a.T == XSqname || a.T == XSnotation:
		// A QName/NOTATION value IS its expanded name (XSD 1.0 Part 2
		// §3.2.18/§3.2.19), and "eq" compares exactly that — the prefix is
		// lexical spelling only. Three attributes written one:mp3, first:mp3
		// and (default-namespaced) mp3 are therefore ONE key value, which is
		// the whole point of notation-0305. The "q:" tag keeps such a value
		// from colliding with an unrelated string that happens to be spelled
		// the same way, exactly as "n:" does for numerics above.
		return "q:{" + a.qn.Space + "}" + a.qn.Local
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

// Put sets a key/value entry (returns the same map; maps are treated as
// mutable internally but the map: functions copy where the spec requires).
func (m *Map) Put(key Item, val Object) {
	k := mapKey(key)
	if _, exists := m.values[k]; !exists {
		m.keys = append(m.keys, key)
	} else {
		// Replacing an entry replaces its KEY item too: map:put(map{3:..},
		// xs:float('3.0'), ..) is map(xs:float, ..) (map-put-011, map-merge-011
		// use-last, same-key-004 keys become xs:float+).
		for i, old := range m.keys {
			if mapKey(old) == k {
				m.keys[i] = key
				break
			}
		}
	}
	m.values[k] = val
	m.raw[k] = key
}

// Get returns the value for key (empty sequence if absent).
func (m *Map) Get(key Item) Object {
	if v, ok := m.values[mapKey(key)]; ok {
		return v
	}
	return Sequence{}
}

// Contains reports whether key is present.
func (m *Map) Contains(key Item) bool {
	_, ok := m.values[mapKey(key)]
	return ok
}

// Keys returns the keys in insertion order.
func (m *Map) Keys() []Item { return m.keys }

// Size returns the number of entries.
func (m *Map) Size() int { return len(m.keys) }

// Copy returns a shallow copy of the map.
func (m *Map) Copy() *Map {
	c := NewMap()
	for _, k := range m.keys {
		c.Put(k, m.values[mapKey(k)])
	}
	return c
}

// Array is an XPath 3.1 array: a sequence of members, each an Object (sequence).
type Array struct {
	members []Object
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

// Members returns the underlying members slice.
func (a *Array) Members() []Object { return a.members }

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
