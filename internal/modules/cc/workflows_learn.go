package cc

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/computer"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

// Learning a workflow from a recording (docs/agent/COMPUTER_CONTROL.md, plan P3 piece 30):
// every browser or desktop recording an agent stores (piece 29) queues cc.workflow.learn. The job numbers
// the trace's events, seeds the values that may change from run to run (dates, months, file
// names, amounts in typed values and URLs) and asks the background model to group the events
// into steps, name the workflow and pick the parameters among the seeded candidates. What the
// model answers only names and groups: each step's action and element come from the recorded
// event, and its risk from computeruse.Classify. The draft is stored as the recording owner's
// (not shared), and a card asks them to review it on the admin's /workflows/<id>.
//
// Desktop recordings (plan P3 pieces 34-35) have another event shape (parseDesktopTrace) and are
// learnt the same way: the model sees each event's app, window and element, and each step
// targets the recorded app and window (buildDesktopWorkflow), recording the AX/AT-SPI element when the event had one.

const (
	learnJob          = "cc.workflow.learn"
	learnCardCategory = "workflow_learned"

	// maskedValue stands for a value the recorder masked (a password, a card number): the model
	// sees it, and the step types it, as this; the replay asks for the real one.
	maskedValue = computer.MaskedValue
	// recordedRef is the ref of a learnt press, select or set_value: there is no snapshot at
	// learning time, so a replay (piece 31) finds the element again from the step's Element.
	recordedRef = computer.RecordedRef

	learnMaxEvents    = 300  // events in the prompt; the rest of a longer trace is dropped
	learnMaxValue     = 200  // runes of a value or an element name in the prompt
	learnMaxNarration = 1000 // runes of the narration in the prompt
	learnMaxTokens    = 4000
)

// traceEvent is one event of a browser trace (the agent's browser.RecordEvent; "filename" is
// accepted for a download too).
type traceEvent struct {
	Kind     string        `json:"kind"`          // click, input, change, submit, key, navigate, download
	TS       int64         `json:"ts,omitempty"`  // ms, the page's clock
	Tab      int64         `json:"tab,omitempty"` // the browser's tab id
	URL      string        `json:"url,omitempty"`
	Key      string        `json:"key,omitempty"`
	Element  *traceElement `json:"element,omitempty"`
	Value    string        `json:"value,omitempty"`
	Masked   bool          `json:"masked,omitempty"`
	Mime     string        `json:"mime,omitempty"`
	Filename string        `json:"filename,omitempty"`
	// ResultURL is where a click, submit or key took its tab (foldNavigations).
	ResultURL string `json:"-"`
}

type traceElement struct {
	Role         string `json:"role,omitempty"`
	Name         string `json:"name,omitempty"`
	Tag          string `json:"tag,omitempty"`
	Type         string `json:"type,omitempty"`
	Autocomplete string `json:"autocomplete,omitempty"`
	Selector     string `json:"selector,omitempty"`
	Frame        int    `json:"frame,omitempty"`
}

var traceKinds = []string{"click", "input", "change", "submit", "key", "navigate", "download"}

func (e traceEvent) typing() bool {
	return (e.Kind == "input" || e.Kind == "change") && e.Element != nil
}

// acts is whether e is something the user did that can take its tab to another page.
func (e traceEvent) acts() bool {
	return e.Kind == "click" || e.Kind == "submit" || e.Kind == "key"
}

// parseTrace reads a recording's trace: its known events, a password field's value dropped
// (the agent masks it already), navigations folded (foldNavigations), and each run of typing
// into one field kept as its last event (the extension reports input as the user types, then
// change).
func parseTrace(raw json.RawMessage) ([]traceEvent, error) {
	var all []traceEvent
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, fmt.Errorf("the trace is not a list of events: %w", err)
	}
	all = slices.DeleteFunc(all, func(e traceEvent) bool { return !slices.Contains(traceKinds, e.Kind) })
	var out []traceEvent
	for _, e := range foldSubmits(foldNavigations(all)) {
		if e.Element != nil && strings.EqualFold(e.Element.Type, "password") {
			e.Masked = true
		}
		if e.Masked {
			e.Value = ""
		}
		if n := len(out); n > 0 && e.typing() && out[n-1].typing() && *e.Element == *out[n-1].Element {
			out[n-1] = e
			continue
		}
		out = append(out, e)
	}
	if len(out) > learnMaxEvents {
		out = out[:learnMaxEvents]
	}
	return out, nil
}

// foldSubmits drops a form's submit that a click or a key in its tab caused (its submit button,
// Enter in a field), the page it opened becoming the action's: replaying the action submits the
// form again, so as a step of its own it would submit twice. Events are in time order
// (foldNavigations); a trace without times folds a submit right after the action.
func foldSubmits(evs []traceEvent) []traceEvent {
	var out []traceEvent
	last := map[int64]int{} // each tab's last event, when it is a click or a key: its index in out
	for _, e := range evs {
		i, ok := last[e.Tab]
		delete(last, e.Tab)
		if e.Kind == "submit" && ok && (out[i].TS == 0 || e.TS == 0 || e.TS-out[i].TS <= navFoldMS) {
			if out[i].ResultURL == "" {
				out[i].ResultURL = e.ResultURL
			}
			continue
		}
		if e.Kind == "click" || e.Kind == "key" {
			last[e.Tab] = len(out)
		}
		out = append(out, e)
	}
	return out
}

// navFoldMS is how soon after a click, submit or key in a tab a navigation in that tab is the
// action's doing (a link, a form, an app's pushState route), by the page's clock.
const navFoldMS = 1500

