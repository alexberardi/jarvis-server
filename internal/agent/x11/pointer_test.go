package x11

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"slices"
	"strings"
	"testing"
)

func TestFakeClick(t *testing.T) {
	f := newFake()
	d, _ := display(f)
	if err := d.Click(context.Background(), 120, 45, ButtonLeft, 2); err != nil {
		t.Fatal(err)
	}
	want := []string{"motion 120,45", "button 1 down", "button 1 up", "button 1 down", "button 1 up", "sync"}
	if !slices.Equal(f.log, want) {
		t.Fatalf("got  %v\nwant %v", f.log, want)
	}
	if x, y, err := d.Pointer(context.Background()); err != nil || x != 120 || y != 45 {
		t.Fatalf("pointer %d,%d %v", x, y, err)
	}
}

func TestScrollButtons(t *testing.T) {
	for _, tc := range []struct {
		dx, dy int
		want   string
	}{
		{0, 2, "motion 5,6; button 5 down; button 5 up; button 5 down; button 5 up; sync"},
		{0, -1, "motion 5,6; button 4 down; button 4 up; sync"},
		{-1, 0, "motion 5,6; button 6 down; button 6 up; sync"},
		{1, 1, "motion 5,6; button 5 down; button 5 up; button 7 down; button 7 up; sync"},
	} {
		f := newFake()
		d, _ := display(f)
		if err := d.Scroll(context.Background(), 5, 6, tc.dx, tc.dy); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(f.log, "; "); got != tc.want {
			t.Errorf("dx %d dy %d: got %s\nwant %s", tc.dx, tc.dy, got, tc.want)
		}
	}
}

func TestDragHoldsTheLeftButton(t *testing.T) {
	f := newFake()
	d, _ := display(f)
	if err := d.Drag(context.Background(), [][2]int{{1, 2}, {3, 4}, {5, 6}}); err != nil {
		t.Fatal(err)
	}
	want := "motion 1,2; button 1 down; motion 3,4; motion 5,6; button 1 up; sync"
	if got := strings.Join(f.log, "; "); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if err := d.Drag(context.Background(), [][2]int{{1, 2}}); err == nil {
		t.Fatal("a one-point drag was accepted")
	}
}

func TestPointerNeedsXTest(t *testing.T) {
	f := newFake()
	f.xtest = false
	d, _ := display(f)
	if err := d.Move(context.Background(), 1, 1); err == nil || !strings.Contains(err.Error(), "XTEST") {
		t.Fatalf("err %v", err)
	}
	if len(f.log) != 0 {
		t.Fatalf("posted without XTEST: %v", f.log)
	}
}

// chordFake adds Control_L (16), t/T (17), F5 (18) and Super_L (19) to the fake's keymap.
func chordFake() *fakeConn {
	f := newFake()
	f.keymap = append(f.keymap, keyControlL, 0, 't', 'T', keyF1+4, 0, keySuperL, 0)
	return f
}

func TestChordPressesModifiersAroundTheKey(t *testing.T) {
	for _, tc := range []struct {
		chord []string
		want  string
	}{
		{[]string{"ctrl", "shift", "t"}, "key 16 down; key 10 down; key 17 down; key 17 up; key 10 up; key 16 up; sync"},
		{[]string{"Cmd", "F5"}, "key 19 down; key 18 down; key 18 up; key 19 up; sync"},
		{[]string{"super"}, "key 19 down; key 19 up; sync"},
		{[]string{"enter"}, "key 15 down; key 15 up; sync"},
		{[]string{"shift", "1"}, "key 10 down; key 13 down; key 13 up; key 10 up; sync"},
	} {
		f := chordFake()
		d, _ := display(f)
		if err := d.Keys(context.Background(), tc.chord); err != nil {
			t.Fatalf("%v: %v", tc.chord, err)
		}
		if got := strings.Join(f.log, "; "); got != tc.want {
			t.Errorf("%v: got %s\nwant %s", tc.chord, got, tc.want)
		}
	}
}

