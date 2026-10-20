package screen

import (
	"context"
	"testing"
)

func ids(ws []Window) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.ID
	}
	return out
}

// Hyprland's windows front to back: those on a shown workspace (or pinned), floating ones above
// the tiled, each by focus; a fullscreen window covers its workspace; another workspace's
// windows and a group's hidden tab aren't on screen.
func TestHyprlandWindows(t *testing.T) {
	f := hypr()
	f.out["hyprctl -j monitors"] = []byte(`[{"id":0,"name":"DP-1","x":0,"y":0,"width":2560,"height":1440,"scale":1.25,"focused":true,"activeWorkspace":{"id":1}},
		{"id":1,"name":"HDMI-A-1","x":2048,"y":0,"width":1920,"height":1080,"scale":1,"activeWorkspace":{"id":2}}]`)
	f.out["hyprctl -j clients"] = []byte(`[
		{"address":"0xa","at":[0,0],"size":[1000,1100],"title":"notes.txt","class":"foot","pid":10,"workspace":{"id":1},"monitor":0,"mapped":true,"focusHistoryID":0},
		{"address":"0xb","at":[1000,0],"size":[1000,1100],"title":"Jarvis Test Notes","class":"Notes","pid":11,"workspace":{"id":1},"monitor":0,"mapped":true,"focusHistoryID":2},
		{"address":"0xc","at":[300,300],"size":[400,300],"title":"Calculator","class":"calc","pid":12,"workspace":{"id":1},"monitor":0,"mapped":true,"floating":true,"focusHistoryID":3},
		{"address":"0xd","at":[2048,0],"size":[1920,1080],"title":"Docs","class":"firefox","pid":13,"workspace":{"id":2},"monitor":1,"mapped":true,"focusHistoryID":1},
		{"address":"0xe","at":[0,0],"size":[10,10],"title":"elsewhere","class":"x","pid":14,"workspace":{"id":5},"monitor":0,"mapped":true,"focusHistoryID":4},
		{"address":"0xf","at":[1000,0],"size":[1000,1100],"title":"tab","class":"y","pid":15,"workspace":{"id":1},"monitor":0,"mapped":true,"hidden":true,"focusHistoryID":5},
		{"address":"0x10","at":[50,50],"size":[200,100],"title":"pinned","class":"pip","pid":16,"workspace":{"id":7},"monitor":0,"mapped":true,"floating":true,"pinned":true,"focusHistoryID":6}
	]`)
	l := &Wayland{X: f}
	ws, err := l.Windows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(ws); len(got) != 5 || got[0] != "0xc" || got[1] != "0x10" || got[2] != "0xa" || got[3] != "0xd" || got[4] != "0xb" {
		t.Fatalf("order = %v", got)
	}
	a := ws[2]
	if a.App != "foot" || a.PID != 10 || a.Title != "notes.txt" || !a.Focused || a.X != 0 || a.W != 1000 || a.H != 1100 ||
		a.Geo.Scale != 1.25 || a.Geo.Monitor != "DP-1" {
		t.Errorf("focused window = %+v", a)
	}
	if ws[0].Focused || ws[3].Focused {
		t.Error("only the focused window is marked")
	}

	// A fullscreen window covers the rest of its workspace.
	f.out["hyprctl -j clients"] = []byte(`[
		{"address":"0xa","at":[0,0],"size":[1000,1100],"class":"foot","workspace":{"id":1},"monitor":0,"mapped":true,"focusHistoryID":1},
		{"address":"0xb","at":[0,0],"size":[2048,1152],"class":"mpv","workspace":{"id":1},"monitor":0,"mapped":true,"fullscreen":2,"focusHistoryID":0}
	]`)
	ws, err = l.Windows(context.Background())
	if err != nil || len(ws) != 1 || ws[0].ID != "0xb" {
		t.Fatalf("fullscreen: %v %v", ids(ws), err)
	}
}

