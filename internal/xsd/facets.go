package xsd

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// validateFacetValues performs schema-validity checks on a simple type's facets:
// (1) applicability — a facet must be legal for the type's primitive/variety;
// (2) mutual consistency — conflicting facets are rejected; (3) value validity —
// a bound/enumeration value must itself be a valid value of the base type. These
// make an otherwise-accepted but ill-formed schema invalid.
func validateFacetValues(st *SimpleType, ver Version) error {
	f := &st.facets
	hasLen := f.hasLength || f.hasMinLength || f.hasMaxLength
	hasBound := f.hasMinIncl || f.hasMaxIncl || f.hasMinExcl || f.hasMaxExcl
	hasDigits := f.hasTotalDigits || f.hasFractionDigits

	if f.explicitTZ != "" {
		// §4.3.16: value vocabulary and applicability (only the date/time
		// primitives, atomic variety — zone006/007/008, d4_3_16si01s).
		switch f.explicitTZ {
		case "optional", "required", "prohibited":
		default:
			return invalidf("", "invalid explicitTimezone value %q", f.explicitTZ)
		}
		if st.variety != vAtomic || !dateTimePrim(st.resolvePrim()) {
			return invalidf("", "explicitTimezone is not applicable here")
		}
	}
	switch st.variety {
	case vList:
		if hasBound || hasDigits {
			return invalidf("", "bound/digit facets are not applicable to a list type")
		}
	case vUnion:
		if hasLen || hasBound || hasDigits {
			return invalidf("", "length/bound/digit facets are not applicable to a union type")
		}
	default: // atomic
		prim := st.resolvePrim()
		if hasLen && !lengthFacetAllowed(prim) {
			return invalidf("", "length facets are not applicable to %s", prim)
		}
		if hasBound && !boundsApplicable(prim) {
			return invalidf("", "bound facets are not applicable to %s", prim)
		}
		if hasDigits && !digitsApplicable(prim) {
			return invalidf("", "digit facets are not applicable to %s", prim)
		}
		// xs:integer (and every type derived from it) fixes fractionDigits at
		// {value 0, fixed true}: a restriction cannot loosen a fixed facet, so any
		// fractionDigits > 0 in the integer family is invalid.
		if f.hasFractionDigits && f.fractionDigits > 0 && isIntegerLike(prim) {
			return invalidf("", "fractionDigits must be 0 for integer-derived type %s", prim)
		}
		for _, b := range []struct {
			has bool
			val string
		}{
			{f.hasMinIncl, f.minIncl}, {f.hasMaxIncl, f.maxIncl},
			{f.hasMinExcl, f.minExcl}, {f.hasMaxExcl, f.maxExcl},
		} {
			if b.has {
				if err := checkPrimitiveLexical(prim, applyWhiteSpace("collapse", b.val)); err != nil {
					return invalidf("", "invalid facet value %q for %s", b.val, prim)
				}
			}
		}
		if f.hasEnum {
			ws := st.effectiveWhiteSpace()
			for _, e := range f.enumeration {
				if err := checkPrimitiveLexical(prim, applyWhiteSpace(ws, e)); err != nil {
					return invalidf("", "invalid enumeration value %q for %s", e, prim)
				}
				// XSD 1.0 constrained xs:anyURI to RFC 2396 URI references; 1.1
				// dropped the constraint (anyURI_a003/b004/b006 invalid@1.0).
				if ver == Version10 && prim == xpath.XSanyURI &&
					!validAnyURI10(applyWhiteSpace("collapse", e)) {
					return invalidf("", "enumeration value %q is not a valid RFC 2396 URI reference", e)
				}
			}
		}
		if err := checkBoundOrder(f, prim); err != nil {
			return err
		}
	}

	// Mutual consistency (independent of variety).
	if f.hasMinIncl && f.hasMinExcl {
		return invalidf("", "minInclusive and minExclusive are mutually exclusive")
	}
	if f.hasMaxIncl && f.hasMaxExcl {
		return invalidf("", "maxInclusive and maxExclusive are mutually exclusive")
	}
	// length with minLength/maxLength: decided in checkFacetConsistency,
	// where the base chain is visible (legal iff the min/max facet is
	// inherited from an ancestor's {facets} — WG bug 6446).
	if f.hasMinLength && f.hasMaxLength && f.minLength > f.maxLength {
		return invalidf("", "minLength exceeds maxLength")
	}
	if f.hasTotalDigits && f.hasFractionDigits && f.fractionDigits > f.totalDigits {
		return invalidf("", "fractionDigits exceeds totalDigits")
	}
	return nil
}

