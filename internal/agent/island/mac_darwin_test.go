//go:build darwin

package island

import (
	"context"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"structs"
	"testing"
	"time"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// TestMacLiveSnapshot shows each state on the real screen and saves what the window server
// composited for the island's own window (a process may capture its own windows without the
// Screen Recording grant). Opt-in, and it must run in the user's GUI session (a LaunchAgent
// job), not over SSH:
//
//	JARVIS_ISLAND_MAC_LIVE=/tmp/island-shots ./island.test -test.run MacLive -test.v

func init() { runtime.LockOSThread() } // Run needs the main thread

var liveIsland *Island

func TestMain(m *testing.M) {
	if os.Getenv("JARVIS_ISLAND_MAC_LIVE") == "" {
		os.Exit(m.Run())
	}
	code := 0
	_ = Run(context.Background(), slog.New(slog.NewTextHandler(os.Stderr, nil)), func(i *Island) error {
		liveIsland = i
		code = m.Run()
		return nil
	})
	os.Exit(code)
}

type cgRect struct {
	_          structs.HostLayout
	X, Y, W, H float64
}

func TestMacLiveSnapshot(t *testing.T) {
	dir := os.Getenv("JARVIS_ISLAND_MAC_LIVE")
	if dir == "" {
		t.Skip("JARVIS_ISLAND_MAC_LIVE=DIR to show the island and save snapshots")
	}
	if liveIsland == nil {
		t.Fatal("no island (see the log)")
	}
	cg, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		t.Fatal(err)
	}
	io_, err := purego.Dlopen("/System/Library/Frameworks/ImageIO.framework/ImageIO", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		t.Fatal(err)
	}
	cf, _ := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	var (
		createImage func(r cgRect, opt uint32, win uint32, imgOpt uint32) uintptr
		urlFromPath func(alloc uintptr, path string, n int, dir bool) uintptr
		cfStr       func(alloc uintptr, s string, enc uint32) uintptr
		destCreate  func(url, typ uintptr, n int, opts uintptr) uintptr
		destAdd     func(dest, img, props uintptr)
		destFinal   func(dest uintptr) bool
	)
	purego.RegisterLibFunc(&createImage, cg, "CGWindowListCreateImage")
	purego.RegisterLibFunc(&urlFromPath, cf, "CFURLCreateFromFileSystemRepresentation")
	purego.RegisterLibFunc(&cfStr, cf, "CFStringCreateWithCString")
	purego.RegisterLibFunc(&destCreate, io_, "CGImageDestinationCreateWithURL")
	purego.RegisterLibFunc(&destAdd, io_, "CGImageDestinationAddImage")
	purego.RegisterLibFunc(&destFinal, io_, "CGImageDestinationFinalize")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := liveIsland.be.(*macPanel)
	for _, st := range []struct {
		s      State
		window time.Duration
	}{{Listening, 0}, {Thinking, 0}, {Speaking, 0}, {FollowUp, 4 * time.Second}, {Error, 0}} {
		liveIsland.Set(st.s, st.window)
		for k := 0; k < 10; k++ {
			liveIsland.Level(float32(0.05 + 0.15*math.Abs(math.Sin(float64(k)))))
			time.Sleep(60 * time.Millisecond)
		}
		var num int64
		onMain(func() { num = objc.Send[int64](p.panel, objc.RegisterName("windowNumber")) })
		null := cgRect{X: math.Inf(1), Y: math.Inf(1)}
		// kCGWindowListOptionIncludingWindow; kCGWindowImageBoundsIgnoreFraming | BestResolution
		img := createImage(null, 1<<3, uint32(num), 1|1<<3)
		if img == 0 {
			t.Fatalf("%s: CGWindowListCreateImage returned nothing (window %d)", st.s, num)
		}
		path := filepath.Join(dir, st.s.String()+".png")
		url := urlFromPath(0, path, len(path), false)
		dest := destCreate(url, cfStr(0, "public.png", 0x08000100), 1, 0)
		destAdd(dest, img, 0)
		if !destFinal(dest) {
			t.Fatalf("%s: writing %s failed", st.s, path)
		}
		t.Logf("%s: window %d → %s", st.s, num, path)
	}
	liveIsland.Set(Hidden, 0)
	time.Sleep(400 * time.Millisecond)
}
