//go:build linux

package atspi

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

const (
	ifAccessible   = "org.a11y.atspi.Accessible"
	ifComponent    = "org.a11y.atspi.Component"
	ifAction       = "org.a11y.atspi.Action"
	ifEditableText = "org.a11y.atspi.EditableText"
	ifText         = "org.a11y.atspi.Text"

	registryName = "org.a11y.atspi.Registry"
	rootPath     = dbus.ObjectPath("/org/a11y/atspi/accessible/root")
	nullPath     = dbus.ObjectPath("/org/a11y/atspi/null")

	coordScreen = uint32(0) // ATSPI_COORD_TYPE_SCREEN
	coordWindow = uint32(1) // ATSPI_COORD_TYPE_WINDOW: from the toplevel's corner (Wayland)
	callTimeout = 5 * time.Second
)

// AtspiRole numbers (at-spi2-core, atspi-constants.h) the snapshot keeps.
const (
	roleCheckBox      = 7
	roleCheckMenuItem = 8
	roleComboBox      = 11
	roleFrame         = 23
	roleListItem      = 32
	roleMenuItem      = 35
	rolePageTab       = 37
	rolePasswordText  = 40
	roleButton        = 43 // ATSPI_ROLE_PUSH_BUTTON
	roleRadioButton   = 44
	roleRadioMenuItem = 45
	roleSlider        = 51
	roleSpinButton    = 52
	roleTableCell     = 56
	roleText          = 61
	roleToggleButton  = 62
	roleEntry         = 79
	roleLink          = 88
	roleTableRow      = 90
	roleTreeItem      = 91
	roleSwitch        = 130
)

// roleNames are the interactive roles and their snapshot names (the package doc lists them).
var roleNames = map[uint32]string{
	roleButton: "button", roleToggleButton: "togglebutton", roleSwitch: "switch",
	roleCheckBox: "checkbox", roleRadioButton: "radio", roleComboBox: "combobox",
	roleText: "textbox", roleEntry: "textbox", rolePasswordText: RolePassword,
	roleSpinButton: "spinbutton", roleSlider: "slider", roleMenuItem: "menuitem",
	roleCheckMenuItem: "menuitemcheckbox", roleRadioMenuItem: "menuitemradio", roleLink: "link",
	roleListItem: "listitem", roleTreeItem: "treeitem", roleTableCell: "cell", roleTableRow: "row",
	rolePageTab: "tab",
}

// containerRoles name a group of controls (a toolbar, a dialog, a menu): the nearest named one
// above an element is its Context (ELEMENT-LAYER-WORK.md §2). The window (frame) itself isn't
// one: its title would be every element's context. Numbers are AtspiRole's (atspi-constants.h).
var containerRoles = map[uint32]bool{
	16:  true, // dialog
	20:  true, // filler
	31:  true, // list
	33:  true, // menu
	34:  true, // menu bar
	38:  true, // page tab list
	39:  true, // panel
	54:  true, // status bar
	55:  true, // table
	63:  true, // tool bar
	71:  true, // header
	72:  true, // footer
	85:  true, // section
	87:  true, // form
	99:  true, // grouping
	110: true, // landmark
}

// textRoles hold what the user typed in their Text: it's their value, never their name.
var textRoles = map[string]bool{"textbox": true, "combobox": true}

// toggleRoles report STATE_CHECKED as "checked".
var toggleRoles = map[string]bool{
	"checkbox": true, "radio": true, "togglebutton": true, "switch": true,
	"menuitemcheckbox": true, "menuitemradio": true,
}

// AtspiStateType bits (atspi-constants.h). GetState returns them as 32-bit words.
const (
	stateActive    = 1
	stateChecked   = 4
	stateEnabled   = 8
	stateExpanded  = 10
	stateFocused   = 12
	stateSelected  = 23
	stateSensitive = 24
	stateShowing   = 25
	stateRequired  = 33
)

type states []uint32

func (s states) has(bit int) bool { return bit/32 < len(s) && s[bit/32]&(1<<(bit%32)) != 0 }

// objRef is an accessible's address on the bus, AT-SPI's (so).
type objRef struct {
	Name string
	Path dbus.ObjectPath
}

func (r objRef) null() bool { return r.Name == "" || r.Path == "" || r.Path == nullPath }

// obj is one accessible object.
type obj struct {
	c   *dbus.Conn
	ref objRef
}

