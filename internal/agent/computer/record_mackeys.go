package computer

import (
	"strings"
	"time"
	"unicode"
)

// macOS key codes (HIToolbox's kVK_*) and event flags (kCGEventFlagMask*) a desktop recording
// tells apart. Platform-free so the decoding tests everywhere.
const (
	vkReturn      = 36
	vkTab         = 48
	vkDelete      = 51 // Backspace
	vkEscape      = 53
	vkKeypadEnter = 76

	macFlagShift = 1 << 17
	macFlagCtrl  = 1 << 18
	macFlagAlt   = 1 << 19
	macFlagCmd   = 1 << 20
)

var macNamedKeys = map[uint16]string{vkReturn: KeyEnter, vkKeypadEnter: KeyEnter, vkTab: KeyTab, vkEscape: KeyEscape, vkDelete: KeyBackspace}

// keyOther is a key that types nothing (an arrow, a function key): the grouper skips it.
const keyOther = "other"

// macKeyInput decodes one key down. text reads the characters the key types; it is never
// called while secure input is on, so a password's keystrokes never leave the tap.
func macKeyInput(code uint16, flags uint64, secure bool, at time.Time, text func() string) DeskInput {
	in := DeskInput{Kind: InKey, At: at, Secure: secure}
	named := macNamedKeys[code]
	shortcut := flags&(macFlagCmd|macFlagCtrl) != 0
	if secure {
		if named != KeyBackspace && !shortcut {
			in.Key = named // Enter, Tab and Escape say nothing of what was typed
		}
		return in
	}
	t := ""
	if named == "" {
		t = text()
	}
	if shortcut {
		base := strings.ToLower(named)
		if base == "" {
			base = shortcutBase(t)
		}
		if base == "" {
			in.Key = keyOther
			return in
		}
		var mods []string
		for _, m := range []struct {
			flag uint64
			name string
		}{{macFlagCtrl, "ctrl"}, {macFlagAlt, "alt"}, {macFlagShift, "shift"}, {macFlagCmd, "cmd"}} {
			if flags&m.flag != 0 {
				mods = append(mods, m.name)
			}
		}
		in.Key = strings.Join(append(mods, base), "+")
		return in
	}
	if named != "" {
		in.Key = named
		return in
	}
	if t == "" || !typable(t) {
		in.Key = keyOther
		return in
	}
	in.Text = t
	return in
}

// typable: no control characters and none of the private-use ones macOS gives arrows and
// function keys (U+F700–U+F8FF).
func typable(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || (r >= 0xF700 && r <= 0xF8FF) {
			return false
		}
	}
	return true
}

// shortcutBase is the key under a shortcut's modifiers: Control turns a letter into its
// control character, which is mapped back.
func shortcutBase(t string) string {
	r := []rune(t)
	if len(r) != 1 {
		return ""
	}
	switch c := r[0]; {
	case c >= 1 && c <= 26:
		return string('a' + c - 1)
	case typable(t) && !unicode.IsSpace(c):
		return strings.ToLower(t)
	}
	return ""
}
