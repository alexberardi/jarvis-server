//go:build darwin

package tray

import (
	"errors"
	"fmt"
	"runtime"
	"structs"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// The icon on macOS is an NSStatusItem in the menu bar: the shield as a template image (macOS
// tints it for light and dark menu bars) with an NSMenu. AppKit runs only on the main thread,
// which the island's event loop keeps (island.RunWith / RunLoop): every AppKit call here goes
// through onMain, and a menu click is handed to a goroutine at once, so the main thread never
// waits on the agent. purego throughout: the build stays CGO_ENABLED=0.

type nsSize struct {
	_    structs.HostLayout
	W, H float64
}

const (
	variableStatusItemLength = -1.0 // NSVariableStatusItemLength
	menuBarHeight            = 18.0 // points: the template's height
	templateScale            = 2    // pixels per point in the template (Retina)
)

var (
	macOnce sync.Once
	macErr  error

	pthreadMainNP func() int32
	dispatchSyncF func(queue, ctx, work uintptr)
	mainQueue     uintptr
	callbackPtr   uintptr

	jobs   sync.Map // id → func()
	nextID atomic.Uintptr

	targetOnce  sync.Once
	targetClass objc.Class
	targetErr   error
	selAction   = objc.RegisterName("menuAction:")
	targets     sync.Map // the target object (as uintptr) → *macItem
)

func loadMac() error {
	macOnce.Do(func() {
		for _, fw := range []string{"Foundation", "AppKit"} {
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
		purego.RegisterLibFunc(&pthreadMainNP, sys, "pthread_main_np")
		purego.RegisterLibFunc(&dispatchSyncF, sys, "dispatch_sync_f")
		if mainQueue, err = purego.Dlsym(sys, "_dispatch_main_q"); err != nil {
			macErr = err
			return
		}
		callbackPtr = purego.NewCallback(func(id uintptr) uintptr {
			if f, ok := jobs.LoadAndDelete(id); ok {
				f.(func())()
			}
			return 0
		})
	})
	return macErr
}

// onMain runs f on the main thread and waits for it. The main thread must be running the event
// loop (or be the caller).
func onMain(f func()) {
	if pthreadMainNP() != 0 {
		f()
		return
	}
	id := nextID.Add(1)
	jobs.Store(id, f)
	dispatchSyncF(mainQueue, id, callbackPtr)
}

func class(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

func send(id objc.ID, sel string, args ...any) objc.ID {
	return id.Send(objc.RegisterName(sel), args...)
}

// nsString is an autoreleased NSString with s's text.
func nsString(s string) objc.ID {
	b := append([]byte(s), 0)
	str := objc.Send[objc.ID](class("NSString"), objc.RegisterName("stringWithUTF8String:"), unsafe.Pointer(&b[0]))
	runtime.KeepAlive(b)
	return str
}

// menuTarget makes the class whose menuAction: receives every item's click.
func menuTarget() (objc.Class, error) {
	targetOnce.Do(func() {
		targetClass, targetErr = objc.RegisterClass("JarvisAgentTrayTarget", objc.GetClass("NSObject"), nil, nil,
			[]objc.MethodDef{{Cmd: selAction, Fn: menuAction}})
	})
	return targetClass, targetErr
}

// menuAction runs on the main thread (AppKit's menu tracking): it only hands the click on.
func menuAction(self objc.ID, _ objc.SEL, sender objc.ID) {
	m, ok := targets.Load(uintptr(self))
	if !ok || sender == 0 {
		return
	}
	tag := objc.Send[int64](sender, objc.RegisterName("tag"))
	go m.(*macItem).tray.clicked(int32(tag))
}

// macItem is the backend on macOS.
type macItem struct {
	tray               *Tray
	item, menu, target objc.ID
	button             objc.ID
}

// Start puts the icon in the menu bar. It needs the island's AppKit loop (o.MainLoop).
func Start(o Options) (*Tray, error) {
	if !o.MainLoop {
		return nil, fmt.Errorf("%w: AppKit's event loop isn't running", ErrUnsupported)
	}
	if err := loadMac(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	cls, err := menuTarget()
	if err != nil {
		return nil, err
	}
	t := newTray(o.Log, o.Actions, o.State)
	m := &macItem{tray: t}
	onMain(func() { err = m.create(cls) })
	if err != nil {
		return nil, err
	}
	if err := t.attach(m); err != nil {
		m.close()
		return nil, err
	}
	return t, nil
}

func (m *macItem) create(cls objc.Class) error {
	bar := send(class("NSStatusBar"), "systemStatusBar")
	if bar == 0 {
		return errors.New("no NSStatusBar")
	}
	item := objc.Send[objc.ID](bar, objc.RegisterName("statusItemWithLength:"), variableStatusItemLength)
	if item == 0 {
		return errors.New("no status item")
	}
	item.Send(objc.RegisterName("retain"))
	m.item = item
	m.button = send(item, "button")

	tpl := Template(int(menuBarHeight) * templateScale)
	data := PNG(tpl)
	nsData := objc.Send[objc.ID](class("NSData"), objc.RegisterName("dataWithBytes:length:"), unsafe.Pointer(&data[0]), uint64(len(data)))
	runtime.KeepAlive(data)
	img := send(send(class("NSImage"), "alloc"), "initWithData:", nsData)
	if img != 0 {
		objc.Send[objc.ID](img, objc.RegisterName("setSize:"),
			nsSize{W: float64(tpl.Bounds().Dx()) / templateScale, H: menuBarHeight})
		send(img, "setTemplate:", true)
		if m.button != 0 {
			send(m.button, "setImage:", img)
		}
		img.Send(objc.RegisterName("release"))
	}
	if m.button != 0 {
		send(m.button, "setAccessibilityLabel:", nsString("Jarvis Automation"))
	}
	send(item, "setAutosaveName:", nsString("net.jarvisautomation.agent.status"))

	m.menu = send(send(class("NSMenu"), "alloc"), "init")
	send(m.menu, "setAutoenablesItems:", false)
	send(item, "setMenu:", m.menu)
	m.target = objc.ID(cls).Send(objc.RegisterName("alloc")).Send(objc.RegisterName("init"))
	targets.Store(uintptr(m.target), m)
	return nil
}

func (m *macItem) update(items []Item, tooltip string) error {
	onMain(func() {
		if m.menu == 0 {
			return
		}
		send(m.menu, "removeAllItems")
		for _, it := range items {
			if it.Separator {
				send(m.menu, "addItem:", send(class("NSMenuItem"), "separatorItem"))
				continue
			}
			var action objc.SEL
			if it.Enabled {
				action = selAction
			}
			mi := objc.Send[objc.ID](send(class("NSMenuItem"), "alloc"),
				objc.RegisterName("initWithTitle:action:keyEquivalent:"), nsString(it.Label), action, nsString(""))
			send(mi, "setTarget:", m.target)
			send(mi, "setTag:", int64(it.ID))
			send(mi, "setEnabled:", it.Enabled)
			if it.Toggle {
				state := int64(0) // NSControlStateValueOff
				if it.Checked {
					state = 1 // NSControlStateValueOn
				}
				send(mi, "setState:", state)
			}
			send(m.menu, "addItem:", mi)
			mi.Send(objc.RegisterName("release"))
		}
		if m.button != 0 {
			send(m.button, "setToolTip:", nsString(tooltip))
		}
	})
	return nil
}

func (m *macItem) close() {
	onMain(func() {
		if m.item != 0 {
			send(send(class("NSStatusBar"), "systemStatusBar"), "removeStatusItem:", m.item)
			m.item.Send(objc.RegisterName("release"))
			m.item = 0
		}
		if m.menu != 0 {
			m.menu.Send(objc.RegisterName("release"))
			m.menu = 0
		}
		if m.target != 0 {
			targets.Delete(uintptr(m.target))
			m.target.Send(objc.RegisterName("release"))
			m.target = 0
		}
	})
}

// Probe says, for `status`, where the icon shows.
func Probe() string { return "on (the menu bar)" }
