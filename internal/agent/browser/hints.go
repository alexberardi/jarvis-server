package browser

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// Numbered hints (DESKTOP-COMMANDS.md L3, "show numbers"): the extension draws a number on
// everything clickable in view, and "click 7", or just "7", presses it. All on the node: a
// page's links are where the model was slowest and least sure ("open the second link" took it
// seven calls and two wrong pages).

var (
	reShowHints = regexp.MustCompile(`(?i)^\s*(?:(?:hey\s+)?jarvis[,.]?\s+)?(?:please\s+)?(?:show|give me)\s+(?:me\s+)?(?:the\s+)?(?:numbers|hints|links|labels)(?:\s+on\s+(?:the|this)\s+page)?[\s.!?]*$|^\s*(?:numbers|hints)[\s.!?]*$`)
	reHideHints = regexp.MustCompile(`(?i)^\s*(?:(?:hide|clear|close|remove)\s+(?:the\s+)?(?:numbers|hints|links|labels)|hide|cancel|never\s*mind|stop)[\s.!?]*$`)
	// "click 7", "number 7", "click on number seven", "open 12"; with numbers shown, just "7".
	// "type ABC into 29", "enter hello in number 4", "put my email in 12".
	reTypeInto = regexp.MustCompile(`(?i)^\s*(?:(?:hey\s+)?jarvis[,.]?\s+)?(?:please\s+)?(?:type|enter|put)\s+(.+?)\s+(?:in|into|in\s+to|on)\s+(?:number\s+|box\s+|field\s+)?([\w-]+(?:\s+[\w-]+)?)[\s.!?]*$`)
	reHintVerb = regexp.MustCompile(`(?i)^\s*(?:click|press|open|tap|select|choose|pick)\s+(?:on\s+)?(?:number\s+)?(.+?)[\s.!?]*$|^\s*number\s+(.+?)[\s.!?]*$`)
)

var numberWords = map[string]int{
	"zero": 0, "one": 1, "two": 2, "to": 2, "too": 2, "three": 3, "four": 4, "for": 4, "five": 5, "six": 6,
	"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13,
	"fourteen": 14, "fifteen": 15, "sixteen": 16, "seventeen": 17, "eighteen": 18, "nineteen": 19,
	"twenty": 20, "thirty": 30, "forty": 40, "fifty": 50, "sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90,
	"hundred": 100, "a hundred": 100, "one hundred": 100,
}

// spokenNumber reads "7", "7.", "seven", "twenty three", "twenty-three", "one hundred and five".
func spokenNumber(s string) (int, bool) {
	s = strings.ToLower(strings.Trim(strings.TrimSpace(s), ".!?,"))
	if n, err := strconv.Atoi(s); err == nil {
		return n, n > 0
	}
	if n, ok := numberWords[s]; ok {
		return n, n > 0
	}
	total, seen := 0, false
	for _, w := range strings.Fields(strings.NewReplacer("-", " ", " and ", " ").Replace(s)) {
		n, ok := numberWords[w]
		if !ok {
			return 0, false
		}
		switch {
		case n == 100:
			total = max(total, 1) * 100
		default:
			total += n
		}
		seen = true
	}
	return total, seen && total > 0
}

// hintState is the numbers on screen: the tab, its snapshot and what each number names.
type hintState struct {
	tab   any
	gen   float64
	url   string
	names map[int]string
}

// hintNumber is the number said, or ok false. bare allows a number with no verb ("7"), only
// while numbers are on screen.
func hintNumber(said string, bare bool) (int, bool) {
	if m := reHintVerb.FindStringSubmatch(said); m != nil {
		return spokenNumber(m[1] + m[2])
	}
	if bare {
		return spokenNumber(said)
	}
	return 0, false
}

// directHints handles "show numbers", "click 7" and "hide numbers"; ok false: not one of them.
func (t *Tools) directHints(ctx context.Context, said string) (map[string]any, bool) {
	t.mu.Lock()
	on := t.hints != nil
	t.mu.Unlock()
	switch {
	case reShowHints.MatchString(said):
		return t.showHints(ctx), true
	case on && reHideHints.MatchString(said):
		t.hideHints(ctx)
		return map[string]any{"success": true, node.IslandKey: "Numbers hidden", node.IslandOnlyKey: true}, true
	}
	if !on {
		return nil, false
	}
	if m := reTypeInto.FindStringSubmatch(said); m != nil {
		if n, ok := spokenNumber(m[2]); ok {
			text := strings.TrimSpace(m[1])
			if q := reQuoted.FindStringSubmatch(text); q != nil {
				text = q[1]
			}
			return t.fillHint(ctx, n, text), true
		}
	}
	if n, ok := hintNumber(said, true); ok {
		return t.clickHint(ctx, n, said), true
	}
	return nil, false
}

