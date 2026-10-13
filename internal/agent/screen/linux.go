package screen

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Wayland captures with grim (wlroots screencopy): Hyprland and sway. The focused window's
// geometry comes from the compositor (hyprctl / swaymsg), in the layout coordinates grim's -g
// takes. GNOME and KDE don't offer screencopy; they need the xdg-desktop-portal Screenshot
// path (later), as does X11.
type Wayland struct {
	X Exec
}

// compositor names the supported compositor of this session, or "".
func (l *Wayland) compositor() string {
	if l.X.Getenv("WAYLAND_DISPLAY") == "" {
		return ""
	}
	switch {
	case l.X.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" && l.X.LookPath("hyprctl"):
		return "hyprland"
	case l.X.Getenv("SWAYSOCK") != "" && l.X.LookPath("swaymsg"):
		return "sway"
	}
	return ""
}

func (l *Wayland) Available(context.Context) (bool, string) {
	if l.compositor() == "" {
		return false, "reading the screen needs Hyprland or sway on this Linux desktop (GNOME, KDE and X11 aren't supported yet)"
	}
	if !l.X.LookPath("grim") {
		return false, "reading the screen needs grim installed (the Wayland screenshot tool)"
	}
	return true, ""
}

// geometry is a region in layout coordinates.
type geometry struct{ X, Y, W, H int }

func (g geometry) arg() string { return fmt.Sprintf("%d,%d %dx%d", g.X, g.Y, g.W, g.H) }

// target is what to capture: a region (window) or an output (screen).
type target struct {
	region *geometry
	output string
	title  string
	app    string
}

func (l *Wayland) Capture(ctx context.Context, scope, window string) (Shot, error) {
	var (
		t   target
		err error
	)
	named := normalizeQuery(window) != ""
	switch c := l.compositor(); {
	case c == "hyprland" && named:
		t, err = l.hyprlandNamed(ctx, window)
	case c == "hyprland":
		t, err = l.hyprland(ctx, scope)
	case c == "sway":
		t, err = l.sway(ctx, scope, window)
	default:
		return Shot{}, ErrUnavailable
	}
	if err != nil {
		return Shot{}, err
	}
	args := []string{"-t", "png", "-l", "1"}
	switch {
	case t.region != nil:
		args = append(args, "-g", t.region.arg())
	case t.output != "":
		args = append(args, "-o", t.output)
	}
	png, err := l.X.Output(ctx, "grim", append(args, "-")...)
	if err != nil {
		return Shot{}, err
	}
	img, err := decode(png)
	if err != nil {
		return Shot{}, err
	}
	return Shot{Image: img, Title: t.title, App: t.app}, nil
}

func (l *Wayland) hyprland(ctx context.Context, scope string) (target, error) {
	if scope == ScopeWindow {
		b, err := l.X.Output(ctx, "hyprctl", "-j", "activewindow")
		if err != nil {
			return target{}, err
		}
		var w struct {
			At    []int  `json:"at"`
			Size  []int  `json:"size"`
			Title string `json:"title"`
			Class string `json:"class"`
		}
		if json.Unmarshal(b, &w) == nil && len(w.At) == 2 && len(w.Size) == 2 && w.Size[0] > 0 && w.Size[1] > 0 {
			return target{region: &geometry{w.At[0], w.At[1], w.Size[0], w.Size[1]}, title: w.Title, app: w.Class}, nil
		}
		// No focused window (an empty workspace): the screen instead.
	}
	b, err := l.X.Output(ctx, "hyprctl", "-j", "monitors")
	if err != nil {
		return target{}, err
	}
	var mons []struct {
		Name    string `json:"name"`
		Focused bool   `json:"focused"`
	}
	if err := json.Unmarshal(b, &mons); err != nil {
		return target{}, fmt.Errorf("hyprctl monitors: %w", err)
	}
	for _, m := range mons {
		if m.Focused {
			return target{output: m.Name}, nil
		}
	}
	return target{}, nil // every output
}

