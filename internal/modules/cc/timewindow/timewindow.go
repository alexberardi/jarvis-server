// Package timewindow is deterministic time-window validation for phone-call scheduling, ported
// from CC's app/services/time_window.py (docs/cc/08 §3.6; its caller is the phone gateway's
// check-time route, doc 11, D16).
//
// The bounds come from a confirmed brief's constraint envelope:
//
//	Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm
//	Do not book: Wed 7am-5pm (Work); Tue 8am-4pm (Same day Epic training)
//
// A time is available when it lands inside an Acceptable window on that day and inside no
// Do-not-book window. This is interval arithmetic, so it lives in code, not in the model.
package timewindow

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Days are Mon=0 … Sun=6, matched on the first three letters ("Tue", "Tuesday", "tues").
var days = map[string]int{"mon": 0, "tue": 1, "wed": 2, "thu": 3, "fri": 4, "sat": 5, "sun": 6}

var dayNames = [...]string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

const (
	acceptablePrefix = "acceptable times:"
	blockedPrefix    = "do not book:"
)

// Interval is a window on one weekday, in minutes past midnight (end exclusive).
type Interval struct {
	Day, Start, End int
}

func (w Interval) contains(day, minute int) bool {
	return day == w.Day && w.Start <= minute && minute < w.End
}

// Windows are an envelope's parsed lines.
type Windows struct {
	Acceptable []Interval
	Blocked    []Interval
}

func (ws Windows) isOpen(day, minute int) bool {
	return anyContains(ws.Acceptable, day, minute) && !anyContains(ws.Blocked, day, minute)
}

func anyContains(list []Interval, day, minute int) bool {
	for _, w := range list {
		if w.contains(day, minute) {
			return true
		}
	}
	return false
}

// Proposed is a day plus one or more candidate minutes. A bare hour ("6") has no am/pm, so both
// readings are carried and the envelope decides which one the speaker plainly meant.
type Proposed struct {
	Day     int
	Minutes []int
	Label   string
}

// Result is check_time's verdict, in the route's JSON shape. Available is nil when a time was
// detected but could not be judged, so the model carries the turn.
type Result struct {
	TimeDetected      bool    `json:"time_detected"`
	Available         *bool   `json:"available"`
	ProposedLabel     *string `json:"proposed_label"`
	AcceptableSummary *string `json:"acceptable_summary"`
}

// --- time parsing ---

// parseClock is (hour, minute, am/pm) -> minutes past midnight; ok=false if impossible.
func parseClock(hour, minute int, meridiem string) (int, bool) {
	if minute < 0 || minute >= 60 || hour < 0 || hour > 23 {
		return 0, false
	}
	switch meridiem {
	case "am":
		if hour == 12 {
			hour = 0
		} else if hour > 12 {
			return 0, false
		}
	case "pm":
		if hour < 12 {
			hour += 12
		} else if hour > 12 {
			return 0, false
		}
	}
	return hour*60 + minute, true
}

// candidateMinutes is every plausible reading of a clock face: one with an explicit am/pm, or
// with a 13-23 hour; both am and pm for a bare 1-12 hour.
func candidateMinutes(hour, minute int, meridiem string) []int {
	if meridiem != "" || hour > 12 {
		if m, ok := parseClock(hour, minute, meridiem); ok {
			return []int{m}
		}
		return nil
	}
	var out []int
	for _, mer := range []string{"am", "pm"} {
		if m, ok := parseClock(hour, minute, mer); ok && (len(out) == 0 || out[0] != m) {
			out = append(out, m)
		}
	}
	return out
}

var (
	// "3pm", "10 am", "9:30pm", "12", "6", "2 p.m."
	timeRE     = regexp.MustCompile(`(?i)\b(\d{1,2})(?::(\d{2}))?\s*(a\.?m\.?|p\.?m\.?)?`)
	noonRE     = regexp.MustCompile(`(?i)\bnoon\b`)
	midnightRE = regexp.MustCompile(`(?i)\bmidnight\b`)
	dayRE      = regexp.MustCompile(`(?i)\b(mon|tue|wed|thu|fri|sat|sun)[a-z]*\b`)
	faceRE     = regexp.MustCompile(`(?i)^(\d{1,2})(?::(\d{2}))?\s*(a\.?m\.?|p\.?m\.?)?$`)
	labelRE    = regexp.MustCompile(`\s*\([^)]*\)\s*$`)
	// A time RANGE anywhere in an entry ("Tue Aug 5 4-8pm", "Tuesday 4pm to 8pm"), with -, an
	// en or em dash, or "to" between.
	rangeRE = regexp.MustCompile(`(?i)(\d{1,2}(?::\d{2})?\s*(?:a\.?m\.?|p\.?m\.?)?)\s*(?:-|–|—|to)\s*` +
		`(\d{1,2}(?::\d{2})?\s*(?:a\.?m\.?|p\.?m\.?)?)`)
)

func meridiemOf(raw string) string {
	if raw == "" {
		return ""
	}
	if strings.ToLower(raw)[0] == 'a' {
		return "am"
	}
	return "pm"
}

