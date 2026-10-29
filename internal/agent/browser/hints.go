package browser

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/targets"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

var (
	// "type dogs into the search box", "type dogs into Search".
	reTypeInto = regexp.MustCompile(`(?i)^\s*(?:(?:hey\s+)?jarvis[,.]?\s+)?(?:please\s+)?(?:type|enter|put)\s+(.+?)\s+(?:in|into|in\s+to|on)\s+(?:box\s+|field\s+)?(.+?)[\s.!?]*$`)
	// "Click" or a common whisper substitution in the final sentence, even after earlier speech.
	reClickHeard = regexp.MustCompile(`(?i)(?:^|[.!?,]\s+)(quick|good|clique|klick|clip|click)\s+(?:on\s+)?([^.!?,]+?)[\s.!?]*$`)
	reClickVerb  = regexp.MustCompile(`(?i)^\s*(?:(?:hey\s+)?jarvis[,.]?\s+)?(?:please\s+)?(?:click|tap)\s`) // not "press": "press enter" is a key
	reHintVerb   = regexp.MustCompile(`(?i)^\s*(?:click|press|open|tap|select|choose|pick|go\s+to)\s+(?:on\s+)?(.+?)[\s.!?]*$`)
)

// voiceSnapshotMax is how many on-screen controls a voice turn reads.
const voiceSnapshotMax = 150

// hintState is the voice turn's snapshot of the page's visible controls, and the tab it came
// from: what was said is resolved against it (internal/agent/targets) and acted on in it, never
// against a newer one (ELEMENT-LAYER-WORK.md §3).
type hintState struct {
	tab  any
	snap computeruse.Snapshot
}

// directHints resolves direct browser actions against the current page snapshot.
func (t *Tools) directHints(ctx context.Context, said string) (map[string]any, bool) {
	t.mu.Lock()
	h := t.hints
	t.mu.Unlock()
	if h == nil {
		// No snapshot, or a step changed the page since (refreshHints): read it now, once
		// it has loaded, while a turn listens or for a "click …" with the browser in front.
		t.mu.Lock()
		on := t.listening
		t.mu.Unlock()
		heardClick := reClickHeard.FindStringSubmatch(said)
		click := reClickVerb.MatchString(said) || (heardClick != nil && strings.EqualFold(heardClick[1], "click"))
		if !(on || click) || !node.Enabled(ctx, ToolClick) || !t.browserInFront() {
			return nil, false
		}
		t.readTargets(ctx, true)
		t.mu.Lock()
		h = t.hints
		t.mu.Unlock()
		if h == nil {
			if click {
				return noSuchTarget(strings.TrimSpace(said)), true
			}
			return nil, false
		}
	}
	if m := reTypeInto.FindStringSubmatch(said); m != nil {
		text := strings.TrimSpace(m[1])
		if q := reQuoted.FindStringSubmatch(text); q != nil {
			text = q[1]
		}
		ref, ok := targets.Resolve(h.snap, m[2], true)
		if ok {
			return t.fillHint(ctx, h, ref, text, false), true
		}
		if ref, result := t.picker().Pick(ctx, h.snap, targets.FieldPrefix+m[2]); result == targets.Selected {
			return t.fillHint(ctx, h, ref, text, false), true
		} else if result == targets.Uncertain {
			return uncertainTarget(), true
		}
		return nil, false // no plausible page field: dictation's ("type hello into Slack")
	}
	target := strings.TrimRight(strings.TrimSpace(said), ".!?")
	m := reHintVerb.FindStringSubmatch(said)
	if m != nil {
		target = m[1]
	}
	loose := m != nil
	if hm := reClickHeard.FindStringSubmatch(said); m == nil && hm != nil {
		target := strings.TrimSpace(hm[2])
		if r, found := targets.Ordinal(h.snap, target); found {
			return t.clickHint(ctx, h, r), true
		}
		if r, found := targets.Resolve(h.snap, target, true); found {
			return t.clickHint(ctx, h, r), true
		}
		if r, result := t.picker().Pick(ctx, h.snap, "click "+target); result == targets.Selected {
			return t.clickHint(ctx, h, r), true
		} else if result == targets.Uncertain {
			return uncertainTarget(), true
		}
		if strings.EqualFold(hm[1], "click") {
			return noSuchTarget(target), true
		}
		// A literal click stays on the page handler on a miss; only a possible whisper substitution
		// falls through when nothing on the page fits, preserving speech such as "Good morning".
		return nil, false
	}
	if m != nil {
		if r, found := targets.Ordinal(h.snap, target); found {
			return t.clickHint(ctx, h, r), true
		}
	}
	ref, ok := targets.Resolve(h.snap, target, loose)
	switch {
	case ok:
		return t.clickHint(ctx, h, ref), true
	case reClickVerb.MatchString(said):
		// "click …" with no name like it: the model picks from the page's list (pick.go), else
		// it stays here, never a free model turn (that made "click line over" a light).
		if ref, result := t.picker().Pick(ctx, h.snap, said); result == targets.Selected {
			return t.clickHint(ctx, h, ref), true
		} else if result == targets.Uncertain {
			return uncertainTarget(), true
		}
		return noSuchTarget(target), true
	}
	return nil, false
}

