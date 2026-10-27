package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/timers"
)

// Timers are set_timer, check_timers and cancel_timer (commands/timer_command.py,
// check_timers_command.py, cancel_timer_command.py) on the node's timer service, with their
// fast paths: "set a timer for 5 minutes", "how much time is left" and "cancel the pasta timer"
// run with no model.
type Timers struct {
	Service *timers.Service
	Store   *nodesettings.Store // the fast-path switches; nil: all on
}

var timerNames = []string{"set_timer", "check_timers", "cancel_timer"}

// timerFastPaths are the commands' patterns in N:'s discovery order (cancel_timer,
// check_timers, then timer_command), so "cancel the 5 minute timer" cancels rather than sets.
var timerFastPaths = []struct {
	name string
	ps   []pattern
}{{"cancel_timer", cancelPatterns}, {"check_timers", checkPatterns}, {"set_timer", setPatterns}}

func (t *Timers) Available(context.Context) []node.ToolDef {
	out := make([]node.ToolDef, 0, len(timerNames))
	for _, n := range timerNames {
		out = append(out, def(n))
	}
	return out
}

func (t *Timers) Call(_ context.Context, name string, args map[string]any, _ node.Request) (map[string]any, bool) {
	if !slices.Contains(timerNames, name) {
		return nil, false
	}
	if out := validated(def(name), args); out != nil {
		return out, true
	}
	switch name {
	case "set_timer":
		return t.set(args), true
	case "check_timers":
		return t.check(args), true
	case "cancel_timer":
		return t.cancel(args), true
	}
	return nil, false
}

// Direct runs the first command whose fast path claims what was said (timerFastPaths' order).
func (t *Timers) Direct(ctx context.Context, said string) (map[string]any, bool) {
	for _, c := range timerFastPaths {
		if out, ok := directBy(ctx, t.Store, c.name, c.ps, said, func(a map[string]any) map[string]any {
			out, _ := t.Call(ctx, c.name, a, node.Request{})
			return out
		}); ok {
			return out, true
		}
	}
	return nil, false
}

// Match is a fast path's claim: the command and its arguments.
type Match struct {
	Command string
	Args    map[string]any
}

// TimersMatch is the command and arguments Direct would run for what was said, with every
// pattern on (for comparing the fast paths with N:'s pre_route).
func TimersMatch(said string) (Match, bool) {
	for _, c := range timerFastPaths {
		for _, p := range c.ps {
			if args, ok := p.match(said); ok {
				return Match{c.name, args}, true
			}
		}
	}
	return Match{}, false
}

// FastPaths are the commands' patterns for the app's switches.
func (t *Timers) FastPaths(command string) []nodesettings.FastPath {
	switch command {
	case "set_timer":
		return patternList(setPatterns)
	case "check_timers":
		return patternList(checkPatterns)
	case "cancel_timer":
		return patternList(cancelPatterns)
	}
	return nil
}

// --- set_timer ---

func (t *Timers) set(args map[string]any) map[string]any {
	raw, present := args["duration_seconds"]
	label := labelArg(args)
	if !present || raw == nil {
		return failure("Duration is required", map[string]any{"error": "missing_duration"})
	}
	secs, err := pyInt(raw)
	if err != nil {
		return failure("Invalid parameters: "+err.Error(), map[string]any{"error": err.Error(), "parameters_received": args})
	}
	if secs <= 0 {
		return failure("Duration must be positive", map[string]any{"error": "invalid_duration", "duration_seconds": secs})
	}
	id, err := t.Service.Set(secs, label)
	if err != nil {
		return failure("Timer error: the timer could not be set", map[string]any{"error": "timer_unavailable", "parameters_received": args})
	}
	text := formatDuration(secs)
	msg := "Timer set for " + text
	var l any
	if label != nil {
		l = *label
		msg += " for " + *label
	}
	return finish(map[string]any{"timer_id": id, "duration_seconds": secs, "duration_text": text, "label": l, "message": msg},
		true, false)
}

