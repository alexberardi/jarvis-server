//go:build linux

package computer

import (
	"context"
	"errors"
	"image"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/alexberardi/jarvis-server/internal/agent/screen"
	"github.com/alexberardi/jarvis-server/internal/agent/x11"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// fakeX11Desk is an X11 session: its clients top first, and what Capture grabbed.
type fakeX11Desk struct {
	clients  []x11.Client
	captured []uint32
}

func (f *fakeX11Desk) Clients(context.Context) ([]x11.Client, error) { return f.clients, nil }
func (f *fakeX11Desk) Capture(_ context.Context, win uint32) (x11.Capture, error) {
	f.captured = append(f.captured, win)
	for _, c := range f.clients {
		if c.ID == win {
			return x11.Capture{Image: image.NewRGBA(image.Rect(0, 0, c.W, c.H)), X: c.X, Y: c.Y, W: c.W, H: c.H}, nil
		}
	}
	return x11.Capture{}, errors.New("BadWindow")
}

// fakeExec is the shell the Wayland side runs hyprctl, swaymsg and grim in.
type fakeExec struct {
	env   map[string]string
	paths map[string]bool
	out   map[string]string // "name arg0 arg1"
}

func (f *fakeExec) LookPath(n string) bool { return f.paths[n] }
func (f *fakeExec) Getenv(k string) string { return f.env[k] }
func (f *fakeExec) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args[:min(2, len(args))]...), " ")
	if b, ok := f.out[key]; ok {
		return []byte(b), nil
	}
	return nil, errors.New(key + ": unexpected call")
}

var (
	xNotes = x11.Client{Owner: x11.Owner{ID: 0x600001, PID: 77, Class: "Notes"}, Title: "Jarvis Test Notes", X: 10, Y: 20, W: 800, H: 600}
	xTerm  = x11.Client{Owner: x11.Owner{ID: 0x600002, PID: 78, Class: "XTerm"}, Title: "xterm", X: 500, Y: 400, W: 300, H: 200, Active: true}
)

func x11Session(x *fakeX11Desk) *linuxWindows {
	env := map[string]string{"DISPLAY": ":90"}
	return &linuxWindows{getenv: func(k string) string { return env[k] }, wayland: &screen.Wayland{X: &fakeExec{env: env}}, x11: x}
}

