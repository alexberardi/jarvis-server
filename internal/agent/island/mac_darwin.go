//go:build darwin

package island

import (
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"math"
	"structs"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// The island on macOS: a borderless, non-activating NSPanel just under the menu bar (the
// notch, on a MacBook), click-through, on every Space and over full-screen apps, with no Dock
// icon (the bundle is LSUIElement, and the app is an accessory). Its content is a CALayer
// whose contents are each frame as a CGImage. AppKit only works on the main thread, so Run
// keeps the main thread for an event loop and runs the agent on other goroutines; every
// AppKit call goes through onMain. purego throughout: the build stays CGO_ENABLED=0.

type nsPoint struct {
	_    structs.HostLayout
	X, Y float64
}

type nsSize struct {
	_    structs.HostLayout
	W, H float64
}

type nsRect struct {
	_      structs.HostLayout
	Origin nsPoint
	Size   nsSize
}

const (
	activationPolicyAccessory = 1
	styleNonactivatingPanel   = 1 << 7 // borderless is 0
	backingBuffered           = 2
	statusWindowLevel         = 25
	// canJoinAllSpaces | stationary | ignoresCycle | fullScreenAuxiliary
	collectionBehavior          = 1<<0 | 1<<4 | 1<<6 | 1<<8
	animationBehaviorNone       = 2
	bitmapPremultipliedLastBE32 = 1 | 4<<12 // kCGImageAlphaPremultipliedLast | kCGBitmapByteOrder32Big
)

var (
	macOnce sync.Once
	macErr  error

	pthreadMainNP   func() int32
	dispatchSyncF   func(queue, ctx, work uintptr)
	mainQueue       uintptr
	callbackPtr     uintptr
	autoreleasePush func() uintptr
	autoreleasePop  func(uintptr)
	defaultMode     uintptr

	cgColorSpaceCreateDeviceRGB  func() uintptr
	cfDataCreate                 func(alloc uintptr, bytes unsafe.Pointer, n int) uintptr
	cgDataProviderCreateWithData func(data uintptr) uintptr
	cgImageCreate                func(w, h, bpc, bpp, bpr int, cs uintptr, info uint32, provider uintptr, decode uintptr, interpolate bool, intent int32) uintptr
	cfRelease                    func(uintptr)

	jobs   sync.Map // id → func()
	nextID atomic.Uintptr
)

func loadMac() error {
	macOnce.Do(func() {
		for _, fw := range []string{"Foundation", "AppKit", "QuartzCore", "CoreGraphics"} {
			if _, err := purego.Dlopen("/System/Library/Frameworks/"+fw+".framework/"+fw, purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
				macErr = fmt.Errorf("load %s: %w", fw, err)
				return
			}
		}
		sys, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_GLOBAL|purego.RTLD_NOW)
		if err != nil {
			macErr = err
			return
		}
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_GLOBAL|purego.RTLD_NOW)
		if err != nil {
			macErr = err
			return
		}
		cg, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_GLOBAL|purego.RTLD_NOW)
		if err != nil {
			macErr = err
			return
		}
		lobjc, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_GLOBAL|purego.RTLD_NOW)
		if err != nil {
			macErr = err
			return
		}
		purego.RegisterLibFunc(&pthreadMainNP, sys, "pthread_main_np")
		purego.RegisterLibFunc(&dispatchSyncF, sys, "dispatch_sync_f")
		if mainQueue, err = purego.Dlsym(sys, "_dispatch_main_q"); err != nil {
			macErr = err
			return
		}
		purego.RegisterLibFunc(&autoreleasePush, lobjc, "objc_autoreleasePoolPush")
		purego.RegisterLibFunc(&autoreleasePop, lobjc, "objc_autoreleasePoolPop")
		purego.RegisterLibFunc(&cgColorSpaceCreateDeviceRGB, cg, "CGColorSpaceCreateDeviceRGB")
		purego.RegisterLibFunc(&cgDataProviderCreateWithData, cg, "CGDataProviderCreateWithCFData")
		purego.RegisterLibFunc(&cgImageCreate, cg, "CGImageCreate")
		purego.RegisterLibFunc(&cfDataCreate, cf, "CFDataCreate")
		purego.RegisterLibFunc(&cfRelease, cf, "CFRelease")
		sym, err := purego.Dlsym(purego.RTLD_DEFAULT, "NSDefaultRunLoopMode")
		if err != nil {
			macErr = err
			return
		}
		defaultMode = *(*uintptr)(unsafe.Add(unsafe.Pointer(nil), sym)) // a C global holding an NSString*
		callbackPtr = purego.NewCallback(func(id uintptr) uintptr {
			if f, ok := jobs.LoadAndDelete(id); ok {
				f.(func())()
			}
			return 0
		})
	})
	return macErr
}

// onMain runs f on the main thread and waits for it.
func onMain(f func()) {
	if pthreadMainNP() != 0 {
		f()
		return
	}
	id := nextID.Add(1)
	jobs.Store(id, f)
	dispatchSyncF(mainQueue, id, callbackPtr)
}

var (
	selAlloc, selInit, selRelease = objc.RegisterName("alloc"), objc.RegisterName("init"), objc.RegisterName("release")
)

