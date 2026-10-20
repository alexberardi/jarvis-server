package screen

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// Window is one window on the screen for desktop tasks (computer.Desktop on Linux): whose it
// is, where it is in layout coordinates and the geometry a capture of it has.
type Window struct {
	ID         string // Hyprland's address, sway's con id
	App        string // Hyprland's class, sway's app_id (an Xwayland window's X11 class)
	PID        int
	Title      string
	Focused    bool
	X, Y, W, H int
	Geo        computeruse.Geometry // with the output's scale and name when known
}

// Windows lists the windows on the screen front to back (Hyprland or sway): those on a
// workspace an output shows now, a fullscreen one first, then floating ones above the tiled,
// each kind most recently focused first. Tiled windows don't overlap, so this is the stacking
// order a point is looked up in.
func (l *Wayland) Windows(ctx context.Context) ([]Window, error) {
	switch l.compositor() {
	case "hyprland":
		return l.hyprWindows(ctx)
	case "sway":
		return l.swayWindows(ctx)
	}
	return nil, ErrUnavailable
}

// stacked is a window with its sort keys: kind (0 fullscreen, 1 floating, 2 tiled) and recency.
type stacked struct {
	w       Window
	kind    int
	recency int
}

func sortStacked(s []stacked) []Window {
	sort.SliceStable(s, func(a, b int) bool {
		return cmp.Or(cmp.Compare(s[a].kind, s[b].kind), cmp.Compare(s[a].recency, s[b].recency)) < 0
	})
	out := make([]Window, len(s))
	for i, x := range s {
		out[i] = x.w
	}
	return out
}

func kind(fullscreen, floating bool) int {
	switch {
	case fullscreen:
		return 0
	case floating:
		return 1
	}
	return 2
}

func (l *Wayland) hyprWindows(ctx context.Context) ([]Window, error) {
	b, err := l.X.Output(ctx, "hyprctl", "-j", "clients")
	if err != nil {
		return nil, err
	}
	var clients []hyprClient
	if err := json.Unmarshal(b, &clients); err != nil {
		return nil, fmt.Errorf("hyprctl clients: %w", err)
	}
	mons, err := l.hyprMonitors(ctx)
	if err != nil {
		return nil, err
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
	mapped := func(c hyprClient) bool {
		return (c.Mapped == nil || *c.Mapped) && !c.Hidden && (c.Visible == nil || *c.Visible) &&
			len(c.At) == 2 && len(c.Size) == 2 && c.Size[0] > 0 && c.Size[1] > 0
	}
	fullscreenOn := map[int]int{} // workspace → its fullscreen window's index
	for i, c := range clients {
		if c.fullscreen() && mapped(c) {
			fullscreenOn[c.Workspace.ID] = i
		}
	}
	var s []stacked
	for i, c := range clients {
		if !mapped(c) || !(shown[c.Workspace.ID] || c.Pinned) {
			continue
		}
		if fs, covered := fullscreenOn[c.Workspace.ID]; covered && fs != i && !c.Pinned {
			continue
		}
		w := Window{ID: c.Address, App: cmp.Or(c.Class, c.InitialClass), PID: c.PID, Title: c.Title, Focused: c.FocusHistoryID == 0,
			X: c.At[0], Y: c.At[1], W: c.Size[0], H: c.Size[1], Geo: windowGeo(c.At, c.Size, mons, c.Monitor)}
		s = append(s, stacked{w: w, kind: kind(c.fullscreen(), c.Floating || c.Pinned), recency: c.FocusHistoryID})
	}
	return sortStacked(s), nil
}

func (l *Wayland) swayWindows(ctx context.Context) ([]Window, error) {
	b, err := l.X.Output(ctx, "swaymsg", "-t", "get_tree")
	if err != nil {
		return nil, err
	}
	var root swayNode
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("swaymsg get_tree: %w", err)
	}
	var vs []swayView
	root.views(nil, nil, false, &vs)
	sort.SliceStable(vs, func(a, b int) bool { return lessPath(vs[a].path, vs[b].path) })
	var s []stacked
	for i, v := range vs {
		if !v.visible {
			continue
		}
		n := v.n
		app := n.AppID
		if app == "" && n.WindowProperties != nil {
			app = n.WindowProperties.Class
		}
		w := Window{ID: strconv.FormatInt(n.ID, 10), App: app, PID: n.Pid, Title: n.Name, Focused: n.Focused,
			X: n.Rect.X, Y: n.Rect.Y, W: n.Rect.Width, H: n.Rect.Height, Geo: n.Rect.geometry(v.output)}
		s = append(s, stacked{w: w, kind: kind(n.FullscreenMode != 0, n.Type == "floating_con"), recency: i})
	}
	// A fullscreen window covers its output.
	full := map[string]string{}
	for _, x := range s {
		if x.kind == 0 {
			full[x.w.Geo.Monitor] = x.w.ID
		}
	}
	return slices.DeleteFunc(sortStacked(s), func(w Window) bool {
		id, ok := full[w.Geo.Monitor]
		return ok && id != w.ID
	}), nil
}

// CaptureWindow captures w's area of the screen (grim -g): what the screen shows there.
func (l *Wayland) CaptureWindow(ctx context.Context, w Window) (Shot, error) {
	if l.compositor() == "" {
		return Shot{}, ErrUnavailable
	}
	if w.W <= 0 || w.H <= 0 {
		return Shot{}, fmt.Errorf("the window %q has no size", w.Title)
	}
	g := geometry{w.X, w.Y, w.W, w.H}
	png, err := l.X.Output(ctx, "grim", "-t", "png", "-l", "1", "-g", g.arg(), "-")
	if err != nil {
		return Shot{}, err
	}
	img, err := decode(png)
	if err != nil {
		return Shot{}, err
	}
	geo := w.Geo
	if geo.LogicalW == 0 {
		geo = computeruse.Geometry{OriginX: w.X, OriginY: w.Y, LogicalW: w.W, LogicalH: w.H}
	}
	return Shot{Image: img, Title: w.Title, App: w.App, Geometry: physicalScale(geo, img)}, nil
}