func (o obj) call(ctx context.Context, method string, args ...any) *dbus.Call {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	return o.c.Object(o.ref.Name, o.ref.Path).CallWithContext(ctx, method, 0, args...)
}

func (o obj) role(ctx context.Context) (uint32, error) {
	var r uint32
	err := o.call(ctx, ifAccessible+".GetRole").Store(&r)
	return r, err
}

// children are GetChildren's, else ChildCount × GetChildAtIndex for a toolkit without it.
// children are o's children, at most maxChildren of them, or maxVirtualChildren when o lists none
// and only reports a count (a spreadsheet's sheet: millions of virtual cells; A15 F1).
func (o obj) children(ctx context.Context) ([]obj, error) {
	var refs []objRef
	if err := o.call(ctx, ifAccessible+".GetChildren").Store(&refs); err != nil {
		var n int32
		if v, perr := o.prop(ctx, "ChildCount"); perr != nil {
			return nil, err
		} else if n, _ = v.(int32); n <= 0 {
			return nil, nil
		}
		for i := range min(n, maxVirtualChildren) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			var r objRef
			if err := o.call(ctx, ifAccessible+".GetChildAtIndex", i).Store(&r); err == nil {
				refs = append(refs, r)
			}
		}
	}
	if len(refs) > maxChildren {
		refs = refs[:maxChildren]
	}
	out := make([]obj, 0, len(refs))
	for _, r := range refs {
		if !r.null() {
			out = append(out, obj{o.c, r})
		}
	}
	return out, nil
}

func (o obj) prop(ctx context.Context, name string) (any, error) {
	var v dbus.Variant
	if err := o.call(ctx, "org.freedesktop.DBus.Properties.Get", ifAccessible, name).Store(&v); err != nil {
		return nil, err
	}
	return v.Value(), nil
}

func (o obj) str(ctx context.Context, name string) string {
	v, _ := o.prop(ctx, name)
	s, _ := v.(string)
	return s
}

// name is the accessible's Name, else its Description.
func (o obj) name(ctx context.Context) string {
	if n := clip(o.str(ctx, "Name")); n != "" {
		return n
	}
	return clip(o.str(ctx, "Description"))
}

// state is nil when unknown.
func (o obj) state(ctx context.Context) states {
	var s []uint32
	if o.call(ctx, ifAccessible+".GetState").Store(&s) != nil {
		return nil
	}
	return s
}

// bounds are the screen extents; nil when unknown or empty.
func (o obj) bounds(ctx context.Context) *[4]int {
	var e struct{ X, Y, W, H int32 }
	if o.call(ctx, ifComponent+".GetExtents", coordScreen).Store(&e) != nil || e.W <= 0 || e.H <= 0 {
		return nil
	}
	if e.X == math.MinInt32 || e.Y == math.MinInt32 {
		return nil // the toolkit's "nowhere": not on screen (LibreOffice's closed menu items)
	}
	return &[4]int{int(e.X), int(e.Y), int(e.W), int(e.H)}
}

func (o obj) text(ctx context.Context) string {
	var s string
	_ = o.call(ctx, ifText+".GetText", int32(0), int32(-1)).Store(&s)
	return s
}

// do calls a method that answers a boolean, false being the app's refusal.
func (o obj) do(method string, args ...any) error {
	var ok bool
	if err := o.call(context.Background(), method, args...).Store(&ok); err != nil {
		return callErr(method, err)
	}
	if !ok {
		return fmt.Errorf("atspi: the app refused %s", method)
	}
	return nil
}

// callErr maps D-Bus errors to computer_act codes: the object or app is gone (stale_ref), or it
// lacks the interface (unsupported).
func callErr(method string, err error) error {
	var de dbus.Error
	if errors.As(err, &de) {
		switch de.Name {
		case "org.freedesktop.DBus.Error.UnknownObject", "org.freedesktop.DBus.Error.ServiceUnknown":
			return &Error{Code: CodeStaleRef, Msg: "the element is gone (" + de.Name + ")"}
		case "org.freedesktop.DBus.Error.UnknownMethod", "org.freedesktop.DBus.Error.UnknownInterface":
			return &Error{Code: CodeUnsupported, Msg: "the element has no " + method}
		}
	}
	return fmt.Errorf("atspi: %s: %w", method, err)
}

