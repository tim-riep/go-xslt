package xpath

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func init() {
	coreFuncs["adjust-dateTime-to-timezone"] = dtAdjustToTimezone(XSdateTime)
	coreFuncs["adjust-date-to-timezone"] = dtAdjustToTimezone(XSdate)
	coreFuncs["adjust-time-to-timezone"] = dtAdjustToTimezone(XStime)
	coreFuncs["parse-ietf-date"] = fnParseIetfDate
}

// dtAdjustToTimezone implements fn:adjust-{dateTime,date,time}-to-timezone.
//
//	1-arg: adjust to the implicit timezone (we use UTC).
//	2-arg with $tz = (): remove the timezone.
//	2-arg with a dayTimeDuration: adjust to that offset.
func dtAdjustToTimezone(def AtomType) coreFunc {
	return func(c *Context, a []Object) (Object, error) {
		o := arg(a, 0)
		if dtIsEmpty(o) {
			return Sequence{}, nil
		}
		tm, hasTZ, typ, err := dtParseDT(o, def)
		if err != nil {
			return nil, fmt.Errorf("err:FORG0001: %v", err)
		}
		hasTarget := true
		off := 0
		if len(a) >= 2 {
			if dtIsEmpty(arg(a, 1)) {
				hasTarget = false
			} else {
				dur, err := dtParseDur(arg(a, 1))
				if err != nil {
					return nil, err
				}
				// Range and whole-minute checks on the exact value: a fractional
				// second (PT14H0M0.001S, K-AdjDateToTimezoneFunc-8) must not be
				// truncated away before the test.
				if dur.Secs > 14*3600 || dur.Secs < -14*3600 || math.Mod(dur.Secs, 60) != 0 {
					return nil, fmt.Errorf("err:FODT0003: timezone offset out of range")
				}
				off = int(dur.Secs)
			}
		}
		ntm, nTZ := adjustTZ(tm, hasTZ, hasTarget, off)
		if typ == XStime {
			// A time carries no date: the instant shift may have rolled the
			// internal reference day, which would make 03:00:00+10:00 unequal
			// to xs:time("03:00:00+10:00") (K-AdjTimeToTimezoneFunc-16,
			// fo-test-fn-adjust-time-to-timezone-007). Rebuild on the
			// reference date every xs:time is parsed onto.
			h, mi, s := ntm.Clock()
			ntm = time.Date(1972, 12, 31, h, mi, s, ntm.Nanosecond(), ntm.Location())
		}
		if typ == XSdate {
			// A date carries no time of day: drop the clock the instant shift
			// left behind so the result compares equal to the xs:date with the
			// same day and zone (K-AdjDateToTimezoneFunc-10/12).
			y, mo, d := ntm.Date()
			ntm = time.Date(y, mo, d, 0, 0, 0, 0, ntm.Location())
		}
		return NewDateTime(typ, ntm, nTZ), nil
	}
}

func adjustTZ(tm time.Time, hasTZ, hasTarget bool, off int) (time.Time, bool) {
	if !hasTarget { // remove the timezone, keeping the local components
		if !hasTZ {
			return tm, false
		}
		y, mo, d := tm.Date()
		h, mi, s := tm.Clock()
		return time.Date(y, mo, d, h, mi, s, tm.Nanosecond(), time.UTC), false
	}
	loc := time.FixedZone("", off)
	if !hasTZ { // attach: reinterpret the local components in the new zone
		y, mo, d := tm.Date()
		h, mi, s := tm.Clock()
		return time.Date(y, mo, d, h, mi, s, tm.Nanosecond(), loc), true
	}
	return tm.In(loc), true // same instant, displayed in the new zone
}

// --- parse-ietf-date --------------------------------------------------------

var ietfMonths = map[string]time.Month{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}
var ietfWeekdays = map[string]bool{
	"mon": true, "tue": true, "wed": true, "thu": true, "fri": true, "sat": true, "sun": true,
	"monday": true, "tuesday": true, "wednesday": true, "thursday": true,
	"friday": true, "saturday": true, "sunday": true,
}
var ietfZones = map[string]int{
	"gmt": 0, "ut": 0, "utc": 0, "z": 0,
	"est": -5 * 3600, "edt": -4 * 3600, "cst": -6 * 3600, "cdt": -5 * 3600,
	"mst": -7 * 3600, "mdt": -6 * 3600, "pst": -8 * 3600, "pdt": -7 * 3600,
}

func fnParseIetfDate(c *Context, a []Object) (Object, error) {
	o := arg(a, 0)
	if dtIsEmpty(o) {
		return Sequence{}, nil
	}
	tm, err := parseIETF(itemString(firstItem(o)))
	if err != nil {
		return nil, fmt.Errorf("err:FORG0010: %v", err)
	}
	return NewDateTime(XSdateTime, tm, true), nil
}

