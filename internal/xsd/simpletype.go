package xsd

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// validate checks a raw lexical value against the simple type, applying the
// type's effective whiteSpace processing first.
func (st *SimpleType) validate(raw string) error { return st.validateIn(raw, nil) }

// validateIn is validate carrying the INSTANCE NODE whose value is being
// checked. One facet needs it: an enumeration over xs:QName or xs:NOTATION
// constrains the VALUE space (XSD 1.0 Part 2 §4.3.5), whose members are
// EXPANDED names — so "one:mp3" and "smokey:mp3" are the same value when both
// prefixes are bound to the same namespace, and the instance's own bindings
// are the only place that can be read from. Every other facet ignores it, and
// a nil node simply falls back to the lexical comparison this always did.
func (st *SimpleType) validateIn(raw string, at *xmltree.Node) error {
	return st.checkIn(applyWhiteSpace(st.effectiveWhiteSpace(), raw), at)
}

// fixedEqual reports whether content equals a fixed value constraint in the
// type's VALUE space: both lexicals are normalized with the governing type's
// whiteSpace — for a union, the whiteSpace of the FIRST member accepting each
// side — then compared as values (boolean "1"="true", decimal "2"="2.0").
// A preserve-whiteSpace string therefore distinguishes "   1  2" from " 1 2"
// (attO008/addB105/test93160), while a collapse member equates them
// (attO006/stE065/stE066 — the stE063-vs-stE065 pair pins the union rule).
func (st *SimpleType) fixedEqual(content, fixed string) bool {
	switch st.variety {
	case vUnion:
		// Each side takes the first member that accepts it; equal only when
		// both land in the same member and are equal THERE.
		ma, mb := st.firstMemberRaw(content), st.firstMemberRaw(fixed)
		return ma != nil && ma == mb && ma.fixedEqual(content, fixed)
	case vList:
		fa, fb := xmlFields(content), xmlFields(fixed)
		if len(fa) != len(fb) {
			return false
		}
		for i := range fa {
			if st.item == nil {
				if fa[i] != fb[i] {
					return false
				}
				continue
			}
			if !st.item.fixedEqual(fa[i], fb[i]) {
				return false
			}
		}
		return true
	default:
		ws := st.effectiveWhiteSpace()
		a, b := applyWhiteSpace(ws, content), applyWhiteSpace(ws, fixed)
		if a == b {
			return true
		}
		return st.atomicEqual(a, b)
	}
}

// firstMemberRaw returns the first union member whose whiteSpace-normalized
// reading of the raw lexical validates.
func (st *SimpleType) firstMemberRaw(v string) *SimpleType {
	for _, m := range st.members {
		if m.validate(v) == nil {
			return m
		}
	}
	return nil
}

func (st *SimpleType) atomicEqual(a, b string) bool {
	prim := st.resolvePrim()
	ca, e1 := xpath.CastTo(xpath.NewString(a), prim)
	cb, e2 := xpath.CastTo(xpath.NewString(b), prim)
	if e1 != nil || e2 != nil {
		return false
	}
	c, ok := xpath.CompareAtomic(ca, cb)
	return ok && c == 0
}

// listEnumMatch reports whether the list value items equals one of the
// enumerated lists item by item in the value space of the ATOMIC item type
// (any other item type keeps the lexical comparison of the caller).
func (f *facetSet) listEnumMatch(items []string, item *SimpleType) bool {
	if item == nil || item.variety != vAtomic {
		return false
	}
	for _, e := range f.enumeration {
		ev := xmlFields(e)
		if len(ev) != len(items) {
			continue
		}
		eq := true
		for i := range ev {
			if ev[i] != items[i] && !item.atomicEqual(ev[i], items[i]) {
				eq = false
				break
			}
		}
		if eq {
			return true
		}
	}
	return false
}

// check validates an already-whiteSpace-processed value.
// xmlFields splits a list value on XML whitespace ONLY (space/tab/CR/LF).
// strings.Fields would also split on Unicode spaces like U+000C, NEL, and
// U+2028, silently turning one invalid NMTOKEN into two valid ones
// (xv009.n01-n03).
func xmlFields(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
}

func (st *SimpleType) check(v string) error { return st.checkIn(v, nil) }