// reQuoted is text said in quotes, which keeps its own punctuation.
var reQuoted = regexp.MustCompile(`^["“'‘](.+?)["”'’][.!?,]?$`)

func (t *Tools) showHints(ctx context.Context) map[string]any { return t.drawHints(ctx, false) }

// drawHints numbers what's on screen; wait lets a page that is loading (after a click or an
// open) finish first.
func (t *Tools) drawHints(ctx context.Context, wait bool) map[string]any {
	if err := t.notJarvisPage(ctx, map[string]any{}, "No numbers were shown"); err != nil {
		return t.hintFail(err)
	}
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	extra := map[string]any{"show": true}
	if wait {
		extra["wait"] = true
	}
	res, err := t.call(ctx, "hints", map[string]any{}, extra)
	if err != nil {
		return t.hintFail(err)
	}
	h := &hintState{gen: num(res["gen"]), url: str(res["url"]), names: map[int]string{}}
	if tab, ok := res["tab"].(map[string]any); ok {
		h.tab = tab["tab_id"]
	}
	els, _ := res["elements"].([]any)
	for _, e := range els {
		m, _ := e.(map[string]any)
		if n, err := strconv.Atoi(strings.TrimPrefix(str(m["ref"]), "e")); err == nil {
			h.names[n] = str(m["name"])
		}
	}
	if len(h.names) == 0 {
		return map[string]any{"success": false, "error": "nothing on this page can be clicked",
			node.IslandKey: "Nothing to click here", node.IslandOnlyKey: true}
	}
	t.mu.Lock()
	t.hints, t.hintConfirm = h, ""
	t.mu.Unlock()
	// A list on the island: the next words are its answer, heard at once (the choice window).
	return map[string]any{"success": true, "count": len(h.names), node.IslandKey: "Say a number to click",
		node.IslandOnlyKey: true, node.ChoiceKey: true}
}

// clickHint presses number n. A risky label ("Send", "Buy") asks first, as browser_click does;
// the spoken yes comes back to AnswerChoice.
func (t *Tools) clickHint(ctx context.Context, n int, said string) map[string]any {
	return t.actHint(ctx, n, "press", "")
}

// fillHint types text into number n ("type ABC into 29"), as browser_fill does: the text is
// sanitized, a password field is refused by the page, and confirmation follows the same gate.
func (t *Tools) fillHint(ctx context.Context, n int, text string) map[string]any {
	clean, err := dictate.Sanitize(text)
	if err != nil {
		return t.hintFail(fmt.Errorf("%w: %w", errDictate, err))
	}
	return t.actHint(ctx, n, "set_value", clean)
}

// actHint does op on number n: press, or set_value with value.
func (t *Tools) actHint(ctx context.Context, n int, op, value string) map[string]any {
	t.mu.Lock()
	h := t.hints
	t.mu.Unlock()
	if h == nil {
		return map[string]any{"success": false, "error_code": "no_numbers",
			"error":        "no numbers are on the page right now; use the field's or button's label instead",
			node.IslandKey: "No numbers on the page", node.IslandOnlyKey: true}
	}
	name, ok := h.names[n]
	if !ok {
		return map[string]any{"success": false, "error_code": "no_such_number", "error": fmt.Sprintf("there's no number %d on the page", n),
			node.IslandKey: fmt.Sprintf("No number %d — say another", n), node.IslandOnlyKey: true, node.ChoiceKey: true}
	}
	label := cmp.Or(name, fmt.Sprint(n))
	press := op == "press"
	what, outcome, question := "Clicking ", "Nothing was clicked", fmt.Sprintf("Click %q in the browser?", dictate.Preview(label, 60))
	if !press {
		what, outcome = "Typing into ", "Nothing was typed"
		question = fmt.Sprintf("Type %q into %q?", dictate.Preview(value, 60), dictate.Preview(label, 40))
	}
	args := map[string]any{"tab_id": h.tab}
	act := func(ctx context.Context) (map[string]any, error) {
		if err := t.notJarvisPage(ctx, args, outcome); err != nil {
			return nil, err
		}
		t.show(what + dictate.Preview(label, 60))
		extra := map[string]any{"gen": h.gen, "ref": fmt.Sprintf("e%d", n), "op": op}
		if !press {
			extra["value"] = value
		}
		res, err := t.call(ctx, "act", args, extra)
		if press {
			t.hideHints(ctx) // a number pressed is used up; the page has likely changed
		}
		if err != nil {
			return nil, err
		}
		if press {
			t.refreshHints(ctx)
			res["clicked"] = label
		} else {
			res["filled"] = label
		}
		res["success"] = true
		return res, nil
	}
	g := dictate.GateType
	// A number with no readable label is asked about too (DESKTOP-COMMANDS.md Q8): its risk can't be told.
	if press && t.Confirm.Needs(dictate.GateSubmit) && (strings.TrimSpace(name) == "" || clickRisk(name, h.url) != "") {
		g = dictate.GateSubmit
	}
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	out, err := t.gate(ctx, g, question, act)
	if err != nil {
		return t.hintFail(err)
	}
	if out["status"] == "awaiting_confirmation" {
		q, _ := out["question"].(string)
		t.mu.Lock()
		t.hintConfirm, _ = out["confirmation_id"].(string)
		t.mu.Unlock()
		out[node.IslandKey], out[node.IslandOnlyKey], out[node.ChoiceKey] = dictate.WaitingPrefix+q, true, true
		return out
	}
	out[node.IslandOnlyKey] = true
	return out
}

