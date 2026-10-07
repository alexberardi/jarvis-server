// Package timewindow is the deterministic time-window check for phone-call scheduling
// (legacy app/services/time_window.py, docs/cc/08 and 11 §3.7).
//
// The live model cannot reliably decide whether a proposed clock time falls inside an
// availability window, so the call loop asks this package per turn and states the verdict
// instead of computing it. The bounds are the confirmed brief's constraint envelope:
//
//	Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm
//	Do not book: Wed 7am-5pm (Work); Tue 8am-4pm (Same day Epic training)
//
// A time is available when it lands inside an Acceptable window on that day and inside no
// Do-not-book window. Everything here is a pure function of its inputs: no clock, no I/O.
package timewindow

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Mon=0 … Sun=6, matched on the first three letters so "Tue"/"Tuesday"/"tues" all resolve.
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

// Contains reports whether minute on day falls inside the window.
func (w Interval) Contains(day, minute int) bool {
	return day == w.Day && w.Start <= minute && minute < w.End
}

// Windows is a parsed envelope.
type Windows struct {
	Acceptable []Interval
	Blocked    []Interval
}

// IsOpen: inside some acceptable window and no blocked one.
func (ws Windows) IsOpen(day, minute int) bool {
	in := false
	for _, w := range ws.Acceptable {
		if w.Contains(day, minute) {
			in = true
			break
		}
	}
	if !in {
		return false
	}
	for _, w := range ws.Blocked {
		if w.Contains(day, minute) {
			return false
		}
	}
	return true
}

// Proposed is a day plus one or more candidate minutes. A bare hour ("6") has no am/pm, so
// both readings are carried and the envelope decides which one the speaker meant.
type Proposed struct {
	Day     int
	Minutes []int
	Label   string
}

// Result is check_time's CheckResult. Available is nil when a time was detected but could
// not be validated; ProposedLabel and AcceptableSummary are nil when Python had None.
type Result struct {
	TimeDetected      bool
	Available         *bool
	ProposedLabel     *string
	AcceptableSummary *string
}

// JSON is the legacy check-time route's response body (None → null).
func (r Result) JSON() map[string]any {
	out := map[string]any{"time_detected": r.TimeDetected, "available": nil, "proposed_label": nil, "acceptable_summary": nil}
	if r.Available != nil {
		out["available"] = *r.Available
	}
	if r.ProposedLabel != nil {
		out["proposed_label"] = *r.ProposedLabel
	}
	if r.AcceptableSummary != nil {
		out["acceptable_summary"] = *r.AcceptableSummary
	}
	return out
}

// --- time parsing ---

// parseClock is (hour, minute, am/pm) → minutes past midnight; ok=false if impossible.
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

// candidateMinutes is every plausible reading of a clock face: one with an explicit am/pm
// or a 13-23 hour, else the am and pm readings (deduplicated, in that order).
func candidateMinutes(hour, minute int, meridiem string) []int {
	if meridiem != "" || hour > 12 {
		if m, ok := parseClock(hour, minute, meridiem); ok {
			return []int{m}
		}
		return nil
	}
	var out []int
	for _, mer := range []string{"am", "pm"} {
		if m, ok := parseClock(hour, minute, mer); ok && !containsInt(out, m) {
			out = append(out, m)
		}
	}
	return out
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

var (
	// "3pm", "10 am", "9:30pm", "12", "6", "2 p.m."
	timeRE     = regexp.MustCompile(`(?i)\b(\d{1,2})(?::(\d{2}))?\s*(a\.?m\.?|p\.?m\.?)?`)
	noonRE     = regexp.MustCompile(`(?i)\bnoon\b`)
	midnightRE = regexp.MustCompile(`(?i)\bmidnight\b`)
	dayRE      = regexp.MustCompile(`(?i)\b(mon|tue|wed|thu|fri|sat|sun)[a-z]*\b`)
	faceRE     = regexp.MustCompile(`(?i)\A(\d{1,2})(?::(\d{2}))?\s*(a\.?m\.?|p\.?m\.?)?\z`)
	// A time range anywhere in an entry: "-", en/em dash, or "to".
	rangeRE = regexp.MustCompile(`(?i)(\d{1,2}(?::\d{2})?\s*(?:a\.?m\.?|p\.?m\.?)?)\s*(?:-|–|—|to)\s*` +
		`(\d{1,2}(?::\d{2})?\s*(?:a\.?m\.?|p\.?m\.?)?)`)
	trailingLabelRE = regexp.MustCompile(`\s*\([^)]*\)\s*$`)
)

func firstDay(text string) (int, bool) {
	m := dayRE.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}
	return days[strings.ToLower(m[1])], true
}