// foldNavigations keeps a navigation as an event only when the user opened the page themselves
// (typed, a bookmark): one an action in its tab caused becomes that action's ResultURL, and one
// to the URL its tab is already on (a reload, the browser reporting it again) is dropped. Events
// go in the order of their times: a navigation can reach the extension before the click or Enter
// that caused it (the page's events come by polling). A trace without times folds a navigation
// right after the action.
func foldNavigations(evs []traceEvent) []traceEvent {
	if !slices.ContainsFunc(evs, func(e traceEvent) bool { return e.TS == 0 }) {
		evs = slices.Clone(evs)
		slices.SortStableFunc(evs, func(a, b traceEvent) int { return cmp.Compare(a.TS, b.TS) })
	}
	var out []traceEvent
	at := map[int64]string{}  // the URL each tab is on
	action := map[int64]int{} // each tab's last click, submit or key: its index in out
	for _, e := range evs {
		if e.Kind != "navigate" {
			if e.acts() {
				action[e.Tab] = len(out)
			}
			if e.Kind != "download" && e.URL != "" {
				at[e.Tab] = e.URL
			}
			out = append(out, e)
			continue
		}
		if at[e.Tab] == e.URL {
			continue
		}
		at[e.Tab] = e.URL
		if i, ok := action[e.Tab]; ok {
			a := out[i]
			caused := i == len(out)-1 // no times: right after it
			if a.TS > 0 && e.TS > 0 {
				caused = e.TS-a.TS <= navFoldMS
			}
			if caused {
				out[i].ResultURL = e.URL
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// desktopEvent is one event of a desktop trace (the agent's computer.DesktopEvent, mirrored
// here so cc doesn't import the agent): where a browser event has a URL and a selector, it has
// the app, the focused window (Wayland only) and the element's AX path (AT-SPI on Linux, AX on
// macOS) when the recording had an accessibility context.
type desktopEvent struct {
	Seq     int64           `json:"seq,omitempty"`
	TS      int64           `json:"ts,omitempty"`  // ms since the epoch
	Kind    string          `json:"kind"`          // click, input, key (scroll is dropped)
	App     string          `json:"app,omitempty"` // the bundle id (macOS), else the window class
	Key     string          `json:"key,omitempty"`
	Element *desktopElement `json:"element,omitempty"`
	Value   string          `json:"value,omitempty"`
	Masked  bool            `json:"masked,omitempty"`
	Button  string          `json:"button,omitempty"` // click: right for a right click
	X       int             `json:"x,omitempty"`      // click: the global point
	Y       int             `json:"y,omitempty"`
	Window  *desktopWindow  `json:"window,omitempty"`
	// Context is "none" when the recording had no accessibility context: no element, and
	// nothing the recorder could mask.
	Context string `json:"context,omitempty"`
	// KeyframePoint: the event has a keyframe with its point marked, here in the JPEG's pixels
	// (Wayland); the agent uploads it after the trace (recording_keyframes.go).
	KeyframePoint *desktopPoint `json:"keyframe_point,omitempty"`
}

type desktopPoint struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type desktopElement struct {
	Role   string `json:"role,omitempty"`
	Name   string `json:"name,omitempty"`
	AXPath string `json:"ax_path,omitempty"`
	// Vision: the vision model named it from the click's keyframe (workflows_learn_sight.go),
	// with Confidence; Did is what it says the click did. Never read from a trace.
	Vision     bool    `json:"-"`
	Confidence float64 `json:"-"`
	Did        string  `json:"-"`
}

type desktopWindow struct {
	Class string `json:"class,omitempty"`
	Title string `json:"title,omitempty"`
	X     int    `json:"x,omitempty"`
	Y     int    `json:"y,omitempty"`
	W     int    `json:"w,omitempty"`
	H     int    `json:"h,omitempty"`
}

// desktopContextNone is a desktop event's Context without accessibility (computer.ContextNone).
const desktopContextNone = "none"

// desktopKinds are the desktop events learnt from; a scroll says nothing a replay can redo by
// its element, so it is dropped like an unknown kind.
var desktopKinds = []string{"click", "input", "key"}

// sameTarget is whether e and o were typed into the same place: the same element, else (no
// element) the same window and app.
func (e desktopEvent) sameTarget(o desktopEvent) bool {
	if (e.Element == nil) != (o.Element == nil) {
		return false
	}
	if e.Element != nil {
		return *e.Element == *o.Element && e.App == o.App
	}
	return e.App == o.App && (e.Window == nil) == (o.Window == nil) &&
		(e.Window == nil || (e.Window.Class == o.Window.Class && e.Window.Title == o.Window.Title))
}

// parseDesktopTrace reads a desktop recording's trace as parseTrace reads a browser one: its
// known events in order, a masked value dropped, and each run of typing into one place kept as
// its last event. Typing recorded without an accessibility context (Wayland with no AT-SPI) is
// treated as masked: the recorder couldn't tell a password field, so its value never reaches
// the model and a replay asks for it.
func parseDesktopTrace(raw json.RawMessage) ([]desktopEvent, error) {
	var all []desktopEvent
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, fmt.Errorf("the trace is not a list of desktop events: %w", err)
	}
	var out []desktopEvent
	for _, e := range all {
		if !slices.Contains(desktopKinds, e.Kind) {
			continue
		}
		if e.Kind == "input" && e.Context == desktopContextNone {
			e.Masked = true
		}
		if e.Masked {
			e.Value = ""
		}
		if n := len(out); n > 0 && e.Kind == "input" && out[n-1].Kind == "input" && e.sameTarget(out[n-1]) {
			out[n-1] = e
			continue
		}
		out = append(out, e)
	}
	if len(out) > learnMaxEvents {
		out = out[:learnMaxEvents]
	}
	return out, nil
}

// --- parameter candidates ---

// paramCandidate is a value in the trace that may change from one run to the next.
type paramCandidate struct {
	Event int    // the event's number (1-based, as in the prompt)
	Kind  string // date | month | number | file
	Value string
	Key   string // the URL query key it is the value of ("" elsewhere)
	From  string // typed | clicked | url | download
}

const monthNames = `january|february|march|april|may|june|july|august|september|october|november|december`

// candidatePatterns are tried in order; a later one doesn't match inside an earlier one's match
// ("statement_2026-09.csv" is a file, not a month).
var candidatePatterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"file", regexp.MustCompile(`(?i)\b[\w.-]+\.(?:csv|pdf|xlsx|xls|ods|ofx|qfx|qif|txt|json|xml|zip|docx|doc|png|jpe?g)\b`)},
	{"date", regexp.MustCompile(`(?i)\b(?:\d{4}-\d{2}-\d{2}|\d{1,2}/\d{1,2}/\d{2,4}|(?:` + monthNames +
		`|jan|feb|mar|apr|jun|jul|aug|sept|sep|oct|nov|dec)\.? \d{1,2}(?:, \d{4})?)\b`)},
	{"month", regexp.MustCompile(`(?i)\b(?:\d{4}-\d{2}|\d{1,2}/\d{4}|(?:` + monthNames + `)(?: \d{4})?)\b`)},
	{"number", regexp.MustCompile(`[$€£]\s?\d[\d,]*(?:\.\d{1,2})?|\b\d[\d,]*\.\d{2}\b`)},
}

// queryKeyKinds name a URL query value's kind by its key when the value says nothing itself
// (?month=09).
var queryKeyKinds = []struct{ word, kind string }{
	{"month", "month"}, {"date", "date"}, {"day", "date"}, {"amount", "number"}, {"total", "number"},
	{"price", "number"}, {"file", "file"},
}

// scanCandidates finds the candidates in s, in the order they appear.
func scanCandidates(s string) []paramCandidate {
	type span struct{ at, end int }
	var taken []span
	var out []struct {
		at int
		c  paramCandidate
	}
	for _, p := range candidatePatterns {
	next:
		for _, m := range p.re.FindAllStringIndex(s, -1) {
			for _, t := range taken {
				if m[0] < t.end && t.at < m[1] {
					continue next
				}
			}
			taken = append(taken, span{m[0], m[1]})
			out = append(out, struct {
				at int
				c  paramCandidate
			}{m[0], paramCandidate{Kind: p.kind, Value: s[m[0]:m[1]]}})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].at < out[j].at })
	cs := make([]paramCandidate, len(out))
	for i, o := range out {
		cs[i] = o.c
	}
	return cs
}

