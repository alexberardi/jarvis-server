//go:build linux

package portal

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"

	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

// TestFrontend runs this client against the real xdg-desktop-portal frontend on a private
// dbus-daemon, with a fake RemoteDesktop backend exported from this test. It checks the D-Bus
// wire (Request paths and Responses, session handles, argument types), the frontend's own
// validation, and the restore-token round trip through the frontend's permission store. What
// it can't check is a real GNOME/KDE backend: their dialog and their key injection.
//
// Opt-in: JARVIS_PORTAL_FRONTEND=/usr/lib/xdg-desktop-portal
// JARVIS_PORTAL_STORE=/usr/lib/xdg-permission-store (where restore data lives). Never touches
// the user's session bus.
func TestFrontend(t *testing.T) {
	frontend, store := os.Getenv("JARVIS_PORTAL_FRONTEND"), os.Getenv("JARVIS_PORTAL_STORE")
	if frontend == "" || store == "" {
		t.Skip("JARVIS_PORTAL_FRONTEND=/usr/lib/xdg-desktop-portal JARVIS_PORTAL_STORE=/usr/lib/xdg-permission-store run the real portal frontend on a private bus")
	}
	dir, err := os.MkdirTemp("", "pt") // short: the bus socket path must fit sun_path
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	addr := startBus(t, dir)

	back := &fakeImpl{}
	bc, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bc.Close() })
	back.conn = bc
	if err := bc.Export(back, "/org/freedesktop/portal/desktop", "org.freedesktop.impl.portal.RemoteDesktop"); err != nil {
		t.Fatal(err)
	}
	if _, err := prop.Export(bc, "/org/freedesktop/portal/desktop", prop.Map{
		"org.freedesktop.impl.portal.RemoteDesktop": {
			"AvailableDeviceTypes": {Value: uint32(7)},
			"version":              {Value: uint32(2)},
		}}); err != nil {
		t.Fatal(err)
	}
	if r, err := bc.RequestName("org.freedesktop.impl.portal.desktop.jarvistest", dbus.NameFlagDoNotQueue); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatal(r, err)
	}

	runFrontend(t, dir, addr, bc, "org.freedesktop.impl.portal.RemoteDesktop", frontend, store)

	ctx := context.Background()
	rd := &RemoteDesktop{Dial: func() (Bus, error) { return dialBusAt(addr) },
		Tokens: &TokenStore{Path: filepath.Join(dir, "cfg", "token")}, Timeout: 20 * time.Second}
	if b, err := dialBusAt(addr); err != nil || b.(*dbusBus).registered != nil {
		t.Fatalf("registering as %s: %v %v", AppID, err, b.(*dbusBus).registered)
	} else {
		b.Close()
	}
	st := rd.Probe(ctx)
	t.Logf("probe before allow: %+v", st)
	if !st.Portal || st.Allowed {
		t.Fatalf("probe: %+v", st)
	}
	if err := rd.Allow(ctx); err != nil {
		t.Fatalf("allow: %v (backend saw %q)", err, back.events())
	}
	tok1, _ := rd.Tokens.Load()
	t.Logf("allow: token %d chars; backend saw %q", len(tok1), back.events())
	if ev := back.events(); len(ev) == 0 || ev[0] != `create app="`+AppID+`"` {
		t.Fatalf("the backend saw app %q, want %s", ev, AppID)
	}
	if tok1 == "" || back.dialogs() != 1 {
		t.Fatalf("token %q, dialogs %d", tok1, back.dialogs())
	}
	if st := rd.Probe(ctx); !st.Allowed {
		t.Fatalf("after allow: %+v", st)
	}

	strokes, _ := xkb.Strokes("hé日")
	strokes = append(strokes, xkb.Stroke{Sym: xkb.KeyReturn}, xkb.Stroke{Sym: xkb.KeyTab, Shift: true})
	if err := rd.Send(ctx, strokes); err != nil {
		t.Fatalf("send: %v (backend saw %q)", err, back.events())
	}
	tok2, _ := rd.Tokens.Load()
	t.Logf("send: backend saw %q", back.events())
	if back.dialogs() != 1 {
		t.Fatalf("typing showed the dialog again (%d): the restore token didn't restore", back.dialogs())
	}
	// The spec calls a restore token single use; xdg-desktop-portal 1.22 hands the same one
	// back for persist_mode 2. Either way the stored one must be what Start returned.
	if tok2 == "" {
		t.Fatal("lost the token")
	}
	t.Logf("token after typing: same as before = %v", tok2 == tok1)
	want := []string{"0x68 1", "0x68 0", "0xe9 1", "0xe9 0", "0x10065e5 1", "0x10065e5 0", "0xff0d 1", "0xff0d 0",
		"0xffe1 1", "0xff09 1", "0xff09 0", "0xffe1 0"}
	if got := back.keys(); !slices.Equal(got, want) {
		t.Fatalf("keys\n got %q\nwant %q", got, want)
	}
	if !back.sessionsClosed() {
		t.Fatalf("a session was left open: %q", back.events())
	}
	// A token the frontend doesn't know (revoked, or from another install): 1.22 refuses it in
	// SelectDevices without a dialog, and the agent forgets it.
	_ = rd.Tokens.Save("not-a-token-the-portal-issued")
	err = rd.Send(ctx, strokes[:1])
	t.Logf("unknown token: %v", err)
	if !errors.Is(err, ErrBadToken) || back.dialogs() != 1 || len(back.keys()) != len(want) {
		t.Fatalf("err %v, dialogs %d, keys %d", err, back.dialogs(), len(back.keys()))
	}
	if tok, _ := rd.Tokens.Load(); tok != "" {
		t.Fatal("kept a rejected token")
	}

	if agent := os.Getenv("JARVIS_PORTAL_AGENT"); agent != "" {
		agentEndToEnd(t, agent, addr, dir, back)
	}
}

