package dictate

import (
	"context"
	"fmt"
	"unicode/utf16"
)

// KeyInput is one keyboard INPUT for SendInput.
type KeyInput struct {
	VK    uint16
	Scan  uint16
	Flags uint32
}

// SendInput flags and virtual keys (winuser.h).
const (
	keyeventfExtended = 0x0001
	keyeventfKeyUp    = 0x0002
	keyeventfUnicode  = 0x0004

	vkTab, vkReturn, vkShift, vkEscape, vkSpace = 0x09, 0x0D, 0x10, 0x1B, 0x20
	vkLeft, vkUp, vkRight, vkDown               = 0x25, 0x26, 0x27, 0x28
)

// WinKeyboard types with SendInput and KEYEVENTF_UNICODE (layout-independent; surrogate pairs
// go as two units, which Windows recombines). It can't type into a window of an app running
// as administrator (UIPI): SendInput then inserts nothing, and that is reported.
type WinKeyboard struct {
	Desktop func() bool // an interactive desktop (not Session 0)
	Send    func([]KeyInput) (int, error)
}

func (*WinKeyboard) Name() string { return "SendInput" }

func (w *WinKeyboard) Available(context.Context) (bool, string) {
	if w.Send == nil || w.Desktop == nil || !w.Desktop() {
		return false, "typing needs an interactive Windows desktop"
	}
	return true, ""
}

// unicodeInputs is a key down and up per UTF-16 unit.
func unicodeInputs(text string) []KeyInput {
	u := utf16.Encode([]rune(text))
	out := make([]KeyInput, 0, 2*len(u))
	for _, c := range u {
		out = append(out, KeyInput{Scan: c, Flags: keyeventfUnicode}, KeyInput{Scan: c, Flags: keyeventfUnicode | keyeventfKeyUp})
	}
	return out
}

var winKeys = map[Key]uint16{KeyEnter: vkReturn, KeyEscape: vkEscape, KeyTab: vkTab, KeyShiftTab: vkTab, KeySpace: vkSpace,
	KeyUp: vkUp, KeyDown: vkDown, KeyLeft: vkLeft, KeyRight: vkRight}

func keyInputs(k Key) ([]KeyInput, error) {
	vk, ok := winKeys[k]
	if !ok {
		return nil, fmt.Errorf("%w: unknown key %q", errBadArgs, k)
	}
	var flags uint32
	if vk >= vkLeft && vk <= vkDown {
		flags = keyeventfExtended // the arrow keys, not the keypad's
	}
	in := []KeyInput{{VK: vk, Flags: flags}, {VK: vk, Flags: flags | keyeventfKeyUp}}
	if k == KeyShiftTab {
		in = append([]KeyInput{{VK: vkShift}}, append(in, KeyInput{VK: vkShift, Flags: keyeventfKeyUp})...)
	}
	return in, nil
}

func (w *WinKeyboard) send(in []KeyInput) error {
	n, err := w.Send(in)
	if err != nil {
		return err
	}
	if n != len(in) {
		return fmt.Errorf("windows accepted %d of %d key events (an app running as administrator can't be typed into)", n, len(in))
	}
	return nil
}

func (w *WinKeyboard) Type(ctx context.Context, text string) error {
	if ok, why := w.Available(ctx); !ok {
		return fmt.Errorf("%w: %s", ErrUnavailable, why)
	}
	return w.send(unicodeInputs(text))
}

func (w *WinKeyboard) Press(ctx context.Context, k Key) error {
	if ok, why := w.Available(ctx); !ok {
		return fmt.Errorf("%w: %s", ErrUnavailable, why)
	}
	in, err := keyInputs(k)
	if err != nil {
		return err
	}
	return w.send(in)
}
