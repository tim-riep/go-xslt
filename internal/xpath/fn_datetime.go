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

func init() {
	coreFuncs["default-language"] = func(c *Context, a []Object) (Object, error) {
		lang := "en"
		if c != nil && c.DefaultLanguage != "" {
			lang = c.DefaultLanguage
		}
		return &Atomic{T: XSlanguage, s: lang}, nil
	}
	coreFuncs["current-dateTime"] = dtCurrentDateTime
	coreFuncs["current-date"] = dtCurrentDate
	coreFuncs["current-time"] = dtCurrentTime
	coreFuncs["dateTime"] = dtDateTime
	coreFuncs["implicit-timezone"] = dtImplicitTimezone

	coreFuncs["year-from-dateTime"] = dtYearFromDateTime
	coreFuncs["month-from-dateTime"] = dtMonthFromDateTime
	coreFuncs["day-from-dateTime"] = dtDayFromDateTime
	coreFuncs["hours-from-dateTime"] = dtHoursFromDateTime
	coreFuncs["minutes-from-dateTime"] = dtMinutesFromDateTime
	coreFuncs["seconds-from-dateTime"] = dtSecondsFromDateTime
	coreFuncs["timezone-from-dateTime"] = dtTimezoneFromDateTime

	coreFuncs["year-from-date"] = dtYearFromDate
	coreFuncs["month-from-date"] = dtMonthFromDate
	coreFuncs["day-from-date"] = dtDayFromDate
	coreFuncs["timezone-from-date"] = dtTimezoneFromDate

	coreFuncs["hours-from-time"] = dtHoursFromTime
	coreFuncs["minutes-from-time"] = dtMinutesFromTime
	coreFuncs["seconds-from-time"] = dtSecondsFromTime
	coreFuncs["timezone-from-time"] = dtTimezoneFromTime

	coreFuncs["years-from-duration"] = dtYearsFromDuration
	coreFuncs["months-from-duration"] = dtMonthsFromDuration
	coreFuncs["days-from-duration"] = dtDaysFromDuration
	coreFuncs["hours-from-duration"] = dtHoursFromDuration
	coreFuncs["minutes-from-duration"] = dtMinutesFromDuration
	coreFuncs["seconds-from-duration"] = dtSecondsFromDuration
}

// dtTypeOf returns the AtomType of the value's first item if it is an *Atomic,
// else the supplied fallback type.
func dtTypeOf(o Object, fallback AtomType) AtomType {
	if a, ok := firstItem(o).(*Atomic); ok {
		return a.T
	}
	return fallback
}

// dtIsEmpty reports whether the argument is an empty sequence.
func dtIsEmpty(o Object) bool {
	if o == nil {
		return true
	}
	return len(Items(o)) == 0
}

// dtParseDT obtains a time.Time (and tz flag) from a date/time argument by
// re-parsing its lexical form. atype is the value's stored type if available,
// else def.
func dtParseDT(o Object, def AtomType) (time.Time, bool, AtomType, error) {
	at := dtTypeOf(o, def)
	lex := itemString(firstItem(o))
	tm, hasTZ, err := parseDateTimeValue(at, lex)
	return tm, hasTZ, at, err
}

// dtParseDur obtains a Duration from a duration argument by re-parsing its
// lexical form.
func dtParseDur(o Object) (Duration, error) {
	at := dtTypeOf(o, XSduration)
	lex := itemString(firstItem(o))
	return parseDurationValue(at, lex)
}

// dtSecondsDecimal builds an xs:decimal from a time's seconds + fractional part.
func dtSecondsDecimal(tm time.Time) *Atomic {
	secs := float64(tm.Second()) + float64(tm.Nanosecond())/1e9
	return dtDecimalFromFloat(secs)
}

func dtDecimalFromFloat(f float64) *Atomic {
	a, _ := NewDecimalFromString(strconv.FormatFloat(f, 'f', -1, 64))
	if a == nil {
		return NewDecimal(new(big.Rat))
	}
	return a
}

// dtTZDuration returns the timezone as an xs:dayTimeDuration, or an empty
// sequence when the value carries no timezone.
func dtTZDuration(tm time.Time, hasTZ bool) Object {
	if !hasTZ {
		return Sequence{}
	}
	_, off := tm.Zone()
	return NewDuration(XSdayTimeDuration, Duration{Secs: float64(off)})
}

// --- current accessors ------------------------------------------------------

func dtCurrentDateTime(c *Context, a []Object) (Object, error) {
	return NewDateTime(XSdateTime, ctxNow(c), true), nil
}

func dtCurrentDate(c *Context, a []Object) (Object, error) {
	now := ctxNow(c)
	d := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return NewDateTime(XSdate, d, true), nil
}

func dtCurrentTime(c *Context, a []Object) (Object, error) {
	now := ctxNow(c)
	t := time.Date(1972, 12, 31, now.Hour(), now.Minute(), now.Second(), now.Nanosecond(), now.Location())
	return NewDateTime(XStime, t, true), nil
}

