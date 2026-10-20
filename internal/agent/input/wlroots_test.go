package input

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/vptr"
	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

// fakeWl records the pointer events and keyboard strokes as lines.
type fakeWl struct {
	got    []string
	why    string // not "" : unavailable
	fail   error
	during func() // runs inside Send
}

func (f *fakeWl) Available(context.Context) (bool, string) { return f.why == "", f.why }

func (f *fakeWl) Send(_ context.Context, evs ...vptr.Event) error {
	if f.during != nil {
		f.during()
	}
	if f.fail != nil {
		return f.fail
	}
	var s []string
	for _, e := range evs {
		s = append(s, e.String())
	}
	f.got = append(f.got, strings.Join(s, ", "))
	return nil
}

type fakeKbd struct{ fakeWl }

func (f *fakeKbd) Send(_ context.Context, strokes []xkb.Stroke) error {
	if f.fail != nil {
		return f.fail
	}
	for _, s := range strokes {
		f.got = append(f.got, fmt.Sprintf("%#x shift=%v mods=%d", s.Sym, s.Shift, s.Mods))
	}
	return nil
}

func newWl() (*Wlroots, *fakeWl, *fakeKbd) {
	p, k := &fakeWl{}, &fakeKbd{}
	return &Wlroots{Pointer: p, Keyboard: k, Getenv: func(string) string { return "" }}, p, k
}

func TestWlPointer(t *testing.T) {
	w, p, _ := newWl()
	ctx := context.Background()
	for _, err := range []error{
		w.Click(ctx, 10, 20, "", 2),
		w.Click(ctx, 5, 6, "right", 1),
		w.Move(ctx, 7, 8),
		w.Button(ctx, "middle", true),
		w.Button(ctx, "middle", false),
		w.Drag(ctx, [][2]int{{1, 2}, {3, 4}, {5, 6}}),
		w.Scroll(ctx, 30, 40, -1, 3),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	check(t, p.got, []string{
		"motion 10,20, press 0x110, release 0x110, press 0x110, release 0x110",
		"motion 5,6, press 0x111, release 0x111",
		"motion 7,8",
		"press 0x112",
		"release 0x112",
		"motion 1,2, press 0x110, motion 3,4, motion 5,6, release 0x110",
		"motion 30,40, wheel -1,3",
	})
	if err := w.Click(ctx, 1, 1, "back", 1); err == nil {
		t.Fatal("unknown button accepted")
	}
	if err := w.Click(ctx, 1, 1, "", 4); err == nil {
		t.Fatal("4 clicks accepted")
	}
	if err := w.Drag(ctx, [][2]int{{1, 1}}); err == nil {
		t.Fatal("one-point drag accepted")
	}
}

// Chords go to the virtual keyboard as one stroke: the key with the modifiers' XKB mask (Shift
// as the stroke's Shift); cmd, meta and super are Mod4.
func TestWlChords(t *testing.T) {
	w, _, k := newWl()
	ctx := context.Background()
	for _, c := range [][]string{{"ctrl", "s"}, {"cmd", "shift", "T"}, {"alt", "f4"}, {"super"}, {"ctrl", "alt", "delete"},
		{"shift", "tab"}, {"meta", "pagedown"}, {"home"}, {"f12"}} {
		if err := w.Keys(ctx, c); err != nil {
			t.Fatalf("%v: %v", c, err)
		}
	}
	check(t, k.got, []string{
		"0x73 shift=false mods=4",    // ctrl+s
		"0x74 shift=true mods=64",    // cmd+shift+t
		"0xffc1 shift=false mods=8",  // alt+F4
		"0xffeb shift=false mods=64", // Super_L alone
		"0xffff shift=false mods=12", // ctrl+alt+Delete
		"0xff09 shift=true mods=0",   // shift+Tab
		"0xff56 shift=false mods=64", // meta+Next
		"0xff50 shift=false mods=0",  // Home
		"0xffc9 shift=false mods=0",  // F12
	})
	for _, c := range [][]string{{"ctrl", "a", "b"}, {"hyper", "a"}, {}} {
		if err := w.Keys(ctx, c); err == nil {
			t.Fatalf("%v accepted", c)
		}
	}
	for _, n := range keyNames() {
		if _, err := wlChord([]string{n}); err != nil {
			t.Errorf("%q: %v", n, err)
		}
	}
	k.got = nil
	if err := w.Type(ctx, "hé"); err != nil {
		t.Fatal(err)
	}
	check(t, k.got, []string{"0x68 shift=false mods=0", "0xe9 shift=false mods=0"})
}

func TestWlCursor(t *testing.T) {
	ctx := context.Background()
	// Hyprland says where the pointer is.
	w, _, _ := newWl()
	w.Getenv = func(k string) string { return map[string]string{"HYPRLAND_INSTANCE_SIGNATURE": "abc"}[k] }
	var ran []string
	w.Output = func(_ context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return []byte(`{"x": 1203.6, "y": -40}` + "\n"), nil
	}
	if x, y, err := w.Cursor(ctx); err != nil || x != 1204 || y != -40 || len(ran) != 1 || ran[0] != "hyprctl -j cursorpos" {
		t.Fatalf("hyprland: %d,%d %v %q", x, y, err, ran)
	}
	w.Output = func(context.Context, string, ...string) ([]byte, error) { return []byte("ok"), nil }
	if _, _, err := w.Cursor(ctx); err == nil {
		t.Fatal("garbage accepted")
	}
	// sway doesn't: the last position Jarvis moved to, once there is one.
	w, _, _ = newWl()
	w.Output = func(context.Context, string, ...string) ([]byte, error) { t.Fatal("ran a command"); return nil, nil }
	if _, _, err := w.Cursor(ctx); err == nil {
		t.Fatal("a position before any move")
	}
	_ = w.Click(ctx, 11, 12, "left", 1)
	_ = w.Scroll(ctx, 13, 14, 0, 1)
	if x, y, err := w.Cursor(ctx); err != nil || x != 13 || y != 14 {
		t.Fatalf("sway: %d,%d %v", x, y, err)
	}
	// A failed move doesn't count.
	w.Pointer.(*fakeWl).fail = errors.New("outside the screen")
	if err := w.Move(ctx, 99, 99); err == nil {
		t.Fatal("no error")
	}
	if x, y, _ := w.Cursor(ctx); x != 13 || y != 14 {
		t.Fatalf("after a failed move: %d,%d", x, y)
	}
}

func TestWlAvailableAndInjecting(t *testing.T) {
	ctx := context.Background()
	w, p, k := newWl()
	if ok, why := w.Available(ctx); !ok {
		t.Fatal(why)
	}
	k.why = "the compositor doesn't offer the virtual-keyboard protocol"
	if ok, why := w.Available(ctx); ok || why != k.why {
		t.Fatal(ok, why)
	}
	p.why = "the compositor doesn't offer the virtual-pointer protocol"
	if ok, why := w.Available(ctx); ok || why != p.why {
		t.Fatal(ok, why)
	}
	if s, e := w.Injecting(); !s.IsZero() || !e.IsZero() {
		t.Fatal("window before any call")
	}
	p.during = func() {
		if s, e := w.Injecting(); s.IsZero() || !e.IsZero() {
			t.Errorf("mid-call window %v..%v", s, e)
		}
	}
	if err := w.Move(ctx, 1, 1); err != nil {
		t.Fatal(err)
	}
	if s, e := w.Injecting(); s.IsZero() || e.Before(s) {
		t.Fatalf("window %v..%v", s, e)
	}
}
