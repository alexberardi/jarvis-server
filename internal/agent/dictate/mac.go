package dictate

import (
	"context"
	"fmt"
)

// MacPoster posts keyboard events (cgevent_darwin.go: CGEvent over purego; a stub elsewhere).
type MacPoster interface {
	// Unicode posts one key down/up carrying these UTF-16 units (at most 20).
	Unicode(units []uint16) error
	// Key posts a virtual key code down/up, with Shift held when shift is set.
	Key(code uint16, shift bool) error
}

// MacKeyboard types with CGEvent keyboard events carrying Unicode strings, so the keyboard
// layout doesn't matter. Posting events needs the Accessibility grant the agent already asks
// for (macax.Trusted).
type MacKeyboard struct {
	Trusted func() bool
	Post    MacPoster
}

// macKeys are kVK_* virtual key codes (HIToolbox Events.h).
var macKeys = map[Key]uint16{KeyEnter: 36, KeyTab: 48, KeyShiftTab: 48, KeySpace: 49, KeyEscape: 53,
	KeyLeft: 123, KeyRight: 124, KeyDown: 125, KeyUp: 126}

func (*MacKeyboard) Name() string { return "CGEvent" }

func (m *MacKeyboard) Available(context.Context) (bool, string) {
	if m.Post == nil {
		return false, "typing isn't supported in this build"
	}
	if m.Trusted == nil || !m.Trusted() {
		return false, "typing needs Accessibility for Jarvis Automation Agent: turn it on in System Settings > Privacy & Security > Accessibility"
	}
	return true, ""
}

func (m *MacKeyboard) Type(ctx context.Context, text string) error {
	if ok, why := m.Available(ctx); !ok {
		return fmt.Errorf("%w: %s", ErrUnavailable, why)
	}
	for _, c := range utf16Chunks(text, 20) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.Post.Unicode(c); err != nil {
			return err
		}
	}
	return nil
}

func (m *MacKeyboard) Press(ctx context.Context, k Key) error {
	if ok, why := m.Available(ctx); !ok {
		return fmt.Errorf("%w: %s", ErrUnavailable, why)
	}
	code, ok := macKeys[k]
	if !ok {
		return fmt.Errorf("%w: unknown key %q", errBadArgs, k)
	}
	return m.Post.Key(code, k == KeyShiftTab)
}
