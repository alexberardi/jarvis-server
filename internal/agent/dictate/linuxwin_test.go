package dictate

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func hyprFake(out map[string]string) *fakeExec {
	return &fakeExec{env: map[string]string{"WAYLAND_DISPLAY": "wayland-1", "HYPRLAND_INSTANCE_SIGNATURE": "sig"}, bins: bins("hyprctl"),
		out: func(c Cmd) (string, error) {
			if o, ok := out[strings.Join(c.Args, " ")]; ok {
				return o, nil
			}
			return "", errors.New("unexpected " + strings.Join(c.Args, " "))
		}}
}

func TestFocusedWindowHyprland(t *testing.T) {
	w := &LinuxWindows{X: hyprFake(map[string]string{"-j activewindow": `{"address":"0x5a","at":[1930,40],"size":[1200,800],` +
		`"class":"org.gnome.TextEditor","title":"notes.txt","pid":4242}`})}
	got, err := w.FocusedWindow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := FocusedWindow{Class: "org.gnome.TextEditor", Title: "notes.txt", PID: 4242, X: 1930, Y: 40, W: 1200, H: 800}
	if got == nil || *got != want {
		t.Fatalf("got %+v", got)
	}
	if c := w.Compositor(); c != "hyprland" {
		t.Fatalf("compositor %q", c)
	}
}

func TestFocusedWindowHyprlandNone(t *testing.T) {
	w := &LinuxWindows{X: hyprFake(map[string]string{"-j activewindow": `{}`})}
	got, err := w.FocusedWindow(context.Background())
	if err != nil || got != nil {
		t.Fatalf("an empty workspace has no window: %+v %v", got, err)
	}
}

func TestCursorHyprland(t *testing.T) {
	w := &LinuxWindows{X: hyprFake(map[string]string{"cursorpos": "2010, 377"})}
	x, y, ok := w.Cursor(context.Background())
	if !ok || x != 2010 || y != 377 {
		t.Fatalf("got %v,%v %v", x, y, ok)
	}
	bad := &LinuxWindows{X: hyprFake(map[string]string{"cursorpos": "nope"})}
	if _, _, ok := bad.Cursor(context.Background()); ok {
		t.Fatal("garbage is no position")
	}
}

func TestFocusedWindowSway(t *testing.T) {
	tree := `{"type":"root","nodes":[{"type":"output","name":"DP-1","nodes":[{"type":"workspace","nodes":[
		{"type":"con","id":7,"name":"Terminal","app_id":"foot","pid":99,"focused":false,"rect":{"x":0,"y":0,"width":960,"height":1080}},
		{"type":"con","id":8,"name":"Inbox - Thunderbird","pid":321,"focused":true,"window_properties":{"class":"thunderbird"},
		 "rect":{"x":960,"y":0,"width":960,"height":1080}}]}]}]}`
	f := &fakeExec{env: map[string]string{"WAYLAND_DISPLAY": "wayland-1", "SWAYSOCK": "/run/sway"}, bins: bins("swaymsg"),
		out: func(c Cmd) (string, error) { return tree, nil }}
	w := &LinuxWindows{X: f}
	got, err := w.FocusedWindow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := FocusedWindow{Class: "thunderbird", Title: "Inbox - Thunderbird", PID: 321, X: 960, Y: 0, W: 960, H: 1080}
	if got == nil || *got != want {
		t.Fatalf("got %+v", got)
	}
	if _, _, ok := w.Cursor(context.Background()); ok {
		t.Fatal("sway has no IPC for the cursor")
	}
	if len(f.ran) != 1 {
		t.Fatalf("Cursor ran %v on sway", f.ran)
	}
}

func TestFocusedWindowElsewhere(t *testing.T) {
	gnome := &LinuxWindows{X: &fakeExec{env: map[string]string{"WAYLAND_DISPLAY": "wayland-0"}}}
	if c := gnome.Compositor(); c != "" {
		t.Fatalf("compositor %q", c)
	}
	if _, err := gnome.FocusedWindow(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err %v", err)
	}
}