// hyprClient is the slice of `hyprctl -j clients` a named capture needs.
type hyprClient struct {
	At             []int            `json:"at"`
	Size           []int            `json:"size"`
	Title          string           `json:"title"`
	Class          string           `json:"class"`
	InitialClass   string           `json:"initialClass"`
	Workspace      struct{ ID int } `json:"workspace"`
	Monitor        int              `json:"monitor"`
	Mapped         *bool            `json:"mapped"`
	Hidden         bool             `json:"hidden"`  // a group's inactive member
	Visible        *bool            `json:"visible"` // newer Hyprland only
	FocusHistoryID int              `json:"focusHistoryID"`
	Fullscreen     json.RawMessage  `json:"fullscreen"` // int mode (newer) or bool (older)
}

func (c hyprClient) fullscreen() bool {
	s := string(c.Fullscreen)
	return s != "" && s != "0" && s != "false" && s != "null"
}

// hyprlandNamed captures the window the user named: one on a workspace some monitor shows now,
// not a group's hidden tab, not under another window's fullscreen. grim captures a region of
// the screen, so a window that isn't on it would capture whatever is there instead.
func (l *Wayland) hyprlandNamed(ctx context.Context, window string) (target, error) {
	b, err := l.X.Output(ctx, "hyprctl", "-j", "clients")
	if err != nil {
		return target{}, err
	}
	var clients []hyprClient
	if err := json.Unmarshal(b, &clients); err != nil {
		return target{}, fmt.Errorf("hyprctl clients: %w", err)
	}
	b, err = l.X.Output(ctx, "hyprctl", "-j", "monitors")
	if err != nil {
		return target{}, err
	}
	var mons []struct {
		ActiveWorkspace  struct{ ID int } `json:"activeWorkspace"`
		SpecialWorkspace struct{ ID int } `json:"specialWorkspace"`
		Disabled         bool             `json:"disabled"`
	}
	if err := json.Unmarshal(b, &mons); err != nil {
		return target{}, fmt.Errorf("hyprctl monitors: %w", err)
	}
	shown := map[int]bool{}
	for _, m := range mons {
		switch {
		case m.Disabled:
		case m.SpecialWorkspace.ID != 0: // a shown special workspace covers the regular one
			shown[m.SpecialWorkspace.ID] = true
		default:
			shown[m.ActiveWorkspace.ID] = true
		}
	}
	fullscreenOn := map[int]int{} // workspace → the index of its fullscreen window
	for i, c := range clients {
		if c.fullscreen() && (c.Mapped == nil || *c.Mapped) && !c.Hidden {
			fullscreenOn[c.Workspace.ID] = i
		}
	}
	cands := make([]candidate, len(clients))
	for i, c := range clients {
		fs, covered := fullscreenOn[c.Workspace.ID]
		cands[i] = candidate{Apps: []string{c.Class, c.InitialClass}, Title: c.Title, Recency: c.FocusHistoryID,
			Visible: shown[c.Workspace.ID] && (c.Mapped == nil || *c.Mapped) && !c.Hidden && (c.Visible == nil || *c.Visible) &&
				(!covered || fs == i) && len(c.At) == 2 && len(c.Size) == 2 && c.Size[0] > 0 && c.Size[1] > 0}
	}
	i, err := pickWindow(cands, window)
	if err != nil {
		return target{}, err
	}
	c := clients[i]
	return target{region: &geometry{c.At[0], c.At[1], c.Size[0], c.Size[1]}, title: c.Title, app: c.Class}, nil
}

// swayNode is the slice of sway's tree a capture needs.
type swayNode struct {
	ID               int64   `json:"id"`
	Type             string  `json:"type"`
	Name             string  `json:"name"`
	AppID            string  `json:"app_id"`
	Pid              int     `json:"pid"`
	Focused          bool    `json:"focused"`
	Focus            []int64 `json:"focus"` // children, most recently focused first
	Visible          *bool   `json:"visible"`
	CurrentWorkspace string  `json:"current_workspace"` // outputs
	WindowProperties *struct {
		Class    string `json:"class"`
		Instance string `json:"instance"`
	} `json:"window_properties"` // XWayland windows
	Rect          swayRect   `json:"rect"`
	Nodes         []swayNode `json:"nodes"`
	FloatingNodes []swayNode `json:"floating_nodes"`
}

