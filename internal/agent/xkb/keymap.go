package xkb

import (
	"fmt"
	"strings"
)

// MinKeycode is the first XKB keycode a generated keymap uses (evdev code 1: XKB keycodes are
// evdev codes + 8).
const MinKeycode = 9

// MaxKeysPerMap keeps every keycode ≤ 255: X11 (XWayland clients) can't see keycodes above
// that, so text with more distinct characters is typed in several batches, each with its own
// keymap.
const MaxKeysPerMap = 255 - MinKeycode + 1

// Batch is a run of strokes typed with one keymap: Syms[i] is on XKB keycode MinKeycode+i.
type Batch struct {
	Syms    []uint32
	Strokes []Stroke
}

// Keycode is the XKB keycode producing sym in this batch, or 0.
func (b Batch) Keycode(sym uint32) uint32 {
	for i, s := range b.Syms {
		if s == sym {
			return uint32(MinKeycode + i)
		}
	}
	return 0
}

// Plan splits strokes into batches of at most maxKeys distinct keysyms, keeping their order.
func Plan(strokes []Stroke, maxKeys int) []Batch {
	if maxKeys <= 0 || maxKeys > MaxKeysPerMap {
		maxKeys = MaxKeysPerMap
	}
	var out []Batch
	var cur Batch
	seen := map[uint32]bool{}
	for _, s := range strokes {
		if !seen[s.Sym] {
			if len(cur.Syms) == maxKeys {
				out = append(out, cur)
				cur, seen = Batch{}, map[uint32]bool{}
			}
			seen[s.Sym] = true
			cur.Syms = append(cur.Syms, s.Sym)
		}
		cur.Strokes = append(cur.Strokes, s)
	}
	if len(cur.Strokes) > 0 {
		out = append(out, cur)
	}
	return out
}

// Keymap is the XKB keymap (text v1) for this batch: one key per keysym, the standard
// "complete" types and compatibility so Shift works, the way wtype builds its own. Tab gets
// ISO_Left_Tab on its shifted level, like a real keyboard, so Shift+Tab reads as a back-tab to
// toolkits that look at the keysym.
func (b Batch) Keymap() string {
	var s strings.Builder
	s.WriteString("xkb_keymap {\nxkb_keycodes \"(unnamed)\" {\n")
	fmt.Fprintf(&s, "minimum = %d;\nmaximum = %d;\n", MinKeycode-1, max(MinKeycode+len(b.Syms), MinKeycode+1))
	for i := range b.Syms {
		fmt.Fprintf(&s, "<K%d> = %d;\n", i+1, MinKeycode+i)
	}
	s.WriteString("};\nxkb_types \"(unnamed)\" { include \"complete\" };\n")
	s.WriteString("xkb_compatibility \"(unnamed)\" { include \"complete\" };\n")
	s.WriteString("xkb_symbols \"(unnamed)\" {\n")
	for i, sym := range b.Syms {
		levels := Name(sym)
		if sym == KeyTab {
			levels += ", " + Name(KeyISOLeftTab)
		}
		fmt.Fprintf(&s, "key <K%d> {[ %s ]};\n", i+1, levels)
	}
	s.WriteString("};\n};\n")
	return s.String()
}

// ShiftMask is Shift in a serialized XKB modifier mask (real modifier 0 in every keymap).
const ShiftMask uint32 = 1
