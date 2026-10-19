package screen

import (
	"context"
	"errors"
	"image"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/x11"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// fakeX11 is an X11 desktop: a 1920x1080 root, windows at root positions, the one active.
type fakeX11 struct {
	session bool
	active  uint32
	windows []x11.Window
	stack   []uint32
	rects   map[uint32]image.Rectangle
	asked   []uint32
}

func (f *fakeX11) Session() (bool, string) {
	if !f.session {
		return false, "no X11 display (DISPLAY is unset)"
	}
	return true, ""
}
func (f *fakeX11) Active(context.Context) (uint32, error)        { return f.active, nil }
func (f *fakeX11) Windows(context.Context) ([]x11.Window, error) { return f.windows, nil }
func (f *fakeX11) Stacking(context.Context) ([]uint32, error)    { return f.stack, nil }
func (f *fakeX11) Capture(_ context.Context, win uint32) (x11.Capture, error) {
	f.asked = append(f.asked, win)
	r, ok := f.rects[win]
	if !ok {
		return x11.Capture{}, errors.New("BadWindow")
	}
	return x11.Capture{Image: image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy())), X: r.Min.X, Y: r.Min.Y, W: r.Dx(), H: r.Dy()}, nil
}

func x11Desktop() *fakeX11 {
	return &fakeX11{session: true, active: 0x400,
		windows: []x11.Window{{ID: 0x400, Title: "notes", Class: "notes.Notes"}, {ID: 0x500, Title: "Docs", Class: "Navigator.firefox"},
			{ID: 0x600, Title: "hidden", Class: "xterm.XTerm", Minimized: true}},
		stack: []uint32{0x400, 0x500, 0x600},
		rects: map[uint32]image.Rectangle{0: image.Rect(0, 0, 1920, 1080), 0x400: image.Rect(1210, 340, 1850, 820), 0x500: image.Rect(0, 0, 800, 600)}}
}

func TestX11Geometry(t *testing.T) {
	f := x11Desktop()
	c := &X11{D: f}
	shot, err := c.Capture(context.Background(), ScopeWindow, "")
	if err != nil {
		t.Fatal(err)
	}
	if shot.Title != "notes" || shot.App != "Notes" {
		t.Errorf("shot = %q %q", shot.Title, shot.App)
	}
	wantGeo(t, "active window", shot.Geometry, computeruse.Geometry{OriginX: 1210, OriginY: 340, LogicalW: 640, LogicalH: 480, Scale: 1})
	// The model's click on the encoded image lands on the window's spot on the screen.
	if _, err := EncodeShot(&shot); err != nil {
		t.Fatal(err)
	}
	if x, y := shot.Geometry.ToGlobal(320, 240); x != 1530 || y != 580 {
		t.Errorf("ToGlobal(320, 240) = %d,%d, want 1530,580", x, y)
	}

	if shot, err = c.Capture(context.Background(), ScopeScreen, ""); err != nil {
		t.Fatal(err)
	}
	wantGeo(t, "screen", shot.Geometry, computeruse.Geometry{LogicalW: 1920, LogicalH: 1080, Scale: 1})
	if shot.Title != "" || shot.App != "" {
		t.Errorf("screen shot named %q %q", shot.Title, shot.App)
	}

	// No active window: the screen.
	f.active = 0
	if shot, err = c.Capture(context.Background(), ScopeWindow, ""); err != nil {
		t.Fatal(err)
	}
	wantGeo(t, "no active window", shot.Geometry, computeruse.Geometry{LogicalW: 1920, LogicalH: 1080, Scale: 1})
}

func TestX11NamedWindow(t *testing.T) {
	f := x11Desktop()
	c := &X11{D: f}
	shot, err := c.Capture(context.Background(), ScopeScreen, "firefox")
	if err != nil {
		t.Fatal(err)
	}
	if shot.Title != "Docs" || shot.App != "firefox" || f.asked[len(f.asked)-1] != 0x500 {
		t.Errorf("named shot = %q %q (asked %v)", shot.Title, shot.App, f.asked)
	}
	var we *WindowError
	if _, err := c.Capture(context.Background(), ScopeWindow, "xterm"); !errors.As(err, &we) || we.Code != CodeWindowNotVisible {
		t.Errorf("minimised window: %v", err)
	}
	if _, err := c.Capture(context.Background(), ScopeWindow, "spotify"); !errors.As(err, &we) || we.Code != CodeWindowNotFound {
		t.Errorf("missing window: %v", err)
	}
}

func TestLinuxPicksTheSessionsCapturer(t *testing.T) {
	wl := map[string]string{"WAYLAND_DISPLAY": "w", "HYPRLAND_INSTANCE_SIGNATURE": "x"}
	for _, c := range []struct {
		name    string
		env     map[string]string
		x11     bool
		want    string // "wayland", "x11" or "" (unavailable)
		because string
	}{
		{"hyprland", wl, false, "wayland", ""},
		{"x11", map[string]string{"DISPLAY": ":0"}, true, "x11", ""},
		{"x11 unreachable", map[string]string{"DISPLAY": ":0"}, false, "", "X11 session"},
		{"gnome wayland", map[string]string{"WAYLAND_DISPLAY": "w", "DISPLAY": ":0", "XDG_CURRENT_DESKTOP": "GNOME"}, false, "", "Hyprland, sway"},
		{"headless", nil, false, "", "Hyprland, sway"},
	} {
		w := &Wayland{X: &fakeExec{env: c.env, paths: map[string]bool{"hyprctl": true, "grim": true}}}
		x := &X11{D: &fakeX11{session: c.x11}}
		l := &Linux{Wayland: w, X11: x}
		got, why := l.pick(context.Background())
		name := ""
		switch got {
		case w:
			name = "wayland"
		case x:
			name = "x11"
		}
		if name != c.want || (c.want == "" && !strings.Contains(why, c.because)) {
			t.Errorf("%s: picked %q (%q), want %q (%q)", c.name, name, why, c.want, c.because)
		}
		if ok, _ := l.Available(context.Background()); ok != (c.want != "") {
			t.Errorf("%s: Available = %v", c.name, ok)
		}
	}
	if _, err := (&Linux{Wayland: &Wayland{X: &fakeExec{}}, X11: &X11{D: &fakeX11{}}}).Capture(context.Background(), ScopeScreen, ""); !errors.Is(err, ErrUnavailable) {
		t.Errorf("headless Capture = %v", err)
	}
}