func TestChordErrors(t *testing.T) {
	for _, tc := range []struct {
		chord []string
		want  string
	}{
		{nil, "empty chord"},
		{[]string{"ctrl", "a", "t"}, "two keys"},
		{[]string{"hyper"}, "unknown key"},
		{[]string{"f13"}, "unknown key"},
		{[]string{"alt", "a"}, `no "alt" key`},
	} {
		f := chordFake()
		d, _ := display(f)
		err := d.Keys(context.Background(), tc.chord)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err %v, want %q", tc.chord, err, tc.want)
		}
		for _, l := range f.log {
			if strings.HasPrefix(l, "key ") {
				t.Errorf("%v: pressed keys on error: %v", tc.chord, f.log)
				break
			}
		}
	}
}

func TestGetImageDecode(t *testing.T) {
	f := newFake()
	f.geoms = map[uint32][4]int{root: {0, 0, 2, 1}}
	f.depth = 24
	// two pixels, B G R pad: pure red, then (10, 20, 30)
	f.pixels = []byte{0, 0, 0xff, 0, 30, 20, 10, 0}
	d, _ := display(f)
	c, err := d.Capture(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Image.Bounds() != image.Rect(0, 0, 2, 1) || c.W != 2 || c.H != 1 {
		t.Fatalf("bounds %v %+v", c.Image.Bounds(), c)
	}
	if got := c.Image.RGBAAt(0, 0); got != (color.RGBA{0xff, 0, 0, 0xff}) {
		t.Errorf("pixel 0: %v", got)
	}
	if got := c.Image.RGBAAt(1, 0); got != (color.RGBA{10, 20, 30, 0xff}) {
		t.Errorf("pixel 1: %v", got)
	}
	f.depth = 16
	if _, err := d.Capture(context.Background(), 0); err == nil {
		t.Error("16-bit depth decoded")
	}
	f.depth, f.pixels = 24, f.pixels[:4]
	if _, err := d.Capture(context.Background(), 0); err == nil {
		t.Error("short image decoded")
	}
}

func TestCaptureWindowIsItsScreenArea(t *testing.T) {
	f := newFake()
	const win = 0x400
	f.geoms = map[uint32][4]int{root: {0, 0, 100, 80}, win: {90, 70, 20, 20}}
	f.depth = 24
	f.pixels = make([]byte, 10*10*4)
	d, _ := display(f)
	c, err := d.Capture(context.Background(), win)
	if err != nil {
		t.Fatal(err)
	}
	// Clipped to the screen, read from the root window.
	if c.X != 90 || c.Y != 70 || c.W != 10 || c.H != 10 {
		t.Fatalf("capture %+v", c)
	}
	if want := "image 0x100 (90,70)-(100,80)"; !slices.Contains(f.log, want) {
		t.Fatalf("log %v, want %q", f.log, want)
	}
	f.geoms[win] = [4]int{200, 200, 10, 10}
	if _, err := d.Capture(context.Background(), win); err == nil {
		t.Fatal("captured a window off the screen")
	}
}

func TestChordCoversKeyNames(t *testing.T) {
	// Every name computeruse.KeyNames accepts (kept in step by hand, as input's test does).
	names := []string{"enter", "return", "tab", "escape", "esc", "space", "backspace", "delete", "insert",
		"home", "end", "pageup", "pagedown", "up", "down", "left", "right",
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
	for _, n := range names {
		if _, ok := chordSym(n); !ok {
			t.Errorf("%q has no keysym", n)
		}
	}
	for _, m := range []string{"ctrl", "alt", "shift", "meta", "cmd", "super"} {
		if len(chordMods[m]) == 0 {
			t.Errorf("modifier %q has no keysym", m)
		}
	}
	if s, _ := chordSym("f12"); s != 0xffc9 {
		t.Errorf("f12 = %#x", s)
	}
}