// urlCandidates are the candidates in a URL's path and query values.
func urlCandidates(raw string) []paramCandidate {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	out := scanCandidates(u.Path)
	q := u.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		for _, v := range q[k] {
			found := scanCandidates(v)
			if len(found) == 0 && strings.TrimSpace(v) != "" {
				for _, h := range queryKeyKinds {
					if strings.Contains(strings.ToLower(k), h.word) {
						found = []paramCandidate{{Kind: h.kind, Value: v}}
						break
					}
				}
			}
			for _, c := range found {
				c.Key = k
				out = append(out, c)
			}
		}
	}
	return out
}

// paramCandidates seeds the parameters deterministically, before the model sees the trace:
// the dates, months, file names and amounts in typed values (never masked ones), in the names
// of the elements clicked (a "Statement 2026-09" link), navigations' and downloads' URLs, and
// downloaded file names.
func paramCandidates(evs []traceEvent) []paramCandidate {
	var out []paramCandidate
	for i, e := range evs {
		var found []paramCandidate
		add := func(cs []paramCandidate, from string) {
			for _, c := range cs {
				c.Event, c.From = i+1, from
				if !slices.ContainsFunc(found, func(o paramCandidate) bool { return o.Kind == c.Kind && o.Value == c.Value }) {
					found = append(found, c)
				}
			}
		}
		switch {
		case e.typing() && !e.Masked:
			add(scanCandidates(e.Value), "typed")
		case e.Kind == "click" && e.Element != nil:
			add(scanCandidates(e.Element.Name), "clicked")
		case e.Kind == "navigate":
			add(urlCandidates(e.URL), "url")
		case e.Kind == "download":
			add(scanCandidates(firstNonEmptyStr(downloadName(e), downloadURLName(e))), "download")
			add(urlCandidates(e.URL), "url")
		}
		out = append(out, found...)
	}
	return out
}

// desktopParamCandidates are paramCandidates for a desktop trace: the dates, months, file
// names and amounts in typed values (never masked ones). There are no URLs on a desktop.
func desktopParamCandidates(evs []desktopEvent) []paramCandidate {
	var out []paramCandidate
	for i, e := range evs {
		if e.Kind != "input" || e.Masked {
			continue
		}
		var found []paramCandidate
		for _, c := range scanCandidates(e.Value) {
			c.Event, c.From = i+1, "typed"
			if !slices.ContainsFunc(found, func(o paramCandidate) bool { return o.Kind == c.Kind && o.Value == c.Value }) {
				found = append(found, c)
			}
		}
		out = append(out, found...)
	}
	return out
}

// downloadName is a download event's file name, as the browser reported it ("" when it
// didn't: Chromium names the file only after the download starts).
func downloadName(e traceEvent) string {
	if e.Filename != "" {
		return e.Filename
	}
	return e.Value
}

// downloadURLName is the last part of a download's URL path, which is often not the file's
// name (/statements/2026-09/download saves statement-2026-09.csv).
func downloadURLName(e traceEvent) string {
	if u, err := url.Parse(e.URL); err == nil && u.Path != "" && u.Path != "/" {
		return path.Base(u.Path)
	}
	return ""
}

// --- the prompt ---

const learnSystemPrompt = `You turn a recording of someone doing a task in their web browser into a workflow Jarvis can replay for them later. Answer only JSON.`

const learnInstructions = `Answer with this JSON:
{"name": "...", "triggers": ["..."], "steps": [{"intent": "...", "event_seqs": [1], "target": "..."}], "params": [{"name": "...", "kind": "...", "example": "...", "from_seqs": [1]}], "success": {...}}

- name: a short title for the task, like "Download bank statement".
- triggers: one to three things the user could say to run it, like "download my bank statement".
- steps: the events grouped into steps, in order. intent is one short line saying what the step does; event_seqs are the numbers of its events; target is what it acts on, in a few words. Leave out events that do nothing for the task.
- params: the candidate values that change each time the task is done (another month, another file). name is lower_snake_case, like "month"; kind is one of date, month, text, number, file; example is the candidate's value; from_seqs are the numbers of the events it comes from. Only use the candidates listed above, and leave out the ones that stay the same. Write a param as {name} in the intents that use it.
- success: how to tell the task worked: {"download": {"name_like": "statement_*.csv"}} for a download, {"url_contains": "..."} or {"text_present": ["..."]} for a page that shows it.
- ••• is a value that was hidden on purpose: never guess it.`

