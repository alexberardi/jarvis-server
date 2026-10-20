//go:build linux

package portal

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

// TestFrontendGlobalShortcuts runs BindStopShortcut against the real xdg-desktop-portal
// frontend on a private dbus-daemon, with a fake GlobalShortcuts backend exported from this
// test: the wire (a(sa{sv}) both ways, the session handle, the Activated signal the frontend
// forwards) and the frontend's own checks of the shortcut. Opt-in, as TestFrontend.
func TestFrontendGlobalShortcuts(t *testing.T) {
	frontend, store := os.Getenv("JARVIS_PORTAL_FRONTEND"), os.Getenv("JARVIS_PORTAL_STORE")
	if frontend == "" || store == "" {
		t.Skip("JARVIS_PORTAL_FRONTEND=/usr/lib/xdg-desktop-portal JARVIS_PORTAL_STORE=/usr/lib/xdg-permission-store run the real portal frontend on a private bus")
	}
	dir, err := os.MkdirTemp("", "pt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	addr := startBus(t, dir)

	bc, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bc.Close() })
	back := &fakeGS{conn: bc}
	const impl = "org.freedesktop.impl.portal.GlobalShortcuts"
	if err := bc.Export(back, "/org/freedesktop/portal/desktop", impl); err != nil {
		t.Fatal(err)
	}
	if _, err := prop.Export(bc, "/org/freedesktop/portal/desktop", prop.Map{impl: {"version": {Value: uint32(1)}}}); err != nil {
		t.Fatal(err)
	}
	if r, err := bc.RequestName("org.freedesktop.impl.portal.desktop.jarvistest", dbus.NameFlagDoNotQueue); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatal(r, err)
	}
	runFrontend(t, dir, addr, bc, impl, frontend, store)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pressed := make(chan struct{}, 1)
	label, err := BindStopShortcut(ctx, func() (ShortcutsBus, error) {
		c, err := dbus.Connect(addr)
		if err != nil {
			return nil, err
		}
		return newDbusBus(c), nil
	}, func() { pressed <- struct{}{} })
	if err != nil {
		t.Fatalf("bind: %v (backend saw %q)", err, back.events())
	}
	t.Logf("label %q; backend saw %q", label, back.events())
	if label != "Ctrl+Alt+Escape" {
		t.Fatalf("label %q, want the backend's trigger_description", label)
	}
	want := `bind stop {description: "Stop what Jarvis is doing on the computer", preferred_trigger: "CTRL+ALT+Escape"}`
	if ev := back.events(); len(ev) != 2 || ev[0] != `create app="`+AppID+`"` || ev[1] != want {
		t.Fatalf("backend saw %q, want create (app %s) then %q", ev, AppID, want)
	}
	back.activate(t, "other")
	back.activate(t, StopShortcutID)
	select {
	case <-pressed:
	case <-time.After(10 * time.Second):
		t.Fatal("the forwarded Activated didn't call on")
	}
	select {
	case <-pressed:
		t.Fatal("another shortcut's Activated called on")
	case <-time.After(200 * time.Millisecond):
	}
}

// fakeGS is org.freedesktop.impl.portal.GlobalShortcuts.
type fakeGS struct {
	conn *dbus.Conn

	mu      sync.Mutex
	log     []string
	session dbus.ObjectPath
}

func (f *fakeGS) note(s string) { f.mu.Lock(); f.log = append(f.log, s); f.mu.Unlock() }
func (f *fakeGS) events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

func (f *fakeGS) CreateSession(handle, session dbus.ObjectPath, app string, opts map[string]dbus.Variant) (uint32, map[string]dbus.Variant, *dbus.Error) {
	f.mu.Lock()
	f.session = session
	f.mu.Unlock()
	f.note(fmt.Sprintf("create app=%q", app))
	return 0, map[string]dbus.Variant{"session_handle": dbus.MakeVariant(string(session))}, nil
}

type gsShortcut struct {
	ID   string
	Opts map[string]dbus.Variant
}

func (f *fakeGS) BindShortcuts(handle, session dbus.ObjectPath, shortcuts []gsShortcut, parent string, opts map[string]dbus.Variant) (uint32, map[string]dbus.Variant, *dbus.Error) {
	var out []gsShortcut
	for _, s := range shortcuts {
		f.note(fmt.Sprintf("bind %s {description: %q, preferred_trigger: %q}", s.ID,
			variantValue(s.Opts, "description"), variantValue(s.Opts, "preferred_trigger")))
		out = append(out, gsShortcut{s.ID, map[string]dbus.Variant{
			"description":         s.Opts["description"],
			"trigger_description": dbus.MakeVariant("Ctrl+Alt+Escape"),
		}})
	}
	return 0, map[string]dbus.Variant{"shortcuts": dbus.MakeVariant(out)}, nil
}

func (f *fakeGS) ListShortcuts(handle, session dbus.ObjectPath) (uint32, map[string]dbus.Variant, *dbus.Error) {
	return 0, map[string]dbus.Variant{}, nil
}

// activate emits Activated for id on the session, as a desktop does on the key press.
func (f *fakeGS) activate(t *testing.T, id string) {
	f.mu.Lock()
	s := f.session
	f.mu.Unlock()
	if err := f.conn.Emit("/org/freedesktop/portal/desktop", "org.freedesktop.impl.portal.GlobalShortcuts.Activated",
		s, id, uint64(time.Now().UnixMilli()), map[string]dbus.Variant{}); err != nil {
		t.Fatal(err)
	}
}
