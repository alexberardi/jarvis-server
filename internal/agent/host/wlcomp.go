package host

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Wayland forbids one client from controlling another's windows, but two compositors expose
// their own IPC for it: Hyprland (hyprctl) and sway (swaymsg). On those, Linux lists, focuses,
// closes and arranges windows through it. Only native actions are offered
// (docs/agent/DESKTOP-COMMANDS.md, Q4): neither has a real minimise (a special workspace or the
// scratchpad is a stand-in that behaves differently), and sway has no maximise, only fullscreen.
// Hyprland's maximise is its fullscreen mode 1 ("maximized"); unmaximize leaves either mode.
//
// dictate/linuxwin.go still has its own copy of this for typing into a named window; it moves
// onto this one in a follow-up.

const (
	compHyprland = "hyprland"
	compSway     = "sway"
)

// compositor names the session's controllable Wayland compositor, or "".
func (l *Linux) compositor() string {
	if l.Sys.Getenv("WAYLAND_DISPLAY") == "" {
		return ""
	}
	switch {
	case l.Sys.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" && l.Sys.LookPath("hyprctl"):
		return compHyprland
	case l.Sys.Getenv("SWAYSOCK") != "" && l.Sys.LookPath("swaymsg"):
		return compSway
	}
	return ""
}

var (
	reHyprAddr = regexp.MustCompile(`^0x[0-9a-fA-F]{1,16}$`)
	reSwayID   = regexp.MustCompile(`^[0-9]{1,12}$`)
)

func (l *Linux) compListWindows(ctx context.Context, comp string) ([]Window, error) {
	if comp == compHyprland {
		return l.hyprList(ctx)
	}
	return l.swayList(ctx)
}

// compRequireWindow checks the id's format for this compositor, then that it is still open.
func (l *Linux) compRequireWindow(ctx context.Context, comp, id string) error {
	re := reHyprAddr
	if comp == compSway {
		re = reSwayID
	}
	if !re.MatchString(id) {
		return Errorf(ErrWindowNotFound, "not a %s window id: %q", comp, id)
	}
	ws, err := l.compListWindows(ctx, comp)
	if err != nil {
		return err
	}
	for _, w := range ws {
		if w.ID == id {
			return nil
		}
	}
	return Errorf(ErrWindowNotFound, "window %s no longer exists", id)
}

func (l *Linux) compFocus(ctx context.Context, comp, id string) error {
	if err := l.compRequireWindow(ctx, comp, id); err != nil {
		return err
	}
	if comp == compHyprland {
		return l.hyprDispatch(ctx, []string{"dispatch", "focuswindow", "address:" + id},
			fmt.Sprintf(`hl.dsp.focus({ window = "address:%s" })`, id))
	}
	_, err := l.run(ctx, "swaymsg", fmt.Sprintf("[con_id=%s]", id), "focus")
	return err
}

// compClose is a graceful close request on both: Hyprland's closewindow and sway's kill send
// the app its close event (xdg_toplevel.close, or WM_DELETE_WINDOW for XWayland), exactly as
// its close button would. Neither signals the process.
func (l *Linux) compClose(ctx context.Context, comp, id string) error {
	if err := l.compRequireWindow(ctx, comp, id); err != nil {
		return err
	}
	if comp == compHyprland {
		return l.hyprDispatch(ctx, []string{"dispatch", "closewindow", "address:" + id},
			fmt.Sprintf(`hl.dsp.window.close({ window = "address:%s" })`, id))
	}
	_, err := l.run(ctx, "swaymsg", fmt.Sprintf("[con_id=%s]", id), "kill")
	return err
}

// compCapabilities: what this compositor can do now. Moving to a monitor needs a second one.
func (l *Linux) compCapabilities(ctx context.Context, comp string) []string {
	caps := []string{CapWindowsList, CapWindowsFocus, CapWindowsClose, CapWindowsUnmaximize,
		CapWindowsFullscreen, CapWindowsSnap, CapWindowsWorkspace}
	if comp == compHyprland {
		caps = append(caps, CapWindowsMaximize)
	}
	if ms, err := l.compMonitors(ctx, comp); err == nil && len(ms) > 1 {
		caps = append(caps, CapWindowsMonitor)
	}
	return caps
}