func (st *SimpleType) checkIn(v string, at *xmltree.Node) error {
	if st == absentType {
		// Using a declaration whose type reference never resolved IS the error
		// the Missing family defers to validation time (cvc-type / src-resolve).
		return invalidf("cvc-type", "the declaration's type %s is absent from the schema", st.name.Local[1:])
	}
	if st == xsErrorType {
		return invalidf("cvc-type", "xs:error has an empty value space")
	}
	switch st.variety {
	case vList:
		items := xmlFields(v)
		// EVERY restriction step's facets apply (a derived list must still
		// honor the base's — e.g. the built-in list types' minLength 1);
		// item checks run once against the resolved item type.
		hasAsserts := false
		for s := st; s != nil; s = s.base {
			if err := s.facets.checkListLength(len(items)); err != nil {
				return err
			}
			if err := s.facets.checkPatternEnumIn(v, xpath.XSstring, at); err != nil {
				// The patterns passed (they are checked first); the
				// enumeration compares VALUES — "Two lists A and B are equal
				// if and only if they have the same length and their items
				// are pairwise equal" (Datatypes §2.2.3) — so "01 2" is the
				// enumerated "1 2" of an xs:int list.
				if e, ok := err.(*Error); !ok || e.Code != "cvc-enumeration-valid" ||
					!s.facets.listEnumMatch(items, st.item) {
					return err
				}
			}
			if len(s.assertions) > 0 {
				hasAsserts = true
			}
		}
		if st.item != nil {
			for _, it := range items {
				if err := st.item.checkIn(it, at); err != nil {
					return err
				}
			}
		}
		if hasAsserts {
			// $value is the SEQUENCE of typed items (assert-simple005:
			// count($value) over a list of xs:integer).
			itemPrim := xpath.XSuntypedAtomic
			if st.item != nil {
				itemPrim = st.item.resolvePrim()
			}
			seq := make([]xpath.Item, 0, len(items))
			for _, it := range items {
				seq = append(seq, typedSimpleValue(it, itemPrim))
			}
			val := xpath.FromItems(seq)
			for s := st; s != nil; s = s.base {
				if err := evalSimpleAssertions(s.assertions, val); err != nil {
					return err
				}
			}
		}
		return nil
	case vUnion:
		var acc *SimpleType
		for _, m := range st.members {
			// Each member applies its OWN whiteSpace processing (a union has none of
			// its own), so validate — not check — the value against the member.
			if m.validateIn(v, at) == nil {
				acc = m
				break
			}
		}
		if acc == nil {
			return invalidf("cvc-datatype-valid", "value %q is not valid for any union member", v)
		}
		// Facets on the union restriction (pattern/enumeration) constrain the
		// whole lexical in addition to member validity. They also hold if they
		// match the value as normalized by the ACCEPTING member's whiteSpace
		// (simple085: the pattern applies to the member-normalized value).
		err := st.facets.checkPatternEnumIn(v, xpath.XSstring, at)
		if err != nil {
			if mv := applyWhiteSpace(acc.effectiveWhiteSpace(), v); mv != v &&
				st.facets.checkPatternEnumIn(mv, xpath.XSstring, at) == nil {
				err = nil
			}
		}
		if err != nil {
			return err
		}
		if len(st.assertions) > 0 {
			// $value carries the ACCEPTING member's type (assert-simple006:
			// string($value) of a date/dateTime member).
			mv := applyWhiteSpace(acc.effectiveWhiteSpace(), v)
			if err := evalSimpleAssertions(st.assertions, typedSimpleValue(mv, acc.resolvePrim())); err != nil {
				return err
			}
		}
		return nil
	default: // atomic
		prim := st.resolvePrim()
		if st.base != nil {
			if err := st.base.checkIn(v, at); err != nil {
				return err
			}
		} else {
			if err := checkPrimitiveLexical(prim, v); err != nil {
				return err
			}
			// XSD Part 2 §3.2.18: the VALUE of an xs:QName (and of an
			// xs:NOTATION) is an expanded name, so a prefixed lexical is valid
			// only where that prefix is actually bound. checkPrimitiveLexical
			// sees nothing but the string and so can only check the shape; the
			// binding lives on the node carrying the value, which is exactly
			// what the `at` carrier exists to supply.
			//
			// Skipped when no carrier is available (a value checked outside any
			// instance, e.g. a schema's own default), which keeps the old
			// shape-only reading wherever the question cannot be asked.
			if at != nil && (prim == xpath.XSqname || prim == xpath.XSnotation) {
				if err := checkQNamePrefixBound(v, prim, at); err != nil {
					return err
				}
			}
		}
		if err := st.facets.checkIn(v, prim, at); err != nil {
			return err
		}
		if len(st.assertions) > 0 {
			if err := evalSimpleAssertions(st.assertions, typedSimpleValue(v, prim)); err != nil {
				return err
			}
		}
		return nil
	}
}

