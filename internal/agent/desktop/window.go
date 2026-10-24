package desktop

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/winmatch"
)

// actionUnsupported is the one action every desktop_window enum carries: the model picks it when
// the user asks for a window action this computer can't do, so the answer is the fixed message
// below rather than an improvised one (DESKTOP-COMMANDS.md Q3). It never changes with the
// capabilities, so it doesn't churn the prompt cache.
const actionUnsupported = "unsupported"

// UnsupportedMessage is what an unsupported action answers.
const UnsupportedMessage = "Action not supported on this OS/Host"

// windowExamples are offered only when their action is in this host's enum.
var windowExamples = []node.Example{
	{VoiceCommand: "switch to firefox", Params: map[string]any{"window": "firefox", "action": "focus"}, Primary: true},
	{VoiceCommand: "maximize this", Params: map[string]any{"action": "maximize"}},
	{VoiceCommand: "move slack to workspace 2", Params: map[string]any{"window": "slack", "action": "move_to_workspace", "workspace": 2}},
	{VoiceCommand: "minimize the terminal", Params: map[string]any{"window": "the terminal", "action": "minimize"}},
}

// windowActions is the actions this host can do now, in WindowActions order (close has its own
// tool).
func windowActions(have map[string]bool) []string {
	var out []string
	for _, a := range host.WindowActions {
		if a != host.ActionClose && have[host.ActionCapability[a]] {
			out = append(out, string(a))
		}
	}
	return out
}

func windowSpec(have map[string]bool) (node.Spec, bool) {
	actions := windowActions(have)
	if len(actions) == 0 {
		return node.Spec{}, false
	}
	var examples []node.Example
	for _, e := range windowExamples {
		if slices.Contains(actions, e.Params["action"].(string)) {
			examples = append(examples, e)
		}
	}
	params := []node.Param{
		{Name: "action", Type: "string", Required: true, Enum: append(actions, actionUnsupported),
			Description: "focus = switch to; unmaximize = restore to normal size (leaves maximize or fullscreen); unsupported = anything this computer can't do"},
		{Name: "window", Type: "string",
			Description: "The app or window as the user said it ('Firefox', 'the terminal'), 'it' (the one just acted on), 'the other one' (the one before this one, as 'switch back' means), or an id from desktop_list_windows. Leave out for the window in front"},
	}
	if have[host.CapWindowsMonitor] {
		params = append(params, node.Param{Name: "monitor", Type: "string", Description: "move_to_monitor: next, previous, a number, or a name like DP-1"})
	}
	if have[host.CapWindowsWorkspace] {
		d := "move_to_workspace: the workspace number"
		if !have[host.CapWindowsMonitor] {
			// One monitor: small models otherwise turn "my other monitor" into a workspace move.
			d += " (not a monitor)"
		}
		params = append(params, node.Param{Name: "workspace", Type: "int", Description: d})
	}
	return node.Spec{Name: ToolWindow,
		Description: "Act on one window on the user's computer. Can: " + verbs(have, true) + ". Can't: " + verbs(have, false) +
			", keep on top: for those call with action unsupported, never a different action.",
		Params:   params,
		Keywords: []string{"window", "switch to", "focus", "minimize", "maximize", "fullscreen", "restore", "snap", "move", "monitor", "workspace"},
		Examples: examples,
	}, true
}

// verbs says in words what the enum allows (can) or what this computer lacks (!can). Small models
// map an action missing from the enum onto the nearest one they see ("minimize" became a snap)
// unless the description names it as one to answer unsupported.
func verbs(have map[string]bool, can bool) string {
	var out []string
	for _, v := range []struct{ cap, words string }{
		{host.CapWindowsFocus, "switch to it"},
		{host.CapWindowsMinimize, "minimize"},
		{host.CapWindowsUnminimize, "unminimize"},
		{host.CapWindowsMaximize, "maximize"},
		{host.CapWindowsFullscreen, "fullscreen"},
		{host.CapWindowsUnmaximize, "restore from maximize or fullscreen"},
		{host.CapWindowsSnap, "snap it left or right"},
		{host.CapWindowsMonitor, "move it to another monitor"},
		{host.CapWindowsWorkspace, "move it to another workspace"},
	} {
		if have[v.cap] == can {
			out = append(out, v.words)
		}
	}
	return strings.Join(out, ", ")
}

