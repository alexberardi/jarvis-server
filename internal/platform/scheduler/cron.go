package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed 5-field cron expression: minute hour day-of-month month day-of-week.
// Fields accept *, numbers, ranges (a-b), lists (a,b) and steps (*/n, a-b/n). Day-of-week is
// 0-6 with 0 = Sunday (7 is also Sunday). As in Vixie cron, when both day fields are
// restricted a time matches if EITHER does.
type Cron struct {
	minute, hour, dom, month, dow uint64 // bitsets
	domStar, dowStar              bool
}

var cronFields = []struct {
	name     string
	min, max int
}{{"minute", 0, 59}, {"hour", 0, 23}, {"day-of-month", 1, 31}, {"month", 1, 12}, {"day-of-week", 0, 7}}

// ParseCron parses a 5-field expression.
func ParseCron(expr string) (Cron, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return Cron{}, fmt.Errorf("cron: %q: want 5 fields, got %d", expr, len(parts))
	}
	var sets [5]uint64
	for i, p := range parts {
		f := cronFields[i]
		set, err := parseField(p, f.min, f.max)
		if err != nil {
			return Cron{}, fmt.Errorf("cron: %q: %s: %w", expr, f.name, err)
		}
		sets[i] = set
	}
	if sets[4]&(1<<7) != 0 { // 7 means Sunday
		sets[4] |= 1
	}
	return Cron{
		minute: sets[0], hour: sets[1], dom: sets[2], month: sets[3], dow: sets[4],
		domStar: parts[2] == "*", dowStar: parts[4] == "*",
	}, nil
}

func parseField(s string, lo, hi int) (uint64, error) {
	var set uint64
	for _, part := range strings.Split(s, ",") {
		rng, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepStr)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("bad step %q", stepStr)
			}
			step = n
		}
		a, b := lo, hi
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			x, y, _ := strings.Cut(rng, "-")
			var err1, err2 error
			a, err1 = strconv.Atoi(x)
			b, err2 = strconv.Atoi(y)
			if err1 != nil || err2 != nil {
				return 0, fmt.Errorf("bad range %q", rng)
			}
		default:
			n, err := strconv.Atoi(rng)
			if err != nil {
				return 0, fmt.Errorf("bad value %q", rng)
			}
			a, b = n, n
			if hasStep {
				b = hi
			}
		}
		if a < lo || b > hi || a > b {
			return 0, fmt.Errorf("%q out of range %d-%d", part, lo, hi)
		}
		for v := a; v <= b; v += step {
			set |= 1 << v
		}
	}
	return set, nil
}

func has(set uint64, v int) bool { return set&(1<<v) != 0 }

func (c Cron) dayMatches(t time.Time) bool {
	dom, dow := has(c.dom, t.Day()), has(c.dow, int(t.Weekday()))
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return dow
	case c.dowStar:
		return dom
	default:
		return dom || dow
	}
}

// Next returns the first matching minute strictly after `after`, in after's location. It
// gives up (ok=false) for expressions that never match, e.g. Feb 31.
func (c Cron) Next(after time.Time) (time.Time, bool) {
	loc := after.Location()
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := after.AddDate(5, 0, 0)
	for t.Before(limit) {
		if !has(c.month, int(t.Month())) {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
			continue
		}
		if !c.dayMatches(t) {
			n := time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
			if !n.After(t) { // a zone where midnight is skipped by DST
				n = t.Add(time.Hour)
			}
			t = n
			continue
		}
		if !has(c.hour, t.Hour()) {
			// Step in absolute time: time.Date with a wall hour that a DST jump skips can
			// normalize backwards and loop forever.
			t = t.Add(time.Hour - time.Duration(t.Minute())*time.Minute)
			continue
		}
		if !has(c.minute, t.Minute()) {
			t = t.Add(time.Minute)
			continue
		}
		return t, true
	}
	return time.Time{}, false
}