// resolvePrim walks the base chain to the built-in primitive (cycle-guarded).
func (st *SimpleType) resolvePrim() xpath.AtomType {
	for s, n := st, 0; s != nil && n < 64; n++ {
		if s.baseBuiltin {
			return s.prim
		}
		if s.base == nil {
			if s.prim != 0 {
				return s.prim
			}
			return xpath.XSanyAtomicType
		}
		s = s.base
	}
	return xpath.XSanyAtomicType
}

// effectiveWhiteSpace is the strongest of the primitive default and any explicit
// whiteSpace facet down the base chain (preserve < replace < collapse).
func (st *SimpleType) effectiveWhiteSpace() string {
	best := primitiveWhiteSpace(st.resolvePrim(), st.variety)
	for s, n := st, 0; s != nil && n < 64; n++ {
		if s.facets.whiteSpace != "" {
			best = strongerWS(best, s.facets.whiteSpace)
		}
		if s.baseBuiltin || s.base == nil {
			break
		}
		s = s.base
	}
	return best
}

func primitiveWhiteSpace(prim xpath.AtomType, v variety) string {
	if v == vList {
		return "collapse"
	}
	if v == vUnion {
		return "preserve"
	}
	switch prim {
	case xpath.XSstring, xpath.XSuntypedAtomic, xpath.XSanyAtomicType:
		return "preserve"
	case xpath.XSnormalizedString:
		return "replace"
	default:
		return "collapse"
	}
}

func strongerWS(a, b string) string {
	if wsRank(b) > wsRank(a) {
		return b
	}
	return a
}
func wsRank(w string) int {
	switch w {
	case "replace":
		return 1
	case "collapse":
		return 2
	default:
		return 0
	}
}

func applyWhiteSpace(mode, s string) string {
	switch mode {
	case "replace":
		return strings.Map(func(r rune) rune {
			if r == '\t' || r == '\n' || r == '\r' {
				return ' '
			}
			return r
		}, s)
	case "collapse":
		return collapseWhiteSpace(s)
	default:
		return s
	}
}

func isXMLSpace(c byte) bool { return byteClass[c] == clSpace || byteClass[c] == clBreak }

// byteClass sorts bytes for the hand-written scanners below.
const (
	clOther  = iota
	clSpace  // ' '
	clBreak  // '\t' '\r' '\n'
	clB64    // [A-Za-z0-9+/]
	clEquals // '='
)

var byteClass = func() (t [256]byte) {
	t[' '] = clSpace
	t['\t'], t['\r'], t['\n'] = clBreak, clBreak, clBreak
	for c := 'A'; c <= 'Z'; c++ {
		t[c], t[c+'a'-'A'] = clB64, clB64
	}
	for c := '0'; c <= '9'; c++ {
		t[c] = clB64
	}
	t['+'], t['/'] = clB64, clB64
	t['='] = clEquals
	return
}()

// collapseWhiteSpace is the whiteSpace="collapse" normalization: every run of
// XML whitespace (#x20 #x9 #xD #xA, nothing else) becomes one space, and a
// leading or trailing run is dropped. One linear pass; a value that is
// already collapsed (the usual case) is returned as is, without copying —
// this runs over every simple value, including multi-megabyte base64 ones.
func collapseWhiteSpace(s string) string {
	i := 0
	for ; i < len(s); i++ {
		switch byteClass[s[i]] {
		case clSpace:
			if i == 0 || i == len(s)-1 || s[i+1] == ' ' {
				goto slow
			}
		case clBreak:
			goto slow
		}
	}
	return s
slow:
	if i > 0 && s[i-1] == ' ' {
		i-- // the run starts at that space: the kept prefix ends in a non-space
	}
	b := make([]byte, 0, len(s))
	b = append(b, s[:i]...)
	pending := false
	for ; i < len(s); i++ {
		c := s[i]
		if isXMLSpace(c) {
			pending = len(b) > 0
			continue
		}
		if pending {
			b = append(b, ' ')
			pending = false
		}
		b = append(b, c)
	}
	return string(b)
}

