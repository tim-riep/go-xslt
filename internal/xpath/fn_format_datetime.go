package xpath

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func init() {
	coreFuncs["format-date"] = fnFormatDate
	coreFuncs["format-time"] = fnFormatTime
	coreFuncs["format-dateTime"] = fnFormatDateTime
}

func fnFormatDate(c *Context, a []Object) (Object, error)     { return formatDTArgs(a) }
func fnFormatTime(c *Context, a []Object) (Object, error)     { return formatDTArgs(a) }
func fnFormatDateTime(c *Context, a []Object) (Object, error) { return formatDTArgs(a) }

func formatDTArgs(a []Object) (Object, error) {
	v := arg(a, 0)
	if numIsEmpty(v) {
		return Sequence{}, nil
	}
	at := numFirstAtomic(v)
	if at == nil || !isDateTimeType(at.T) {
		// Atomize a node value if needed.
		items, _ := Atomize(v)
		if len(items) == 0 {
			return Sequence{}, nil
		}
		var ok bool
		at, ok = items[0].(*Atomic)
		if !ok || !isDateTimeType(at.T) {
			return nil, fmt.Errorf("err:XPTY0004: format-date/time expects a date/time value")
		}
	}
	// $language, $calendar and $place are xs:string? — a non-string atomic
	// there is a type error (format-date-inpt-er3 passes the integer 5).
	for i := 2; i < len(a) && i <= 4; i++ {
		if x := numFirstAtomic(a[i]); x != nil && !isStringType(x.T) && x.T != XSuntypedAtomic && x.T != XSanyURI {
			return nil, fmt.Errorf("err:XPTY0004: format-date/time argument %d must be xs:string?, got %s", i+1, x.T)
		}
	}
	var opts dtOptions
	if len(a) > 2 && !numIsEmpty(a[2]) {
		// Only English is supported: another language falls back to it and
		// says so with a "[Language: en]" prefix when the output actually
		// needed language-dependent data (format-date-en151).
		lang := strings.ToLower(ToString(a[2]))
		if lang == "de" || strings.HasPrefix(lang, "de-") {
			// German month/weekday names are a genuinely bounded, learnable
			// data table (unlike UCA tailoring or a full timezone-abbreviation
			// database) — format-date-de101..116. Everything else
			// language-dependent (ordinal-word spellout, era/calendar names)
			// stays English-only and still falls back with the usual
			// "[Language: en]" prefix if actually used.
			opts.lang = "de"
		} else {
			opts.langFallback = lang != "" && lang != "en" && !strings.HasPrefix(lang, "en-")
		}
	}
	if len(a) > 3 && !numIsEmpty(a[3]) {
		p, err := calendarPrefix(ToString(a[3]))
		if err != nil {
			return nil, err
		}
		opts.calPrefix = p
	}
	return formatDateTimePicture(at, ToString(arg(a, 1)), &opts)
}

// dtOptions carries the $language/$calendar decisions across a picture.
type dtOptions struct {
	langFallback bool   // requested language unsupported → "[Language: en]"
	calPrefix    string // "[Calendar: AD]" for a known but unsupported calendar
	usedLang     bool   // a name, word, ordinal or era was emitted
	lang         string // "" (English, the default) or "de" (German month/weekday names)
}

// knownCalendars are the calendar designators of F&O §9.8.4.5; AD and ISO
// are the ones implemented, the rest fall back to AD with a prefix.
var knownCalendars = map[string]bool{
	"AD": true, "AH": true, "AME": true, "AM": true, "AP": true, "AS": true,
	"BE": true, "CB": true, "CE": true, "CL": true, "CS": true, "EE": true,
	"FE": true, "ISO": true, "JE": true, "KE": true, "KY": true, "ME": true,
	"MS": true, "NS": true, "OS": true, "RS": true, "SE": true, "SH": true,
	"SS": true, "TE": true, "VE": true, "VS": true,
}