func TestLinuxWindowsX11(t *testing.T) {
	x := &fakeX11Desk{clients: []x11.Client{xTerm, xNotes}}
	l := x11Session(x)
	ws, err := l.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []DesktopWindow{
		{ID: "0x00600002", App: "XTerm", PID: 78, Title: "xterm", X: 500, Y: 400, W: 300, H: 200, Focused: true},
		{ID: "0x00600001", App: "Notes", PID: 77, Title: "Jarvis Test Notes", X: 10, Y: 20, W: 800, H: 600},
	}
	if len(ws) != 2 || ws[0] != want[0] || ws[1] != want[1] {
		t.Fatalf("windows = %+v", ws)
	}
	shot, err := l.capture(context.Background(), ws[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(x.captured) != 1 || x.captured[0] != 0x600001 || shot.App != "Notes" || shot.Title != "Jarvis Test Notes" ||
		shot.Geometry != (computeruse.Geometry{OriginX: 10, OriginY: 20, LogicalW: 800, LogicalH: 600, Scale: 1}) {
		t.Errorf("capture: %v %q %q %+v", x.captured, shot.App, shot.Title, shot.Geometry)
	}
}

// Hyprland's windows come from the compositor, its capture from grim; without grim the task
// says what to install. GNOME (no compositor IPC) and no session at all are refused by name.
func TestLinuxWindowsWayland(t *testing.T) {
	env := map[string]string{"WAYLAND_DISPLAY": "wayland-1", "HYPRLAND_INSTANCE_SIGNATURE": "x"}
	ex := &fakeExec{env: env, paths: map[string]bool{"hyprctl": true}, out: map[string]string{
		"hyprctl -j monitors": `[{"id":0,"name":"DP-1","width":1920,"height":1080,"scale":1,"activeWorkspace":{"id":1}}]`,
		"hyprctl -j clients": `[{"address":"0xa","at":[0,0],"size":[960,1080],"class":"Notes","title":"Jarvis Test Notes","pid":5,"workspace":{"id":1},"focusHistoryID":0},
			{"address":"0xb","at":[960,0],"size":[960,1080],"class":"kitty","title":"zsh","pid":6,"workspace":{"id":1},"focusHistoryID":1}]`,
	}}
	l := &linuxWindows{getenv: func(k string) string { return env[k] }, wayland: &screen.Wayland{X: ex}, x11: &fakeX11Desk{}}
	ws, err := l.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 || ws[0] != (DesktopWindow{ID: "0xa", App: "Notes", PID: 5, Title: "Jarvis Test Notes", W: 960, H: 1080, Focused: true}) || ws[1].App != "kitty" {
		t.Fatalf("windows = %+v", ws)
	}
	if _, err := l.capture(context.Background(), ws[0]); err == nil || !strings.Contains(err.Error(), "grim") {
		t.Errorf("capture without grim = %v, want what to install", err)
	}

	env["HYPRLAND_INSTANCE_SIGNATURE"], env["XDG_CURRENT_DESKTOP"] = "", "GNOME"
	if _, err := l.List(context.Background()); !errors.Is(err, errNoDesktop) {
		t.Errorf("GNOME: %v", err)
	}
	delete(env, "WAYLAND_DISPLAY")
	if _, err := l.List(context.Background()); !errors.Is(err, errNoDesktop) {
		t.Errorf("no session: %v", err)
	}
}

// The element under a point is AT-SPI's in the window there (by its process), named by the
// window's app; the focused one is in the focused window. On Wayland AT-SPI's coordinates
// aren't the screen's, so there is no hit test.
func TestLinuxAX(t *testing.T) {
	x := &fakeX11Desk{clients: []x11.Client{xTerm, xNotes}}
	l := x11Session(x)
	var asked []int
	a := &linuxAX{windows: l, getenv: l.getenv,
		elementAt: func(pid, px, py int) (*computeruse.Element, error) {
			asked = append(asked, pid, px, py)
			return &computeruse.Element{Role: "textbox", Name: "Body"}, nil
		},
		focused: func(pid int) (*computeruse.Element, error) {
			asked = append(asked, pid)
			return &computeruse.Element{Role: "terminal"}, nil
		}}
	r, err := a.At(20, 30)
	if err != nil || r == nil || r.App != "Notes" || r.PID != 77 || r.Role != "textbox" || len(asked) != 3 || asked[1] != 20 {
		t.Fatalf("At = %+v %v (asked %v)", r, err, asked)
	}
	if r, _ := a.At(1200, 900); r != nil {
		t.Errorf("At on the desktop = %+v", r)
	}
	r, err = a.Focused()
	if err != nil || r == nil || r.App != "XTerm" || r.PID != 78 {
		t.Errorf("Focused = %+v %v", r, err)
	}

	env := map[string]string{"WAYLAND_DISPLAY": "wayland-1"}
	a.getenv = func(k string) string { return env[k] }
	asked = nil
	if r, _ := a.At(20, 30); r != nil || len(asked) != 0 {
		t.Errorf("Wayland At = %+v (asked %v)", r, asked)
	}
}

type fakeLogind struct {
	session dbus.ObjectPath
	noSess  error
	locked  bool
	hintErr error
}

func (f fakeLogind) Session(context.Context) (dbus.ObjectPath, error) { return f.session, f.noSess }
func (f fakeLogind) LockedHint(context.Context, dbus.ObjectPath) (bool, error) {
	return f.locked, f.hintErr
}

// The lock state is the session's LockedHint; with no logind session to ask (a headless
// session) nothing is locked, and a session that can't be read refuses.
func TestLogindLocked(t *testing.T) {
	ctx := context.Background()
	if l, err := logindLocked(ctx, fakeLogind{session: "/s/2", locked: true}); !l || err != nil {
		t.Errorf("locked: %v %v", l, err)
	}
	if l, err := logindLocked(ctx, fakeLogind{session: "/s/2"}); l || err != nil {
		t.Errorf("unlocked: %v %v", l, err)
	}
	if l, err := logindLocked(ctx, fakeLogind{noSess: errors.New("NoSessionForPID")}); l || err != nil {
		t.Errorf("no session: %v %v", l, err)
	}
	if _, err := logindLocked(ctx, fakeLogind{session: "/s/2", hintErr: errors.New("access denied")}); err == nil {
		t.Error("an unreadable LockedHint must be an error")
	}
}
