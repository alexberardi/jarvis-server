// Package dates is command-center's side of the date pipeline (docs/cc/03 §3.5–3.7): the
// date context (GET /generate/date-context and the resolver's lookup table), date-key
// resolution, the ISO guard and date-time parameter injection.
//
// It implements the CORRECTED spec (D8, 03.Q1), not the legacy bugs:
//   - all calendar maths is done on local civil dates with time.Date in the user's zone, so
//     every utc_start_of_day and time expression is DST-correct (legacy reused the current
//     UTC offset for every date);
//   - next_<day> / last_<day> are the named weekday of next / last week (legacy was one day
//     early);
//   - next_year / last_year are the calendar years ±1 (legacy used ±365 days);
//   - UTC±H:MM offsets keep their minutes; an invalid zone falls back to UTC instead of raising;
//   - a time modifier (morning, at_7_30pm, during_lunch, …) sets the local clock time on the
//     day keys it accompanies and yields one combined instant per day (legacy replaced the hour
//     on the UTC instant and also kept the bare midnight);
//   - the vocabulary gap is closed (D40 03.Q9): every key of the shared Vocabulary resolves,
//     and there is no LLM fallback.
//
// fixtures/golden/dates holds the legacy outputs; dates_golden_test.go asserts equality for
// every value legacy got right and documents each intended divergence.
package dates

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // zone database on every OS (Windows has none)

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// isoLayout is the wire form of every instant: UTC, whole seconds, a literal Z.
const isoLayout = "2006-01-02T15:04:05Z"

// Context is the date context for one instant in one zone.
type Context struct {
	now      time.Time // in loc, truncated to whole seconds
	loc      *time.Location
	zoneName string // user_timezone
	flat     *flatMap
}

// civil is a local calendar date with an optional wall-clock time.
type civil struct {
	y       int
	m       time.Month
	d       int
	timed   bool
	hh, min int
}

// at returns the same day at hh:mm.
func (c civil) at(hh, mm int) civil { return civil{c.y, c.m, c.d, true, hh, mm} }

// addDays returns the date n days later (time of day kept).
func (c civil) addDays(n int) civil {
	t := time.Date(c.y, c.m, c.d+n, 12, 0, 0, 0, time.UTC)
	return civil{t.Year(), t.Month(), t.Day(), c.timed, c.hh, c.min}
}

// dateString is %Y-%m-%d.
func (c civil) dateString() string {
	return time.Date(c.y, c.m, c.d, 12, 0, 0, 0, time.UTC).Format("2006-01-02")
}

// weekday of the date.
func (c civil) weekday() time.Weekday {
	return time.Date(c.y, c.m, c.d, 12, 0, 0, 0, time.UTC).Weekday()
}

// instant renders the local date (midnight unless timed) in loc as the UTC wire string.
func (c civil) instant(loc *time.Location) string { return c.time(loc).UTC().Format(isoLayout) }

// time is the local wall time in loc. An ambiguous wall time (DST fall-back) is its first
// occurrence; a nonexistent one (spring-forward gap) uses the offset in force before the
// transition, so 02:15 on a 02:00→03:00 day is 03:15 new time. Both match zoneinfo's fold=0.
func (c civil) time(loc *time.Location) time.Time {
	t := time.Date(c.y, c.m, c.d, c.hh, c.min, 0, 0, loc)
	norm := time.Date(c.y, c.m, c.d, c.hh, c.min, 0, 0, time.UTC)
	if y, m, d := t.Date(); y == norm.Year() && m == norm.Month() && d == norm.Day() && t.Hour() == norm.Hour() && t.Minute() == norm.Minute() {
		return t
	}
	_, before := t.Add(-12 * time.Hour).Zone()
	return norm.Add(-time.Duration(before) * time.Second).In(loc)
}

var utcOffsetRE = regexp.MustCompile(`^UTC([+-])(\d{1,2}):?(\d{2})?$`)