// calendarPrefix validates the $calendar argument (a lexical QName or
// Q{uri}local) and returns the "[Calendar: AD]" prefix for a calendar we do
// not implement. A no-namespace name outside the spec's list, or a name that
// is not a QName, is FOFD1340 (format-date-en152/155/156/157/158).
func calendarPrefix(cal string) (string, error) {
	namespaced := false
	local := cal
	switch {
	case strings.HasPrefix(cal, "Q{"):
		i := strings.IndexByte(cal, '}')
		if i < 0 {
			return "", fmt.Errorf("err:FOFD1340: invalid calendar name %q", cal)
		}
		namespaced = i > 2
		local = cal[i+1:]
	case strings.Contains(cal, ":"):
		i := strings.IndexByte(cal, ':')
		if !derivedStringValid(XSncname, cal[:i]) {
			return "", fmt.Errorf("err:FOFD1340: invalid calendar name %q", cal)
		}
		namespaced = true // an unresolvable prefix: implementation-defined calendar
		local = cal[i+1:]
	}
	if !derivedStringValid(XSncname, local) {
		return "", fmt.Errorf("err:FOFD1340: invalid calendar name %q", cal)
	}
	switch {
	case namespaced:
		return "[Calendar: AD]", nil
	case local == "AD" || local == "ISO":
		return "", nil
	case knownCalendars[local]:
		return "[Calendar: AD]", nil
	}
	return "", fmt.Errorf("err:FOFD1340: unknown calendar %q", cal)
}

var monthNames = []string{"January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December"}
var weekdayNames = []string{"Monday", "Tuesday", "Wednesday", "Thursday",
	"Friday", "Saturday", "Sunday"} // ISO order: 1=Monday

// German month/weekday name tables (format-date-de101..116). Truncation to a
// given width — e.g. [MN,3-3]'s "MÄR" for März, [FN,2-2]'s "MO" for Montag —
// is handled generically by presentNumberOrName's existing rune-based cut,
// same as English; no special abbreviation forms are needed here.
var monthNamesDE = []string{"Januar", "Februar", "März", "April", "Mai", "Juni",
	"Juli", "August", "September", "Oktober", "November", "Dezember"}
var weekdayNamesDE = []string{"Montag", "Dienstag", "Mittwoch", "Donnerstag",
	"Freitag", "Samstag", "Sonntag"} // ISO order: 1=Montag

// monthWeekdayNames returns the month/weekday name tables for opts.lang
// ("de" or, by default, English).
func monthWeekdayNames(opts *dtOptions) ([]string, []string) {
	if opts != nil && opts.lang == "de" {
		return monthNamesDE, weekdayNamesDE
	}
	return monthNames, weekdayNames
}

// formatDateTimePicture renders a date/time per the XPath [component] picture.
func formatDateTimePicture(at *Atomic, picture string, opts *dtOptions) (Object, error) {
	tm := at.tm
	var b strings.Builder
	b.WriteString(opts.calPrefix)
	rs := []rune(picture)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch r {
		case '[':
			if i+1 < len(rs) && rs[i+1] == '[' { // escaped "[["
				b.WriteByte('[')
				i++
				continue
			}
			// read marker up to ']'
			j := i + 1
			for j < len(rs) && rs[j] != ']' {
				j++
			}
			if j >= len(rs) {
				return nil, fmt.Errorf("err:FOFD1340: unclosed '[' in date/time picture %q", picture)
			}
			// Whitespace inside a variable marker is ignored (§9.8.4.1:
			// "[ f 0 0 0 ]", format-date-038's newline-split pattern).
			marker := strings.Map(func(r rune) rune {
				if unicode.IsSpace(r) {
					return -1
				}
				return r
			}, string(rs[i+1:j]))
			out, err := formatComponent(at, tm, marker, opts)
			if err != nil {
				return nil, err
			}
			b.WriteString(out)
			i = j
		case ']':
			if i+1 < len(rs) && rs[i+1] == ']' { // escaped "]]"
				b.WriteByte(']')
				i++
				continue
			}
			b.WriteByte(']')
		default:
			b.WriteRune(r)
		}
	}
	if opts.langFallback && opts.usedLang {
		return "[Language: en]" + b.String(), nil
	}
	return b.String(), nil
}

// dtComponentLetters are the component specifiers of F&O §9.8.4.1.
const dtComponentLetters = "YMDdFWwHhPmsfZzCE"