// agentEndToEnd drives the jarvis-agent binary as on GNOME Wayland (no virtual keyboard: the
// WAYLAND_DISPLAY socket doesn't exist; no ydotool) against the same private bus: status before
// and after `dictation allow`, typing, a key, and the refusal to name an app.
func agentEndToEnd(t *testing.T, agent, addr, dir string, back *fakeImpl) {
	home := filepath.Join(dir, "agent")
	_ = os.MkdirAll(home, 0o700)
	env := []string{"DBUS_SESSION_BUS_ADDRESS=" + addr, "HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, "cfg"),
		"XDG_DATA_HOME=" + filepath.Join(dir, "data"),
		"XDG_RUNTIME_DIR=" + filepath.Join(dir, "run"), "WAYLAND_DISPLAY=wayland-none", "XDG_SESSION_TYPE=wayland",
		"XDG_CURRENT_DESKTOP=GNOME", "PATH=/usr/bin:/bin"}
	run := func(args ...string) string {
		cmd := exec.Command(agent, args...)
		cmd.Env = env
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	line := func(out, prefix string) string {
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, prefix) {
				return l
			}
		}
		return ""
	}
	before := line(run("status"), "dictation:")
	t.Logf("agent status before: %s", before)
	if !strings.Contains(before, "off: typing on this desktop needs a one-time permission: run `"+AllowCommand+"`") {
		t.Fatalf("status before allow: %q", before)
	}
	shown := back.dialogs()
	out := run("dictation", "allow")
	t.Logf("agent dictation allow:\n%s", out)
	if back.dialogs() != shown+1 || !strings.Contains(out, "Allowed.") {
		t.Fatalf("allow: dialogs %d (was %d)", back.dialogs(), shown)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "data/applications", AppID+".desktop")); !strings.Contains(string(b), "Exec="+agent+" run") {
		t.Fatalf("allow didn't point the .desktop file at the agent:\n%s", b)
	}
	tok, err := os.Stat(filepath.Join(home, "cfg", "jarvis-agent", "portal-remote-desktop.token"))
	if err != nil || tok.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v %v", tok, err)
	}
	after := line(run("status"), "dictation:")
	t.Logf("agent status after: %s", after)
	if !strings.HasPrefix(after, "dictation:    on ("+Name+"); typing into a named app: off (") {
		t.Fatalf("status after allow: %q", after)
	}
	t.Logf("agent dictation status: %s", strings.TrimSpace(run("dictation", "status")))
	keysBefore := len(back.keys())
	out = run("try", "desktop_type_text", `{"text":"hé 日"}`)
	if !strings.Contains(out, `"typed_chars": 4`) {
		t.Fatalf("type: %s", out)
	}
	out = run("try", "desktop_press_key", `{"key":"shift+tab"}`)
	if !strings.Contains(out, `"success": true`) {
		t.Fatalf("press: %s", out)
	}
	got := back.keys()[keysBefore:]
	want := []string{"0x68 1", "0x68 0", "0xe9 1", "0xe9 0", "0x20 1", "0x20 0", "0x10065e5 1", "0x10065e5 0",
		"0xffe1 1", "0xff09 1", "0xff09 0", "0xffe1 0"}
	if !slices.Equal(got, want) || back.dialogs() != shown+1 {
		t.Fatalf("agent keys %q (dialogs %d)", got, back.dialogs())
	}
	for _, ev := range back.events() {
		if strings.HasPrefix(ev, "create ") && ev != `create app="`+AppID+`"` {
			t.Fatalf("a session without the registered app id: %q", back.events())
		}
	}
	out = run("try", "desktop_type_text", `{"text":"x","app":"firefox"}`)
	t.Logf("named app: %s", strings.Join(strings.Fields(out), " "))
	if !strings.Contains(out, "not_supported") || len(back.keys()) != keysBefore+len(want) {
		t.Fatalf("typed into a named app on GNOME: %s", out)
	}
}