// resolveZone is _normalize_timezone + pytz.timezone with the D8 fixes: "" and invalid names
// fall back to UTC, whole-hour UTC±H offsets map to Etc/GMT∓H as before, and offsets with
// minutes become a fixed zone that keeps them.
func resolveZone(tz string) (*time.Location, string) {
	switch tz {
	case "", "UTC", "UTC-00:00", "UTC+00:00", "UTC-0", "UTC+0":
		return time.UTC, "UTC"
	}
	if m := utcOffsetRE.FindStringSubmatch(tz); m != nil {
		h, _ := strconv.Atoi(m[2])
		mins := 0
		if m[3] != "" {
			mins, _ = strconv.Atoi(m[3])
		}
		sign := 1
		if m[1] == "-" {
			sign = -1
		}
		switch {
		case h == 0 && mins == 0:
			return time.UTC, "UTC"
		case mins == 0:
			etc := "Etc/GMT+" + strconv.Itoa(h) // Etc/GMT signs are inverted
			if sign == 1 {
				etc = "Etc/GMT-" + strconv.Itoa(h)
			}
			if loc, err := time.LoadLocation(etc); err == nil {
				return loc, etc
			}
			return time.UTC, "UTC"
		case h > 14 || mins > 59:
			return time.UTC, "UTC"
		}
		name := "UTC" + m[1] + pad2(h) + ":" + pad2(mins)
		return time.FixedZone(name, sign*(h*3600+mins*60)), name
	}
	if tz == "Local" || strings.HasPrefix(tz, "/") {
		return time.UTC, "UTC" // never the server's own zone, never a path
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC, "UTC"
	}
	return loc, tz
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// New builds the date context for now in tz (an IANA name, "UTC±H[:MM]", or "" for UTC).
func New(now time.Time, tz string) *Context {
	loc, name := resolveZone(tz)
	c := &Context{now: now.In(loc).Truncate(time.Second), loc: loc, zoneName: name}
	c.flat = c.buildFlat()
	return c
}

// Location is the zone the context resolved tz to.
func (c *Context) Location() *time.Location { return c.loc }

// Now is the context's clock (local, whole seconds).
func (c *Context) Now() time.Time { return c.now }

// today is the local calendar date.
func (c *Context) today() civil {
	y, m, d := c.now.Date()
	return civil{y: y, m: m, d: d}
}

// Today is today's utc_start_of_day (the date injection default).
func (c *Context) Today() string { return c.today().instant(c.loc) }

// weekNames is Python's %A, Monday first (weekday_number order).
var weekNames = []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

// mondayIndex is now.weekday(): Monday 0 … Sunday 6.
func mondayIndex(w time.Weekday) int { return (int(w) + 6) % 7 }

// weekendDays is the legacy weekend rule: on Saturday this weekend is today and tomorrow, on
// Sunday yesterday and today, otherwise the coming Saturday and Sunday; last and next are a
// week either side.
func (c *Context) weekendDays() (this, next, last [2]civil) {
	t := c.today()
	switch wd := mondayIndex(t.weekday()); wd {
	case 5:
		this = [2]civil{t, t.addDays(1)}
		last = [2]civil{t.addDays(-7), t.addDays(-6)}
		next = [2]civil{t.addDays(7), t.addDays(8)}
	case 6:
		this = [2]civil{t.addDays(-1), t}
		last = [2]civil{t.addDays(-8), t.addDays(-7)}
		next = [2]civil{t.addDays(6), t.addDays(7)}
	default:
		sat := t.addDays((5 - wd) % 7)
		this = [2]civil{sat, sat.addDays(1)}
		ls := t.addDays(-(wd + 2))
		last = [2]civil{ls, ls.addDays(1)}
		next = [2]civil{sat.addDays(7), sat.addDays(8)}
	}
	return
}

// weekStart is the Sunday starting the week that contains today.
func (c *Context) weekStart() civil {
	t := c.today()
	if wd := mondayIndex(t.weekday()); wd != 6 {
		return t.addDays(-(wd + 1))
	}
	return t
}

func firstOfMonth(y int, m time.Month) civil { return civil{y: y, m: m, d: 1} }

// lastOfMonth is the day before the first of the next month.
func lastOfMonth(y int, m time.Month) civil { return firstOfMonth(y, m+1).addDays(-1) }

// timeExpression is one time_expressions entry: its legacy key (spaces), day and wall time.
type timeExpression struct {
	key  string
	when civil
}

// timeExpressions is _generate_time_expressions in order, on DST-correct local wall times.
func (c *Context) timeExpressions() []timeExpression {
	today := c.today()
	tomorrow, yesterday := today.addDays(1), today.addDays(-1)
	out := []timeExpression{
		{"this morning", today.at(7, 0)},
		{"this afternoon", today.at(14, 0)},
		{"this evening", today.at(19, 0)},
		{"tonight", today.at(20, 0)},
		{"during lunch", today.at(12, 0)},
		{"at breakfast", today.at(8, 0)},
		{"at dinner", today.at(18, 0)},
		{"at noon", today.at(12, 0)},
		{"at midnight", today.at(0, 0)},
		{"tomorrow morning", tomorrow.at(7, 0)},
		{"tomorrow afternoon", tomorrow.at(14, 0)},
		{"tomorrow evening", tomorrow.at(19, 0)},
		{"tomorrow night", tomorrow.at(20, 0)},
		{"yesterday morning", yesterday.at(7, 0)},
		{"yesterday afternoon", yesterday.at(14, 0)},
		{"yesterday evening", yesterday.at(19, 0)},
		{"last night", yesterday.at(20, 0)},
	}
	for h := 1; h <= 12; h++ {
		am, pm := h, h+12
		if h == 12 {
			am, pm = 0, 12
		}
		hs := strconv.Itoa(h)
		out = append(out,
			timeExpression{"at " + hs + "am", today.at(am, 0)},
			timeExpression{"at " + hs + "pm", today.at(pm, 0)},
			timeExpression{"at " + hs + ":30am", today.at(am, 30)},
			timeExpression{"at " + hs + ":30pm", today.at(pm, 30)},
		)
		switch h {
		case 9, 10, 11, 1, 2, 3:
			out = append(out,
				timeExpression{"at " + hs + ":15am", today.at(am, 15)},
				timeExpression{"at " + hs + ":45am", today.at(am, 45)},
				timeExpression{"at " + hs + ":15pm", today.at(pm, 15)},
				timeExpression{"at " + hs + ":45pm", today.at(pm, 45)},
			)
		}
	}
	return out
}

// mealKeys close the vocabulary gap (D40 03.Q9): keys in the shared Vocabulary that the legacy
// context never produced. They resolve like at_breakfast / at_dinner.
var mealKeys = []timeExpression{
	{"during_breakfast", civil{timed: true, hh: 8}},
	{"during_dinner", civil{timed: true, hh: 18}},
	{"after_dinner", civil{timed: true, hh: 19}},
}

// Object is generate_date_context_object's dict, in the legacy key order: the body of
// GET /api/v0/generate/date-context. timezone.user_timezone is always a string and is_dst
// always a bool (03.Q11: the node's strict DateContext rejects null).
func (c *Context) Object() *pyjson.Object {
	t := c.today()
	day := func(d civil) *pyjson.Object {
		return obj("date", d.dateString(), "utc_start_of_day", d.instant(c.loc))
	}
	labelled := func(label string, d civil) *pyjson.Object {
		return obj("day", label, "date", d.dateString(), "utc_start_of_day", d.instant(c.loc))
	}
	thisWE, nextWE, lastWE := c.weekendDays()
	weekend := func(w [2]civil) []any {
		return []any{labelled("Saturday", w[0]), labelled("Sunday", w[1])}
	}
	ws := c.weekStart()
	week := func(start civil, prefix string) []any {
		var out []any
		for i := 0; i < 7; i++ {
			d := start.addDays(i)
			out = append(out, labelled(prefix+weekNames[mondayIndex(d.weekday())], d))
		}
		return out
	}
	y, m := t.y, t.m
	weekdays := pyjson.NewObject()
	for _, w := range c.weekdayKeys() {
		weekdays.Set(w.key, day(w.when))
	}
	yesterday := t.addDays(-1)
	expressions := pyjson.NewObject()
	for _, e := range c.timeExpressions() {
		expressions.Set(e.key, e.when.instant(c.loc))
	}
	return obj(
		"current", obj(
			"date", c.now.Format("Monday, January 02 2006"),
			"date_iso", c.now.Format("2006-01-02"),
			"time", c.now.Format("03:04 PM"),
			"datetime", c.now.UTC().Format(isoLayout),
			"weekday", strings.ToLower(c.now.Format("Monday")),
			"weekday_number", mondayIndex(c.now.Weekday()),
			"utc_start_of_day", t.instant(c.loc),
		),
		"relative_dates", obj(
			"tomorrow", day(t.addDays(1)),
			"yesterday", day(yesterday),
			"last_night", obj("date", yesterday.dateString(), "time", "19:00:00", "datetime", yesterday.at(19, 0).instant(c.loc)),
			"day_after_tomorrow", day(t.addDays(2)),
			"day_before_yesterday", day(t.addDays(-2)),
		),
		"weekend", obj("this_weekend", weekend(thisWE), "next_weekend", weekend(nextWE), "last_weekend", weekend(lastWE)),
		"weeks", obj("this_week", week(ws, ""), "next_week", week(ws.addDays(7), "Next "), "last_week", week(ws.addDays(-7), "Last ")),
		"months", obj(
			"this_month", []any{day(firstOfMonth(y, m)), day(lastOfMonth(y, m))},
			"next_month", []any{day(firstOfMonth(y, m+1)), day(lastOfMonth(y, m+1))},
			"last_month", []any{day(firstOfMonth(y, m-1)), day(lastOfMonth(y, m-1))},
		),
		"years", obj(
			"this_year", []any{day(civil{y: y, m: 1, d: 1}), day(civil{y: y, m: 12, d: 31})},
			"next_year", []any{day(civil{y: y + 1, m: 1, d: 1}), day(civil{y: y + 1, m: 12, d: 31})},
			"last_year", []any{day(civil{y: y - 1, m: 1, d: 1}), day(civil{y: y - 1, m: 12, d: 31})},
		),
		"weekdays", weekdays,
		"timezone", obj("user_timezone", c.zoneName, "current_timezone", c.loc.String(), "is_dst", c.now.IsDST()),
		"time_expressions", expressions,
	)
}

// weekdayKeys are next_<day> then last_<day> (Monday first): the named weekday of next week
// and of last week (weeks run Sunday to Saturday, like weeks.*).
func (c *Context) weekdayKeys() []timeExpression {
	ws := c.weekStart()
	var out []timeExpression
	for _, rel := range []struct {
		prefix string
		start  civil
	}{{"next_", ws.addDays(7)}, {"last_", ws.addDays(-7)}} {
		for i, name := range weekNames {
			out = append(out, timeExpression{rel.prefix + strings.ToLower(name), rel.start.addDays((i + 1) % 7)})
		}
	}
	return out
}

// obj builds an ordered object from alternating keys and values.
func obj(kv ...any) *pyjson.Object {
	o := pyjson.NewObject()
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