// TurnListening shows numbers on the page while a voice turn listens, when the browser is the
// window in front, and takes them down when the turn ends: no "show numbers" turn (user,
// 2026-10-27).
func (t *Tools) TurnListening(ctx context.Context, on bool) {
	t.mu.Lock()
	t.listening = on
	t.mu.Unlock()
	if !on {
		t.hideHints(ctx)
		return
	}
	if node.Enabled(ctx, ToolClick) && t.browserInFront() {
		t.showHints(ctx)
	}
}

// refreshHints numbers the page again after a step changed it, while the turn listens and the
// browser is in front, in the background.
func (t *Tools) refreshHints(ctx context.Context) {
	t.mu.Lock()
	on := t.listening
	t.mu.Unlock()
	if !on || !t.browserInFront() {
		return
	}
	ctx = context.WithoutCancel(ctx)
	go func() { // off the turn: the next listen mustn't wait for the page to load
		t.hideHints(ctx) // the old numbers, maybe on the tab just left
		t.drawHints(ctx, true)
	}()
}

func (t *Tools) browserInFront() bool {
	for _, b := range t.Hub.Browsers() {
		if b.Focused {
			return true
		}
	}
	return false
}

// hideHints takes the numbers down (best effort: the page may be gone) and forgets them.
func (t *Tools) hideHints(ctx context.Context) {
	t.mu.Lock()
	h := t.hints
	t.hints = nil
	t.mu.Unlock()
	if h == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultTimeout)
	defer cancel()
	_, _ = t.call(ctx, "hints", map[string]any{"tab_id": h.tab}, map[string]any{"show": false})
}

// AnswerChoice takes the words said while numbers are on screen ("7", "click 12", "hide"), or
// the yes or no to a risky click's question.
func (t *Tools) AnswerChoice(ctx context.Context, said string) (map[string]any, bool) {
	t.mu.Lock()
	id := t.hintConfirm
	t.hintConfirm = ""
	t.mu.Unlock()
	if id != "" && t.Confirm.Pending(id) {
		out, err := t.Confirm.Answer(ctx, id, said)
		if err != nil {
			return t.hintFail(err), true
		}
		out[node.IslandOnlyKey] = true
		return out, true
	}
	return t.directHints(ctx, said)
}

// hintToolOut is a numbered action's result for the model's browser_fill or browser_click: no
// island markers (the model speaks for itself), and a failure as an error.
func hintToolOut(out map[string]any) (map[string]any, error) {
	delete(out, node.IslandKey)
	delete(out, node.IslandOnlyKey)
	delete(out, node.ChoiceKey)
	if out["success"] == false && out["status"] != "awaiting_confirmation" {
		return nil, &CommandError{Code: cmp.Or(str(out["error_code"]), "failed"), Message: str(out["error"])}
	}
	return out, nil
}

func (t *Tools) hintFail(err error) map[string]any {
	msg, shown := message(err), dictate.Preview(message(err), 80)
	if code(err) == "extension_outdated" {
		shown = "Numbers need the newer Jarvis browser extension"
	}
	return map[string]any{"success": false, "error": msg, "error_code": code(err),
		node.IslandKey: shown, node.IslandOnlyKey: true}
}

func num(v any) float64 { f, _ := v.(float64); return f }
func str(v any) string  { s, _ := v.(string); return s }

var (
	_ node.ChoiceAnswerer = (*Tools)(nil)
	_ node.TurnWatcher    = (*Tools)(nil)
)
