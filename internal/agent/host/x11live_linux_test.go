//go:build linux

package host

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/alexberardi/jarvis-server/internal/agent/x11"
)

// TestX11ArrangingLive drives every X11 window action through the real backend against windows
// it creates itself. Opt in with JARVIS_X11_ARRANGE=1, and only on a throwaway X server with an
// EWMH window manager and nothing else on it:
//
//	Xvfb :99 -screen 0 1920x1080x24 & DISPLAY=:99 openbox &
//	env -i PATH="$PATH" HOME="$HOME" DISPLAY=:99 JARVIS_X11_ARRANGE=1 go test -run X11ArrangingLive -v ./internal/agent/host/
func TestX11ArrangingLive(t *testing.T) {
	if os.Getenv("JARVIS_X11_ARRANGE") != "1" {
		t.Skip("set JARVIS_X11_ARRANGE=1 on a throwaway X server")
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		t.Fatal("WAYLAND_DISPLAY is set: run under env -i against the throwaway X server only")
	}
	ctx := context.Background()
	conn, err := xgb.NewConn()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	a, b := liveWindow(t, conn, "jarvis-a"), liveWindow(t, conn, "jarvis-b")
	h := New(&Linux{Sys: OSSys{}, X: &x11.Display{}})
	d := &x11.Display{}
	waitFor(t, "both windows managed", func() bool {
		ws, _ := h.ListWindows(ctx)
		return len(ws) >= 2
	})
	caps, _ := h.Capabilities(ctx)
	t.Logf("capabilities: %v", caps)
	id := x11.FormatID(a)
	state := func() x11.Window {
		ws, _ := d.Windows(ctx)
		for _, w := range ws {
			if w.ID == a {
				return w
			}
		}
		t.Fatal("window gone")
		return x11.Window{}
	}
	geom := func() (int, int, int, int) {
		g, err := xproto.GetGeometry(conn, xproto.Drawable(a)).Reply()
		if err != nil {
			t.Fatal(err)
		}
		tr, err := xproto.TranslateCoordinates(conn, xproto.Window(a), xproto.Setup(conn).DefaultScreen(conn).Root, 0, 0).Reply()
		if err != nil {
			t.Fatal(err)
		}
		return int(tr.DstX), int(tr.DstY), int(g.Width), int(g.Height)
	}
	do := func(act WindowAction, target string) {
		t.Helper()
		var err error
		if NeedsTarget(act) {
			err = h.WindowMove(ctx, act, id, target)
		} else {
			err = h.WindowDo(ctx, act, id)
		}
		if err != nil {
			t.Fatalf("%s: %v", act, err)
		}
	}

	do(ActionFocus, "")
	waitFor(t, "a focused", func() bool { return state().Active })
	ws, _ := h.ListWindows(ctx)
	if ws[0].ID != id || !ws[0].Focused {
		t.Fatalf("newest first with focus: %+v", ws)
	}
	do(ActionMaximize, "")
	waitFor(t, "maximized", func() bool { return state().Maximized })
	do(ActionFullscreen, "")
	waitFor(t, "fullscreen", func() bool { return state().Fullscreen })
	do(ActionUnmaximize, "")
	waitFor(t, "normal again", func() bool { s := state(); return !s.Fullscreen && !s.Maximized })
	do(ActionSnapLeft, "")
	waitFor(t, "left half", func() bool { x, _, w, _ := geom(); return x < 50 && w > 800 && w < 1000 })
	do(ActionSnapRight, "")
	waitFor(t, "right half", func() bool { x, _, w, _ := geom(); return x >= 900 && w > 800 && w < 1000 })
	do(ActionMinimize, "")
	waitFor(t, "minimized", func() bool { return state().Minimized })
	do(ActionUnminimize, "")
	waitFor(t, "back", func() bool { return !state().Minimized })
	do(ActionMoveToWorkspace, "3")
	waitFor(t, "on desktop 3", func() bool {
		v, err := xproto.GetProperty(conn, false, xproto.Window(a), atom(t, conn, "_NET_WM_DESKTOP"), xproto.AtomCardinal, 0, 1).Reply()
		return err == nil && len(v.Value) == 4 && xgb.Get32(v.Value) == 2
	})
	do(ActionClose, "")
	waitFor(t, "closed", func() bool {
		ws, _ := h.ListWindows(ctx)
		for _, w := range ws {
			if w.ID == id {
				return false
			}
		}
		return true
	})
	_ = b
}

func liveWindow(t *testing.T, c *xgb.Conn, name string) uint32 {
	t.Helper()
	s := xproto.Setup(c).DefaultScreen(c)
	wid, err := xproto.NewWindowId(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := xproto.CreateWindowChecked(c, s.RootDepth, wid, s.Root, 100, 100, 640, 480, 0,
		xproto.WindowClassInputOutput, s.RootVisual, xproto.CwBackPixel, []uint32{s.WhitePixel}).Check(); err != nil {
		t.Fatal(err)
	}
	set := func(prop, typ xproto.Atom, v string) {
		xproto.ChangeProperty(c, xproto.PropModeReplace, wid, prop, typ, 8, uint32(len(v)), []byte(v))
	}
	set(xproto.AtomWmName, xproto.AtomString, name)
	set(xproto.AtomWmClass, xproto.AtomString, name+"\x00Jarvis\x00")
	// Take WM_DELETE_WINDOW, and close when asked: a graceful close needs a window that answers.
	del, proto := atom(t, c, "WM_DELETE_WINDOW"), atom(t, c, "WM_PROTOCOLS")
	xproto.ChangeProperty(c, xproto.PropModeReplace, wid, proto, xproto.AtomAtom, 32, 1, u32le(uint32(del)))
	xproto.MapWindow(c, wid)
	go func() {
		for {
			ev, err := c.WaitForEvent()
			if ev == nil && err == nil {
				return
			}
			if cm, ok := ev.(xproto.ClientMessageEvent); ok && cm.Window == wid && cm.Data.Data32[0] == uint32(del) {
				xproto.DestroyWindow(c, wid)
			}
		}
	}()
	return uint32(wid)
}

func u32le(v uint32) []byte { return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)} }

func atom(t *testing.T, c *xgb.Conn, name string) xproto.Atom {
	r, err := xproto.InternAtom(c, false, uint16(len(name)), name).Reply()
	if err != nil {
		t.Fatal(err)
	}
	return r.Atom
}

// waitFor polls a condition on a real window manager, which applies requests asynchronously. It
// is the only waiting in host's tests, and it runs only in this opt-in live test.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for range 50 {
		if ok() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}