// namedRef resolves a spoken page name when a voice-turn snapshot is available.
func (t *Tools) namedRef(said string) (ref string, h *hintState, handled bool) {
	t.mu.Lock()
	h = t.hints
	t.mu.Unlock()
	if strings.TrimSpace(said) == "" || h == nil {
		return "", nil, false
	}
	ref, ok := targets.Resolve(h.snap, said, true)
	return ref, h, ok
}

// picker is the narrow model pick for this browser: page text wrapped as untrusted (Wrap).
func (t *Tools) picker() targets.Picker { return targets.Picker{CC: t.CC, Wrap: Wrap, Log: t.Log} }

func noSuchTarget(said string) map[string]any {
	said = strings.TrimSpace(said)
	return map[string]any{"success": false, "error_code": "target_not_found", "error": fmt.Sprintf("no matching control for %s on the page", said),
		node.IslandKey: "I couldn't find that control. Please repeat the command with another detail.", node.IslandOnlyKey: true, node.ChoiceKey: true}
}

func uncertainTarget() map[string]any {
	return map[string]any{"success": false, "error_code": "target_uncertain", "error": "the intended control is ambiguous",
		node.IslandKey: "I'm not sure which control you mean. Please repeat that with another detail, like its nearby text or position.", node.IslandOnlyKey: true, node.ChoiceKey: true}
}

// reQuoted is text said in quotes, which keeps its own punctuation.
var reQuoted = regexp.MustCompile(`^["“'‘](.+?)["”'’][.!?,]?$`)

// readTargets takes an invisible snapshot of interactive controls, optionally after navigation.
func (t *Tools) readTargets(ctx context.Context, wait bool) map[string]any {
	if err := t.notJarvisPage(ctx, map[string]any{}, "Couldn't inspect page controls"); err != nil {
		return t.hintFail(err)
	}
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	extra := map[string]any{"max": voiceSnapshotMax, "viewport": true}
	if wait {
		extra["wait"] = true
	}
	snap, res, err := t.snapshot(ctx, map[string]any{}, extra, voiceSnapshotMax)
	if err != nil {
		return t.hintFail(err)
	}
	h := &hintState{snap: snap}
	if tab, ok := res["tab"].(map[string]any); ok {
		h.tab = tab["tab_id"]
	}
	if len(h.snap.Elements) == 0 {
		return map[string]any{"success": false, "error": "no interactive controls are visible on this page",
			node.IslandKey: "I don't see any controls on this page", node.IslandOnlyKey: true}
	}
	t.mu.Lock()
	t.hints, t.hintConfirm = h, ""
	t.mu.Unlock()
	// A list on the island: the next words are its answer, heard at once (the choice window).
	return map[string]any{"success": true, "count": len(h.snap.Elements), node.IslandKey: "Say what to click",
		node.IslandOnlyKey: true, node.ChoiceKey: true}
}

// clickHint presses an internally referenced control; risky labels still ask first.
func (t *Tools) clickHint(ctx context.Context, h *hintState, ref string) map[string]any {
	return t.actHint(ctx, h, ref, "press", "", false)
}

// fillHint types text into an internally referenced control, through the existing safety gate.
func (t *Tools) fillHint(ctx context.Context, h *hintState, ref string, text string, submit bool) map[string]any {
	clean, err := dictate.Sanitize(text)
	if err != nil {
		return t.hintFail(fmt.Errorf("%w: %w", errDictate, err))
	}
	return t.actHint(ctx, h, ref, "set_value", clean, submit)
}

