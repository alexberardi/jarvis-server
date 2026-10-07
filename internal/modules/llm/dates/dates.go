// Package dates is the date-key matcher (llm-proxy services/date_key_matcher.py, ported
// exactly) and the date-key vocabulary (services/date_keys.py, the static part).
//
// Extract is regex only (D9, D40): no fastText fallback and no [DATE_HINT] message. CC calls
// it in-process once per turn on the raw transcript; the external /v1/chat/completions keeps
// include_date_context → date_keys for HTTP callers.
//
// Python semantics are kept where they decide the output (docs/llm/04 §7.2):
//   - positions are code points, so the 50% negative-overlap rule measures what Python measured
//   - \w, \b, \s and \d are Python's Unicode classes, spelled out for regexp2 (pure Go): Python
//     \w is letters, numbers and "_" (no combining marks, unlike .NET's), \d is Nd, and \s is
//     str.isspace (which includes \x1c-\x1f)
//   - str.lower() maps İ to "i̇", and re.IGNORECASE also lets i match ı and s match ſ
//
// The look-arounds (three negative patterns and the static forms' (?<!\w)…(?!\w) wrap) run
// on regexp2 or, for the 288 literal static forms, as hand-written boundary checks.
package dates

import (
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/dlclark/regexp2"
)

// --- vocabulary (services/date_keys.py:33-151) ---

// Version is DATE_KEYS_VERSION.
const Version = "2.0"

var (
	relativeDays = []string{"today", "tomorrow", "yesterday", "day_after_tomorrow", "day_before_yesterday"}
	combinedKeys = []string{
		"tonight", "last_night", "tomorrow_night", "tomorrow_morning", "tomorrow_afternoon",
		"tomorrow_evening", "yesterday_morning", "yesterday_afternoon", "yesterday_evening",
		"this_morning", "this_afternoon", "this_evening",
	}
	timeModifiers = []string{"morning", "afternoon", "evening", "night", "noon", "midnight", "at_noon", "at_midnight"}
	mealTimes     = []string{"at_breakfast", "during_breakfast", "during_lunch", "at_dinner", "during_dinner", "after_dinner"}
	weekdays      = []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}
	periodKeys    = []string{
		"this_week", "next_week", "last_week", "this_weekend", "next_weekend", "last_weekend",
		"this_month", "next_month", "last_month", "this_year", "next_year", "last_year",
	}
)

// Vocabulary is the 64 static date keys, sorted (ALL_DATE_KEYS, as /v1/adapters/date-keys
// serves static_keys). It is the one shared constant behind CC's DT_KEYS prompt line, the
// matcher and the resolver (D40 Q9).
var Vocabulary = func() []string {
	var all []string
	all = append(all, relativeDays...)
	all = append(all, combinedKeys...)
	all = append(all, timeModifiers...)
	all = append(all, mealTimes...)
	for _, prefix := range []string{"this", "next", "last"} {
		for _, d := range weekdays {
			all = append(all, prefix+"_"+d)
		}
	}
	all = append(all, periodKeys...)
	sort.Strings(all)
	return all
}()

// DynamicPattern documents a numeric key family.
type DynamicPattern struct {
	Pattern     string `json:"pattern"`
	Regex       string `json:"regex"`
	Description string `json:"description"`
}

// DynamicPatterns is DYNAMIC_PATTERNS.
var DynamicPatterns = []DynamicPattern{
	{Pattern: "in_{N}_minutes", Regex: `^in_(\d+)_minutes$`, Description: "Relative offset in minutes from current time"},
	{Pattern: "in_{N}_days", Regex: `^in_(\d+)_days$`, Description: "Relative offset in days from current time"},
}

// KV is one ordered key/value pair of the vocabulary's documentation maps.
type KV struct{ Key, Value string }

