package ax

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// fakeNode is an accessibility element in memory. It counts releases so the tests can check that
// every node is released exactly once: the walker's own nodes at once, the ref table's when the
// next generation replaces them.
type fakeNode struct {
	attrs    map[string]string
	box      *[4]int
	kids     []*fakeNode
	released *int
	pressed  int
	focused  int
	value    string
	gone     bool
	fetched  int    // children() calls
	onFetch  func() // runs on each children() call
}

func (n *fakeNode) attr(name string) string { return n.attrs[name] }
func (n *fakeNode) bounds() *[4]int         { return n.box }
func (n *fakeNode) children() []axNode {
	n.fetched++
	if n.onFetch != nil {
		n.onFetch()
	}
	out := make([]axNode, len(n.kids))
	for i, k := range n.kids {
		out[i] = k
	}
	return out
}
func (n *fakeNode) release() { *n.released++ }
func (n *fakeNode) press() error {
	if n.gone {
		return errGone
	}
	n.pressed++
	return nil
}
func (n *fakeNode) setValue(s string) error { n.value = s; return nil }
func (n *fakeNode) focus() error            { n.focused++; return nil }

var errGone = &Error{Code: CodeStaleRef, Msg: "element is gone"}

// tree builds fake nodes sharing one release counter.
type tree struct{ released int }

func (t *tree) n(role string, attrs map[string]string, kids ...*fakeNode) *fakeNode {
	a := map[string]string{"AXRole": role}
	for k, v := range attrs {
		a[k] = v
	}
	return &fakeNode{attrs: a, kids: kids, released: &t.released}
}

func (t *tree) count(root *fakeNode) int {
	c := 1
	for _, k := range root.kids {
		c += t.count(k)
	}
	return c
}

func roles(s computeruse.Snapshot) []string {
	var out []string
	for _, e := range s.Elements {
		out = append(out, e.Ref+":"+e.Role+":"+e.Name)
	}
	return out
}

func TestSnapshotRoles(t *testing.T) {
	var tr tree
	ok := tr.n("AXButton", map[string]string{"AXTitle": "OK"})
	ok.box = &[4]int{10, 20, 80, 24}
	win := tr.n("AXWindow", map[string]string{"AXTitle": "Notes"},
		tr.n("AXGroup", nil,
			tr.n("AXStaticText", map[string]string{"AXValue": "Hello"}),
			ok,
			tr.n("AXTextField", map[string]string{"AXDescription": "Search", "AXValue": "milk", "AXFocused": "true"}),
			tr.n("AXCheckBox", map[string]string{"AXTitle": "Pinned", "AXValue": "1", "AXEnabled": "false"}),
		),
		tr.n("AXTabGroup", nil,
			tr.n("AXRadioButton", map[string]string{"AXTitle": "General", "AXValue": "1"}),
			tr.n("AXGroup", map[string]string{"AXDescription": "Advanced"}),
		),
		tr.n("AXOutline", nil,
			tr.n("AXRow", map[string]string{"AXSelected": "true"},
				tr.n("AXCell", map[string]string{"AXDescription": "Groceries"}),
			),
		),
		tr.n("AXLink", map[string]string{"AXValue": "Help"}),
		tr.n("AXImage", map[string]string{"AXDescription": "logo"}),
		tr.n("AXTextField", map[string]string{"AXValue": "eggs"}),
	)
	total := tr.count(win)

	var refs refTable
	s, err := refs.snapshot(context.Background(), win, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"a1:AXButton:OK", "a2:AXTextField:Search", "a3:AXCheckBox:Pinned",
		"a4:AXRadioButton:General", "a5:AXGroup:Advanced",
		"a6:AXRow:", "a7:AXCell:Groceries", "a8:AXLink:Help",
		"a9:AXTextField:", // typed text is never a name, even with no title
	}
	if got := roles(s); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("elements\n got %v\nwant %v", got, want)
	}
	if s.Gen != 1 {
		t.Errorf("gen = %d, want 1", s.Gen)
	}
	el := s.Elements
	if el[0].Bounds == nil || *el[0].Bounds != [4]int{10, 20, 80, 24} {
		t.Errorf("button bounds = %v", el[0].Bounds)
	}
	if el[1].Value != "milk" || !has(el[1].State, "focused") {
		t.Errorf("text field = %+v, want value milk and focused", el[1])
	}
	if el[2].Value != "" || !has(el[2].State, "checked") || !has(el[2].State, "disabled") {
		t.Errorf("checkbox = %+v, want checked, disabled and no value", el[2])
	}
	if !has(el[3].State, "checked") || !has(el[5].State, "selected") {
		t.Errorf("tab %+v should be checked, row %+v selected", el[3], el[5])
	}
	if el[8].Value != "eggs" {
		t.Errorf("untitled text field = %+v, want value eggs", el[8])
	}
	if el[7].Value != "" {
		t.Errorf("link value %q: a name taken from AXValue isn't repeated as the value", el[7].Value)
	}

	// Everything the walker didn't keep is released now; the kept ones when the next
	// generation replaces them.
	if tr.released != total-len(el) {
		t.Errorf("released %d of %d after the walk, want %d", tr.released, total, total-len(el))
	}
	s2, err := refs.snapshot(context.Background(), tr.n("AXWindow", nil), 0)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Gen != 2 || len(s2.Elements) != 0 {
		t.Errorf("second snapshot = %+v", s2)
	}
	if tr.released != total+1 {
		t.Errorf("released %d, want all %d nodes once", tr.released, total+1)
	}
}

