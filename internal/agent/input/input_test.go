package input

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// fakePoster records each event as a line.
type fakePoster struct {
	got  []string
	x, y float64
}

func (f *fakePoster) Mouse(typ uint32, x, y float64, button uint32, clicks int64) error {
	f.got = append(f.got, fmt.Sprintf("mouse %d %g,%g b%d c%d", typ, x, y, button, clicks))
	f.x, f.y = x, y
	return nil
}

func (f *fakePoster) Wheel(w1, w2 int32) error {
	f.got = append(f.got, fmt.Sprintf("wheel %d %d", w1, w2))
	return nil
}

func (f *fakePoster) KeyEvent(code uint16, down bool, flags uint64) error {
	f.got = append(f.got, fmt.Sprintf("key %d %v %#x", code, down, flags))
	return nil
}

func (f *fakePoster) Location() (float64, float64, error) { return f.x, f.y, nil }

func (f *fakePoster) Unicode(units []uint16) error {
	f.got = append(f.got, fmt.Sprintf("unicode %q", string(rune(units[0])))+fmt.Sprint(len(units)))
	return nil
}

func (f *fakePoster) Key(code uint16, shift bool) error {
	f.got = append(f.got, fmt.Sprintf("dictate key %d %v", code, shift))
	return nil
}

func newMac() (*Mac, *fakePoster) {
	f := &fakePoster{}
	return &Mac{Trusted: func() bool { return true }, Post: f}, f
}

func check(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events:\n got %q\nwant %q", got, want)
	}
}

func TestClickSequence(t *testing.T) {
	ctx := context.Background()
	m, f := newMac()
	if err := m.Click(ctx, 10, 20, "left", 2); err != nil {
		t.Fatal(err)
	}
	check(t, f.got, []string{
		"mouse 5 10,20 b0 c0",
		"mouse 1 10,20 b0 c1", "mouse 2 10,20 b0 c1",
		"mouse 1 10,20 b0 c2", "mouse 2 10,20 b0 c2",
	})

	f.got = nil
	if err := m.Click(ctx, 3, 4, "right", 1); err != nil {
		t.Fatal(err)
	}
	check(t, f.got, []string{"mouse 5 3,4 b0 c0", "mouse 3 3,4 b1 c1", "mouse 4 3,4 b1 c1"})

	f.got = nil
	if err := m.Click(ctx, 3, 4, "middle", 1); err != nil {
		t.Fatal(err)
	}
	check(t, f.got, []string{"mouse 5 3,4 b0 c0", "mouse 25 3,4 b2 c1", "mouse 26 3,4 b2 c1"})

	if err := m.Click(ctx, 3, 4, "fourth", 1); err == nil {
		t.Error("unknown button: want an error")
	}
	if err := m.Click(ctx, 3, 4, "left", 0); err == nil {
		t.Error("zero clicks: want an error")
	}
}

func TestButtonThenMoveDrags(t *testing.T) {
	ctx := context.Background()
	m, f := newMac()
	_ = m.Move(ctx, 5, 6)
	_ = m.Button(ctx, "left", true)
	_ = m.Move(ctx, 7, 8)
	_ = m.Button(ctx, "left", false)
	_ = m.Move(ctx, 9, 9)
	check(t, f.got, []string{
		"mouse 5 5,6 b0 c0", "mouse 1 5,6 b0 c1", "mouse 6 7,8 b0 c0", "mouse 2 7,8 b0 c1", "mouse 5 9,9 b0 c0",
	})
}

func TestChordFlags(t *testing.T) {
	ctx := context.Background()
	m, f := newMac()
	if err := m.Keys(ctx, []string{"cmd", "Shift", "z"}); err != nil {
		t.Fatal(err)
	}
	check(t, f.got, []string{"key 6 true 0x120000", "key 6 false 0x120000"})

	f.got = nil
	_ = m.Keys(ctx, []string{"ctrl", "alt", "delete"})
	_ = m.Keys(ctx, []string{"f12"})
	_ = m.Keys(ctx, []string{"meta", "pagedown"})
	_ = m.Keys(ctx, []string{"shift"})
	check(t, f.got, []string{
		"key 117 true 0xc0000", "key 117 false 0xc0000",
		"key 111 true 0x0", "key 111 false 0x0",
		"key 121 true 0x100000", "key 121 false 0x100000",
		"key 56 true 0x20000", "key 56 false 0x20000",
	})

	for _, bad := range [][]string{{"cmd", "a", "b"}, {"hyper"}, {}} {
		if err := m.Keys(ctx, bad); err == nil {
			t.Errorf("Keys(%q): want an error", bad)
		}
	}
}