// TimePatterns is TIME_PATTERNS, in order.
var TimePatterns = []KV{
	{"hourly", "at_Xam, at_Xpm (X = 1-12)"},
	{"quarter_past", "at_X_15am, at_X_15pm"},
	{"half_past", "at_X_30am, at_X_30pm"},
	{"quarter_to", "at_X_45am, at_X_45pm"},
}

// Notes is the response's notes map, in order.
var Notes = []KV{
	{"composability", "Multiple keys may be returned, e.g., ['next_tuesday', 'morning']"},
	{"no_date", "Empty array returned if no date reference detected"},
	{"combined_keys", "Common phrases like 'tonight', 'tomorrow_morning' are standalone keys"},
	{"relative_time", "Dynamic keys like 'in_30_minutes', 'in_3_days' are generated from relative time expressions"},
}

// --- matcher patterns (services/date_key_matcher.py) ---

type group struct {
	key      string
	variants []string
}

var combinedPatterns = []group{
	{"tomorrow_morning", []string{"tomorrow morning", "tmrw morning", "tomorrow am", "tomorrow morn", "2morrow morning", "2mrw morning", "tmrw morn"}},
	{"tomorrow_afternoon", []string{"tomorrow afternoon", "tmrw afternoon", "2morrow afternoon"}},
	{"tomorrow_evening", []string{"tomorrow evening", "tmrw evening", "2morrow evening"}},
	{"tomorrow_night", []string{"tomorrow night", "tmrw night", "tomorrow nite", "2morrow night"}},
	{"yesterday_morning", []string{"yesterday morning", "yest morning", "yesterday am", "ystrdy morning"}},
	{"yesterday_afternoon", []string{"yesterday afternoon", "yest afternoon", "ystrdy afternoon"}},
	{"yesterday_evening", []string{"yesterday evening", "yest evening", "ystrdy evening"}},
	{"tonight", []string{"tonight", "tonite", "this evening late", "later tonight"}},
	{"last_night", []string{"last night", "lastnight", "yesterday night", "yesternight"}},
	{"this_morning", []string{"this morning", "earlier today", "earlier this morning"}},
	{"this_afternoon", []string{"this afternoon", "later today", "this aft"}},
	{"this_evening", []string{"this evening", "early tonight"}},
}

var relativeDayPatterns = []group{
	{"day_after_tomorrow", []string{"the day after tomorrow", "day after tomorrow", "overmorrow", "in two days", "in 2 days"}},
	{"day_before_yesterday", []string{"the day before yesterday", "day before yesterday", "two days ago", "2 days ago"}},
	{"today", []string{"today", "2day", "tday"}},
	{"tomorrow", []string{"tomorrow", "tmrw", "tmr", "2morrow", "2mrw", "tom", "2mrw"}},
	{"yesterday", []string{"yesterday", "yest", "yday", "ystrdy"}},
}

var weekdayAbbrevs = map[string][]string{
	"monday":    {"monday", "mon"},
	"tuesday":   {"tuesday", "tues", "tue"},
	"wednesday": {"wednesday", "weds", "wed"},
	"thursday":  {"thursday", "thurs", "thur", "thu"},
	"friday":    {"friday", "fri"},
	"saturday":  {"saturday", "sat"},
	"sunday":    {"sunday", "sun"},
}

// weekdayPatterns rebuilds _WEEKDAY_PATTERNS with Python dict insertion order: next_/last_/
// this_ keys are created per weekday in that order, then the bare forms append to this_X.
func weekdayPatterns() []group {
	var order []string
	m := map[string][]string{}
	add := func(k string, vs ...string) {
		if _, ok := m[k]; !ok {
			order = append(order, k)
		}
		m[k] = append(m[k], vs...)
	}
	for _, d := range weekdays {
		for _, a := range weekdayAbbrevs[d] {
			add("next_"+d, "next "+a, "the following "+a, "nxt "+a)
			add("last_"+d, "last "+a, "the previous "+a, "lst "+a)
			add("this_"+d, "this "+a)
		}
	}
	for _, d := range weekdays {
		for _, a := range weekdayAbbrevs[d] {
			add("this_"+d, a)
		}
	}
	out := make([]group, len(order))
	for i, k := range order {
		out[i] = group{k, m[k]}
	}
	return out
}

