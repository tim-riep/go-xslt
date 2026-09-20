package xpath

import (
	"fmt"
	"math"
	"math/big"
	"time"
)

// unaryArith implements unary minus (negate) and unary plus (!negate),
// preserving the operand's numeric type (integer → integer, decimal → decimal,
// float → float, double → double) rather than collapsing everything to double.
// The operand follows the binary-arithmetic rules: empty → empty, untypedAtomic
// → double, and a typed non-numeric (string, boolean, date, duration …) is
// XPTY0004 (K-NumericUnaryMinus-1 / K-NumericUnaryPlus-1: -"a string").
func unaryArith(ctx *Context, o Object, negate bool) (Object, error) {
	a, err := arithOperand(ctx, o)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return Sequence{}, nil
	}
	if !a.IsNumeric() {
		return nil, fmt.Errorf("err:XPTY0004: %s is not a valid operand for a unary operator", a.T)
	}
	if !negate {
		return a, nil
	}
	switch {
	case isIntegerType(a.T) && a.i != nil:
		return NewIntegerBig(new(big.Int).Neg(a.i)), nil
	case a.T == XSdecimal && a.d != nil:
		return NewDecimal(new(big.Rat).Neg(a.d)), nil
	case a.T == XSfloat:
		return NewFloat(-a.f), nil
	}
	return NewDouble(-a.Float()), nil
}

// evalArith evaluates a binary arithmetic operator with XPath 2.0/3.1 typing:
// numeric type promotion (integer → decimal → double) and date/time/duration
// arithmetic. Empty operand → empty sequence.
func evalArith(ctx *Context, op string, l, r Object) (Object, error) {
	la, err := arithOperand(ctx, l)
	if err != nil {
		return nil, err
	}
	ra, err := arithOperand(ctx, r)
	if err != nil {
		return nil, err
	}
	if la == nil || ra == nil {
		return Sequence{}, nil
	}

	// date/time and duration arithmetic
	if isDateTimeType(la.T) || isDurationType(la.T) || isDateTimeType(ra.T) || isDurationType(ra.T) {
		return dateDurationArith(op, la, ra)
	}
	return numericArith(op, la, ra)
}

// arithOperand atomizes a value to a single atomic (untyped → double), or nil
// for the empty sequence. More than one item is a type error — XPath 2.0+
// dropped the 1.0 first-item rule ((1,2)+1 must raise XPTY0004) — UNLESS
// ctx.BC10 (XSLT's own [xsl:]version<2.0) is in effect, which restores it:
// see bc10ArithOperand.
func arithOperand(ctx *Context, o Object) (*Atomic, error) {
	if ctx != nil && ctx.BC10 {
		return bc10ArithOperand(o)
	}
	items, err := Atomize(o)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	if len(items) > 1 {
		return nil, fmt.Errorf("err:XPTY0004: arithmetic operand is a sequence of %d items", len(items))
	}
	a := items[0].(*Atomic)
	if a.T == XSuntypedAtomic {
		return castToDouble(a, XSdouble)
	}
	// A typed STRING/anyURI/QName/binary operand is a type error — only
	// untypedAtomic coerces ("3" + "3" is XPTY0004, K-NumericAdd-43/44). XSLT
	// values now arrive coerced to their @as type (evalVarDef), so this no
	// longer trips proper stylesheets.
	if isStringType(a.T) || a.T == XSanyURI ||
		a.T == XShexBinary || a.T == XSbase64Binary || a.T == XSqname {
		return nil, fmt.Errorf("err:XPTY0004: %s is not a valid arithmetic operand", a.T)
	}
	return a, nil
}