// actHint performs op on an internally referenced control.
func (t *Tools) actHint(ctx context.Context, h *hintState, ref string, op, value string, submitting bool) map[string]any {
	if h == nil {
		return map[string]any{"success": false, "error_code": "no_page_snapshot",
			"error":        "there is no current browser page snapshot",
			node.IslandKey: "I can't identify that control yet", node.IslandOnlyKey: true}
	}
	e := h.snap.Find(ref)
	if e == nil {
		return noSuchTarget("that control")
	}
	name := e.Name
	label := cmp.Or(name, e.Context, e.Role, "unlabeled control")
	press := op == "press"
	what, outcome, question := "Clicking ", "Nothing was clicked", fmt.Sprintf("Click %q in the browser?", dictate.Preview(label, 60))
	if !press {
		what, outcome = "Typing into ", "Nothing was typed"
		question = fmt.Sprintf("Type %q into %q?", dictate.Preview(value, 60), dictate.Preview(label, 40))
		if submitting {
			question = fmt.Sprintf("Type %q into %q and submit?", dictate.Preview(value, 60), dictate.Preview(label, 40))
		}
	}
	args := map[string]any{"tab_id": h.tab}
	act := func(ctx context.Context) (map[string]any, error) {
		if err := t.notJarvisPage(ctx, args, outcome); err != nil {
			return nil, err
		}
		t.show(what + dictate.Preview(label, 60))
		extra := map[string]any{"gen": h.snap.Gen, "ref": ref, "op": op}
		if !press {
			extra["value"] = value
			if submitting {
				extra["submit"] = true
			}
		}
		res, err := t.call(ctx, "act", args, extra)
		if press {
			t.clearTargetSnapshot(h) // a pressed control may have changed the page
		}
		if err != nil {
			return nil, err
		}
		if press || submitting {
			t.refreshHints(ctx)
			res["clicked"] = label
		} else {
			res["filled"] = label
		}
		res["success"] = true
		return res, nil
	}
	g := dictate.GateType
	// An unlabeled control asks too because its risk cannot be classified from its name.
	if press && t.Confirm.Needs(dictate.GateSubmit) && (strings.TrimSpace(name) == "" || clickRisk(name, h.snap.URL) != "") {
		g = dictate.GateSubmit
	} else if submitting && t.Confirm.Needs(dictate.GateSubmit) && submitRisk(name, h.snap.URL) != "" {
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

// TurnListening snapshots visible controls while a voice turn listens.
func (t *Tools) TurnListening(ctx context.Context, on bool) {
	t.mu.Lock()
	t.listening = on
	t.mu.Unlock()
	t.clearTargets()
	if !on {
		return
	}
	if node.Enabled(ctx, ToolClick) && t.browserInFront() {
		t.readTargets(ctx, false)
	}
}

// refreshHints marks the snapshot stale after a step changed the page; the next command reads
// it again (directHints). Reading at once got the old page: a click's navigation hasn't begun
// when the click returns (2026-10-28).
func (t *Tools) refreshHints(context.Context) { t.clearTargets() }

func (t *Tools) browserInFront() bool {
	for _, b := range t.Hub.Browsers() {
		if b.Focused {
			return true
		}
	}
	return false
}

// clearTargets invalidates the active page snapshot.
func (t *Tools) clearTargets() {
	t.mu.Lock()
	t.hints = nil
	t.mu.Unlock()
}

// clearTargetSnapshot invalidates only the snapshot this action resolved against.
func (t *Tools) clearTargetSnapshot(h *hintState) {
	t.mu.Lock()
	if t.hints == h {
		t.hints = nil
	}
	t.mu.Unlock()
}

// AnswerChoice takes a target clarification or the yes/no answer to a risky action.
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

// hintToolOut is a page action's result for browser_fill or browser_click: no
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
		shown = "Browser controls need the newer Jarvis extension"
	}
	return map[string]any{"success": false, "error": msg, "error_code": code(err),
		node.IslandKey: shown, node.IslandOnlyKey: true}
}

func str(v any) string { s, _ := v.(string); return s }

var (
	_ node.ChoiceAnswerer = (*Tools)(nil)
	_ node.TurnWatcher    = (*Tools)(nil)
)