// checkBoundOrder rejects a lower bound greater than the upper bound (an empty
// value space). It compares the casted bound values exactly.
func checkBoundOrder(f *facetSet, prim xpath.AtomType) error {
	loVal, loSet, loExcl := f.minIncl, f.hasMinIncl, false
	if f.hasMinExcl {
		loVal, loSet, loExcl = f.minExcl, true, true
	}
	hiVal, hiSet, hiExcl := f.maxIncl, f.hasMaxIncl, false
	if f.hasMaxExcl {
		hiVal, hiSet, hiExcl = f.maxExcl, true, true
	}
	if !loSet || !hiSet {
		return nil
	}
	lo, e1 := xpath.CastTo(xpath.NewString(applyWhiteSpace("collapse", loVal)), prim)
	hi, e2 := xpath.CastTo(xpath.NewString(applyWhiteSpace("collapse", hiVal)), prim)
	if e1 != nil || e2 != nil {
		return nil
	}
	c, ok := xpath.CompareAtomic(lo, hi)
	if !ok {
		return nil
	}
	if c > 0 || (c == 0 && (loExcl || hiExcl)) {
		return invalidf("", "lower bound %q exceeds upper bound %q", loVal, hiVal)
	}
	return nil
}

// boundsApplicable reports whether min/max In/Exclusive apply to prim (the
// ordered types: numeric, date/time, duration).
func boundsApplicable(t xpath.AtomType) bool {
	return isNumericLike(t) || isDateLike(t) || isDurationLike(t)
}

// lengthFacetAllowed reports whether the length family is a legal facet for prim
// (string family, anyURI, binary, QName, NOTATION — length on QName/NOTATION is
// a legal but always-satisfied constraint).
func lengthFacetAllowed(t xpath.AtomType) bool {
	switch t {
	case xpath.XSstring, xpath.XSnormalizedString, xpath.XStoken, xpath.XSlanguage,
		xpath.XSname, xpath.XSncname, xpath.XSid, xpath.XSidref, xpath.XSentity,
		xpath.XSnmtoken, xpath.XSanyURI, xpath.XShexBinary, xpath.XSbase64Binary,
		xpath.XSqname, xpath.XSnotation, xpath.XSanyAtomicType:
		return true
	}
	return false
}

// digitsApplicable reports whether totalDigits/fractionDigits apply to prim
// (decimal and the integer tower — not float/double).
func digitsApplicable(t xpath.AtomType) bool {
	return t == xpath.XSdecimal || isIntegerLike(t)
}

// check applies the atomic constraining facets to an already-normalized value of
// primitive type prim.
func (f *facetSet) check(v string, prim xpath.AtomType) error { return f.checkIn(v, prim, nil) }

func (f *facetSet) checkIn(v string, prim xpath.AtomType, at *xmltree.Node) error {
	if err := f.checkPatternEnumIn(v, prim, at); err != nil {
		return err
	}
	if err := f.checkLength(v, prim); err != nil {
		return err
	}
	if err := f.checkBounds(v, prim); err != nil {
		return err
	}
	if f.explicitTZ != "" && dateTimePrim(prim) {
		has := lexicalHasTZ(v)
		switch f.explicitTZ {
		case "required":
			if !has {
				return invalidf("cvc-explicitTimezone-valid", "value %q must carry a timezone", v)
			}
		case "prohibited":
			if has {
				return invalidf("cvc-explicitTimezone-valid", "value %q must not carry a timezone", v)
			}
		}
	}
	return f.checkDigits(v)
}

// dateTimePrim reports whether prim is one of the nine date/time primitives
// the explicitTimezone facet applies to.
func dateTimePrim(prim xpath.AtomType) bool {
	switch prim {
	case xpath.XSdateTime, xpath.XSdateTimeStamp, xpath.XSdate, xpath.XStime,
		xpath.XSgYear, xpath.XSgYearMonth, xpath.XSgMonth, xpath.XSgMonthDay, xpath.XSgDay:
		return true
	}
	return false
}

var reTZSuffix = regexp.MustCompile(`(Z|[+-][0-9]{2}:[0-9]{2})$`)

// lexicalHasTZ reports whether a date/time lexical ends in a timezone part.
func lexicalHasTZ(v string) bool { return reTZSuffix.MatchString(strings.TrimSpace(v)) }