// bc10ArithOperand implements XPath 1.0's number() conversion for an
// arithmetic operand under XSLT's own backwards-compatible processing: only
// the FIRST item of a multi-item operand participates — no cardinality
// error (backwards-024's "1 + (6 to 10)" takes 6) — and a value that does
// not convert to a number is NaN, never an error (backwards-023's empty
// operand, backwards-025's xs:boolean, backwards-026's non-numeric string
// all participate in "+" without error). The result is unconditionally
// xs:double, matching 1.0's one numeric type (backwards-027).
func bc10ArithOperand(o Object) (*Atomic, error) {
	items, err := Atomize(o)
	if err != nil {
		// Atomization can still fail outright for a map/function item — BC10
		// only relaxes the NUMBER conversion, not that.
		return nil, err
	}
	if len(items) == 0 {
		return NewDouble(math.NaN()), nil
	}
	return NewDouble(ToNumber(items[0])), nil
}

func numericArith(op string, a, b *Atomic) (Object, error) {
	// promote to a common numeric type: double wins over everything, float over
	// the decimal/integer tower (K-NumericAdd-17..21: decimal + float is float).
	switch {
	case a.T == XSdouble || b.T == XSdouble:
		return doubleArith(op, a.Float(), b.Float())
	case a.T == XSfloat || b.T == XSfloat:
		return floatArith(op, a.Float(), b.Float())
	case a.T == XSdecimal || b.T == XSdecimal:
		return decimalArith(op, toRat(a), toRat(b))
	case isIntegerType(a.T) && isIntegerType(b.T):
		return integerArith(op, a.i, b.i)
	default:
		return doubleArith(op, a.Float(), b.Float())
	}
}

func toRat(a *Atomic) *big.Rat {
	if a.T == XSdecimal {
		return a.d
	}
	if isIntegerType(a.T) {
		return new(big.Rat).SetInt(a.i)
	}
	r := new(big.Rat).SetFloat64(a.Float())
	if r == nil {
		return new(big.Rat)
	}
	return r
}

func integerArith(op string, a, b *big.Int) (Object, error) {
	z := new(big.Int)
	switch op {
	case "+":
		return NewIntegerBig(z.Add(a, b)), nil
	case "-":
		return NewIntegerBig(z.Sub(a, b)), nil
	case "*":
		return NewIntegerBig(z.Mul(a, b)), nil
	case "idiv":
		if b.Sign() == 0 {
			return nil, fmt.Errorf("err:FOAR0001: integer division by zero")
		}
		return NewIntegerBig(z.Quo(a, b)), nil
	case "mod":
		if b.Sign() == 0 {
			return nil, fmt.Errorf("err:FOAR0001: integer division by zero")
		}
		return NewIntegerBig(z.Rem(a, b)), nil
	case "div":
		if b.Sign() == 0 {
			return nil, fmt.Errorf("err:FOAR0001: division by zero")
		}
		// integer div integer → decimal
		return NewDecimal(new(big.Rat).SetFrac(a, b)), nil
	}
	return nil, fmt.Errorf("bad operator %q", op)
}

func decimalArith(op string, a, b *big.Rat) (Object, error) {
	z := new(big.Rat)
	switch op {
	case "+":
		return NewDecimal(z.Add(a, b)), nil
	case "-":
		return NewDecimal(z.Sub(a, b)), nil
	case "*":
		return NewDecimal(z.Mul(a, b)), nil
	case "div":
		if b.Sign() == 0 {
			return nil, fmt.Errorf("err:FOAR0001: division by zero")
		}
		return NewDecimal(z.Quo(a, b)), nil
	case "idiv":
		if b.Sign() == 0 {
			return nil, fmt.Errorf("err:FOAR0001: integer division by zero")
		}
		q := new(big.Rat).Quo(a, b)
		return NewIntegerBig(new(big.Int).Quo(q.Num(), q.Denom())), nil
	case "mod":
		// Exact: a - b * trunc(a div b), so 4.5 mod 1.2 is precisely 0.9
		// rather than float64's 0.8999999999999999 (K-NumericMod-19).
		if b.Sign() == 0 {
			return nil, fmt.Errorf("err:FOAR0001: division by zero")
		}
		q := new(big.Rat).Quo(a, b)
		t := new(big.Rat).SetInt(new(big.Int).Quo(q.Num(), q.Denom()))
		return NewDecimal(z.Sub(a, t.Mul(t, b))), nil
	}
	return nil, fmt.Errorf("bad operator %q", op)
}

