package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/jpeg"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

func TestSnapshotParses(t *testing.T) {
	h, path := startHub(t)
	long := strings.Repeat("x", 300)
	f := connectExt(t, h, path, "Firefox", true, func(m Message) Message {
		return ok(map[string]any{"gen": 7, "url": "https://shop.test/", "title": "Shop", "elements": []any{
			map[string]any{"ref": "e1", "role": "searchbox", "name": "Search", "type": "search", "value": "socks", "frame": 0},
			map[string]any{"ref": "e2", "role": "button", "name": long, "type": "button", "state": []any{"disabled"}},
			map[string]any{"ref": "e3", "role": "textbox", "name": "Password", "type": "password", "value": "leaked"},
			map[string]any{"ref": "e4", "role": "link", "name": "Cart", "type": "a", "frame": 3},
			map[string]any{"ref": "e5", "role": "textbox", "name": "Card", "type": "text", "autocomplete": "billing cc-number", "value": "4111111111111111"},
			map[string]any{"ref": "e6", "role": "textbox", "name": "Code", "type": "text", "autocomplete": "one-time-code", "value": "123456"},
			map[string]any{"ref": "e7", "role": "textbox", "type": "hidden", "value": "csrf-token"},
		}})
	})
	ts := NewTools(h)
	s, err := ts.Snapshot(context.Background(), "firefox:5", 2000)
	if err != nil {
		t.Fatal(err)
	}
	if s.Gen != 7 || s.URL != "https://shop.test/" || s.Title != "Shop" || len(s.Elements) != 7 {
		t.Fatalf("snapshot %+v", s)
	}
	if e := s.Find("e1"); e == nil || e.Role != "searchbox" || e.Value != "socks" || e.Frame != 0 {
		t.Errorf("e1 %+v", e)
	}
	if e := s.Find("e2"); len([]rune(e.Name)) != snapshotNameMax || len(e.State) != 1 || e.State[0] != "disabled" {
		t.Errorf("e2 name %d runes, state %v", len([]rune(e.Name)), e.State)
	}
	if e := s.Find("e3"); e.Value != "" || !e.Element().Secure {
		t.Errorf("password field: %+v", e)
	}
	for _, ref := range []string{"e5", "e6", "e7"} {
		if e := s.Find(ref); e.Value != "" {
			t.Errorf("secret field %s value %q", ref, e.Value)
		}
	}
	if e := s.Find("e4"); e.Frame != 3 {
		t.Errorf("e4 frame %d", e.Frame)
	}
	cmd := f.received()[0]
	if cmd.Cmd != "snapshot" || cmd.Args["tab_id"] != float64(5) || cmd.Args["max"] != float64(MaxSnapshotMax) {
		t.Errorf("sent %s %v", cmd.Cmd, cmd.Args)
	}
	if _, err := ts.Snapshot(context.Background(), "", 0); err != nil {
		t.Fatal(err)
	}
	if a := f.received()[1].Args; a["max"] != float64(DefaultSnapshotMax) || a["tab_id"] != nil {
		t.Errorf("default snapshot sent %v", a)
	}
}

func TestSnapshotMalformed(t *testing.T) {
	h, path := startHub(t)
	connectExt(t, h, path, "Firefox", true, func(Message) Message { return ok(map[string]any{"elements": []any{}}) })
	if _, err := NewTools(h).Snapshot(context.Background(), "1", 0); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("no gen: %v", err)
	}
}

