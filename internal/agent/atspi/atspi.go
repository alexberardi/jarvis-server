// Package atspi reads a Linux app's active window through AT-SPI (the accessibility bus) as a
// computeruse.Snapshot with refs, and acts on an element by ref (press, set value, focus). It is
// the Linux counterpart of internal/agent/ax and has the same shape: refs are "a<n>" within a
// generation, each Snapshot starts a new one, and an action with an older generation fails with
// stale_ref. Other platforms get ErrUnsupported.
//
// The accessibility bus is AT_SPI_BUS_ADDRESS when set, else the address the session bus's
// org.a11y.Bus gives. Apps that don't expose AT-SPI (or a desktop without the bus) leave only
// vision.
//
// Roles are AT-SPI's, reported under these names (stable; the deny check and the risk
// classifier read them): button, togglebutton, switch, checkbox, radio, combobox, textbox,
// passwordtext, spinbutton, slider, menuitem, menuitemcheckbox, menuitemradio, link, listitem,
// treeitem, cell, row, tab. A passwordtext's text is never read or set.
package atspi

import (
	"context"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// DefaultMax is the element cap when Snapshot is given max <= 0.
const DefaultMax = 300

const (
	maxDepth = 64  // a broken app can report a cycle; the walk stops here
	maxText  = 200 // runes kept of a name or value (a text view's text can be a whole document)
)

// RolePassword is the snapshot role of an AT-SPI password text (ROLE_PASSWORD_TEXT).
const RolePassword = "passwordtext"

// Error codes, as in computer_act's results.
const (
	CodeStaleRef      = "stale_ref"
	CodePasswordField = "password_field"
	CodeNotFound      = "not_found"
	CodeUnsupported   = "unsupported"
)

// Error is an atspi failure with a computer_act code.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return "atspi: " + e.Msg }

// ErrUnsupported: this platform has no AT-SPI.
var ErrUnsupported = &Error{Code: CodeUnsupported, Msg: "AT-SPI snapshots are only supported on Linux"}

// Snapshot walks the active window (else the first window) of the app with this pid, depth-first,
// and returns at most max interactive elements (DefaultMax when max <= 0) under a new generation.
func Snapshot(ctx context.Context, pid int, max int) (computeruse.Snapshot, error) {
	return snapshot(ctx, pid, max)
}

// Press performs ref's first action (Action.DoAction(0)) from snapshot generation gen.
func Press(gen int, ref string) error { return press(gen, ref) }

// SetValue replaces ref's text (EditableText.SetTextContents). A password text is refused
// (password_field): typing a password is the user's.
func SetValue(gen int, ref, text string) error { return setValue(gen, ref, text) }

// Focus moves keyboard focus to ref (Component.GrabFocus).
func Focus(gen int, ref string) error { return focus(gen, ref) }

// Element is what the deny check and the risk classifier know about ref.
func Element(gen int, ref string) (*computeruse.Element, error) { return element(gen, ref) }

// ElementAt returns the deepest element under screen point (x, y) in the active window of the app
// with this pid (the caller finds the window under the point), for the deny check on a coordinate
// action and for recording; nil when there's none.
func ElementAt(pid int, x, y int) (*computeruse.Element, error) {
	return elementAt(pid, x, y, coordScreen)
}

// ElementAtWindow is ElementAt with (x, y) from the active window's top-left corner: on Wayland
// an app doesn't know where its window is on the screen, so screen points find nothing.
func ElementAtWindow(pid int, x, y int) (*computeruse.Element, error) {
	return elementAt(pid, x, y, coordWindow)
}

// Focused returns the element with the keyboard focus in the active window of the app with
// this pid, which recorded typing goes to (a passwordtext is Secure); nil when none has it.
func Focused(pid int) (*computeruse.Element, error) { return focused(pid) }

func clip(s string) string {
	r := []rune(s)
	if len(r) <= maxText {
		return s
	}
	return string(r[:maxText]) + "…"
}

// State is the accessibility setup as Enable found it; a desktop recording logs it at its start.
type State struct {
	SessionBus   bool   // DBUS_SESSION_BUS_ADDRESS is set
	From         string // where the bus address comes from: AT_SPI_BUS_ADDRESS or org.a11y.Bus
	Enabled      bool   // org.a11y.Status IsEnabled, after Enable
	TurnedOn     bool   // Enable switched IsEnabled on
	ScreenReader bool   // org.a11y.Status ScreenReaderEnabled
	Err          error  // why the accessibility bus can't be reached; nil when it can
}

// Enable switches the session's org.a11y.Status IsEnabled on when it is off, as a screen
// reader does: Qt and Chromium apps join the accessibility bus only then (GTK 4 always does).
// It stays on after the recording. It reports the setup, and whether the bus can be reached.
func Enable(ctx context.Context) State { return enable(ctx) }

// Tab is a page tab in one of an app's windows: a browser tab, a spreadsheet's sheet, an
// editor's or terminal's tab. Index is its place in the app's tabs (the active window's first,
// each window's in tree order); Window is its window's title.
type Tab struct {
	Index    int
	Name     string
	Selected bool
	Window   string
}

// Tabs lists the page tabs of every window of the app with this pid, the active window's first.
func Tabs(ctx context.Context, pid int) ([]Tab, error) { return tabs(ctx, pid) }

// SelectTab switches to the tab Tabs reported at index with this name (stale_ref when the tabs
// changed since).
func SelectTab(ctx context.Context, pid, index int, name string) error {
	return selectTab(ctx, pid, index, name)
}