type swayRect struct{ X, Y, Width, Height int }

// focusedPath returns the focused node and the output it is on.
func (n *swayNode) focused(output string) (*swayNode, string) {
	if n.Type == "output" {
		output = n.Name
	}
	if n.Focused {
		return n, output
	}
	for _, list := range [][]swayNode{n.Nodes, n.FloatingNodes} {
		for i := range list {
			if f, o := list[i].focused(output); f != nil {
				return f, o
			}
		}
	}
	return nil, ""
}

// swayView is one window of the tree: the node, whether it is on screen, and its focus path
// (positions in each ancestor's focus list; a smaller path was focused more recently).
type swayView struct {
	n       *swayNode
	visible bool
	path    []int
}

func (n *swayNode) views(path []int, shownWS bool, out *[]swayView) {
	kids := make([]*swayNode, 0, len(n.Nodes)+len(n.FloatingNodes))
	for i := range n.Nodes {
		kids = append(kids, &n.Nodes[i])
	}
	for i := range n.FloatingNodes {
		kids = append(kids, &n.FloatingNodes[i])
	}
	if len(kids) == 0 {
		if (n.Type == "con" || n.Type == "floating_con") && (n.AppID != "" || n.WindowProperties != nil || n.Pid > 0) {
			*out = append(*out, swayView{n: n, path: path,
				visible: shownWS && (n.Visible == nil || *n.Visible) && n.Rect.Width > 0 && n.Rect.Height > 0})
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
		shown := shownWS
		if k.Type == "workspace" {
			shown = n.Type == "output" && !strings.HasPrefix(n.Name, "__") && k.Name == n.CurrentWorkspace
		}
		k.views(append(append([]int(nil), path...), pos), shown, out)
	}
}

func lessPath(a, b []int) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

func (l *Wayland) sway(ctx context.Context, scope, window string) (target, error) {
	b, err := l.X.Output(ctx, "swaymsg", "-t", "get_tree")
	if err != nil {
		return target{}, err
	}
	var root swayNode
	if err := json.Unmarshal(b, &root); err != nil {
		return target{}, fmt.Errorf("swaymsg get_tree: %w", err)
	}
	if normalizeQuery(window) != "" {
		var vs []swayView
		root.views(nil, false, &vs)
		sort.SliceStable(vs, func(a, b int) bool { return lessPath(vs[a].path, vs[b].path) })
		cands := make([]candidate, len(vs))
		for i, v := range vs {
			apps := []string{v.n.AppID}
			if v.n.WindowProperties != nil {
				apps = append(apps, v.n.WindowProperties.Class, v.n.WindowProperties.Instance)
			}
			cands[i] = candidate{Apps: apps, Title: v.n.Name, Recency: i, Visible: v.visible}
		}
		i, err := pickWindow(cands, window)
		if err != nil {
			return target{}, err
		}
		n := vs[i].n
		return target{region: &geometry{n.Rect.X, n.Rect.Y, n.Rect.Width, n.Rect.Height}, title: n.Name, app: cands[i].App()}, nil
	}
	f, output := root.focused("")
	if f == nil {
		return target{}, nil
	}
	if scope == ScopeWindow && (f.Type == "con" || f.Type == "floating_con") && f.Rect.Width > 0 && f.Rect.Height > 0 {
		app := f.AppID
		if app == "" && f.WindowProperties != nil {
			app = f.WindowProperties.Class
		}
		return target{region: &geometry{f.Rect.X, f.Rect.Y, f.Rect.Width, f.Rect.Height}, title: f.Name, app: app}, nil
	}
	return target{output: output}, nil
}
