package xpath

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// --- binary helpers ---------------------------------------------------------

func hexEncode(b []byte) string    { return hex.EncodeToString(b) }
func base64Encode(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// stripWS removes all whitespace (XSD hexBinary/base64Binary permit internal
// whitespace, which Go's decoders reject).
func stripWS(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, s)
}

func hexDecode(s string) ([]byte, error) { return hex.DecodeString(stripWS(s)) }
func base64Decode(s string) ([]byte, error) {
	c := stripWS(s)
	b, err := base64.StdEncoding.DecodeString(c)
	if err != nil {
		return nil, err
	}
	// XSD base64Binary is CANONICAL-strict about trailing pad bits: a lexical
	// whose re-encoding differs (nonzero bits under '=' padding,
	// K-SeqExprCast-129) is invalid.
	if base64.StdEncoding.EncodeToString(b) != c {
		return nil, fmt.Errorf("non-canonical base64 padding")
	}
	return b, nil
}

// --- date/time parsing ------------------------------------------------------

var (
	reDateTime = regexp.MustCompile(`^(-?\d{4,})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2}(?:\.\d+)?)(Z|[+-]\d{2}:\d{2})?$`)
	reDate     = regexp.MustCompile(`^(-?\d{4,})-(\d{2})-(\d{2})(Z|[+-]\d{2}:\d{2})?$`)
	reTime     = regexp.MustCompile(`^(\d{2}):(\d{2}):(\d{2}(?:\.\d+)?)(Z|[+-]\d{2}:\d{2})?$`)
	reGYear    = regexp.MustCompile(`^(-?\d{4,})(Z|[+-]\d{2}:\d{2})?$`)
	reGYearMo  = regexp.MustCompile(`^(-?\d{4,})-(\d{2})(Z|[+-]\d{2}:\d{2})?$`)
	reGMonth   = regexp.MustCompile(`^--(\d{2})(Z|[+-]\d{2}:\d{2})?$`)
	reGMonthDy = regexp.MustCompile(`^--(\d{2})-(\d{2})(Z|[+-]\d{2}:\d{2})?$`)
	reGDay     = regexp.MustCompile(`^---(\d{2})(Z|[+-]\d{2}:\d{2})?$`)
)

// parseDateTimeValue parses a lexical date/time value of type t.
func parseDateTimeValue(t AtomType, s string) (time.Time, bool, error) {
	s = strings.TrimSpace(s)
	switch t {
	case XSdateTime, XSdateTimeStamp:
		m := reDateTime.FindStringSubmatch(s)
		if m == nil {
			return time.Time{}, false, fmt.Errorf("invalid xs:dateTime %q", s)
		}
		return buildTime(m[1], m[2], m[3], m[4], m[5], m[6], m[7])
	case XSdate:
		m := reDate.FindStringSubmatch(s)
		if m == nil {
			return time.Time{}, false, fmt.Errorf("invalid xs:date %q", s)
		}
		return buildTime(m[1], m[2], m[3], "00", "00", "00", m[4])
	case XStime:
		m := reTime.FindStringSubmatch(s)
		if m == nil {
			return time.Time{}, false, fmt.Errorf("invalid xs:time %q", s)
		}
		t, hasTZ, err := buildTime("1972", "12", "31", m[1], m[2], m[3], m[4])
		if err != nil {
			return t, hasTZ, err
		}
		if m[1] == "24" {
			// Reaching here with an hour of "24" means buildTime's own
			// hour==24 check already required minute/second to be exactly
			// zero (K-SeqExprCast-351/352, K2-SeqExprCast-448/449/450 —
			// "24:00:00.001"/"24:01:00"/etc. are still rejected above,
			// unchanged) — so this really is the valid lexical "24:00:00",
			// numerically EQUAL to "00:00:00". But, unlike xs:dateTime,
			// xs:time has no date component for that equivalence to roll
			// INTO: Go's time.Date already auto-normalized hour 24 onto the
			// reference date's NEXT day (1973-01-01), which is only correct
			// for dateTime's "24:00:00 = 00:00:00 of the following day" rule.
			// For a standalone time value, 24:00:00 must land on the SAME
			// reference day as 00:00:00, so undo that day here (date-090:
			// xs:time('24:00:00') - xs:time('23:59:59') is -PT23H59M59S, not
			// +PT1S; date-087: a timezone-crossing comparison against
			// 24:00:00 must not silently gain a day it never had).
			t = t.AddDate(0, 0, -1)
		}
		return t, hasTZ, nil
	case XSgYear:
		m := reGYear.FindStringSubmatch(s)
		if m == nil {
			return time.Time{}, false, fmt.Errorf("invalid xs:gYear %q", s)
		}
		return buildTime(m[1], "01", "01", "00", "00", "00", m[2])
	case XSgYearMonth:
		m := reGYearMo.FindStringSubmatch(s)
		if m == nil {
			return time.Time{}, false, fmt.Errorf("invalid xs:gYearMonth %q", s)
		}
		return buildTime(m[1], m[2], "01", "00", "00", "00", m[3])
	case XSgMonth:
		m := reGMonth.FindStringSubmatch(s)
		if m == nil {
			return time.Time{}, false, fmt.Errorf("invalid xs:gMonth %q", s)
		}
		return buildTime("1972", m[1], "01", "00", "00", "00", m[2])
	case XSgMonthDay:
		m := reGMonthDy.FindStringSubmatch(s)
		if m == nil {
			return time.Time{}, false, fmt.Errorf("invalid xs:gMonthDay %q", s)
		}
		return buildTime("1972", m[1], m[2], "00", "00", "00", m[3]) // 1972 is a leap year (--02-29 valid)
	case XSgDay:
		m := reGDay.FindStringSubmatch(s)
		if m == nil {
			return time.Time{}, false, fmt.Errorf("invalid xs:gDay %q", s)
		}
		return buildTime("1972", "12", m[1], "00", "00", "00", m[2])
	}
	return time.Time{}, false, fmt.Errorf("unsupported date/time type %s", t)
}