// --- primitive lexical validation -------------------------------------------

// Approximate XML productions (Unicode letters) for the string-family types
// whose lexical space xpath.CastTo does not itself validate.
// XML Name productions (approximate but broad enough not to reject valid names):
// NameStartChar admits Nl (letter-numbers, e.g. Roman numerals) as well as \p{L};
// NameChar adds \p{N}, combining marks \p{M}, and the extender codepoints.
var (
	reLanguage = regexp.MustCompile(`^[A-Za-z]{1,8}(-[A-Za-z0-9]{1,8})*$`)
	// The XML Name productions, built from the same range tables that back the
	// XSD regex escapes \i and \c (internal/xpath/regex_class.go), so a value
	// and a pattern can never disagree about what a name character is.
	reNCName  = regexp.MustCompile(`^[` + xpath.NCNameStartClass + `][` + xpath.NCNameCharClass + `]*$`)
	reName    = regexp.MustCompile(`^[` + xpath.NameStartClass + `][` + xpath.NameCharClass + `]*$`)
	reNmtoken = regexp.MustCompile(`^[` + xpath.NameCharClass + `]+$`)
	reInteger = regexp.MustCompile(`^[+-]?[0-9]+$`)
	reDecimal = regexp.MustCompile(`^[+-]?([0-9]+\.?[0-9]*|\.[0-9]+)$`)
	reDouble  = regexp.MustCompile(`^[+-]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][+-]?[0-9]+)?$`)
)

// validQNameLexical checks the structural shape of a QName: (NCName ':')? NCName,
// with a DELIBERATELY permissive NCName reading — the first rune must be a letter
// or '_' and later runes must be name-ish (letters, digits, marks, ._-, extender
// dots, or any non-ASCII rune) — so exotic-but-valid Unicode names are never
// rejected, while ASCII shape violations ("", "1fo", "-foo", ":foo", "@x", "a/b",
// a second ':') are. The reserved prefix "xmlns" is never a legal QName prefix.
func validQNameLexical(s string) bool {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		prefix, local := s[:i], s[i+1:]
		return prefix != "xmlns" && permissiveNCName(prefix) && permissiveNCName(local)
	}
	return permissiveNCName(s)
}

func permissiveNCName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == ':' {
			return false
		}
		if i == 0 {
			// Nl (letter numbers) covers XML name starters IsLetter misses:
			// U+3007 ideographic zero, Roman numerals (sun AD_name00102m1).
			// The 5th edition's NameStartChar adds what no letter category
			// holds (an emoji, a Bengali digit).
			if !(unicode.IsLetter(r) || unicode.Is(unicode.Nl, r) || r == '_' || xmltree.IsNCNameStartChar(r)) {
				return false
			}
			continue
		}
		if r < 0x80 {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
				r == '.', r == '-', r == '_':
			default:
				return false
			}
		}
		// any non-ASCII rune is permitted (avoids Unicode-class false rejections)
	}
	return true
}

// checkQNamePrefixBound reports whether the prefix of a QName/NOTATION lexical
// is bound to a namespace where the value sits. An UNPREFIXED name is always
// fine: it takes the default namespace, or none.
func checkQNamePrefixBound(v string, prim xpath.AtomType, at *xmltree.Node) error {
	i := strings.IndexByte(v, ':')
	if i <= 0 {
		return nil
	}
	if _, ok := at.LookupPrefix(v[:i]); !ok {
		return invalidf("cvc-datatype-valid",
			"the prefix %q of %s value %q is not bound to a namespace", v[:i], atomName(prim), v)
	}
	return nil
}