func (t *Tools) window(ctx context.Context, args map[string]any) (map[string]any, error) {
	raw, _ := args["action"].(string)
	a := host.WindowAction(strings.ToLower(strings.TrimSpace(raw)))
	capability, known := host.ActionCapability[a]
	if !known || a == host.ActionClose || !t.Host.Has(ctx, capability) {
		// "unsupported", or an action that vanished between rendering and calling (a revoked
		// Accessibility grant, a monitor unplugged).
		return nil, host.Errorf(host.ErrNotSupported, UnsupportedMessage)
	}
	target := ""
	switch a {
	case host.ActionMoveToWorkspace:
		n, err := intArg(args, "workspace")
		if err != nil {
			return nil, fmt.Errorf("%w: move_to_workspace needs the workspace number", errBadArgs)
		}
		target = strconv.Itoa(n)
	case host.ActionMoveToMonitor:
		target, _ = args["monitor"].(string)
		target = strings.ToLower(strings.TrimSpace(target))
		if target == "" {
			target = "next"
		}
	}
	w, err := t.targetWindow(ctx, args)
	if err != nil {
		return nil, err
	}
	if host.NeedsTarget(a) {
		err = t.Host.WindowMove(ctx, a, w.ID, target)
	} else {
		err = t.Host.WindowDo(ctx, a, w.ID)
	}
	if err != nil {
		return nil, err
	}
	t.acted(w)
	out := map[string]any{"window_id": w.ID, "action": string(a), "matched_window": windowName(w)}
	if target != "" {
		out["target"] = target
	}
	t.island(out, islandText(a, w, target), false)
	return out, nil
}

// appWindow is a window of the app a close-all names: "the terminal" with two terminals open
// means that app, not "which one?". A reference word ("it", "this") is resolved as usual.
func (t *Tools) appWindow(ctx context.Context, args map[string]any) (host.Window, error) {
	name, _ := args["window_id"].(string)
	word := strings.ToLower(strings.TrimSpace(name))
	if slices.Contains(focusedWords, word) || slices.Contains(itWords, word) || slices.Contains(otherWords, word) {
		return t.targetWindow(ctx, args)
	}
	ws, err := t.Host.ListWindows(ctx)
	if err != nil {
		return host.Window{}, err
	}
	var apps []host.Window // the first window of each app
	var cands []winmatch.Candidate
	for i, w := range ws {
		if w.App == "" || slices.ContainsFunc(apps, func(x host.Window) bool { return strings.EqualFold(x.App, w.App) }) {
			continue
		}
		apps = append(apps, w)
		cands = append(cands, winmatch.Candidate{Apps: []string{w.App}, Recency: i, Visible: !w.Minimized})
	}
	m, err := winmatch.PickUniqueMatch(cands, windowQuery(name), -1)
	if err != nil {
		var we *winmatch.Error
		if errors.As(err, &we) {
			return host.Window{}, &windowError{match: we, windows: ws}
		}
		return host.Window{}, err
	}
	return apps[m.Index], nil
}

// itFor is how long "it" means the window Jarvis last acted on (Q7).
const itFor = 60 * time.Second

// islandFor and islandFailFor are how long the island shows a window command's outcome.
const (
	islandFor     = 3 * time.Second
	islandFailFor = 5 * time.Second
)

var (
	focusedWords = []string{"", "this", "this window", "this one", "current", "the current window", "the window in front",
		"the front window", "the active window", "the focused window", "focused", "active"}
	itWords    = []string{"it", "that", "that window", "that one"}
	otherWords = []string{"the other one", "the other window", "other", "the other", "the previous one",
		"the previous window", "previous", "the last window", "the window before"}
)

// saidKey carries the turn's voice command into a call (Tools.Call).
type saidKey struct{}

var (
	reSaidOther = regexp.MustCompile(`(?i)\b(the other (one|window)|the previous (one|window)|switch back)\b`)
	reSaidIt    = regexp.MustCompile(`(?i)\b(it|that)\b`)
)

// saidReference: when the model leaves the window out, what the user said decides between "the
// other one", "it" and the focused window. Small models drop those words from the call
// (toolbench: "switch to the other window" became a bare focus every time).
func saidReference(ctx context.Context) string {
	said, _ := ctx.Value(saidKey{}).(string)
	switch {
	case reSaidOther.MatchString(said):
		return "the other one"
	case reSaidIt.MatchString(said):
		return "it"
	}
	return ""
}