func dtImplicitTimezone(c *Context, a []Object) (Object, error) {
	// Implementation-defined; we pin UTC — consistent with ctxNow, so
	// adjust(current-dateTime(), implicit-timezone()) is the identity
	// (cbcl-adjust-dateTime-001) and offset-hardcoding tests stay stable.
	return NewDuration(XSdayTimeDuration, Duration{}), nil
}

// dateTime($date, $time) -> xs:dateTime
func dtDateTime(c *Context, a []Object) (Object, error) {
	d := arg(a, 0)
	t := arg(a, 1)
	if dtIsEmpty(d) || dtIsEmpty(t) {
		return Sequence{}, nil
	}
	dtm, dTZ, _, err := dtParseDT(d, XSdate)
	if err != nil {
		return nil, fmt.Errorf("err:FORG0001: %v", err)
	}
	ttm, tTZ, _, err := dtParseDT(t, XStime)
	if err != nil {
		return nil, fmt.Errorf("err:FORG0001: %v", err)
	}
	loc := time.UTC
	hasTZ := false
	if dTZ && tTZ {
		// Both operands carry a timezone: they must agree (FORG0008 —
		// forg0008-1, K-DateTimeFunc-6/7, cbcl-dateTime-002).
		_, dOff := dtm.Zone()
		_, tOff := ttm.Zone()
		if dOff != tOff {
			return nil, fmt.Errorf("err:FORG0008: fn:dateTime arguments have different timezones")
		}
	}
	if dTZ {
		loc = dtm.Location()
		hasTZ = true
	} else if tTZ {
		loc = ttm.Location()
		hasTZ = true
	}
	combined := time.Date(dtm.Year(), dtm.Month(), dtm.Day(),
		ttm.Hour(), ttm.Minute(), ttm.Second(), ttm.Nanosecond(), loc)
	return NewDateTime(XSdateTime, combined, hasTZ), nil
}

// --- generic component extraction -------------------------------------------

func dtComponent(def AtomType, f func(tm time.Time, hasTZ bool) Object) coreFunc {
	return func(c *Context, a []Object) (Object, error) {
		o := arg(a, 0)
		if dtIsEmpty(o) {
			return Sequence{}, nil
		}
		tm, hasTZ, _, err := dtParseDT(o, def)
		if err != nil {
			return nil, fmt.Errorf("err:FORG0001: %v", err)
		}
		return f(tm, hasTZ), nil
	}
}

// dateTime components
func dtYearFromDateTime(c *Context, a []Object) (Object, error) {
	return dtComponent(XSdateTime, func(tm time.Time, _ bool) Object { return NewInteger(int64(tm.Year())) })(c, a)
}
func dtMonthFromDateTime(c *Context, a []Object) (Object, error) {
	return dtComponent(XSdateTime, func(tm time.Time, _ bool) Object { return NewInteger(int64(tm.Month())) })(c, a)
}
func dtDayFromDateTime(c *Context, a []Object) (Object, error) {
	return dtComponent(XSdateTime, func(tm time.Time, _ bool) Object { return NewInteger(int64(tm.Day())) })(c, a)
}
func dtHoursFromDateTime(c *Context, a []Object) (Object, error) {
	return dtComponent(XSdateTime, func(tm time.Time, _ bool) Object { return NewInteger(int64(tm.Hour())) })(c, a)
}
func dtMinutesFromDateTime(c *Context, a []Object) (Object, error) {
	return dtComponent(XSdateTime, func(tm time.Time, _ bool) Object { return NewInteger(int64(tm.Minute())) })(c, a)
}
func dtSecondsFromDateTime(c *Context, a []Object) (Object, error) {
	return dtComponent(XSdateTime, func(tm time.Time, _ bool) Object { return dtSecondsDecimal(tm) })(c, a)
}
func dtTimezoneFromDateTime(c *Context, a []Object) (Object, error) {
	return dtComponent(XSdateTime, dtTZDuration)(c, a)
}

// date components
func dtYearFromDate(c *Context, a []Object) (Object, error) {
	return dtComponent(XSdate, func(tm time.Time, _ bool) Object { return NewInteger(int64(tm.Year())) })(c, a)
}
func dtMonthFromDate(c *Context, a []Object) (Object, error) {
	return dtComponent(XSdate, func(tm time.Time, _ bool) Object { return NewInteger(int64(tm.Month())) })(c, a)
}
func dtDayFromDate(c *Context, a []Object) (Object, error) {
	return dtComponent(XSdate, func(tm time.Time, _ bool) Object { return NewInteger(int64(tm.Day())) })(c, a)
}
func dtTimezoneFromDate(c *Context, a []Object) (Object, error) {
	return dtComponent(XSdate, dtTZDuration)(c, a)
}