const desktopLearnSystemPrompt = `You turn a recording of someone doing a task in the apps on their computer into a workflow Jarvis can replay for them later. Answer only JSON.`

const (
	learnSuccessBrowser = `- success: how to tell the task worked: {"download": {"name_like": "statement_*.csv"}} for a download, {"url_contains": "..."} or {"text_present": ["..."]} for a page that shows it.`
	learnSuccessDesktop = `- success: how to tell the task worked: {"text_present": ["..."]} for text the app shows when it is done, or {"screen": "..."} saying what the screen shows then.`
)

// desktopLearnInstructions are learnInstructions with a success check a desktop can show.
var desktopLearnInstructions = strings.Replace(learnInstructions, learnSuccessBrowser, learnSuccessDesktop, 1)

// learnPrompt is the user message: the narration, the numbered events, the candidates.
func learnPrompt(evs []traceEvent, cands []paramCandidate, narration string) string {
	lines := make([]string, len(evs))
	for i, e := range evs {
		lines[i] = describeEvent(e)
	}
	return eventsPrompt(lines, cands, narration, learnInstructions)
}

// desktopLearnPrompt is learnPrompt for a desktop trace.
func desktopLearnPrompt(evs []desktopEvent, cands []paramCandidate, narration string) string {
	lines := make([]string, len(evs))
	for i, e := range evs {
		lines[i] = describeDesktopEvent(e)
	}
	return eventsPrompt(lines, cands, narration, desktopLearnInstructions)
}

func eventsPrompt(lines []string, cands []paramCandidate, narration, instructions string) string {
	var b strings.Builder
	if n := oneLine(narration); n != "" {
		fmt.Fprintf(&b, "What the user said about it: %q\n\n", truncateRunes(n, learnMaxNarration))
	}
	b.WriteString("What the user did, one event per line:\n")
	for i, l := range lines {
		fmt.Fprintf(&b, "%d. %s\n", i+1, l)
	}
	b.WriteString("\nValues that may change from one run to the next (the only parameter candidates):\n")
	if len(cands) == 0 {
		b.WriteString("none\n")
	}
	for _, c := range cands {
		fmt.Fprintf(&b, "- event %d: %s %q (%s)\n", c.Event, c.Kind, truncateRunes(c.Value, learnMaxValue), c.From)
	}
	b.WriteString("\n" + instructions)
	return b.String()
}

// describeEvent is one event as the prompt shows it.
func describeEvent(e traceEvent) string {
	if e.ResultURL != "" {
		a := e
		a.ResultURL = ""
		return describeEvent(a) + " (opens " + truncateRunes(e.ResultURL, learnMaxValue) + ")"
	}
	value := func() string {
		if e.Masked {
			return maskedValue
		}
		return strconv.Quote(truncateRunes(oneLine(e.Value), learnMaxValue))
	}
	switch e.Kind {
	case "navigate":
		return "open " + truncateRunes(e.URL, learnMaxValue)
	case "click":
		return "click " + describeElement(e.Element)
	case "input", "change":
		if selectLike(e.Element) {
			return "choose " + value() + " in " + describeElement(e.Element)
		}
		return "type into " + describeElement(e.Element) + ": " + value()
	case "submit":
		return "submit " + describeElement(e.Element)
	case "key":
		if e.Element != nil {
			return "press " + e.Key + " in " + describeElement(e.Element)
		}
		return "press " + e.Key
	case "download":
		s := "download " + strconv.Quote(truncateRunes(downloadName(e), learnMaxValue))
		if downloadName(e) == "" {
			s = "download from " + truncateRunes(e.URL, learnMaxValue) + ", file name unknown"
		}
		if e.Mime != "" {
			s += " (" + e.Mime + ")"
		}
		return s
	}
	return e.Kind
}

// describeDesktopEvent is one desktop event as the prompt shows it: what was done, on which
// element when the recording had one, in which app and window.
func describeDesktopEvent(e desktopEvent) string {
	where := orDefault(e.App, "an app")
	if e.Window != nil && e.Window.Title != "" {
		where += " — " + strconv.Quote(truncateRunes(oneLine(e.Window.Title), learnMaxValue))
	}
	if e.Element != nil {
		el := orDefault(e.Element.Role, "element")
		if name := oneLine(e.Element.Name); name != "" {
			el += " " + strconv.Quote(truncateRunes(name, learnMaxValue))
		}
		if e.Element.Vision {
			el += " (seen on the screen)"
		}
		where = el + " in " + where
	}
	switch e.Kind {
	case "click":
		verb := "click"
		if e.Button == "right" || e.Button == "middle" {
			verb = e.Button + "-click"
		}
		if e.Element == nil {
			verb += " in"
		}
		return fmt.Sprintf("%s %s at (%d,%d)", verb, where, e.X, e.Y)
	case "input":
		value := maskedValue
		if !e.Masked {
			value = strconv.Quote(truncateRunes(oneLine(e.Value), learnMaxValue))
		}
		return "type into " + where + ": " + value
	case "key":
		return "press " + e.Key + " in " + where
	}
	return e.Kind
}

func describeElement(el *traceElement) string {
	if el == nil {
		return "the page"
	}
	role := el.Role
	if role == "" {
		role = orDefault(el.Tag, "element")
	}
	if name := oneLine(el.Name); name != "" {
		return role + " " + strconv.Quote(truncateRunes(name, learnMaxValue))
	}
	if el.Selector != "" {
		return role + " " + truncateRunes(el.Selector, learnMaxValue)
	}
	return role
}

func orDefault(s, dflt string) string {
	if s == "" {
		return dflt
	}
	return s
}