// parseIETF parses an RFC 5322 / IETF date such as
// "Wed, 20 Aug 2014 19:36:01 GMT" or "20 Aug 2014 19:36 -0500".
func parseIETF(s string) (time.Time, error) {
	// A parenthesised zone name is only permitted right after a numeric
	// offset — timezone ::= tzname | tzoffset (S? "(" S? tzname S? ")")? — so
	// "()" / "(CET)" (not a tzname) and "19:36(EST)" (no offset) are errors
	// (parse-ietf-date-errs30/31/33).
	for {
		i := strings.IndexByte(s, '(')
		if i < 0 {
			break
		}
		j := strings.IndexByte(s[i:], ')')
		if j < 0 {
			return time.Time{}, fmt.Errorf("unterminated comment in %q", s)
		}
		j += i
		name := strings.ToLower(strings.TrimSpace(s[i+1 : j]))
		if _, ok := ietfZones[name]; !ok || name == "z" {
			return time.Time{}, fmt.Errorf("invalid timezone comment %q", s[i:j+1])
		}
		if !reIETFOffsetTail.MatchString(strings.TrimRight(s[:i], " \t")) {
			return time.Time{}, fmt.Errorf("timezone comment %q must follow a numeric offset", s[i:j+1])
		}
		s = s[:i] + " " + s[j+1:]
	}
	// A comma is only permitted directly after a leading day name, and must
	// be followed by whitespace: input ::= S? (dayname ","? S)? …
	// ("Wed,20 Aug" / "Aug,20" are errors: parse-ietf-date-errs13/15).
	if c := strings.IndexByte(s, ','); c >= 0 {
		head := strings.ToLower(strings.TrimRight(strings.TrimLeft(s[:c], " \t"), "."))
		if !ietfWeekdays[head] || c+1 >= len(s) || (s[c+1] != ' ' && s[c+1] != '\t') ||
			strings.IndexByte(s[c+1:], ',') >= 0 {
			return time.Time{}, fmt.Errorf("misplaced comma in %q", s)
		}
		s = s[:c] + " " + s[c+1:]
	}
	// A hyphen is a permitted date-part separator (Aug-20, 20 - Aug - 2014).
	// Only the date part — everything before the time's first ':' — is
	// affected; a hyphen in a timezone offset comes after the time and is
	// preserved (parse-ietf-date-19/21/22/23).
	if c := strings.IndexByte(s, ':'); c >= 0 {
		s = strings.ReplaceAll(s[:c], "-", " ") + s[c:]
	} else {
		s = strings.ReplaceAll(s, "-", " ")
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return time.Time{}, fmt.Errorf("empty IETF date")
	}
	// An optional leading day-of-week name; any other non-numeric leading token
	// that is not a month name is invalid.
	if !isAllDigits(fields[0]) {
		w := strings.ToLower(strings.TrimRight(fields[0], "."))
		if ietfWeekdays[w] {
			fields = fields[1:]
		} else if _, isMonth := ietfMonths[w]; !isMonth {
			return time.Time{}, fmt.Errorf("invalid leading token %q", fields[0])
		}
	}
	// Locate the month-name field.
	monIdx := -1
	for i, f := range fields {
		if _, ok := ietfMonths[strings.ToLower(f)]; ok {
			monIdx = i
			break
		}
	}
	if monIdx < 0 {
		return time.Time{}, fmt.Errorf("no month in %q", s)
	}
	month := ietfMonths[strings.ToLower(fields[monIdx])]

	var dayStr, yearStr, timeStr, tzStr string
	var err error
	if monIdx >= 1 && isAllDigits(fields[monIdx-1]) {
		// Standard: DD Mon YYYY ...
		dayStr = fields[monIdx-1]
		yearStr, timeStr, tzStr, err = pickYearTimeTZ(fields[monIdx+1:])
	} else {
		// asctime: Mon DD HH:MM:SS YYYY — no timezone in this layout, so a
		// stray offset is junk (parse-ietf-date-errs30/31).
		rest := fields[monIdx+1:]
		if len(rest) >= 1 {
			dayStr = rest[0]
			rest = rest[1:]
		}
		yearStr, timeStr, tzStr, err = pickYearTimeTZ(rest)
	}
	if err != nil {
		return time.Time{}, err
	}

	// daynum is 1-2 digits in 1..31.
	if len(dayStr) < 1 || len(dayStr) > 2 || !isAllDigits(dayStr) {
		return time.Time{}, fmt.Errorf("invalid day %q", dayStr)
	}
	day, _ := strconv.Atoi(dayStr)
	if day < 1 || day > 31 {
		return time.Time{}, fmt.Errorf("day out of range: %d", day)
	}
	// year is 2 or 4 digits (never 1 or 3).
	if len(yearStr) != 2 && len(yearStr) != 4 {
		return time.Time{}, fmt.Errorf("invalid year %q", yearStr)
	}
	year, _ := strconv.Atoi(yearStr)
	if len(yearStr) == 2 {
		// A two-digit year is 19xx (F&O 3.1 §9.8.4.4; parse-ietf-date-41/58).
		year += 1900
	}
	hh, mm, ss := 0, 0, 0
	nanos := 0
	endOfDay := false // 24:00(:00) is midnight at the END of the day
	if timeStr != "" {
		// A zone may be glued to the time with no space: 19:36:01GMT,
		// 14:36:01-05:00, 14:36:01-05 (parse-ietf-date-12/13/45/46). Split
		// the leading HH:MM(:SS(.frac)?)? off, the remainder is the zone.
		m := reIETFTime.FindStringSubmatch(timeStr)
		if m == nil {
			return time.Time{}, fmt.Errorf("invalid time %q", timeStr)
		}
		hh, _ = strconv.Atoi(m[1])
		mm, _ = strconv.Atoi(m[2])
		if m[3] != "" {
			ss, _ = strconv.Atoi(m[3])
		}
		if m[4] != "" { // fractional seconds -> nanoseconds
			frac := m[4]
			for len(frac) < 9 {
				frac += "0"
			}
			nanos, _ = strconv.Atoi(frac[:9])
		}
		if glued := m[5]; glued != "" {
			if tzStr != "" {
				return time.Time{}, fmt.Errorf("duplicate timezone")
			}
			tzStr = glued
		}
		if hh == 24 && mm == 0 && ss == 0 && nanos == 0 {
			// As for xs:dateTime, 24:00:00 denotes the end of the day
			// (parse-ietf-date-63/64).
			hh, endOfDay = 0, true
		}
		if hh > 23 || mm > 59 || ss > 60 {
			return time.Time{}, fmt.Errorf("time out of range in %q", timeStr)
		}
	}
	off, ok := parseIETFZone(tzStr)
	if !ok {
		return time.Time{}, fmt.Errorf("unrecognized timezone %q", tzStr)
	}
	loc := time.FixedZone("", off)
	tm := time.Date(year, month, day, hh, mm, ss, nanos, loc)
	// Reject days that don't exist in the month (e.g. 29 Feb in a non-leap year):
	// time.Date normalises overflow, so the components must round-trip.
	if tm.Day() != day || tm.Month() != month {
		return time.Time{}, fmt.Errorf("invalid date: %d %s %d", day, month, year)
	}
	if endOfDay {
		tm = tm.AddDate(0, 0, 1)
	}
	return tm, nil
}

