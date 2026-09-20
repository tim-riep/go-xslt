package xpath

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// AtomType identifies an XSD atomic type. The set covers the built-in types
// referenced by XPath 3.1 (the numeric tower, string/boolean, the date/time and
// duration families, binary, anyURI, QName, untypedAtomic, and the gregorian
// types).
type AtomType int

const (
	XSuntypedAtomic AtomType = iota
	XSanyAtomicType
	XSstring
	XSnormalizedString
	XStoken
	XSlanguage
	XSname
	XSncname
	XSid
	XSidref
	XSentity
	XSnmtoken
	XSboolean
	XSdecimal
	XSinteger
	XSnonNegativeInteger
	XSpositiveInteger
	XSnonPositiveInteger
	XSnegativeInteger
	XSlong
	XSint
	XSshort
	XSbyte
	XSunsignedLong
	XSunsignedInt
	XSunsignedShort
	XSunsignedByte
	XSdouble
	XSfloat
	XSdate
	XSdateTime
	XSdateTimeStamp
	XStime
	XSduration
	XSyearMonthDuration
	XSdayTimeDuration
	XShexBinary
	XSbase64Binary
	XSanyURI
	XSqname
	XSnotation
	XSgYearMonth
	XSgYear
	XSgMonthDay
	XSgDay
	XSgMonth
	XSnumeric // the union(double,float,decimal) — usable as a type / constructor
	XSerror   // empty value space: matches no value (xs:error)
)

// atomTypeNames maps types to their lexical QName (xs: prefix) for instance-of,
// error messages, and the type() accessor.
var atomTypeNames = map[AtomType]string{
	XSuntypedAtomic: "xs:untypedAtomic", XSanyAtomicType: "xs:anyAtomicType",
	XSstring: "xs:string", XSnormalizedString: "xs:normalizedString", XStoken: "xs:token",
	XSlanguage: "xs:language", XSname: "xs:Name", XSncname: "xs:NCName",
	XSid: "xs:ID", XSidref: "xs:IDREF", XSentity: "xs:ENTITY", XSnmtoken: "xs:NMTOKEN",
	XSboolean: "xs:boolean", XSdecimal: "xs:decimal", XSinteger: "xs:integer",
	XSnonNegativeInteger: "xs:nonNegativeInteger", XSpositiveInteger: "xs:positiveInteger",
	XSnonPositiveInteger: "xs:nonPositiveInteger", XSnegativeInteger: "xs:negativeInteger",
	XSlong: "xs:long", XSint: "xs:int", XSshort: "xs:short", XSbyte: "xs:byte",
	XSunsignedLong: "xs:unsignedLong", XSunsignedInt: "xs:unsignedInt",
	XSunsignedShort: "xs:unsignedShort", XSunsignedByte: "xs:unsignedByte",
	XSdouble: "xs:double", XSfloat: "xs:float",
	XSdate: "xs:date", XSdateTime: "xs:dateTime", XSdateTimeStamp: "xs:dateTimeStamp",
	XStime: "xs:time", XSduration: "xs:duration",
	XSyearMonthDuration: "xs:yearMonthDuration", XSdayTimeDuration: "xs:dayTimeDuration",
	XShexBinary: "xs:hexBinary", XSbase64Binary: "xs:base64Binary",
	XSanyURI: "xs:anyURI", XSqname: "xs:QName", XSnotation: "xs:NOTATION",
	XSgYearMonth: "xs:gYearMonth", XSgYear: "xs:gYear", XSgMonthDay: "xs:gMonthDay",
	XSgDay: "xs:gDay", XSgMonth: "xs:gMonth",
}

var atomTypeByLocal = func() map[string]AtomType {
	m := map[string]AtomType{}
	for t, n := range atomTypeNames {
		m[strings.TrimPrefix(n, "xs:")] = t
	}
	m["numeric"] = XSnumeric // no canonical String() name; type/constructor only
	m["error"] = XSerror     // empty value space: constructor of () → (), else error
	return m
}()

// AtomTypeByName resolves an "xs:"-prefixed (or bare) local type name.
func AtomTypeByName(name string) (AtomType, bool) {
	name = strings.TrimPrefix(name, "xs:")
	t, ok := atomTypeByLocal[name]
	return t, ok
}

func (t AtomType) String() string {
	if n, ok := atomTypeNames[t]; ok {
		return n
	}
	return "xs:anyAtomicType"
}

// Duration models the XSD duration family: signed months (year-month part) and
// signed seconds (day-time part).
type Duration struct {
	Months int
	Secs   float64
}