// sway's windows front to back: shown workspaces only, floating above tiled, a fullscreen one
// above all; Xwayland windows go by their X11 class.
func TestSwayWindows(t *testing.T) {
	f := &fakeExec{
		env:   map[string]string{"WAYLAND_DISPLAY": "w", "SWAYSOCK": "/s"},
		paths: map[string]bool{"swaymsg": true, "grim": true},
		out: map[string][]byte{
			"swaymsg -t get_tree": []byte(`{"type":"root","nodes":[
				{"type":"output","name":"__i3","nodes":[{"type":"workspace","name":"__i3_scratch","floating_nodes":[{"id":9,"type":"floating_con","app_id":"hidden","pid":9,"rect":{"x":0,"y":0,"width":5,"height":5}}]}]},
				{"type":"output","name":"HEADLESS-1","scale":1,"current_workspace":"1","rect":{"x":0,"y":0,"width":1280,"height":800},"nodes":[
					{"type":"workspace","name":"1","focus":[4,3,2],"nodes":[
						{"id":2,"type":"con","name":"term","app_id":"foot","pid":20,"rect":{"x":0,"y":0,"width":640,"height":800}},
						{"id":3,"type":"con","name":"Jarvis Test Notes","pid":21,"focused":true,"window_properties":{"class":"Notes","instance":"notes"},"rect":{"x":640,"y":0,"width":640,"height":800}}],
					 "floating_nodes":[{"id":4,"type":"floating_con","name":"calc","app_id":"calc","pid":22,"rect":{"x":100,"y":100,"width":300,"height":200}}]},
					{"type":"workspace","name":"2","nodes":[{"id":5,"type":"con","name":"other","app_id":"x","pid":23,"rect":{"x":0,"y":0,"width":1280,"height":800}}]}]}]}`),
		},
	}
	l := &Wayland{X: f}
	ws, err := l.Windows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(ws); len(got) != 3 || got[0] != "4" || got[1] != "3" || got[2] != "2" {
		t.Fatalf("order = %v", got)
	}
	n := ws[1]
	if n.App != "Notes" || n.PID != 21 || !n.Focused || n.X != 640 || n.W != 640 || n.Geo.Monitor != "HEADLESS-1" || n.Geo.Scale != 1 {
		t.Errorf("notes = %+v", n)
	}

	// A fullscreen window covers its output.
	f.out["swaymsg -t get_tree"] = []byte(`{"type":"root","nodes":[{"type":"output","name":"HEADLESS-1","current_workspace":"1","nodes":[
		{"type":"workspace","name":"1","nodes":[
			{"id":2,"type":"con","app_id":"foot","rect":{"x":0,"y":0,"width":640,"height":800}},
			{"id":3,"type":"con","app_id":"mpv","fullscreen_mode":1,"rect":{"x":0,"y":0,"width":1280,"height":800}}]}]}]}`)
	if ws, err = l.Windows(context.Background()); err != nil || len(ws) != 1 || ws[0].ID != "3" {
		t.Fatalf("fullscreen: %v %v", ids(ws), err)
	}
}

// CaptureWindow grabs the window's area of the layout.
func TestWaylandCaptureWindow(t *testing.T) {
	f := hypr()
	l := &Wayland{X: f}
	shot, err := l.CaptureWindow(context.Background(), Window{ID: "0xb", App: "Notes", Title: "Jarvis Test Notes",
		X: 1000, Y: 0, W: 1000, H: 1100})
	if err != nil {
		t.Fatal(err)
	}
	if g := lastGrim(t, f); g != "-t png -l 1 -g 1000,0 1000x1100 -" {
		t.Errorf("grim args = %q", g)
	}
	if shot.Title != "Jarvis Test Notes" || shot.App != "Notes" || shot.Geometry.OriginX != 1000 || shot.Geometry.LogicalW != 1000 {
		t.Errorf("shot = %q %q %+v", shot.Title, shot.App, shot.Geometry)
	}
}
