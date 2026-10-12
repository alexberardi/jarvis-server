package screen

import (
	"context"
	"encoding/json"
	"fmt"
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

func (l *Wayland) Capture(ctx context.Context, scope string) (Shot, error) {
	var (
		t   target
		err error
	)
	switch l.compositor() {
	case "hyprland":
		t, err = l.hyprland(ctx, scope)
	case "sway":
		t, err = l.sway(ctx, scope)
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

// swayNode is the slice of sway's tree a capture needs.
type swayNode struct {
	Type          string     `json:"type"`
	Name          string     `json:"name"`
	AppID         string     `json:"app_id"`
	Focused       bool       `json:"focused"`
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

func (l *Wayland) sway(ctx context.Context, scope string) (target, error) {
	b, err := l.X.Output(ctx, "swaymsg", "-t", "get_tree")
	if err != nil {
		return target{}, err
	}
	var root swayNode
	if err := json.Unmarshal(b, &root); err != nil {
		return target{}, fmt.Errorf("swaymsg get_tree: %w", err)
	}
	f, output := root.focused("")
	if f == nil {
		return target{}, nil
	}
	if scope == ScopeWindow && (f.Type == "con" || f.Type == "floating_con") && f.Rect.Width > 0 && f.Rect.Height > 0 {
		return target{region: &geometry{f.Rect.X, f.Rect.Y, f.Rect.Width, f.Rect.Height}, title: f.Name, app: f.AppID}, nil
	}
	return target{output: output}, nil
}