// parseSecFraction splits a seconds lexical "SS(.fff...)" into whole seconds
// and nanoseconds by DECIMAL string arithmetic (no float rounding).
func parseSecFraction(ss string) (int, int) {
	whole, frac, _ := strings.Cut(ss, ".")
	sec, _ := strconv.Atoi(whole)
	if frac == "" {
		return sec, 0
	}
	if len(frac) > 9 {
		frac = frac[:9]
	}
	for len(frac) < 9 {
		frac += "0"
	}
	ns, _ := strconv.Atoi(frac)
	return sec, ns
}

func buildTime(yr, mo, dy, hh, mm, ss, tz string) (time.Time, bool, error) {
	// A non-canonical year (leading zeros beyond 4 digits) is invalid. Year 0000
	// is permitted (XSD 1.1 / XPath datatypes).
	yabs := strings.TrimPrefix(yr, "-")
	if len(yabs) > 4 && yabs[0] == '0' {
		return time.Time{}, false, fmt.Errorf("invalid year %q", yr)
	}
	// Years beyond the representable range are rejected (an implementation-
	// defined limit the spec allows) instead of silently wrapping in time.Date
	// (cbcl-cast-date-001: "-25252734927766555-06-06" read back as 0000-01-01).
	// The limit is nine digits — under a billion years either side of the
	// epoch, and deliberately below 2^32, which the W3C suite takes as beyond
	// every processor's range (date-094e/f, date-095e/f).
	if len(yabs) > 9 {
		return time.Time{}, false, fmt.Errorf("err:FODT0001: year %q is outside the implementation-supported range", yr)
	}
	year, _ := strconv.Atoi(yr)
	month, _ := strconv.Atoi(mo)
	day, _ := strconv.Atoi(dy)
	hour, _ := strconv.Atoi(hh)
	minute, _ := strconv.Atoi(mm)
	secF, _ := strconv.ParseFloat(ss, 64)
	if month < 1 || month > 12 {
		return time.Time{}, false, fmt.Errorf("month out of range: %d", month)
	}
	if day < 1 || day > daysInMonth(year, month) {
		return time.Time{}, false, fmt.Errorf("day out of range: %d", day)
	}
	if hour > 24 || (hour == 24 && (minute != 0 || secF != 0)) {
		return time.Time{}, false, fmt.Errorf("hour out of range: %d", hour)
	}
	if minute < 0 || minute > 59 || secF < 0 || secF >= 60 {
		return time.Time{}, false, fmt.Errorf("time out of range")
	}
	// Parse seconds & fraction EXACTLY from the lexical, not via float
	// (".110" must be 110000000 ns, not 109999999 — K-SeqExprCast-346/371).
	sec, nsec := parseSecFraction(ss)
	loc := time.UTC
	hasTZ := false
	if tz != "" {
		hasTZ = true
		if tz != "Z" {
			sign := 1
			if tz[0] == '-' {
				sign = -1
			}
			oh, _ := strconv.Atoi(tz[1:3])
			om, _ := strconv.Atoi(tz[4:6])
			if oh > 14 || om > 59 || oh*60+om > 14*60 {
				return time.Time{}, false, fmt.Errorf("timezone out of range")
			}
			loc = time.FixedZone("", sign*(oh*3600+om*60))
		}
	}
	return time.Date(year, time.Month(month), day, hour, minute, sec, nsec, loc), hasTZ, nil
}