// fakeImpl is org.freedesktop.impl.portal.RemoteDesktop: Start "shows the dialog" unless
// SelectDevices brought restore_data from the frontend's permission store.
type fakeImpl struct {
	conn    *dbus.Conn
	mu      sync.Mutex
	log     []string
	keyLog  []string
	shown   int
	open    map[dbus.ObjectPath]bool
	restore map[dbus.ObjectPath]bool
	answer  uint32
}

type implSession struct {
	f    *fakeImpl
	path dbus.ObjectPath
}

func (s *implSession) Close() *dbus.Error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	delete(s.f.open, s.path)
	s.f.log = append(s.f.log, "close")
	return nil
}

func (f *fakeImpl) note(s string) { f.mu.Lock(); f.log = append(f.log, s); f.mu.Unlock() }
func (f *fakeImpl) events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.log)
}
func (f *fakeImpl) keys() []string { f.mu.Lock(); defer f.mu.Unlock(); return slices.Clone(f.keyLog) }
func (f *fakeImpl) dialogs() int   { f.mu.Lock(); defer f.mu.Unlock(); return f.shown }
func (f *fakeImpl) sessionsClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.open) == 0
}

func (f *fakeImpl) CreateSession(handle, session dbus.ObjectPath, app string, opts map[string]dbus.Variant) (uint32, map[string]dbus.Variant, *dbus.Error) {
	f.mu.Lock()
	if f.open == nil {
		f.open, f.restore = map[dbus.ObjectPath]bool{}, map[dbus.ObjectPath]bool{}
	}
	f.open[session] = true
	f.mu.Unlock()
	if err := f.conn.Export(&implSession{f: f, path: session}, session, "org.freedesktop.impl.portal.Session"); err != nil {
		return 2, nil, dbus.MakeFailedError(err)
	}
	f.note(fmt.Sprintf("create app=%q", app))
	return 0, map[string]dbus.Variant{}, nil
}

func (f *fakeImpl) SelectDevices(handle, session dbus.ObjectPath, app string, opts map[string]dbus.Variant) (uint32, map[string]dbus.Variant, *dbus.Error) {
	_, hasRestore := opts["restore_data"]
	f.mu.Lock()
	f.restore[session] = hasRestore
	f.mu.Unlock()
	f.note(fmt.Sprintf("select types=%v persist=%v restore=%v", opts["types"].Value(), variantValue(opts, "persist_mode"), hasRestore))
	return 0, map[string]dbus.Variant{}, nil
}

func variantValue(m map[string]dbus.Variant, k string) any {
	if v, ok := m[k]; ok {
		return v.Value()
	}
	return nil
}

type restoreData struct {
	Vendor  string
	Version uint32
	Data    dbus.Variant
}