// targetWindow is the window a call means (DESKTOP-COMMANDS.md Q7, resolved here, never in
// host): nothing or "this" is the focused window; "it" or "that" the last one Jarvis acted on,
// for a minute, else the focused one; "the other one" the one focused before it (where the
// host lists windows newest first: Hyprland, sway); a name is matched against the list.
func (t *Tools) targetWindow(ctx context.Context, args map[string]any) (host.Window, error) {
	name, _ := args["window"].(string)
	if name == "" {
		name, _ = args["window_id"].(string)
	}
	name = strings.TrimSpace(name)
	word := strings.ToLower(name)
	if word == "" {
		word = saidReference(ctx)
	}
	if !slices.Contains(focusedWords, word) && !slices.Contains(itWords, word) && !slices.Contains(otherWords, word) {
		w, ok, err := t.resolveWindow(ctx, name)
		if err != nil {
			return host.Window{}, err
		}
		if !ok {
			return host.Window{ID: name}, nil // the list can't be read: the host judges the id
		}
		return w, nil
	}
	ws, err := t.Host.ListWindows(ctx)
	if err != nil {
		return host.Window{}, err
	}
	if slices.Contains(itWords, word) {
		t.mu.Lock()
		last, at := t.lastActed, t.actedAt
		t.mu.Unlock()
		if last.ID != "" && t.now().Sub(at) <= itFor {
			for _, w := range ws {
				if w.ID == last.ID {
					return w, nil
				}
			}
		}
	}
	if slices.Contains(otherWords, word) {
		if len(ws) > 1 && ws[0].Focused {
			return ws[1], nil
		}
		return host.Window{}, fmt.Errorf("%w: this computer doesn't say which window was in front before; say which window", errBadArgs)
	}
	for _, w := range ws {
		if w.Focused {
			return w, nil
		}
	}
	return host.Window{}, fmt.Errorf("%w: no window has focus here that Jarvis can see; say which window", errBadArgs)
}

func (t *Tools) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// acted remembers the window for "it".
func (t *Tools) acted(w host.Window) {
	t.mu.Lock()
	t.lastActed, t.actedAt = w, t.now()
	t.mu.Unlock()
}

// island marks out as shown on the island (Q9: desktop commands don't talk) and shows it.
func (t *Tools) island(out map[string]any, text string, failed bool) {
	out[node.IslandKey], out[node.IslandOnlyKey] = text, true
	if t.Show != nil {
		d := islandFor
		if failed {
			d = islandFailFor
		}
		t.Show(text, d)
	}
}

// shortName is how the island names a window: its app, else its title.
func shortName(w host.Window) string {
	if w.App != "" {
		return w.App
	}
	if w.Title != "" {
		return w.Title
	}
	return "the window"
}

// islandText says what was done, never what it hopes happened (Q9).
func islandText(a host.WindowAction, w host.Window, target string) string {
	n := shortName(w)
	switch a {
	case host.ActionFocus:
		return "Switched to " + n
	case host.ActionMinimize:
		return "Minimized " + n
	case host.ActionUnminimize, host.ActionUnmaximize:
		return "Restored " + n
	case host.ActionMaximize:
		return "Maximized " + n
	case host.ActionFullscreen:
		return n + " fullscreen"
	case host.ActionSnapLeft:
		return "Snapped " + n + " left"
	case host.ActionSnapRight:
		return "Snapped " + n + " right"
	case host.ActionMoveToWorkspace:
		return "Moved " + n + " to workspace " + target
	case host.ActionMoveToMonitor:
		switch target {
		case "next", "previous":
			return "Moved " + n + " to the " + target + " monitor"
		}
		return "Moved " + n + " to monitor " + target
	case host.ActionClose:
		return "Closed " + n
	}
	return n + ": " + string(a)
}

// close is desktop_close_window: one window (no question: apps ask before losing work, Q8), or
// every window of its app, which asks first when confirmation is on.
func (t *Tools) close(ctx context.Context, args map[string]any) (map[string]any, error) {
	all, _ := args["all_of_app"].(bool)
	var w host.Window
	var err error
	if all {
		w, err = t.appWindow(ctx, args)
	} else {
		w, err = t.targetWindow(ctx, args)
	}
	if err != nil {
		return nil, err
	}
	if !all || w.App == "" {
		if err := t.Host.WindowDo(ctx, host.ActionClose, w.ID); err != nil {
			return nil, err
		}
		t.acted(w)
		out := map[string]any{"window_id": w.ID, "action": "close", "matched_window": windowName(w)}
		t.island(out, islandText(host.ActionClose, w, ""), false)
		return out, nil
	}
	ws, err := t.Host.ListWindows(ctx)
	if err != nil {
		return nil, err
	}
	var same []host.Window
	for _, x := range ws {
		if strings.EqualFold(x.App, w.App) {
			same = append(same, x)
		}
	}
	act := func(ctx context.Context) (map[string]any, error) {
		closed := 0
		for _, x := range same {
			if err := t.Host.WindowDo(ctx, host.ActionClose, x.ID); err == nil {
				closed++
			}
		}
		out := map[string]any{"success": closed > 0, "action": "close", "app": w.App, "closed": closed, "of": len(same)}
		t.island(out, fmt.Sprintf("Closed %d %s window%s", closed, w.App, map[bool]string{true: "", false: "s"}[closed == 1]), closed == 0)
		return out, nil
	}
	if t.Confirm.On() {
		return t.Confirm.Ask(fmt.Sprintf("Close all %d %s windows?", len(same), w.App), act), nil
	}
	return act(ctx)
}
