package screen

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// fakeExec scripts commands: outputs by the command line's first words.
type fakeExec struct {
	env   map[string]string
	paths map[string]bool
	out   map[string][]byte // key: "name arg0" (or "name")
	calls [][]string
	// onRun, when set, runs for each call (the macOS fake writes screencapture's file).
	onRun func(name string, args []string) error
}

func (f *fakeExec) LookPath(n string) bool { return f.paths[n] }
func (f *fakeExec) Getenv(k string) string { return f.env[k] }
func (f *fakeExec) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.onRun != nil {
		if err := f.onRun(name, args); err != nil {
			return nil, err
		}
	}
	key := name
	if len(args) > 0 {
		key += " " + args[0]
		if len(args) > 1 && name != "grim" {
			key += " " + args[1]
		}
	}
	if b, ok := f.out[key]; ok {
		return b, nil
	}
	if b, ok := f.out[name]; ok {
		return b, nil
	}
	return nil, errors.New(name + ": unexpected call")
}

func pngOf(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func hypr() *fakeExec {
	return &fakeExec{
		env:   map[string]string{"WAYLAND_DISPLAY": "wayland-1", "HYPRLAND_INSTANCE_SIGNATURE": "x"},
		paths: map[string]bool{"hyprctl": true, "grim": true},
		out: map[string][]byte{
			"hyprctl -j activewindow": []byte(`{"at":[9,33],"size":[1071,1308],"title":"build.log","class":"foot"}`),
			"hyprctl -j monitors":     []byte(`[{"name":"DP-1","focused":false},{"name":"HDMI-A-1","focused":true}]`),
			"grim":                    pngOf(40, 30),
		},
	}
}

func lastGrim(t *testing.T, f *fakeExec) string {
	t.Helper()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i][0] == "grim" {
			return strings.Join(f.calls[i][1:], " ")
		}
	}
	t.Fatal("grim never ran")
	return ""
}

func TestWaylandAvailability(t *testing.T) {
	for _, c := range []struct {
		name string
		env  map[string]string
		path map[string]bool
		ok   bool
	}{
		{"hyprland", map[string]string{"WAYLAND_DISPLAY": "w", "HYPRLAND_INSTANCE_SIGNATURE": "x"}, map[string]bool{"hyprctl": true, "grim": true}, true},
		{"sway", map[string]string{"WAYLAND_DISPLAY": "w", "SWAYSOCK": "/s"}, map[string]bool{"swaymsg": true, "grim": true}, true},
		{"no grim", map[string]string{"WAYLAND_DISPLAY": "w", "HYPRLAND_INSTANCE_SIGNATURE": "x"}, map[string]bool{"hyprctl": true}, false},
		{"gnome", map[string]string{"WAYLAND_DISPLAY": "w", "XDG_CURRENT_DESKTOP": "GNOME"}, map[string]bool{"grim": true}, false},
		{"x11", map[string]string{"DISPLAY": ":0"}, map[string]bool{"grim": true}, false},
		{"headless", nil, map[string]bool{"grim": true, "hyprctl": true}, false},
	} {
		l := &Wayland{X: &fakeExec{env: c.env, paths: c.path}}
		if ok, why := l.Available(context.Background()); ok != c.ok || (!ok && why == "") {
			t.Errorf("%s: Available = %v %q, want %v (with a reason when false)", c.name, ok, why, c.ok)
		}
	}
}

