// Package clock is the node's get_current_time tool, built in so every Go node answers "what
// time is it?" from its own clock. jarvisd's per-turn <calendar> block also carries the exact
// local time, which the model usually answers from directly; the tool covers other places'
// times and the date questions below. (The ambient block's quarter-hour clock, which the model
// read out as "12:45" at 00:55, is gone.)
//
// The name, parameter and output follow the Python node's commands/timezone_command.py, so
// command-center can't tell them apart. Differences: a location is an IANA zone name or
// one of a few common places (the model is asked for IANA names), and an unknown location
// answers local time with a note instead of failing (docs/EXTERNAL-CHANGES.md, A10 F20).
//
// jarvisd speaks a tool's message directly (FastPathMessage), so date questions need their own
// message: query picks what is said ("what day is it?" → "It's Monday."), days_offset moves the
// day (tomorrow, yesterday) and until_date counts the days to a date. A port of the ideas in
// the Python node's jarvis-node-setup #148/#149.
package clock

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // IANA names on hosts without a zoneinfo database (Windows)

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// ToolName is the Python node's command name.
const ToolName = "get_current_time"

// localNames are the values a model fills in when it means "here" (Python's _LOCAL_PLACEHOLDERS
// plus the generic ones EXTERNAL-CHANGES F20 lists).
var localNames = map[string]bool{
	"here": true, "local": true, "current": true, "now": true, "default": true, "none": true,
	"null": true, "my location": true, "": true, "today": true, "local time": true, "home": true,
	"my timezone": true, "this location": true, "my time zone": true,
}

// places maps common spoken places to zones, for models that ignore the IANA instruction.
var places = map[string]string{
	"new york": "America/New_York", "nyc": "America/New_York", "boston": "America/New_York",
	"chicago": "America/Chicago", "denver": "America/Denver", "phoenix": "America/Phoenix",
	"los angeles": "America/Los_Angeles", "la": "America/Los_Angeles", "san francisco": "America/Los_Angeles",
	"seattle": "America/Los_Angeles", "california": "America/Los_Angeles", "texas": "America/Chicago",
	"florida": "America/New_York", "hawaii": "Pacific/Honolulu", "alaska": "America/Anchorage",
	"toronto": "America/Toronto", "vancouver": "America/Vancouver", "mexico city": "America/Mexico_City",
	"london": "Europe/London", "uk": "Europe/London", "england": "Europe/London", "dublin": "Europe/Dublin",
	"paris": "Europe/Paris", "france": "Europe/Paris", "berlin": "Europe/Berlin", "germany": "Europe/Berlin",
	"madrid": "Europe/Madrid", "spain": "Europe/Madrid", "rome": "Europe/Rome", "italy": "Europe/Rome",
	"amsterdam": "Europe/Amsterdam", "moscow": "Europe/Moscow", "dubai": "Asia/Dubai",
	"india": "Asia/Kolkata", "mumbai": "Asia/Kolkata", "delhi": "Asia/Kolkata", "singapore": "Asia/Singapore",
	"hong kong": "Asia/Hong_Kong", "beijing": "Asia/Shanghai", "shanghai": "Asia/Shanghai", "china": "Asia/Shanghai",
	"tokyo": "Asia/Tokyo", "japan": "Asia/Tokyo", "seoul": "Asia/Seoul", "korea": "Asia/Seoul",
	"sydney": "Australia/Sydney", "melbourne": "Australia/Melbourne", "australia": "Australia/Sydney",
	"auckland": "Pacific/Auckland", "new zealand": "Pacific/Auckland", "sao paulo": "America/Sao_Paulo",
	"brazil": "America/Sao_Paulo", "utc": "UTC", "gmt": "UTC",
}

// Queries are what the spoken message answers.
const (
	QueryTime    = "time"
	QueryDate    = "date"
	QueryWeekday = "weekday"
	QueryYear    = "year"
	QueryMonth   = "month"
	QueryWeekend = "weekend"
)

// maxOffset bounds days_offset to about a year either way.
const maxOffset = 366

