package desktop

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
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
			Description: "The app or window as the user said it ('Firefox', 'the terminal'), or an id from desktop_list_windows. Leave out for the window in front"},
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
	out := map[string]any{"window_id": w.ID, "action": string(a), "matched_window": windowName(w)}
	if target != "" {
		out["target"] = target
	}
	return out, nil
}

// targetWindow is the window a desktop_window call means: the one named, else the focused one.
func (t *Tools) targetWindow(ctx context.Context, args map[string]any) (host.Window, error) {
	name, _ := args["window"].(string)
	name = strings.TrimSpace(name)
	switch strings.ToLower(name) {
	case "", "this", "this window", "current", "the current window", "the window in front",
		"the front window", "the active window", "the focused window", "focused", "active":
		ws, err := t.Host.ListWindows(ctx)
		if err != nil {
			return host.Window{}, err
		}
		for _, w := range ws {
			if w.Focused {
				return w, nil
			}
		}
		return host.Window{}, fmt.Errorf("%w: no window has focus here that Jarvis can see; say which window", errBadArgs)
	}
	w, ok, err := t.resolveWindow(ctx, name)
	if err != nil {
		return host.Window{}, err
	}
	if !ok {
		return host.Window{ID: name}, nil // the list can't be read: the host judges the id
	}
	return w, nil
}
