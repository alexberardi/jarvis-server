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
// Grace is how far either side of a single acceptable time ("Friday at 7pm") still counts as
// it, in minutes; a range ("5-8pm") is exact (decision 2026-11-06-call-time-grace).
const Grace = 30

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
	// leadRE is the filler a drafted entry can start with before its day ("this Friday at 7pm").
	leadRE = regexp.MustCompile(`(?i)^(?:this|next|on|coming)\s+`)
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
// The day (or days: "Mon–Fri", "this weekend") must lead; the times may sit anywhere after.
// An entry of days alone takes the next entry's times ("Friday or Saturday at 7pm").
func parseEntries(body string) []Interval {
	var out []Interval
	var pending []int
	for _, chunk := range strings.Split(entrySepRE.ReplaceAllString(body, "; $1"), ";") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		// Strip a trailing "(Work)"-style label that Do-not-book lines carry.
		chunk = strings.TrimSpace(labelRE.ReplaceAllString(chunk, ""))
		chunk = leadRE.ReplaceAllString(chunk, "")
		ds, rest, ok := leadDays(chunk)
		if !ok {
			pending = nil
			continue
		}
		ds = append(pending, ds...)
		pending = nil
		rest = strings.TrimSpace(dateRE.ReplaceAllString(rest, " "))
		if strings.Trim(rest, " ,.") == "" {
			pending = ds
			continue
		}
		for _, sp := range entrySpans(rest) {
			for _, d := range ds {
				out = append(out, Interval{d, sp[0], sp[1]})
			}
		}
	}
	return out
}

var (
	// entrySepRE splits "Fri 5-8pm, Sat 6-9pm" and "Friday or Saturday" into entries.
	entrySepRE = regexp.MustCompile(`(?i)\s*(?:,|/|&|\band\b|\bor\b)\s*((?:mon|tue|wed|thu|fri|sat|sun)[a-z]*\b)`)
	dayRangeRE = regexp.MustCompile(`(?i)^\s*(?:-|–|—|to|through|thru)\s*`)
	groupRE    = regexp.MustCompile(`(?i)^(?:(?:this|the)\s+)?(weekends?|weekdays?|every\s*day|daily)\b`)
	// dateRE is a calendar date inside an entry ("Tue Aug 5", "November 13, 2026", "the 13th",
	// "11/13"): its numbers are not times; the weekday carries the day.
	dateRE = regexp.MustCompile(`(?i)\b(?:(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s+\d{1,2}(?:st|nd|rd|th)?\b(?:,?\s+\d{4}\b)?|(?:the\s+)?\d{1,2}(?:st|nd|rd|th)\b|\d{1,2}/\d{1,2}(?:/\d{2,4})?\b)`)
	// boundRE is an open-ended entry: "after 6pm", "from 6pm", "before 2pm", "until 2pm".
	boundRE = regexp.MustCompile(`(?i)\b(after|from|before|until|till|til)\s+(\d{1,2}(?::\d{2})?\s*(?:a\.?m\.?|p\.?m\.?)?)`)
	// laterRE is "6pm or later" / "2pm or earlier".
	laterRE = regexp.MustCompile(`(?i)(\d{1,2}(?::\d{2})?\s*(?:a\.?m\.?|p\.?m\.?)?)\s*or\s+(later|after|earlier|before)\b`)
)

// leadDays reads the day(s) an entry starts with: "Fri", "Mon–Fri", "this weekend",
// "weekdays".
func leadDays(chunk string) ([]int, string, bool) {
	if g := groupRE.FindStringSubmatch(chunk); g != nil {
		rest := chunk[len(g[0]):]
		switch w := strings.ToLower(g[1]); {
		case strings.HasPrefix(w, "weekend"):
			return []int{5, 6}, rest, true
		case strings.HasPrefix(w, "weekday"):
			return []int{0, 1, 2, 3, 4}, rest, true
		default:
			return []int{0, 1, 2, 3, 4, 5, 6}, rest, true
		}
	}
	loc := dayRE.FindStringSubmatchIndex(chunk)
	if loc == nil || loc[0] != 0 {
		return nil, "", false
	}
	first := days[strings.ToLower(chunk[loc[2]:loc[3]])]
	rest := chunk[loc[1]:]
	if sep := dayRangeRE.FindString(rest); sep != "" {
		r := rest[len(sep):]
		if l2 := dayRE.FindStringSubmatchIndex(r); l2 != nil && l2[0] == 0 {
			last := days[strings.ToLower(r[l2[2]:l2[3]])]
			var ds []int
			for d := first; ; d = (d + 1) % 7 {
				ds = append(ds, d)
				if d == last {
					break
				}
			}
			return ds, r[l2[1]:], true
		}
	}
	return []int{first}, rest, true
}

// entrySpans is an entry's time spans after its day: a range ("4-8pm") is exact; "after 6pm"
// runs to midnight and "before 2pm" from it; a single time ("at 7pm", a drafted brief's exact
// slot) gets Grace either side (7:15 for a 7pm table is fine). A bare hour gives every reading.
func entrySpans(rest string) [][2]int {
	if rm := rangeRE.FindStringSubmatch(rest); rm != nil {
		start, end, ok := parseRange(strings.TrimSpace(rm[1]) + "-" + strings.TrimSpace(rm[2]))
		if !ok {
			return nil
		}
		return [][2]int{{start, end}}
	}
	after, faceText := false, ""
	if m := boundRE.FindStringSubmatch(rest); m != nil {
		w := strings.ToLower(m[1])
		after, faceText = w == "after" || w == "from", m[2]
	} else if m := laterRE.FindStringSubmatch(rest); m != nil {
		w := strings.ToLower(m[2])
		after, faceText = w == "later" || w == "after", m[1]
	}
	if faceText != "" {
		f, ok := parseFace(faceText)
		if !ok {
			return nil
		}
		var out [][2]int
		for _, m := range candidateMinutes(f.hour, f.minute, f.meridiem) {
			if after {
				out = append(out, [2]int{m, 24 * 60})
			} else {
				out = append(out, [2]int{0, m})
			}
		}
		return out
	}
	var out [][2]int
	for i, tm := range timeRE.FindAllStringSubmatch(rest, -1) {
		// After the first, only clock-shaped times count ("at 7pm or 8pm"; "for 2 people" isn't one).
		if i > 0 && tm[2] == "" && tm[3] == "" {
			continue
		}
		hour, _ := strconv.Atoi(tm[1])
		minute := 0
		if tm[2] != "" {
			minute, _ = strconv.Atoi(tm[2])
		}
		for _, m := range candidateMinutes(hour, minute, meridiemOf(tm[3])) {
			out = append(out, [2]int{m - Grace, m + Grace + 1})
		}
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

// acceptableSummary is the envelope's Acceptable line; several are joined into one.
func acceptableSummary(envelope string) *string {
	var sum string
	for _, line := range strings.Split(envelope, "\n") {
		l := strings.TrimSpace(line)
		switch {
		case !strings.HasPrefix(strings.ToLower(l), acceptablePrefix):
		case sum == "":
			sum = l
		default:
			sum = strings.TrimRight(sum, " .;") + "; " + strings.TrimSpace(l[len(acceptablePrefix):])
		}
	}
	if sum == "" {
		return nil
	}
	return &sum
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