func selectLike(el *traceElement) bool {
	if el == nil {
		return false
	}
	switch strings.ToLower(el.Role) {
	case "combobox", "listbox", "option", "radio", "checkbox":
		return true
	}
	return strings.EqualFold(el.Tag, "select")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// --- the model's answer ---

type learnOutput struct {
	Name     string   `json:"name"`
	Triggers []string `json:"triggers"`
	Steps    []struct {
		Intent    string `json:"intent"`
		EventSeqs []int  `json:"event_seqs"`
		Target    string `json:"target"`
	} `json:"steps"`
	Params []struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		Example  string `json:"example"`
		FromSeqs []int  `json:"from_seqs"`
	} `json:"params"`
	Success computer.SuccessCheck `json:"success"`
}

func parseLearnOutput(s string) (learnOutput, error) {
	var out learnOutput
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return out, errors.New("the model's answer isn't JSON")
	}
	if err := json.Unmarshal([]byte(s[i:j+1]), &out); err != nil {
		return out, fmt.Errorf("the model's answer isn't the workflow JSON: %w", err)
	}
	return out, nil
}

// buildWorkflow turns the model's answer into the draft. Events and candidates the model made up
// are dropped, and so are params of an unknown kind; each step's action and element are the
// recorded event's, its risk Classify's. A step on the built-in deny list is dropped (a replay
// could never run it).
func buildWorkflow(rec Recording, evs []traceEvent, cands []paramCandidate, out learnOutput) (TaughtWorkflow, error) {
	w := newDraft(rec, out)
	subs := learnParams(&w, out, cands)

	// The page each event happened on.
	pages := make([]string, len(evs))
	for i, e := range evs {
		switch {
		case e.URL != "":
			pages[i] = e.URL
		case i > 0:
			pages[i] = pages[i-1]
		}
	}
	var start string // the page the first step was on
	for _, s := range out.Steps {
		n := primaryEvent(evs, stepEvents(s.EventSeqs, len(evs)))
		if n == 0 {
			continue
		}
		e := evs[n-1]
		intent := stepIntent(s.Intent, describeEvent(e))
		a, ok := eventAction(e, intent)
		if !ok {
			continue
		}
		step := WStep{Intent: intent, Target: "browser", Action: a, Element: recordedElement(e.Element)}
		if u, err := url.Parse(pages[n-1]); err == nil {
			step.Site = u.Host
		}
		risk, ok := stepRisk(a, riskElement(e), computeruse.Target{Kind: "browser", Site: step.Site}, e.Masked)
		if !ok {
			continue
		}
		step.Risk = risk
		if len(w.Steps) == 0 {
			start = pages[n-1]
		}
		addStep(&w, step, subs[n])
	}
	if s, ok := startStep(w.Steps, start); ok {
		w.Steps = append([]WStep{s}, w.Steps...)
	}
	return finishDraft(w, successCheck(out.Success, evs))
}

// startStep is the step that opens start, the page a recording's first step was on, when the
// recording began on the site rather than with a navigation: without it a replay opens
// https://<site> and not that page (#418). The page's query and fragment are dropped (they may
// hold what was typed into it); false when the first step already navigates, or there's no room.
func startStep(steps []WStep, start string) (WStep, bool) {
	if len(steps) == 0 || steps[0].Action.Kind == "navigate" || len(steps) >= maxWorkflowSteps {
		return WStep{}, false
	}
	u, err := url.Parse(start)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Host != steps[0].Site {
		return WStep{}, false
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	intent := "Open " + u.Host
	return WStep{Intent: intent, Target: "browser", Site: u.Host,
		Action: computeruse.Action{Kind: "navigate", URL: u.String(), Intent: intent}}, true
}

// buildDesktopWorkflow is buildWorkflow for a desktop trace: each step targets the recorded
// app and window, and replays its last event (a click at the recorded point, typing, a key).
// A step whose event had an accessibility element (AX on macOS, AT-SPI on Linux) records it
// with source "ax", so a replay finds it again; a click the vision model named
// (nameClicksBySight) records that with source "vision"; one without (no accessibility
// context, no keyframe, a key with nothing focused) keeps only the point, the app and the window.
func buildDesktopWorkflow(rec Recording, evs []desktopEvent, cands []paramCandidate, out learnOutput) (TaughtWorkflow, error) {
	w := newDraft(rec, out)
	subs := learnParams(&w, out, cands)
	for _, s := range out.Steps {
		nums := stepEvents(s.EventSeqs, len(evs))
		if len(nums) == 0 {
			continue
		}
		n := nums[len(nums)-1]
		e := evs[n-1]
		described := describeDesktopEvent(e)
		if e.Element != nil && e.Element.Did != "" {
			described = e.Element.Did
		}
		intent := stepIntent(s.Intent, described)
		a, ok := desktopEventAction(e, intent)
		if !ok {
			continue
		}
		step := WStep{Intent: intent, Target: "desktop", App: e.App, Action: a}
		if e.Window != nil && (e.Window.Class != "" || e.Window.Title != "") {
			step.Window = &StepWindow{Class: e.Window.Class, Title: e.Window.Title}
		}
		step.Element = desktopRecordedElement(e.Element)
		var el *computeruse.Element
		if e.Element != nil {
			el = &computeruse.Element{Role: e.Element.Role, Name: e.Element.Name, Secure: e.Masked}
		}
		risk, ok := stepRisk(a, el, computeruse.Target{Kind: "desktop", App: e.App}, e.Masked)
		if !ok {
			continue
		}
		step.Risk = risk
		addStep(&w, step, subs[n])
	}
	// No URL or download on a desktop: the model's text or screen check, if any.
	sc := successCheck(out.Success, nil)
	sc.URLContains = ""
	return finishDraft(w, sc)
}

// newDraft is the draft's owner, name and triggers.
func newDraft(rec Recording, out learnOutput) TaughtWorkflow {
	w := TaughtWorkflow{HouseholdID: rec.HouseholdID, OwnerID: rec.OwnerID, RecordingID: rec.ID,
		Name: truncateRunes(oneLine(out.Name), maxWorkflowName)}
	if w.Name == "" {
		w.Name = "Recorded task"
	}
	for _, t := range out.Triggers {
		if t = oneLine(t); t != "" && !slices.Contains(w.Triggers, t) && len(w.Triggers) < maxWorkflowTriggers {
			w.Triggers = append(w.Triggers, t)
		}
	}
	return w
}

// paramSub is a param to write into the step of the candidate's event.
type paramSub struct {
	c    paramCandidate
	name string
}

// learnParams adds the params to w, named by the model and valued by the candidates, and returns
// what to substitute in each event's step.
func learnParams(w *TaughtWorkflow, out learnOutput, cands []paramCandidate) map[int][]paramSub {
	subs := map[int][]paramSub{}
	for _, p := range out.Params {
		name := strings.Trim(slugRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(p.Name)), "_"), "_")
		if name == "" || !slices.Contains(paramKinds, p.Kind) || len(w.Params) == maxWorkflowParams ||
			slices.ContainsFunc(w.Params, func(o Param) bool { return o.Name == name }) {
			continue
		}
		var picked []paramCandidate
		for _, n := range p.FromSeqs {
			var at []paramCandidate
			for _, c := range cands {
				if c.Event == n {
					at = append(at, c)
				}
			}
			if i := slices.IndexFunc(at, func(c paramCandidate) bool { return c.Value == p.Example }); i >= 0 {
				picked = append(picked, at[i])
			} else if len(at) > 0 {
				picked = append(picked, at[0])
			}
		}
		if len(picked) == 0 {
			continue
		}
		example := picked[0].Value
		if slices.ContainsFunc(picked, func(c paramCandidate) bool { return c.Value == p.Example }) {
			example = p.Example
		}
		w.Params = append(w.Params, Param{Name: name, Kind: p.Kind, Example: example})
		for _, c := range picked {
			subs[c.Event] = append(subs[c.Event], paramSub{c, name})
		}
	}
	return subs
}

