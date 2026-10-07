package dates

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	llmdates "github.com/alexberardi/jarvis-server/internal/modules/llm/dates"
)

// Vocabulary is the one shared date-key vocabulary (D40 03.Q9): the DT_KEYS prompt line, the
// llm module's regex matcher and this resolver all use it. Every key resolves here.
var Vocabulary = llmdates.Vocabulary

// NormalizeKey is normalize_date_key: strip, lowercase, whitespace runs and ":" → "_".
func NormalizeKey(raw string) string { return parse.NormalizeDateKey(raw) }

// flatEntry is one key of flatten_date_context: a single date/instant or a list (buckets).
type flatEntry struct {
	key    string
	values []civil
	list   bool
}

// flatMap is the flattened context, in flatten_date_context's insertion order (a later
// duplicate key replaces the value in place, as a Python dict does).
type flatMap struct {
	entries []*flatEntry
	index   map[string]*flatEntry
}

func (f *flatMap) set(key string, list bool, values ...civil) {
	if e, ok := f.index[key]; ok {
		e.values, e.list = values, list
		return
	}
	e := &flatEntry{key: key, values: values, list: list}
	f.entries = append(f.entries, e)
	f.index[key] = e
}

func (f *flatMap) get(key string) *flatEntry { return f.index[key] }

// buildFlat is flatten_date_context over the corrected context, plus the gap keys.
func (c *Context) buildFlat() *flatMap {
	f := &flatMap{index: map[string]*flatEntry{}}
	t := c.today()
	yesterday := t.addDays(-1)
	f.set("today", false, t)
	f.set("tomorrow", false, t.addDays(1))
	f.set("yesterday", false, yesterday)
	f.set("last_night", false, yesterday.at(19, 0)) // relative_dates.last_night.datetime
	f.set("day_after_tomorrow", false, t.addDays(2))
	f.set("day_before_yesterday", false, t.addDays(-2))

	thisWE, nextWE, lastWE := c.weekendDays()
	f.set("this_weekend", true, thisWE[0], thisWE[1])
	f.set("next_weekend", true, nextWE[0], nextWE[1])
	f.set("last_weekend", true, lastWE[0], lastWE[1])
	ws := c.weekStart()
	week := func(start civil) []civil {
		out := make([]civil, 7)
		for i := range out {
			out[i] = start.addDays(i)
		}
		return out
	}
	f.set("this_week", true, week(ws)...)
	f.set("next_week", true, week(ws.addDays(7))...)
	f.set("last_week", true, week(ws.addDays(-7))...)
	y, m := t.y, t.m
	f.set("this_month", true, firstOfMonth(y, m), lastOfMonth(y, m))
	f.set("next_month", true, firstOfMonth(y, m+1), lastOfMonth(y, m+1))
	f.set("last_month", true, firstOfMonth(y, m-1), lastOfMonth(y, m-1))
	f.set("this_year", true, civil{y: y, m: 1, d: 1}, civil{y: y, m: 12, d: 31})
	f.set("next_year", true, civil{y: y + 1, m: 1, d: 1}, civil{y: y + 1, m: 12, d: 31})
	f.set("last_year", true, civil{y: y - 1, m: 1, d: 1}, civil{y: y - 1, m: 12, d: 31})

	for _, w := range c.weekdayKeys() {
		f.set(w.key, false, w.when)
	}
	for _, d := range week(ws) {
		f.set("this_"+strings.ToLower(weekNames[mondayIndex(d.weekday())]), false, d)
	}
	for _, e := range c.timeExpressions() {
		f.set(NormalizeKey(e.key), false, e.when)
	}
	for _, e := range mealKeys {
		f.set(e.key, false, t.at(e.when.hh, e.when.min))
	}
	return f
}

// Lookup returns a key's flat value: one instant, or the bucket's instants. ok=false for a
// key the context does not know (in_* keys are not in the table; Resolve handles them).
func (c *Context) Lookup(key string) ([]string, bool) {
	e := c.flat.get(key)
	if e == nil {
		return nil, false
	}
	return c.render(e.values), true
}

func (c *Context) render(vs []civil) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.instant(c.loc)
	}
	return out
}

var relativeRE = regexp.MustCompile(`^in_([0-9]+)_(minutes|hours|days)(?:_([0-9]+)_(minutes))?$`)

// relative is resolve_relative_time: in_N_minutes / in_N_hours / in_N_days, optionally
// followed by _M_minutes. Minutes and hours are added to the instant; days move the local
// calendar date and keep the wall time (DST-correct; legacy added N×24 h).
func (c *Context) relative(key string) (string, bool) {
	m := relativeRE.FindStringSubmatch(key)
	if m == nil {
		return "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return "", false
	}
	t := c.now
	switch m[2] {
	case "minutes":
		t = t.Add(time.Duration(n) * time.Minute)
	case "hours":
		t = t.Add(time.Duration(n) * time.Hour)
	case "days":
		t = t.AddDate(0, 0, n)
	}
	if m[3] != "" {
		extra, err := strconv.Atoi(m[3])
		if err != nil {
			return "", false
		}
		t = t.Add(time.Duration(extra) * time.Minute)
	}
	return t.UTC().Format(isoLayout), true
}

