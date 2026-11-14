package dates

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Explain spells out the relative dates a text names ("last month", "next Friday", "the day
// before yesterday") as calendar dates, one per phrase, in the order they appear: "last month =
// October 2026 (2026-10-01 to 2026-10-31)". "" when it names none.
//
// It is for work that runs long after the words were said, by a model that has to act on the
// date: a computer task told only "Today is Friday, November 13" worked out "last month" as
// November and hunted for a statement that doesn't exist (suite on Qwen3.5-9B, 2026-11-13).
// The arithmetic is the context's, the same keys voice turns resolve (Lookup); only keys that
// are days or ranges of days are explained, not times of day ("tonight", "at 3pm").
func (c *Context) Explain(text string) string {
	low := strings.ToLower(text)
	type hit struct {
		at     int
		phrase string
		e      *flatEntry
	}
	var keys []*flatEntry
	for _, e := range c.flat.entries {
		if len(e.values) > 0 && !e.values[0].timed {
			keys = append(keys, e)
		}
	}
	// Longest first, so "day before yesterday" isn't read as "yesterday".
	sort.SliceStable(keys, func(a, b int) bool { return len(keys[a].key) > len(keys[b].key) })
	var hits []hit
	for _, e := range keys {
		phrase := strings.ReplaceAll(e.key, "_", " ")
		re := regexp.MustCompile(`\b` + strings.ReplaceAll(regexp.QuoteMeta(phrase), " ", `\s+`) + `\b`)
		loc := re.FindStringIndex(low)
		if loc == nil {
			continue
		}
		hits = append(hits, hit{loc[0], phrase, e})
		low = low[:loc[0]] + strings.Repeat(" ", loc[1]-loc[0]) + low[loc[1]:]
	}
	if len(hits) == 0 {
		return ""
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].at < hits[b].at })
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.phrase + " = " + explainValues(h.e.values)
	}
	return strings.Join(out, "; ")
}

// explainValues is a key's days in words: one day with its weekday, a whole month or year by
// name with its first and last day, any other range by its ends. Days go through time.Date:
// the table holds some unnormalized (last month in January is month 0).
func explainValues(vs []civil) string {
	day := func(v civil) time.Time { return time.Date(v.y, v.m, v.d, 12, 0, 0, 0, time.UTC) }
	first, last := day(vs[0]), day(vs[len(vs)-1])
	const iso = "2006-01-02"
	span := first.Format(iso) + " to " + last.Format(iso)
	switch {
	case len(vs) == 1:
		return first.Format("Monday, January 2, 2006") + " (" + first.Format(iso) + ")"
	case first.Day() == 1 && first.Year() == last.Year() && first.Month() == last.Month() && last.AddDate(0, 0, 1).Day() == 1:
		return first.Format("January 2006") + " (" + span + ")"
	case first.YearDay() == 1 && last.Year() == first.Year() && last.Month() == time.December && last.Day() == 31:
		return fmt.Sprintf("%d (%s)", first.Year(), span)
	}
	return span
}
