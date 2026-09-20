package xpath

import (
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// itemAtomType returns the atomic type of an item, applying the implied types
// of bare Go scalars (string->xs:string, float64->xs:double, bool->xs:boolean).
func itemAtomType(it Item) (AtomType, bool) {
	switch v := it.(type) {
	case *Atomic:
		return v.T, true
	case string:
		return XSstring, true
	case float64:
		return XSdouble, true
	case bool:
		return XSboolean, true
	}
	return 0, false
}

// ItemAtomTypeTag returns the atomic type tag of an atomic item (an *Atomic,
// or a bare Go string/float64/bool) as an int32 suitable for
// xmltree.Node.TypeAnno. A host that must model a non-node sequence ITEM as a
// synthetic node — the context item of an xsl:for-each/filter/analyze-string
// iteration over an atomic sequence — stamps this so atomization later
// recovers the item's exact original type instead of degrading it to
// xs:untypedAtomic (position-0102: "." over an xs:integer item must stay
// xs:integer through arithmetic, not promote to xs:double as untypedAtomic
// would). ok is false for a non-atomic item (a node, map, array, or
// function), which the caller should leave unstamped.
func ItemAtomTypeTag(it Item) (int32, bool) {
	t, ok := itemAtomType(it)
	return int32(t), ok
}

// AtomicFromItem coerces a bare scalar, node, or *Atomic item to an *Atomic
// (exported wrapper around toAtomic, for hosts — e.g. the XSLT engine's
// xsl:merge cross-source key comparability check, XTTE2230 — that need this
// outside the fn:-call path).
func AtomicFromItem(it Item) (*Atomic, error) { return toAtomic(it) }

// toAtomic coerces a bare scalar or *Atomic item to an *Atomic. Nodes are
// atomized to xs:untypedAtomic; non-atomizable items return an error.
func toAtomic(it Item) (*Atomic, error) {
	switch v := it.(type) {
	case *Atomic:
		return v, nil
	case string:
		return NewString(v), nil
	case float64:
		return NewDouble(v), nil
	case bool:
		return NewBool(v), nil
	case *xmltree.Node:
		// A comment, namespace or processing-instruction node's typed-value is
		// always xs:string (XDM 3.1 5.13/6.5.4), regardless of schema
		// validation — unlike every other node kind, it never carries
		// xs:untypedAtomic (accessor-021/022/026: data() on comment()/
		// processing-instruction()/namespace::* must be an xs:string, not
		// xs:untypedAtomic).
		if v.Kind == xmltree.KindComment || v.Kind == xmltree.KindPI || v.Kind == xmltree.KindNamespace {
			return NewString(v.StringValue()), nil
		}
		// A QName- or NOTATION-annotated node needs its own prefix resolution
		// — CastTo has no namespace context (assert024: @name eq
		// xsd:QName('xsd:element')).
		//
		// xs:NOTATION shares xs:QName's value space (XSD 1.0 Part 2 §3.2.19:
		// expanded names) and must take this branch for a second reason: it is
		// an ABSTRACT type, so CastTo refuses it outright (XPST0080) and
		// nodeTypedValue would read that refusal as "not annotated after all"
		// and hand back xs:untypedAtomic. That rule is about `cast as`, not
		// about the typed value of a node the PSVI already annotated
		// (notation-0103/0201/0202/0501/0502), and building the value here
		// leaves the cast path untouched.
		if at, ok := nodeAnnotationType(v); ok && (at == XSqname || at == XSnotation) {
			s := strings.TrimSpace(v.StringValue())
			pre, local := "", s
			if i := strings.IndexByte(s, ':'); i >= 0 {
				pre, local = s[:i], s[i+1:]
			}
			if uri, ok := v.LookupPrefix(pre); ok || pre == "" {
				// The prefix is kept: it is what the lexical form of the value
				// is built from (notation-0303 asserts the written prefixes).
				//
				// The node's USER-DEFINED type identity is carried across just
				// as the general path below does it: a NOTATION is only ever
				// usable through a named restriction of xs:NOTATION (XSD 1.0
				// Part 2 §3.2.19), so `data(@a) instance of my:nota` — exactly
				// what notation-0101/0102 ask — can only be answered from the
				// annotation's own name, never from the primitive.
				qa := &Atomic{T: at, qn: xmltree.Name{Space: uri, Local: local, Prefix: pre}}
				return qa.withSchemaType(v.SchemaType), nil
			}
		}
		// A type-annotated node (XSD assertion trees, schema validation)
		// atomizes to its typed value — see nodeTypedValue (typeanno.go) —
		// which KEEPS the node's user-defined type identity: `data($e)
		// instance of my:hatsize` is the whole point of having validated it,
		// and the built-in primitive alone cannot answer that.
		if a, ok := nodeTypedValue(v); ok {
			return a.withSchemaType(nodeValueTypeName(v)), nil
		}
		return NewUntyped(v.StringValue()), nil
	}
	return nil, fmt.Errorf("item of type %T is not atomizable", it)
}

// Atomize implements fn:data: it converts a value to a sequence of atomic
// items. Nodes yield xs:untypedAtomic; arrays atomize their members; maps and
// functions raise a type error.
func Atomize(o Object) ([]Item, error) {
	var out []Item
	for _, it := range Items(o) {
		switch v := it.(type) {
		case *Array:
			for _, m := range v.Members() {
				sub, err := Atomize(m)
				if err != nil {
					return nil, err
				}
				out = append(out, sub...)
			}
		case *Map, *Function:
			return nil, fmt.Errorf("err:FOTY0012: cannot atomize a %T", it)
		default:
			if nd, isNode := it.(*xmltree.Node); isNode {
				// XDM 3.1 §5.3 dm:typed-value: a NILLED element's typed value
				// is the empty sequence, whatever its declared type — checked
				// before anything else, since a nilled element can carry any
				// content type otherwise (fn-nilled-38/39/47/51: nilled
				// elements of a mixed, simple-content-complex, and
				// user-defined simple type all atomize to empty).
				if nd.Kind == xmltree.KindElement && nd.Nilled {
					continue
				}
				// A LIST-typed node atomizes to a SEQUENCE, so it is handled
				// here rather than in toAtomic, whose one-*Atomic result
				// cannot carry it (see nodeTypedItems).
				if items, ok := nodeTypedItems(nd); ok {
					out = append(out, items...)
					continue
				}
				// A validated element whose type has no simple content at
				// all — element-only complex content, with TypeAnno left at
				// zero because there is no built-in primitive to atomize
				// through (see internal/xsd/bridge.go's typeAnnoFor) — has
				// no typed value: F&O 3.1's atomization rule raises
				// err:FOTY0012 rather than falling back to a lenient
				// untypedAtomic reading (fn-normalize-space-24,
				// fn-string-length-23). Scoped to a node with at least one
				// ELEMENT child and no non-whitespace text of its own, so a
				// genuinely MIXED-content type (whose typed value IS the
				// untypedAtomic string value, not an error — this engine
				// has no separate "mixed vs element-only" bit on
				// SchemaTypeName to tell the two apart from the annotation
				// alone) is not misclassified merely for lacking one.
				if isElementOnlyCandidate(nd) {
					return nil, fmt.Errorf("err:FOTY0012: element %q has element-only content and no typed value", nd.Name.Local)
				}
			}
			a, err := toAtomic(it)
			if err != nil {
				return nil, err
			}
			out = append(out, a)
		}
	}
	return out, nil
}

// isElementOnlyCandidate reports whether a validated element's simple-content
// facts (SchemaType.Complex true, no simple typed value at all — see the
// caller in Atomize) make it a genuine element-only-content case rather than
// a mixed-content one, which this engine cannot otherwise tell apart from the
// annotation alone. An element with at least one ELEMENT child and no
// non-whitespace text of its own is exactly what element-only content always
// looks like (character data other than whitespace is not permitted there at
// all); a mixed-content element, by contrast, ordinarily carries real text.
func isElementOnlyCandidate(nd *xmltree.Node) bool {
	if nd.Kind != xmltree.KindElement || nd.SchemaType == nil || !nd.SchemaType.Complex ||
		nd.TypeAnno != 0 || nd.ListTyped {
		return false
	}
	hasElem := false
	for _, c := range nd.Children {
		switch c.Kind {
		case xmltree.KindElement:
			hasElem = true
		case xmltree.KindText:
			if strings.TrimSpace(c.Value) != "" {
				return false
			}
		}
	}
	return hasElem
}

// CastTo casts an atomic item to the target type, returning an *Atomic or an
// error (err:FORG0001 for invalid lexical values).
func CastTo(it Item, target AtomType) (*Atomic, error) {
	a, err := toAtomic(it)
	if err != nil {
		return nil, err
	}
	// xs:string and xs:untypedAtomic are universal targets (cast from any type).
	if target == XSuntypedAtomic {
		return NewUntyped(a.Lexical()), nil
	}
	if target == XSstring {
		return NewString(a.Lexical()), nil
	}

	// xs:NOTATION is an abstract type — it can never be a cast/constructor target.
	if target == XSnotation {
		return nil, fmt.Errorf("err:XPST0080: xs:NOTATION is an abstract type")
	}
	// xs:numeric is the union(double,float,decimal): a value that already is
	// a member type is returned unchanged (xs-numeric-013..017: 17 cast as
	// xs:numeric is the xs:integer 17); anything else casts to xs:double
	// (xs-numeric-007: xs:numeric('12') instance of xs:double).
	if target == XSnumeric {
		if a.IsNumeric() {
			return a, nil
		}
		target = XSdouble
	}
	// Numeric targets accept only numeric, boolean and string-like sources;
	// date/time, duration, gregorian, binary, QName and anyURI sources are invalid.
	if isNumericType(target) {
		if !(a.IsNumeric() || a.T == XSboolean || isStringType(a.T) || a.T == XSuntypedAtomic) {
			return nil, fmt.Errorf("err:XPTY0004: cannot cast %s to %s", a.T, target)
		}
	}

	// stringSource reports whether the source can supply a lexical form to a
	// "parse from string" target (anyURI, QName, gregorian, duration, binary,
	// derived string types). Per the XSD casting table, those targets reject
	// numeric/date/duration/binary sources.
	stringSource := isStringType(a.T) || a.T == XSuntypedAtomic || a.T == XSanyURI

	switch {
	case isStringType(target): // derived string types (normalizedString/token/Name/NCName…)
		// Any string source, plus boolean and numeric values, can cast to a
		// (derived) string via their canonical lexical form; the lexical space
		// is then checked below (K2-SeqExprCast-157/158: xs:language(true())).
		if !(stringSource || a.T == XSboolean || a.IsNumeric()) {
			return nil, fmt.Errorf("err:XPTY0004: cannot cast %s to %s", a.T, target)
		}
		lex := whitespaceFacet(target, a.Lexical())
		if !derivedStringValid(target, lex) {
			return nil, fmt.Errorf("err:FORG0001: %q is not a valid %s", a.Lexical(), target)
		}
		return &Atomic{T: target, s: lex}, nil

	case target == XSanyURI:
		if !stringSource {
			return nil, fmt.Errorf("err:XPTY0004: cannot cast %s to xs:anyURI", a.T)
		}
		// NB: XSD 1.1 anyURI accepts even ":a" and "%" (anyURI_a003/b004), so
		// no stricter well-formedness check can run here without breaking the
		// XSD validator, which shares this cast — the XPath cast expression
		// and xs:anyURI() constructor add it (castInContext/anyURIWellFormed).
		// The anyURI whiteSpace facet is collapse (K2-SeqExprCast-208/420).
		return NewAnyURI(whitespaceFacet(XStoken, a.Lexical())), nil

	case target == XSboolean:
		return castToBool(a)

	case isIntegerType(target):
		return castToInteger(a, target)

	case target == XSdecimal:
		return castToDecimal(a)

	case target == XSdouble || target == XSfloat:
		return castToDouble(a, target)

	case isDateTimeType(target):
		var res *Atomic
		if isDateTimeType(a.T) {
			r, err := castDateTimeToDateTime(a, target)
			if err != nil {
				return nil, err
			}
			res = r
		} else {
			if !stringSource {
				return nil, fmt.Errorf("err:XPTY0004: cannot cast %s to %s", a.T, target)
			}
			tm, hasTZ, err := parseDateTimeValue(target, a.Lexical())
			if err != nil {
				return nil, fmt.Errorf("err:FORG0001: %v", err)
			}
			res = NewDateTime(target, tm, hasTZ)
		}
		// xs:dateTimeStamp is an xs:dateTime whose timezone is REQUIRED
		// (xs-dateTimeStamp-3/4: no timezone is FORG0001).
		if target == XSdateTimeStamp && !res.hasTZ {
			return nil, fmt.Errorf("err:FORG0001: xs:dateTimeStamp requires a timezone")
		}
		return res, nil

	case isDurationType(target):
		if isDurationType(a.T) {
			return castDurationToDuration(a, target), nil
		}
		if !stringSource {
			return nil, fmt.Errorf("err:XPTY0004: cannot cast %s to %s", a.T, target)
		}
		d, err := parseDurationValue(target, a.Lexical())
		if err != nil {
			return nil, fmt.Errorf("err:FORG0001: %v", err)
		}
		if target == XSyearMonthDuration && d.Secs != 0 {
			return nil, fmt.Errorf("err:FORG0001: yearMonthDuration with time component")
		}
		if target == XSdayTimeDuration && d.Months != 0 {
			return nil, fmt.Errorf("err:FORG0001: dayTimeDuration with year/month component")
		}
		return NewDuration(target, d), nil

	case target == XShexBinary:
		if a.T == XShexBinary || a.T == XSbase64Binary {
			return NewBinary(XShexBinary, a.bin), nil
		}
		if !stringSource {
			return nil, fmt.Errorf("err:XPTY0004: cannot cast %s to xs:hexBinary", a.T)
		}
		b, err := hexDecode(a.Lexical())
		if err != nil {
			return nil, fmt.Errorf("err:FORG0001: invalid hexBinary")
		}
		return NewBinary(XShexBinary, b), nil

	case target == XSbase64Binary:
		if a.T == XShexBinary || a.T == XSbase64Binary {
			return NewBinary(XSbase64Binary, a.bin), nil
		}
		if !stringSource {
			return nil, fmt.Errorf("err:XPTY0004: cannot cast %s to xs:base64Binary", a.T)
		}
		b, err := base64Decode(a.Lexical())
		if err != nil {
			return nil, fmt.Errorf("err:FORG0001: invalid base64Binary")
		}
		return NewBinary(XSbase64Binary, b), nil

	case target == XSqname || target == XSnotation:
		// The casting table admits string-family, QName and NOTATION sources
		// only: xs:anyURI → xs:QName is XPTY0004 (K-SeqExprCast-1414/1415),
		// while xs:NOTATION → xs:QName is "Y" (F&O 3.0 §19.1's table, shipped
		// with the suite at specs/functions-and-operators-rec30.xml — the two
		// types share one value space, the expanded name), which is exactly
		// what notation-0002's case g asks for.
		if !(stringSource || a.T == XSqname || a.T == XSnotation) || a.T == XSanyURI {
			return nil, fmt.Errorf("err:XPTY0004: cannot cast %s to xs:QName", a.T)
		}
		// A computed xs:untypedAtomic cannot be cast to xs:QName/xs:NOTATION —
		// only a string literal (handled by the xs:QName() constructor) or an
		// existing QName may (K-SeqExprCast-71a/422/423). xs:string is left
		// permitted because the XSD validator casts string instance values.
		if a.T == XSuntypedAtomic {
			return nil, fmt.Errorf("err:XPTY0004: cannot cast xs:untypedAtomic to xs:QName")
		}
		if a.T == XSqname || a.T == XSnotation {
			return NewQName(a.qn), nil
		}
		prefix, local := "", a.Lexical()
		if i := strings.IndexByte(local, ':'); i >= 0 {
			prefix, local = local[:i], local[i+1:]
		}
		return NewQName(xmltree.Name{Prefix: prefix, Local: local}), nil
	}
	return nil, fmt.Errorf("err:XPST0080: cannot cast to %s", target)
}

// derivedStringValid enforces the lexical space of the derived string types
// that constrain it (language / Name / NCName / NMTOKEN / the ID family).
// The looser types (string / normalizedString / token) accept anything.
func derivedStringValid(target AtomType, s string) bool {
	switch target {
	case XSlanguage:
		return reLangLex.MatchString(s)
	case XSname:
		return reNameLex.MatchString(s)
	case XSncname, XSid, XSidref, XSentity:
		return reNCNameLex.MatchString(s)
	case XSnmtoken:
		return reNmtokenLex.MatchString(s)
	}
	return true
}

var (
	reLangLex    = regexp.MustCompile(`^[A-Za-z]{1,8}(-[A-Za-z0-9]{1,8})*$`)
	reNameLex    = regexp.MustCompile("^[" + NameStartClass + "][" + NameCharClass + "]*$")
	reNCNameLex  = regexp.MustCompile("^[" + NCNameStartClass + "][" + NCNameCharClass + "]*$")
	reNmtokenLex = regexp.MustCompile("^[" + NameCharClass + "]+$")
)

// whitespaceFacet applies the XSD whitespace facet implied by a derived string
// type: "replace" (tab/newline/CR → space) for xs:normalizedString, "collapse"
// (replace, then trim and squeeze runs) for xs:token and everything derived from
// it, and "preserve" for xs:string.
func whitespaceFacet(target AtomType, s string) string {
	switch target {
	case XSstring:
		return s
	case XSnormalizedString:
		return strings.Map(func(r rune) rune {
			if r == '\t' || r == '\n' || r == '\r' {
				return ' '
			}
			return r
		}, s)
	default: // token, language, Name, NCName, ID, IDREF, ENTITY, NMTOKEN
		// Collapse acts on the four XML whitespace characters only — a
		// no-break space (U+00A0) is content (CastAs678).
		return strings.Join(strings.FieldsFunc(s, isXMLSpaceRune), " ")
	}
}

// castDateTimeToDateTime converts between date/time/dateTime subtypes (and is
// the entry point for date/time → gregorian, handled in datetime.go).
func castDateTimeToDateTime(a *Atomic, target AtomType) (*Atomic, error) {
	allowed := map[AtomType]map[AtomType]bool{
		XSdateTime:      {XSdate: true, XStime: true, XSdateTime: true, XSdateTimeStamp: true, XSgYear: true, XSgYearMonth: true, XSgMonth: true, XSgMonthDay: true, XSgDay: true},
		XSdateTimeStamp: {XSdate: true, XStime: true, XSdateTime: true, XSdateTimeStamp: true, XSgYear: true, XSgYearMonth: true, XSgMonth: true, XSgMonthDay: true, XSgDay: true},
		XSdate:          {XSdate: true, XSdateTime: true, XSdateTimeStamp: true, XSgYear: true, XSgYearMonth: true, XSgMonth: true, XSgMonthDay: true, XSgDay: true},
		XStime:          {XStime: true},
		// Gregorian types can only cast to themselves (and to string, handled earlier).
		XSgYear:      {XSgYear: true},
		XSgYearMonth: {XSgYearMonth: true},
		XSgMonth:     {XSgMonth: true},
		XSgMonthDay:  {XSgMonthDay: true},
		XSgDay:       {XSgDay: true},
	}
	if m, ok := allowed[a.T]; ok && m[target] {
		tm := a.tm
		// Casting to a GREGORIAN type keeps only that type's components and
		// fills the rest with the SAME reference values the lexical parser
		// uses (parseDateTimeValue), so xs:gYear(dateTime) equals
		// xs:gYear("YYYY") under the ordinary instant comparison
		// (K-SeqExprCast-326..).
		_, off := tm.Zone()
		loc := tm.Location()
		y, mo, d := tm.Date()
		hh, mi, s := tm.Clock()
		ns := tm.Nanosecond()
		switch target {
		case XSdate:
			hh, mi, s, ns = 0, 0, 0, 0
		case XStime:
			y, mo, d = 1972, 12, 31
		case XSgYear:
			mo, d, hh, mi, s, ns = 1, 1, 0, 0, 0, 0
		case XSgYearMonth:
			d, hh, mi, s, ns = 1, 0, 0, 0, 0
		case XSgMonth:
			y, d, hh, mi, s, ns = 1972, 1, 0, 0, 0, 0
		case XSgMonthDay:
			y, hh, mi, s, ns = 1972, 0, 0, 0, 0
		case XSgDay:
			y, mo, hh, mi, s, ns = 1972, 12, 0, 0, 0, 0
		}
		_ = off
		tm = time.Date(y, time.Month(mo), d, hh, mi, s, ns, loc)
		return NewDateTime(target, tm, a.hasTZ), nil
	}
	return nil, fmt.Errorf("err:XPTY0004: cannot cast %s to %s", a.T, target)
}

// castDurationToDuration extracts the relevant components when converting among
// duration subtypes.
func castDurationToDuration(a *Atomic, target AtomType) *Atomic {
	d := a.dur
	switch target {
	case XSyearMonthDuration:
		d.Secs = 0
	case XSdayTimeDuration:
		d.Months = 0
	}
	return NewDuration(target, d)
}

func castToBool(a *Atomic) (*Atomic, error) {
	if a.T == XSboolean {
		return a, nil
	}
	if a.IsNumeric() {
		f := a.Float()
		return NewBool(f != 0 && !math.IsNaN(f)), nil
	}
	switch strings.TrimSpace(a.Lexical()) {
	case "true", "1":
		return NewBool(true), nil
	case "false", "0":
		return NewBool(false), nil
	}
	return nil, fmt.Errorf("err:FORG0001: invalid boolean %q", a.Lexical())
}

func castToInteger(a *Atomic, target AtomType) (*Atomic, error) {
	var z *big.Int
	switch {
	case isIntegerType(a.T):
		z = new(big.Int).Set(a.i)
	case a.T == XSdecimal:
		z = new(big.Int).Quo(a.d.Num(), a.d.Denom())
	case a.T == XSdouble || a.T == XSfloat:
		if math.IsNaN(a.f) || math.IsInf(a.f, 0) {
			return nil, fmt.Errorf("err:FOCA0002: cannot cast %v to integer", a.f)
		}
		bf := big.NewFloat(math.Trunc(a.f))
		z, _ = bf.Int(nil)
	case a.T == XSboolean:
		n := int64(0)
		if a.b {
			n = 1
		}
		z = big.NewInt(n)
	default:
		s := strings.TrimSpace(a.Lexical())
		// A STRING/untypedAtomic source must be a valid xs:integer LEXICAL:
		// no '.' and no exponent (xs:long("3.0") is FORG0001, not 3). Only a
		// numeric decimal/double VALUE source truncates, handled above.
		var ok bool
		z, ok = new(big.Int).SetString(s, 10)
		if !ok {
			return nil, fmt.Errorf("err:FORG0001: invalid integer %q", s)
		}
	}
	if err := checkIntegerBounds(z, target); err != nil {
		return nil, err
	}
	return &Atomic{T: target, i: z}, nil
}

// checkIntegerBounds enforces the value range of a derived integer type.
func checkIntegerBounds(z *big.Int, target AtomType) error {
	type rng struct{ lo, hi *big.Int }
	bi := func(s string) *big.Int { v, _ := new(big.Int).SetString(s, 10); return v }
	var b rng
	switch target {
	case XSbyte:
		b = rng{big.NewInt(-128), big.NewInt(127)}
	case XSshort:
		b = rng{big.NewInt(-32768), big.NewInt(32767)}
	case XSint:
		b = rng{big.NewInt(-2147483648), big.NewInt(2147483647)}
	case XSlong:
		b = rng{bi("-9223372036854775808"), bi("9223372036854775807")}
	case XSunsignedByte:
		b = rng{big.NewInt(0), big.NewInt(255)}
	case XSunsignedShort:
		b = rng{big.NewInt(0), big.NewInt(65535)}
	case XSunsignedInt:
		b = rng{big.NewInt(0), big.NewInt(4294967295)}
	case XSunsignedLong:
		b = rng{big.NewInt(0), bi("18446744073709551615")}
	case XSnonNegativeInteger:
		b = rng{big.NewInt(0), nil}
	case XSpositiveInteger:
		b = rng{big.NewInt(1), nil}
	case XSnonPositiveInteger:
		b = rng{nil, big.NewInt(0)}
	case XSnegativeInteger:
		b = rng{nil, big.NewInt(-1)}
	default:
		return nil
	}
	if b.lo != nil && z.Cmp(b.lo) < 0 {
		return fmt.Errorf("err:FORG0001: value %s below %s minimum", z, target)
	}
	if b.hi != nil && z.Cmp(b.hi) > 0 {
		return fmt.Errorf("err:FORG0001: value %s above %s maximum", z, target)
	}
	return nil
}

func castToDecimal(a *Atomic) (*Atomic, error) {
	switch {
	case a.T == XSdecimal:
		return a, nil
	case isIntegerType(a.T):
		return NewDecimal(new(big.Rat).SetInt(a.i)), nil
	case a.T == XSdouble || a.T == XSfloat:
		if math.IsNaN(a.f) || math.IsInf(a.f, 0) {
			return nil, fmt.Errorf("err:FOCA0002: cannot cast to decimal")
		}
		r := new(big.Rat).SetFloat64(a.f)
		if r == nil {
			return nil, fmt.Errorf("err:FOCA0002: cannot cast to decimal")
		}
		return NewDecimal(r), nil
	case a.T == XSboolean:
		if a.b {
			return NewDecimal(big.NewRat(1, 1)), nil
		}
		return NewDecimal(new(big.Rat)), nil
	default:
		s := strings.TrimSpace(a.Lexical())
		if strings.ContainsAny(s, "eE") {
			return nil, fmt.Errorf("err:FORG0001: decimal lexical may not use exponent %q", s)
		}
		r, ok := new(big.Rat).SetString(s)
		if !ok {
			return nil, fmt.Errorf("err:FORG0001: invalid decimal %q", s)
		}
		return NewDecimal(r), nil
	}
}

// newFloating builds an xs:double, or an xs:float rounded to single precision
// (NewFloat) — the cast path must produce the same value as arithmetic does,
// or xs:float("1.5e38") + 0 would not eq xs:float("1.5e38").
func newFloating(target AtomType, f float64) *Atomic {
	if target == XSfloat {
		return NewFloat(f)
	}
	return &Atomic{T: target, f: f}
}

func castToDouble(a *Atomic, target AtomType) (*Atomic, error) {
	if a.IsNumeric() {
		return newFloating(target, a.Float()), nil
	}
	if a.T == XSboolean {
		if a.b {
			return &Atomic{T: target, f: 1}, nil
		}
		return &Atomic{T: target, f: 0}, nil
	}
	s := strings.TrimSpace(a.Lexical())
	switch s {
	case "NaN":
		return &Atomic{T: target, f: math.NaN()}, nil
	case "INF", "+INF":
		return &Atomic{T: target, f: math.Inf(1)}, nil
	case "-INF":
		return &Atomic{T: target, f: math.Inf(-1)}, nil
	}
	f := parseNumber(s)
	if math.IsNaN(f) && s != "NaN" {
		return nil, fmt.Errorf("err:FORG0001: invalid %s %q", target, s)
	}
	// Only the canonical INF forms are valid — Go's parser also accepts
	// "Inf"/"Infinity", which XSD rejects (K2-SeqExprCast-215..218). "+INF"
	// is valid in XSD 1.1 / XPath 3.1, so it is accepted (K2-SeqExprCast-231
	// wants the 1.0 rejection, but the engine is 3.1 — net loser to strip).
	if math.IsInf(f, 0) && s != "INF" && s != "+INF" && s != "-INF" {
		return nil, fmt.Errorf("err:FORG0001: invalid %s %q", target, s)
	}
	return newFloating(target, f), nil
}

// anyURIWellFormed applies the XPath-level xs:anyURI lexical check (F&O 19.2
// allows a processor to reject a string that is not a valid IRI reference):
// a percent sign must start a %HH escape (K2-SeqExprCast-210/422,
// K2-SeqExprCastable-6/7) and a URI may not begin with ':' — an empty scheme
// (K2-SeqExprCast-423/424/505). Everything else, including spaces and
// non-ASCII characters (IRIs), is accepted.
func anyURIWellFormed(s string) bool {
	if strings.HasPrefix(s, ":") {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		if i+2 >= len(s) || !isHexDigit(s[i+1]) || !isHexDigit(s[i+2]) {
			return false
		}
		i += 2
	}
	return true
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// Castable reports whether the item can be cast to target without error.
func Castable(it Item, target AtomType) bool {
	_, err := CastTo(it, target)
	return err == nil
}

// CallConstructor implements an xs:T(arg) type-constructor function call.
func CallConstructor(local string, arg Object) (Object, bool, error) {
	t, ok := AtomTypeByName(local)
	if !ok {
		return nil, false, nil
	}
	items := Items(arg)
	if len(items) == 0 {
		return Sequence{}, true, nil
	}
	res, err := CastTo(items[0], t)
	if err != nil {
		return nil, true, err
	}
	return res, true, nil
}