// namedTimes are the time modifiers with a fixed local clock time. at_<time> keys parse their
// time (ParseTime); the meal keys close the vocabulary gap.
var namedTimes = map[string][2]int{
	"morning": {7, 0}, "afternoon": {13, 0}, "evening": {18, 0}, "night": {21, 0},
	"noon": {12, 0}, "midnight": {0, 0},
	"at_noon": {12, 0}, "at_midnight": {0, 0}, "at_breakfast": {8, 0}, "at_dinner": {18, 0},
	"during_breakfast": {8, 0}, "during_lunch": {12, 0}, "during_dinner": {18, 0}, "after_dinner": {19, 0},
}

// modifier reports a time-of-day key and its local clock time.
func modifier(key string) (hh, mm int, ok bool) {
	if t, ok := namedTimes[key]; ok {
		return t[0], t[1], true
	}
	if strings.HasPrefix(key, "at_") {
		return ParseTime(key[3:])
	}
	return 0, 0, false
}

// ParseTime is parse_time_string for "9am", "3pm", "9_30am", "7:30pm", "7.30pm", "730pm",
// "1930": (hour, minute) in 24-hour form. ok=false where legacy returned (0, 0) for
// unparseable or out-of-range input, so "at_home" is not mistaken for midnight.
func ParseTime(s string) (hh, mm int, ok bool) {
	s = parse.PyLower(parse.PyStrip(s))
	meridiem := ""
	if strings.HasSuffix(s, "am") || strings.HasSuffix(s, "pm") {
		meridiem = s[len(s)-2:]
		s = strings.Trim(s[:len(s)-2], "_ ")
	}
	s = strings.NewReplacer(".", "_", ":", "_").Replace(s)
	switch {
	case strings.Contains(s, "_"):
		parts := strings.Split(s, "_")
		var okH, okM bool
		hh, okH = digits(parts[0])
		if len(parts) > 1 {
			mm, okM = digits(parts[1])
		}
		if !okH {
			return 0, 0, false
		}
		if !okM {
			mm = 0
		}
	default:
		if _, isNum := digits(s); !isNum {
			return 0, 0, false
		}
		if len(s) >= 3 {
			hh, _ = digits(s[:len(s)-2])
			mm, _ = digits(s[len(s)-2:])
		} else {
			hh, _ = digits(s)
		}
	}
	switch {
	case meridiem == "pm" && hh != 12:
		hh += 12
	case meridiem == "am" && hh == 12:
		hh = 0
	}
	if hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, 0, false
	}
	return hh, mm, true
}

// digits parses a non-empty run of ASCII digits.
func digits(s string) (int, bool) {
	if s == "" || len(s) > 9 {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

// Resolve is resolve_date_keys with the corrected combination rule. Keys are normalized,
// then in order:
//
//   - in_* keys give an instant relative to now (never modified);
//   - time modifiers (morning … midnight, at_<time>, the meal keys) are collected;
//   - any other key known to the context gives its date(s) or instant(s);
//   - anything else is unresolved.
//
// Modifiers set their local clock time on every date-bearing key (one instant per date per
// modifier, replacing the key's own midnight or time); with no date-bearing key they apply to
// today; next to in_* keys alone they are ignored. The result is de-duplicated in order.
func (c *Context) Resolve(keys []string) (resolved, unresolved []string) {
	type item struct {
		fixed  string
		values []civil
	}
	var items []item
	var mods [][2]int
	dateBearing := false
	for _, raw := range keys {
		key := NormalizeKey(raw)
		if s, ok := c.relative(key); ok {
			items = append(items, item{fixed: s})
			continue
		}
		if hh, mm, ok := modifier(key); ok {
			mods = append(mods, [2]int{hh, mm})
			continue
		}
		if e := c.flat.get(key); e != nil {
			items = append(items, item{values: e.values})
			dateBearing = true
			continue
		}
		unresolved = append(unresolved, key)
	}
	if len(mods) > 0 && !dateBearing && len(items) == 0 {
		items = append(items, item{values: []civil{c.today()}})
		dateBearing = true
	}
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			resolved = append(resolved, s)
		}
	}
	for _, it := range items {
		if it.values == nil {
			add(it.fixed)
			continue
		}
		for _, v := range it.values {
			if len(mods) == 0 {
				add(v.instant(c.loc))
				continue
			}
			for _, m := range mods {
				add(v.at(m[0], m[1]).instant(c.loc))
			}
		}
	}
	return resolved, unresolved
}