func (f *facetSet) checkPatternEnum(v string, prim xpath.AtomType) error {
	return f.checkPatternEnumIn(v, prim, nil)
}

func (f *facetSet) checkPatternEnumIn(v string, prim xpath.AtomType, at *xmltree.Node) error {
	// pattern groups: match ≥1 alternative in every group (base patterns are a
	// separate group, checked at that level ⇒ AND across levels).
	for _, group := range f.patterns {
		ok := false
		for _, re := range group {
			if re.MatchString(v) {
				ok = true
				break
			}
		}
		if !ok {
			return invalidf("cvc-pattern-valid", "value %q does not match the required pattern", v)
		}
	}
	if f.hasEnum {
		ok := false
		qnamey := prim == xpath.XSqname || prim == xpath.XSnotation
		for i, e := range f.enumeration {
			if valueEqual(v, e, prim) {
				ok = true
				break
			}
			// An enumeration over xs:QName/xs:NOTATION constrains the VALUE
			// space, whose members are EXPANDED names: the facet's prefix and
			// the instance's need not be spelled alike, only bound alike
			// (XSD 1.0 Part 2 §4.3.5 + §3.2.19; notation-0301..0305/0401..
			// 0404/0701/0702 bind "one" and "smokey" to one namespace). Each
			// side is expanded against its OWN declaring element.
			if qnamey && at != nil && i < len(f.enumQName) && f.enumQName[i] != "" {
				if resolveQName(at, v).String() == f.enumQName[i] {
					ok = true
					break
				}
			}
		}
		if !ok {
			return invalidf("cvc-enumeration-valid", "value %q is not in the enumeration", v)
		}
	}
	return nil
}

func (f *facetSet) checkListLength(n int) error {
	if f.hasLength && n != f.length {
		return invalidf("cvc-length-valid", "list length %d != %d", n, f.length)
	}
	if f.hasMinLength && n < f.minLength {
		return invalidf("cvc-minLength-valid", "list length %d < %d", n, f.minLength)
	}
	if f.hasMaxLength && n > f.maxLength {
		return invalidf("cvc-maxLength-valid", "list length %d > %d", n, f.maxLength)
	}
	return nil
}

func (f *facetSet) checkLength(v string, prim xpath.AtomType) error {
	if !f.hasLength && !f.hasMinLength && !f.hasMaxLength {
		return nil
	}
	// The length family is only measured for the string family, anyURI and the
	// binary types. On other primitives (QName, NOTATION, numeric, date, …) the
	// facet has no character-length meaning and is treated as satisfied.
	if !lengthApplies(prim) {
		return nil
	}
	n := valueLength(v, prim)
	if f.hasLength && n != f.length {
		return invalidf("cvc-length-valid", "length %d != %d", n, f.length)
	}
	if f.hasMinLength && n < f.minLength {
		return invalidf("cvc-minLength-valid", "length %d < %d", n, f.minLength)
	}
	if f.hasMaxLength && n > f.maxLength {
		return invalidf("cvc-maxLength-valid", "length %d > %d", n, f.maxLength)
	}
	return nil
}

func (f *facetSet) checkBounds(v string, prim xpath.AtomType) error {
	if !(f.hasMinIncl || f.hasMaxIncl || f.hasMinExcl || f.hasMaxExcl) {
		return nil
	}
	val, err := xpath.CastTo(xpath.NewString(v), prim)
	if err != nil {
		return invalidf("cvc-datatype-valid", "%q is not a valid %s", v, prim)
	}
	cmp := func(bound string) (int, bool) {
		b, err := xpath.CastTo(xpath.NewString(bound), prim)
		if err != nil {
			return 0, false
		}
		return xpath.CompareAtomic(val, b)
	}
	if f.hasMinIncl {
		if c, ok := cmp(f.minIncl); ok && c < 0 {
			return invalidf("cvc-minInclusive-valid", "%q < minInclusive %s", v, f.minIncl)
		}
	}
	if f.hasMaxIncl {
		if c, ok := cmp(f.maxIncl); ok && c > 0 {
			return invalidf("cvc-maxInclusive-valid", "%q > maxInclusive %s", v, f.maxIncl)
		}
	}
	if f.hasMinExcl {
		if c, ok := cmp(f.minExcl); ok && c <= 0 {
			return invalidf("cvc-minExclusive-valid", "%q <= minExclusive %s", v, f.minExcl)
		}
	}
	if f.hasMaxExcl {
		if c, ok := cmp(f.maxExcl); ok && c >= 0 {
			return invalidf("cvc-maxExclusive-valid", "%q >= maxExclusive %s", v, f.maxExcl)
		}
	}
	return nil
}