func formatComponent(at *Atomic, tm time.Time, marker string, opts *dtOptions) (string, error) {
	if marker == "" {
		return "", fmt.Errorf("err:FOFD1340: empty variable marker in date/time picture")
	}
	mr := []rune(marker)
	comp := mr[0]
	if comp > 0x7F || !strings.ContainsRune(dtComponentLetters, comp) {
		return "", fmt.Errorf("err:FOFD1340: unknown component specifier [%c] in date/time picture", comp)
	}
	// A date carries no time-of-day components and a time no calendar
	// components: FOFD1350 (format-date-802err…807err, format-time-809err…816err).
	switch at.T {
	case XSdate:
		if strings.ContainsRune("HhPmsf", comp) {
			return "", fmt.Errorf("err:FOFD1350: component [%c] is not available in an xs:date", comp)
		}
	case XStime:
		if strings.ContainsRune("YMDdFWwE", comp) {
			return "", fmt.Errorf("err:FOFD1350: component [%c] is not available in an xs:time", comp)
		}
	}
	mod, wmin, wmax, err := splitWidthModifier(string(mr[1:]))
	if err != nil {
		return "", fmt.Errorf("%v in [%s]", err, marker)
	}

	// Resolve a numeric value and an optional name for the component.
	var num int
	var name string
	months, weekdays := monthWeekdayNames(opts)
	switch comp {
	case 'Y':
		num = tm.Year()
		if num < 0 {
			num = -num // the year is an absolute value; [E] carries the era (format-date-en141)
		}
	case 'M':
		num = int(tm.Month())
		name = months[tm.Month()-1]
	case 'D':
		num = tm.Day()
	case 'd':
		num = tm.YearDay()
	case 'F':
		wd := (int(tm.Weekday()) + 6) % 7 // Go Sunday=0 → ISO Monday=1
		num = wd + 1
		name = weekdays[wd]
	case 'H':
		num = tm.Hour()
	case 'h':
		num = tm.Hour() % 12
		if num == 0 {
			num = 12
		}
	case 'P':
		name = "am"
		if tm.Hour() >= 12 {
			name = "pm"
		}
		if mod == "" {
			mod = "n" // default presentation of P is lower case (format-dateTime-en142)
		}
		// Fall through to presentNumberOrName below (same as M/F) instead of
		// returning early: a width modifier must be honored too — [PNn,1-1]
		// shortens "am"/"pm" to a single character "a"/"p" (date-064), which
		// only the shared Nn+width branch there applies.
	case 'm':
		num = tm.Minute()
		if mod == "" {
			mod = "01"
		}
	case 's':
		num = tm.Second()
		if mod == "" {
			mod = "01"
		}
	case 'f':
		return formatFractional(tm.Nanosecond(), mod, wmin, wmax)
	case 'Z', 'z':
		return formatTimezone(at, tm, comp, mod, wmax), nil
	case 'E':
		opts.usedLang = true
		if tm.Year() < 0 {
			return "BC", nil
		}
		return "AD", nil
	case 'C':
		opts.usedLang = true
		return "Gregorian", nil
	case 'W':
		_, wk := tm.ISOWeek()
		num = wk
	case 'w':
		num = weekInMonth(tm)
	}

	return presentNumberOrName(comp, num, name, mod, wmin, wmax, opts)
}

// weekInMonth numbers the weeks of a month the ISO way: weeks run Monday to
// Sunday and week 1 is the week containing the month's first Thursday; the
// days before it belong to the last week of the previous month
// (format-date-011, format-dateTime-011).
func weekInMonth(tm time.Time) int {
	y, m, d := tm.Date()
	first := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
	wd := (int(first.Weekday()) + 6) % 7 // Monday=0 … Sunday=6
	mondayOfWeek1 := 1 + (3-wd+7)%7 - 3  // first Thursday minus three days
	if d < mondayOfWeek1 {
		return weekInMonth(first.AddDate(0, 0, -1))
	}
	return (d-mondayOfWeek1)/7 + 1
}

