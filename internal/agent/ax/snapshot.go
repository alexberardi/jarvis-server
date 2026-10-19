// Package ax reads the front window's accessibility tree as a computeruse.Snapshot with refs, and
// acts on an element by ref (press, set value, focus). It's implemented on macOS (ax_darwin.go,
// purego); other platforms get ErrUnsupported. Callers gate on macax.Trusted(): without the
// Accessibility grant every call fails with ErrNotTrusted.
//
// Refs are "a<n>" within a generation: each Snapshot starts a new one and releases the last, and
// an action with an older generation fails with stale_ref.
package ax

import (
	"context"
	"strconv"
	"sync"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// DefaultMax is the element cap when Snapshot is given max <= 0.
const DefaultMax = 300

const (
	maxDepth = 64  // AX trees aren't cyclic, but a broken app shouldn't hang the walk
	maxText  = 200 // runes kept of a name or value (a text area's value can be a whole document)
)

// Error codes, as in computer_act's results.
const (
	CodeStaleRef      = "stale_ref"
	CodePasswordField = "password_field"
	CodeNotFound      = "not_found"
	CodeNotTrusted    = "not_trusted"
	CodeUnsupported   = "unsupported"
)

// Error is an ax failure with a computer_act code.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return "ax: " + e.Msg }

var (
	// ErrUnsupported: this platform has no accessibility API.
	ErrUnsupported = &Error{Code: CodeUnsupported, Msg: "accessibility snapshots are not supported on this platform"}
	// ErrNotTrusted: this process doesn't hold the Accessibility grant.
	ErrNotTrusted = &Error{Code: CodeNotTrusted, Msg: "this process is not trusted for Accessibility"}
)

// axNode is one accessibility element. The darwin one wraps an AXUIElementRef; tests use a fake.
// Every node returned by children (and the root handed to the walk) is owned by the caller and
// released exactly once.
type axNode interface {
	attr(name string) string // a string, number or boolean attribute as text; "" when unset
	bounds() *[4]int         // AXPosition + AXSize in global points; nil when unknown
	children() []axNode
	press() error
	setValue(text string) error
	focus() error
	release()
}

// interactive are the roles a snapshot keeps (besides the children of a tab group).
var interactive = map[string]bool{
	"AXButton": true, "AXTextField": true, "AXTextArea": true, "AXCheckBox": true,
	"AXRadioButton": true, "AXPopUpButton": true, "AXMenuItem": true, "AXLink": true,
	"AXCell": true, "AXRow": true, "AXComboBox": true, "AXSecureTextField": true,
}

// textRoles hold what the user typed in AXValue: it's their value, never their name.
var textRoles = map[string]bool{"AXTextField": true, "AXTextArea": true, "AXComboBox": true}

// toggleRoles hold 0/1 in AXValue.
var toggleRoles = map[string]bool{"AXCheckBox": true, "AXRadioButton": true}

type entry struct {
	node   axNode
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

// refs is the process's table: one front window is observed at a time.
var refs refTable

// Snapshot walks the focused window (else the main window) of the app with this pid, depth-first,
// and returns at most max interactive elements (DefaultMax when max <= 0) under a new generation.
func Snapshot(ctx context.Context, pid int, max int) (computeruse.Snapshot, error) {
	win, err := frontWindow(pid)
	if err != nil {
		return computeruse.Snapshot{}, err
	}
	return refs.snapshot(ctx, win, max)
}

// snapshot walks root (which it releases, unless kept) and replaces the table with what it kept.
func (t *refTable) snapshot(ctx context.Context, root axNode, max int) (computeruse.Snapshot, error) {
	if max <= 0 {
		max = DefaultMax
	}
	w := walker{ctx: ctx, max: max, nodes: map[string]entry{}}
	if !w.visit(root, "", 0) {
		root.release()
	}
	if w.err != nil {
		for _, e := range w.nodes {
			e.node.release()
		}
		return computeruse.Snapshot{}, w.err
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	for _, e := range t.nodes {
		e.node.release()
	}
	t.gen++
	t.nodes = w.nodes
	return computeruse.Snapshot{Gen: t.gen, Elements: w.out}, nil
}

type walker struct {
	ctx   context.Context
	max   int
	out   []computeruse.SnapshotElement
	nodes map[string]entry
	err   error
}

func (w *walker) full() bool { return w.err != nil || len(w.out) >= w.max }

// visit looks at n and its subtree and reports whether it kept n (the caller releases it if not).
func (w *walker) visit(n axNode, parentRole string, depth int) bool {
	if err := w.ctx.Err(); err != nil {
		w.err = err
		return false
	}
	role := n.attr("AXRole")
	kept := false
	if interactive[role] || parentRole == "AXTabGroup" {
		w.keep(n, role)
		kept = true
	}
	if depth >= maxDepth || w.full() {
		return kept
	}
	for _, c := range n.children() {
		if w.full() || !w.visit(c, role, depth+1) {
			c.release()
		}
	}
	return kept
}

func (w *walker) keep(n axNode, role string) {
	role, secure := secureRole(n, role)
	name, v := describe(n, role, secure)
	el := computeruse.SnapshotElement{Ref: "a" + strconv.Itoa(len(w.out)+1), Role: role, Name: name, Bounds: n.bounds()}
	switch {
	case toggleRoles[role]:
		if v == "1" {
			el.State = append(el.State, "checked")
		}
	case textRoles[role]:
		el.Value = v
	}
	if n.attr("AXEnabled") == "false" {
		el.State = append(el.State, "disabled")
	}
	if n.attr("AXFocused") == "true" {
		el.State = append(el.State, "focused")
	}
	if n.attr("AXSelected") == "true" {
		el.State = append(el.State, "selected")
	}
	w.out = append(w.out, el)
	w.nodes[el.Ref] = entry{node: n, role: role, name: el.Name, secure: secure}
}

// identity reads n's role, reported as AXSecureTextField for a secure text field (a subrole of
// AXTextField) so a snapshot shows it.
func identity(n axNode) (role string, secure bool) { return secureRole(n, n.attr("AXRole")) }

func secureRole(n axNode, role string) (string, bool) {
	if role == "AXSecureTextField" || n.attr("AXSubrole") == "AXSecureTextField" {
		return "AXSecureTextField", true
	}
	return role, false
}

// describe reads n's name and AXValue. The name is the first of AXTitle, AXDescription and
// AXLabel that is set, then AXValue, but only for roles whose value isn't typed text or a toggle
// (and value is "" once it became the name). A secure field's value is never read.
func describe(n axNode, role string, secure bool) (name, value string) {
	for _, a := range []string{"AXTitle", "AXDescription", "AXLabel"} {
		if name = clip(n.attr(a)); name != "" {
			break
		}
	}
	if secure {
		return name, ""
	}
	value = clip(n.attr("AXValue"))
	if name == "" && !textRoles[role] && !toggleRoles[role] {
		return value, ""
	}
	return name, value
}

func clip(s string) string {
	r := []rune(s)
	if len(r) <= maxText {
		return s
	}
	return string(r[:maxText]) + "…"
}
