package dictate

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// LinuxWindows focuses windows on Hyprland (hyprctl) and sway (swaymsg); on X11 it uses the
// host backend's window control (built-in EWMH, else wmctrl) and asks which window is active
// (EWMH's _NET_ACTIVE_WINDOW, else xdotool). Other Wayland compositors (GNOME, KDE) have no
// cross-client window control: naming an app is unavailable there, and typing goes to whatever
// has focus.
type LinuxWindows struct {
	X   Exec
	X11 Windows // X11 (HostWindows over the host backend); nil for none
	// XWM is the built-in EWMH client, which can say which window is active; nil: xdotool only.
	XWM interface {
		WMAvailable(ctx context.Context) (bool, string)
	}
}

func (l *LinuxWindows) compositor() string {
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

// Compositor names the supported Wayland compositor of this session ("hyprland", "sway"), or
// "" (X11, GNOME, KDE).
func (l *LinuxWindows) Compositor() string { return l.compositor() }

// FocusedWindow is the window with keyboard focus, as the compositor says: its app (Hyprland's
// class, sway's app_id or X11 class), title, process and layout geometry.
type FocusedWindow struct {
	Class, Title string
	PID          int
	X, Y, W, H   int
}

// FocusedWindow asks Hyprland or sway which window has focus; nil when none has (an empty
// workspace). ErrUnavailable on other desktops.
func (l *LinuxWindows) FocusedWindow(ctx context.Context) (*FocusedWindow, error) {
	switch l.compositor() {
	case "hyprland":
		out, err := l.X.Run(ctx, Cmd{Name: "hyprctl", Args: []string{"-j", "activewindow"}})
		if err != nil {
			return nil, err
		}
		var w struct {
			Address string `json:"address"`
			At      []int  `json:"at"`
			Size    []int  `json:"size"`
			Class   string `json:"class"`
			Title   string `json:"title"`
			PID     int    `json:"pid"`
		}
		if err := json.Unmarshal([]byte(out), &w); err != nil {
			return nil, fmt.Errorf("hyprctl activewindow: %w", err)
		}
		if w.Address == "" {
			return nil, nil
		}
		f := &FocusedWindow{Class: w.Class, Title: w.Title, PID: w.PID}
		if len(w.At) == 2 && len(w.Size) == 2 {
			f.X, f.Y, f.W, f.H = w.At[0], w.At[1], w.Size[0], w.Size[1]
		}
		return f, nil
	case "sway":
		root, err := l.swayTree(ctx)
		if err != nil {
			return nil, err
		}
		n := root.focusedLeaf()
		if n == nil {
			return nil, nil
		}
		class := n.AppID
		if class == "" && n.WindowProperties != nil {
			class = n.WindowProperties.Class
		}
		return &FocusedWindow{Class: class, Title: n.Name, PID: n.Pid, X: n.Rect.X, Y: n.Rect.Y, W: n.Rect.Width, H: n.Rect.Height}, nil
	}
	return nil, ErrUnavailable
}

// Cursor is the pointer's position in layout coordinates: Hyprland's `hyprctl cursorpos`. sway
// has no IPC for it (ok false), nor do other desktops.
func (l *LinuxWindows) Cursor(ctx context.Context) (x, y float64, ok bool) {
	if l.compositor() != "hyprland" {
		return 0, 0, false
	}
	out, err := l.X.Run(ctx, Cmd{Name: "hyprctl", Args: []string{"cursorpos"}})
	if err != nil {
		return 0, 0, false
	}
	xs, ys, found := strings.Cut(out, ",")
	if !found {
		return 0, 0, false
	}
	x, err1 := strconv.ParseFloat(strings.TrimSpace(xs), 64)
	y, err2 := strconv.ParseFloat(strings.TrimSpace(ys), 64)
	return x, y, err1 == nil && err2 == nil
}

func (l *LinuxWindows) Available(ctx context.Context) (bool, string) {
	if l.compositor() != "" {
		return true, ""
	}
	if l.X.Getenv("WAYLAND_DISPLAY") == "" && l.X.Getenv("DISPLAY") != "" && l.X11 != nil {
		native := false
		if l.XWM != nil {
			native, _ = l.XWM.WMAvailable(ctx)
		}
		if !native && !l.X.LookPath("xdotool") {
			return false, "switching to a named app on X11 needs an EWMH window manager (or xdotool and wmctrl)"
		}
		return l.X11.Available(ctx)
	}
	return false, "switching to a named app needs Hyprland, sway or X11; on this desktop click into the app and ask to type without naming it"
}

func (l *LinuxWindows) List(ctx context.Context) ([]Window, error) {
	switch l.compositor() {
	case "hyprland":
		return l.hyprList(ctx)
	case "sway":
		return l.swayList(ctx)
	}
	if l.X11 != nil && l.X.Getenv("WAYLAND_DISPLAY") == "" {
		return l.X11.List(ctx)
	}
	return nil, ErrUnavailable
}

var (
	reHyprAddr = regexp.MustCompile(`^0x[0-9a-fA-F]{1,16}$`)
	reConID    = regexp.MustCompile(`^[0-9]{1,12}$`)
)

func (l *LinuxWindows) Focus(ctx context.Context, id string) error {
	switch l.compositor() {
	case "hyprland":
		if !reHyprAddr.MatchString(id) {
			return fmt.Errorf("not a Hyprland window address: %q", id)
		}
		return l.hyprFocus(ctx, id)
	case "sway":
		if !reConID.MatchString(id) {
			return fmt.Errorf("not a sway container id: %q", id)
		}
		_, err := l.X.Run(ctx, Cmd{Name: "swaymsg", Args: []string{fmt.Sprintf("[con_id=%s]", id), "focus"}})
		return err
	}
	if l.X11 != nil && l.X.Getenv("WAYLAND_DISPLAY") == "" {
		return l.X11.Focus(ctx, id)
	}
	return ErrUnavailable
}

func (l *LinuxWindows) Active(ctx context.Context) (string, error) {
	switch l.compositor() {
	case "hyprland":
		out, err := l.X.Run(ctx, Cmd{Name: "hyprctl", Args: []string{"-j", "activewindow"}})
		if err != nil {
			return "", err
		}
		var w struct {
			Address string `json:"address"`
		}
		_ = json.Unmarshal([]byte(out), &w) // "{}" when nothing has focus
		return w.Address, nil
	case "sway":
		root, err := l.swayTree(ctx)
		if err != nil {
			return "", err
		}
		if f := root.focusedLeaf(); f != nil {
			return strconv.FormatInt(f.ID, 10), nil
		}
		return "", nil
	}
	if l.X11 != nil && l.X.Getenv("WAYLAND_DISPLAY") == "" {
		return l.X11.Active(ctx)
	}
	return "", ErrUnavailable
}

func (l *LinuxWindows) Same(id, active string) bool {
	if l.compositor() == "" && l.X11 != nil {
		return l.X11.Same(id, active)
	}
	return id != "" && strings.EqualFold(id, active)
}

// --- Hyprland ---

// hyprFocus focuses a window by address. A Hyprland configured in Lua (hyprland.lua, 0.55+)
// reads `hyprctl dispatch` as Lua (`hl.dispatch(...)`), where the classic `focuswindow
// address:…` is a syntax error (exit status 7); its dispatcher is hl.dsp.focus. The classic form
// is tried first, the Lua one when that fails.
func (l *LinuxWindows) hyprFocus(ctx context.Context, addr string) error {
	run := func(arg ...string) error {
		out, err := l.X.Run(ctx, Cmd{Name: "hyprctl", Args: append([]string{"dispatch"}, arg...)})
		if err != nil {
			return err
		}
		if out != "" && !strings.EqualFold(out, "ok") {
			return fmt.Errorf("hyprctl: %s", out)
		}
		return nil
	}
	err := run("focuswindow", "address:"+addr)
	if err == nil {
		return nil
	}
	if lerr := run(fmt.Sprintf(`hl.dsp.focus({ window = "address:%s" })`, addr)); lerr != nil {
		return err
	}
	return nil
}

// HyprX11Focused says, on Hyprland, whether the focused window is an XWayland one (false
// anywhere else, or when it can't tell).
func HyprX11Focused(x Exec) func(context.Context) bool {
	return func(ctx context.Context) bool {
		if x.Getenv("WAYLAND_DISPLAY") == "" || x.Getenv("HYPRLAND_INSTANCE_SIGNATURE") == "" || !x.LookPath("hyprctl") {
			return false
		}
		out, err := x.Run(ctx, Cmd{Name: "hyprctl", Args: []string{"-j", "activewindow"}})
		if err != nil {
			return false
		}
		var w struct {
			XWayland bool `json:"xwayland"`
		}
		return json.Unmarshal([]byte(out), &w) == nil && w.XWayland
	}
}

type hyprClient struct {
	Address        string           `json:"address"`
	Title          string           `json:"title"`
	Class          string           `json:"class"`
	InitialClass   string           `json:"initialClass"`
	Workspace      struct{ ID int } `json:"workspace"`
	Mapped         *bool            `json:"mapped"`
	Hidden         bool             `json:"hidden"`
	FocusHistoryID int              `json:"focusHistoryID"`
}

func (l *LinuxWindows) hyprList(ctx context.Context) ([]Window, error) {
	out, err := l.X.Run(ctx, Cmd{Name: "hyprctl", Args: []string{"-j", "clients"}})
	if err != nil {
		return nil, err
	}
	var clients []hyprClient
	if err := json.Unmarshal([]byte(out), &clients); err != nil {
		return nil, fmt.Errorf("hyprctl clients: %w", err)
	}
	shown := map[int]bool{}
	if out, err := l.X.Run(ctx, Cmd{Name: "hyprctl", Args: []string{"-j", "monitors"}}); err == nil {
		var mons []struct {
			ActiveWorkspace  struct{ ID int } `json:"activeWorkspace"`
			SpecialWorkspace struct{ ID int } `json:"specialWorkspace"`
		}
		if json.Unmarshal([]byte(out), &mons) == nil {
			for _, m := range mons {
				shown[m.ActiveWorkspace.ID] = true
				if m.SpecialWorkspace.ID != 0 {
					shown[m.SpecialWorkspace.ID] = true
				}
			}
		}
	}
	ws := make([]Window, 0, len(clients))
	for _, c := range clients {
		if !reHyprAddr.MatchString(c.Address) || (c.Mapped != nil && !*c.Mapped) {
			continue
		}
		ws = append(ws, Window{ID: c.Address, Apps: []string{c.Class, c.InitialClass}, Title: c.Title,
			Recency: c.FocusHistoryID, Visible: shown[c.Workspace.ID] && !c.Hidden})
	}
	return ws, nil
}

// --- sway ---

type swayNode struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	AppID   string `json:"app_id"`
	Pid     int    `json:"pid"`
	Focused bool   `json:"focused"`
	Rect    struct {
		X      int `json:"x"`
		Y      int `json:"y"`
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"rect"`
	Focus            []int64 `json:"focus"`
	Visible          *bool   `json:"visible"`
	CurrentWorkspace string  `json:"current_workspace"`
	WindowProperties *struct {
		Class    string `json:"class"`
		Instance string `json:"instance"`
	} `json:"window_properties"`
	Nodes         []swayNode `json:"nodes"`
	FloatingNodes []swayNode `json:"floating_nodes"`
}

func (n *swayNode) window() bool {
	return (n.Type == "con" || n.Type == "floating_con") && len(n.Nodes) == 0 && len(n.FloatingNodes) == 0 &&
		(n.AppID != "" || n.WindowProperties != nil || n.Pid > 0)
}

func (n *swayNode) focusedLeaf() *swayNode {
	if n.Focused {
		if n.window() {
			return n
		}
		return nil
	}
	for _, list := range [][]swayNode{n.Nodes, n.FloatingNodes} {
		for i := range list {
			if f := list[i].focusedLeaf(); f != nil {
				return f
			}
		}
	}
	return nil
}

type swayView struct {
	n       *swayNode
	visible bool
	path    []int
}

// views lists the windows with their focus path (positions in each ancestor's focus list: a
// smaller path was focused more recently) and whether they are on a shown workspace.
func (n *swayNode) views(path []int, shownWS bool, out *[]swayView) {
	kids := make([]*swayNode, 0, len(n.Nodes)+len(n.FloatingNodes))
	for i := range n.Nodes {
		kids = append(kids, &n.Nodes[i])
	}
	for i := range n.FloatingNodes {
		kids = append(kids, &n.FloatingNodes[i])
	}
	if len(kids) == 0 {
		if n.window() {
			*out = append(*out, swayView{n: n, path: path, visible: shownWS && (n.Visible == nil || *n.Visible)})
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

func (l *LinuxWindows) swayTree(ctx context.Context) (*swayNode, error) {
	out, err := l.X.Run(ctx, Cmd{Name: "swaymsg", Args: []string{"-t", "get_tree"}})
	if err != nil {
		return nil, err
	}
	var root swayNode
	if err := json.Unmarshal([]byte(out), &root); err != nil {
		return nil, fmt.Errorf("swaymsg get_tree: %w", err)
	}
	return &root, nil
}

func (l *LinuxWindows) swayList(ctx context.Context) ([]Window, error) {
	root, err := l.swayTree(ctx)
	if err != nil {
		return nil, err
	}
	var vs []swayView
	root.views(nil, false, &vs)
	sort.SliceStable(vs, func(a, b int) bool { return lessPath(vs[a].path, vs[b].path) })
	ws := make([]Window, len(vs))
	for i, v := range vs {
		apps := []string{v.n.AppID}
		if v.n.WindowProperties != nil {
			apps = append(apps, v.n.WindowProperties.Class, v.n.WindowProperties.Instance)
		}
		ws[i] = Window{ID: strconv.FormatInt(v.n.ID, 10), Apps: apps, Title: v.n.Name, Recency: i, Visible: v.visible}
	}
	return ws, nil
}
