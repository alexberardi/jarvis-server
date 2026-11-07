package timewindow

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// --- offers in speech ---

// CheckAll judges every time a callee's utterance mentions ("Friday at 7 is booked; we have
// 6:15 or 7:30" is three proposals), where Check judges only the first. Each time is judged on
// the weekday named nearest to it. With no weekday, times are judged on the envelope's day when
// it names only one, unless the utterance names a day that can't be placed ("this weekend").
// Only times with a verdict are returned, once per label (none when nothing can be judged).
// Spoken forms ("seven thirty", "730") are phone's to normalise first (spokenClock).
func CheckAll(envelope, utterance string) []Result {
	text := clockWords(utterance)
	offers := offeredTimes(text)
	if len(offers) == 0 {
		return nil
	}
	ws := ParseWindows(envelope)
	mentions := dayMentions(text)
	sole, hasSole := soleDay(ws.Acceptable)
	if unplacedDayRE.MatchString(text) {
		hasSole = false
	}
	summary := acceptableSummary(envelope)
	var out []Result
	seen := map[string]bool{}
	for _, o := range offers {
		day, ok := nearestDay(mentions, o)
		if !ok {
			if !hasSole {
				continue
			}
			day = sole
		}
		label := formatLabel(day, o.hour, o.minute, o.mer)
		if seen[label] {
			continue
		}
		res := Result{TimeDetected: true, ProposedLabel: &label, AcceptableSummary: summary}
		blocked, open := false, false
		for _, c := range candidateMinutes(o.hour, o.minute, o.mer) {
			blocked = blocked || anyContains(ws.Blocked, day, c)
			open = open || ws.isOpen(day, c)
		}
		switch {
		case blocked:
			res.Available = new(bool)
		case len(ws.Acceptable) > 0:
			res.Available = &open
		default:
			continue
		}
		seen[label] = true
		out = append(out, res)
	}
	return out
}

// MentionsTime reports whether CheckAll would read a clock time in the utterance.
func MentionsTime(utterance string) bool {
	return len(offeredTimes(clockWords(utterance))) > 0
}

// soleDay is the one day every interval is on.
func soleDay(ivs []Interval) (int, bool) {
	if len(ivs) == 0 {
		return 0, false
	}
	for _, iv := range ivs[1:] {
		if iv.Day != ivs[0].Day {
			return 0, false
		}
	}
	return ivs[0].Day, true
}

type offer struct {
	start, end   int
	hour, minute int
	mer          string
}

var (
	twelveRE = regexp.MustCompile(`(?i)\b12\s+(noon|midnight)\b`)
	// offerClockRE is a clock face in speech; its edges are checked in offeredTimes (RE2 has no
	// lookaround): "7", "7:30", "7pm", "7 p.m.", "7 o'clock", "7ish", "7-ish".
	offerClockRE = regexp.MustCompile(`(?i)(\d{1,2})(?::(\d{2}))?(?:\s*(a\.?m\b\.?|p\.?m\b\.?)|\s*(o'?clock)\b|[\s-]?(ish)\b)?`)
	// A bare number ("8") is a time only after one of these ("at 8", "how about 8", "can do 8").
	// "until 9" is when they're busy or close, not an offer; "for 8" and "I have 8" are counts.
	timePreps = map[string]bool{"at": true, "around": true, "about": true, "after": true, "before": true, "by": true, "say": true, "do": true}
	countRE   = regexp.MustCompile(`(?i)^\s*(?:people|persons?|guests?|adults?|kids|children|of us|seats?|tables?|minutes?|mins?|hours?|hrs?|days?|weeks?|months?|years?|dollars?|bucks|percent)\b`)
	streetRE  = regexp.MustCompile(`^\s+(?:[A-Z][a-z]*\.?\s+){0,2}(?i:street|st|avenue|ave|road|rd|boulevard|blvd|lane|ln|drive|dr|way|court|ct|place|pl|suite)\b`)
	// offerDayRE is a weekday in speech. Unlike dayRE it is a whole word: "friend" isn't Friday.
	offerDayRE = regexp.MustCompile(`(?i)\b(?:mon(?:day)?|tue(?:s|sday)?|wed(?:s|nesday)?|thu(?:r|rs|rsday)?|fri(?:day)?|sat(?:urday)?|sun(?:day)?)s?\b`)
	// unplacedDayRE is a day CheckAll can't put on a weekday, so the envelope's day isn't assumed.
	unplacedDayRE = regexp.MustCompile(`(?i)\b(?:(?:weekends?|weeks?|today|tonight|tomorrow)\b|\d{1,2}(?:st|nd|rd|th)\b|(?:jan|feb|mar|apr|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s+\d)`)
)