// pickYearTimeTZ classifies the post-month fields into year / time / timezone,
// rejecting an unrecognized or duplicated field as junk.
func pickYearTimeTZ(fields []string) (year, tm, tz string, err error) {
	for _, f := range fields {
		switch {
		case len(f) > 0 && f[0] >= '0' && f[0] <= '9' && strings.Contains(f, ":"):
			if tm != "" {
				return "", "", "", fmt.Errorf("duplicate time field %q", f)
			}
			tm = f
		case isAllDigits(f):
			if year != "" {
				return "", "", "", fmt.Errorf("duplicate year field %q", f)
			}
			year = f
		default:
			if tz != "" {
				return "", "", "", fmt.Errorf("unexpected token %q", f)
			}
			tz = f
		}
	}
	return year, tm, tz, nil
}

// parseIETFZone resolves an IETF timezone token to an offset in seconds. It
// reports ok=false for an unrecognized zone (so the caller can reject it).
func parseIETFZone(tz string) (int, bool) {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return 0, true // no zone → UTC
	}
	if off, ok := ietfZones[strings.ToLower(tz)]; ok {
		return off, true
	}
	// tzoffset ::= ("+"|"-") hours ":"? minutes? with hours = 1-2 digits, so
	// -5, -500, -5:00, -05, -05:, -0500 and -05:00 are all valid
	// (parse-ietf-date-61/62/errs28) while -05:0 is not.
	if m := reIETFOffset.FindStringSubmatch(tz); m != nil {
		sign := 1
		if m[1] == "-" {
			sign = -1
		}
		h, _ := strconv.Atoi(m[2])
		min := 0
		if m[3] != "" {
			min, _ = strconv.Atoi(m[3])
		}
		// FODT0003: minutes 0-59 and total offset within ±14:00
		// (parse-ietf-date-errs38 -15:00, errs39 -05:60).
		if min > 59 || h*3600+min*60 > 14*3600 {
			return 0, false
		}
		return sign * (h*3600 + min*60), true
	}
	return 0, false
}

// reIETFOffset matches a complete tzoffset; reIETFOffsetTail matches text that
// ends with one (what a parenthesised zone name must follow).
var (
	reIETFOffset     = regexp.MustCompile(`^([+-])(\d{1,2}):?(\d{2})?$`)
	reIETFOffsetTail = regexp.MustCompile(`[+-]\d{1,2}:?(\d{2})?$`)
)

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// reIETFTime matches HH:MM(:SS(.frac)?)? with an optional glued zone tail
// (GMT / UT / a named zone / a numeric offset with no separating space).
var reIETFTime = regexp.MustCompile(`^(\d{1,2}):(\d{2})(?::(\d{2}))?(?:\.(\d+))?(.*)$`)
