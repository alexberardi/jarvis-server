//go:build darwin

package tray

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"runtime"
	"structs"
	"testing"
	"time"

	"github.com/ebitengine/purego/objc"

	"github.com/alexberardi/jarvis-server/internal/agent/island"
)

// TestMacStatusItemLive puts the icon in the real menu bar, checks the menu AppKit built, and
// clicks the toggle and Quit through AppKit's own action path (performActionForItemAtIndex:).
// Opt-in, and it must run in the user's GUI session (a LaunchAgent job), not over SSH:
//
//	JARVIS_TRAY_MAC_LIVE=1 ./tray.test -test.run MacStatusItemLive -test.v

func init() { runtime.LockOSThread() } // AppKit's loop needs the main thread

var liveLoop bool

func TestMain(m *testing.M) {
	if os.Getenv("JARVIS_TRAY_MAC_LIVE") == "" {
		os.Exit(m.Run())
	}
	code := 0
	_ = island.RunLoop(context.Background(), slog.New(slog.NewTextHandler(os.Stderr, nil)), island.AppEvents{}, func(loop bool) error {
		liveLoop = loop
		code = m.Run()
		return nil
	})
	os.Exit(code)
}

func TestMacNeedsTheMainLoop(t *testing.T) {
	if _, err := Start(Options{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("without the AppKit loop: %v", err)
	}
}

func TestMacStatusItemLive(t *testing.T) {
	if os.Getenv("JARVIS_TRAY_MAC_LIVE") == "" {
		t.Skip("JARVIS_TRAY_MAC_LIVE=1 to show the icon in the menu bar")
	}
	if !liveLoop {
		t.Fatal("no AppKit loop (see the log)")
	}
	voice := make(chan bool, 2)
	quit := make(chan struct{}, 1)
	tr, err := Start(Options{MainLoop: true, Log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		State:   State{Status: "Running", VoiceAvailable: true, Voice: true},
		Actions: Actions{SetVoice: func(on bool) { voice <- on }, Quit: func() { quit <- struct{}{} }}})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	m := tr.be.(*macItem)
	type mi struct {
		title   string
		enabled bool
		state   int64
	}
	read := func() []mi {
		var out []mi
		onMain(func() {
			n := objc.Send[int64](m.menu, objc.RegisterName("numberOfItems"))
			for i := int64(0); i < n; i++ {
				it := objc.Send[objc.ID](m.menu, objc.RegisterName("itemAtIndex:"), i)
				title := objc.Send[string](send(it, "title"), objc.RegisterName("UTF8String"))
				out = append(out, mi{title, objc.Send[bool](it, objc.RegisterName("isEnabled")),
					objc.Send[int64](it, objc.RegisterName("state"))})
			}
		})
		return out
	}
	items := read()
	t.Logf("menu: %+v", items)
	if len(items) != 5 || items[0].title != "Jarvis Automation Agent — Running" || items[0].enabled ||
		items[2].title != `Listen for "Hey Jarvis"` || items[2].state != 1 || items[4].title != "Quit Jarvis Automation Agent" {
		t.Fatalf("menu %+v", items)
	}
	var visible bool
	onMain(func() { visible = objc.Send[bool](m.item, objc.RegisterName("isVisible")) })
	if !visible {
		t.Fatal("status item not visible")
	}
	type rect struct {
		_          structs.HostLayout
		X, Y, W, H float64
	}
	var win int64
	var frame rect
	var winVisible bool
	onMain(func() {
		w := send(m.button, "window")
		if w != 0 {
			win = objc.Send[int64](w, objc.RegisterName("windowNumber"))
			frame = objc.Send[rect](w, objc.RegisterName("frame"))
			winVisible = objc.Send[bool](w, objc.RegisterName("isVisible"))
		}
	})
	t.Logf("status item window %d, frame %+v, visible %v, pid %d", win, frame, winVisible, os.Getpid())
	if os.Getenv("JARVIS_TRAY_MAC_HOLD") != "" {
		// Opt-in live run only: hold the icon up while a window-list probe looks. macOS 26
		// hosts status item windows in Control Center, so the probe finds a new Control Center
		// window at this item's x, not one of this process.
		time.Sleep(4 * time.Second)
	}
	click := func(i int64) {
		onMain(func() { send(m.menu, "performActionForItemAtIndex:", i) })
	}
	click(2)
	select {
	case on := <-voice:
		if on {
			t.Fatal("unticking asked for voice on")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the toggle's click didn't arrive")
	}
	tr.Update(func(s *State) { s.Voice = false })
	if items := read(); items[2].state != 0 {
		t.Fatalf("toggle still on: %+v", items)
	}
	click(4)
	select {
	case <-quit:
	case <-time.After(10 * time.Second):
		t.Fatal("Quit's click didn't arrive")
	}
}