// Atomic is a typed atomic value. Only the field(s) relevant to T are populated.
type Atomic struct {
	T     AtomType
	s     string       // string family, anyURI, untypedAtomic, gregorian lexical
	b     bool         // boolean
	i     *big.Int     // integer family
	d     *big.Rat     // decimal
	f     float64      // double, float
	tm    time.Time    // date, dateTime, time
	hasTZ bool         // whether tm carries an explicit timezone
	dur   Duration     // duration family
	bin   []byte       // binary
	qn    xmltree.Name // QName / NOTATION
	// st is the USER-DEFINED schema type this value was validated against,
	// when there is one: T alone records only the built-in primitive its value
	// space sits in, which cannot distinguish my:hatsize from xs:integer.
	//
	// Set only where a schema actually said so — atomizing a validated node,
	// a cast to a named simple type, a constructor call — so it is nil for
	// every value in a non-schema-aware run, and `instance of my:T` is then
	// false for everything, which is the fail-closed answer.
	st *xmltree.SchemaTypeName
}

// SchemaType returns the user-defined schema type this value was validated
// against, or nil when it carries only a built-in type.
func (a *Atomic) SchemaType() *xmltree.SchemaTypeName {
	if a == nil {
		return nil
	}
	return a.st
}

// withSchemaType returns a copy of a carrying the named schema type. A copy,
// never a mutation: atomic values are shared freely (cached constants, values
// read straight out of a map or array), so stamping one in place would leak a
// type identity into every other holder of the same value.
func (a *Atomic) withSchemaType(n *xmltree.SchemaTypeName) *Atomic {
	if a == nil || n == nil || n.Complex {
		return a
	}
	b := *a
	b.st = n
	return &b
}

// --- constructors -----------------------------------------------------------

func NewString(s string) *Atomic  { return &Atomic{T: XSstring, s: s} }
func NewUntyped(s string) *Atomic { return &Atomic{T: XSuntypedAtomic, s: s} }
func NewAnyURI(s string) *Atomic  { return &Atomic{T: XSanyURI, s: s} }
func NewBool(b bool) *Atomic      { return &Atomic{T: XSboolean, b: b} }
func NewDouble(f float64) *Atomic { return &Atomic{T: XSdouble, f: f} }

// NewFloat rounds to single precision: the xs:float value space is IEEE
// binary32, so xs:float(1.01) is 1.0099999904632568 and is NOT equal to
// xs:double(1.01) (fn-deep-equal-mix-args-019); Lexical already formats it
// with 32-bit shortest digits, so it still prints as "1.01".
func NewFloat(f float64) *Atomic { return &Atomic{T: XSfloat, f: float64(float32(f))} }

// smallInts caches the xs:integer values -16..1023 as shared Atomics. An
// Atomic is never mutated after construction, so sharing is safe, and it
// spares a big.Int allocation per integer on the hottest paths — a range
// like "1 to 1000" materialized inside a predicate for every item
// (normalize-unicode-008 evaluates $lines[position()=1 to 1000] over ~18,000
// lines: 18 million integers).
var smallInts = func() [1040]*Atomic {
	var t [1040]*Atomic
	for i := range t {
		t[i] = &Atomic{T: XSinteger, i: big.NewInt(int64(i) - 16)}
	}
	return t
}()

func NewInteger(n int64) *Atomic {
	if n >= -16 && n < 1024 {
		return smallInts[n+16]
	}
	return &Atomic{T: XSinteger, i: big.NewInt(n)}
}
func NewIntegerBig(n *big.Int) *Atomic       { return &Atomic{T: XSinteger, i: n} }
func NewQName(n xmltree.Name) *Atomic        { return &Atomic{T: XSqname, qn: n} }
func NewBinary(t AtomType, b []byte) *Atomic { return &Atomic{T: t, bin: b} }

func NewDecimal(r *big.Rat) *Atomic { return &Atomic{T: XSdecimal, d: r} }

func NewDecimalFromString(s string) (*Atomic, bool) {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, false
	}
	return &Atomic{T: XSdecimal, d: r}, true
}

func NewDateTime(t AtomType, tm time.Time, hasTZ bool) *Atomic {
	return &Atomic{T: t, tm: tm, hasTZ: hasTZ}
}

func NewDuration(t AtomType, d Duration) *Atomic { return &Atomic{T: t, dur: d} }

// --- classification ---------------------------------------------------------

func isIntegerType(t AtomType) bool {
	switch t {
	case XSinteger, XSnonNegativeInteger, XSpositiveInteger, XSnonPositiveInteger,
		XSnegativeInteger, XSlong, XSint, XSshort, XSbyte, XSunsignedLong,
		XSunsignedInt, XSunsignedShort, XSunsignedByte:
		return true
	}
	return false
}