func (f *facetSet) checkDigits(v string) error {
	if !f.hasTotalDigits && !f.hasFractionDigits {
		return nil
	}
	total, frac := countDigits(v)
	if f.hasTotalDigits && total > f.totalDigits {
		return invalidf("cvc-totalDigits-valid", "%d total digits > %d", total, f.totalDigits)
	}
	if f.hasFractionDigits && frac > f.fractionDigits {
		return invalidf("cvc-fractionDigits-valid", "%d fraction digits > %d", frac, f.fractionDigits)
	}
	return nil
}

// valueEqual compares two lexicals by *value* for value-based enumeration; for
// non-comparable types it falls back to lexical equality.
func valueEqual(v, e string, prim xpath.AtomType) bool {
	if isNumericLike(prim) || isDateLike(prim) || isDurationLike(prim) || prim == xpath.XSboolean {
		a, ea := xpath.CastTo(xpath.NewString(v), prim)
		b, eb := xpath.CastTo(xpath.NewString(e), prim)
		if ea != nil || eb != nil {
			return v == e
		}
		if c, ok := xpath.CompareAtomic(a, b); ok {
			return c == 0
		}
	}
	return v == e
}

func valueLength(v string, prim xpath.AtomType) int {
	switch prim {
	case xpath.XShexBinary:
		return len(strings.Map(dropSpace, v)) / 2
	case xpath.XSbase64Binary:
		s := strings.Map(dropSpace, v)
		n := len(s) / 4 * 3
		for strings.HasSuffix(s, "=") {
			n--
			s = s[:len(s)-1]
		}
		return n
	default:
		return utf8.RuneCountInString(v)
	}
}

func dropSpace(r rune) rune {
	if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
		return -1
	}
	return r
}

// countDigits returns the total and fractional significant-digit counts of a
// decimal lexical (approximate: leading/trailing zeros stripped).
func countDigits(v string) (total, frac int) {
	v = strings.TrimSpace(v)
	v = strings.TrimLeft(v, "+-")
	intPart, fracPart := v, ""
	if i := strings.IndexByte(v, '.'); i >= 0 {
		intPart, fracPart = v[:i], v[i+1:]
	}
	intPart = strings.TrimLeft(intPart, "0")
	fracPart = strings.TrimRight(fracPart, "0")
	return len(intPart) + len(fracPart), len(fracPart)
}

// lengthApplies reports whether the length/minLength/maxLength facets are
// measured for prim (string family, anyURI, and the binary types). Lists are
// handled separately via checkListLength.
func lengthApplies(t xpath.AtomType) bool {
	switch t {
	case xpath.XSstring, xpath.XSnormalizedString, xpath.XStoken, xpath.XSlanguage,
		xpath.XSname, xpath.XSncname, xpath.XSid, xpath.XSidref, xpath.XSentity,
		xpath.XSnmtoken, xpath.XSanyURI, xpath.XShexBinary, xpath.XSbase64Binary:
		return true
	}
	return false
}

func isNumericLike(t xpath.AtomType) bool {
	switch t {
	case xpath.XSdecimal, xpath.XSinteger, xpath.XSnonNegativeInteger, xpath.XSpositiveInteger,
		xpath.XSnonPositiveInteger, xpath.XSnegativeInteger, xpath.XSlong, xpath.XSint,
		xpath.XSshort, xpath.XSbyte, xpath.XSunsignedLong, xpath.XSunsignedInt,
		xpath.XSunsignedShort, xpath.XSunsignedByte, xpath.XSdouble, xpath.XSfloat:
		return true
	}
	return false
}

func isDateLike(t xpath.AtomType) bool {
	switch t {
	case xpath.XSdate, xpath.XSdateTime, xpath.XSdateTimeStamp, xpath.XStime,
		xpath.XSgYearMonth, xpath.XSgYear, xpath.XSgMonthDay, xpath.XSgDay, xpath.XSgMonth:
		return true
	}
	return false
}

func isDurationLike(t xpath.AtomType) bool {
	switch t {
	case xpath.XSduration, xpath.XSyearMonthDuration, xpath.XSdayTimeDuration:
		return true
	}
	return false
}
