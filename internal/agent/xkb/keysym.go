// Package xkb is the keyboard vocabulary the native typing backends share: X keysyms for text
// and the few named keys dictation presses, the desktop's keyboard layout read from a keymap, and
// the XKB keymap a Wayland virtual keyboard uploads. Pure Go and OS-independent, so it is unit-tested everywhere.
//
// A keysym names what a key produces, independent of any layout. Typing a character means
// finding (or, for a virtual keyboard, inventing) a key that produces its keysym.
package xkb

import "fmt"

// Stroke is one key press and release: the keysym, optionally with Shift held, and for a
// chord the other modifiers held (ControlMask, Mod1Mask, Mod4Mask).
type Stroke struct {
	Sym   uint32
	Shift bool
	Mods  uint32
}

// Named keysyms (X11 keysymdef.h).
const (
	KeyReturn     uint32 = 0xff0d
	KeyEscape     uint32 = 0xff1b
	KeyTab        uint32 = 0xff09
	KeyISOLeftTab uint32 = 0xfe20
	KeySpace      uint32 = 0x0020
	KeyLeft       uint32 = 0xff51
	KeyUp         uint32 = 0xff52
	KeyRight      uint32 = 0xff53
	KeyDown       uint32 = 0xff54
	KeyShiftL     uint32 = 0xffe1
	KeyControlL   uint32 = 0xffe3
	KeyAltL       uint32 = 0xffe9
	KeySuperL     uint32 = 0xffeb
	KeyBackSpace  uint32 = 0xff08
	KeyDelete     uint32 = 0xffff
	KeyInsert     uint32 = 0xff63
	KeyHome       uint32 = 0xff50
	KeyEnd        uint32 = 0xff57
	KeyPageUp     uint32 = 0xff55 // Prior
	KeyPageDown   uint32 = 0xff56 // Next
	KeyF1         uint32 = 0xffbe // F2..F12 follow: KeyF1 + n - 1
)

var names = map[uint32]string{
	KeyReturn: "Return", KeyEscape: "Escape", KeyTab: "Tab", KeyISOLeftTab: "ISO_Left_Tab",
	KeySpace: "space", KeyLeft: "Left", KeyUp: "Up", KeyRight: "Right", KeyDown: "Down",
	KeyShiftL: "Shift_L", KeyControlL: "Control_L", KeyAltL: "Alt_L", KeySuperL: "Super_L",
	KeyBackSpace: "BackSpace", KeyDelete: "Delete", KeyInsert: "Insert", KeyHome: "Home", KeyEnd: "End",
	KeyPageUp: "Prior", KeyPageDown: "Next",
	KeyF1: "F1", KeyF1 + 1: "F2", KeyF1 + 2: "F3", KeyF1 + 3: "F4", KeyF1 + 4: "F5", KeyF1 + 5: "F6",
	KeyF1 + 6: "F7", KeyF1 + 7: "F8", KeyF1 + 8: "F9", KeyF1 + 9: "F10", KeyF1 + 10: "F11", KeyF1 + 11: "F12",
}

// unicodeBit marks a keysym that is a Unicode code point (keysymdef.h: 0x01000000 + U+xxxx).
const unicodeBit = 0x01000000

// FromRune is r's keysym: Latin-1 printable characters are their own keysym, everything else
// is a Unicode keysym. Control characters and invalid runes have none.
func FromRune(r rune) (uint32, bool) {
	switch {
	case r < 0x20, r >= 0x7f && r < 0xa0, r > 0x10ffff, r >= 0xd800 && r <= 0xdfff:
		return 0, false
	case r < 0x100:
		return uint32(r), true
	}
	return unicodeBit | uint32(r), true
}

// Rune is the character a keysym from FromRune types (0 for named keys).
func Rune(sym uint32) rune {
	switch {
	case sym >= 0x20 && sym < 0x7f, sym >= 0xa0 && sym < 0x100:
		return rune(sym)
	case sym&0xff000000 == unicodeBit:
		return rune(sym &^ unicodeBit)
	}
	return 0
}

// Name is the keysym's name in an XKB keymap. Characters use the "Uxxxx" form, which
// libxkbcommon and Xlib both resolve (Latin-1 ones to their legacy keysyms).
func Name(sym uint32) string {
	if n, ok := names[sym]; ok {
		return n
	}
	if r := Rune(sym); r != 0 {
		return fmt.Sprintf("U%04X", r)
	}
	return fmt.Sprintf("0x%08x", sym)
}

// Strokes turns text into strokes, one per character. ok is false if a character has no
// keysym (the caller sanitizes first, so that means a bug).
func Strokes(text string) ([]Stroke, bool) {
	out := make([]Stroke, 0, len(text))
	for _, r := range text {
		s, ok := FromRune(r)
		if !ok {
			return nil, false
		}
		out = append(out, Stroke{Sym: s})
	}
	return out, true
}