func isStringType(t AtomType) bool {
	switch t {
	case XSstring, XSnormalizedString, XStoken, XSlanguage, XSname, XSncname,
		XSid, XSidref, XSentity, XSnmtoken:
		return true
	}
	return false
}

func isNumericType(t AtomType) bool {
	return isIntegerType(t) || t == XSdecimal || t == XSdouble || t == XSfloat
}

func isDateTimeType(t AtomType) bool {
	switch t {
	case XSdate, XSdateTime, XSdateTimeStamp, XStime, XSgYearMonth, XSgYear,
		XSgMonthDay, XSgDay, XSgMonth:
		return true
	}
	return false
}

func isDurationType(t AtomType) bool {
	return t == XSduration || t == XSyearMonthDuration || t == XSdayTimeDuration
}

// IsNumeric reports whether the atomic holds a numeric value.
func (a *Atomic) IsNumeric() bool { return isNumericType(a.T) }

// IsStringFamily reports whether a is a genuine xs:string-derived value (NOT
// xs:untypedAtomic, which is deliberately type-agnostic).
func (a *Atomic) IsStringFamily() bool { return isStringType(a.T) }

// IsOrderableNonString reports whether a's type is comparable via
// CompareAtomic but is neither numeric nor string-family (the date/time and
// yearMonthDuration/dayTimeDuration primitives) — for hosts implementing
// xsl:sort's default comparison, which for such values orders by VALUE
// rather than by their lexical string form (date-019/date-054: dateTime/
// yearMonthDuration keys with no @data-type sort chronologically/by
// magnitude, not lexically). The general xs:duration is deliberately
// excluded: it has no defined 'lt' ordering (mixed month/second components
// aren't totally comparable), so a host sorting by it with no @data-type
// must reject it (XTDE1030 — date-053), not silently order it somehow.
func (a *Atomic) IsOrderableNonString() bool {
	if a.IsNumeric() || isComparableAsString(a) || a.T == XSduration {
		return false
	}
	return isDateTimeType(a.T) || isDurationType(a.T)
}

// IsUnorderedDuration reports whether a is the general xs:duration type
// (not a yearMonthDuration/dayTimeDuration subtype) — see IsOrderableNonString.
func (a *Atomic) IsUnorderedDuration() bool { return a.T == XSduration }

// Float returns the value as a float64 (for numeric and where meaningful).
func (a *Atomic) Float() float64 {
	switch {
	case isIntegerType(a.T):
		if a.i.IsInt64() {
			return float64(a.i.Int64()) // exact below 2^53, and allocation-free
		}
		f := new(big.Float).SetInt(a.i)
		v, _ := f.Float64()
		return v
	case a.T == XSdecimal:
		v, _ := a.d.Float64()
		return v
	case a.T == XSdouble || a.T == XSfloat:
		return a.f
	case a.T == XSboolean:
		if a.b {
			return 1
		}
		return 0
	default:
		return parseNumber(a.Lexical())
	}
}

// Bool returns the boolean value (only meaningful for xs:boolean).
func (a *Atomic) Bool() bool { return a.b }

// QNameValue returns the expanded name of an xs:QName / xs:NOTATION atomic,
// and whether the atomic is one. Hosts (XSLT's xsl:evaluate/@with-params map
// keys) need the parts, which are otherwise unexported.
func (a *Atomic) QNameValue() (xmltree.Name, bool) {
	if a == nil || (a.T != XSqname && a.T != XSnotation) {
		return xmltree.Name{}, false
	}
	return a.qn, true
}

// TimeValue returns the stored time value and whether it carries an explicit
// timezone (zero Time for non-date/time atomics).
func (a *Atomic) TimeValue() (time.Time, bool) { return a.tm, a.hasTZ }

// --- canonical lexical form -------------------------------------------------

// Lexical returns the canonical string representation of the atomic value.
func (a *Atomic) Lexical() string {
	switch {
	case a.T == XSboolean:
		if a.b {
			return "true"
		}
		return "false"
	case isIntegerType(a.T):
		if a.i == nil {
			return "0"
		}
		return a.i.String()
	case a.T == XSdecimal:
		return formatDecimal(a.d)
	case a.T == XSdouble || a.T == XSfloat:
		if a.T == XSfloat {
			return formatDouble(a.f, 32)
		}
		return formatDouble(a.f, 64)
	case isStringType(a.T) || a.T == XSuntypedAtomic || a.T == XSanyURI:
		return a.s
	case a.T == XSqname || a.T == XSnotation:
		// xs:NOTATION shares xs:QName's value space (XSD 1.0 Part 2 §3.2.19)
		// and therefore its lexical form too.
		if a.qn.Prefix != "" {
			return a.qn.Prefix + ":" + a.qn.Local
		}
		return a.qn.Local
	case a.T == XShexBinary:
		return strings.ToUpper(hexEncode(a.bin))
	case a.T == XSbase64Binary:
		return base64Encode(a.bin)
	case isDurationType(a.T):
		return formatDuration(a.T, a.dur)
	case isDateTimeType(a.T):
		return formatDateTime(a.T, a.tm, a.hasTZ)
	}
	return a.s
}

