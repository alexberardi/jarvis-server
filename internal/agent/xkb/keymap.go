package xkb

import (
	"fmt"
	"slices"
	"strings"
)

// MinKeycode is the first XKB keycode (evdev code 1: XKB keycodes are evdev codes + 8).
const MinKeycode = 9

// KeymapName names every section of a generated keymap, so a keymap read back from the
// compositor (it hands the active keyboard's keymap to new clients) can be told apart from a
// real layout.
const KeymapName = "jarvis-agent"

// Key is one key of a generated keymap: its keycode and level 1 and 2 keysyms (0: none).
type Key struct {
	Code uint32
	Syms [2]uint32
}

// Press is one key press and release on a generated keymap, optionally with Shift held.
type Press struct {
	Code  uint32
	Shift bool
}

// Batch is a run of presses typed with one keymap.
type Batch struct {
	Keys    []Key
	Presses []Press
	// Extra are the keysyms the layout doesn't have, put on keycodes it leaves empty: only a
	// client that reads the virtual keyboard's own keymap types them.
	Extra []uint32
}

// Plan maps strokes onto the layout: a keysym the layout has is pressed where the layout has
// it (with Shift for level 2), a keysym it lacks gets one of the layout's empty keycodes.
// When there are more of those than empty keycodes, the strokes go in several batches, each
// with its own keymap, keeping their order. nil layout: US.
func Plan(strokes []Stroke, l *Layout) ([]Batch, error) {
	if l == nil {
		l = US()
	}
	free := l.free()
	var out []Batch
	var cur Batch
	used := map[uint32]bool{}    // keycodes in cur
	extra := map[uint32]uint32{} // keysym → keycode, for cur's extra keysyms
	flush := func() {
		if len(cur.Presses) > 0 {
			slices.SortFunc(cur.Keys, func(a, b Key) int { return int(a.Code) - int(b.Code) })
			out = append(out, cur)
		}
		cur, used, extra = Batch{}, map[uint32]bool{}, map[uint32]uint32{}
	}
	for _, s := range strokes {
		if p, ok := l.pos[s.Sym]; ok {
			if !used[p.code] {
				used[p.code] = true
				cur.Keys = append(cur.Keys, Key{Code: p.code, Syms: l.levels[p.code]})
			}
			cur.Presses = append(cur.Presses, Press{Code: p.code, Shift: p.shift || s.Shift})
			continue
		}
		code, ok := extra[s.Sym]
		if !ok {
			if len(free) == 0 {
				return nil, fmt.Errorf("xkb: the keyboard layout has no free key for %s", Name(s.Sym))
			}
			if len(extra) == len(free) {
				flush()
			}
			code = free[len(extra)]
			extra[s.Sym] = code
			used[code] = true
			cur.Keys = append(cur.Keys, Key{Code: code, Syms: [2]uint32{s.Sym}})
			cur.Extra = append(cur.Extra, s.Sym)
		}
		cur.Presses = append(cur.Presses, Press{Code: code, Shift: s.Shift})
	}
	flush()
	return out, nil
}

// Keymap is the XKB keymap (text v1) for this batch: its keys, with the standard "complete"
// types and compatibility so Shift works, the way wtype builds its own.
func (b Batch) Keymap() string {
	var s strings.Builder
	maxCode := uint32(MinKeycode + 1)
	for _, k := range b.Keys {
		maxCode = max(maxCode, k.Code)
	}
	fmt.Fprintf(&s, "xkb_keymap {\nxkb_keycodes %q {\n", KeymapName)
	fmt.Fprintf(&s, "minimum = %d;\nmaximum = %d;\n", MinKeycode-1, maxCode)
	for _, k := range b.Keys {
		fmt.Fprintf(&s, "<K%d> = %d;\n", k.Code, k.Code)
	}
	fmt.Fprintf(&s, "};\nxkb_types %q { include \"complete\" };\n", KeymapName)
	fmt.Fprintf(&s, "xkb_compatibility %q { include \"complete\" };\n", KeymapName)
	fmt.Fprintf(&s, "xkb_symbols %q {\n", KeymapName)
	for _, k := range b.Keys {
		levels := Name(k.Syms[0])
		if k.Syms[0] == 0 {
			levels = "NoSymbol"
		}
		if k.Syms[1] != 0 {
			levels += ", " + Name(k.Syms[1])
		}
		fmt.Fprintf(&s, "key <K%d> {[ %s ]};\n", k.Code, levels)
	}
	s.WriteString("};\n};\n")
	return s.String()
}

// ShiftMask is Shift in a serialized XKB modifier mask (real modifier 0 in every keymap).
const ShiftMask uint32 = 1
