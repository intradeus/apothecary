package schedule

import (
	"strings"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNext(t *testing.T) {
	for _, tc := range []struct{ expr, after, want string }{
		// Weekday mornings: a Saturday afternoon waits for Monday.
		{"0 9 * * 1-5", "2026-10-24T15:00:00Z", "2026-10-26T09:00:00Z"},
		// Strictly after: on the dot moves to the next slot.
		{"0 9 * * 1-5", "2026-10-26T09:00:00Z", "2026-10-27T09:00:00Z"},
		{"*/15 * * * *", "2026-10-23T10:07:00Z", "2026-10-23T10:15:00Z"},
		{"*/15 * * * *", "2026-10-23T10:45:30Z", "2026-10-23T11:00:00Z"},
		// First of the month, across a year end.
		{"0 0 1 * *", "2026-12-15T00:00:00Z", "2027-01-01T00:00:00Z"},
		// Names, and 7 as Sunday.
		{"30 6 * jan sun", "2026-10-23T00:00:00Z", "2027-01-03T06:30:00Z"},
		{"30 6 * * 7", "2026-10-23T00:00:00Z", "2026-10-25T06:30:00Z"},
		// Both day fields restricted: either matches (Vixie cron).
		{"0 12 15 * mon", "2026-10-13T00:00:00Z", "2026-10-15T12:00:00Z"},
		{"0 12 15 * mon", "2026-10-15T13:00:00Z", "2026-10-19T12:00:00Z"},
		// A list with a range and a step.
		{"0 8,12-14,20/2 * * *", "2026-10-23T12:30:00Z", "2026-10-23T13:00:00Z"},
		{"0 8,12-14,20/2 * * *", "2026-10-23T14:30:00Z", "2026-10-23T20:00:00Z"},
		{"0 8,12-14,20/2 * * *", "2026-10-23T20:30:00Z", "2026-10-23T22:00:00Z"},
	} {
		c, err := Parse(tc.expr)
		if err != nil {
			t.Fatalf("%q: %v", tc.expr, err)
		}
		if got := c.Next(at(tc.after)); !got.Equal(at(tc.want)) {
			t.Errorf("%q after %s = %s, want %s", tc.expr, tc.after, got.Format(time.RFC3339), tc.want)
		}
	}
	// A day that never comes.
	c, _ := Parse("0 0 30 2 *")
	if !c.Next(at("2026-10-23T00:00:00Z")).IsZero() {
		t.Error("February 30th came")
	}
}

func TestParseRefuses(t *testing.T) {
	for expr, want := range map[string]string{
		"0 9 * *":       "five fields",
		"60 * * * *":    "outside 0-59",
		"0 24 * * *":    "outside 0-23",
		"0 9 * * mon-x": `"x" is not a day of week`,
		"0 9 5-2 * *":   "runs backwards",
		"*/0 * * * *":   "not a positive number",
		"0,,9 * * * *":  "empty list item",
	} {
		if _, err := Parse(expr); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", expr, err, want)
		}
	}
}

func TestStringIsTheSpellingItWasParsedFrom(t *testing.T) {
	c, err := Parse("*/5 * * * 1")
	if err != nil {
		t.Fatal(err)
	}
	if c.String() != "*/5 * * * 1" {
		t.Errorf("string = %q", c.String())
	}
}

func TestParseFieldRefusesAnEmptyListItem(t *testing.T) {
	if _, err := Parse("1,,2 * * * *"); err == nil {
		t.Error("an empty item is refused")
	}
}