// checkPrimitiveLexical validates v against the built-in primitive's lexical
// space. String-family types are handled directly (with approximate XML-name
// productions); everything else defers to xpath.CastTo, which rejects invalid
// numeric/date/duration/binary/URI/boolean lexicals.
func checkPrimitiveLexical(prim xpath.AtomType, v string) error {
	switch prim {
	case xpath.XSstring, xpath.XSnormalizedString, xpath.XStoken,
		xpath.XSanyAtomicType, xpath.XSuntypedAtomic:
		return nil // string family: every value is lexically valid
	case xpath.XSqname, xpath.XSnotation:
		// QName/NOTATION: structural lexical check only ((NCName ':')? NCName with
		// a permissive NCName reading — no namespace resolution, so an unbound
		// prefix is NOT an error here; only shape violations are.
		if !validQNameLexical(v) {
			return invalidf("cvc-datatype-valid", "%q is not a valid %s", v, atomName(prim))
		}
		return nil
	case xpath.XSlanguage:
		return matchLexical(reLanguage, prim, v)
	case xpath.XSname:
		return matchLexical(reName, prim, v)
	case xpath.XSncname, xpath.XSid, xpath.XSidref, xpath.XSentity:
		return checkLexical(isNCNameLex(v), prim, v)
	case xpath.XSnmtoken:
		return matchLexical(reNmtoken, prim, v)
	case xpath.XShexBinary:
		return checkLexical(validHexBinary(v), prim, v)
	case xpath.XSbase64Binary:
		return checkLexical(validBase64Binary(v), prim, v)
	case xpath.XSdouble, xpath.XSfloat:
		// Overflow of the mantissa/exponent maps to ±INF and is a VALID double/
		// float (IEEE), so validate the lexical shape directly rather than via
		// CastTo, whose parseNumber turns an out-of-range value into NaN→error.
		switch v {
		case "INF", "+INF", "-INF", "NaN":
			return nil
		}
		return matchLexical(reDouble, prim, v)
	default:
		// The integer/decimal lexical spaces need an explicit guard: xpath.CastTo
		// truncates "1.5"→1 for integers and accepts big.Rat fraction syntax
		// ("1/2") for decimals, neither of which is a valid XSD lexical.
		if isIntegerLike(prim) {
			if !isIntegerLex(v) {
				return invalidf("cvc-datatype-valid", "%q is not a valid %s", v, atomName(prim))
			}
			// With the lexical shape settled, all the cast below can still
			// reject is the derived type's value range (xpath.checkIntegerBounds).
			// A value that fits int64 is range-checked here against the same
			// bounds, without building the string atomic, the big.Int and the
			// result atomic the cast builds for every integer an instance
			// holds; one that overflows int64 takes the cast as before.
			if n, perr := strconv.ParseInt(v, 10, 64); perr == nil {
				if !integerInRange(n, prim) {
					return invalidf("cvc-datatype-valid", "%q is not a valid %s", v, atomName(prim))
				}
				return nil
			}
		}
		if prim == xpath.XSdecimal {
			if !isDecimalLex(v) {
				return invalidf("cvc-datatype-valid", "%q is not a valid xs:decimal", v)
			}
			// Every string the regex admits is one big.Rat.SetString accepts
			// (checked exhaustively over the regex's shapes), and that is the
			// whole of what the cast below does with a string source, so for a
			// decimal the cast cannot fail: the lexical check is complete here.
			return nil
		}
		if handled, err := xpath.CheckDateTimeLexical(prim, v); handled {
			// The cast's own verdict, without the two atomics it would build
			// (every date in an instance passed through here).
			if err != nil {
				return invalidf("cvc-datatype-valid", "%q is not a valid %s", v, atomName(prim))
			}
			return nil
		}
		if _, err := xpath.CastTo(xpath.NewString(v), prim); err != nil {
			return invalidf("cvc-datatype-valid", "%q is not a valid %s", v, atomName(prim))
		}
		return nil
	}
}

func isIntegerLike(t xpath.AtomType) bool {
	switch t {
	case xpath.XSinteger, xpath.XSnonNegativeInteger, xpath.XSpositiveInteger,
		xpath.XSnonPositiveInteger, xpath.XSnegativeInteger, xpath.XSlong, xpath.XSint,
		xpath.XSshort, xpath.XSbyte, xpath.XSunsignedLong, xpath.XSunsignedInt,
		xpath.XSunsignedShort, xpath.XSunsignedByte:
		return true
	}
	return false
}