// splitWidthModifier separates the presentation modifier from an optional
// trailing ",min-max" width modifier. The split is at the LAST comma whose
// tail parses as a width, so a comma may serve as a grouping separator in
// the presentation part ([Y9,999,*] → 2,012; format-date-030/031). Widths
// of zero or min > max are FOFD1340 (format-date-048, millisecs-902/903/904).
// wmin/wmax are 0 when unspecified ('*' or absent).
func splitWidthModifier(rest string) (mod string, wmin, wmax int, err error) {
	mod = rest
	k := strings.LastIndexByte(rest, ',')
	if k < 0 {
		return mod, 0, 0, nil
	}
	parts := strings.SplitN(rest[k+1:], "-", 2)
	vals := make([]int, len(parts))
	for i, p := range parts {
		if p == "*" {
			continue
		}
		n, e := strconv.Atoi(p)
		if e != nil || p == "" {
			return mod, 0, 0, nil // not a width modifier: the comma belongs to the presentation
		}
		if n == 0 {
			return "", 0, 0, fmt.Errorf("err:FOFD1340: zero width modifier")
		}
		vals[i] = n
	}
	wmin = vals[0]
	if len(vals) == 2 {
		wmax = vals[1]
	}
	if wmin > 0 && wmax > 0 && wmin > wmax {
		return "", 0, 0, fmt.Errorf("err:FOFD1340: width modifier min %d exceeds max %d", wmin, wmax)
	}
	return rest[:k], wmin, wmax, nil
}

// presentNumberOrName applies the presentation/width modifiers to a component.
func presentNumberOrName(comp rune, num int, name, mod string, wmin, wmax int, opts *dtOptions) (string, error) {
	// Second presentation modifier: 'o' (ordinal), 't' (traditional),
	// 'c' (cardinal), 'a' (alphabetic after a digit pattern).
	ordinal := false
	if n := len(mod); n > 1 {
		switch mod[n-1] {
		case 'o':
			ordinal = true
			mod = mod[:n-1]
		case 't', 'c':
			mod = mod[:n-1]
		case 'a':
			if p := mod[n-2]; p == '#' || (p >= '0' && p <= '9') {
				mod = mod[:n-1]
			}
		}
	}
	// A maximum width on the year keeps its low-order digits whatever the
	// numbering: [Yi,3-3] renders 1004 as "iv " (format-dateTime-006).
	if comp == 'Y' && wmax > 0 && wmax < 10 {
		p := 1
		for i := 0; i < wmax; i++ {
			p *= 10
		}
		num %= p
	}
	switch {
	case strings.ContainsAny(mod, "Nn") && name != "":
		opts.usedLang = true
		s := applyNameModifier(name, mod)
		if wmax > 0 && len([]rune(s)) > wmax {
			// A name that must be shortened takes its conventional
			// three-letter abbreviation when that fits the width, else it is
			// truncated ([FNn,3-4] → Wed, not Wedn; format-date-en117/118).
			cut := wmax
			if wmin <= 3 && 3 <= wmax {
				cut = 3
			}
			s = string([]rune(s)[:cut])
		}
		return s, nil
	case mod == "I" || mod == "i":
		if num > 0 {
			return padRightSpaces(romanNumeral(int64(num), mod == "I"), wmin), nil
		}
	case mod == "a" || mod == "A":
		if num > 0 {
			return padRightSpaces(alphaSequence(int64(num), mod == "A"), wmin), nil
		}
	case mod == "w" || mod == "W" || mod == "Ww":
		opts.usedLang = true
		return numberWords(int64(num), mod, ordinal), nil
	}
	if ordinal {
		opts.usedLang = true
	}
	// Decimal digit pattern (default "1"): optional '#' signs before the
	// mandatory digits of one family, grouping separators between them
	// ([Y9;999] → 2;012, [Y#.0] → 1.6). A pattern that contains digits but
	// does not parse is malformed ([Y999#], [H9#]: FOFD1340).
	if mod == "" {
		mod = "1"
	}
	dp, ok := parseDigitPattern(mod)
	if !ok {
		if isDigitPatternPicture(mod) || strings.ContainsRune(mod, '#') {
			return "", fmt.Errorf("err:FOFD1340: invalid presentation modifier [%c%s]", comp, mod)
		}
		dp, _ = parseDigitPattern("1") // unrecognised token: format as "1"
		mod = "1"
	}
	min, max := effectiveWidths(dp.mandatory, countDigits(mod)+strings.Count(mod, "#"), wmin, wmax)

	neg := num < 0
	a := num
	if neg {
		a = -a
	}
	s := strconv.Itoa(a)
	if max > 0 && len(s) > max {
		s = s[len(s)-max:] // truncate to low-order digits ([Y01] → last two)
	}
	if len(s) < min {
		s = strings.Repeat("0", min-len(s)) + s
	}
	s = dp.render(s) // grouping separators + the picture's digit family
	if ordinal {
		s += ordinalSuffix(int64(num))
	}
	if neg {
		s = "-" + s
	}
	return s, nil
}

