//go:build linux

package vkbd

import (
	"context"
	"os"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

// TestNativeType types JARVIS_VKBD_TYPE into whatever has focus in this session's compositor.
// Only for a nested, headless compositor (docs/agent/DESIGN.md §14 "Evidence"): never run it
// in a desktop someone is using.
func TestNativeType(t *testing.T) {
	text := os.Getenv("JARVIS_VKBD_TYPE")
	if text == "" {
		t.Skip("JARVIS_VKBD_TYPE=<text> types into the focused window (nested compositors only)")
	}
	k := &Keyboard{}
	if ok, why := k.Available(context.Background()); !ok {
		t.Fatal(why)
	}
	strokes, ok := xkb.Strokes(text)
	if !ok {
		t.Fatal("no keysym")
	}
	if os.Getenv("JARVIS_VKBD_KEYS") != "" {
		strokes = append(strokes,
			xkb.Stroke{Sym: xkb.KeyReturn}, xkb.Stroke{Sym: xkb.KeyEscape}, xkb.Stroke{Sym: xkb.KeyTab},
			xkb.Stroke{Sym: xkb.KeyTab, Shift: true}, xkb.Stroke{Sym: xkb.KeySpace}, xkb.Stroke{Sym: xkb.KeyUp},
			xkb.Stroke{Sym: xkb.KeyDown}, xkb.Stroke{Sym: xkb.KeyLeft}, xkb.Stroke{Sym: xkb.KeyRight})
	}
	if os.Getenv("JARVIS_VKBD_NOSETTLE") != "" {
		k.Settle = -1
	}
	if err := k.Send(context.Background(), strokes); err != nil {
		t.Fatal(err)
	}
}