// floatArith promotes both operands to single precision, evaluates, and types
// the result xs:float — NewFloat rounds it back to binary32 (idiv stays
// integer, and the error cases are shared with doubleArith).
func floatArith(op string, a, b float64) (Object, error) {
	r, err := doubleArith(op, float64(float32(a)), float64(float32(b)))
	if err != nil {
		return nil, err
	}
	if at, ok := r.(*Atomic); ok && at.T == XSdouble {
		return NewFloat(at.f), nil
	}
	return r, nil
}

func doubleArith(op string, a, b float64) (Object, error) {
	switch op {
	case "+":
		return NewDouble(a + b), nil
	case "-":
		return NewDouble(a - b), nil
	case "*":
		return NewDouble(a * b), nil
	case "div":
		return NewDouble(a / b), nil
	case "idiv":
		if math.IsNaN(a) || math.IsNaN(b) || math.IsInf(a, 0) {
			return nil, fmt.Errorf("err:FOAR0002: idiv with NaN or INF operand")
		}
		if b == 0 {
			return nil, fmt.Errorf("err:FOAR0001: integer division by zero")
		}
		q := math.Trunc(a / b)
		if math.IsInf(q, 0) {
			return nil, fmt.Errorf("err:FOAR0002: idiv overflow")
		}
		if math.Abs(q) >= 1<<62 {
			// A quotient beyond int64 is still an xs:integer
			// (cbcl-numeric-idivide-008: 1e38 idiv 1e-37 has 75 digits).
			bi, _ := new(big.Float).SetFloat64(q).Int(nil)
			return NewIntegerBig(bi), nil
		}
		return NewInteger(int64(q)), nil
	case "mod":
		return NewDouble(math.Mod(a, b)), nil
	}
	return nil, fmt.Errorf("bad operator %q", op)
}