// ParseProposed pulls a proposed day+time out of a callee utterance. It needs BOTH a weekday
// and a time: a lone "3pm" can't be checked against a per-day envelope.
func ParseProposed(utterance string) (Proposed, bool) {
	text := strings.TrimSpace(utterance)
	if text == "" {
		return Proposed{}, false
	}
	dm := dayRE.FindStringSubmatch(text)
	if dm == nil {
		return Proposed{}, false
	}
	day := days[strings.ToLower(dm[1])]
	if noonRE.MatchString(text) {
		return Proposed{day, []int{12 * 60}, dayNames[day] + " at noon"}, true
	}
	if midnightRE.MatchString(text) {
		return Proposed{day, []int{0}, dayNames[day] + " at midnight"}, true
	}
	for _, m := range timeRE.FindAllStringSubmatch(text, -1) {
		hour, _ := strconv.Atoi(m[1])
		minute := 0
		if m[2] != "" {
			minute, _ = strconv.Atoi(m[2])
		}
		mer := meridiemOf(m[3])
		cands := candidateMinutes(hour, minute, mer)
		if len(cands) == 0 {
			continue
		}
		return Proposed{day, cands, formatLabel(day, hour, minute, mer)}, true
	}
	return Proposed{}, false
}

func formatLabel(day, hour, minute int, meridiem string) string {
	clock := strconv.Itoa(hour)
	if minute != 0 {
		clock = fmt.Sprintf("%d:%02d", hour, minute)
	}
	suffix := ""
	if meridiem != "" {
		suffix = " " + meridiem
	}
	return strings.TrimSpace(dayNames[day] + " at " + clock + suffix)
}

// --- window parsing ---

type face struct {
	hour, minute int
	meridiem     string
}

func parseFace(s string) (face, bool) {
	m := faceRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return face{}, false
	}
	f := face{meridiem: meridiemOf(m[3])}
	f.hour, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		f.minute, _ = strconv.Atoi(m[2])
	}
	return f, true
}

// parseRange reads "4-8pm" / "9am-8pm" as (start, end) minutes. A start with no am/pm inherits
// the end's, so "4-8pm" is 4pm-8pm; an end with none is pm.
func parseRange(text string) (int, int, bool) {
	a, b, found := strings.Cut(text, "-")
	if !found {
		return 0, 0, false
	}
	sf, ok1 := parseFace(a)
	ef, ok2 := parseFace(b)
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	endMer := ef.meridiem
	if endMer == "" {
		endMer = "pm"
	}
	end, okEnd := parseClock(ef.hour, ef.minute, endMer)
	startMer := sf.meridiem
	if startMer == "" {
		startMer = ef.meridiem
	}
	start, okStart := parseClock(sf.hour, sf.minute, startMer)
	if !okStart || !okEnd || start >= end {
		return 0, 0, false
	}
	return start, end, true
}

// parseEntries reads "Tue 4-8pm; Wed 5-8pm; ..." into intervals. Bad entries drop silently.
// The day must lead; the range may sit anywhere after it.
func parseEntries(body string) []Interval {
	var out []Interval
	for _, chunk := range strings.Split(body, ";") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		// Strip a trailing "(Work)"-style label that Do-not-book lines carry.
		chunk = strings.TrimSpace(labelRE.ReplaceAllString(chunk, ""))
		loc := dayRE.FindStringSubmatchIndex(chunk)
		if loc == nil || loc[0] != 0 {
			continue
		}
		day := days[strings.ToLower(chunk[loc[2]:loc[3]])]
		rm := rangeRE.FindStringSubmatch(chunk[loc[1]:])
		if rm == nil {
			continue
		}
		start, end, ok := parseRange(strings.TrimSpace(rm[1]) + "-" + strings.TrimSpace(rm[2]))
		if !ok {
			continue
		}
		out = append(out, Interval{day, start, end})
	}
	return out
}

// ParseWindows parses an envelope's Acceptable / Do-not-book lines.
func ParseWindows(envelope string) Windows {
	var ws Windows
	for _, line := range strings.Split(envelope, "\n") {
		line = strings.TrimSpace(line)
		low := strings.ToLower(line)
		switch {
		case strings.HasPrefix(low, acceptablePrefix):
			ws.Acceptable = append(ws.Acceptable, parseEntries(line[len(acceptablePrefix):])...)
		case strings.HasPrefix(low, blockedPrefix):
			ws.Blocked = append(ws.Blocked, parseEntries(line[len(blockedPrefix):])...)
		}
	}
	return ws
}

func acceptableSummary(envelope string) *string {
	for _, line := range strings.Split(envelope, "\n") {
		if l := strings.TrimSpace(line); strings.HasPrefix(strings.ToLower(l), acceptablePrefix) {
			return &l
		}
	}
	return nil
}

// --- check ---

// Check answers whether the time proposed in utterance is available under envelope.
// Available is set only when a day+time was found and either a Do-not-book window vetoes it
// or real acceptable windows exist to judge it against.
func Check(envelope, utterance string) Result {
	p, ok := ParseProposed(utterance)
	if !ok {
		return Result{}
	}
	ws := ParseWindows(envelope)
	res := Result{TimeDetected: true, ProposedLabel: &p.Label, AcceptableSummary: acceptableSummary(envelope)}

	// A slot in an explicit Do-not-book window is unavailable, even when the Acceptable line
	// didn't parse: a blocked slot must never fall through to the model. Any reading of an
	// ambiguous bare hour landing blocked vetoes it.
	for _, m := range p.Minutes {
		if anyContains(ws.Blocked, p.Day, m) {
			res.Available = new(bool)
			return res
		}
	}
	if len(ws.Acceptable) == 0 {
		return res // nothing to judge against: the model carries the turn
	}
	// Any candidate reading that lands open wins ("6" on a 5-8pm day is 6pm).
	open := false
	for _, m := range p.Minutes {
		if ws.isOpen(p.Day, m) {
			open = true
			break
		}
	}
	res.Available = &open
	return res
}
