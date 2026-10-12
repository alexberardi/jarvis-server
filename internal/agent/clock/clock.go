// Package clock is the node's get_current_time tool, built in so every Go node answers "what
// time is it?" from its own clock. Without it the model falls back to the prompt's ambient
// block, which is rounded down to the quarter hour ("12:45" at 00:55).
//
// The name, parameter and output follow the Python node's commands/timezone_command.py, so
// command-center can't tell them apart. Differences: a location is an IANA zone name or
// one of a few common places (the model is asked for IANA names), and an unknown location
// answers local time with a note instead of failing (docs/EXTERNAL-CHANGES.md, A10 F20).
package clock

import (
	"context"
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

var spec = node.Spec{
	Name: ToolName,
	Description: "Current time/date, locally or in a city/state/country. Use for 'what time is it?', " +
		"'what day is it?', 'what time is it in Tokyo?'. Not for weather — use get_weather.",
	Params: []node.Param{{Name: "location", Type: "string",
		Description: "Omit for local time. Otherwise an IANA time zone (e.g. 'Asia/Tokyo', 'Europe/London') or a city/country name."}},
	Keywords: []string{"time", "date", "day", "clock", "timezone"},
	Examples: []node.Example{
		{VoiceCommand: "what time is it", Params: map[string]any{}, Primary: true},
		{VoiceCommand: "what time is it in Tokyo", Params: map[string]any{"location": "Asia/Tokyo"}},
		{VoiceCommand: "what day is it today", Params: map[string]any{}},
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
	out := map[string]any{
		"success":      true,
		"location":     label,
		"timezone":     zoneName(loc, at),
		"current_time": at.Format("3:04 PM"),
		"current_date": at.Format("Monday, January 02"),
		"iso_datetime": at.Format(time.RFC3339),
	}
	if label == "local" {
		out["message"] = "It's " + at.Format("3:04 PM") + "."
	} else {
		out["message"] = "It's " + at.Format("3:04 PM") + " in " + label + "."
	}
	if note != "" {
		out["note"] = note
	}
	return out, true
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