var spec = node.Spec{
	Name: ToolName,
	Description: "Current time, date, day of the week, month or year, locally or in a city/state/country; " +
		"also tomorrow's/yesterday's day and date, whether it's the weekend, and how many days until a date. " +
		"Use for 'what time is it?', 'what day is it?', 'what's the date tomorrow?', 'is it the weekend?', " +
		"'how many days until Christmas?', 'what time is it in Tokyo?'. Not for weather — use get_weather.",
	Params: []node.Param{
		{Name: "location", Type: "string",
			Description: "Omit for local time. Otherwise an IANA time zone (e.g. 'Asia/Tokyo', 'Europe/London') or a city/country name."},
		{Name: "query", Type: "string", Enum: []string{QueryTime, QueryDate, QueryWeekday, QueryYear, QueryMonth, QueryWeekend},
			Description: "What is asked: time (default), date, weekday, year, month or weekend."},
		{Name: "days_offset", Type: "int",
			Description: "Days from today for date questions: tomorrow 1, yesterday -1, 'in 3 days' 3. Omit for today."},
		{Name: "until_date", Type: "string",
			Description: "For 'how many days until ...': the date as MM-DD (Christmas '12-25')."},
	},
	Keywords: []string{"time", "date", "day", "weekday", "weekend", "month", "year", "tomorrow", "yesterday", "until", "clock", "timezone"},
	Examples: []node.Example{
		{VoiceCommand: "what time is it", Params: map[string]any{}, Primary: true},
		{VoiceCommand: "what time is it in Tokyo", Params: map[string]any{"location": "Asia/Tokyo"}},
		{VoiceCommand: "what day is it today", Params: map[string]any{"query": QueryWeekday}},
		{VoiceCommand: "what's the date", Params: map[string]any{"query": QueryDate}},
		{VoiceCommand: "what's the date tomorrow", Params: map[string]any{"query": QueryDate, "days_offset": 1}},
		{VoiceCommand: "what day was it yesterday", Params: map[string]any{"query": QueryWeekday, "days_offset": -1}},
		{VoiceCommand: "is it the weekend", Params: map[string]any{"query": QueryWeekend}},
		{VoiceCommand: "what year is it", Params: map[string]any{"query": QueryYear}},
		{VoiceCommand: "how many days until Christmas", Params: map[string]any{"until_date": "12-25"}},
	},
}

// Tool serves get_current_time. Now and Local default to the host clock and zone.
type Tool struct {
	Now   func() time.Time
	Local *time.Location
}

func (t *Tool) Available(context.Context) []node.ToolDef { return []node.ToolDef{spec.Render()} }

func (t *Tool) Call(_ context.Context, name string, args map[string]any, _ node.Request) (map[string]any, bool) {
	if name != ToolName {
		return nil, false
	}
	now := time.Now
	if t.Now != nil {
		now = t.Now
	}
	local := time.Local
	if t.Local != nil {
		local = t.Local
	}
	asked, _ := args["location"].(string)
	asked = strings.TrimSpace(asked)
	loc, label, note := local, "local", ""
	if key := strings.ToLower(asked); !localNames[key] {
		if z, ok := zoneFor(asked); ok {
			loc, label = z, asked
		} else {
			note = "Unknown location \"" + asked + "\": this is local time."
		}
	}
	at := now().In(loc)
	offset := max(-maxOffset, min(maxOffset, intArg(args["days_offset"])))
	day := at.AddDate(0, 0, offset)
	out := map[string]any{
		"success":      true,
		"location":     label,
		"timezone":     zoneName(loc, at),
		"current_time": at.Format("3:04 PM"),
		"current_date": at.Format("Monday, January 02"),
		"iso_datetime": at.Format(time.RFC3339),
		// The asked day: today unless days_offset.
		"day_of_week": day.Weekday().String(),
		"year":        day.Year(),
		"is_weekend":  weekend(day),
	}
	if offset != 0 {
		out["days_offset"] = offset
		out["target_date"] = day.Format(time.DateOnly)
	}
	query, _ := args["query"].(string)
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		query = QueryTime
	}
	if query == QueryTime && offset != 0 {
		query = QueryDate // "what's tomorrow" with the default query
	}
	out["message"] = message(query, at, day, offset, label)
	if until, _ := args["until_date"].(string); strings.TrimSpace(until) != "" {
		if m, d, ok := parseMonthDay(until); ok {
			n, date := daysUntil(at, m, d)
			out["days_until"] = n
			out["until_date"] = date.Format(time.DateOnly)
			out["message"] = untilMessage(n, date)
		} else {
			note = strings.TrimSpace(note + " Couldn't read until_date \"" + until + "\": expected MM-DD.")
		}
	}
	if note != "" {
		out["note"] = note
	}
	return out, true
}