func class(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

func send(id objc.ID, sel string, args ...any) objc.ID {
	return id.Send(objc.RegisterName(sel), args...)
}

// Run runs fn on another goroutine while the main thread runs AppKit's event loop, with the
// island on screen; or runs fn plainly (island nil, logged once) when it can't. It must be
// called on the main goroutine of a program that locked it to the main thread
// (runtime.LockOSThread in an init).
func Run(ctx context.Context, log *slog.Logger, fn func(*Island) error) error {
	if err := loadMac(); err != nil {
		logOff(log, err)
		return fn(nil)
	}
	if pthreadMainNP() == 0 {
		logOff(log, "not on the main thread (call runtime.LockOSThread in an init)")
		return fn(nil)
	}
	app := send(class("NSApplication"), "sharedApplication")
	send(app, "setActivationPolicy:", activationPolicyAccessory)
	send(app, "finishLaunching")

	i := newIsland(&macPanel{}, log)
	var done atomic.Bool
	res := make(chan error, 1)
	go func() {
		err := fn(i)
		i.Close() // its last AppKit calls need the loop below still running
		done.Store(true)
		res <- err
	}()
	date := class("NSDate")
	for !done.Load() {
		pool := autoreleasePush()
		until := objc.Send[objc.ID](date, objc.RegisterName("dateWithTimeIntervalSinceNow:"), 0.25)
		ev := objc.Send[objc.ID](app, objc.RegisterName("nextEventMatchingMask:untilDate:inMode:dequeue:"),
			uint64(math.MaxUint64), until, objc.ID(defaultMode), true)
		if ev != 0 {
			send(app, "sendEvent:", ev)
		}
		autoreleasePop(pool)
	}
	return <-res
}

// macPanel is the island's backend on macOS.
type macPanel struct {
	panel, layer objc.ID
	colorSpace   uintptr
}

func (p *macPanel) create() {
	rect := nsRect{Size: nsSize{W: CanvasW, H: CanvasH}}
	panel := objc.ID(objc.GetClass("NSPanel")).Send(selAlloc)
	panel = objc.Send[objc.ID](panel, objc.RegisterName("initWithContentRect:styleMask:backing:defer:"),
		rect, uint64(styleNonactivatingPanel), uint64(backingBuffered), false)
	send(panel, "setReleasedWhenClosed:", false)
	send(panel, "setOpaque:", false)
	send(panel, "setBackgroundColor:", send(class("NSColor"), "clearColor"))
	send(panel, "setHasShadow:", false)
	send(panel, "setIgnoresMouseEvents:", true)
	send(panel, "setLevel:", int64(statusWindowLevel))
	send(panel, "setCollectionBehavior:", uint64(collectionBehavior))
	send(panel, "setHidesOnDeactivate:", false)
	send(panel, "setAnimationBehavior:", int64(animationBehaviorNone))

	view := send(panel, "contentView")
	send(view, "setWantsLayer:", true)
	layer := objc.ID(objc.GetClass("CALayer")).Send(selAlloc).Send(selInit)
	objc.Send[objc.ID](layer, objc.RegisterName("setFrame:"), rect)
	send(send(view, "layer"), "addSublayer:", layer)
	p.panel, p.layer = panel, layer
	p.colorSpace = cgColorSpaceCreateDeviceRGB()
}

func (p *macPanel) show() (scale float64, err error) {
	onMain(func() {
		if p.panel == 0 {
			p.create()
		}
		if p.panel == 0 || p.layer == 0 {
			err = errors.New("couldn't create the panel")
			return
		}
		screen := send(class("NSScreen"), "mainScreen")
		if screen == 0 {
			err = errors.New("no screen")
			return
		}
		frame := objc.Send[nsRect](screen, objc.RegisterName("frame"))
		vis := objc.Send[nsRect](screen, objc.RegisterName("visibleFrame"))
		scale = objc.Send[float64](screen, objc.RegisterName("backingScaleFactor"))
		if scale <= 0 {
			scale = 1
		}
		// Centred, its top edge on the menu bar's bottom edge: right under the notch.
		at := nsRect{Origin: nsPoint{X: math.Round(frame.Origin.X + (frame.Size.W-CanvasW)/2),
			Y: vis.Origin.Y + vis.Size.H - CanvasH}, Size: nsSize{W: CanvasW, H: CanvasH}}
		objc.Send[objc.ID](p.panel, objc.RegisterName("setFrame:display:"), at, false)
		objc.Send[objc.ID](p.layer, objc.RegisterName("setContentsScale:"), scale)
		send(p.panel, "orderFrontRegardless")
	})
	return scale, err
}

func (p *macPanel) present(img *image.RGBA) error {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	// A copy of the pixels: the image must not alias the buffer the next frame is drawn into.
	data := cfDataCreate(0, unsafe.Pointer(&img.Pix[0]), len(img.Pix))
	if data == 0 {
		return errors.New("CFDataCreate failed")
	}
	defer cfRelease(data)
	prov := cgDataProviderCreateWithData(data)
	if prov == 0 {
		return errors.New("CGDataProviderCreateWithCFData failed")
	}
	defer cfRelease(prov)
	im := cgImageCreate(w, h, 8, 32, img.Stride, p.colorSpace, bitmapPremultipliedLastBE32, prov, 0, false, 0)
	if im == 0 {
		return errors.New("CGImageCreate failed")
	}
	defer cfRelease(im)
	onMain(func() {
		tx := class("CATransaction")
		send(tx, "begin")
		send(tx, "setDisableActions:", true)
		send(p.layer, "setContents:", objc.ID(im))
		send(tx, "commit")
	})
	return nil
}

func (p *macPanel) hide() {
	onMain(func() {
		if p.panel != 0 {
			send(p.layer, "setContents:", objc.ID(0))
			send(p.panel, "orderOut:", objc.ID(0))
		}
	})
}

func (p *macPanel) close() {
	onMain(func() {
		if p.panel != 0 {
			send(p.panel, "orderOut:", objc.ID(0))
			send(p.panel, "close")
			p.layer.Send(selRelease)
			p.panel.Send(selRelease)
			p.panel, p.layer = 0, 0
		}
		if p.colorSpace != 0 {
			cfRelease(p.colorSpace)
			p.colorSpace = 0
		}
	})
}