// stepEvents is a step's event numbers that are in the trace (1 to n), each once.
func stepEvents(seqs []int, n int) []int {
	var nums []int
	for _, s := range seqs {
		if s >= 1 && s <= n && !slices.Contains(nums, s) {
			nums = append(nums, s)
		}
	}
	return nums
}

// stepIntent is the model's intent for a step, else the description of its event.
func stepIntent(intent, described string) string {
	if intent = truncateRunes(oneLine(intent), computeruse.MaxIntent); intent != "" {
		return intent
	}
	return truncateRunes(described, computeruse.MaxIntent)
}

// stepRisk is a step's risk category; false for a step Classify denies. Classify sees the
// recorded value, before the params replace it.
func stepRisk(a computeruse.Action, el *computeruse.Element, t computeruse.Target, masked bool) (string, bool) {
	switch r := computeruse.Classify(a, el, t); {
	case r.Level == computeruse.LevelDeny:
		return "", false
	case r.Level == computeruse.LevelConfirm && slices.Contains(riskCategories, r.Category):
		return r.Category, true
	case masked:
		return computeruse.CatSensitiveInput, true
	}
	return "", true
}

// addStep writes the step's params and adds it, up to maxWorkflowSteps.
func addStep(w *TaughtWorkflow, step WStep, subs []paramSub) {
	for _, sb := range subs {
		substitute(&step, sb.c, sb.name)
	}
	if len(w.Steps) < maxWorkflowSteps {
		w.Steps = append(w.Steps, step)
	}
}

// finishDraft sets the success check and validates the draft, which needs a step.
func finishDraft(w TaughtWorkflow, sc computer.SuccessCheck) (TaughtWorkflow, error) {
	if len(w.Steps) == 0 {
		return TaughtWorkflow{}, errors.New("the model's answer has no step on a recorded event")
	}
	w.Success = sc
	return w, w.Validate()
}

// primaryEvent is the event a step's action replays: its last action (a click, typing, a key),
// else its first navigation; 0 when it has neither (a download alone).
func primaryEvent(evs []traceEvent, nums []int) int {
	for i := len(nums) - 1; i >= 0; i-- {
		if k := evs[nums[i]-1].Kind; k != "navigate" && k != "download" {
			return nums[i]
		}
	}
	for _, n := range nums {
		if evs[n-1].Kind == "navigate" {
			return n
		}
	}
	return 0
}

// eventAction is the action that replays e (false when none does).
func eventAction(e traceEvent, intent string) (computeruse.Action, bool) {
	a := computeruse.Action{Intent: intent}
	switch e.Kind {
	case "navigate":
		u, err := url.Parse(e.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return a, false
		}
		a.Kind, a.URL = "navigate", e.URL
	case "click":
		if e.Element == nil {
			return a, false
		}
		a.Kind, a.Ref = "press", recordedRef
	case "input", "change":
		if e.Element == nil {
			return a, false
		}
		text := truncateRunes(e.Value, computeruse.MaxText)
		if e.Masked {
			text = maskedValue
		}
		switch {
		case selectLike(e.Element):
			a.Kind, a.Text, a.Ref = "select", text, recordedRef
		case text == "":
			a.Kind, a.Ref = "set_value", recordedRef // the field was cleared
		default:
			a.Kind, a.Text = "type", text
		}
	case "submit":
		if e.Element != nil {
			a.Kind, a.Ref = "press", recordedRef
		} else {
			a.Kind, a.Keys = "keypress", []string{"enter"}
		}
	case "key":
		k := strings.ToLower(e.Key)
		if !computeruse.KeyNames[k] {
			return a, false
		}
		a.Kind, a.Keys = "keypress", []string{k}
	default:
		return a, false
	}
	return a, true
}