// time components
func dtHoursFromTime(c *Context, a []Object) (Object, error) {
	return dtComponent(XStime, func(tm time.Time, _ bool) Object { return NewInteger(int64(tm.Hour())) })(c, a)
}
func dtMinutesFromTime(c *Context, a []Object) (Object, error) {
	return dtComponent(XStime, func(tm time.Time, _ bool) Object { return NewInteger(int64(tm.Minute())) })(c, a)
}
func dtSecondsFromTime(c *Context, a []Object) (Object, error) {
	return dtComponent(XStime, func(tm time.Time, _ bool) Object { return dtSecondsDecimal(tm) })(c, a)
}
func dtTimezoneFromTime(c *Context, a []Object) (Object, error) {
	return dtComponent(XStime, dtTZDuration)(c, a)
}

// --- duration components ----------------------------------------------------

func dtDurationComponent(f func(d Duration) Object) coreFunc {
	return func(c *Context, a []Object) (Object, error) {
		o := arg(a, 0)
		if dtIsEmpty(o) {
			return Sequence{}, nil
		}
		d, err := dtParseDur(o)
		if err != nil {
			return nil, fmt.Errorf("err:FORG0001: %v", err)
		}
		return f(d), nil
	}
}

// dtSign returns -1 if either component of the duration is negative, else 1.
func dtSign(d Duration) int {
	if d.Months < 0 || d.Secs < 0 {
		return -1
	}
	return 1
}

func dtYearsFromDuration(c *Context, a []Object) (Object, error) {
	return dtDurationComponent(func(d Duration) Object {
		m := d.Months
		if m < 0 {
			m = -m
		}
		return NewInteger(int64(dtSign(d) * (m / 12)))
	})(c, a)
}

func dtMonthsFromDuration(c *Context, a []Object) (Object, error) {
	return dtDurationComponent(func(d Duration) Object {
		m := d.Months
		if m < 0 {
			m = -m
		}
		return NewInteger(int64(dtSign(d) * (m % 12)))
	})(c, a)
}

func dtDaysFromDuration(c *Context, a []Object) (Object, error) {
	return dtDurationComponent(func(d Duration) Object {
		secs := d.Secs
		if secs < 0 {
			secs = -secs
		}
		return NewInteger(int64(dtSign(d)) * int64(secs/86400))
	})(c, a)
}

func dtHoursFromDuration(c *Context, a []Object) (Object, error) {
	return dtDurationComponent(func(d Duration) Object {
		secs := d.Secs
		if secs < 0 {
			secs = -secs
		}
		hours := int64(secs/3600) % 24
		return NewInteger(int64(dtSign(d)) * hours)
	})(c, a)
}

func dtMinutesFromDuration(c *Context, a []Object) (Object, error) {
	return dtDurationComponent(func(d Duration) Object {
		secs := d.Secs
		if secs < 0 {
			secs = -secs
		}
		mins := int64(secs/60) % 60
		return NewInteger(int64(dtSign(d)) * mins)
	})(c, a)
}

func dtSecondsFromDuration(c *Context, a []Object) (Object, error) {
	return dtDurationComponent(func(d Duration) Object {
		secs := d.Secs
		if secs < 0 {
			secs = -secs
		}
		if secs >= 9.2e9 { // beyond int64 nanoseconds: float fallback
			rem := secs - float64(int64(secs/60)*60)
			return dtDecimalFromFloat(float64(dtSign(d)) * rem)
		}
		// Decompose in integer nanoseconds so the fraction stays exact
		// (P3DT8H2M1.03S -> 1.03, not 1.0300000000279397 —
		// K-SecondsFromDurationFunc-5/6/7).
		ns := int64(math.Round(secs*1e9)) % (60 * 1e9)
		lex := strconv.FormatInt(ns/1e9, 10)
		if frac := ns % 1e9; frac != 0 {
			lex += "." + strings.TrimRight(fmt.Sprintf("%09d", frac), "0")
		}
		if dtSign(d) < 0 && ns != 0 {
			lex = "-" + lex
		}
		dec, _ := NewDecimalFromString(lex)
		return dec
	})(c, a)
}

// nowBox lazily pins the evaluation's current instant: XPath's fn:current-*
// are deterministic within one evaluation — current-dateTime() must equal
// dateTime(current-date(), current-time()) (fn-for-each-pair-031).
type nowBox struct {
	t time.Time
	// nsScope caches the synthesized in-scope namespace nodes per element for
	// the duration of one evaluation (see namespaceNodes / ctxNSScope).
	nsScope map[*xmltree.Node][]*xmltree.Node
}

func ctxNow(c *Context) time.Time {
	if c == nil {
		return time.Now().UTC()
	}
	if c.nowCache == nil {
		t := c.Now
		if t.IsZero() {
			t = time.Now().UTC()
		}
		c.nowCache = &nowBox{t: t}
	}
	return c.nowCache.t
}