func daysInMonth(y, m int) int {
	switch m {
	case 4, 6, 9, 11:
		return 30
	case 2:
		if (y%4 == 0 && y%100 != 0) || y%400 == 0 {
			return 29
		}
		return 28
	}
	return 31
}

// --- date/time formatting ---------------------------------------------------

func formatDateTime(t AtomType, tm time.Time, hasTZ bool) string {
	var s string
	switch t {
	case XSdateTime, XSdateTimeStamp:
		s = tm.Format("2006-01-02T15:04:05.999999999")
	case XSdate:
		s = tm.Format("2006-01-02")
	case XStime:
		s = tm.Format("15:04:05.999999999")
	case XSgYear:
		s = formatYear(tm.Year())
	case XSgYearMonth:
		s = fmt.Sprintf("%s-%02d", formatYear(tm.Year()), int(tm.Month()))
	case XSgMonth:
		s = fmt.Sprintf("--%02d", int(tm.Month()))
	case XSgMonthDay:
		s = fmt.Sprintf("--%02d-%02d", int(tm.Month()), tm.Day())
	case XSgDay:
		s = fmt.Sprintf("---%02d", tm.Day())
	default:
		s = tm.Format("2006-01-02T15:04:05")
	}
	if hasTZ {
		s += formatTZ(tm)
	}
	return s
}

// formatYear renders a gregorian year with at least four digits and the sign
// OUTSIDE the padding ("-0012", not "-012" — CastAs055/137/423/472).
func formatYear(y int) string {
	if y < 0 {
		return fmt.Sprintf("-%04d", -y)
	}
	return fmt.Sprintf("%04d", y)
}

func formatTZ(tm time.Time) string {
	_, off := tm.Zone()
	if off == 0 {
		return "Z"
	}
	sign := "+"
	if off < 0 {
		sign = "-"
		off = -off
	}
	return fmt.Sprintf("%s%02d:%02d", sign, off/3600, (off%3600)/60)
}

// --- duration parsing/formatting -------------------------------------------

var reDuration = regexp.MustCompile(`^(-)?P(?:(\d+)Y)?(?:(\d+)M)?(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+(?:\.\d+)?)S)?)?$`)

func parseDurationValue(t AtomType, s string) (Duration, error) {
	s = strings.TrimSpace(s)
	m := reDuration.FindStringSubmatch(s)
	if m == nil || (m[2] == "" && m[3] == "" && m[4] == "" && m[5] == "" && m[6] == "" && m[7] == "") {
		return Duration{}, fmt.Errorf("invalid duration %q", s)
	}
	// A "T" separator must be followed by at least one time component.
	if strings.Contains(s, "T") && m[5] == "" && m[6] == "" && m[7] == "" {
		return Duration{}, fmt.Errorf("invalid duration %q: T without time component", s)
	}
	neg := m[1] == "-"
	// The months component is an integer: a Y/M field that does not fit, or a
	// total that overflows int64, is an invalid lexical rather than a silent
	// wrap (cbcl-castable-duration-001: P768614336404564651Y).
	field := func(x string) (int64, bool) {
		if x == "" {
			return 0, true
		}
		n, err := strconv.ParseInt(x, 10, 64)
		return n, err == nil
	}
	years, okY := field(m[2])
	mons, okM := field(m[3])
	if !okY || !okM || years > (math.MaxInt64-mons)/12 {
		return Duration{}, fmt.Errorf("invalid duration %q: months out of range", s)
	}
	months := int(years*12 + mons)
	num := func(x string) float64 { f, _ := strconv.ParseFloat(x, 64); return f }
	var secs float64
	secs += num(m[4]) * 86400
	secs += num(m[5]) * 3600
	secs += num(m[6]) * 60
	if m[7] != "" {
		f, _ := strconv.ParseFloat(m[7], 64)
		secs += f
	}
	if neg {
		months, secs = -months, -secs
	}
	return Duration{Months: months, Secs: secs}, nil
}