// desktopEventAction is the action that replays a desktop event (false when none does). A
// click keeps its recorded global point (docs/decisions/2026-10-19-learning-workflows.md); a key
// is its chord ("Ctrl+s" is ctrl and s), dropped when a key isn't one keypress knows.
func desktopEventAction(e desktopEvent, intent string) (computeruse.Action, bool) {
	a := computeruse.Action{Intent: intent}
	switch e.Kind {
	case "click":
		if e.X < 0 || e.Y < 0 {
			return a, false
		}
		x, y := e.X, e.Y
		a.Kind, a.X, a.Y = "click", &x, &y
		if e.Button == "right" || e.Button == "middle" {
			a.Button = e.Button
		}
	case "input":
		text := truncateRunes(e.Value, computeruse.MaxText)
		if e.Masked {
			text = maskedValue
		}
		if text == "" {
			return a, false // a cleared field: without an element there is nothing to set
		}
		a.Kind, a.Text = "type", text
	case "key":
		var keys []string
		for _, k := range strings.Split(strings.ToLower(e.Key), "+") {
			if !computeruse.KeyNames[k] {
				return a, false
			}
			keys = append(keys, k)
		}
		a.Kind, a.Keys = "keypress", keys
	default:
		return a, false
	}
	return a, true
}

func recordedElement(el *traceElement) *RecordedElement {
	if el == nil {
		return nil
	}
	r := &RecordedElement{Role: el.Role, Name: el.Name, Selector: el.Selector}
	if el.Frame != 0 {
		r.Frame = strconv.Itoa(el.Frame)
	}
	return r
}

// desktopRecordedElement is the recorded element of a desktop event: its role, label and path
// from the accessibility tree, never a value (a password field's is not kept at all).
func desktopRecordedElement(el *desktopElement) *RecordedElement {
	if el == nil || (el.Role == "" && el.Name == "") {
		return nil
	}
	if el.Vision {
		return &RecordedElement{Role: el.Role, Name: el.Name, Source: elementSourceVision, Confidence: el.Confidence}
	}
	return &RecordedElement{Role: el.Role, Name: el.Name, AXPath: el.AXPath, Source: "ax"}
}

func riskElement(e traceEvent) *computeruse.Element {
	if e.Element == nil {
		return nil
	}
	el := e.Element
	return &computeruse.Element{Role: el.Role, Name: el.Name, Type: orDefault(el.Type, el.Tag), Autocomplete: el.Autocomplete,
		Secure: e.Masked}
}

// substitute writes param name as {name} where the step uses the candidate's value.
func substitute(s *WStep, c paramCandidate, name string) {
	ph := "{" + name + "}"
	switch s.Action.Kind {
	case "navigate":
		if c.Key != "" {
			s.Action.URL = strings.Replace(s.Action.URL, url.QueryEscape(c.Key)+"="+url.QueryEscape(c.Value), url.QueryEscape(c.Key)+"="+ph, 1)
		} else {
			s.Action.URL = strings.Replace(s.Action.URL, c.Value, ph, 1)
		}
	case "type", "select":
		s.Action.Text = strings.ReplaceAll(s.Action.Text, c.Value, ph)
	}
	// The element pressed for the value ("Statement 2026-09"): the replay presses the one for
	// the new value.
	if s.Element != nil && c.From == "clicked" {
		s.Element.Name = strings.ReplaceAll(s.Element.Name, c.Value, ph)
		s.Element.Selector = strings.ReplaceAll(s.Element.Selector, c.Value, ph)
	}
	s.Intent = strings.ReplaceAll(s.Intent, c.Value, ph)
	s.Action.Intent = s.Intent
}

