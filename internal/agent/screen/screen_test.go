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
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
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
	shot, err := l.Capture(context.Background(), ScopeWindow, "")
	if err != nil {
		t.Fatal(err)
	}
	if g := lastGrim(t, f); g != "-t png -l 1 -g 9,33 1071x1308 -" {
		t.Errorf("window grim args = %q", g)
	}
	if shot.Title != "build.log" || shot.App != "foot" || shot.Image.Bounds().Dx() != 40 {
		t.Errorf("shot = %q %q %v", shot.Title, shot.App, shot.Image.Bounds())
	}
	if _, err := l.Capture(context.Background(), ScopeScreen, ""); err != nil {
		t.Fatal(err)
	}
	if g := lastGrim(t, f); g != "-t png -l 1 -o HDMI-A-1 -" {
		t.Errorf("screen grim args = %q", g)
	}
	// An empty workspace has no active window: the focused monitor instead.
	f.out["hyprctl -j activewindow"] = []byte(`{}`)
	shot, err = l.Capture(context.Background(), ScopeWindow, "")
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
	shot, err := l.Capture(context.Background(), ScopeWindow, "")
	if err != nil {
		t.Fatal(err)
	}
	if g := lastGrim(t, f); g != "-t png -l 1 -g 10,20 300x200 -" || shot.Title != "vim" || shot.App != "foot" {
		t.Errorf("window: grim %q, shot %q %q", g, shot.Title, shot.App)
	}
	if _, err := l.Capture(context.Background(), ScopeScreen, ""); err != nil {
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
		st, err := os.Stat(filepath.Dir(file))
		if err != nil {
			t.Errorf("capture dir: %v", err)
		} else if runtime.GOOS != "windows" && st.Mode().Perm() != 0o700 { // Windows has no Unix modes
			t.Errorf("capture dir not private: %v", st.Mode())
		}
		return os.WriteFile(file, pngOf(20, 10), 0o600)
	}}
	m := &Mac{X: f, Trusted: func() bool { return true },
		Windows: func() ([]MacWindow, error) {
			return []MacWindow{{ID: 7, App: "Window Server", Layer: 25, W: 3000, H: 30, OnScreen: true},
				{ID: 4242, Title: "Report", App: "Preview", W: 800, H: 600, OnScreen: true}}, nil
		}}
	shot, err := m.Capture(context.Background(), ScopeWindow, "")
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
	if _, err := m.Capture(context.Background(), ScopeScreen, ""); err != nil {
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
	ok     bool
	shot   Shot
	err    error
	scope  string
	window string
}

