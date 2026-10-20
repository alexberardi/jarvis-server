package xkb

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Layout is where the desktop's own keyboard layout puts keysyms: for each XKB keycode, what
// it produces on levels 1 and 2 (Shift). A virtual keyboard types a keysym on the keycode the
// layout already has it on, so a client that reads keys with the layout instead of the
// virtual keyboard's keymap (XWayland on Hyprland does: DESIGN.md §14) still sees the right
// character, and the compositor's own key bindings, which it resolves with its layout too,
// see an ordinary key. Keysyms the layout lacks go on keycodes it leaves empty.
type Layout struct {
	levels map[uint32][2]uint32 // keycode → level 1 and 2 keysyms (0: none)
	pos    map[uint32]pos       // keysym → where it is (lowest keycode, level 1 first)
	// firstFree: keycodes below it are never free. The built-in US layout only knows the
	// main block, so it keeps clear of every keycode a real (evdev) keymap may use.
	firstFree uint32
}

type pos struct {
	code  uint32
	shift bool
}

// MaxKeycode is the largest keycode a generated keymap uses (evdev's KEY_MAX + 8).
const MaxKeycode = 0x2ff + 8

// MaxX11Keycode is the largest keycode X11 (and so XWayland) can see.
const MaxX11Keycode = 255

var (
	reKeycode = regexp.MustCompile(`^\s*<([^>]+)>\s*=\s*(\d+)\s*;`)
	reKeyHead = regexp.MustCompile(`^\s*key\s+<([^>]+)>\s*\{(.*)$`)
	reType    = regexp.MustCompile(`type(?:\[[^\]]*\])?\s*=\s*"([^"]*)"`)
	reLevels  = regexp.MustCompile(`\[([^\]]*)\]`)
)

// Key types whose level 2 is Shift (implicit types are inferred the same way for the keysyms
// typed here). Others (KEYPAD: NumLock; CTRL+ALT; PC_*) are skipped.
var shiftTypes = map[string]bool{"": true, "ONE_LEVEL": true, "TWO_LEVEL": true, "ALPHABETIC": true,
	"FOUR_LEVEL": true, "FOUR_LEVEL_ALPHABETIC": true, "FOUR_LEVEL_SEMIALPHABETIC": true}

// ParseKeymap reads a keymap in XKB text format v1, as a compositor sends it with
// wl_keyboard.keymap: group 1, levels 1 and 2 of keycodes up to MaxKeycode.
func ParseKeymap(text string) (*Layout, error) {
	section := ""
	codes := map[string]uint32{}
	type key struct {
		name string
		body string
	}
	var keys []key
	cur := -1 // the key being read, across lines
	for line := range strings.Lines(text) {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "xkb_keycodes"):
			section = "keycodes"
			continue
		case strings.HasPrefix(t, "xkb_symbols"):
			section = "symbols"
			continue
		case strings.HasPrefix(t, "xkb_types"), strings.HasPrefix(t, "xkb_compat"), strings.HasPrefix(t, "xkb_geometry"):
			section = "other"
			continue
		}
		switch section {
		case "keycodes":
			if m := reKeycode.FindStringSubmatch(t); m != nil {
				if n, err := strconv.ParseUint(m[2], 10, 32); err == nil {
					codes[m[1]] = uint32(n)
				}
			}
		case "symbols":
			if cur < 0 {
				m := reKeyHead.FindStringSubmatch(t)
				if m == nil {
					continue
				}
				keys = append(keys, key{name: m[1]})
				cur, t = len(keys)-1, m[2]
			}
			keys[cur].body += " " + t
			if strings.Contains(t, "};") {
				cur = -1
			}
		}
	}
	l := &Layout{levels: map[uint32][2]uint32{}, pos: map[uint32]pos{}}
	for _, k := range keys {
		code, ok := codes[k.name]
		if !ok || code < MinKeycode || code > MaxKeycode {
			continue
		}
		typ := ""
		if m := reType.FindStringSubmatch(k.body); m != nil {
			typ = m[1]
		}
		lv := reLevels.FindStringSubmatch(k.body) // the first list is group 1
		if lv == nil {
			continue
		}
		var syms [2]uint32
		for i, f := range strings.Split(lv[1], ",") {
			if i == 2 {
				break
			}
			syms[i] = ParseSym(strings.TrimSpace(f))
		}
		if !shiftTypes[typ] {
			// Still taken: its keycode isn't free for other keysyms.
			l.levels[code] = [2]uint32{}
			continue
		}
		if typ == "ONE_LEVEL" {
			syms[1] = 0
		}
		l.levels[code] = syms
	}
	if len(l.levels) == 0 {
		return nil, errors.New("xkb: no keys in the keymap")
	}
	l.index()
	return l, nil
}

func (l *Layout) index() {
	best := map[uint32]int{}
	for code, syms := range l.levels {
		for lvl, s := range syms {
			if s == 0 {
				continue
			}
			r := rank(code, lvl)
			if b, seen := best[s]; seen && b <= r {
				continue
			}
			best[s] = r
			l.pos[s] = pos{code: code, shift: lvl == 1}
		}
	}
}

// mainBlockMax is the last keycode of a PC keyboard's main and editing blocks (evdev KEY_RIGHTMETA
// is 126+8 = 134; LSGT, the 102nd key, is 94).
const mainBlockMax = 135

// rank orders the places a keysym is found, best first: a keycode X11 can see (XWayland reads
// no other), then one on a real keyboard's main block (evdev keymaps also put "(" or "$" on
// rare multimedia keycodes), then level 1, then the lowest keycode.
func rank(code uint32, lvl int) int {
	r := int(code) + lvl*1_000
	if code > mainBlockMax {
		r += 10_000
	}
	if code > MaxX11Keycode {
		r += 100_000
	}
	return r
}