// downloadExts are the file types a download's MIME type names, for one the browser didn't name.
var downloadExts = map[string]string{"text/csv": ".csv", "application/pdf": ".pdf", "application/json": ".json",
	"application/zip": ".zip", "text/plain": ".txt", "application/vnd.ms-excel": ".xls",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": ".xlsx"}

// downloadExt is an unnamed download's extension: its MIME type's, else its URL's ("" when
// neither says).
func downloadExt(e traceEvent) string {
	mt, _, _ := strings.Cut(strings.ToLower(e.Mime), ";")
	if ext, ok := downloadExts[strings.TrimSpace(mt)]; ok {
		return ext
	}
	return path.Ext(downloadURLName(e))
}

var digitRun = regexp.MustCompile(`\d+(?:[-_.]\d+)*`)

// successCheck is the model's check, tidied; a trace with a download always checks one (the
// last download's name, its numbers as *, when the model gave none). When the browser didn't
// name the last download, its name isn't known, so a name the model gave for it is a guess: the
// check is then a file of the download's type (*.csv), or any file.
func successCheck(sc computer.SuccessCheck, evs []traceEvent) computer.SuccessCheck {
	out := computer.SuccessCheck{URLContains: strings.TrimSpace(sc.URLContains), Screen: oneLine(sc.Screen)}
	for _, t := range sc.TextPresent {
		if t = oneLine(t); t != "" && len(out.TextPresent) < 5 {
			out.TextPresent = append(out.TextPresent, t)
		}
	}
	if sc.Download != nil && strings.TrimSpace(sc.Download.NameLike) != "" {
		out.Download = &computer.DownloadCheck{NameLike: strings.TrimSpace(sc.Download.NameLike), MinBytes: sc.Download.MinBytes}
	}
	if sc.File != nil && strings.TrimSpace(sc.File.Path) != "" {
		out.File = sc.File
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind != "download" {
			continue
		}
		switch name := downloadName(evs[i]); {
		case name == "":
			out.Download = &computer.DownloadCheck{NameLike: "*" + downloadExt(evs[i])}
		case out.Download == nil:
			out.Download = &computer.DownloadCheck{NameLike: digitRun.ReplaceAllString(name, "*")}
		}
		break
	}
	return out
}

// --- the job ---

type learnJobPayload struct {
	RecordingID string `json:"recording_id"`
}

// registerWorkflowLearning registers the job and hands it every stored recording.
func (m *Module) registerWorkflowLearning() {
	if m.learnRecording == nil {
		m.learnRecording = m.queueLearning
	}
	if q := m.deps.Queue; q != nil {
		q.Register(learnJob, queue.Handler{Run: func(ctx context.Context, j queue.Job) ([]byte, error) {
			var p learnJobPayload
			if err := json.Unmarshal(j.Payload, &p); err != nil {
				return nil, queue.Permanent(err)
			}
			return nil, m.learnFromRecording(ctx, p.RecordingID)
		}, MaxAttempts: learnMaxAttempts, Backoff: learnBackoff, Lease: 10 * time.Minute})
	}
}

// queueLearning queues learning a workflow from a browser or desktop recording (once per
// recording).
func (m *Module) queueLearning(ctx context.Context, rec Recording) error {
	if (rec.Mode != "browser" && rec.Mode != "desktop") || m.LLM == nil {
		return nil
	}
	if m.deps.Queue == nil {
		m.background(learnJob, func() {
			if err := m.learnFromRecording(context.Background(), rec.ID); err != nil {
				m.deps.Log.Warn("cc: couldn't learn a workflow from a recording", "recording", rec.ID, "err", err)
			}
		})
		return nil
	}
	payload, _ := json.Marshal(learnJobPayload{RecordingID: rec.ID})
	_, err := m.deps.Queue.Enqueue(ctx, learnJob, payload, queue.Options{DedupKey: learnJob + ":" + rec.ID})
	if errors.Is(err, queue.ErrDuplicate) {
		return nil
	}
	return err
}

// learnFromRecording learns the workflow draft from one recording and posts its card. A
// recording deleted meanwhile, or one a workflow was already learnt from, is nothing to do.
// What retrying can't fix (the trace, the model's answer) fails permanently.
func (m *Module) learnFromRecording(ctx context.Context, id string) error {
	rec, err := m.getRecording(ctx, id)
	if err != nil || rec == nil {
		return err
	}
	var have string
	err = m.deps.DB.Read.QueryRowContext(ctx, `SELECT id FROM cc_taught_workflows WHERE recording_id = ? LIMIT 1`, id).Scan(&have)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var (
		system, prompt string
		build          func(learnOutput) (TaughtWorkflow, error)
		note           string // for the card: what the draft lacks (naming steps by sight)
	)
	if rec.Mode == "browser" {
		evs, err := parseTrace(rec.Trace)
		if err != nil {
			return queue.Permanent(err)
		}
		if len(evs) == 0 {
			return queue.Permanent(errors.New("the recording has no events"))
		}
		cands := paramCandidates(evs)
		system, prompt = learnSystemPrompt, learnPrompt(evs, cands, rec.Narration)
		build = func(out learnOutput) (TaughtWorkflow, error) { return buildWorkflow(*rec, evs, cands, out) }
	} else {
		evs, err := parseDesktopTrace(rec.Trace)
		if err != nil {
			return queue.Permanent(err)
		}
		if len(evs) == 0 {
			return queue.Permanent(errors.New("the recording has no events"))
		}
		if err := m.awaitKeyframes(ctx, *rec, evs); err != nil {
			return err
		}
		note = m.nameClicksBySight(ctx, *rec, evs)
		cands := desktopParamCandidates(evs)
		system, prompt = desktopLearnSystemPrompt, desktopLearnPrompt(evs, cands, rec.Narration)
		build = func(out learnOutput) (TaughtWorkflow, error) { return buildDesktopWorkflow(*rec, evs, cands, out) }
	}
	if m.LLM == nil {
		return errors.New("no llm module")
	}
	temp, maxTokens, think := 0.0, learnMaxTokens, 0
	resp, err := m.LLM.Chat(ctx, llm.ChatRequest{
		Label:           llm.LabelBackground,
		Temperature:     &temp, // greedy: structured, stable run to run
		MaxTokens:       &maxTokens,
		ReasoningBudget: &think,
		ResponseFormat:  &llm.ResponseFormat{Type: "json_object"},
		Messages: []llm.Message{
			{Role: "system", Content: llm.TextContent(system)},
			{Role: "user", Content: llm.TextContent(prompt)},
		},
	})
	if err != nil {
		return err
	}
	out, err := parseLearnOutput(resp.Content)
	if err != nil {
		return queue.Permanent(err)
	}
	w, err := build(out)
	if err != nil {
		return queue.Permanent(err)
	}
	stored, err := m.createWorkflow(ctx, w)
	if err != nil {
		return err
	}
	m.deps.Log.Info("cc: workflow learnt from a recording", "recording", id, "mode", rec.Mode, "workflow", stored.ID,
		"steps", len(stored.Steps), "params", len(stored.Params))
	m.postLearnedCard(ctx, stored, note)
	return nil
}

// postLearnedCard asks the workflow's owner to review it. The admin's inbox shows the body as
// markdown, where the Review link opens the workflow's page (the server doesn't know the
// admin's public address, so the link is the SPA path, which metadata.path also carries).
func (m *Module) postLearnedCard(ctx context.Context, w TaughtWorkflow, note string) {
	path := "/workflows/" + w.ID
	title := `I learned "` + w.Name + `"`
	summary := fmt.Sprintf("%s (%d step%s). Review it.", title, len(w.Steps), plural(len(w.Steps)))
	var b strings.Builder
	b.WriteString(summary + "\n\n")
	for i, s := range w.Steps {
		fmt.Fprintf(&b, "%d. %s", i+1, s.Intent)
		if s.Risk != "" {
			b.WriteString(" (I'll ask first)")
		}
		b.WriteString("\n")
	}
	if len(w.Params) > 0 {
		names := make([]string, len(w.Params))
		for i, p := range w.Params {
			names[i] = p.Name
		}
		fmt.Fprintf(&b, "\nWhat changes each time: %s.\n", strings.Join(names, ", "))
	}
	if note != "" {
		b.WriteString("\n" + note + "\n")
	}
	b.WriteString("\n[Review](" + path + ")\n")
	uid := w.OwnerID
	m.postInboxItem(ctx, w.HouseholdID, &uid, title, summary, b.String(), learnCardCategory, map[string]any{
		"household_id": w.HouseholdID, "workflow_id": w.ID, "recording_id": w.RecordingID, "path": path,
	}, true, "user")
}