func formatDuration(t AtomType, d Duration) string {
	switch t {
	case XSyearMonthDuration:
		return formatYearMonth(d.Months)
	case XSdayTimeDuration:
		return formatDayTime(d.Secs)
	}
	// xs:duration: combine.
	neg := d.Months < 0 || d.Secs < 0
	months := d.Months
	secs := d.Secs
	if months < 0 {
		months = -months
	}
	if secs < 0 {
		secs = -secs
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	b.WriteByte('P')
	y, mo := months/12, months%12
	if y > 0 {
		fmt.Fprintf(&b, "%dY", y)
	}
	if mo > 0 {
		fmt.Fprintf(&b, "%dM", mo)
	}
	writeDayTime(&b, secs, months == 0)
	return b.String()
}

func formatYearMonth(months int) string {
	neg := months < 0
	if neg {
		months = -months
	}
	y, mo := months/12, months%12
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	b.WriteByte('P')
	if y > 0 {
		fmt.Fprintf(&b, "%dY", y)
	}
	if mo > 0 || y == 0 {
		fmt.Fprintf(&b, "%dM", mo)
	}
	return b.String()
}

func formatDayTime(secs float64) string {
	neg := secs < 0
	if neg {
		secs = -secs
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	b.WriteByte('P')
	writeDayTime(&b, secs, true)
	return b.String()
}

// writeDayTimeFloat is the float-based fallback for huge durations whose
// nanosecond count overflows int64.
func writeDayTimeFloat(b *strings.Builder, secs float64, forceZero bool) {
	days := int64(secs / 86400)
	rem := secs - float64(days)*86400
	hours := int64(rem / 3600)
	rem -= float64(hours) * 3600
	mins := int64(rem / 60)
	rem -= float64(mins) * 60
	if days > 0 {
		fmt.Fprintf(b, "%dD", days)
	}
	if hours == 0 && mins == 0 && rem == 0 {
		if days == 0 && forceZero {
			b.WriteString("T0S")
		}
		return
	}
	b.WriteByte('T')
	if hours > 0 {
		fmt.Fprintf(b, "%dH", hours)
	}
	if mins > 0 {
		fmt.Fprintf(b, "%dM", mins)
	}
	if rem != 0 {
		if rem == math.Trunc(rem) {
			fmt.Fprintf(b, "%dS", int64(rem))
		} else {
			b.WriteString(strconv.FormatFloat(rem, 'f', -1, 64) + "S")
		}
	}
}

func writeDayTime(b *strings.Builder, secs float64, forceZero bool) {
	// Decompose in integer NANOSECONDS so no binary drift reaches the output
	// (P31DT3H2M10.001S must round-trip; PT1M1231.432S -> PT21M31.432S —
	// K-SeqExprCast-163/186+). Beyond int64-ns range (~9.2e9 s), a value can
	// only have come from a huge lexical anyway — fall back to float
	// rendering (date-073's P1234567890DT...S).
	if math.Abs(secs) >= 9.2e9 {
		writeDayTimeFloat(b, secs, forceZero)
		return
	}
	totalNs := int64(math.Round(secs * 1e9))
	days := totalNs / (86400 * 1e9)
	rem := totalNs % (86400 * 1e9)
	hours := rem / (3600 * 1e9)
	rem %= 3600 * 1e9
	mins := rem / (60 * 1e9)
	rem %= 60 * 1e9
	wholeSec := rem / 1e9
	frac := rem % 1e9
	if days > 0 {
		fmt.Fprintf(b, "%dD", days)
	}
	if hours == 0 && mins == 0 && wholeSec == 0 && frac == 0 {
		if days == 0 && forceZero {
			b.WriteString("T0S")
		}
		return
	}
	b.WriteByte('T')
	if hours > 0 {
		fmt.Fprintf(b, "%dH", hours)
	}
	if mins > 0 {
		fmt.Fprintf(b, "%dM", mins)
	}
	if wholeSec != 0 || frac != 0 {
		if frac == 0 {
			fmt.Fprintf(b, "%dS", wholeSec)
		} else {
			f := fmt.Sprintf("%09d", frac)
			f = strings.TrimRight(f, "0")
			fmt.Fprintf(b, "%d.%sS", wholeSec, f)
		}
	}
}
