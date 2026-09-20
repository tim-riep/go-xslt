package xpath

import "testing"

func TestFnDateTimeComponents(t *testing.T) {
	cases := []struct{ expr, want string }{
		// dateTime constructor
		{`dateTime(xs:date("2004-05-06"), xs:time("07:08:09"))`, "2004-05-06T07:08:09"},

		// dateTime component extractors
		{`year-from-dateTime(xs:dateTime("2004-05-06T07:08:09"))`, "2004"},
		{`month-from-dateTime(xs:dateTime("2004-05-06T07:08:09"))`, "5"},
		{`day-from-dateTime(xs:dateTime("2004-05-06T07:08:09"))`, "6"},
		{`hours-from-dateTime(xs:dateTime("2004-05-06T07:08:09"))`, "7"},
		{`minutes-from-dateTime(xs:dateTime("2004-05-06T07:08:09"))`, "8"},
		{`seconds-from-dateTime(xs:dateTime("2004-05-06T07:08:09"))`, "9"},
		{`seconds-from-dateTime(xs:dateTime("2004-05-06T07:08:09.5"))`, "9.5"},
		{`timezone-from-dateTime(xs:dateTime("2004-05-06T07:08:09+02:00"))`, "PT2H"},
		{`timezone-from-dateTime(xs:dateTime("2004-05-06T07:08:09Z"))`, "PT0S"},
		{`timezone-from-dateTime(xs:dateTime("2004-05-06T07:08:09"))`, ""},

		// date component extractors
		{`year-from-date(xs:date("2004-05-06"))`, "2004"},
		{`month-from-date(xs:date("2004-05-06"))`, "5"},
		{`day-from-date(xs:date("2004-05-06"))`, "6"},
		{`timezone-from-date(xs:date("2004-05-06-05:00"))`, "-PT5H"},

		// time component extractors
		{`hours-from-time(xs:time("07:08:09"))`, "7"},
		{`minutes-from-time(xs:time("07:08:09"))`, "8"},
		{`seconds-from-time(xs:time("07:08:09"))`, "9"},
		{`timezone-from-time(xs:time("07:08:09Z"))`, "PT0S"},

		// duration component extractors
		{`years-from-duration(xs:duration("P2Y6M"))`, "2"},
		{`months-from-duration(xs:duration("P2Y6M"))`, "6"},
		{`days-from-duration(xs:duration("P3DT4H5M6S"))`, "3"},
		{`hours-from-duration(xs:duration("P3DT4H5M6S"))`, "4"},
		{`minutes-from-duration(xs:duration("P3DT4H5M6S"))`, "5"},
		{`seconds-from-duration(xs:duration("P3DT4H5M6S"))`, "6"},
		{`years-from-duration(xs:yearMonthDuration("-P2Y6M"))`, "-2"},
		{`hours-from-duration(xs:dayTimeDuration("-PT4H5M"))`, "-4"},

		// implicit-timezone
		{`implicit-timezone()`, "PT0S"},
	}
	for _, c := range cases {
		if got := xpStr(t, c.expr); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}

func TestFnDateTimeCurrent(t *testing.T) {
	// current-* should produce non-empty lexical forms.
	for _, expr := range []string{"current-dateTime()", "current-date()", "current-time()"} {
		if got := xpStr(t, expr); got == "" {
			t.Errorf("%s returned empty", expr)
		}
	}
}