func TestActSends(t *testing.T) {
	h, path := startHub(t)
	f := connectExt(t, h, path, "Firefox", true, func(Message) Message {
		return ok(map[string]any{"ok": true, "url_after": "https://shop.test/cart", "title_after": "Cart"})
	})
	ts := NewTools(h)
	r, err := ts.Act(context.Background(), "firefox:5", 7, "e12", "keys", "", []string{"ArrowDown", "Enter"})
	if err != nil || !r.OK || r.URLAfter != "https://shop.test/cart" || r.TitleAfter != "Cart" {
		t.Fatalf("act: %+v %v", r, err)
	}
	a := f.received()[0].Args
	if a["gen"] != float64(7) || a["ref"] != "e12" || a["op"] != "keys" || a["tab_id"] != float64(5) || len(a["keys"].([]any)) != 2 {
		t.Errorf("sent %v", a)
	}
	for _, c := range []struct {
		gen       int
		ref, op   string
		value     string
		keys      []string
		wantInMsg string
	}{
		{0, "e1", "press", "", nil, "gen"},
		{1, "12", "press", "", nil, "ref"},
		{1, "e1", "drag", "", nil, "op"},
		{1, "e1", "keys", "", nil, "keys"},
		{1, "e1", "keys", "", []string{"ctrl"}, "keys"},
		{1, "e1", "set_value", strings.Repeat("a", 2001), nil, "longer"},
	} {
		if _, err := ts.Act(context.Background(), "1", c.gen, c.ref, c.op, c.value, c.keys); !errors.Is(err, errBadArgs) || !strings.Contains(err.Error(), c.wantInMsg) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	if n := len(f.received()); n != 1 {
		t.Errorf("bad arguments reached the extension: %d commands", n)
	}
}

func TestActStaleRef(t *testing.T) {
	h, path := startHub(t)
	connectExt(t, h, path, "Firefox", true, func(Message) Message {
		return Message{Code: "stale_ref", Error: "the page changed since that snapshot; take a new one"}
	})
	_, err := NewTools(h).Act(context.Background(), "firefox:5", 6, "e3", "press", "", nil)
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Code != "stale_ref" || code(err) != "stale_ref" {
		t.Fatalf("stale ref: %v", err)
	}
}

func TestActPasswordRefused(t *testing.T) {
	h, path := startHub(t)
	connectExt(t, h, path, "Firefox", true, func(m Message) Message {
		if m.Args["op"] == "set_value" {
			return Message{Code: "password_field", Error: "that is a password field"}
		}
		return ok(map[string]any{"ok": true})
	})
	_, err := NewTools(h).Act(context.Background(), "firefox:5", 6, "e3", "set_value", "hunter2", nil)
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Code != "password_field" || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("password field: %v", err)
	}
}

func TestOpenBackground(t *testing.T) {
	h, path := startHub(t)
	f := connectExt(t, h, path, "Firefox", true, func(Message) Message {
		return ok(map[string]any{"tab": map[string]any{"tab_id": 41, "window_id": 9, "url": "https://shop.test/"}, "window_id": 9})
	})
	ts := NewTools(h)
	tab, win, err := ts.OpenBackground(context.Background(), "shop.test")
	if err != nil || tab != "firefox:41" || win != "firefox:9" {
		t.Fatalf("open: %q %q %v", tab, win, err)
	}
	a := f.received()[0].Args
	if f.received()[0].Cmd != "open" || a["background"] != true || a["url"] != "https://shop.test" {
		t.Errorf("sent %v", a)
	}
	if _, _, err := ts.OpenBackground(context.Background(), "javascript:alert(1)"); !errors.Is(err, errBadArgs) {
		t.Errorf("javascript url: %v", err)
	}
}

// An extension before 0.3.0 doesn't know snapshot or act, and ignores open's background flag.
func TestOutdatedExtension(t *testing.T) {
	h, path := startHub(t)
	connectExt(t, h, path, "Firefox", true, func(m Message) Message {
		if m.Cmd == "open" {
			return ok(map[string]any{"tab": map[string]any{"tab_id": 41, "window_id": 9}})
		}
		return Message{Code: "unknown_command", Error: "unknown command " + m.Cmd}
	})
	ts := NewTools(h)
	ctx := context.Background()
	_, err1 := ts.Snapshot(ctx, "1", 0)
	_, err2 := ts.Act(ctx, "1", 1, "e1", "press", "", nil)
	_, _, err3 := ts.OpenBackground(ctx, "https://shop.test")
	_, _, err4 := ts.Screenshot(ctx, "firefox:9")
	_, err5 := ts.Download(ctx, "1", "e1", "")
	for i, err := range []error{err1, err2, err3, err4, err5} {
		if code(err) != "extension_outdated" || !strings.Contains(err.Error(), "Firefox is version 0.1.0") {
			t.Errorf("%d: %v", i, err)
		}
	}
}