var periodPatterns = []group{
	{"this_weekend", []string{"this weekend", "the weekend", "this sat and sun", "the wknd", "this wknd"}},
	{"next_weekend", []string{"next weekend", "the following weekend"}},
	{"last_weekend", []string{"last weekend", "the previous weekend"}},
	{"this_week", []string{"this week", "the current week"}},
	{"next_week", []string{"next week", "the following week", "the week after", "nxt wk", "nxt week"}},
	{"last_week", []string{"last week", "the previous week", "the week before"}},
	{"this_month", []string{"this month", "the current month"}},
	{"next_month", []string{"next month", "the following month"}},
	{"last_month", []string{"last month", "the previous month"}},
	{"this_year", []string{"this year", "the current year"}},
	{"next_year", []string{"next year", "the following year"}},
	{"last_year", []string{"last year", "the previous year"}},
}

var mealPatterns = []group{
	{"at_breakfast", []string{"at breakfast time", "at breakfasttime", "at breakfast", "breakfast time"}},
	{"during_breakfast", []string{"during breakfast", "while eating breakfast", "over breakfast"}},
	{"during_lunch", []string{"during lunch", "at lunchtime", "at lunch time", "at lunch", "lunch time", "lunchtime", "over lunch"}},
	{"at_dinner", []string{"at dinner time", "at dinnertime", "at dinner", "dinner time"}},
	{"during_dinner", []string{"during dinner", "while eating dinner", "over dinner"}},
	{"after_dinner", []string{"after dinnertime", "after dinner", "post dinner", "once dinner is done", "post-dinner"}},
}

var timeModPatterns = []group{
	{"morning", []string{"in the morning", "in the am", "morning", "morn"}},
	{"afternoon", []string{"in the afternoon", "afternoon", "aft"}},
	{"evening", []string{"in the evening", "evening", "eve"}},
	{"night", []string{"at night", "in the night", "night", "nite"}},
	{"noon", []string{"at noon", "midday", "12 o'clock", "12 oclock", "noon"}},
	{"midnight", []string{"at midnight", "midnight"}},
}

type staticForm struct {
	form []rune
	key  string
}

// staticForms is _build_pattern_list: every variant in group/insertion order, stable-sorted
// by length (characters) descending.
var staticForms = func() []staticForm {
	var out []staticForm
	for _, g := range [][]group{combinedPatterns, relativeDayPatterns, weekdayPatterns(), periodPatterns, mealPatterns, timeModPatterns} {
		for _, gr := range g {
			for _, v := range gr.variants {
				out = append(out, staticForm{[]rune(v), gr.key})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].form) > len(out[j].form) })
	return out
}()

// Python's Unicode classes, spelled out for regexp2.
const (
	pyW = `\p{L}\p{N}_`
	pyS = `\t\n\x0b\f\r\x1c-\x20\x85\xa0  -     　`
	pyD = `\p{Nd}`
)

// translate rewrites a Python pattern's \b, \w, \s and \d into Python's Unicode semantics.
func translate(p string) string {
	var b strings.Builder
	inClass := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '\\' && i+1 < len(p) {
			n := p[i+1]
			i++
			switch {
			case n == 'b' && !inClass:
				b.WriteString(`(?:(?<=[` + pyW + `])(?![` + pyW + `])|(?<![` + pyW + `])(?=[` + pyW + `]))`)
			case n == 'w':
				if inClass {
					b.WriteString(pyW)
				} else {
					b.WriteString(`[` + pyW + `]`)
				}
			case n == 's':
				if inClass {
					b.WriteString(pyS)
				} else {
					b.WriteString(`[` + pyS + `]`)
				}
			case n == 'd':
				b.WriteString(pyD)
			default:
				b.WriteByte('\\')
				b.WriteByte(n)
			}
			continue
		}
		switch c {
		case '[':
			inClass = true
		case ']':
			inClass = false
		}
		b.WriteByte(c)
	}
	return b.String()
}

