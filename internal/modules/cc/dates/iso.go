package dates

import (
	"sort"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// IsISODatetime is param_validation.is_iso_datetime: the value contains "T" and
// datetime.fromisoformat (CPython 3.11, the version prod's command-center image pins; a
// trailing "Z" is first rewritten to "+00:00") parses it with a UTC offset.
func IsISODatetime(value string) bool {
	if !strings.Contains(value, "T") {
		return false
	}
	if strings.HasSuffix(value, "Z") {
		value = value[:len(value)-1] + "+00:00"
	}
	hasTZ, ok := fromISOFormat(value)
	return ok && hasTZ
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// num parses exactly n ASCII digits at s[i:].
func num(s string, i, n int) (int, bool) {
	if i < 0 || i+n > len(s) {
		return 0, false
	}
	v := 0
	for j := i; j < i+n; j++ {
		if !isDigit(s[j]) {
			return 0, false
		}
		v = v*10 + int(s[j]-'0')
	}
	return v, true
}

// fromISOFormat ports CPython 3.11's C datetime.fromisoformat (Modules/_datetimemodule.c)
// far enough to decide acceptance: it reports whether the string parses and carries a UTC
// offset. Positions are UTF-8 bytes, as in the C code.
func fromISOFormat(s string) (hasTZ, ok bool) {
	if len(s) < 7 {
		return false, false
	}
	sep := isoSeparator(s)
	if sep < 0 || sep > len(s) {
		return false, false
	}
	if !parseISODate(s[:sep]) {
		return false, false
	}
	if sep+1 >= len(s) {
		return false, true // a bare date: never tz-aware
	}
	return parseISOTime(s[sep+1:])
}

// isoSeparator is _find_isoformat_datetime_separator.
func isoSeparator(s string) int {
	n := len(s)
	if n == 7 {
		return 7
	}
	if s[4] == '-' {
		if s[5] == 'W' {
			if n < 8 {
				return -1
			}
			if n > 8 && s[8] == '-' {
				if n == 9 {
					return -1
				}
				if n > 10 && isDigit(s[10]) {
					return 8
				}
				return 10
			}
			return 8
		}
		return 10
	}
	if s[4] == 'W' {
		idx := 7
		for idx < n && isDigit(s[idx]) {
			idx++
		}
		if idx < 9 {
			return idx
		}
		if idx%2 == 0 {
			return 7
		}
		return 8
	}
	return 8
}

// parseISODate is parse_isoformat_date plus the date constructor's range checks.
func parseISODate(d string) bool {
	year, ok := num(d, 0, 4)
	if !ok || year < 1 {
		return false
	}
	if len(d) < 5 {
		return false
	}
	sep := d[4] == '-'
	p := 4
	if sep {
		p++
	}
	if p < len(d) && d[p] == 'W' {
		p++
		week, ok := num(d, p, 2)
		if !ok {
			return false
		}
		p += 2
		day := 1
		if p < len(d) {
			if sep {
				if d[p] != '-' {
					return false
				}
				p++
			}
			if day, ok = num(d, p, 1); !ok {
				return false
			}
			p++
		}
		if p != len(d) {
			return false
		}
		return validISOWeek(year, week, day)
	}
	month, ok := num(d, p, 2)
	if !ok {
		return false
	}
	p += 2
	if sep {
		if p >= len(d) || d[p] != '-' {
			return false
		}
		p++
	}
	day, ok := num(d, p, 2)
	if !ok || p+2 != len(d) {
		return false
	}
	return month >= 1 && month <= 12 && day >= 1 && day <= daysIn(year, month)
}

func isLeap(y int) bool { return y%4 == 0 && (y%100 != 0 || y%400 == 0) }

func daysIn(y, m int) int {
	switch m {
	case 2:
		if isLeap(y) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	}
	return 31
}

// validISOWeek mirrors iso_to_ymd's checks (week 53 only in long ISO years, day 1-7).
func validISOWeek(year, week, day int) bool {
	if week <= 0 || week >= 53 {
		if week != 53 {
			return false
		}
		// Long years start on a Thursday, or a Wednesday in leap years. Jan 1's weekday by
		// Zeller-free arithmetic: days since 0001-01-01 (a Monday).
		y := year - 1
		ord := y*365 + y/4 - y/100 + y/400 + 1 // ordinal of Jan 1
		first := ord % 7                       // 1 = Monday … 0 = Sunday (Python's ord % 7)
		if !(first == 4 || (first == 3 && isLeap(year))) {
			return false
		}
	}
	return day >= 1 && day <= 7
}

// parseISOTime is parse_isoformat_time plus the time / timezone constructor checks.
func parseISOTime(t string) (hasTZ, ok bool) {
	tz := len(t)
	for i := 0; i < len(t); i++ {
		if t[i] == 'Z' || t[i] == '+' || t[i] == '-' {
			tz = i
			break
		}
	}
	h, m, sec, rest, ok := parseHMS(t, 0, tz)
	if !ok {
		return false, false
	}
	if h > 23 || m > 59 || sec > 59 {
		return false, false
	}
	if tz == len(t) {
		return false, !rest
	}
	if t[tz] == 'Z' {
		return true, tz == len(t)-1
	}
	th, tm, ts, trest, ok := parseHMS(t, tz+1, len(t))
	if !ok || trest {
		return false, false
	}
	off := th*3600 + tm*60 + ts
	return true, off < 24*3600 // timezone(): strictly within a day
}

// parseHMS is parse_hh_mm_ss_ff over t[p:end]. rest reports "not the end of the string"
// (characters remain after the parsed time), which the caller treats as an error unless a
// UTC offset follows.
func parseHMS(t string, p, end int) (h, m, s int, rest, ok bool) {
	vals := [3]int{}
	hasSep := true
	for i := 0; i < 3; i++ {
		v, good := num(t, p, 2)
		if !good {
			return 0, 0, 0, false, false
		}
		vals[i] = v
		p += 2
		var c byte
		if p < len(t) {
			c = t[p]
		}
		p++
		if i == 0 {
			hasSep = c == ':'
		}
		if p >= end {
			return vals[0], vals[1], vals[2], c != 0, true
		}
		if hasSep && c == ':' {
			continue
		}
		if c == '.' || c == ',' {
			break
		}
		if !hasSep {
			p--
			continue
		}
		return 0, 0, 0, false, false
	}
	// Fractional seconds: up to six digits, extra digits skipped.
	remains := end - p
	toParse := remains
	if toParse > 6 {
		toParse = 6
	}
	if toParse <= 0 {
		return 0, 0, 0, false, false
	}
	if _, good := num(t, p, toParse); !good {
		return 0, 0, 0, false, false
	}
	p += toParse
	for p < len(t) && isDigit(t[p]) {
		p++
	}
	return vals[0], vals[1], vals[2], p < len(t), true
}

// FixISODates is the engine's ISO guard (_try_fix_iso_dates, G4): a model that wrote ISO
// timestamps into resolved_datetimes instead of date keys gets them reverse-mapped to keys
// through the date context — a bucket first (the sorted values of e.g. this_weekend), then
// each value on its own (the first key holding it, in context order). args are the calls'
// JSON argument strings; out has the rewritten ones (json.dumps bytes) and the others as
// given. status is "clean" (no ISO values), "fixed" (all mapped) or "bad" (some could not
// be; the engine then pops the reply and nags once with [ISO_DATE_RETRY]).
func (c *Context) FixISODates(args []string) (status string, out []string) {
	out = append([]string(nil), args...)
	type entry struct {
		idx  int
		isos []string
	}
	var entries []entry
	for i, a := range args {
		v, err := pyjson.Loads(a)
		if err != nil {
			continue
		}
		o, isObj := v.(*pyjson.Object)
		if !isObj {
			continue
		}
		dts, ok := o.Get("resolved_datetimes")
		if !ok {
			continue
		}
		var list []any
		switch x := dts.(type) {
		case string:
			list = []any{x}
		case []any:
			list = x
		default:
			continue
		}
		var isos []string
		for _, d := range list {
			if s, ok := d.(string); ok && IsISODatetime(s) {
				isos = append(isos, s)
			}
		}
		if len(isos) > 0 {
			entries = append(entries, entry{i, isos})
		}
	}
	if len(entries) == 0 {
		return "clean", out
	}

	single := map[string]string{}
	multi := map[string]string{}
	for _, e := range c.flat.entries {
		vals := c.render(e.values)
		if e.list {
			multi[sortedKey(vals)] = e.key
			for _, v := range vals {
				if _, ok := single[v]; !ok {
					single[v] = e.key
				}
			}
			continue
		}
		if _, ok := single[vals[0]]; !ok {
			single[vals[0]] = e.key
		}
	}

	allFixed := true
	for _, e := range entries {
		if k, ok := multi[sortedKey(e.isos)]; ok {
			out[e.idx] = replaceDatetimes(out[e.idx], []string{k})
			continue
		}
		var keys []string
		matched := true
		for _, iso := range e.isos {
			k, ok := single[iso]
			if !ok {
				matched = false
				break
			}
			keys = append(keys, k)
		}
		if matched && len(keys) > 0 {
			out[e.idx] = replaceDatetimes(out[e.idx], keys)
		} else {
			allFixed = false
		}
	}
	if allFixed {
		return "fixed", out
	}
	return "bad", out
}

func sortedKey(vals []string) string {
	s := append([]string(nil), vals...)
	sort.Strings(s)
	return strings.Join(s, "\x00")
}

// replaceDatetimes is _replace_datetimes: set resolved_datetimes, re-serialize.
func replaceDatetimes(args string, keys []string) string {
	v, err := pyjson.Loads(args)
	if err != nil {
		return args
	}
	o, ok := v.(*pyjson.Object)
	if !ok {
		return args
	}
	list := make([]any, len(keys))
	for i, k := range keys {
		list[i] = k
	}
	o.Set("resolved_datetimes", list)
	return pyjson.Dumps(o, true)
}