func TestSecureFieldHidden(t *testing.T) {
	var tr tree
	pw := tr.n("AXTextField", map[string]string{"AXSubrole": "AXSecureTextField", "AXValue": "hunter2"})
	pw2 := tr.n("AXSecureTextField", map[string]string{"AXTitle": "Password", "AXValue": "hunter2"})
	win := tr.n("AXWindow", nil, pw, pw2)

	var refs refTable
	s, err := refs.snapshot(context.Background(), win, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Elements) != 2 {
		t.Fatalf("elements = %v", roles(s))
	}
	for _, e := range s.Elements {
		if e.Role != "AXSecureTextField" || e.Value != "" || strings.Contains(e.Name, "hunter2") {
			t.Errorf("secure field leaked: %+v", e)
		}
	}
	if s.Elements[1].Name != "Password" {
		t.Errorf("name = %q, want the title", s.Elements[1].Name)
	}

	err = refs.setValue(s.Gen, "a1", "secret")
	if code(err) != CodePasswordField {
		t.Errorf("SetValue on a secure field = %v, want %s", err, CodePasswordField)
	}
	if pw.value != "" {
		t.Errorf("secure field was set to %q", pw.value)
	}
	if el, _ := refs.element(s.Gen, "a1"); el == nil || !el.Secure {
		t.Errorf("element = %+v, want Secure", el)
	}
}

func TestMaxElements(t *testing.T) {
	var tr tree
	var kids []*fakeNode
	for i := range 20 {
		kids = append(kids, tr.n("AXGroup", nil, tr.n("AXButton", map[string]string{"AXTitle": fmt.Sprint("b", i)})))
	}
	win := tr.n("AXWindow", nil, kids...)

	var refs refTable
	s, err := refs.snapshot(context.Background(), win, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Elements) != 5 || s.Elements[4].Name != "b4" {
		t.Fatalf("elements = %v, want b0..b4", roles(s))
	}
	if b4 := kids[4].kids[0]; b4.fetched != 0 {
		t.Errorf("fetched the children of the last kept node %d times after the cap", b4.fetched)
	}
	// Handed out: the window, its 20 groups and the first 5 groups' buttons. The other groups'
	// children are never fetched once the cap is reached.
	if want := 1 + 20 + 5 - 5; tr.released != want {
		t.Errorf("released %d, want every node handed out but the 5 kept (%d)", tr.released, want)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := refs.snapshot(ctx, tr.n("AXWindow", nil, tr.n("AXButton", nil)), 5); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled snapshot err = %v", err)
	}
}

// A walk cancelled after it kept some nodes releases them, and leaves the table as it was.
func TestCancelMidWalk(t *testing.T) {
	var tr tree
	var refs refTable
	prev, err := refs.snapshot(context.Background(), tr.n("AXWindow", nil, tr.n("AXButton", nil)), 0)
	if err != nil {
		t.Fatal(err)
	}
	tr.released = 0

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	late := tr.n("AXGroup", nil, tr.n("AXButton", nil), tr.n("AXButton", nil))
	late.onFetch = cancel
	win := tr.n("AXWindow", nil, tr.n("AXButton", nil), tr.n("AXCheckBox", nil), late)
	total := tr.count(win)
	if _, err := refs.snapshot(ctx, win, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
	if tr.released != total {
		t.Errorf("released %d of %d nodes handed out", tr.released, total)
	}
	if refs.gen != prev.Gen || len(refs.nodes) != 1 {
		t.Errorf("table = gen %d with %d nodes, want the previous generation untouched", refs.gen, len(refs.nodes))
	}
}

func TestActByRef(t *testing.T) {
	var tr tree
	btn := tr.n("AXButton", map[string]string{"AXTitle": "OK"})
	field := tr.n("AXTextField", nil)
	var refs refTable
	s, err := refs.snapshot(context.Background(), tr.n("AXWindow", nil, btn, field), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := refs.press(s.Gen, "a1"); err != nil || btn.pressed != 1 {
		t.Errorf("press = %v, pressed %d", err, btn.pressed)
	}
	if err := refs.setValue(s.Gen, "a2", "milk"); err != nil || field.value != "milk" {
		t.Errorf("setValue = %v, value %q", err, field.value)
	}
	if err := refs.focus(s.Gen, "a2"); err != nil || field.focused != 1 {
		t.Errorf("focus = %v, focused %d", err, field.focused)
	}
	if err := refs.press(s.Gen, "a9"); code(err) != CodeNotFound {
		t.Errorf("unknown ref = %v, want %s", err, CodeNotFound)
	}
	btn.gone = true
	if err := refs.press(s.Gen, "a1"); code(err) != CodeStaleRef {
		t.Errorf("vanished element = %v, want %s", err, CodeStaleRef)
	}
}

func TestStaleRef(t *testing.T) {
	var tr tree
	btn := tr.n("AXButton", map[string]string{"AXTitle": "OK"})
	var refs refTable
	old, _ := refs.snapshot(context.Background(), tr.n("AXWindow", nil, btn), 0)
	if _, err := refs.snapshot(context.Background(), tr.n("AXWindow", nil, tr.n("AXButton", nil)), 0); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"press":    refs.press(old.Gen, "a1"),
		"setValue": refs.setValue(old.Gen, "a1", "x"),
		"focus":    refs.focus(old.Gen, "a1"),
	} {
		if code(err) != CodeStaleRef {
			t.Errorf("%s with gen %d = %v, want %s", name, old.Gen, err, CodeStaleRef)
		}
	}
	if btn.pressed != 0 {
		t.Error("a stale ref acted on the old element")
	}
}

func has(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