// formatDecimal renders an xs:decimal in canonical form (no exponent, minimal
// trailing zeros, always at least one digit each side of the point if present).
func formatDecimal(r *big.Rat) string {
	if r == nil {
		return "0"
	}
	if r.IsInt() {
		return r.Num().String()
	}
	s := r.FloatString(decimalFractionDigits(r))
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	return s
}

// decimalFractionDigits picks how many digits after the point FloatString
// needs to render r EXACTLY, when that's possible, rather than a fixed
// truncation. r (already in lowest terms, as every big.Rat is) has a
// TERMINATING decimal expansion exactly when its denominator's only prime
// factors are 2 and 5 — math-3313/3315: a decimal `mod`/arithmetic result
// can terminate with dozens of leading zero digits after the point (e.g.
// "0.000000000000000000000000019999", 26 leading zeros), and a fixed
// 18-digit cutoff would silently round such a value to "0" even though it is
// exactly representable. When the denominator has OTHER prime factors (a
// genuinely repeating decimal, e.g. from 1.0 div 3.0), 18 digits remains the
// used precision, matching XPath F&O's "at least 18 digits" `div` guarantee.
func decimalFractionDigits(r *big.Rat) int {
	const fallback = 18
	tmp := new(big.Int).Set(r.Denom())
	two, five := big.NewInt(2), big.NewInt(5)
	mod := new(big.Int)
	a, b := 0, 0
	for {
		mod.Mod(tmp, two)
		if mod.Sign() != 0 {
			break
		}
		tmp.Div(tmp, two)
		a++
	}
	for {
		mod.Mod(tmp, five)
		if mod.Sign() != 0 {
			break
		}
		tmp.Div(tmp, five)
		b++
	}
	if tmp.Cmp(big.NewInt(1)) != 0 {
		return fallback // not exactly representable in decimal
	}
	n := a
	if b > n {
		n = b
	}
	if n < fallback {
		n = fallback
	}
	return n
}

// formatDouble renders an xs:double (bits=64) or xs:float (bits=32) per the
// XPath canonical rules: decimal notation for 1e-6 <= |x| < 1e6, otherwise
// scientific notation with a "d.ddd" mantissa and a bare exponent (e.g.
// 1.26743233E15). Shortest round-tripping digits at the given precision.
func formatDouble(f float64, bits int) string {
	if math.IsNaN(f) {
		return "NaN"
	}
	if math.IsInf(f, 1) {
		return "INF"
	}
	if math.IsInf(f, -1) {
		return "-INF"
	}
	if f == 0 {
		if math.Signbit(f) {
			return "-0"
		}
		return "0"
	}
	abs := math.Abs(f)
	if abs >= 1e-6 && abs < 1e6 {
		return strconv.FormatFloat(f, 'f', -1, bits)
	}
	// Scientific form. Go's 'E' gives e.g. "1.26743233E+15" or "1E+15".
	s := strconv.FormatFloat(f, 'E', -1, bits)
	mant, exp := s, ""
	if i := strings.IndexByte(s, 'E'); i >= 0 {
		mant, exp = s[:i], s[i+1:]
	}
	if !strings.Contains(mant, ".") {
		mant += ".0" // mantissa must carry a decimal point
	}
	// Bare exponent: strip '+' and leading zeros (keep sign for negatives).
	neg := strings.HasPrefix(exp, "-")
	exp = strings.TrimLeft(strings.TrimLeft(exp, "+-"), "0")
	if exp == "" {
		exp = "0"
	}
	if neg {
		exp = "-" + exp
	}
	return mant + "E" + exp
}

var _ = fmt.Sprintf

// IntegerBig returns the exact arbitrary-precision value of an integer-family
// atomic item, for a host that must not round it through float64 (xsl:number's
// @value: number-0111 numbers with 1234567890^3, which no double can hold).
// ok is false for every other item kind.
func IntegerBig(it Item) (*big.Int, bool) {
	a, isAtomic := it.(*Atomic)
	if !isAtomic || !isIntegerType(a.T) || a.i == nil {
		return nil, false
	}
	return new(big.Int).Set(a.i), true
}
