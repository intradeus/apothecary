// Package schedule reads the five-field cron spelling an automation fires on
// ("0 9 * * 1-5": nine o'clock on weekdays) and finds the next firing after a
// moment — in UTC, at minute resolution. Only what an automation needs: `*`,
// lists, ranges, steps, and names for months and weekdays. No seconds, no
// `@daily` shorthands, no time zones: the spelling is the one every cron
// reader knows, and the daemon's clock is UTC.
package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed schedule.
type Cron struct {
	minute, hour, dom, month, dow uint64
	// anyDom / anyDow remember a `*`: Vixie cron fires on EITHER day field
	// when both are restricted, on the restricted one when only one is.
	anyDom, anyDow bool
	expr           string
}

type field struct {
	name     string
	min, max int
	names    []string
}

var fields = []field{
	{"minute", 0, 59, nil},
	{"hour", 0, 23, nil},
	{"day of month", 1, 31, nil},
	{"month", 1, 12, []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}},
	{"day of week", 0, 7, []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}},
}

// Parse reads "minute hour day-of-month month day-of-week".
func Parse(expr string) (*Cron, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return nil, fmt.Errorf("schedule %q: want five fields (minute hour day-of-month month day-of-week), got %d", expr, len(parts))
	}
	c := &Cron{expr: expr}
	var sets [5]uint64
	var any [5]bool
	for i, f := range fields {
		set, isAny, err := parseField(parts[i], f)
		if err != nil {
			return nil, fmt.Errorf("schedule %q: %s: %w", expr, f.name, err)
		}
		sets[i], any[i] = set, isAny
	}
	c.minute, c.hour, c.dom, c.month, c.dow = sets[0], sets[1], sets[2], sets[3], sets[4]
	// 7 is Sunday too.
	if c.dow&(1<<7) != 0 {
		c.dow |= 1
	}
	c.anyDom, c.anyDow = any[2], any[4]
	return c, nil
}

// String is the spelling it was parsed from.
func (c *Cron) String() string { return c.expr }

func parseField(s string, f field) (set uint64, any bool, err error) {
	for item := range strings.SplitSeq(s, ",") {
		if item == "" {
			return 0, false, fmt.Errorf("empty list item in %q", s)
		}
		step := 1
		rng := item
		if i := strings.IndexByte(item, '/'); i >= 0 {
			rng = item[:i]
			step, err = strconv.Atoi(item[i+1:])
			if err != nil || step < 1 {
				return 0, false, fmt.Errorf("step %q is not a positive number", item[i+1:])
			}
		}
		lo, hi := f.min, f.max
		switch {
		case rng == "*":
			if step == 1 {
				any = true
			}
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			if lo, err = f.value(a); err != nil {
				return 0, false, err
			}
			if hi, err = f.value(b); err != nil {
				return 0, false, err
			}
			if hi < lo {
				return 0, false, fmt.Errorf("range %q runs backwards", rng)
			}
		default:
			if lo, err = f.value(rng); err != nil {
				return 0, false, err
			}
			if step == 1 {
				hi = lo
			}
		}
		for v := lo; v <= hi; v += step {
			set |= 1 << uint(v)
		}
	}
	return set, any, nil
}

func (f field) value(s string) (int, error) {
	if f.names != nil {
		for i, n := range f.names {
			if strings.EqualFold(s, n) {
				return f.min + i, nil
			}
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a %s", s, f.name)
	}
	if v < f.min || v > f.max {
		return 0, fmt.Errorf("%d is outside %d-%d", v, f.min, f.max)
	}
	return v, nil
}

func has(set uint64, v int) bool { return set&(1<<uint(v)) != 0 }

func (c *Cron) dayMatches(t time.Time) bool {
	dom := has(c.dom, t.Day())
	dow := has(c.dow, int(t.Weekday()))
	switch {
	case c.anyDom && c.anyDow:
		return true
	case c.anyDom:
		return dow
	case c.anyDow:
		return dom
	default:
		return dom || dow
	}
}

// Next is the first minute strictly after `after` the schedule matches, in
// UTC; zero when none comes within five years (a February 30th).
func (c *Cron) Next(after time.Time) time.Time {
	t := after.UTC().Truncate(time.Minute).Add(time.Minute)
	end := t.AddDate(5, 0, 0)
	for t.Before(end) {
		y, m, d := t.Date()
		switch {
		case !has(c.month, int(m)):
			t = time.Date(y, m+1, 1, 0, 0, 0, 0, time.UTC)
		case !c.dayMatches(t):
			t = time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
		case !has(c.hour, t.Hour()):
			t = time.Date(y, m, d, t.Hour()+1, 0, 0, 0, time.UTC)
		case !has(c.minute, t.Minute()):
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}
