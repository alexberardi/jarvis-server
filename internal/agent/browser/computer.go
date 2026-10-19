package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// Computer control (blueprint P3 piece 6): the computer-task executor reads a page as a list of
// interactive elements with refs and acts on one element by its ref, in a task window opened
// behind the user's. These are not model tools: the executor (computer_observe, computer_act)
// calls them. Snapshot names and values are untrusted page content; whoever renders them for a
// model wraps them with Wrap.

// Snapshot limits (the extension applies the same).
const (
	DefaultSnapshotMax = 300
	MaxSnapshotMax     = 1000
	snapshotNameMax    = 120
	actValueMax        = 2000 // the extension's FILL_MAX
)

// Act operations and the keys the keys op sends.
var (
	ActOps  = []string{"press", "set_value", "select", "check", "focus", "scroll_into_view", "keys"}
	ActKeys = []string{"Enter", "Escape", "Tab", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight"}
)

var refPattern = regexp.MustCompile(`^e[1-9][0-9]{0,5}$`)

// ActResult is where the tab ended up after an act.
type ActResult struct {
	OK         bool   `json:"ok"`
	URLAfter   string `json:"url_after,omitempty"`
	TitleAfter string `json:"title_after,omitempty"`
}

// Snapshot lists the interactive elements of tab ("KEY:T", or a bare id for the default
// browser), at most max (0: DefaultSnapshotMax).
func (t *Tools) Snapshot(ctx context.Context, tab string, max int) (computeruse.Snapshot, error) {
	if max <= 0 {
		max = DefaultSnapshotMax
	}
	max = min(max, MaxSnapshotMax)
	res, err := t.call(ctx, "snapshot", map[string]any{"tab_id": tab}, map[string]any{"max": max})
	if err != nil {
		return computeruse.Snapshot{}, err
	}
	var s computeruse.Snapshot
	if err := decode(res, &s); err != nil || s.Gen <= 0 {
		return computeruse.Snapshot{}, fmt.Errorf("the extension's snapshot is malformed: %v", cmpErr(err, "no gen"))
	}
	if len(s.Elements) > max {
		s.Elements = s.Elements[:max]
	}
	for i := range s.Elements {
		e := &s.Elements[i]
		e.Name = clipRunes(e.Name, snapshotNameMax)
		e.Value = clipRunes(e.Value, snapshotNameMax)
		if secretField(*e) {
			e.Value = "" // never, even from an extension that sent it
		}
	}
	return s, nil
}

// secretField is snapCollect's secret(): a password, hidden, card or one-time-code field,
// whose value a snapshot never carries.
func secretField(e computeruse.SnapElement) bool {
	if e.Element().Secure || strings.EqualFold(e.Type, "hidden") {
		return true
	}
	for _, a := range strings.Fields(strings.ToLower(e.Autocomplete)) {
		if strings.HasPrefix(a, "cc-") || a == "one-time-code" {
			return true
		}
	}
	return false
}

// Act does op on the element ref ("e12") of snapshot gen in tab. value is set_value's text,
// select's option label or check's state ("false" unchecks); keys are the keys op's.
func (t *Tools) Act(ctx context.Context, tab string, gen int, ref, op, value string, keys []string) (ActResult, error) {
	switch {
	case gen <= 0:
		return ActResult{}, fmt.Errorf("%w: gen must be a snapshot's gen", errBadArgs)
	case !refPattern.MatchString(ref):
		return ActResult{}, fmt.Errorf("%w: ref must be a snapshot ref such as e12", errBadArgs)
	case !slices.Contains(ActOps, op):
		return ActResult{}, fmt.Errorf("%w: op must be one of %s", errBadArgs, strings.Join(ActOps, ", "))
	case op == "keys" && len(keys) == 0:
		return ActResult{}, fmt.Errorf("%w: keys is required", errBadArgs)
	case len([]rune(value)) > actValueMax:
		return ActResult{}, fmt.Errorf("%w: value is longer than %d characters", errBadArgs, actValueMax)
	}
	for _, k := range keys {
		if !slices.Contains(ActKeys, k) {
			return ActResult{}, fmt.Errorf("%w: keys may only be %s", errBadArgs, strings.Join(ActKeys, ", "))
		}
	}
	extra := map[string]any{"gen": gen, "ref": ref, "op": op}
	if value != "" {
		extra["value"] = value
	}
	if len(keys) > 0 {
		extra["keys"] = keys
	}
	res, err := t.call(ctx, "act", map[string]any{"tab_id": tab}, extra)
	if err != nil {
		return ActResult{}, err
	}
	var r ActResult
	if err := decode(res, &r); err != nil {
		return ActResult{}, fmt.Errorf("the extension's act result is malformed: %v", err)
	}
	return r, nil
}

// OpenBackground opens url in a new browser window that doesn't take focus, in the default
// browser, and returns its tab and window ("KEY:N" each).
func (t *Tools) OpenBackground(ctx context.Context, rawURL string) (tab, window string, err error) {
	u, err := webURL(rawURL)
	if err != nil {
		return "", "", err
	}
	// Not t.call: that drops window ids, which the executor needs (screenshots are per window).
	res, info, err := t.Hub.Call(ctx, 0, "open", map[string]any{"url": u, "background": true})
	var ce *CommandError
	if errors.As(err, &ce) && ce.Code == "unknown_command" {
		return "", "", outdated(info, "open a task window")
	}
	if err != nil {
		return "", "", err
	}
	w, wok := res["window_id"].(float64)
	tb, _ := res["tab"].(map[string]any)
	tid, tok := tb["tab_id"].(float64)
	if !wok || !tok {
		// An extension before 0.3.0 ignores background and opens a foreground tab.
		return "", "", outdated(info, "open a task window")
	}
	return fmt.Sprintf("%s:%d", info.Key, int64(tid)), fmt.Sprintf("%s:%d", info.Key, int64(w)), nil
}

func decode(m map[string]any, v any) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func cmpErr(err error, dflt string) string {
	if err != nil {
		return err.Error()
	}
	return dflt
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
