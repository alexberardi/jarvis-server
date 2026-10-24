package host

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
)

// Wayland forbids one client from controlling another's windows, but two compositors expose
// their own IPC for it: Hyprland (hyprctl) and sway (swaymsg). On those, Linux lists, focuses
// and closes windows through it. Neither has a real minimise (a special workspace or the
// scratchpad is a stand-in that behaves differently), so minimise is never offered there
// (docs/agent/DESKTOP-COMMANDS.md, Q4: native actions only).
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
		return l.hyprDispatch(ctx, []string{"focuswindow", "address:" + id},
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
		return l.hyprDispatch(ctx, []string{"closewindow", "address:" + id},
			fmt.Sprintf(`hl.dsp.window.close({ window = "address:%s" })`, id))
	}
	_, err := l.run(ctx, "swaymsg", fmt.Sprintf("[con_id=%s]", id), "kill")
	return err
}

// --- Hyprland ---

// hyprDispatch runs a dispatcher in its classic form, then its Lua one. A Hyprland configured in
// Lua (hyprland.lua, 0.55+) reads `hyprctl dispatch` as Lua, where the classic form is a syntax
// error (exit status 7).
func (l *Linux) hyprDispatch(ctx context.Context, classic []string, lua string) error {
	run := func(args ...string) error {
		out, err := l.run(ctx, "hyprctl", append([]string{"dispatch"}, args...)...)
		if err != nil {
			return err
		}
		if out != "" && out != "ok" {
			return Errorf(ErrOperationFailed, "hyprctl: %s", out)
		}
		return nil
	}
	err := run(classic...)
	if err == nil {
		return nil
	}
	if lerr := run(lua); lerr != nil {
		return err
	}
	return nil
}

type hyprClient struct {
	Address        string `json:"address"`
	Title          string `json:"title"`
	Class          string `json:"class"`
	Mapped         *bool  `json:"mapped"`
	FocusHistoryID int    `json:"focusHistoryID"`
}

// hyprList: the mapped clients, most recently focused first (focusHistoryID 0 is the newest).
func (l *Linux) hyprList(ctx context.Context) ([]Window, error) {
	raw, err := l.run(ctx, "hyprctl", "-j", "clients")
	if err != nil {
		return nil, err
	}
	var clients []hyprClient
	if err := json.Unmarshal([]byte(raw), &clients); err != nil {
		return nil, Errorf(ErrOperationFailed, "hyprctl clients: %v", err)
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
		out = append(out, Window{ID: c.Address, Title: c.Title, App: c.Class, Focused: c.Address == active.Address})
	}
	return out, nil
}

// --- sway ---

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
	n    *swayNode
	path []int // position in each ancestor's focus list: smaller was focused more recently
}

func (n *swayNode) leaves(path []int, out *[]swayLeaf) {
	kids := make([]*swayNode, 0, len(n.Nodes)+len(n.FloatingNodes))
	for i := range n.Nodes {
		kids = append(kids, &n.Nodes[i])
	}
	for i := range n.FloatingNodes {
		kids = append(kids, &n.FloatingNodes[i])
	}
	if len(kids) == 0 {
		if n.window() {
			*out = append(*out, swayLeaf{n: n, path: path})
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
		k.leaves(append(append([]int(nil), path...), pos), out)
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
		out[i] = Window{ID: strconv.FormatInt(lf.n.ID, 10), Title: lf.n.Name, App: app, Focused: lf.n.Focused}
	}
	return out, nil
}