func mustCompile(p string) *regexp2.Regexp {
	return regexp2.MustCompile(translate(p), regexp2.None)
}

type dynamic struct {
	re  *regexp2.Regexp
	key func(g []string) string
}

var writtenHours = map[string]int{"one": 1, "two": 2, "three": 3, "four": 4}

func fixed(k string) func([]string) string { return func([]string) string { return k } }

// _DYNAMIC_REGEXES, in order. g[0] is the whole match, g[n] group n.
var dynamics = []dynamic{
	{mustCompile(`\bin\s+(\d+)\s+hours?\s+and\s+(\d+)\s+minutes?\b`), func(g []string) string {
		return "in_" + itoa(pyInt(g[1])*60+pyInt(g[2])) + "_minutes"
	}},
	{mustCompile(`\bin\s+(one|two|three|four)\s+hours?\s+and\s+(\d+)\s+minutes?\b`), func(g []string) string {
		return "in_" + itoa(writtenHours[g[1]]*60+pyInt(g[2])) + "_minutes"
	}},
	{mustCompile(`\bin\s+two\s+and\s+a\s+half\s+hours?\b`), fixed("in_150_minutes")},
	{mustCompile(`\bin\s+(?:an?|one)\s+hour\s+and\s+a\s+half\b`), fixed("in_90_minutes")},
	{mustCompile(`\bin\s+(?:an?|one|1)\s+hour\s+and\s+(\d+)\s+minutes?\b`), func(g []string) string {
		return "in_" + itoa(60+pyInt(g[1])) + "_minutes"
	}},
	{mustCompile(`\bin\s+a\s+couple\s+(?:of\s+)?hours?\b`), fixed("in_120_minutes")},
	{mustCompile(`\bin\s+(?:a\s+)?half\s+(?:an?\s+)?hour\b`), fixed("in_30_minutes")},
	{mustCompile(`\bin\s+(?:a\s+)?quarter\s+(?:of\s+)?an?\s+hour\b`), fixed("in_15_minutes")},
	{mustCompile(`\bin\s+(?:about\s+)?(?:an?|one|1)\s+hour\b`), fixed("in_60_minutes")},
	{mustCompile(`\b(?:an?|one)\s+hour\s+from\s+now\b`), fixed("in_60_minutes")},
	{mustCompile(`\bin\s+(?:about\s+)?(\d+)\s+hours?\b`), func(g []string) string { return "in_" + itoa(pyInt(g[1])*60) + "_minutes" }},
	{mustCompile(`\b(\d+)\s+hours?\s+from\s+now\b`), func(g []string) string { return "in_" + itoa(pyInt(g[1])*60) + "_minutes" }},
	{mustCompile(`\bin\s+(two)\s+hours?\b`), fixed("in_120_minutes")},
	{mustCompile(`\bin\s+(three)\s+hours?\b`), fixed("in_180_minutes")},
	{mustCompile(`\bin\s+(four)\s+hours?\b`), fixed("in_240_minutes")},
	{mustCompile(`\bin\s+(?:exactly\s+|about\s+|like\s+)?(\d+)\s+minutes?\b`), func(g []string) string { return "in_" + g[1] + "_minutes" }},
	{mustCompile(`\b(?:for\s+)?(\d+)\s+minutes?\s+from\s+now\b`), func(g []string) string { return "in_" + g[1] + "_minutes" }},
	{mustCompile(`\bin\s+(?:a\s+)?minute\b`), fixed("in_1_minutes")},
	{mustCompile(`\bin\s+one\s+minutes?\b`), fixed("in_1_minutes")},
	{mustCompile(`\bin\s+five\s+minutes?\b`), fixed("in_5_minutes")},
	{mustCompile(`\bin\s+ten\s+minutes?\b`), fixed("in_10_minutes")},
	{mustCompile(`\bin\s+fifteen\s+minutes?\b`), fixed("in_15_minutes")},
	{mustCompile(`\bin\s+twenty\s+minutes?\b`), fixed("in_20_minutes")},
	{mustCompile(`\bin\s+thirty\s+minutes?\b`), fixed("in_30_minutes")},
	{mustCompile(`\bin\s+forty[- ]?five\s+minutes?\b`), fixed("in_45_minutes")},
	{mustCompile(`\bin\s+a\s+couple\s+(?:of\s+)?days?\b`), fixed("day_after_tomorrow")},
	{mustCompile(`\bin\s+(?:2|two)\s+days?\b`), fixed("day_after_tomorrow")},
	{mustCompile(`\bin\s+(?:a|one|1)\s+day\b`), fixed("in_1_days")},
	{mustCompile(`\bin\s+a\s+couple\s+(?:of\s+)?days?\b`), fixed("in_2_days")},
	{mustCompile(`\bin\s+(\d+)\s+days?\b`), func(g []string) string { return "in_" + g[1] + "_days" }},
	{mustCompile(`\bin\s+three\s+days?\b`), fixed("in_3_days")},
	{mustCompile(`\bin\s+five\s+days?\b`), fixed("in_5_days")},
	{mustCompile(`\bin\s+(?:a|one|1)\s+week\b`), fixed("in_7_days")},
	{mustCompile(`\bin\s+two\s+weeks?\b`), fixed("in_14_days")},
	{mustCompile(`\bin\s+(\d+)\s+weeks?\b`), func(g []string) string { return "in_" + itoa(pyInt(g[1])*7) + "_days" }},
	{mustCompile(`\bquarter\s+past\s+(\d{1,2})\s*(am|pm)\b`), func(g []string) string { return "at_" + g[1] + "_15" + g[2] }},
	{mustCompile(`\bhalf\s+past\s+(\d{1,2})\s*(am|pm)\b`), func(g []string) string { return "at_" + g[1] + "_30" + g[2] }},
	{mustCompile(`\bquarter\s+to\s+(\d{1,2})\s*(am|pm)\b`), func(g []string) string {
		h := pyInt(g[1])
		if h > 1 {
			h--
		} else {
			h = 12
		}
		return "at_" + itoa(h) + "_45" + g[2]
	}},
	{mustCompile(`\b(?:at\s+)?12\s*:\s*00\s*am\b`), fixed("midnight")},
	{mustCompile(`\b(?:at\s+)?12\s*am\b`), fixed("midnight")},
	{mustCompile(`\b12\s+am\b`), fixed("midnight")},
	{mustCompile(`\b(?:at\s+)?12\s*:\s*00\s*pm\b`), fixed("noon")},
	{mustCompile(`\b(?:at\s+)?12\s*pm\b`), fixed("noon")},
	{mustCompile(`\b12\s+pm\b`), fixed("noon")},
	{mustCompile(`\b(?:at\s+)?(\d{1,2}):(\d{2})\s*(am|pm)\b`), func(g []string) string {
		if g[2] == "00" {
			return "at_" + g[1] + g[3]
		}
		return "at_" + g[1] + "_" + g[2] + g[3]
	}},
	{mustCompile(`\bat\s+(\d{1,2})\s*(am|pm)\b`), func(g []string) string { return "at_" + g[1] + g[2] }},
	{mustCompile(`\b(\d{1,2})\s*(am|pm)\b`), func(g []string) string { return "at_" + g[1] + g[2] }},
	{mustCompile(`\b(\d{1,2})\s+thirty\s*(am|pm)\b`), func(g []string) string { return "at_" + g[1] + "_30" + g[2] }},
	{mustCompile(`\b(\d{1,2})\s+fifteen\s*(am|pm)\b`), func(g []string) string { return "at_" + g[1] + "_15" + g[2] }},
	{mustCompile(`\b(\d{1,2})\s+forty[- ]?five\s*(am|pm)\b`), func(g []string) string { return "at_" + g[1] + "_45" + g[2] }},
	{mustCompile(`\b12\s+o'?clock\b`), fixed("noon")},
}

