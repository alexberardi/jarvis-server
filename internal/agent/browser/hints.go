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
	if n, ok := hintNumber(said, true); ok {
		return t.clickHint(ctx, n, said), true
	}
	return nil, false
}

func (t *Tools) showHints(ctx context.Context) map[string]any {
	if err := t.notJarvisPage(ctx, map[string]any{}, "No numbers were shown"); err != nil {
		return t.hintFail(err)
	}
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	res, err := t.call(ctx, "hints", map[string]any{}, map[string]any{"show": true})
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
	t.mu.Lock()
	h := t.hints
	t.mu.Unlock()
	name, ok := h.names[n]
	if !ok {
		return map[string]any{"success": false, "error": fmt.Sprintf("there's no number %d on the page", n),
			node.IslandKey: fmt.Sprintf("No number %d — say another", n), node.IslandOnlyKey: true, node.ChoiceKey: true}
	}
	args := map[string]any{"tab_id": h.tab}
	act := func(ctx context.Context) (map[string]any, error) {
		if err := t.notJarvisPage(ctx, args, "Nothing was clicked"); err != nil {
			return nil, err
		}
		t.show("Clicking " + dictate.Preview(cmp.Or(name, fmt.Sprint(n)), 60))
		res, err := t.call(ctx, "act", args, map[string]any{"gen": h.gen, "ref": fmt.Sprintf("e%d", n), "op": "press"})
		t.hideHints(ctx) // a number pressed is used up; the page has likely changed
		if err != nil {
			return nil, err
		}
		res["success"], res["clicked"] = true, cmp.Or(name, fmt.Sprint(n))
		return res, nil
	}
	g := dictate.GateType
	// A number with no readable label is asked about too (DESKTOP-COMMANDS.md Q8): its risk can't be told.
	if t.Confirm.Needs(dictate.GateSubmit) && (strings.TrimSpace(name) == "" || clickRisk(name, h.url) != "") {
		g = dictate.GateSubmit
	}
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	out, err := t.gate(ctx, g, fmt.Sprintf("Click %q in the browser?", dictate.Preview(cmp.Or(name, fmt.Sprint(n)), 60)), act)
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

func (t *Tools) hintFail(err error) map[string]any {
	msg := message(err)
	return map[string]any{"success": false, "error": msg, "error_code": code(err),
		node.IslandKey: dictate.Preview(msg, 80), node.IslandOnlyKey: true}
}

func num(v any) float64 { f, _ := v.(float64); return f }
func str(v any) string  { s, _ := v.(string); return s }

var _ node.ChoiceAnswerer = (*Tools)(nil)