func (f *fakeImpl) Start(handle, session dbus.ObjectPath, app, parent string, opts map[string]dbus.Variant) (uint32, map[string]dbus.Variant, *dbus.Error) {
	f.mu.Lock()
	restored := f.restore[session]
	if !restored {
		f.shown++
	}
	answer := f.answer
	f.mu.Unlock()
	if !restored {
		f.note("dialog")
		if answer != 0 {
			return answer, map[string]dbus.Variant{}, nil
		}
	}
	f.note("start")
	return 0, map[string]dbus.Variant{
		"devices":      dbus.MakeVariant(uint32(1)),
		"persist_mode": dbus.MakeVariant(uint32(2)),
		"restore_data": dbus.MakeVariant(restoreData{"jarvistest", 1, dbus.MakeVariant("granted")}),
	}, nil
}

func (f *fakeImpl) NotifyKeyboardKeysym(session dbus.ObjectPath, opts map[string]dbus.Variant, keysym int32, state uint32) *dbus.Error {
	f.mu.Lock()
	f.keyLog = append(f.keyLog, fmt.Sprintf("%#x %d", keysym, state))
	f.mu.Unlock()
	return nil
}

const busConfig = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:dir=%s</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`

// runFrontend starts the permission store and the real portal frontend on the private bus at
// addr, with bc's connection (named org.freedesktop.impl.portal.desktop.jarvistest) as the
// backend for iface, and waits for the frontend's name.
func runFrontend(t *testing.T, dir, addr string, bc *dbus.Conn, iface, frontend, store string) {
	t.Helper()
	for _, d := range []string{"portals", "config/xdg-desktop-portal", "data/applications", "run"} {
		_ = os.MkdirAll(filepath.Join(dir, d), 0o700)
	}
	// The .desktop file `dictation allow` writes, so the frontend can register the app id.
	if err := WriteDesktopFile(filepath.Join(dir, "data/applications", AppID+".desktop"), "/bin/true"); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "portals", "jarvistest.portal"), []byte("[portal]\nDBusName=org.freedesktop.impl.portal.desktop.jarvistest\nInterfaces="+iface+";\nUseIn=jarvistest\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "config/xdg-desktop-portal", "portals.conf"), []byte("[preferred]\ndefault=jarvistest\n"), 0o600)
	env := []string{"DBUS_SESSION_BUS_ADDRESS=" + addr, "HOME=" + dir, "PATH=/usr/bin:/bin",
		"XDG_DESKTOP_PORTAL_DIR=" + filepath.Join(dir, "portals"), "XDG_CONFIG_HOME=" + filepath.Join(dir, "config"),
		"XDG_DATA_HOME=" + filepath.Join(dir, "data"), "XDG_RUNTIME_DIR=" + filepath.Join(dir, "run"),
		"XDG_CURRENT_DESKTOP=jarvistest"}
	spawn(t, dir, "store", env, store)
	spawn(t, dir, "frontend", env, frontend, "--replace")
	waitName(t, bc, "org.freedesktop.portal.Desktop")
}

func startBus(t *testing.T, dir string) string {
	cfg := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(cfg, fmt.Appendf(nil, busConfig, dir), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("dbus-daemon", "--config-file="+cfg, "--nofork", "--print-address=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("no dbus-daemon: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(line)
}

func spawn(t *testing.T, dir, name string, env []string, argv ...string) {
	log, _ := os.Create(filepath.Join(dir, name+".log"))
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		log.Close()
		if t.Failed() {
			b, _ := os.ReadFile(log.Name())
			t.Logf("%s log:\n%s", name, b)
		}
	})
}

// waitName waits for a bus name to get an owner (a hang guard, not a sleep).
func waitName(t *testing.T, c *dbus.Conn, name string) {
	ch := make(chan *dbus.Signal, 8)
	c.Signal(ch)
	defer c.RemoveSignal(ch)
	_ = c.AddMatchSignal(dbus.WithMatchInterface("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, name))
	var has bool
	_ = c.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, name).Store(&has)
	guard := time.NewTimer(30 * time.Second)
	defer guard.Stop()
	for !has {
		select {
		case s := <-ch:
			if len(s.Body) == 3 && s.Body[0] == name && s.Body[2] != "" {
				has = true
			}
		case <-guard.C:
			t.Fatalf("%s never appeared on the bus", name)
		}
	}
}