// _NEGATIVE_PATTERNS, in order.
var negatives = func() []*regexp2.Regexp {
	ps := []string{
		`\b(?:lasted?|took?|waited?)\s+\d+\s+(?:minutes?|hours?|days?|weeks?)\b`,
		`(?<!\bin\s)(?<!\bin\s\s)\b(?:about|approximately)\s+\d+\s+(?:minutes?|hours?|days?|weeks?)\s+(?:long|each|total|straight)\b`,
		`\bfor\s+\d+\s+(?:minutes?|hours?)\s*(?!from\s+now)\b(?:\s+(?:long|each|straight|total)|\s*$|\s*[,.])`,
		`\b\d+\s+(?:minutes?|hours?|weeks?)\s+(?:ago|late|early|long)\b`,
		`\bset\s+(?:a\s+)?timer\s+(?:for)\b`,
		`\bin\s+(?:a\s+)?(?:few|little|bit|some)\b`,
		`\b(?:remind\s+me\s+)?later\b`,
		`\b(?:expires?|takes?|lasts?|downloads?)\s+in\s+(?:about\s+)?\d+\s+(?:minutes?|hours?|days?)\b`,
		`^(?:please\s+)?(?:play|watch|listen\s+to|put\s+on|queue)\s+(?!.*\b(?:for|on|at|during)\b).+`,
		`\bdate\s+night\b`,
		`\bnight\s+owl\b`,
		`\bmorning\s+person\b`,
		`\bweekend\s+warrior\b`,
		`\bnight\s+and\s+day\b`,
		`\bday\s+and\s+night\b`,
		`\bnight\s+(?:shift|cap|owl|light|stand|club|life|gown|time|mare)\b`,
		`\bmorning\s+(?:shift|person|routine|sickness|dew|glory|star|show)\b`,
		`\bevening\s+(?:shift|class(?:es)?|wear|gown|news|prayer|star)\b`,
		`\bmidnight\s+(?:snack|blue|train|oil|run|sun|special)\b`,
		`\blate\s+night\b`,
		`\b(?:monday|tuesday|wednesday|thursday|friday|saturday|sunday)\s+(?:motivation|blues|vibes|specials?|cartoons?|brunch)\b`,
		`\b(?:throwback|taco|casual|manic)\s+(?:monday|tuesday|wednesday|thursday|friday|saturday|sunday)\b`,
		`\bwho'?s\s+playing\s+tonight\b`,
	}
	out := make([]*regexp2.Regexp, len(ps))
	for i, p := range ps {
		out[i] = mustCompile(p)
	}
	return out
}()