func meridiemOf(raw string) string {
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(raw), "a") {
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
	day, ok := firstDay(text)
	if !ok {
		return Proposed{}, false
	}
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
	mer          string
}

func parseFace(s string) (face, bool) {
	m := faceRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return face{}, false
	}
	f := face{mer: meridiemOf(m[3])}
	f.hour, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		f.minute, _ = strconv.Atoi(m[2])
	}
	return f, true
}

// parseRange is "4-8pm" / "9am-8pm" → (start, end). A start with no am/pm inherits the
// end's; an end with none is pm.
func parseRange(text string) (int, int, bool) {
	startRaw, endRaw, ok := strings.Cut(text, "-")
	if !ok {
		return 0, 0, false
	}
	sf, ok1 := parseFace(strings.TrimSpace(startRaw))
	ef, ok2 := parseFace(strings.TrimSpace(endRaw))
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	endMer := ef.mer
	if endMer == "" {
		endMer = "pm"
	}
	end, okE := parseClock(ef.hour, ef.minute, endMer)
	startMer := sf.mer
	if startMer == "" {
		startMer = ef.mer
	}
	start, okS := parseClock(sf.hour, sf.minute, startMer)
	if !okS || !okE || start >= end {
		return 0, 0, false
	}
	return start, end, true
}

// parseEntries is "Tue 4-8pm; Wed 5-8pm; ..." → intervals; bad entries drop silently. The day
// must lead the entry; the range may sit anywhere after it ("Tue Aug 5 4-8pm").
func parseEntries(body string) []Interval {
	var out []Interval
	for _, chunk := range strings.Split(body, ";") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		chunk = strings.TrimSpace(trailingLabelRE.ReplaceAllString(chunk, ""))
		loc := dayRE.FindStringSubmatchIndex(chunk)
		if loc == nil || loc[0] != 0 {
			continue
		}
		day := days[strings.ToLower(chunk[loc[2]:loc[3]])]
		r := rangeRE.FindStringSubmatch(chunk[loc[1]:])
		if r == nil {
			continue
		}
		start, end, ok := parseRange(strings.TrimSpace(r[1]) + "-" + strings.TrimSpace(r[2]))
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
	for _, line := range splitLines(envelope) {
		s := strings.TrimSpace(line)
		low := strings.ToLower(s)
		switch {
		case strings.HasPrefix(low, acceptablePrefix):
			ws.Acceptable = append(ws.Acceptable, parseEntries(s[len(acceptablePrefix):])...)
		case strings.HasPrefix(low, blockedPrefix):
			ws.Blocked = append(ws.Blocked, parseEntries(s[len(blockedPrefix):])...)
		}
	}
	return ws
}

// splitLines is Python's str.splitlines (no keepends).
func splitLines(s string) []string {
	var out []string
	start := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, string(rs[start:i]))
			if rs[i] == '\r' && i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

// --- check ---

func acceptableSummary(envelope string) *string {
	for _, line := range splitLines(envelope) {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(s), acceptablePrefix) {
			return &s
		}
	}
	return nil
}

// Check is check_time: is the time proposed in utterance available under envelope?
// Available is set only when a day+time was found and either a Do-not-book window vetoes it
// or the envelope has real acceptable windows; otherwise nil, so the model carries the turn.
func Check(envelope, utterance string) Result {
	p, ok := ParseProposed(utterance)
	if !ok {
		return Result{}
	}
	ws := ParseWindows(envelope)
	label := p.Label
	res := Result{TimeDetected: true, ProposedLabel: &label, AcceptableSummary: acceptableSummary(envelope)}

	// A slot inside an explicit Do-not-book window is unavailable even when the Acceptable
	// line didn't parse: a blocked slot must never fall through to the model.
	for _, b := range ws.Blocked {
		for _, m := range p.Minutes {
			if b.Contains(p.Day, m) {
				f := false
				res.Available = &f
				return res
			}
		}
	}
	if len(ws.Acceptable) == 0 {
		return res
	}
	avail := false
	for _, m := range p.Minutes {
		if ws.IsOpen(p.Day, m) {
			avail = true
			break
		}
	}
	res.Available = &avail
	return res
}