// integerInRange reports whether n lies in the value space of the integer-
// family type t — the same bounds xpath's checkIntegerBounds enforces, for a
// value already known to fit int64 (so xs:long and the lower half of
// xs:unsignedLong need only the sign test; a larger value never reaches this).
func integerInRange(n int64, t xpath.AtomType) bool {
	switch t {
	case xpath.XSbyte:
		return n >= -128 && n <= 127
	case xpath.XSshort:
		return n >= -32768 && n <= 32767
	case xpath.XSint:
		return n >= -2147483648 && n <= 2147483647
	case xpath.XSunsignedByte:
		return n >= 0 && n <= 255
	case xpath.XSunsignedShort:
		return n >= 0 && n <= 65535
	case xpath.XSunsignedInt:
		return n >= 0 && n <= 4294967295
	case xpath.XSunsignedLong, xpath.XSnonNegativeInteger:
		return n >= 0
	case xpath.XSpositiveInteger:
		return n >= 1
	case xpath.XSnonPositiveInteger:
		return n <= 0
	case xpath.XSnegativeInteger:
		return n <= -1
	}
	return true // xs:integer, xs:long: unbounded within int64
}

// validAnyURI10 checks the XSD 1.0 xs:anyURI lexical space: an RFC 2396
// URI-reference (with RFC 2732's brackets tolerated), where non-ASCII runes and
// <, >, " count as pre-escapable per XLink §5.4 (anyURI_a005/a012-a016 use
// them and are valid). XSD 1.1 dropped the constraint — callers gate on 1.0.
func validAnyURI10(s string) bool {
	isAlpha := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
	isHex := func(c byte) bool {
		return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 0x80: // pre-escapable
		case isAlpha(c), c >= '0' && c <= '9':
		case strings.IndexByte("-_.!~*'();/?:@&=+$,#[]<>\"", c) >= 0:
		case c == '%':
			if i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2]) {
				return false // a bare '%' is not a uric (anyURI_b004)
			}
			i += 2
		default:
			return false // space, backslash, ^, `, |, {, } … (anyURI_b006)
		}
	}
	// A ':' before any '/', '?' or '#' splits off a scheme, which must be
	// ALPHA (alpha|digit|+|-|.)* and be followed by a non-empty part before any
	// fragment (":a" and "b:" are invalid — anyURI_a003; a leading-digit
	// pseudo-scheme is an invalid rel_segment — anyURI_a001).
	head := s
	if stop := strings.IndexAny(s, "/?#"); stop >= 0 {
		head = s[:stop]
	}
	if ci := strings.IndexByte(head, ':'); ci >= 0 {
		scheme := head[:ci]
		if scheme == "" || !isAlpha(scheme[0]) {
			return false
		}
		for j := 1; j < len(scheme); j++ {
			c := scheme[j]
			if !(isAlpha(c) || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.') {
				return false
			}
		}
		rest := s[ci+1:]
		if f := strings.IndexByte(rest, '#'); f >= 0 {
			rest = rest[:f]
		}
		if rest == "" {
			return false
		}
	}
	return true
}

func matchLexical(re *regexp.Regexp, prim xpath.AtomType, v string) error {
	return checkLexical(re.MatchString(v), prim, v)
}

func checkLexical(ok bool, prim xpath.AtomType, v string) error {
	if !ok {
		return invalidf("cvc-datatype-valid", "%q is not a valid %s", v, atomName(prim))
	}
	return nil
}

// hexBinary and base64Binary are scanned by hand rather than by regexp: an
// e-invoice embeds a whole PDF as one base64Binary value, tens of megabytes
// long, and the regexp machine over it dominated validation. Both scanners
// accept exactly what these regexps did (lexical_large_test.go keeps them as
// the oracle):
//
//	hexBinary     ^([0-9A-Fa-f]{2})*$
//	base64Binary  ^((([A-Za-z0-9+/] ?){4})*(([A-Za-z0-9+/] ?){3}[A-Za-z0-9+/]|
//	              ([A-Za-z0-9+/] ?){2}[AEIMQUYcgkosw048] ?=|[A-Za-z0-9+/] ?[AQgw] ?= ?=))?$
//
// i.e. the XSD Part 2 grammar: an even number of hex digits with no
// whitespace; base64 characters in groups of four with optional single spaces
// between characters, where only the final group may be padded — "=" needs a
// B16 character before it, "==" a B04 character before that.