// --- Python string helpers ---

func itoa(n int) string { return strconv.Itoa(n) }

// pyInt is int(s) for a run of Unicode decimal digits (\d matched them).
func pyInt(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + digitValue(r)
	}
	return n
}

// digitValue is the decimal value of an Nd code point. Every Nd range starts at a zero and
// runs in blocks of ten.
func digitValue(r rune) int {
	if r >= '0' && r <= '9' {
		return int(r - '0')
	}
	for _, rg := range unicode.Nd.R16 {
		if r >= rune(rg.Lo) && r <= rune(rg.Hi) {
			return int(r-rune(rg.Lo)) % 10
		}
	}
	for _, rg := range unicode.Nd.R32 {
		if r >= rune(rg.Lo) && r <= rune(rg.Hi) {
			return int(r-rune(rg.Lo)) % 10
		}
	}
	return 0
}

// isPySpace is str.isspace for one code point.
func isPySpace(r rune) bool {
	switch {
	case r >= '\t' && r <= '\r', r >= 0x1c && r <= 0x20, r == 0x85, r == 0xa0, r == 0x1680,
		r >= 0x2000 && r <= 0x200a, r == 0x2028, r == 0x2029, r == 0x202f, r == 0x205f, r == 0x3000:
		return true
	}
	return false
}