// jpegB64 is a w×h JPEG, base64 (what captureVisibleTab returns after its data: prefix).
func jpegB64(t *testing.T, w, h int) string {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func TestScreenshotGeometry(t *testing.T) {
	h, path := startHub(t)
	data := jpegB64(t, 200, 100)
	f := connectExt(t, h, path, "Firefox", true, func(Message) Message {
		// The viewport in CSS pixels; the image is physical (DPR 2).
		return ok(map[string]any{"data": data, "width": 100, "height": 50, "device_pixel_ratio": 2})
	})
	shot, g, err := NewTools(h).Screenshot(context.Background(), "firefox:9")
	if err != nil {
		t.Fatal(err)
	}
	want := computeruse.Geometry{LogicalW: 100, LogicalH: 50, Scale: 2, ImageW: 200, ImageH: 100}
	if g != want || shot.Geometry != want {
		t.Fatalf("geometry %+v, shot %+v, want %+v", g, shot.Geometry, want)
	}
	if b := shot.Image.Bounds(); b.Dx() != 200 || b.Dy() != 100 {
		t.Errorf("image %v", b)
	}
	if x, y := g.ToGlobal(100, 50); x != 50 || y != 25 {
		t.Errorf("image centre maps to %d,%d", x, y)
	}
	cmd := f.received()[0]
	if cmd.Cmd != "screenshot" || cmd.Args["window_id"] != float64(9) || cmd.Args["tab_id"] != nil {
		t.Errorf("sent %s %v", cmd.Cmd, cmd.Args)
	}
}

func TestScreenshotRefused(t *testing.T) {
	h, path := startHub(t)
	connectExt(t, h, path, "Firefox", true, func(m Message) Message {
		if m.Args["window_id"] == float64(1) {
			return Message{Code: "window_minimized", Error: "the task window is minimised"}
		}
		return ok(map[string]any{"data": "not a jpeg", "width": 100, "height": 50, "device_pixel_ratio": 1})
	})
	ts := NewTools(h)
	ctx := context.Background()
	if _, _, err := ts.Screenshot(ctx, "firefox:1"); code(err) != "window_minimized" {
		t.Errorf("minimised: %v", err)
	}
	if _, _, err := ts.Screenshot(ctx, "firefox:2"); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Errorf("bad image: %v", err)
	}
	for _, w := range []string{"", "firefox:x"} {
		if _, _, err := ts.Screenshot(ctx, w); !errors.Is(err, errBadArgs) {
			t.Errorf("window %q: %v", w, err)
		}
	}
}

func TestDownloadByRef(t *testing.T) {
	h, path := startHub(t)
	f := connectExt(t, h, path, "Firefox", true, func(Message) Message {
		return ok(map[string]any{"path": "/home/u/Downloads/statement-2026-09.csv", "bytes": 2048,
			"mime": "text/csv", "filename": "statement-2026-09.csv"})
	})
	ts := NewTools(h)
	ctx := context.Background()
	r, err := ts.Download(ctx, "firefox:5", "e12", "")
	want := DownloadResult{Path: "/home/u/Downloads/statement-2026-09.csv", Bytes: 2048, MIME: "text/csv", Filename: "statement-2026-09.csv"}
	if err != nil || r != want {
		t.Fatalf("download: %+v %v", r, err)
	}
	cmd := f.received()[0]
	if cmd.Cmd != "download" || cmd.Args["tab_id"] != float64(5) || cmd.Args["ref"] != "e12" || cmd.Args["url"] != nil {
		t.Errorf("sent %s %v", cmd.Cmd, cmd.Args)
	}
	if _, err := ts.Download(ctx, "firefox:5", "", "https://bank.test/statement.csv"); err != nil {
		t.Fatal(err)
	}
	if a := f.received()[1].Args; a["url"] != "https://bank.test/statement.csv" || a["ref"] != nil {
		t.Errorf("by url sent %v", a)
	}
	for _, c := range [][2]string{{"", ""}, {"e1", "https://bank.test/x"}, {"12", ""}, {"", "file:///etc/passwd"}} {
		if _, err := ts.Download(ctx, "firefox:5", c[0], c[1]); !errors.Is(err, errBadArgs) {
			t.Errorf("ref %q url %q: %v", c[0], c[1], err)
		}
	}
	if len(f.received()) != 2 {
		t.Errorf("bad arguments reached the extension: %v", f.received()[2:])
	}
}

func TestDownloadTimeout(t *testing.T) {
	defer func(d time.Duration) { downloadTimeout = d }(downloadTimeout)
	downloadTimeout = 50 * time.Millisecond
	h, path := startHub(t)
	connectExt(t, h, path, "Firefox", true, nil) // never answers
	_, err := NewTools(h).Download(context.Background(), "firefox:5", "e3", "")
	if code(err) != "timeout" {
		t.Fatalf("download: %v", err)
	}
}