// compMonitors names the monitors (Hyprland's monitors, sway's active outputs) in their order.
func (l *Linux) compMonitors(ctx context.Context, comp string) ([]string, error) {
	var raw string
	var err error
	if comp == compHyprland {
		raw, err = l.run(ctx, "hyprctl", "-j", "monitors")
	} else {
		raw, err = l.run(ctx, "swaymsg", "-t", "get_outputs")
	}
	if err != nil {
		return nil, err
	}
	var ms []struct {
		Name   string `json:"name"`
		Active *bool  `json:"active"`
	}
	if err := json.Unmarshal([]byte(raw), &ms); err != nil {
		return nil, Errorf(ErrOperationFailed, "%s monitors: %v", comp, err)
	}
	var names []string
	for _, m := range ms {
		if m.Active == nil || *m.Active {
			names = append(names, m.Name)
		}
	}
	return names, nil
}

// compMonitor resolves a validated monitor target to the compositor's spelling: Hyprland's
// "+1"/"-1" or sway's right/left for next/previous, a position to its name, a name as is.
func (l *Linux) compMonitor(ctx context.Context, comp, target string) (string, error) {
	switch target {
	case "next":
		return map[string]string{compHyprland: "+1", compSway: "right"}[comp], nil
	case "previous":
		return map[string]string{compHyprland: "-1", compSway: "left"}[comp], nil
	}
	ms, err := l.compMonitors(ctx, comp)
	if err != nil {
		return "", err
	}
	if n, err := strconv.Atoi(target); err == nil {
		if n > len(ms) {
			return "", Errorf(ErrInvalidTarget, "there is no monitor %d (%d connected)", n, len(ms))
		}
		return ms[n-1], nil
	}
	for _, m := range ms {
		if strings.EqualFold(m, target) {
			return m, nil
		}
	}
	return "", Errorf(ErrInvalidTarget, "no monitor named %q (connected: %s)", target, strings.Join(ms, ", "))
}

func (l *Linux) compArrange(ctx context.Context, comp, id string, a WindowAction, target string) error {
	if err := l.compRequireWindow(ctx, comp, id); err != nil {
		return err
	}
	if a == ActionMoveToMonitor {
		m, err := l.compMonitor(ctx, comp, target)
		if err != nil {
			return err
		}
		target = m
	}
	if comp == compHyprland {
		return l.hyprArrange(ctx, id, a, target)
	}
	return l.swayArrange(ctx, id, a, target)
}

// --- Hyprland ---

func (l *Linux) hyprArrange(ctx context.Context, addr string, a WindowAction, target string) error {
	sel := `window = "address:` + addr + `"`
	fs := func(mode, action string) error {
		internal := map[string]string{"maximized": "1", "fullscreen": "2"}[mode]
		if action == "unset" {
			internal = "0"
		}
		return l.hyprDispatch(ctx, hyprFocused(addr, "fullscreenstate "+internal+" -1"),
			fmt.Sprintf(`hl.dsp.window.fullscreen({ mode = "%s", action = "%s", %s })`, mode, action, sel))
	}
	switch a {
	case ActionMaximize:
		return fs("maximized", "set")
	case ActionFullscreen:
		return fs("fullscreen", "set")
	case ActionUnmaximize:
		clients, err := l.hyprClients(ctx)
		if err != nil {
			return err
		}
		for _, c := range clients {
			if c.Address == addr {
				switch c.Fullscreen {
				case 1:
					return fs("maximized", "unset")
				case 2:
					return fs("fullscreen", "unset")
				}
			}
		}
		return nil // already neither
	case ActionSnapLeft, ActionSnapRight:
		dir := map[WindowAction]string{ActionSnapLeft: "l", ActionSnapRight: "r"}[a]
		return l.hyprDispatch(ctx, hyprFocused(addr, "movewindow "+dir),
			fmt.Sprintf(`hl.dsp.window.move({ direction = "%s", %s })`, dir, sel))
	case ActionMoveToWorkspace:
		return l.hyprDispatch(ctx, []string{"dispatch", "movetoworkspacesilent", target + ",address:" + addr},
			fmt.Sprintf(`hl.dsp.window.move({ workspace = %s, follow = false, %s })`, target, sel))
	case ActionMoveToMonitor:
		return l.hyprDispatch(ctx, hyprFocused(addr, "movewindow mon:"+target),
			fmt.Sprintf(`hl.dsp.window.move({ monitor = "%s", %s })`, target, sel))
	}
	return Errorf(ErrNotSupported, "%s is not available on Hyprland", a)
}

