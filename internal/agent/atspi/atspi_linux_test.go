package atspi

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

// fakeNode is one accessible object a fake app exports on a private bus: the Accessible,
// Component, Action, EditableText and Text methods AT-SPI clients call, with a log of the calls
// that act or read text.
type fakeNode struct {
	role   uint32
	name   string
	states []int // AtspiStateType bits
	box    [4]int32
	text   string
	kids   []*fakeNode
	// actions are GetActions' names; none: a single unnamed action (DoAction 0 still works).
	actions []string

	app  *fakeApp
	path dbus.ObjectPath

	mu    sync.Mutex
	calls []string
}

type extents struct{ X, Y, W, H int32 }

func (n *fakeNode) log(s string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, s)
}

func (n *fakeNode) called() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.calls)
}

func (n *fakeNode) setStates(s ...int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.states = s
}

func (n *fakeNode) ref() objRef { return objRef{n.app.conn.Names()[0], n.path} }

func (n *fakeNode) GetRole() (uint32, *dbus.Error) { return n.role, nil }

func (n *fakeNode) GetChildren() ([]objRef, *dbus.Error) {
	out := []objRef{}
	for _, k := range n.kids {
		out = append(out, k.ref())
	}
	return out, nil
}

func (n *fakeNode) GetState() ([]uint32, *dbus.Error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	s := make([]uint32, 2)
	for _, b := range n.states {
		s[b/32] |= 1 << (b % 32)
	}
	return s, nil
}

func (n *fakeNode) GetExtents(coord uint32) (extents, *dbus.Error) {
	if coord != 0 {
		return extents{}, dbus.MakeFailedError(fmt.Errorf("want screen coords, got %d", coord))
	}
	return extents{n.box[0], n.box[1], n.box[2], n.box[3]}, nil
}

func (n *fakeNode) GetAccessibleAtPoint(x, y int32, coord uint32) (objRef, *dbus.Error) {
	for _, k := range n.kids {
		b := k.box
		if coord == 0 && x >= b[0] && y >= b[1] && x < b[0]+b[2] && y < b[1]+b[3] {
			return k.ref(), nil
		}
	}
	return objRef{n.app.conn.Names()[0], nullPath}, nil
}

func (n *fakeNode) GrabFocus() (bool, *dbus.Error) { n.log("GrabFocus"); return true, nil }

type action struct{ Name, Desc, Key string }

func (n *fakeNode) GetActions() ([]action, *dbus.Error) {
	out := make([]action, len(n.actions))
	for i, a := range n.actions {
		out[i] = action{Name: a}
	}
	return out, nil
}

func (n *fakeNode) DoAction(i int32) (bool, *dbus.Error) {
	n.log("DoAction " + strconv.Itoa(int(i)))
	return true, nil
}

func (n *fakeNode) SetTextContents(s string) (bool, *dbus.Error) {
	n.log("SetTextContents " + s)
	return true, nil
}

func (n *fakeNode) GetText(start, end int32) (string, *dbus.Error) {
	n.log(fmt.Sprintf("GetText %d %d", start, end))
	return n.text, nil
}

type fakeApp struct {
	conn  *dbus.Conn
	count int
}