// clockWords writes noon and midnight as clocks.
func clockWords(s string) string {
	s = twelveRE.ReplaceAllString(strings.TrimSpace(s), "$1")
	return midnightRE.ReplaceAllString(noonRE.ReplaceAllString(s, "12 pm"), "12 am")
}

// offeredTimes is every clock time in text. A number inside another (an address, a phone
// number, a price, an ID, a date) is none; a bare number needs a time word before it.
func offeredTimes(text string) []offer {
	var out []offer
	lastEnd := -10
	for _, m := range offerClockRE.FindAllStringSubmatchIndex(text, -1) {
		start, end := m[0], m[1]
		if start > 0 {
			c := text[start-1]
			if isAlnum(c) || strings.IndexByte("$#:/", c) >= 0 || (c == '.' || c == ',') && start > 1 && isDigit(text[start-2]) {
				continue
			}
		}
		if end < len(text) {
			c := text[end]
			if isAlnum(c) || strings.IndexByte(":/%", c) >= 0 || (c == '.' || c == ',') && end+1 < len(text) && isDigit(text[end+1]) {
				continue
			}
		}
		if streetRE.MatchString(text[end:]) {
			continue
		}
		hour, _ := strconv.Atoi(text[m[2]:m[3]])
		minute := 0
		if m[4] >= 0 {
			minute, _ = strconv.Atoi(text[m[4]:m[5]])
		}
		mer := ""
		if m[6] >= 0 {
			mer = meridiemOf(text[m[6]:m[7]])
		}
		if m[4] < 0 && mer == "" && m[8] < 0 && m[10] < 0 {
			prev := lastWord(text[:start])
			if !(timePreps[prev] || prev == "or" && start-lastEnd <= 5) || countRE.MatchString(text[end:]) {
				continue
			}
		}
		if len(candidateMinutes(hour, minute, mer)) == 0 {
			continue
		}
		out = append(out, offer{start, end, hour, minute, mer})
		lastEnd = end
	}
	return out
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isAlnum(c byte) bool { return isDigit(c) || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// lastWord is the lower-cased word text ends with, ignoring trailing spaces.
func lastWord(text string) string {
	text = strings.TrimRight(text, " ")
	i := len(text)
	for i > 0 && (isAlnum(text[i-1]) || text[i-1] == '\'') {
		i--
	}
	return strings.ToLower(text[i:])
}

type dayMention struct{ start, end, day int }

func dayMentions(text string) []dayMention {
	var out []dayMention
	for _, m := range offerDayRE.FindAllStringIndex(text, -1) {
		w := text[m[0]:m[1]]
		// "we sat", "the sun", "they wed": a lower-case three-letter word is English, not a day.
		if w == "sat" || w == "sun" || w == "wed" || w == "mon" {
			continue
		}
		out = append(out, dayMention{m[0], m[1], days[strings.ToLower(w[:3])]})
	}
	return out
}

// nearestDay is the weekday mentioned closest to an offer ("Friday is full; 7:30 on Saturday?"
// is Saturday's 7:30); a tie goes to the one before it.
func nearestDay(ms []dayMention, o offer) (int, bool) {
	best, bestDist := 0, -1
	for _, m := range ms {
		dist := o.start - m.end
		if m.start >= o.end {
			dist = m.start - o.end
		}
		if bestDist < 0 || dist < bestDist {
			best, bestDist = m.day, dist
		}
	}
	return best, bestDist >= 0
}

// --- relative days ---

var (
	relativeDayRE = regexp.MustCompile(`(?i)\b(?:the\s+)?day\s+after\s+tomorrow\b|\b(?:today|tonight|tomorrow)\b`)
	// calDateRE is "November 13", "Nov. 13th, 2026" or "the 13th" ("may" is a verb; "May" a month).
	calDateRE       = regexp.MustCompile(`\b(?:((?i:jan|feb|mar|apr|jun|jul|aug|sep|oct|nov|dec)[a-zA-Z]*|May)\.?\s+(\d{1,2})(?i:st|nd|rd|th)?(?:,?\s+(\d{4}))?|(?i:the)\s+(\d{1,2})(?i:st|nd|rd|th))\b`)
	weekdayBeforeRE = regexp.MustCompile(`(?i)\b(?:mon|tue|wed|thu|fri|sat|sun)[a-z]*\.?,?\s*$`)
	months          = map[string]time.Month{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
)

func weekdayName(t time.Time) string { return dayNames[(int(t.Weekday())+6)%7] }

// ResolveRelativeDays names the weekday for "today", "tonight", "tomorrow", "the day after
// tomorrow" and a calendar date ("November 13", "the 13th") in an envelope or an utterance: a
// drafted brief says "Acceptable times: Tomorrow at 7 p.m.", and the windows are weekdays. A
// date after a weekday ("Friday the 13th") is dropped: the weekday names it. now is the
// household's local time when the call starts.
// shortcut: a plan made before midnight and called after it shifts by a day; plans expire in
// 20 minutes, so resolve at plan time if that ever matters.
func ResolveRelativeDays(text string, now time.Time) string {
	text = relativeDayRE.ReplaceAllStringFunc(text, func(w string) string {
		switch lw := strings.ToLower(w); {
		case strings.Contains(lw, "after"):
			return weekdayName(now.AddDate(0, 0, 2))
		case lw == "tomorrow":
			return weekdayName(now.AddDate(0, 0, 1))
		}
		return weekdayName(now)
	})
	var b strings.Builder
	last := 0
	for _, m := range calDateRE.FindAllStringSubmatchIndex(text, -1) {
		b.WriteString(text[last:m[0]])
		last = m[1]
		if weekdayBeforeRE.MatchString(text[:m[0]]) {
			if strings.HasSuffix(text[:m[0]], " ") && strings.HasPrefix(text[last:], " ") {
				last++
			}
			continue
		}
		if d, ok := dateOf(text, m, now); ok {
			b.WriteString(weekdayName(d))
		} else {
			b.WriteString(text[m[0]:m[1]])
		}
	}
	b.WriteString(text[last:])
	return b.String()
}

// dateOf is the next date (from now's day) a calDateRE match names.
func dateOf(text string, m []int, now time.Time) (time.Time, bool) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if m[2] >= 0 {
		mon := months[strings.ToLower(text[m[2]:m[2]+3])]
		day, _ := strconv.Atoi(text[m[4]:m[5]])
		year := now.Year()
		if m[6] >= 0 {
			year, _ = strconv.Atoi(text[m[6]:m[7]])
		}
		t := time.Date(year, mon, day, 0, 0, 0, 0, now.Location())
		if m[6] < 0 && t.Before(today) {
			t = t.AddDate(1, 0, 0)
		}
		return t, t.Day() == day
	}
	// "the 31st" is the next month that has one, from this one.
	day, _ := strconv.Atoi(text[m[8]:m[9]])
	for i := range 3 {
		t := time.Date(now.Year(), now.Month()+time.Month(i), day, 0, 0, 0, 0, now.Location())
		if t.Day() == day && !t.Before(today) {
			return t, true
		}
	}
	return time.Time{}, false
}