func isPyWord(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }

// pyLower is str.lower(): the simple mapping plus İ → "i̇" (the one unconditional
// multi-character lowercase in SpecialCasing).
func pyLower(s string) []rune {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == 'İ' {
			out = append(out, 'i', 0x307)
			continue
		}
		out = append(out, unicode.ToLower(r))
	}
	return out
}

// pyStrip is str.strip().
func pyStrip(rs []rune) []rune {
	i, j := 0, len(rs)
	for i < j && isPySpace(rs[i]) {
		i++
	}
	for j > i && isPySpace(rs[j-1]) {
		j--
	}
	return rs[i:j]
}

type span struct{ start, end int }

// Extract is extract_date_keys: the sorted unique date keys mentioned in text, or [] (never nil).
func Extract(text string) []string {
	keys := []string{}
	lowered := pyStrip(pyLower(text))
	if len(lowered) == 0 {
		return keys
	}
	// re.IGNORECASE on ASCII patterns also matches ı for i and ſ for s; same length, so
	// positions are unchanged.
	match := make([]rune, len(lowered))
	for i, r := range lowered {
		switch r {
		case 'ı':
			r = 'i'
		case 'ſ':
			r = 's'
		}
		match[i] = r
	}

	var matched, negative []span
	overlaps := func(s, e int) bool {
		for _, m := range matched {
			if s < m.end && e > m.start {
				return true
			}
		}
		return false
	}
	inNegative := func(s, e int) bool {
		for _, n := range negative {
			if s >= n.start && e <= n.end {
				return true
			}
			ov := min(e, n.end) - max(s, n.start)
			if ov > 0 && float64(ov) >= float64(e-s)*0.5 {
				return true
			}
		}
		return false
	}
	have := map[string]bool{}
	accept := func(s, e int, key string) {
		if overlaps(s, e) || inNegative(s, e) {
			return
		}
		if !have[key] {
			have[key] = true
			keys = append(keys, key)
			matched = append(matched, span{s, e})
		}
	}

	for _, re := range negatives {
		m, _ := re.FindRunesMatch(match)
		for m != nil {
			negative = append(negative, span{m.Index, m.Index + m.Length})
			m, _ = re.FindNextMatch(m)
		}
	}

	for _, d := range dynamics {
		m, _ := d.re.FindRunesMatch(match)
		for m != nil {
			gs := m.Groups()
			g := make([]string, len(gs))
			for i, grp := range gs {
				g[i] = grp.String()
			}
			accept(m.Index, m.Index+m.Length, d.key(g))
			m, _ = d.re.FindNextMatch(m)
		}
	}

	for _, sf := range staticForms {
		for _, s := range findStatic(match, sf.form) {
			accept(s, s+len(sf.form), sf.key)
		}
	}
	sort.Strings(keys)
	return keys
}

// findStatic is finditer over (?<!\w)form(?!\w): non-overlapping, left to right.
func findStatic(text, form []rune) []int {
	var out []int
	n, f := len(text), len(form)
	for i := 0; i+f <= n; {
		if !hasAt(text, form, i) {
			i++
			continue
		}
		if (i > 0 && isPyWord(text[i-1])) || (i+f < n && isPyWord(text[i+f])) {
			i++
			continue
		}
		out = append(out, i)
		i += f
	}
	return out
}

func hasAt(text, form []rune, i int) bool {
	for k, r := range form {
		if text[i+k] != r {
			return false
		}
	}
	return true
}