func TestKeyMapCoversKeyNames(t *testing.T) {
	// Every name computeruse accepts (kept in step by hand: input mustn't import computeruse).
	names := []string{"ctrl", "alt", "shift", "meta", "cmd", "super", "enter", "return", "tab", "escape", "esc",
		"space", "backspace", "delete", "insert", "home", "end", "pageup", "pagedown", "up", "down", "left", "right",
		"-", "=", ",", ".", "/", ";", "'", "[", "]", `\`, "`"}
	for c := 'a'; c <= 'z'; c++ {
		names = append(names, string(c))
	}
	for c := '0'; c <= '9'; c++ {
		names = append(names, string(c))
	}
	for i := 1; i <= 12; i++ {
		names = append(names, fmt.Sprintf("f%d", i))
	}
	seen := map[uint16]string{}
	for _, n := range names {
		if _, _, err := chord([]string{n}); err != nil {
			t.Errorf("%q: %v", n, err)
		}
		if c, ok := macKeyCodes[n]; ok && len(n) == 1 {
			if prev, dup := seen[c]; dup {
				t.Errorf("%q and %q share key code %d", prev, n, c)
			}
			seen[c] = n
		}
	}
}

func TestDrag(t *testing.T) {
	ctx := context.Background()
	m, f := newMac()
	if err := m.Drag(ctx, [][2]int{{1, 2}, {3, 4}, {5, 6}}); err != nil {
		t.Fatal(err)
	}
	check(t, f.got, []string{
		"mouse 5 1,2 b0 c0", "mouse 1 1,2 b0 c1", "mouse 6 3,4 b0 c0", "mouse 6 5,6 b0 c0", "mouse 2 5,6 b0 c1",
	})
	if err := m.Drag(ctx, [][2]int{{1, 2}}); err == nil {
		t.Error("one point: want an error")
	}
}

func TestScroll(t *testing.T) {
	ctx := context.Background()
	m, f := newMac()
	if err := m.Scroll(ctx, 100, 200, 0, 3); err != nil { // three notches down
		t.Fatal(err)
	}
	if err := m.Scroll(ctx, 100, 200, -2, 0); err != nil { // two notches left
		t.Fatal(err)
	}
	check(t, f.got, []string{"mouse 5 100,200 b0 c0", "wheel -3 0", "mouse 5 100,200 b0 c0", "wheel 0 2"})
}

func TestTypeAndCursor(t *testing.T) {
	ctx := context.Background()
	m, f := newMac()
	if err := m.Type(ctx, "hé"); err != nil {
		t.Fatal(err)
	}
	check(t, f.got, []string{`unicode "h"2`})
	_ = m.Move(ctx, 11, 12)
	if x, y, err := m.Cursor(ctx); err != nil || x != 11 || y != 12 {
		t.Fatalf("Cursor = %d,%d,%v; want 11,12", x, y, err)
	}
}

func TestUnavailable(t *testing.T) {
	ctx := context.Background()
	f := &fakePoster{}
	m := &Mac{Trusted: func() bool { return false }, Post: f}
	if ok, why := m.Available(ctx); ok || why == "" {
		t.Fatalf("Available = %v %q", ok, why)
	}
	if err := m.Click(ctx, 1, 1, "left", 1); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Click = %v, want ErrUnavailable", err)
	}
	if len(f.got) != 0 {
		t.Fatalf("posted %q without Accessibility", f.got)
	}
	if ok, _ := (&Mac{Trusted: func() bool { return true }}).Available(ctx); ok {
		t.Fatal("no poster: want unavailable")
	}
}

func TestInjecting(t *testing.T) {
	ctx := context.Background()
	m, _ := newMac()
	if s, e := m.Injecting(); !s.IsZero() || !e.IsZero() {
		t.Fatal("want zero before any input")
	}
	before := time.Now()
	_ = m.Keys(ctx, []string{"a"})
	s, e := m.Injecting()
	if s.Before(before) || e.Before(s) || time.Since(e) < 0 {
		t.Fatalf("Injecting = %v..%v, called at %v", s, e, before)
	}
}
