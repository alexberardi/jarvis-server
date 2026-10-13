package dates

import (
	"strings"
)

// Calendar is the per-turn date lookup table the model reads instead of doing date
// arithmetic: today, then this week, next week and the week after, Sunday to Saturday like
// weeks.* and next_<day> (D8). A 9B model asked "what's the date next Friday?" computed Oct 17
// (a Saturday) on one run and Nov 7 on another; tool arguments never had this problem because
// they carry keys that this package resolves.
func (c *Context) Calendar() string {
	t := c.today()
	short := func(d civil) string {
		s := d.time(c.loc).Format("Mon Jan 2")
		if d == t {
			s += " (today)"
		}
		return s
	}
	week := func(label string, start civil) string {
		days := make([]string, 7)
		for i := range days {
			days[i] = short(start.addDays(i))
		}
		return label + ": " + strings.Join(days, ", ") + "."
	}
	ws := c.weekStart()
	var nextFriday civil
	for _, w := range c.weekdayKeys() {
		if w.key == "next_friday" {
			nextFriday = w.when
		}
	}
	return strings.Join([]string{
		"Today is " + t.time(c.loc).Format("Monday, January 2, 2006") + ". Tomorrow is " +
			t.addDays(1).time(c.loc).Format("Monday, January 2") + ".",
		week("This week", ws),
		week("Next week", ws.addDays(7)),
		week("The week after", ws.addDays(14)),
		`Weeks run Sunday to Saturday. "Next <weekday>" is that day in next week (next Friday is ` +
			nextFriday.time(c.loc).Format("Mon Jan 2") + `); "this <weekday>" or a bare weekday is the coming one.`,
	}, "\n")
}