// dateDurationArith handles date/time ± duration and date − date etc.
func dateDurationArith(op string, a, b *Atomic) (Object, error) {
	switch {
	case isDateTimeType(a.T) && isDurationType(b.T):
		if (op != "+" && op != "-") || !dateDurationPairOK(a.T, b.T) {
			return nil, fmt.Errorf("err:XPTY0004: cannot %s %s and %s", op, a.T, b.T)
		}
		return addDurationToDate(a, b, op == "-")
	case isDurationType(a.T) && isDateTimeType(b.T) && op == "+":
		if !dateDurationPairOK(b.T, a.T) {
			return nil, fmt.Errorf("err:XPTY0004: cannot %s %s and %s", op, a.T, b.T)
		}
		return addDurationToDate(b, a, false)
	case isDateTimeType(a.T) && isDateTimeType(b.T) && op == "-":
		// difference → dayTimeDuration (seconds); only date−date,
		// dateTime−dateTime and time−time are defined.
		if pa := dtPrim(a.T); pa != dtPrim(b.T) || (pa != XSdate && pa != XSdateTime && pa != XStime) {
			return nil, fmt.Errorf("err:XPTY0004: cannot subtract %s from %s", b.T, a.T)
		}
		// Not time.Sub: a time.Duration saturates at ±292 years, which
		// truncates 0001-01-01 − 2005-07-06 (op-subtract-dates-yielding-DTD-8).
		secs := float64(a.tm.Unix()-b.tm.Unix()) + float64(a.tm.Nanosecond()-b.tm.Nanosecond())/1e9
		return NewDuration(XSdayTimeDuration, Duration{Secs: secs}), nil
	case isDurationType(a.T) && isDurationType(b.T):
		// +/-/div require the SAME duration subtype: yearMonth with yearMonth
		// or dayTime with dayTime (never mixed, never plain xs:duration —
		// K-DayTimeDurationSubtract-4..6, K-YearMonthDurationSubtract-4..6,
		// K-DayTimeDurationDivide-11/13/16).
		ym := a.T == XSyearMonthDuration && b.T == XSyearMonthDuration
		dt := a.T == XSdayTimeDuration && b.T == XSdayTimeDuration
		if !ym && !dt {
			return nil, fmt.Errorf("err:XPTY0004: cannot %s %s and %s", op, a.T, b.T)
		}
		switch op {
		case "+":
			return NewDuration(a.T, Duration{Months: a.dur.Months + b.dur.Months, Secs: normDurSecs(a.dur.Secs + b.dur.Secs)}), nil
		case "-":
			return NewDuration(a.T, Duration{Months: a.dur.Months - b.dur.Months, Secs: normDurSecs(a.dur.Secs - b.dur.Secs)}), nil
		case "div":
			// duration div duration → xs:decimal ratio (same duration subtype),
			// computed EXACTLY: a float64 quotient rounded back to a decimal
			// misses the 16th digit (op-divide-dayTimeDuration-by-dTD-1).
			var num, den *big.Rat
			if ym {
				num, den = new(big.Rat).SetInt64(int64(a.dur.Months)), new(big.Rat).SetInt64(int64(b.dur.Months))
			} else {
				num, den = new(big.Rat).SetFloat64(a.dur.Secs), new(big.Rat).SetFloat64(b.dur.Secs)
			}
			if num == nil || den == nil {
				return nil, fmt.Errorf("err:FOAR0002: duration division overflow")
			}
			if den.Sign() == 0 {
				return nil, fmt.Errorf("err:FOAR0001: division by zero-length duration")
			}
			return NewDecimal(new(big.Rat).Quo(num, den)), nil
		}
	case a.T == XSduration || b.T == XSduration:
		// Plain xs:duration takes part in no arithmetic at all
		// (K-DayTimeDurationDivide-7: xs:duration("P1Y3M") div 3).
		return nil, fmt.Errorf("err:XPTY0004: xs:duration is not an arithmetic operand")
	case isDurationType(a.T) && b.IsNumeric() && (op == "*" || op == "div"):
		f := b.Float()
		if op == "div" {
			if f == 0 {
				return nil, fmt.Errorf("err:FODT0002: division of a duration by zero")
			}
			if math.IsNaN(f) {
				return nil, fmt.Errorf("err:FOCA0005: division of a duration by NaN")
			}
			if math.IsInf(f, 0) {
				// Division by ±INF is the zero duration
				// (K-DayTimeDurationDivide-2/3, K-YearMonthDurationDivide-2/3).
				return NewDuration(a.T, Duration{}), nil
			}
			return scaleDuration(a, f, true)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("err:FODT0002: duration arithmetic overflow")
		}
		return scaleDuration(a, f, false)
	case a.IsNumeric() && isDurationType(b.T) && op == "*":
		// number * duration (multiplication is commutative).
		return scaleDuration(b, a.Float(), false)
	}
	return nil, fmt.Errorf("err:XPTY0004: invalid operand types for %q", op)
}