// Complete reports whether l is a whole keyboard layout (letters, digits, space, Return), not
// a fragment such as another virtual keyboard's keymap.
func (l *Layout) Complete() bool {
	if l == nil {
		return false
	}
	need := []uint32{KeySpace, KeyReturn, KeyTab, KeyEscape}
	for c := 'a'; c <= 'z'; c++ {
		need = append(need, uint32(c))
	}
	for c := '0'; c <= '9'; c++ {
		need = append(need, uint32(c))
	}
	for _, s := range need {
		if _, ok := l.pos[s]; !ok {
			return false
		}
	}
	return true
}

// Has reports whether the layout types sym.
func (l *Layout) Has(sym uint32) bool {
	_, ok := l.pos[sym]
	return ok
}

// free lists the keycodes the layout leaves empty, the ones X11 can see first.
func (l *Layout) free() []uint32 {
	var low, high []uint32
	for c := max(uint32(MinKeycode), l.firstFree); c <= MaxKeycode; c++ {
		if _, used := l.levels[c]; used {
			continue
		}
		if c <= MaxX11Keycode {
			low = append(low, c)
		} else {
			high = append(high, c)
		}
	}
	return append(low, high...)
}

// ParseSym reads a keysym as a keymap writes it: hex (0x68, as Hyprland and libxkbcommon
// serialise), a Unicode name (U0068), or one of the names this package knows. 0: unknown.
func ParseSym(s string) uint32 {
	switch {
	case s == "", s == "NoSymbol":
		return 0
	case strings.HasPrefix(s, "0x"):
		v, err := strconv.ParseUint(s[2:], 16, 32)
		if err != nil {
			return 0
		}
		return uint32(v)
	case len(s) >= 5 && s[0] == 'U':
		v, err := strconv.ParseUint(s[1:], 16, 32)
		if err != nil {
			return 0
		}
		if sym, ok := FromRune(rune(v)); ok {
			return sym
		}
		return 0
	case len(s) == 1 && s[0] > ' ' && s[0] < 0x7f:
		return uint32(s[0]) // letters and digits are their own name
	}
	for sym, n := range names {
		if n == s {
			return sym
		}
	}
	if sym, ok := asciiNames[s]; ok {
		return sym
	}
	return 0
}

// asciiNames are the keysym names of ASCII punctuation (keysymdef.h).
var asciiNames = map[string]uint32{
	"exclam": '!', "quotedbl": '"', "numbersign": '#', "dollar": '$', "percent": '%', "ampersand": '&',
	"apostrophe": '\'', "parenleft": '(', "parenright": ')', "asterisk": '*', "plus": '+', "comma": ',',
	"minus": '-', "period": '.', "slash": '/', "colon": ':', "semicolon": ';', "less": '<', "equal": '=',
	"greater": '>', "question": '?', "at": '@', "bracketleft": '[', "backslash": '\\', "bracketright": ']',
	"asciicircum": '^', "underscore": '_', "grave": '`', "braceleft": '{', "bar": '|', "braceright": '}',
	"asciitilde": '~',
}

// usRows is the US layout's main block, the layout to fall back on when the compositor's
// can't be read: keycode, then level 1 and 2 (a row of the same keyboard row, left to right).
var usRows = []struct {
	first  uint32
	l1, l2 string
}{
	{10, "1234567890-=", "!@#$%^&*()_+"},
	{24, "qwertyuiop[]", "QWERTYUIOP{}"},
	{38, "asdfghjkl;'`", "ASDFGHJKL:\"~"},
	{51, "\\zxcvbnm,./", "|ZXCVBNM<>?"},
}

// usNamed are the US layout's named keys (evdev keycodes + 8); occupied keys (the right-hand
// modifiers, Caps Lock) have no keysym.
var usNamed = map[uint32]uint32{9: KeyEscape, 22: KeyBackSpace, 23: KeyTab, 36: KeyReturn, 37: KeyControlL,
	50: KeyShiftL, 62: 0, 64: KeyAltL, 65: KeySpace, 66: 0, 108: 0, 105: 0, 111: KeyUp, 113: KeyLeft,
	114: KeyRight, 116: KeyDown, 133: KeySuperL,
	67: KeyF1, 68: KeyF1 + 1, 69: KeyF1 + 2, 70: KeyF1 + 3, 71: KeyF1 + 4, 72: KeyF1 + 5, 73: KeyF1 + 6,
	74: KeyF1 + 7, 75: KeyF1 + 8, 76: KeyF1 + 9, 95: KeyF1 + 10, 96: KeyF1 + 11,
	110: KeyHome, 112: KeyPageUp, 115: KeyEnd, 117: KeyPageDown, 118: KeyInsert, 119: KeyDelete}

// usFirstFree is past every keycode with a keysym in a standard evdev keymap (709 in
// libxkbcommon's, with the inet extras).
const usFirstFree = 710

// US is the built-in US layout (main block only).
func US() *Layout {
	l := &Layout{levels: map[uint32][2]uint32{}, pos: map[uint32]pos{}, firstFree: usFirstFree}
	for _, r := range usRows {
		l2 := []rune(r.l2)
		for i, c := range []rune(r.l1) {
			l.levels[r.first+uint32(i)] = [2]uint32{uint32(c), uint32(l2[i])}
		}
	}
	for code, sym := range usNamed {
		l.levels[code] = [2]uint32{sym}
	}
	l.levels[23] = [2]uint32{KeyTab, KeyISOLeftTab}
	l.index()
	return l
}