// hyprDispatch runs a dispatcher in its classic form (hyprctl's arguments: "dispatch …", or
// "--batch" for the ones that act on the focused window and need a focus first), then its Lua
// one. A Hyprland configured in Lua (hyprland.lua, 0.55+) reads `hyprctl dispatch` as Lua, where
// the classic form is a syntax error (exit status 7); its Lua dispatchers take the window.
func (l *Linux) hyprDispatch(ctx context.Context, classic []string, lua string) error {
	run := func(args ...string) error {
		out, err := l.run(ctx, "hyprctl", args...)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(out, "\n") {
			if line = strings.TrimSpace(line); line != "" && line != "ok" {
				return Errorf(ErrOperationFailed, "hyprctl: %s", out)
			}
		}
		return nil
	}
	err := run(classic...)
	if err == nil {
		return nil
	}
	if lerr := run("dispatch", lua); lerr != nil {
		return err
	}
	return nil
}

// hyprFocused is the classic form of a dispatcher that acts on the focused window: focus it,
// then run it, in one batch.
func hyprFocused(addr, dispatcher string) []string {
	return []string{"--batch", "dispatch focuswindow address:" + addr + " ; dispatch " + dispatcher}
}

type hyprClient struct {
	Address        string `json:"address"`
	Title          string `json:"title"`
	Class          string `json:"class"`
	Mapped         *bool  `json:"mapped"`
	FocusHistoryID int    `json:"focusHistoryID"`
	Fullscreen     int    `json:"fullscreen"` // 0 none, 1 maximized, 2 fullscreen
	Workspace      struct {
		Name string `json:"name"`
	} `json:"workspace"`
}

func (l *Linux) hyprClients(ctx context.Context) ([]hyprClient, error) {
	raw, err := l.run(ctx, "hyprctl", "-j", "clients")
	if err != nil {
		return nil, err
	}
	var clients []hyprClient
	if err := json.Unmarshal([]byte(raw), &clients); err != nil {
		return nil, Errorf(ErrOperationFailed, "hyprctl clients: %v", err)
	}
	return clients, nil
}

// hyprList: the mapped clients, most recently focused first (focusHistoryID 0 is the newest).
func (l *Linux) hyprList(ctx context.Context) ([]Window, error) {
	clients, err := l.hyprClients(ctx)
	if err != nil {
		return nil, err
	}
	var active struct {
		Address string `json:"address"`
	}
	if raw, err := l.run(ctx, "hyprctl", "-j", "activewindow"); err == nil {
		_ = json.Unmarshal([]byte(raw), &active) // "{}" when nothing has focus
	}
	sort.SliceStable(clients, func(a, b int) bool { return clients[a].FocusHistoryID < clients[b].FocusHistoryID })
	out := []Window{}
	for _, c := range clients {
		if !reHyprAddr.MatchString(c.Address) || (c.Mapped != nil && !*c.Mapped) {
			continue
		}
		out = append(out, Window{ID: c.Address, Title: c.Title, App: c.Class, Focused: c.Address == active.Address,
			Workspace: c.Workspace.Name})
	}
	return out, nil
}

// --- sway ---

