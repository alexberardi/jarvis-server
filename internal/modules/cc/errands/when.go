package errands

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/dates"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
)

// schedule_errand's time parsing (core/tools/schedule_errand_tool.py), in the NODE's zone
// (§7.14). Byte-for-byte, quirks included: "afternoon" matches "noon" first (dict order).

func zoneOf(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

var weekdayNames = []struct {
	name string
	wd   int // Python weekday(): Monday 0 … Sunday 6
}{
	{"monday", 0}, {"tuesday", 1}, {"wednesday", 2}, {"thursday", 3}, {"friday", 4}, {"saturday", 5}, {"sunday", 6},
}

var namedTimes = []struct {
	name   string
	hh, mm int
}{
	{"midnight", 0, 0}, {"morning", 9, 0}, {"noon", 12, 0}, {"afternoon", 13, 0},
	{"evening", 18, 0}, {"tonight", 19, 0}, {"night", 20, 0},
}

var clockRE = regexp.MustCompile(`(?i)\b(\d{1,4})(?:[:.](\d{2}))?\s*(am|pm)\b`)

func pyWeekday(t time.Time) int { return (int(t.Weekday()) + 6) % 7 }

// parseISOInstant is datetime.fromisoformat for the forms the model or resolver sends.
func parseISOInstant(s string, loc *time.Location) (time.Time, bool) {
	if strings.HasSuffix(s, "Z") {
		s = s[:len(s)-1] + "+00:00"
	}
	for _, f := range []string{"2006-01-02T15:04:05.999999999-07:00", "2006-01-02T15:04-07:00", "2006-01-02 15:04:05.999999999-07:00"} {
		if t, err := time.Parse(f, s); err == nil {
			return t, true
		}
	}
	for _, f := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04", "2006-01-02 15:04:05.999999999", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(f, s, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ResolveFireAt is _resolve_fire_at: a natural phrase ("tomorrow at 9am", "friday at noon") or
// an ISO instant, in the node's zone, to a UTC instant. ok=false: no day and no time.
func ResolveFireAt(raw, tzName string, now time.Time) (time.Time, bool) {
	s := parse.PyStrip(raw)
	if s == "" {
		return time.Time{}, false
	}
	loc := zoneOf(tzName)
	if dt, ok := parseISOInstant(s, loc); ok {
		// A bare date at midnight is almost never the intended time: fall through to the phrase.
		if !(dt.Hour() == 0 && dt.Minute() == 0) {
			return dt.UTC(), true
		}
	}
	low := strings.ToLower(s)
	today := now.In(loc)
	y, mo, d := today.Date()
	date := time.Date(y, mo, d, 0, 0, 0, 0, loc)
	dayNamed := strings.Contains(low, "tomorrow") || strings.Contains(low, "today")
	if strings.Contains(low, "tomorrow") {
		date = date.AddDate(0, 0, 1)
	} else {
		for _, w := range weekdayNames {
			if strings.Contains(low, w.name) {
				dayNamed = true
				days := ((w.wd-pyWeekday(today))%7 + 7) % 7
				if days == 0 {
					days = 7
				}
				date = date.AddDate(0, 0, days)
				break
			}
		}
	}
	hour, minute, have := 0, 0, false
	if m := clockRE.FindStringSubmatch(low); m != nil {
		token := m[1]
		if m[2] != "" {
			token += "_" + m[2]
		}
		token += m[3]
		hour, minute, _ = dates.ParseTime(token) // legacy (0, 0) when unparseable
		have = true
	} else {
		for _, nt := range namedTimes {
			if strings.Contains(low, nt.name) {
				hour, minute, have = nt.hh, nt.mm, true
				break
			}
		}
	}
	if !have {
		if !dayNamed {
			return time.Time{}, false
		}
		hour, minute = 9, 0
	}
	y, mo, d = date.Date()
	return time.Date(y, mo, d, hour, minute, 0, 0, loc).UTC(), true
}

var everyRE = regexp.MustCompile(`every\s+(\d+)\s*(minute|min|hour|hr)s?`)

func recJSON(kv ...any) string { return pyjson.Dumps(servertools.Obj(kv...), true) }

// BuildRecurrence is _build_recurrence: a coarse cadence phrase plus the first fire's local
// time to a recurrence spec (JSON), "" for a one-shot or an unrecognised phrase.
func BuildRecurrence(descriptor string, fireUTC time.Time, tzName string) string {
	d := strings.ToLower(parse.PyStrip(descriptor))
	switch d {
	case "", "none", "once", "one-time", "one time", "just once", "no", "never":
		return ""
	}
	if m := everyRE.FindStringSubmatch(d); m != nil {
		n, _ := strconv.Atoi(m[1])
		secs := n * 3600
		if strings.HasPrefix(m[2], "min") {
			secs = n * 60
		}
		if secs <= 0 {
			return ""
		}
		return recJSON("type", "interval", "interval_seconds", secs)
	}
	if strings.Contains(d, "hour") {
		return recJSON("type", "interval", "interval_seconds", 3600)
	}
	local := fireUTC.In(zoneOf(tzName))
	hh, mm := local.Hour(), local.Minute()
	switch {
	case strings.Contains(d, "weekday"):
		return recJSON("type", "cron", "cron", strconv.Itoa(mm)+" "+strconv.Itoa(hh)+" * * 1-5")
	case strings.Contains(d, "week"):
		return recJSON("type", "cron", "cron", strconv.Itoa(mm)+" "+strconv.Itoa(hh)+" * * "+strconv.Itoa(int(local.Weekday())))
	case strings.Contains(d, "month"):
		return recJSON("type", "cron", "cron", strconv.Itoa(mm)+" "+strconv.Itoa(hh)+" "+strconv.Itoa(local.Day())+" * *")
	case strings.Contains(d, "daily"), strings.Contains(d, "day"):
		return recJSON("type", "cron", "cron", strconv.Itoa(mm)+" "+strconv.Itoa(hh)+" * * *")
	}
	return ""
}

// DefaultRecurringFire is _default_recurring_fire: 9:00 local today.
func DefaultRecurringFire(tzName string, now time.Time) time.Time {
	loc := zoneOf(tzName)
	y, mo, d := now.In(loc).Date()
	return time.Date(y, mo, d, 9, 0, 0, 0, loc).UTC()
}

// NextFire is compute_next_fire: the next instant strictly after `after` for a recurrence spec.
// (Doc 08 owns the schedule sweep; this is only schedule_errand's roll-forward of a past first
// fire.)
func NextFire(recurrence string, after time.Time, tzName string) (time.Time, bool) {
	var spec struct {
		Type     string      `json:"type"`
		Interval json.Number `json:"interval_seconds"`
		Cron     string      `json:"cron"`
	}
	if recurrence == "" || json.Unmarshal([]byte(recurrence), &spec) != nil {
		return time.Time{}, false
	}
	switch spec.Type {
	case "interval":
		f, err := spec.Interval.Float64()
		secs := int64(f)
		if err != nil || secs <= 0 {
			return time.Time{}, false
		}
		next := after.Add(time.Duration(secs) * time.Second)
		for !next.After(after) {
			next = next.Add(time.Duration(secs) * time.Second)
		}
		return next, true
	case "cron":
		c, err := scheduler.ParseCron(spec.Cron)
		if err != nil {
			return time.Time{}, false
		}
		n, ok := c.Next(after.In(zoneOf(tzName)))
		return n.UTC(), ok
	}
	return time.Time{}, false
}

// friendlyWhen is _friendly_when: "%A at %-I:%M %p" in the user's zone, ":00" removed.
func friendlyWhen(fireUTC time.Time, tzName string) string {
	local := fireUTC.In(zoneOf(tzName))
	return strings.ReplaceAll(local.Format("Monday at 3:04 PM"), ":00", "")
}

// friendlyRecurrence is _friendly_recurrence.
func friendlyRecurrence(descriptor string) string {
	d := strings.ToLower(parse.PyStrip(descriptor))
	known := map[string]string{
		"daily": "every day", "weekdays": "on weekdays", "weekly": "every week",
		"monthly": "every month", "hourly": "every hour",
	}
	if v, ok := known[d]; ok {
		return v
	}
	if strings.HasPrefix(d, "every") {
		return d
	}
	return "on a repeating schedule"
}