// effectiveWidths combines a digit pattern's mandatory/total positions with
// the width modifier: the pattern's mandatory digits and the width minimum
// both act as minimums; a multi-position pattern caps the width unless the
// modifier overrides it; a single-position pattern ("1") imposes no maximum
// ([Y#0,2-5] → 54321, [Y9999,25], [f111,2-2] → 123). max 0 = unbounded.
func effectiveWidths(mandatory, total, wmin, wmax int) (min, max int) {
	min = mandatory
	if wmin > min {
		min = wmin
	}
	if total > 1 {
		max = total
	}
	if wmax > 0 {
		max = wmax
	}
	if max > 0 && max < min {
		max = min
	}
	return min, max
}

func padRightSpaces(s string, min int) string {
	if n := len([]rune(s)); n < min {
		return s + strings.Repeat(" ", min-n)
	}
	return s
}

func countDigits(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsDigit(r) {
			n++
		}
	}
	return n
}

func applyNameModifier(name, mod string) string {
	switch {
	case strings.HasPrefix(mod, "Nn"):
		return titleCaseWords(name)
	case strings.HasPrefix(mod, "N"):
		return strings.ToUpper(name)
	case strings.HasPrefix(mod, "n"):
		return strings.ToLower(name)
	}
	return titleCaseWords(name)
}

// fracPattern is a parsed LEFT-aligned fractional-seconds digit pattern:
// mandatory digits first, optional '#' after them, grouping separators
// between positions ([f0'0'0] → 1'3'5), one digit family.
type fracPattern struct {
	mandatory, total int
	zero             rune
	seps             map[int]rune // separator preceding the digit at this index
}

func parseFracPattern(p string) (fracPattern, bool) {
	fp := fracPattern{zero: '0', seps: map[int]rune{}}
	rs := []rune(p)
	var famZero rune
	sawOptional, lastWasSep := false, false
	for i, r := range rs {
		switch {
		case r == '#':
			sawOptional = true
			fp.total++
			lastWasSep = false
		case unicode.IsDigit(r):
			if sawOptional {
				return fp, false // mandatory after optional ([f#99], millisecs-901)
			}
			z := r - rune(digitValue(r))
			if famZero == 0 {
				famZero = z
			} else if z != famZero {
				return fp, false // mixed digit families (millisecs-905)
			}
			fp.mandatory++
			fp.total++
			lastWasSep = false
		default:
			if i == 0 || i == len(rs)-1 || lastWasSep || unicode.IsLetter(r) || unicode.IsNumber(r) {
				return fp, false
			}
			fp.seps[fp.total] = r
			lastWasSep = true
		}
	}
	if fp.total == 0 {
		return fp, false
	}
	if famZero != 0 {
		fp.zero = famZero
	}
	return fp, true
}

// formatFractional renders the fractional seconds: the significant digits
// (no trailing zeros) truncated to the maximum width and zero-filled on the
// right to the minimum ([f] → 123, [f99] → 12, [f,6-*] → 135000).
func formatFractional(nanos int, mod string, wmin, wmax int) (string, error) {
	if mod == "" {
		mod = "1"
	}
	fp, ok := parseFracPattern(mod)
	if !ok {
		if isDigitPatternPicture(mod) || strings.ContainsRune(mod, '#') {
			return "", fmt.Errorf("err:FOFD1340: invalid presentation modifier [f%s]", mod)
		}
		fp, _ = parseFracPattern("1")
	}
	min, max := effectiveWidths(fp.mandatory, fp.total, wmin, wmax)
	s := fmt.Sprintf("%09d", nanos)
	if max > 0 && len(s) > max {
		s = s[:max]
	}
	// Trailing zeros go only after truncation: .006 with [f,*-2] is "0",
	// not "00" (format-time-023u).
	for len(s) > min && strings.HasSuffix(s, "0") {
		s = s[:len(s)-1]
	}
	if len(s) < min {
		s += strings.Repeat("0", min-len(s))
	}
	var out []rune
	for i, r := range []rune(mapDigitsToFamily(s, fp.zero)) {
		if sep, ok := fp.seps[i]; ok && i > 0 {
			out = append(out, sep)
		}
		out = append(out, r)
	}
	return string(out), nil
}