// labelArg is the label argument: nil when absent or empty (Python's falsy label).
func labelArg(args map[string]any) *string {
	if s, ok := args["label"].(string); ok && s != "" {
		return &s
	}
	return nil
}

// pyInt is Python's int() of a JSON argument: an integer, a whole or fractional number
// (truncated), or a string of digits.
func pyInt(v any) (int, error) {
	switch x := v.(type) {
	case float64:
		return int(math.Trunc(x)), nil
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n), nil
		}
		f, err := x.Float64()
		return int(math.Trunc(f)), err
	case int:
		return x, nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return 0, fmt.Errorf("invalid literal for int() with base 10: %q", x)
		}
		return n, nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	}
	return 0, fmt.Errorf("int() argument must be a string or a number, not %T", v)
}

var timeUnits = map[string]int{
	"hour": 3600, "hours": 3600, "hr": 3600, "hrs": 3600,
	"minute": 60, "minutes": 60, "min": 60, "mins": 60,
	"second": 1, "seconds": 1, "sec": 1, "secs": 1,
}

// timeChunkRe is _TIME_CHUNK_RE: the units longest first, so "minutes" wins over "min".
var timeChunkRe = regexp.MustCompile(`(?i)(\d+)\s*(?:and\s+)?(minutes|seconds|minute|second|hours|hour|mins|secs|hrs|min|sec|hr)`)

var informalDurations = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`(?i)\bhalf\s+an?\s+hour\b`), "30 minutes"},
	{regexp.MustCompile(`(?i)\ba\s+half\s+hour\b`), "30 minutes"},
	{regexp.MustCompile(`(?i)\bquarter\s+(?:of\s+an?\s+)?hour\b`), "15 minutes"},
	{regexp.MustCompile(`(?i)\ban?\s+hour\b`), "60 minutes"},
}

var labelStripWords = map[string]bool{
	"set": true, "a": true, "an": true, "the": true, "timer": true, "for": true, "remind": true, "me": true,
	"in": true, "wake": true, "up": true, "let": true, "know": true, "notify": true, "and": true,
	"please": true, "half": true, "quarter": true, "about": true,
}

var standaloneNumberRe = regexp.MustCompile(`\b\d+\b`)

// setTimerMatch is TimerCommand.pre_route for one trigger: a duration in what was said (summed
// when compound) and the label left once the time words are taken out.
func setTimerMatch(said string) (map[string]any, bool) {
	text := strings.ToLower(strings.TrimSpace(said))
	for _, d := range informalDurations {
		text = d.re.ReplaceAllString(text, d.with)
	}
	total := 0
	for _, m := range timeChunkRe.FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[1])
		total += n * timeUnits[strings.ToLower(m[2])]
	}
	if total <= 0 {
		return nil, false
	}
	args := map[string]any{"duration_seconds": total}
	if l := timerLabel(text); l != "" {
		args["label"] = l
	}
	return args, true
}

// timerLabel is _extract_label, which also drops the punctuation speech-to-text adds: on N:
// "Set a timer for 5 minutes." was labelled ".".
func timerLabel(text string) string {
	cleaned := standaloneNumberRe.ReplaceAllString(timeChunkRe.ReplaceAllString(text, ""), "")
	var words []string
	for _, w := range strings.Fields(cleaned) {
		w = strings.Trim(w, ".,!?;:\"")
		if w != "" && !labelStripWords[strings.ToLower(w)] {
			words = append(words, w)
		}
	}
	return strings.Join(words, " ")
}

// setTriggers is _FAST_PATH_BY_TRIGGER: what was said belongs to the pattern of the first
// trigger it contains (so turning "timer.set" off doesn't hand "wake me" phrases to it).
var setTriggers = []struct{ phrase, id string }{
	{"timer", "timer.set"}, {"wake me", "timer.wake_me"}, {"let me know", "timer.notify_me"}, {"notify me", "timer.notify_me"},
}