// export puts n and its subtree on the app's connection; the root is the app object.
func (a *fakeApp) export(t *testing.T, n *fakeNode) {
	n.app = a
	if a.count == 0 {
		n.path = rootPath
	} else {
		n.path = dbus.ObjectPath("/org/a11y/atspi/accessible/" + strconv.Itoa(a.count))
	}
	a.count++
	for _, iface := range []string{ifAccessible, ifComponent, ifAction, ifEditableText, ifText} {
		if err := a.conn.Export(n, n.path, iface); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := prop.Export(a.conn, n.path, prop.Map{ifAccessible: {
		"Name":        {Value: n.name},
		"Description": {Value: ""},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, k := range n.kids {
		a.export(t, k)
	}
}

// registry is the bus's org.a11y.atspi.Registry: its root's children are the apps.
type registry struct{ apps []objRef }

func (r *registry) GetChildren() ([]objRef, *dbus.Error) { return r.apps, nil }

// fakeDesktop starts a private bus, points the package at it (AT_SPI_BUS_ADDRESS, as a session
// sets it), registers a registry and one app (this process's pid) exporting root's tree.
func fakeDesktop(t *testing.T, root *fakeNode) {
	t.Helper()
	dir := t.TempDir()
	if len(dir) > 80 { // a unix socket path is at most 108 bytes
		var err error
		if dir, err = os.MkdirTemp("/tmp", "atspi"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
	}
	addr := startBus(t, dir)
	t.Setenv("AT_SPI_BUS_ADDRESS", addr)
	t.Cleanup(reset)
	reset()

	app := &fakeApp{conn: dial(t, addr)}
	app.export(t, root)

	reg := dial(t, addr)
	if r, err := reg.RequestName(registryName, dbus.NameFlagDoNotQueue); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own %s: %v %v", registryName, r, err)
	}
	if err := reg.Export(&registry{apps: []objRef{root.ref()}}, rootPath, ifAccessible); err != nil {
		t.Fatal(err)
	}
}

func dial(t *testing.T, addr string) *dbus.Conn {
	c, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// reset drops the package's connection and refs, so each test sees only its own bus.
func reset() {
	a11y.mu.Lock()
	if a11y.c != nil {
		a11y.c.Close()
		a11y.c = nil
	}
	a11y.mu.Unlock()
	refs.mu.Lock()
	refs.nodes = nil
	refs.mu.Unlock()
	lastFocus.mu.Lock()
	lastFocus.o = nil
	lastFocus.mu.Unlock()
}

const (
	roleApplication = 75
	roleLabel       = 29
	rolePanel       = 39
)

var usable = []int{stateEnabled, stateSensitive}

func with(s ...int) []int { return append(slices.Clone(usable), s...) }

// window is the tree the tests share: an inactive window, then the active one.
type window struct {
	app, other, ok, remember, search, password, disabled, tab *fakeNode
}

func newWindow() window {
	w := window{
		ok:       &fakeNode{role: roleButton, name: "OK", states: with(stateFocused), box: [4]int32{10, 20, 80, 30}},
		remember: &fakeNode{role: roleCheckBox, name: "Remember me", states: with(stateChecked), box: [4]int32{10, 60, 120, 20}},
		search:   &fakeNode{role: roleEntry, name: "Search", states: usable, text: "milk", box: [4]int32{10, 90, 200, 24}},
		password: &fakeNode{role: rolePasswordText, name: "Password", states: usable, text: "hunter2", box: [4]int32{10, 120, 200, 24}},
		disabled: &fakeNode{role: roleButton, name: "Delete"},
		tab:      &fakeNode{role: rolePageTab, name: "General", states: with(stateSelected)},
	}
	w.other = &fakeNode{role: roleFrame, name: "Other", kids: []*fakeNode{{role: roleButton, name: "Elsewhere", states: usable}}}
	main := &fakeNode{role: roleFrame, name: "Main", states: with(stateActive), box: [4]int32{0, 0, 400, 300}, kids: []*fakeNode{
		{role: rolePanel, box: [4]int32{0, 0, 400, 150}, kids: []*fakeNode{
			{role: roleLabel, name: "Sign in"},
			w.ok, w.remember, w.search, w.password,
		}},
		w.disabled,
		{role: rolePageTabList, kids: []*fakeNode{w.tab}},
	}}
	w.app = &fakeNode{role: roleApplication, name: "fake", kids: []*fakeNode{w.other, main}}
	return w
}

func TestSnapshotFromFakeTree(t *testing.T) {
	w := newWindow()
	fakeDesktop(t, w.app)

	s, err := Snapshot(context.Background(), os.Getpid(), 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range s.Elements {
		got = append(got, fmt.Sprintf("%s %s %q %q %v", e.Ref, e.Role, e.Name, e.Value, e.State))
	}
	want := []string{
		`a1 button "OK" "" [focused]`,
		`a2 checkbox "Remember me" "" [checked]`,
		`a3 textbox "Search" "milk" []`,
		`a4 passwordtext "Password" "" []`,
		`a5 button "Delete" "" [disabled]`,
		`a6 tab "General" "" [selected]`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("elements (the active window's, labels and panels skipped):\n got %q\nwant %q", got, want)
	}
	if b := s.Elements[0].Bounds; b == nil || *b != [4]int{10, 20, 80, 30} {
		t.Errorf("OK bounds = %v, want screen extents [10 20 80 30]", b)
	}
	if s.Elements[4].Bounds != nil {
		t.Errorf("an element with no size has bounds %v", s.Elements[4].Bounds)
	}

	s2, err := Snapshot(context.Background(), os.Getpid(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.Elements) != 2 || s2.Gen != s.Gen+1 {
		t.Errorf("max 2: %d elements, gen %d after %d", len(s2.Elements), s2.Gen, s.Gen)
	}

	_, err = Snapshot(context.Background(), os.Getpid()+1, 0)
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeNotFound {
		t.Errorf("another pid: %v, want not_found", err)
	}
}

func TestPasswordTextHidden(t *testing.T) {
	w := newWindow()
	fakeDesktop(t, w.app)

	s, err := Snapshot(context.Background(), os.Getpid(), 0)
	if err != nil {
		t.Fatal(err)
	}
	pw := s.Find("a4")
	if pw == nil || pw.Role != RolePassword || pw.Value != "" {
		t.Fatalf("password text: %+v", pw)
	}
	if !pw.Element().Secure {
		t.Error("the snapshot's password text isn't secure to the deny check")
	}
	if el, err := Element(s.Gen, "a4"); err != nil || !el.Secure {
		t.Errorf("Element(a4) = %+v, %v; want secure", el, err)
	}

	err = SetValue(s.Gen, "a4", "letmein")
	var e *Error
	if !errors.As(err, &e) || e.Code != CodePasswordField {
		t.Errorf("SetValue on password text: %v, want password_field", err)
	}
	if c := w.password.called(); len(c) != 0 {
		t.Errorf("the password text was read or written: %q", c)
	}

	el, err := ElementAt(os.Getpid(), 50, 130)
	if err != nil || el == nil || !el.Secure || el.Name != "Password" {
		t.Errorf("ElementAt over the password text = %+v, %v; want it, secure", el, err)
	}
	if c := w.password.called(); len(c) != 0 {
		t.Errorf("ElementAt read the password text: %q", c)
	}
}

// Focused is the active window's element with the focus; a password text is secure, and its
// text is never read.
func TestFocused(t *testing.T) {
	w := newWindow()
	fakeDesktop(t, w.app)

	el, err := Focused(os.Getpid())
	if err != nil || el == nil || el.Name != "OK" || el.Role != "button" || el.Secure {
		t.Fatalf("Focused = %+v, %v; want the OK button", el, err)
	}
	w.ok.setStates(usable...)
	w.password.setStates(with(stateFocused)...)
	el, err = Focused(os.Getpid())
	if err != nil || el == nil || el.Name != "Password" || !el.Secure {
		t.Fatalf("Focused after the focus moved = %+v, %v; want the password text, secure", el, err)
	}
	if el, _ := Focused(os.Getpid()); el == nil || !el.Secure {
		t.Fatalf("Focused again (the cached element) = %+v", el)
	}
	if c := w.password.called(); len(c) != 0 {
		t.Errorf("Focused read the password text: %q", c)
	}
	w.password.setStates(usable...)
	if el, err := Focused(os.Getpid()); err != nil || el != nil {
		t.Errorf("nothing focused: %+v, %v", el, err)
	}
}

func TestDoAction(t *testing.T) {
	w := newWindow()
	fakeDesktop(t, w.app)

	s, err := Snapshot(context.Background(), os.Getpid(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := Press(s.Gen, "a1"); err != nil {
		t.Fatal(err)
	}
	if err := Focus(s.Gen, "a3"); err != nil {
		t.Fatal(err)
	}
	if err := SetValue(s.Gen, "a3", "eggs"); err != nil {
		t.Fatal(err)
	}
	if c := w.ok.called(); !slices.Equal(c, []string{"DoAction 0"}) {
		t.Errorf("OK got %q, want its first action", c)
	}
	if c := w.search.called(); !slices.Equal(c, []string{"GetText 0 -1", "GrabFocus", "SetTextContents eggs"}) {
		t.Errorf("Search got %q", c)
	}

	if el, err := ElementAt(os.Getpid(), 20, 25); err != nil || el == nil || el.Role != "button" || el.Name != "OK" {
		t.Errorf("ElementAt(OK) = %+v, %v", el, err)
	}
	if el, err := ElementAt(os.Getpid(), 390, 290); err != nil || el != nil {
		t.Errorf("ElementAt(empty corner) = %+v, %v; want none", el, err)
	}

	var e *Error
	if err := Press(s.Gen, "a99"); !errors.As(err, &e) || e.Code != CodeNotFound {
		t.Errorf("unknown ref: %v", err)
	}
	if _, err := Snapshot(context.Background(), os.Getpid(), 0); err != nil {
		t.Fatal(err)
	}
	if err := Press(s.Gen, "a1"); !errors.As(err, &e) || e.Code != CodeStaleRef {
		t.Errorf("old generation: %v, want stale_ref", err)
	}
	if c := w.ok.called(); len(c) != 1 {
		t.Errorf("a stale press reached the app: %q", c)
	}

	s, _ = Snapshot(context.Background(), os.Getpid(), 0)
	w.ok.app.conn.Export(nil, w.ok.path, ifAction)
	if err := Press(s.Gen, "a1"); !errors.As(err, &e) || e.Code != CodeUnsupported {
		t.Errorf("no Action interface: %v, want unsupported", err)
	}
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

// startBus runs a private dbus-daemon (the tests' accessibility bus) and returns its address.
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
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Skipf("no dbus-daemon: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		_ = cmd.Wait()
		t.Fatalf("dbus-daemon: %v: %s", err, stderr.String())
	}
	return strings.TrimSpace(line)
}