// message is the spoken answer to query about day (at moved by offset days).
func message(query string, at, day time.Time, offset int, label string) string {
	where := ""
	if label != "local" {
		where = " in " + label
	}
	switch query {
	case QueryDate:
		layout := "Monday, January 2"
		if day.Year() != at.Year() {
			layout += ", 2006"
		}
		return dayPhrase(offset, "It's ") + day.Format(layout) + where + "."
	case QueryWeekday:
		return dayPhrase(offset, "It's ") + day.Weekday().String() + where + "."
	case QueryMonth:
		return dayPhrase(offset, "It's ") + inPrefix(offset) + day.Month().String() + where + "."
	case QueryYear:
		return dayPhrase(offset, "It's ") + inPrefix(offset) + strconv.Itoa(day.Year()) + where + "."
	case QueryWeekend:
		if offset == 0 {
			if weekend(day) {
				return "Yes, it's the weekend" + where + "."
			}
			return "No, it's " + day.Weekday().String() + where + "."
		}
		subject := dayPhrase(offset, "")
		if weekend(day) {
			return "Yes, " + lowerFirst(subject) + day.Weekday().String() + where + "."
		}
		return "No, " + lowerFirst(subject) + day.Weekday().String() + where + "."
	}
	return "It's " + at.Format("3:04 PM") + where + "."
}

// dayPhrase starts a sentence about the day offset days away ("Tomorrow is ").
func dayPhrase(offset int, today string) string {
	switch {
	case offset == 0:
		return today
	case offset == 1:
		return "Tomorrow is "
	case offset == -1:
		return "Yesterday was "
	case offset > 1:
		return "In " + strconv.Itoa(offset) + " days it'll be "
	default:
		return strconv.Itoa(-offset) + " days ago was "
	}
}

// inPrefix is "in " for a month or year on another day ("Tomorrow is in November").
func inPrefix(offset int) string {
	if offset == 0 {
		return ""
	}
	return "in "
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func weekend(t time.Time) bool { return t.Weekday() == time.Saturday || t.Weekday() == time.Sunday }

// intArg reads an integer argument the way a model sends one: a JSON number or a numeric
// string. Anything else is 0.
func intArg(v any) int {
	switch n := v.(type) {
	case float64:
		return int(math.Round(max(-1e6, min(1e6, n))))
	case int:
		return n
	case int64:
		return int(max(-1e6, min(1e6, n)))
	case json.Number:
		f, _ := n.Float64()
		return intArg(f)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil || math.IsNaN(f) {
			return 0
		}
		return intArg(f)
	}
	return 0
}

// parseMonthDay reads "MM-DD" (a leading year, "2026-12-25", is ignored). February 29 is
// allowed.
func parseMonthDay(s string) (time.Month, int, bool) {
	parts := strings.Split(strings.TrimSpace(s), "-")
	if len(parts) < 2 {
		return 0, 0, false
	}
	m, err1 := strconv.Atoi(parts[len(parts)-2])
	d, err2 := strconv.Atoi(parts[len(parts)-1])
	if err1 != nil || err2 != nil || m < 1 || m > 12 || d < 1 {
		return 0, 0, false
	}
	// 2024 is a leap year, so this is each month's longest.
	if d > time.Date(2024, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC).Day() {
		return 0, 0, false
	}
	return time.Month(m), d, true
}

// daysUntil counts whole days from at's date to the next m/d (0 when it is today). February 29
// is the next one that exists.
func daysUntil(at time.Time, m time.Month, d int) (int, time.Time) {
	today := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	for y := at.Year(); ; y++ {
		date := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		if date.Month() != m || date.Before(today) { // Feb 29 in a common year normalises to Mar 1
			continue
		}
		return int(date.Sub(today).Hours() / 24), date
	}
}

func untilMessage(n int, date time.Time) string {
	name := date.Format("January 2")
	switch n {
	case 0:
		return "Today is " + name + "."
	case 1:
		return "There's 1 day until " + name + "."
	}
	return fmt.Sprintf("There are %d days until %s.", n, name)
}

func zoneFor(s string) (*time.Location, bool) {
	if tz, ok := places[strings.ToLower(s)]; ok {
		s = tz
	}
	if !strings.Contains(s, "/") && s != "UTC" {
		return nil, false
	}
	loc, err := time.LoadLocation(s)
	return loc, err == nil
}

// zoneName is the IANA name when known ("Local" says nothing), else the abbreviation.
func zoneName(loc *time.Location, at time.Time) string {
	if n := loc.String(); n != "Local" && n != "" {
		return n
	}
	abbr, _ := at.Zone()
	return abbr
}