// a11y is the process's connection to the accessibility bus.
var a11y struct {
	mu sync.Mutex
	c  *dbus.Conn
}

func bus(ctx context.Context) (*dbus.Conn, error) {
	a11y.mu.Lock()
	defer a11y.mu.Unlock()
	if a11y.c != nil && a11y.c.Connected() {
		return a11y.c, nil
	}
	addr := os.Getenv("AT_SPI_BUS_ADDRESS")
	if addr == "" {
		var err error
		if addr, err = busAddress(ctx); err != nil {
			return nil, err
		}
	}
	c, err := dbus.Connect(addr)
	if err != nil {
		return nil, &Error{Code: CodeUnsupported, Msg: "connect to the accessibility bus: " + err.Error()}
	}
	a11y.c = c
	return c, nil
}

// busAddress asks the session bus's org.a11y.Bus (at-spi-bus-launcher) for the bus address.
func busAddress(ctx context.Context) (string, error) {
	s, err := dbus.ConnectSessionBus()
	if err != nil {
		return "", &Error{Code: CodeUnsupported, Msg: "no session bus: " + err.Error()}
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var addr string
	if err := s.Object("org.a11y.Bus", "/org/a11y/bus").CallWithContext(ctx, "org.a11y.Bus.GetAddress", 0).Store(&addr); err != nil {
		return "", &Error{Code: CodeUnsupported, Msg: "no accessibility bus (org.a11y.Bus): " + err.Error()}
	}
	return addr, nil
}

// activeWindow is the app's top-level child with STATE_ACTIVE, else its first one.
func activeWindow(ctx context.Context, pid int) (obj, error) {
	c, err := bus(ctx)
	if err != nil {
		return obj{}, err
	}
	app, err := findApp(ctx, c, pid)
	if err != nil {
		return obj{}, err
	}
	wins, err := app.children(ctx)
	if err != nil {
		return obj{}, callErr("GetChildren", err)
	}
	if len(wins) == 0 {
		return obj{}, &Error{Code: CodeNotFound, Msg: "app " + strconv.Itoa(pid) + " has no windows"}
	}
	for _, w := range wins {
		if w.state(ctx).has(stateActive) {
			return w, nil
		}
	}
	return wins[0], nil
}

// findApp is the registry's app whose bus connection belongs to pid.
func findApp(ctx context.Context, c *dbus.Conn, pid int) (obj, error) {
	apps, err := obj{c, objRef{registryName, rootPath}}.children(ctx)
	if err != nil {
		return obj{}, &Error{Code: CodeUnsupported, Msg: "read the AT-SPI registry: " + err.Error()}
	}
	for _, a := range apps {
		var p uint32
		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		err := c.BusObject().CallWithContext(cctx, "org.freedesktop.DBus.GetConnectionUnixProcessID", 0, a.ref.Name).Store(&p)
		cancel()
		if err == nil && int(p) == pid {
			return a, nil
		}
	}
	return obj{}, &Error{Code: CodeNotFound, Msg: "no accessible app with pid " + strconv.Itoa(pid) + " (it may not expose AT-SPI)"}
}

type entry struct {
	o      obj
	role   string
	name   string
	secure bool
}

// refTable holds the current generation's elements by ref.
type refTable struct {
	mu    sync.Mutex
	gen   int
	nodes map[string]entry
}

// refs is the process's table: one window is observed at a time.
var refs refTable

// snapshotOf walks the pid's active window; showing skips subtrees not on screen.
func snapshotOf(ctx context.Context, pid int, max int, showing bool) (computeruse.Snapshot, error) {
	win, err := activeWindow(ctx, pid)
	if err != nil {
		return computeruse.Snapshot{}, err
	}
	if max <= 0 {
		max = DefaultMax
	}
	w := walker{ctx: ctx, max: max, nodes: map[string]entry{}, win: win.bounds(ctx), showing: showing}
	w.visit(win, 0, "")
	if w.err != nil {
		return computeruse.Snapshot{}, w.err
	}
	refs.mu.Lock()
	defer refs.mu.Unlock()
	refs.gen++
	refs.nodes = w.nodes
	return computeruse.Snapshot{Gen: refs.gen, Elements: w.out}, nil
}

type walker struct {
	ctx   context.Context
	max   int
	out   []computeruse.SnapElement
	nodes map[string]entry
	err   error
	win   *[4]int // the window's screen bounds, for Position; nil when unknown
	// showing: only what is on screen (SnapshotShowing); a subtree that isn't is skipped whole.
	showing bool
}

func (w *walker) full() bool { return w.err != nil || len(w.out) >= w.max }

// visit keeps o when it is a control and walks its children; near is the name of the nearest
// named container above o.
func (w *walker) visit(o obj, depth int, near string) {
	if err := w.ctx.Err(); err != nil {
		w.err = err
		return
	}
	r, err := o.role(w.ctx)
	if err != nil {
		return // gone mid-walk, or not an accessible
	}
	if w.showing && !o.state(w.ctx).has(stateShowing) {
		return // a closed menu, a hidden panel: nothing in it is on screen
	}
	if role, ok := roleNames[r]; ok {
		w.keep(o, role, r == rolePasswordText, near)
	} else if containerRoles[r] && o.state(w.ctx).has(stateShowing) {
		// Only a container on screen names its elements: a closed menu or hidden panel never
		// becomes context, in either snapshot kind (A15 F7).
		if n := strings.TrimSpace(o.name(w.ctx)); n != "" {
			near = n
		}
	}
	if depth >= maxDepth || w.full() {
		return
	}
	kids, _ := o.children(w.ctx)
	for _, k := range kids {
		if w.full() {
			return
		}
		w.visit(k, depth+1, near)
	}
}

func (w *walker) keep(o obj, role string, secure bool, near string) {
	ctx := w.ctx
	el := computeruse.SnapElement{Ref: "a" + strconv.Itoa(len(w.out)+1), Role: role, Name: o.name(ctx), Bounds: o.bounds(ctx)}
	if el.Bounds != nil && w.win != nil {
		el.Position = computeruse.PositionIn(*el.Bounds, *w.win)
	}
	if near != el.Name {
		if r := []rune(near); len(r) > computeruse.ContextMax {
			near = string(r[:computeruse.ContextMax])
		}
		el.Context = near // a container's name, never a value
	}
	st := o.state(ctx)
	switch {
	case secure: // a password's text is never read
	case toggleRoles[role]:
		if st.has(stateChecked) {
			el.State = append(el.State, "checked")
		}
	case textRoles[role] && !w.showing: // a voice snapshot never reads what was typed (§2, A15 F6)
		el.Value = clip(o.text(ctx))
	}
	if st != nil && (!st.has(stateEnabled) || !st.has(stateSensitive)) {
		el.State = append(el.State, "disabled")
	}
	for _, s := range []struct {
		bit  int
		name string
	}{{stateFocused, "focused"}, {stateSelected, "selected"}, {stateExpanded, "expanded"}, {stateRequired, "required"}} {
		if st.has(s.bit) {
			el.State = append(el.State, s.name)
		}
	}
	w.out = append(w.out, el)
	w.nodes[el.Ref] = entry{o: o, role: role, name: el.Name, secure: secure}
}

// lookup finds ref in generation gen. It holds the lock while f acts, so a new snapshot can't
// replace the table under it.
func (t *refTable) lookup(gen int, ref string, f func(entry) error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if gen != t.gen || t.nodes == nil {
		return &Error{Code: CodeStaleRef, Msg: fmt.Sprintf("ref %s is from snapshot %d; the current one is %d", ref, gen, t.gen)}
	}
	e, ok := t.nodes[ref]
	if !ok {
		return &Error{Code: CodeNotFound, Msg: "no element " + ref + " in the snapshot"}
	}
	return f(e)
}

func press(gen int, ref string) error {
	return refs.lookup(gen, ref, func(e entry) error { return e.o.do(ifAction+".DoAction", int32(0)) })
}

func setValue(gen int, ref, text string) error {
	return refs.lookup(gen, ref, func(e entry) error {
		if e.secure {
			return &Error{Code: CodePasswordField, Msg: ref + " is a password text; the user types passwords"}
		}
		return e.o.do(ifEditableText+".SetTextContents", text)
	})
}

func focus(gen int, ref string) error {
	return refs.lookup(gen, ref, func(e entry) error { return e.o.do(ifComponent + ".GrabFocus") })
}

func element(gen int, ref string) (*computeruse.Element, error) {
	var el *computeruse.Element
	err := refs.lookup(gen, ref, func(e entry) error {
		el = &computeruse.Element{Role: e.role, Name: e.name, Secure: e.secure}
		return nil
	})
	return el, err
}

// elementAt descends GetAccessibleAtPoint from the active window to the deepest element under
// (x, y) in coord's coordinates. Its role is "" when it isn't one the snapshot keeps.
func elementAt(pid int, x, y int, coord uint32) (*computeruse.Element, error) {
	ctx := context.Background()
	cur, err := activeWindow(ctx, pid)
	if err != nil {
		return nil, err
	}
	hit := false
	for range maxDepth {
		var r objRef
		if err := cur.call(ctx, ifComponent+".GetAccessibleAtPoint", int32(x), int32(y), coord).Store(&r); err != nil || r.null() || r == cur.ref {
			break
		}
		cur, hit = obj{cur.c, r}, true
	}
	if !hit {
		return nil, nil
	}
	r, err := cur.role(ctx)
	if err != nil {
		return nil, callErr("GetRole", err)
	}
	return &computeruse.Element{Role: roleNames[r], Name: cur.name(ctx), Secure: r == rolePasswordText}, nil
}

// maxFocusWalk bounds the walk for the focused element (a document can have thousands).
const maxFocusWalk = 2000

// lastFocus is the element focused() found last: while it keeps STATE_FOCUSED it is still the
// one (an app has one focused element), so a keystroke costs a GetState, not a walk.
var lastFocus struct {
	mu   sync.Mutex
	pid  int
	o    *obj
	role uint32
}

// focused finds the active window's element with STATE_FOCUSED, depth-first.
func focused(pid int) (*computeruse.Element, error) {
	ctx := context.Background()
	lastFocus.mu.Lock()
	defer lastFocus.mu.Unlock()
	if o := lastFocus.o; o != nil && lastFocus.pid == pid && o.c.Connected() && o.state(ctx).has(stateFocused) {
		return focusedElement(ctx, *o, lastFocus.role), nil
	}
	lastFocus.o = nil
	win, err := activeWindow(ctx, pid)
	if err != nil {
		return nil, err
	}
	budget := maxFocusWalk
	var find func(o obj, depth int) (obj, bool)
	find = func(o obj, depth int) (obj, bool) {
		if budget--; budget < 0 || ctx.Err() != nil {
			return obj{}, false
		}
		if o.state(ctx).has(stateFocused) {
			return o, true
		}
		if depth >= maxDepth {
			return obj{}, false
		}
		kids, _ := o.children(ctx)
		for _, k := range kids {
			if f, ok := find(k, depth+1); ok {
				return f, true
			}
		}
		return obj{}, false
	}
	o, ok := find(win, 0)
	if !ok {
		return nil, nil
	}
	r, err := o.role(ctx)
	if err != nil {
		return nil, callErr("GetRole", err)
	}
	lastFocus.pid, lastFocus.o, lastFocus.role = pid, &o, r
	return focusedElement(ctx, o, r), nil
}

func focusedElement(ctx context.Context, o obj, role uint32) *computeruse.Element {
	return &computeruse.Element{Role: roleNames[role], Name: o.name(ctx), Secure: role == rolePasswordText}
}

// enable reads org.a11y.Status on the session bus and switches IsEnabled on when it is off.
func enable(ctx context.Context) State {
	st := State{SessionBus: os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "", From: "AT_SPI_BUS_ADDRESS"}
	if os.Getenv("AT_SPI_BUS_ADDRESS") == "" {
		st.From = "org.a11y.Bus"
	}
	if s, err := dbus.ConnectSessionBus(); err == nil {
		defer s.Close()
		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		o := s.Object("org.a11y.Bus", "/org/a11y/bus")
		get := func(name string) bool {
			var v dbus.Variant
			if o.CallWithContext(cctx, "org.freedesktop.DBus.Properties.Get", 0, "org.a11y.Status", name).Store(&v) != nil {
				return false
			}
			b, _ := v.Value().(bool)
			return b
		}
		st.Enabled, st.ScreenReader = get("IsEnabled"), get("ScreenReaderEnabled")
		if !st.Enabled && o.CallWithContext(cctx, "org.freedesktop.DBus.Properties.Set", 0,
			"org.a11y.Status", "IsEnabled", dbus.MakeVariant(true)).Err == nil {
			st.Enabled, st.TurnedOn = true, true
		}
	}
	_, st.Err = bus(ctx)
	return st
}
