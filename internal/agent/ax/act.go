package ax

import (
	"fmt"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// Press performs AXPress on ref from snapshot generation gen.
func Press(gen int, ref string) error { return refs.press(gen, ref) }

// SetValue sets ref's AXValue to text. A secure text field is refused (password_field): typing a
// password is the user's.
func SetValue(gen int, ref, text string) error { return refs.setValue(gen, ref, text) }

// Focus sets AXFocused on ref.
func Focus(gen int, ref string) error { return refs.focus(gen, ref) }

// Element is what the deny check and the risk classifier know about ref.
func Element(gen int, ref string) (*computeruse.Element, error) { return refs.element(gen, ref) }

// ElementAt returns the element under global point (x, y), for the deny check on a coordinate
// action and for recording; nil when there's none.
func ElementAt(x, y float64) (*computeruse.Element, error) { return elementAt(x, y) }

// lookup finds ref in generation gen. It holds the lock while f acts, so a new snapshot can't
// release the element under it.
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

func (t *refTable) press(gen int, ref string) error {
	return t.lookup(gen, ref, func(e entry) error { return e.node.press() })
}

func (t *refTable) setValue(gen int, ref, text string) error {
	return t.lookup(gen, ref, func(e entry) error {
		if e.secure {
			return &Error{Code: CodePasswordField, Msg: ref + " is a secure text field; the user types passwords"}
		}
		return e.node.setValue(text)
	})
}

func (t *refTable) focus(gen int, ref string) error {
	return t.lookup(gen, ref, func(e entry) error { return e.node.focus() })
}

func (t *refTable) element(gen int, ref string) (*computeruse.Element, error) {
	var el *computeruse.Element
	err := t.lookup(gen, ref, func(e entry) error {
		el = &computeruse.Element{Role: e.role, Name: e.name, Secure: e.secure}
		return nil
	})
	return el, err
}