func TestHyprlandCapturesTheFocusedWindowOrMonitor(t *testing.T) {
	f := hypr()
	l := &Wayland{X: f}
	shot, err := l.Capture(context.Background(), ScopeWindow)
	if err != nil {
		t.Fatal(err)
	}
	if g := lastGrim(t, f); g != "-t png -l 1 -g 9,33 1071x1308 -" {
		t.Errorf("window grim args = %q", g)
	}
	if shot.Title != "build.log" || shot.App != "foot" || shot.Image.Bounds().Dx() != 40 {
		t.Errorf("shot = %q %q %v", shot.Title, shot.App, shot.Image.Bounds())
	}
	if _, err := l.Capture(context.Background(), ScopeScreen); err != nil {
		t.Fatal(err)
	}
	if g := lastGrim(t, f); g != "-t png -l 1 -o HDMI-A-1 -" {
		t.Errorf("screen grim args = %q", g)
	}
	// An empty workspace has no active window: the focused monitor instead.
	f.out["hyprctl -j activewindow"] = []byte(`{}`)
	shot, err = l.Capture(context.Background(), ScopeWindow)
	if err != nil {
		t.Fatal(err)
	}
	if g := lastGrim(t, f); g != "-t png -l 1 -o HDMI-A-1 -" || shot.Title != "" {
		t.Errorf("no window: grim args = %q, title %q", g, shot.Title)
	}
}

func TestSwayCapturesTheFocusedContainer(t *testing.T) {
	f := &fakeExec{
		env:   map[string]string{"WAYLAND_DISPLAY": "w", "SWAYSOCK": "/s"},
		paths: map[string]bool{"swaymsg": true, "grim": true},
		out: map[string][]byte{
			"swaymsg -t get_tree": []byte(`{"type":"root","nodes":[{"type":"output","name":"eDP-1","nodes":[
				{"type":"workspace","name":"1","nodes":[{"type":"con","name":"vim","app_id":"foot","focused":true,
				"rect":{"x":10,"y":20,"width":300,"height":200}}]}]}]}`),
			"grim": pngOf(8, 8),
		},
	}
	l := &Wayland{X: f}
	shot, err := l.Capture(context.Background(), ScopeWindow)
	if err != nil {
		t.Fatal(err)
	}
	if g := lastGrim(t, f); g != "-t png -l 1 -g 10,20 300x200 -" || shot.Title != "vim" || shot.App != "foot" {
		t.Errorf("window: grim %q, shot %q %q", g, shot.Title, shot.App)
	}
	if _, err := l.Capture(context.Background(), ScopeScreen); err != nil {
		t.Fatal(err)
	}
	if g := lastGrim(t, f); g != "-t png -l 1 -o eDP-1 -" {
		t.Errorf("screen grim args = %q", g)
	}
}

// TestMacCaptureLeavesNoFile: screencapture's file lives in a private directory that is gone
// when Capture returns; the front window goes by its CGWindowID.
func TestMacCaptureLeavesNoFile(t *testing.T) {
	var file string
	f := &fakeExec{out: map[string][]byte{screencapture: nil}, onRun: func(name string, args []string) error {
		file = args[len(args)-1]
		st, err := os.Stat(file[:strings.LastIndex(file, "/")])
		if err != nil || st.Mode().Perm() != 0o700 {
			t.Errorf("capture dir not private: %v %v", st.Mode(), err)
		}
		return os.WriteFile(file, pngOf(20, 10), 0o600)
	}}
	m := &Mac{X: f, Trusted: func() bool { return true },
		Front: func() (FrontWindow, error) { return FrontWindow{ID: 4242, Title: "Report", App: "Preview"}, nil }}
	shot, err := m.Capture(context.Background(), ScopeWindow)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls[0][1:len(f.calls[0])-1], " "); got != "-x -t png -o -l 4242" {
		t.Errorf("window args = %q", got)
	}
	if shot.Title != "Report" || shot.App != "Preview" {
		t.Errorf("shot = %+v", shot)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("capture file left behind: %v", err)
	}
	if _, err := m.Capture(context.Background(), ScopeScreen); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls[1][1:len(f.calls[1])-1], " "); got != "-x -t png -m" {
		t.Errorf("screen args = %q", got)
	}
	m.Trusted = func() bool { return false }
	if ok, why := m.Available(context.Background()); ok || !strings.Contains(why, "Screen") {
		t.Errorf("untrusted: Available = %v %q", ok, why)
	}
}