func validHexBinary(v string) bool {
	if len(v)%2 != 0 {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

func validBase64Binary(v string) bool {
	n := 0        // base64 characters and '=' seen, spaces excluded
	pad := 0      // '=' seen so far; nothing but '=' may follow the first
	var last byte // the last base64 character, the one before any padding
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch byteClass[c] {
		case clB64:
			if pad > 0 {
				return false
			}
			last = c
		case clSpace:
			// A single space between characters; none leading, trailing or
			// doubled.
			if i == 0 || i == len(v)-1 || v[i-1] == ' ' {
				return false
			}
			continue
		case clEquals:
			pad++
		default:
			return false
		}
		n++
	}
	if n%4 != 0 {
		return false
	}
	switch pad {
	case 0:
		return true
	case 1:
		return strings.IndexByte("AEIMQUYcgkosw048", last) >= 0
	case 2:
		return strings.IndexByte("AQgw", last) >= 0
	}
	return false
}

func atomName(t xpath.AtomType) string { return t.String() }

// validateXSDRegex checks a pattern-facet value against the XSD
// regular-expression grammar (XML Schema Part 2, Appendix F / G) — the parts
// Go's RE2 accepts but XSD forbids, so that a schema carrying such a pattern is
// correctly rejected. The grammar checker itself lives in internal/xpath
// (regex_grammar.go) so this pattern-facet path and the XPath
// fn:matches/replace/tokenize path share ONE implementation and cannot drift
// apart; only the grammar selector differs.
func validateXSDRegex(pat string, ver Version) error {
	g := xpath.GrammarXSD10
	if ver == Version11 {
		g = xpath.GrammarXSD11
	}
	if err := xpath.ValidateRegexGrammar(pat, g); err != nil {
		return invalidf("", "%v", err)
	}
	return nil
}

// compileXSDPattern translates an XSD pattern to a whole-value Go matcher. XSD
// patterns match the entire value, so the translated Go regex is anchored with
// \A…\z (XSD's own ^/$ are literal characters, handled by the translator).
func compileXSDPattern(pat string, ver Version) (*regexp.Regexp, error) {
	if ver == Version11 {
		pat = rewriteUnknownBlocks(pat)
	}
	re, err := xpath.CompileRegex(escapeXSDAnchors(pat), "")
	if err != nil {
		return nil, err
	}
	return regexp.Compile(`\A(?:` + re.String() + `)\z`)
}

// rewriteUnknownBlocks replaces top-level \\p{IsX}/\\P{IsX} atoms naming a
// block this engine does not know with a match-anything atom — XSD 1.1
// semantics for unrecognized block names (reK88, bug 13670).
func rewriteUnknownBlocks(pat string) string {
	var b strings.Builder
	rs := []rune(pat)
	inClass := false
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '\\' && i+1 < len(rs) {
			n := rs[i+1]
			if (n == 'p' || n == 'P') && !inClass && i+2 < len(rs) && rs[i+2] == '{' {
				j := i + 3
				for j < len(rs) && rs[j] != '}' {
					j++
				}
				if j < len(rs) {
					body := string(rs[i+3 : j])
					if strings.HasPrefix(body, "Is") && !xpath.XSDBlockKnown(body[2:]) {
						b.WriteString(`[\s\S]`) // XSD-syntax match-anything atom
						i = j
						continue
					}
				}
			}
			b.WriteRune(r)
			b.WriteRune(n)
			i++
			continue
		}
		if r == '[' {
			inClass = true
		} else if r == ']' {
			inClass = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// escapeXSDAnchors escapes ^ and $ outside character classes: XSD patterns
// have no anchors — both are ordinary characters (reZ001). fn:matches keeps
// anchor semantics, so this applies only on the XSD pattern-facet path.
func escapeXSDAnchors(pat string) string {
	var b strings.Builder
	rs := []rune(pat)
	inClass := false
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '\\' && i+1 < len(rs) {
			b.WriteRune(r)
			b.WriteRune(rs[i+1])
			i++
			continue
		}
		switch r {
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '^', '$':
			if !inClass {
				b.WriteRune('\\')
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}