func (f *fakeCap) Available(context.Context) (bool, string) { return f.ok, "no capture here" }
func (f *fakeCap) Capture(_ context.Context, scope, window string) (Shot, error) {
	f.scope, f.window = scope, window
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
	if _, has := out["capture"]; has {
		t.Errorf("capture block without a geometry: %v", out["capture"])
	}
	fc.shot.Geometry = computeruse.Geometry{OriginX: 1920, OriginY: 30, LogicalW: 32, LogicalH: 24, Scale: 2, Monitor: "DP-2"}
	out, _ = tl.Call(context.Background(), ToolRead, map[string]any{}, node.Request{})
	want := map[string]any{"origin_x": 1920, "origin_y": 30, "logical_w": 32, "logical_h": 24, "scale": 2.0, "monitor": "DP-2"}
	if got, _ := out["capture"].(map[string]any); !reflect.DeepEqual(got, want) || out["width"] != 64 || out["height"] != 48 {
		t.Errorf("capture = %v (want %v), size %vx%v", out["capture"], want, out["width"], out["height"])
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

func wantGeo(t *testing.T, what string, got, want computeruse.Geometry) {
	t.Helper()
	if got != want {
		t.Errorf("%s: geometry = %+v, want %+v", what, got, want)
	}
}

// TestGeometryHyprlandWindow: a window's at/size are layout coordinates; its monitor gives the
// scale and name. A monitor's logical size is its mode over its scale (turned by a transform).
func TestGeometryHyprlandWindow(t *testing.T) {
	f := hypr()
	f.out["hyprctl -j activewindow"] = []byte(`{"at":[1930,40],"size":[800,600],"title":"Docs","class":"firefox","monitor":1}`)
	f.out["hyprctl -j monitors"] = []byte(`[
		{"id":0,"name":"DP-1","x":0,"y":0,"width":1920,"height":1080,"scale":1,"transform":0,"focused":false,"activeWorkspace":{"id":1}},
		{"id":1,"name":"HDMI-A-1","x":1920,"y":0,"width":3840,"height":2160,"scale":1.5,"transform":0,"focused":true,"activeWorkspace":{"id":2}}]`)
	f.out["hyprctl -j clients"] = []byte(`[{"at":[10,20],"size":[640,480],"title":"notes","class":"foot","workspace":{"id":1},"monitor":0}]`)
	f.out["grim"] = pngOf(120, 90)
	l := &Wayland{X: f}
	shot, err := l.Capture(context.Background(), ScopeWindow, "")
	if err != nil {
		t.Fatal(err)
	}
	wantGeo(t, "window", shot.Geometry, computeruse.Geometry{OriginX: 1930, OriginY: 40, LogicalW: 800, LogicalH: 600, Scale: 1.5, Monitor: "HDMI-A-1"})
	if shot, err = l.Capture(context.Background(), ScopeScreen, ""); err != nil {
		t.Fatal(err)
	}
	wantGeo(t, "monitor", shot.Geometry, computeruse.Geometry{OriginX: 1920, OriginY: 0, LogicalW: 2560, LogicalH: 1440, Scale: 1.5, Monitor: "HDMI-A-1"})
	if shot, err = l.Capture(context.Background(), ScopeWindow, "foot"); err != nil {
		t.Fatal(err)
	}
	wantGeo(t, "named", shot.Geometry, computeruse.Geometry{OriginX: 10, OriginY: 20, LogicalW: 640, LogicalH: 480, Scale: 1, Monitor: "DP-1"})

	// No focused monitor: every output, the box around them at the highest scale. DP-1 is
	// turned a quarter (transform 1): 1080 wide, 1920 high.
	f.out["hyprctl -j monitors"] = []byte(`[
		{"id":0,"name":"DP-1","x":0,"y":0,"width":1920,"height":1080,"scale":1,"transform":1},
		{"id":1,"name":"HDMI-A-1","x":1080,"y":0,"width":3840,"height":2160,"scale":2,"transform":0}]`)
	if shot, err = l.Capture(context.Background(), ScopeScreen, ""); err != nil {
		t.Fatal(err)
	}
	wantGeo(t, "every output", shot.Geometry, computeruse.Geometry{LogicalW: 3000, LogicalH: 1920, Scale: 2})

	// Without the monitor list the window still has its place; the scale comes from the image.
	f.out["hyprctl -j monitors"] = []byte(`not json`)
	f.out["grim"] = pngOf(1200, 900)
	if shot, err = l.Capture(context.Background(), ScopeWindow, ""); err != nil {
		t.Fatal(err)
	}
	wantGeo(t, "no monitors", shot.Geometry, computeruse.Geometry{OriginX: 1930, OriginY: 40, LogicalW: 800, LogicalH: 600, Scale: 1.5})
}

// TestGeometrySwayScale2: rects are layout coordinates; the output's scale is the scale, or the
// image's when the tree has none.
func TestGeometrySwayScale2(t *testing.T) {
	tree := `{"type":"root","rect":{"x":0,"y":0,"width":1280,"height":800},"nodes":[
		{"type":"output","name":"__i3","rect":{"x":0,"y":0,"width":1280,"height":800}},
		{"type":"output","name":"eDP-1","scale":2,"current_workspace":"1","rect":{"x":0,"y":0,"width":1280,"height":800},"nodes":[
		{"type":"workspace","name":"1","nodes":[{"type":"con","name":"vim","app_id":"foot","focused":true,
		"rect":{"x":10,"y":20,"width":300,"height":200}}]}]}]}`
	f := &fakeExec{
		env:   map[string]string{"WAYLAND_DISPLAY": "w", "SWAYSOCK": "/s"},
		paths: map[string]bool{"swaymsg": true, "grim": true},
		out:   map[string][]byte{"swaymsg -t get_tree": []byte(tree), "grim": pngOf(600, 400)},
	}
	l := &Wayland{X: f}
	shot, err := l.Capture(context.Background(), ScopeWindow, "")
	if err != nil {
		t.Fatal(err)
	}
	wantGeo(t, "window", shot.Geometry, computeruse.Geometry{OriginX: 10, OriginY: 20, LogicalW: 300, LogicalH: 200, Scale: 2, Monitor: "eDP-1"})
	if shot, err = l.Capture(context.Background(), ScopeWindow, "vim"); err != nil {
		t.Fatal(err)
	}
	wantGeo(t, "named", shot.Geometry, computeruse.Geometry{OriginX: 10, OriginY: 20, LogicalW: 300, LogicalH: 200, Scale: 2, Monitor: "eDP-1"})
	if shot, err = l.Capture(context.Background(), ScopeScreen, ""); err != nil {
		t.Fatal(err)
	}
	wantGeo(t, "output", shot.Geometry, computeruse.Geometry{LogicalW: 1280, LogicalH: 800, Scale: 2, Monitor: "eDP-1"})

	// An older sway without the output's scale: the image says 600 px over 300 points.
	f.out["swaymsg -t get_tree"] = []byte(strings.Replace(tree, `"scale":2,`, "", 1))
	if shot, err = l.Capture(context.Background(), ScopeWindow, ""); err != nil {
		t.Fatal(err)
	}
	wantGeo(t, "no scale", shot.Geometry, computeruse.Geometry{OriginX: 10, OriginY: 20, LogicalW: 300, LogicalH: 200, Scale: 2, Monitor: "eDP-1"})

	// Nothing focused: every output but sway's scratchpad (__i3).
	f.out["swaymsg -t get_tree"] = []byte(strings.Replace(tree, `"focused":true`, `"focused":false`, 1))
	if shot, err = l.Capture(context.Background(), ScopeScreen, ""); err != nil {
		t.Fatal(err)
	}
	wantGeo(t, "every output", shot.Geometry, computeruse.Geometry{LogicalW: 1280, LogicalH: 800, Scale: 2})
}

// TestGeometryMac: a window's place is its kCGWindowBounds, its scale the image over its points;
// -m is the main display's.
func TestGeometryMac(t *testing.T) {
	img := pngOf(800, 600)
	f := &fakeExec{out: map[string][]byte{screencapture: nil}, onRun: func(_ string, args []string) error {
		return os.WriteFile(args[len(args)-1], img, 0o600)
	}}
	m := &Mac{X: f, Trusted: func() bool { return true },
		Windows: func() ([]MacWindow, error) {
			return []MacWindow{{ID: 9, Title: "Report", App: "Preview", X: -1200, Y: 25, W: 400, H: 300, OnScreen: true}}, nil
		},
		Main: func() (computeruse.Geometry, error) {
			return computeruse.Geometry{LogicalW: 1440, LogicalH: 900, Scale: 2, Monitor: "main"}, nil
		}}
	shot, err := m.Capture(context.Background(), ScopeWindow, "")
	if err != nil {
		t.Fatal(err)
	}
	wantGeo(t, "window", shot.Geometry, computeruse.Geometry{OriginX: -1200, OriginY: 25, LogicalW: 400, LogicalH: 300, Scale: 2})
	if shot, err = m.Capture(context.Background(), ScopeScreen, ""); err != nil {
		t.Fatal(err)
	}
	wantGeo(t, "main display", shot.Geometry, computeruse.Geometry{LogicalW: 1440, LogicalH: 900, Scale: 2, Monitor: "main"})
	m.Main = func() (computeruse.Geometry, error) { return computeruse.Geometry{}, errors.New("no display") }
	if shot, err = m.Capture(context.Background(), ScopeScreen, ""); err != nil || shot.Geometry != (computeruse.Geometry{}) {
		t.Errorf("no main display: %+v %v", shot.Geometry, err)
	}
}

// TestEncodeKeepsGeometry: encoding records the downscaled size and keeps the rest, so a point
// on the image the model saw maps back to the screen.
func TestEncodeKeepsGeometry(t *testing.T) {
	g := computeruse.Geometry{OriginX: 1920, OriginY: 0, LogicalW: 1568, LogicalH: 980, Scale: 2, Monitor: "HDMI-A-1"}
	shot := Shot{Image: image.NewRGBA(image.Rect(0, 0, 3136, 1960)), Geometry: g}
	data, err := EncodeShot(&shot)
	if err != nil || len(data) == 0 {
		t.Fatal(err)
	}
	g.ImageW, g.ImageH = 1568, 980
	wantGeo(t, "encoded", shot.Geometry, g)
	if x, y := shot.Geometry.ToGlobal(784, 490); x != 1920+784 || y != 490 {
		t.Errorf("ToGlobal(784, 490) = %d, %d", x, y)
	}
}
