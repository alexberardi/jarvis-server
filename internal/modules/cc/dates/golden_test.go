package dates

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

func loadDates(t *testing.T, name string) any {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "fixtures", "golden", "dates", name))
	if err != nil {
		t.Fatal(err)
	}
	v, err := pyjson.Loads(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func og(v any, k string) any {
	x, _ := v.(*pyjson.Object).Get(k)
	return x
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

// flatten turns a decoded JSON tree into path → scalar repr.
func flatten(prefix string, v any, out map[string]string) {
	switch x := v.(type) {
	case *pyjson.Object:
		for _, k := range x.Keys() {
			e, _ := x.Get(k)
			flatten(prefix+"."+k, e, out)
		}
	case []any:
		for i, e := range x {
			flatten(prefix+"["+pyjson.Repr(i)+"]", e, out)
		}
	default:
		out[prefix] = pyjson.Dumps(v, true)
	}
}

func treeOf(c *Context) map[string]string {
	v, err := pyjson.Loads(pyjson.Dumps(c.Object(), true))
	if err != nil {
		panic(err)
	}
	m := map[string]string{}
	flatten("", v, m)
	return m
}

func unquote(s string) string {
	v, err := pyjson.Loads(s)
	if err != nil {
		return s
	}
	if str, ok := v.(string); ok {
		return str
	}
	return s
}

// ---------------------------------------------------------------------------------------
// Expected divergences from the legacy fixtures (D8, docs/cc/03 §8.1–8.6, D40 03.Q9/Q11).
// Every legacy value that Go does not reproduce must be claimed by exactly one of these rules,
// and each rule checks the Go value independently (an oracle), so a divergence is never just
// waved through.
// ---------------------------------------------------------------------------------------

type divergence struct {
	id, reason string
}

var divergenceTable = []divergence{
	{"weekday-off-by-one", "§8.1: next_<day>/last_<day> were one day early (Monday-first names indexed from a Sunday week start); Go gives the named weekday of next/last week"},
	{"dst-stale-offset", "§8.4: pytz arithmetic reused the current UTC offset for every date/time; across a DST change Go's local midnight / wall time is 1 h apart from legacy"},
	{"year-365", "§8.6: next_year/last_year were now±365 days, which lands in the wrong year near Dec 31 of a leap year; Go uses the calendar year ±1"},
	{"zone-fallback", "D8/03.Q11: no zone → UTC with user_timezone \"UTC\" and is_dst false (legacy: null/\"local\"/null, which the node's strict DateContext rejects); an invalid zone falls back to UTC instead of raising; UTC±H:MM keeps its minutes (legacy dropped them, so the oracle is legacy Asia/Kolkata at +05:30)"},
	{"modifier-local-single-instant", "§8.2: a time modifier sets the LOCAL clock time on its date and yields one instant (legacy replaced the hour on the UTC instant and kept the bare midnight; at_noon/at_breakfast parsed as midnight; a lone modifier resolved to nothing)"},
	{"vocabulary-gap", "§8.7 / D40 03.Q9: after_dinner, during_breakfast, during_dinner were in DT_KEYS but unresolvable (they went to the LLM fallback); Go resolves them"},
	{"in-days-wall-clock", "D8 (DST-correct zones): in_N_days keeps the local wall time across a DST change (legacy added N×24 h); the legacy corpus has no in_N_days row, so TestCorrectedSpec covers it"},
}

var weekdayKeyRE = regexp.MustCompile(`^\.weekdays\.(next|last)_(monday|tuesday|wednesday|thursday|friday|saturday|sunday)\.(date|utc_start_of_day)$`)

// offsetAt is the zone's UTC offset in seconds at an instant.
func offsetAt(loc *time.Location, iso string) int {
	tm, err := time.Parse(isoLayout, iso)
	if err != nil {
		return 0
	}
	_, off := tm.In(loc).Zone()
	return off
}

// classifyContextField returns the divergence id that explains legacy != got at path, after
// checking the Go value with the rule's oracle; "" when nothing explains it.
func classifyContextField(c *Context, tree map[string]string, path, legacy, got string) string {
	if m := weekdayKeyRE.FindStringSubmatch(path); m != nil {
		// Oracle: the date is the named weekday, inside weeks.next_week / last_week.
		date := unquote(tree[".weekdays."+m[1]+"_"+m[2]+".date"])
		d, err := time.Parse("2006-01-02", date)
		if err != nil || strings.ToLower(d.Weekday().String()) != m[2] {
			return ""
		}
		for i := 0; i < 7; i++ {
			if unquote(tree[".weeks."+m[1]+"_week["+pyjson.Repr(i)+"].date"]) == date {
				if m[3] == "utc_start_of_day" && unquote(got) != (civil{y: d.Year(), m: d.Month(), d: d.Day()}).instant(c.loc) {
					return ""
				}
				return "weekday-off-by-one"
			}
		}
		return ""
	}
	if strings.HasPrefix(path, ".years.next_year") || strings.HasPrefix(path, ".years.last_year") {
		want := c.now.Year() + 1
		if strings.HasPrefix(path, ".years.last_year") {
			want = c.now.Year() - 1
		}
		if strings.HasPrefix(unquote(got), pyjson.Repr(want)) {
			return "year-365"
		}
		// A utc_start_of_day may also straddle a DST change; fall through to that rule.
	}
	if strings.HasSuffix(path, "utc_start_of_day") || strings.HasSuffix(path, ".datetime") || strings.HasPrefix(path, ".time_expressions.") {
		lt, err1 := time.Parse(isoLayout, unquote(legacy))
		gt, err2 := time.Parse(isoLayout, unquote(got))
		if err1 != nil || err2 != nil {
			return ""
		}
		_, nowOff := c.now.Zone()
		delta := offsetAt(c.loc, unquote(got)) - nowOff
		// Oracle: legacy used now's offset; Go uses the date's. They differ by exactly that.
		if delta != 0 && int(lt.Sub(gt).Seconds()) == delta {
			if strings.HasSuffix(path, "utc_start_of_day") {
				if h, m, _ := gt.In(c.loc).Clock(); h != 0 || m != 0 {
					return ""
				}
			}
			return "dst-stale-offset"
		}
	}
	return ""
}

// G2: generate_date_context_object for 12 instants × 6 zones.
func TestDateContextAgainstLegacy(t *testing.T) {
	rows := loadDates(t, "date_context.json").([]any)
	legacyByKey := map[string]any{}
	for _, r := range rows {
		zone, _ := og(r, "timezone").(string)
		legacyByKey[og(r, "now").(string)+"|"+zone] = r
	}
	exact := 0
	counts := map[string]int{}
	for _, r := range rows {
		now := mustTime(t, og(r, "now").(string))
		zone, hasZone := og(r, "timezone").(string)
		c := New(now, zone)
		got := treeOf(c)

		ref := r
		switch {
		case _isErr(r):
			// Legacy raised; Go falls back to UTC: identical to the UTC context.
			if pyjson.Dumps(c.Object(), true) != pyjson.Dumps(New(now, "UTC").Object(), true) {
				t.Errorf("%s %s: invalid zone must fall back to UTC", og(r, "now"), zone)
			}
			ref = legacyByKey[og(r, "now").(string)+"|UTC"]
			counts["zone-fallback"]++
		case zone == "UTC+05:30":
			ref = legacyByKey[og(r, "now").(string)+"|Asia/Kolkata"]
		}
		want := map[string]string{}
		flatten("", og(ref, "date_context"), want)
		if len(want) != len(got) {
			t.Errorf("%s %v: %d fields, legacy %d", og(r, "now"), zone, len(got), len(want))
		}
		for path, lv := range want {
			gv, ok := got[path]
			if !ok {
				t.Errorf("%s %v: missing %s", og(r, "now"), zone, path)
				continue
			}
			if gv == lv {
				exact++
				continue
			}
			if strings.HasPrefix(path, ".timezone.") {
				// zone-fallback oracle: always a string zone and a bool is_dst.
				wantTZ := map[string]string{"": "UTC", "UTC+05:30": "UTC+05:30", "Not/AZone": "UTC"}[zone]
				if !hasZone {
					wantTZ = "UTC"
				}
				switch path {
				case ".timezone.user_timezone", ".timezone.current_timezone":
					if unquote(gv) == wantTZ {
						counts["zone-fallback"]++
						continue
					}
				case ".timezone.is_dst":
					if gv == "false" {
						counts["zone-fallback"]++
						continue
					}
				}
			}
			if id := classifyContextField(c, got, path, lv, gv); id != "" {
				counts[id]++
				continue
			}
			t.Errorf("%s %v %s: got %s, legacy %s (no divergence rule explains it)", og(r, "now"), zone, path, gv, lv)
		}
	}
	t.Logf("G2: %d fields equal to legacy; intended divergences: %v", exact, counts)
}

func _isErr(r any) bool { _, ok := r.(*pyjson.Object).Get("error"); return ok }

var modifierKeys = map[string]bool{}

func init() {
	for k := range namedTimes {
		modifierKeys[k] = true
	}
}

func isModifierKey(k string) bool {
	_, _, ok := modifier(NormalizeKey(k))
	return ok || modifierKeys[NormalizeKey(k)]
}

// G3: resolve_date_keys for every vocabulary key, in_* forms and combinations, per instant,
// in UTC and New York.
func TestResolutionAgainstLegacy(t *testing.T) {
	rows := loadDates(t, "resolution.json").([]any)
	exact := 0
	counts := map[string]int{}
	for _, r := range rows {
		now := mustTime(t, og(r, "now").(string))
		zone := og(r, "timezone").(string)
		c := New(now, zone)
		var keys []string
		for _, k := range og(r, "keys").([]any) {
			keys = append(keys, k.(string))
		}
		got, gotUnres := c.Resolve(keys)
		var want, wantUnres []string
		for _, v := range og(r, "resolved").([]any) {
			want = append(want, v.(string))
		}
		for _, v := range og(r, "unresolved").([]any) {
			wantUnres = append(wantUnres, v.(string))
		}
		if strings.Join(got, ",") == strings.Join(want, ",") && strings.Join(gotUnres, ",") == strings.Join(wantUnres, ",") {
			exact++
			continue
		}
		id := classifyResolution(t, c, keys, got, gotUnres, want)
		if id == "" {
			t.Errorf("%s %s %v: got %v %v, legacy %v %v (no divergence rule explains it)", og(r, "now"), zone, keys, got, gotUnres, want, wantUnres)
			continue
		}
		counts[id]++
	}
	t.Logf("G3: %d/%d rows equal to legacy; intended divergences: %v", exact, len(rows), counts)
}

func classifyResolution(t *testing.T, c *Context, keys, got, gotUnres, legacy []string) string {
	t.Helper()
	if len(gotUnres) > 0 && len(got) == 0 {
		return ""
	}
	if len(keys) == 1 {
		switch k := NormalizeKey(keys[0]); k {
		case "after_dinner", "during_breakfast", "during_dinner":
			hh := map[string]int{"after_dinner": 19, "during_breakfast": 8, "during_dinner": 18}[k]
			if len(legacy) == 0 && len(got) == 1 && got[0] == c.today().at(hh, 0).instant(c.loc) {
				return "vocabulary-gap"
			}
			return ""
		}
	}
	var mods, plain []string
	for _, k := range keys {
		if isModifierKey(k) {
			mods = append(mods, NormalizeKey(k))
		} else {
			plain = append(plain, k)
		}
	}
	if len(mods) > 0 {
		// Oracle: every instant sits at a modifier's local clock time, on exactly the local
		// dates the plain keys resolve to (today when there are none).
		times := map[[2]int]bool{}
		for _, m := range mods {
			hh, mm, _ := modifier(m)
			times[[2]int{hh, mm}] = true
		}
		wantDates := map[string]bool{}
		if len(plain) == 0 {
			wantDates[c.today().dateString()] = true
		} else {
			base, _ := c.Resolve(plain)
			for _, b := range base {
				tm, _ := time.Parse(isoLayout, b)
				wantDates[tm.In(c.loc).Format("2006-01-02")] = true
			}
		}
		gotDates := map[string]bool{}
		for _, g := range got {
			tm, err := time.Parse(isoLayout, g)
			if err != nil {
				return ""
			}
			lt := tm.In(c.loc)
			if !times[[2]int{lt.Hour(), lt.Minute()}] {
				return ""
			}
			gotDates[lt.Format("2006-01-02")] = true
		}
		if len(gotDates) != len(wantDates) {
			return ""
		}
		for d := range wantDates {
			if !gotDates[d] {
				return ""
			}
		}
		if len(got) != len(wantDates)*len(times) {
			return ""
		}
		return "modifier-local-single-instant"
	}
	if len(keys) > 1 && len(got) == len(legacy) {
		// Several plain keys: each differing value must come from one of the keys' context
		// values and be explained by a context rule.
		tree := treeOf(c)
		id := ""
		for i := range got {
			if got[i] == legacy[i] {
				continue
			}
			id = ""
			for _, k := range plain {
				path := contextPathFor(tree, NormalizeKey(k), i, got[i])
				if path == "" {
					for j := 0; j < 7 && path == ""; j++ {
						path = contextPathFor(tree, NormalizeKey(k), j, got[i])
					}
				}
				if path != "" {
					id = classifyContextField(c, tree, path, pyjson.Dumps(legacy[i], true), pyjson.Dumps(got[i], true))
				}
				if id != "" {
					break
				}
			}
			if id == "" {
				return ""
			}
		}
		return id
	}
	if len(keys) == 1 {
		k := NormalizeKey(keys[0])
		if strings.HasPrefix(k, "in_") && strings.Contains(k, "_days") {
			// Oracle: same local wall time as now, N local days later.
			tm, _ := time.Parse(isoLayout, got[0])
			lt := tm.In(c.loc)
			if lt.Hour() == c.now.Hour() && lt.Minute() == c.now.Minute() {
				return "in-days-wall-clock"
			}
			return ""
		}
		// A context value already classified field by field: the resolver must agree with
		// the Go context (Lookup) and differ from legacy only for a context rule.
		vals, ok := c.Lookup(k)
		if !ok || strings.Join(vals, ",") != strings.Join(got, ",") || len(legacy) != len(got) {
			return ""
		}
		tree := treeOf(c)
		for i := range got {
			if got[i] == legacy[i] {
				continue
			}
			path := contextPathFor(tree, k, i, got[i])
			if path == "" {
				return ""
			}
			if id := classifyContextField(c, tree, path, pyjson.Dumps(legacy[i], true), pyjson.Dumps(got[i], true)); id != "" {
				return id
			}
			return ""
		}
	}
	return ""
}

// contextPathFor finds the context JSON path that holds a flat key's i-th value.
func contextPathFor(tree map[string]string, key string, i int, value string) string {
	candidates := []string{
		".weekdays." + key + ".utc_start_of_day",
		".relative_dates." + key + ".utc_start_of_day",
		".time_expressions." + strings.ReplaceAll(key, "_", " "),
	}
	for _, bucket := range []string{"weekend", "weeks", "months", "years"} {
		candidates = append(candidates, "."+bucket+"."+key+"["+pyjson.Repr(i)+"].utc_start_of_day")
	}
	if strings.HasPrefix(key, "this_") {
		for j := 0; j < 7; j++ {
			candidates = append(candidates, ".weeks.this_week["+pyjson.Repr(j)+"].utc_start_of_day")
		}
	}
	keys := make([]string, 0, len(tree))
	for k := range tree {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, p := range candidates {
		if unquote(tree[p]) == value {
			return p
		}
	}
	// at_7_30pm style keys: the time expression is spelled "at 7:30pm".
	for _, p := range keys {
		if strings.HasPrefix(p, ".time_expressions.") && NormalizeKey(strings.TrimPrefix(p, ".time_expressions.")) == key && unquote(tree[p]) == value {
			return p
		}
	}
	return ""
}

func TestNormalizeGolden(t *testing.T) {
	m := loadDates(t, "normalize.json").(*pyjson.Object)
	for _, k := range m.Keys() {
		v, _ := m.Get(k)
		if got := NormalizeKey(k); got != v.(string) {
			t.Errorf("NormalizeKey(%q) = %q, want %q", k, got, v)
		}
	}
}

// The vocabulary is one constant (D40 03.Q9): the legacy 64 keys, and every one resolves.
func TestVocabularyClosed(t *testing.T) {
	var legacy []string
	for _, k := range og(loadDates(t, "vocabulary.json"), "static_keys").([]any) {
		legacy = append(legacy, k.(string))
	}
	if strings.Join(legacy, "|") != strings.Join(Vocabulary, "|") {
		t.Fatalf("Vocabulary differs from the legacy static keys")
	}
	for _, zone := range []string{"UTC", "America/New_York", "Asia/Kolkata", "UTC+05:30", ""} {
		for _, now := range []string{"2026-10-06T03:15:00Z", "2027-03-14T06:30:00Z", "2028-02-29T12:00:00Z"} {
			c := New(mustTime(t, now), zone)
			for _, k := range Vocabulary {
				res, unres := c.Resolve([]string{k})
				if len(res) == 0 || len(unres) > 0 {
					t.Errorf("%s %s: %q does not resolve (%v %v)", zone, now, k, res, unres)
				}
			}
		}
	}
}

// G4: is_iso_datetime and the engine's _try_fix_iso_dates.
func TestISOGuardGolden(t *testing.T) {
	fx := loadDates(t, "iso_guard.json")
	// The fixture ran on the exporter's Python; prod's command-center pins 3.11 (Dockerfile),
	// whose fromisoformat rejects hour 24 (3.12+ accepts it). Verified with python@3.11.
	py311 := map[string]bool{"2025-01-15T24:00:00Z": false}
	for _, row := range og(fx, "is_iso_datetime").([]any) {
		v := og(row, "value").(string)
		want := og(row, "is_iso").(bool)
		if w, ok := py311[v]; ok {
			want = w
		}
		if got := IsISODatetime(v); got != want {
			t.Errorf("IsISODatetime(%q) = %v, want %v", v, got, want)
		}
	}
	cases := og(fx, "try_fix_iso_dates").([]any)
	for _, row := range cases {
		c := New(mustTime(t, og(row, "now").(string)), og(row, "timezone").(string))
		var args []string
		for _, call := range og(row, "calls").([]any) {
			args = append(args, og(og(call, "function"), "arguments").(string))
		}
		status, out := c.FixISODates(args)
		if status != og(row, "status").(string) {
			t.Errorf("%s %s %s: status %s want %s", og(row, "now"), og(row, "timezone"), og(row, "name"), status, og(row, "status"))
		}
		for i, call := range og(row, "after").([]any) {
			if w := og(og(call, "function"), "arguments").(string); out[i] != w {
				t.Errorf("%s %s %s call %d:\n got %s\nwant %s", og(row, "now"), og(row, "timezone"), og(row, "name"), i, out[i], w)
			}
		}
	}
	t.Logf("G4: %d is_iso values, %d guard cases", len(og(fx, "is_iso_datetime").([]any)), len(cases))
}

func TestDivergenceTableDocumented(t *testing.T) {
	for _, d := range divergenceTable {
		if d.id == "" || d.reason == "" {
			t.Errorf("undocumented divergence %+v", d)
		}
	}
}