func TestEncodeScalesAndCaps(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4800, 1350))
	data, w, h, err := Encode(img)
	if err != nil {
		t.Fatal(err)
	}
	if w != MaxSide || h != 1350*MaxSide/4800 {
		t.Errorf("scaled to %dx%d", w, h)
	}
	got, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil || got.Bounds().Dx() != w || got.Bounds().Dy() != h || len(data) > MaxBytes {
		t.Errorf("jpeg %v %v, %d bytes", got.Bounds(), err, len(data))
	}
	small := image.NewRGBA(image.Rect(0, 0, 300, 200))
	if _, w, h, _ := Encode(small); w != 300 || h != 200 {
		t.Errorf("small image resized to %dx%d", w, h)
	}
}

// fakeCap is a Capturer for the tool tests.
type fakeCap struct {
	ok    bool
	shot  Shot
	err   error
	scope string
}

func (f *fakeCap) Available(context.Context) (bool, string) { return f.ok, "no capture here" }
func (f *fakeCap) Capture(_ context.Context, scope string) (Shot, error) {
	f.scope = scope
	return f.shot, f.err
}

func TestToolAttachesTheCapture(t *testing.T) {
	fc := &fakeCap{ok: true, shot: Shot{Image: image.NewRGBA(image.Rect(0, 0, 64, 48)), Title: "Error", App: "firefox"}}
	var order []string
	tl := &Tools{C: fc, Hide: func() func() {
		order = append(order, "hide")
		return func() { order = append(order, "show") }
	}}
	if defs := tl.Available(context.Background()); len(defs) != 1 || defs[0].Name != ToolRead {
		t.Fatalf("offered %v", defs)
	}
	out, ok := tl.Call(context.Background(), ToolRead, map[string]any{}, node.Request{})
	if !ok || out["success"] != true || fc.scope != ScopeWindow {
		t.Fatalf("call: %v %v scope %q", ok, out, fc.scope)
	}
	if strings.Join(order, ",") != "hide,show" {
		t.Errorf("hooks ran %v", order)
	}
	atts, _ := out[AttachmentsKey].([]any)
	if len(atts) != 1 {
		t.Fatalf("attachments = %v", out[AttachmentsKey])
	}
	a := atts[0].(map[string]any)
	raw, err := base64.StdEncoding.DecodeString(a["data"].(string))
	if err != nil || a["mime"] != "image/jpeg" || !bytes.HasPrefix(raw, []byte{0xFF, 0xD8, 0xFF}) {
		t.Errorf("attachment mime %v, err %v", a["mime"], err)
	}
	if a["label"] != `screenshot of the window "Error"` || out["window_title"] != "Error" || out["app"] != "firefox" {
		t.Errorf("label %v, title %v, app %v", a["label"], out["window_title"], out["app"])
	}
	if _, ok := tl.Call(context.Background(), ToolRead, map[string]any{"scope": "SCREEN"}, node.Request{}); !ok || fc.scope != ScopeScreen {
		t.Errorf("scope screen not passed: %q", fc.scope)
	}
	if _, ok := tl.Call(context.Background(), "desktop_open", nil, node.Request{}); ok {
		t.Error("handled another tool's name")
	}
}

func TestToolUnavailableOrFailing(t *testing.T) {
	fc := &fakeCap{ok: false}
	tl := &Tools{C: fc}
	if defs := tl.Available(context.Background()); len(defs) != 0 {
		t.Errorf("offered while unavailable: %v", defs)
	}
	out, _ := tl.Call(context.Background(), ToolRead, nil, node.Request{})
	if out["success"] != false || out["error_code"] != "not_supported" {
		t.Errorf("unavailable call = %v", out)
	}
	fc.ok, fc.err = true, errors.New("grim: compositor doesn't support screencopy")
	out, _ = tl.Call(context.Background(), ToolRead, nil, node.Request{})
	if out["success"] != false || out["error_code"] != "capture_failed" || !strings.Contains(out["error"].(string), "screencopy") {
		t.Errorf("failed capture = %v", out)
	}
	if defs := (&Tools{}).Available(context.Background()); len(defs) != 0 {
		t.Error("a nil capturer offered the tool")
	}
}