// setPattern is one of TimerCommand's fast paths: what was said whose first trigger is id's.
func setPattern(id, description, example string) pattern {
	return pattern{id: id, description: description, example: example, match: func(said string) (map[string]any, bool) {
		text := strings.ToLower(said)
		for _, tr := range setTriggers {
			if strings.Contains(text, tr.phrase) {
				if tr.id != id {
					return nil, false
				}
				return setTimerMatch(said)
			}
		}
		return nil, false
	}}
}

var setPatterns = []pattern{
	setPattern("timer.set", "Set a timer from phrases containing 'timer' (e.g. 'set a 5 minute timer', 'egg timer 10 minutes')",
		"set a timer for 5 minutes"),
	setPattern("timer.wake_me", "Set a timer from 'wake me' phrases (e.g. 'wake me in 20 minutes')", "wake me in 20 minutes"),
	setPattern("timer.notify_me", "Set a timer from 'notify me' / 'let me know' phrases", "let me know in 30 minutes"),
}

// formatDuration is TimerCommand._format_duration.
func formatDuration(secs int) string {
	if secs < 60 {
		return plural(secs, "second")
	}
	var parts []string
	if h := secs / 3600; h > 0 {
		parts = append(parts, plural(h, "hour"))
	}
	if m := secs % 3600 / 60; m > 0 {
		parts = append(parts, plural(m, "minute"))
	}
	if s := secs % 60; s > 0 {
		parts = append(parts, plural(s, "second"))
	}
	return joinParts(parts)
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

// joinParts is "a", "a and b" or "a, b, and c".
func joinParts(parts []string) string {
	switch len(parts) {
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	}
	return parts[0] + ", " + parts[1] + ", and " + parts[2]
}

// --- check_timers ---

var (
	checkGlobalRe  = regexp.MustCompile(`(?i)^\s*(?:how\s+much\s+time(?:\s+is)?\s+left|how\s+long\s+(?:do\s+i\s+have|is\s+left)|time\s+(?:left|remaining)|what\s+timers?\s+(?:are\s+running|do\s+i\s+have)|check\s+(?:my\s+)?timers?|timer\s+status)\s*[?.!]*$`)
	checkLabeledRe = regexp.MustCompile(`(?i)^\s*(?:how\s+(?:long|much)\s+(?:until|on|left)|check|what(?:'?s|\s+is)\s+(?:the\s+)?status\s+of|time\s+(?:left|remaining)\s+on|is)\s+(?:the\s+|my\s+)?(?P<label>[a-zA-Z][a-zA-Z\s\-]+?)\s+timer(?:\s+still\s+running)?\s*[?.!]*$`)
)

// regexPattern is a declared FastPathPattern: the regex searched in what was said, and the
// handler's arguments for the match.
func regexPattern(id, description, example string, re *regexp.Regexp, args func(m []string) (map[string]any, bool)) pattern {
	return pattern{id: id, description: description, example: example, match: func(said string) (map[string]any, bool) {
		m := re.FindStringSubmatch(said)
		if m == nil {
			return nil, false
		}
		return args(m)
	}}
}

// labelGroup is a match's label group, stripped.
func labelGroup(re *regexp.Regexp, m []string) string {
	return strings.TrimSpace(m[re.SubexpIndex("label")])
}

var checkPatterns = []pattern{
	regexPattern("check_timers.labeled", "Bypass LLM for 'check the <label> timer' / 'how long until the <label> timer'",
		"check the pasta timer", checkLabeledRe, func(m []string) (map[string]any, bool) {
			l := labelGroup(checkLabeledRe, m)
			return map[string]any{"label": l}, l != ""
		}),
	regexPattern("check_timers.global", "Bypass LLM for 'how much time is left' / 'what timers are running' / 'timer status'",
		"how much time is left", checkGlobalRe, func([]string) (map[string]any, bool) { return map[string]any{}, true }),
}

// timerLabels are the active timers by label, or "timer <id>" for an unlabelled one.
func timerLabels(active []map[string]any) []string {
	out := make([]string, 0, len(active))
	for _, a := range active {
		if l, _ := a["label"].(*string); l != nil && *l != "" {
			out = append(out, *l)
		} else {
			out = append(out, fmt.Sprintf("timer %v", a["timer_id"]))
		}
	}
	return out
}

func labelOf(a map[string]any) any {
	if l, _ := a["label"].(*string); l != nil {
		return *l
	}
	return nil
}

func (t *Timers) check(args map[string]any) map[string]any {
	label := labelArg(args)
	active := t.Service.Active()
	if len(active) == 0 {
		return finish(map[string]any{"has_timers": false, "timers": []any{}, "count": 0, "message": "No active timers"}, true, false)
	}
	if label != nil {
		if id := t.Service.FindByLabel(*label); id != "" {
			if info := t.Service.Get(id); info != nil {
				rem, _ := info["remaining_seconds"].(int)
				text := formatRemaining(rem)
				name := *label
				if l, _ := info["label"].(*string); l != nil && *l != "" {
					name = *l
				}
				return finish(map[string]any{"has_timers": true, "count": 1,
					"timers":  []any{map[string]any{"timer_id": id, "label": name, "remaining_seconds": rem, "remaining_text": text}},
					"message": "The " + name + " timer has " + text + " remaining"}, true, false)
			}
		}
		labels := timerLabels(active)
		return finish(map[string]any{"has_timers": true, "timers": []any{}, "count": len(active),
			"message": "No timer found with label '" + *label + "'", "available_timers": labels,
			"suggestion": "Available timers: " + strings.Join(labels, ", ")}, true, false)
	}
	details := make([]any, 0, len(active))
	var parts []string
	for _, a := range active {
		rem, _ := a["remaining_seconds"].(int)
		text := formatRemaining(rem)
		details = append(details, map[string]any{"timer_id": a["timer_id"], "label": labelOf(a), "remaining_seconds": rem, "remaining_text": text})
		if l, ok := labelOf(a).(string); ok && l != "" {
			parts = append(parts, "the "+l+" timer has "+text)
		} else {
			parts = append(parts, "a timer has "+text)
		}
	}
	msg := "You have " + strings.Join(parts, ", and ") + " remaining"
	if len(details) == 1 {
		if l, ok := labelOf(active[0]).(string); ok && l != "" {
			msg = "The " + l + " timer has " + formatRemaining(active[0]["remaining_seconds"].(int)) + " remaining"
		} else {
			msg = "Your timer has " + formatRemaining(active[0]["remaining_seconds"].(int)) + " remaining"
		}
	}
	return finish(map[string]any{"has_timers": true, "timers": details, "count": len(details), "message": msg}, true, false)
}

// formatRemaining is CheckTimersCommand._format_remaining: seconds only under an hour.
func formatRemaining(secs int) string {
	if secs < 60 {
		return plural(secs, "second")
	}
	var parts []string
	h, m, s := secs/3600, secs%3600/60, secs%60
	if h > 0 {
		parts = append(parts, plural(h, "hour"))
	}
	if m > 0 {
		parts = append(parts, plural(m, "minute"))
	}
	if s > 0 && h == 0 {
		parts = append(parts, plural(s, "second"))
	}
	return joinParts(parts)
}

// --- cancel_timer ---

var (
	cancelAllRe     = regexp.MustCompile(`(?i)^\s*(?:cancel|stop|clear|remove|delete)\s+all(?:\s+(?:my|the))?\s+timers?\s*[?.!]*$`)
	cancelLabeledRe = regexp.MustCompile(`(?i)^\s*(?:cancel|stop|remove|delete|never\s+mind)\s+(?:the\s+|my\s+)?(?P<label>[a-zA-Z][a-zA-Z\s\-]+?)\s+timer\s*[?.!]*$`)
	cancelNoneRe    = regexp.MustCompile(`(?i)^\s*(?:cancel|stop|never\s+mind)\s+(?:my\s+|the\s+)?timer\s*[?.!]*$`)
)

// cancelPatterns are CancelTimerCommand's fast paths, all and no-label before labeled so
// "cancel timer" isn't read as a label.
var cancelPatterns = []pattern{
	regexPattern("cancel_timer.all", "Bypass LLM for 'cancel all timers' / 'stop all my timers'", "cancel all timers",
		cancelAllRe, func([]string) (map[string]any, bool) { return map[string]any{"label": "all"}, true }),
	regexPattern("cancel_timer.none", "Bypass LLM for 'cancel my timer' / 'stop the timer' (no specific label)", "cancel my timer",
		cancelNoneRe, func([]string) (map[string]any, bool) { return map[string]any{}, true }),
	regexPattern("cancel_timer.labeled", "Bypass LLM for 'cancel the <label> timer' / 'stop the <label> timer'", "cancel the pasta timer",
		cancelLabeledRe, func(m []string) (map[string]any, bool) {
			l := labelGroup(cancelLabeledRe, m)
			switch strings.ToLower(l) {
			case "", "all", "my", "the":
				return nil, false
			}
			return map[string]any{"label": l}, true
		}),
}

func (t *Timers) cancel(args map[string]any) map[string]any {
	label := labelArg(args)
	active := t.Service.Active()
	if len(active) == 0 {
		return finish(map[string]any{"cancelled": false, "message": "No active timers to cancel", "timer_count": 0}, true, false)
	}
	if label != nil && strings.ToLower(*label) == "all" {
		n := t.Service.ClearAll()
		return finish(map[string]any{"cancelled": true, "cancelled_count": n, "message": "Cancelled all " + strconv.Itoa(n) + " timer" +
			map[bool]string{true: "", false: "s"}[n == 1]}, true, false)
	}
	if label != nil {
		if id := t.Service.FindByLabel(*label); id != "" {
			info := t.Service.Get(id)
			t.Service.Cancel(id)
			var l any = *label
			if info != nil {
				l = labelOf(info)
			}
			return finish(map[string]any{"cancelled": true, "timer_id": id, "label": l, "message": "Cancelled the " + *label + " timer"}, true, false)
		}
		labels := timerLabels(active)
		return finish(map[string]any{"cancelled": false, "message": "No timer found with label '" + *label + "'",
			"available_timers": labels, "suggestion": "Available timers: " + strings.Join(labels, ", ")}, true, false)
	}
	if len(active) == 1 {
		a := active[0]
		id, _ := a["timer_id"].(string)
		t.Service.Cancel(id)
		msg := "Cancelled your timer"
		if l, ok := labelOf(a).(string); ok && l != "" {
			msg = "Cancelled the " + l + " timer"
		}
		return finish(map[string]any{"cancelled": true, "timer_id": id, "label": labelOf(a), "message": msg}, true, false)
	}
	descs := make([]string, 0, len(active))
	for i, a := range active {
		rem, _ := a["remaining_seconds"].(int)
		descs = append(descs, timerLabels(active)[i]+" ("+cancelRemaining(rem)+" remaining)")
	}
	return finish(map[string]any{"cancelled": false, "message": "Which timer would you like to cancel?", "active_timers": descs,
		"timer_count": len(active), "needs_clarification": true}, true, true)
}

// cancelRemaining is CancelTimerCommand._format_remaining: hours and minutes, no seconds past
// the first minute.
func cancelRemaining(secs int) string {
	if secs < 60 {
		return plural(secs, "second")
	}
	var parts []string
	if h := secs / 3600; h > 0 {
		parts = append(parts, plural(h, "hour"))
	}
	if m := secs % 3600 / 60; m > 0 {
		parts = append(parts, plural(m, "minute"))
	}
	return strings.Join(parts, " and ")
}
