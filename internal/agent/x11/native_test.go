//go:build linux

package x11

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

// TestNativeType types JARVIS_X11_TYPE into the focused window of $DISPLAY. Only for a
// throwaway headless server (Xvfb), never a desktop someone is using.
func TestNativeType(t *testing.T) {
	text := os.Getenv("JARVIS_X11_TYPE")
	if text == "" {
		t.Skip("JARVIS_X11_TYPE=<text> types into the focused window (Xvfb only)")
	}
	d := &Display{}
	if ok, why := d.TypingAvailable(context.Background()); !ok {
		t.Fatal(why)
	}
	strokes, _ := xkb.Strokes(text)
	if os.Getenv("JARVIS_X11_KEYS") != "" {
		strokes = append(strokes,
			xkb.Stroke{Sym: xkb.KeyReturn}, xkb.Stroke{Sym: xkb.KeyEscape}, xkb.Stroke{Sym: xkb.KeyTab},
			xkb.Stroke{Sym: xkb.KeyTab, Shift: true}, xkb.Stroke{Sym: xkb.KeySpace}, xkb.Stroke{Sym: xkb.KeyUp},
			xkb.Stroke{Sym: xkb.KeyDown}, xkb.Stroke{Sym: xkb.KeyLeft}, xkb.Stroke{Sym: xkb.KeyRight})
	}
	before := keymap(t)
	if err := d.Send(context.Background(), strokes); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before, keymap(t)) {
		t.Fatal("the keyboard mapping wasn't restored")
	}
}

func keymap(t *testing.T) []uint32 {
	c, err := dialX()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, syms, err := c.Keymap()
	if err != nil {
		t.Fatal(err)
	}
	return syms
}

// TestNativeWindows lists $DISPLAY's windows and the active one (read-only). Prints titles:
// for a throwaway Xvfb only.
func TestNativeWindows(t *testing.T) {
	if os.Getenv("JARVIS_X11_LIST") == "" {
		t.Skip("JARVIS_X11_LIST=1 lists the windows of $DISPLAY (Xvfb only: prints titles)")
	}
	d := &Display{}
	if ok, why := d.WMAvailable(context.Background()); !ok {
		t.Fatal(why)
	}
	ws, err := d.Windows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	act, err := d.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		t.Logf("%s %q %q minimized=%v active=%v", FormatID(w.ID), w.Class, w.Title, w.Minimized, w.ID == act)
	}
}

// TestNativePointer clicks JARVIS_X11_CLICK=x,y on $DISPLAY and checks the pointer is there;
// JARVIS_X11_CAPTURE=1 captures the root and the active window and logs their geometry. Only for
// a throwaway headless server (Xvfb), never a desktop someone is using.
func TestNativePointer(t *testing.T) {
	click, capture := os.Getenv("JARVIS_X11_CLICK"), os.Getenv("JARVIS_X11_CAPTURE")
	if click == "" && capture == "" {
		t.Skip("JARVIS_X11_CLICK=x,y clicks there, JARVIS_X11_CAPTURE=1 captures (Xvfb only)")
	}
	ctx := context.Background()
	d := &Display{}
	if click != "" {
		var x, y int
		if _, err := fmt.Sscanf(click, "%d,%d", &x, &y); err != nil {
			t.Fatalf("JARVIS_X11_CLICK=%q: %v", click, err)
		}
		if err := d.Click(ctx, x, y, ButtonLeft, 1); err != nil {
			t.Fatal(err)
		}
		if px, py, err := d.Pointer(ctx); err != nil || px != x || py != y {
			t.Fatalf("pointer at %d,%d (%v), want %d,%d", px, py, err, x, y)
		}
		if keys := os.Getenv("JARVIS_X11_CHORD"); keys != "" {
			if err := d.Keys(ctx, strings.Split(keys, "+")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if capture != "" {
		c, err := d.Capture(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("root: %d,%d %dx%d, image %v", c.X, c.Y, c.W, c.H, c.Image.Bounds())
		if act, err := d.Active(ctx); err == nil && act != 0 {
			c, err := d.Capture(ctx, act)
			t.Logf("active %s: %d,%d %dx%d (err %v)", FormatID(act), c.X, c.Y, c.W, c.H, err)
		}
	}
}

// TestNativeStopKey grabs Ctrl+Alt+Esc on $DISPLAY and presses it from a second client
// (XTEST), as the user would: the grab must see it, and only it. Only for a throwaway headless
// server (Xvfb, a private Xwayland): JARVIS_X11_STOPKEY=1.
func TestNativeStopKey(t *testing.T) {
	if os.Getenv("JARVIS_X11_STOPKEY") != "1" {
		t.Skip("JARVIS_X11_STOPKEY=1 grabs and presses Ctrl+Alt+Esc (a throwaway X server only)")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &Display{}
	pressed := make(chan struct{}, 4)
	if err := d.GrabStopKey(ctx, func() { pressed <- struct{}{} }); err != nil {
		t.Fatal(err)
	}
	user := &Display{}
	if err := user.Keys(ctx, []string{"escape"}); err != nil { // Escape alone: not ours
		t.Fatal(err)
	}
	if err := user.Keys(ctx, []string{"ctrl", "alt", "escape"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pressed:
	case <-time.After(5 * time.Second):
		t.Fatal("the grab didn't see Ctrl+Alt+Esc")
	}
	select {
	case <-pressed:
		t.Fatal("a plain Escape (or the release) counted as the stop hotkey")
	case <-time.After(300 * time.Millisecond):
	}
	// A second grab of the same keys (another program) is refused while ours holds them.
	if err := (&Display{}).GrabStopKey(ctx, func() {}); err == nil || !strings.Contains(err.Error(), "another program") {
		t.Fatalf("a second grab: %v, want refused", err)
	}
}