func (l *Linux) swayArrange(ctx context.Context, id string, a WindowAction, target string) error {
	var cmd []string
	switch a {
	case ActionFullscreen:
		cmd = []string{"fullscreen", "enable"}
	case ActionUnmaximize:
		cmd = []string{"fullscreen", "disable"}
	case ActionSnapLeft:
		cmd = []string{"move", "left"}
	case ActionSnapRight:
		cmd = []string{"move", "right"}
	case ActionMoveToWorkspace:
		cmd = []string{"move", "container", "to", "workspace", "number", target}
	case ActionMoveToMonitor:
		cmd = []string{"move", "container", "to", "output", target}
	default:
		return Errorf(ErrNotSupported, "%s is not available on sway", a)
	}
	_, err := l.run(ctx, "swaymsg", append([]string{fmt.Sprintf("[con_id=%s]", id)}, cmd...)...)
	return err
}

type swayNode struct {
	ID               int64                   `json:"id"`
	Type             string                  `json:"type"`
	Name             string                  `json:"name"`
	AppID            string                  `json:"app_id"`
	Pid              int                     `json:"pid"`
	Focused          bool                    `json:"focused"`
	Focus            []int64                 `json:"focus"`
	WindowProperties *struct{ Class string } `json:"window_properties"`
	Nodes            []swayNode              `json:"nodes"`
	FloatingNodes    []swayNode              `json:"floating_nodes"`
}

func (n *swayNode) window() bool {
	return (n.Type == "con" || n.Type == "floating_con") && len(n.Nodes) == 0 && len(n.FloatingNodes) == 0 &&
		(n.AppID != "" || n.WindowProperties != nil || n.Pid > 0)
}

type swayLeaf struct {
	n         *swayNode
	path      []int // position in each ancestor's focus list: smaller was focused more recently
	workspace string
}

func (n *swayNode) leaves(path []int, out *[]swayLeaf) { n.leavesIn("", path, out) }

func (n *swayNode) leavesIn(ws string, path []int, out *[]swayLeaf) {
	if n.Type == "workspace" {
		ws = n.Name
	}
	kids := make([]*swayNode, 0, len(n.Nodes)+len(n.FloatingNodes))
	for i := range n.Nodes {
		kids = append(kids, &n.Nodes[i])
	}
	for i := range n.FloatingNodes {
		kids = append(kids, &n.FloatingNodes[i])
	}
	if len(kids) == 0 {
		if n.window() {
			*out = append(*out, swayLeaf{n: n, path: path, workspace: ws})
		}
		return
	}
	for i, k := range kids {
		pos := len(n.Focus) + i
		for j, id := range n.Focus {
			if id == k.ID {
				pos = j
				break
			}
		}
		k.leavesIn(ws, append(append([]int(nil), path...), pos), out)
	}
}

// swayList: every window in the tree, most recently focused first.
func (l *Linux) swayList(ctx context.Context) ([]Window, error) {
	raw, err := l.run(ctx, "swaymsg", "-t", "get_tree")
	if err != nil {
		return nil, err
	}
	var root swayNode
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return nil, Errorf(ErrOperationFailed, "swaymsg get_tree: %v", err)
	}
	var ls []swayLeaf
	root.leaves(nil, &ls)
	sort.SliceStable(ls, func(a, b int) bool {
		pa, pb := ls[a].path, ls[b].path
		for i := 0; i < len(pa) && i < len(pb); i++ {
			if pa[i] != pb[i] {
				return pa[i] < pb[i]
			}
		}
		return len(pa) < len(pb)
	})
	out := make([]Window, len(ls))
	for i, lf := range ls {
		app := lf.n.AppID
		if app == "" && lf.n.WindowProperties != nil {
			app = lf.n.WindowProperties.Class
		}
		out[i] = Window{ID: strconv.FormatInt(lf.n.ID, 10), Title: lf.n.Name, App: app, Focused: lf.n.Focused, Workspace: lf.workspace}
	}
	return out, nil
}