// scaleDuration multiplies a duration by a factor. A month count is rounded
// with fn:round semantics — half toward +∞: P1M * -3.5 = -P3M, P5M div 2 =
// P3M (op-multiply-yearMonthDuration-1/20, op-divide-yearMonthDuration-17) —
// and seconds are normalized to whole nanoseconds so 3.1 * 3 equals 9.3
// (K-DayTimeDurationMultiply-1/2). A NaN factor is FOCA0005
// (cbcl-multiply-yearMonthDuration-006).
//
// divide selects division by f instead of multiplication: dividing directly
// (rather than by the reciprocal) keeps P5M div 10 exactly 0.5 months. The
// scaled value is forced through an explicit float64 conversion before the
// +0.5 so the compiler cannot fuse it into an FMA — on arm64 the fused
// -0.1*5+0.5 is -2.8e-17, which floors to -1 instead of 0
// (op-divide-yearMonthDuration-17: P5M div -10 is P0M).
func scaleDuration(d *Atomic, f float64, divide bool) (Object, error) {
	if math.IsNaN(f) {
		return nil, fmt.Errorf("err:FOCA0005: duration multiplied by NaN")
	}
	if math.IsInf(f, 0) {
		return nil, fmt.Errorf("err:FODT0002: duration arithmetic overflow")
	}
	var mf, sf float64
	if divide {
		mf, sf = float64(d.dur.Months)/f, d.dur.Secs/f
	} else {
		mf, sf = float64(d.dur.Months)*f, d.dur.Secs*f
	}
	months := math.Floor(float64(mf) + 0.5)
	if math.Abs(months) > 1e15 {
		return nil, fmt.Errorf("err:FODT0002: duration arithmetic overflow")
	}
	if months == 0 {
		months = 0 // never -0
	}
	return NewDuration(d.T, Duration{Months: int(months), Secs: normDurSecs(sf)}), nil
}

// normDurSecs rounds a seconds value to whole nanoseconds, removing the
// binary noise of float arithmetic (3.1 + 56.303 → 59.403 exactly enough to
// compare equal to the parsed literal — K-DayTimeDurationAdd-3).
func normDurSecs(s float64) float64 {
	if math.Abs(s) > 1e15 {
		return s
	}
	return math.Round(s*1e9) / 1e9
}

// dateDurationPairOK reports whether date/time type dt may be combined with
// duration type dur under the F&O operator table: xs:date and xs:dateTime take
// both subtypes, xs:time only xs:dayTimeDuration (K-TimeSubtractDTD-2/3/5),
// the Gregorian types and plain xs:duration take part in no arithmetic.
func dateDurationPairOK(dt, dur AtomType) bool {
	switch dtPrim(dt) {
	case XSdate, XSdateTime:
		return dur == XSyearMonthDuration || dur == XSdayTimeDuration
	case XStime:
		return dur == XSdayTimeDuration
	}
	return false
}

func addDurationToDate(d *Atomic, dur *Atomic, subtract bool) (Object, error) {
	months := dur.dur.Months
	secs := dur.dur.Secs
	if subtract {
		months, secs = -months, -secs
	}
	tm := d.tm
	if months != 0 {
		// XML Schema Appendix E: the day is clamped to the length of the
		// target month rather than overflowing into the next one
		// (2000-02-29 − P1Y = 1999-02-28, op-subtract-yearMonthDuration-
		// from-date-2/3), which Go's AddDate would not do.
		y, m, day := tm.Date()
		total := int(m) - 1 + months
		y += total / 12
		mo := total % 12
		if mo < 0 {
			mo += 12
			y--
		}
		if dim := daysInMonth(y, mo+1); day > dim {
			day = dim
		}
		tm = time.Date(y, time.Month(mo+1), day, tm.Hour(), tm.Minute(), tm.Second(), tm.Nanosecond(), tm.Location())
	}
	tm = tm.Add(time.Duration(secs * float64(time.Second)))
	switch d.T {
	case XStime:
		// xs:time arithmetic wraps modulo 24h: only the clock survives, on
		// the fixed reference date every xs:time carries (so eq/lt work —
		// K-TimeSubtractDTD-1).
		tm = time.Date(1972, 12, 31, tm.Hour(), tm.Minute(), tm.Second(), tm.Nanosecond(), tm.Location())
	case XSdate:
		// xs:date + dayTimeDuration keeps only the date part.
		y, m, day := tm.Date()
		tm = time.Date(y, m, day, 0, 0, 0, 0, tm.Location())
	}
	return NewDateTime(d.T, tm, d.hasTZ), nil
}