// formatTimezone renders [Z]/[z] per F&O §9.8.4.4. A numeric presentation
// modifier is hour digits, optionally a separator and minute digits, in any
// digit family: one or two hour digits with no separator show the minutes
// only when non-zero ([Z0] → -5, +5:30), three or four digits run hours and
// minutes together ([Z999] → -930, +1400), a separator always shows both
// ([Z0:01] → +0:00, [z00~00] → GMT-14~00). 't' prints Z for UTC, 'Z' is the
// military letter (J for a value with no timezone), and [z] prefixes GMT.
// Width modifiers otherwise do not apply (format-time-016/017/018: a max
// width of 5 or 6, or no width modifier at all, both leave the DEFAULT
// (no presentation modifier) pattern's "always hh:mm" behavior unchanged) —
// EXCEPT for that very default: an EXPLICIT width modifier narrow enough
// that "hh:mm" cannot fit (max in [1,4]) instead falls back to the same
// "minutes only when non-zero" form a bare [Z0] pattern uses (format-date-017:
// "[z,2-2]" — max width 2 — must render "GMT-14", not "GMT-14:00").
func formatTimezone(at *Atomic, tm time.Time, comp rune, mod string, wmax int) string {
	traditional := false
	if n := len(mod); n > 1 && mod[n-1] == 't' {
		traditional = true
		mod = mod[:n-1]
	}
	military := mod == "Z"
	if !at.hasTZ {
		if military {
			return "J"
		}
		return ""
	}
	_, off := tm.Zone()
	sign := "+"
	if off < 0 {
		sign = "-"
		off = -off
	}
	hh := off / 3600
	mm := (off % 3600) / 60
	if military {
		if mm == 0 && hh <= 12 {
			switch {
			case hh == 0:
				return "Z"
			case sign == "+" && hh <= 9:
				return string(rune('A' + hh - 1))
			case sign == "+":
				return string(rune('K' + hh - 10))
			default:
				return string(rune('N' + hh - 1))
			}
		}
		mod = "00:00"
	}
	if traditional && off == 0 && comp == 'Z' {
		return "Z"
	}
	hDigits, mDigits, sep, zero := parseTZPattern(mod)
	if mod == "" && wmax >= 1 && wmax <= 4 {
		// No presentation modifier (parseTZPattern's own "00:00" default),
		// but an explicit width too narrow for "hh:mm" to fit: fall back to
		// the compact "minutes only when non-zero" rendering instead
		// (format-date-017's "[z,2-2]" — max width 2).
		hDigits, mDigits, sep = 2, 0, ""
	}
	pad := func(n, w int) string {
		s := strconv.Itoa(n)
		if len(s) < w {
			s = strings.Repeat("0", w-len(s)) + s
		}
		return s
	}
	var s string
	switch {
	case sep == "" && hDigits <= 2:
		s = pad(hh, hDigits)
		if mm != 0 {
			s += ":" + pad(mm, 2)
		}
	case sep == "":
		s = pad(hh*100+mm, hDigits)
	default:
		s = pad(hh, hDigits) + sep + pad(mm, mDigits)
	}
	s = sign + mapDigitsToFamily(s, zero)
	if comp == 'z' {
		s = "GMT" + s
	}
	return s
}

// parseTZPattern splits a timezone digit pattern into hour digits, minute
// digits, their separator and the digit family; anything without digits
// (the default, [ZN]) means "00:00".
func parseTZPattern(mod string) (hDigits, mDigits int, sep string, zero rune) {
	var sepRunes []rune
	inMinutes := false
	for _, r := range mod {
		switch {
		case unicode.IsDigit(r) || r == '#':
			if r != '#' && zero == 0 {
				zero = r - rune(digitValue(r))
			}
			if inMinutes {
				mDigits++
			} else {
				hDigits++
			}
		default:
			if hDigits > 0 {
				inMinutes = true
			}
			if inMinutes && mDigits == 0 {
				sepRunes = append(sepRunes, r)
			}
		}
	}
	if hDigits == 0 {
		return 2, 2, ":", '0'
	}
	if zero == 0 {
		zero = '0'
	}
	if mDigits > 0 {
		sep = string(sepRunes)
	}
	return hDigits, mDigits, sep, zero
}
