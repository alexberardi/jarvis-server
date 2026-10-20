package xkb

import (
	"slices"
	"strings"
	"testing"
)

func TestFromRune(t *testing.T) {
	for _, c := range []struct {
		r    rune
		sym  uint32
		ok   bool
		name string
	}{
		{'a', 0x61, true, "U0061"}, {' ', 0x20, true, "space"}, {'~', 0x7e, true, "U007E"},
		{'é', 0xe9, true, "U00E9"}, {'ÿ', 0xff, true, "U00FF"},
		{'✓', 0x1002713, true, "U2713"}, {'日', 0x10065e5, true, "U65E5"}, {'😀', 0x101f600, true, "U1F600"},
		{'\n', 0, false, ""}, {0x7f, 0, false, ""}, {0x85, 0, false, ""}, {0xd800, 0, false, ""},
	} {
		sym, ok := FromRune(c.r)
		if sym != c.sym || ok != c.ok {
			t.Errorf("FromRune(%U) = %#x %v, want %#x %v", c.r, sym, ok, c.sym, c.ok)
		}
		if ok {
			if Rune(sym) != c.r {
				t.Errorf("Rune(%#x) = %U", sym, Rune(sym))
			}
			if Name(sym) != c.name {
				t.Errorf("Name(%#x) = %s, want %s", sym, Name(sym), c.name)
			}
		}
	}
	if Name(KeyReturn) != "Return" || Name(KeyISOLeftTab) != "ISO_Left_Tab" || Rune(KeyReturn) != 0 {
		t.Fatal("named keys")
	}
}

func TestStrokes(t *testing.T) {
	s, ok := Strokes("ab日")
	if !ok || len(s) != 3 || s[2].Sym != 0x10065e5 || s[0].Shift {
		t.Fatalf("%v %v", s, ok)
	}
	if _, ok := Strokes("a\x1bb"); ok {
		t.Fatal("control character got a keysym")
	}
}

// hyprKeymap is the shape of the keymap Hyprland hands a new wl_keyboard (keysyms in hex, a
// multi-line key with an explicit type, a keypad key that isn't Shift-levelled), plus a key
// written with names as other compositors serialise.
const hyprKeymap = `xkb_keymap {
xkb_keycodes "evdev_aliases(qwerty)" {
	minimum = 8;
	maximum = 709;
	<ESC> = 9;
	<AE01> = 10;
	<TAB> = 23;
	<AD01> = 24;
	<RTRN> = 36;
	<AC06> = 43;
	<LFSH> = 50;
	<AB08> = 59;
	<SPCE> = 65;
	<KPMU> = 63;
	<I120> = 120;
	alias <MENU> = <COMP>;
	indicator 1 = "Caps Lock";
};
xkb_types "complete" {
	type "ONE_LEVEL" {
		modifiers= none;
		level_name[1]= "Any";
	};
};
xkb_compatibility "complete" {
	interpret Shift_L+AnyOf(all) {
		action= SetMods(modifiers=Shift);
	};
};
xkb_symbols "pc_us_inet(evdev)" {
	name[1]="English (US)";

	key <ESC> {	[ 0xff1b ] };
	key <AE01> {	[ 0x31, 0x21 ] };
	key <TAB> {	[ 0xff09, 0xfe20 ] };
	key <AD01> {	[ 0x71, 0x51 ] };
	key <RTRN> {	[ 0xff0d ] };
	key <AC06> {	[ h, H ] };
	key <LFSH> {
		type= "ALPHABETIC",
		symbols[1]= [ 0xffe1, 0xffe5 ]
	};
	key <AB08> {	[ comma, less ], [ U00E9, U00C9 ] };
	key <SPCE> {	[ 0x20 ] };
	key <KPMU> {
		type= "CTRL+ALT",
		symbols[1]= [ 0xffaa, 0xffaa, 0xffaa, 0xffaa, 0x1008fe21 ]
	};
	modifier_map Shift { <LFSH> };
};
};
`

func TestParseKeymap(t *testing.T) {
	l, err := ParseKeymap(hyprKeymap)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		sym   uint32
		code  uint32
		shift bool
	}{
		{'1', 10, false}, {'!', 10, true}, {KeyTab, 23, false}, {KeyISOLeftTab, 23, true}, {'q', 24, false},
		{'Q', 24, true}, {KeyReturn, 36, false}, {'h', 43, false}, {'H', 43, true}, {',', 59, false},
		{'<', 59, true}, {' ', 65, false}, {KeyEscape, 9, false},
	} {
		if p, ok := l.pos[c.sym]; !ok || p.code != c.code || p.shift != c.shift {
			t.Errorf("%#x at %+v (%v), want %d shift %v", c.sym, p, ok, c.code, c.shift)
		}
	}
	if l.Has(0xe9) {
		t.Error("took a keysym from group 2")
	}
	if l.Has(0xffaa) {
		t.Error("took a level from a CTRL+ALT key")
	}
	free := l.free()
	for _, used := range []uint32{9, 10, 23, 43, 50, 63} {
		if slices.Contains(free, used) {
			t.Errorf("keycode %d is in use but free", used)
		}
	}
	if !slices.Contains(free, 120) || free[0] != 11 || slices.Contains(free, 7) {
		t.Errorf("free %v", free[:5])
	}
	// X11-visible keycodes first.
	if i := slices.Index(free, 256); i < 0 || slices.ContainsFunc(free[:i], func(c uint32) bool { return c > MaxX11Keycode }) {
		t.Error("free keycodes not low first")
	}
	if l.Complete() {
		t.Error("a fragment counted as a whole layout")
	}
	if _, err := ParseKeymap("xkb_keymap { };"); err == nil {
		t.Error("parsed an empty keymap")
	}
}

func TestUS(t *testing.T) {
	l := US()
	if !l.Complete() {
		t.Fatal("US incomplete")
	}
	for sym, want := range map[uint32]pos{'a': {38, false}, 'A': {38, true}, '~': {49, true}, '\\': {51, false},
		'|': {51, true}, '/': {61, false}, '?': {61, true}, KeyUp: {111, false}, KeyISOLeftTab: {23, true},
		KeyF1: {67, false}, KeyF1 + 9: {76, false}, KeyF1 + 11: {96, false}, KeyHome: {110, false}, KeyEnd: {115, false},
		KeyPageUp: {112, false}, KeyPageDown: {117, false}, KeyDelete: {119, false}, KeyBackSpace: {22, false},
		KeyControlL: {37, false}, KeySuperL: {133, false}} {
		if got := l.pos[sym]; got != want {
			t.Errorf("%q at %+v, want %+v", rune(sym), got, want)
		}
	}
	// It knows only the main block, so it keeps off every keycode a real keymap may use.
	if f := l.free(); len(f) == 0 || f[0] != usFirstFree {
		t.Errorf("US free from %v", f[:1])
	}
}

func TestParseSym(t *testing.T) {
	for in, want := range map[string]uint32{"0x68": 'h', "h": 'h', "U65E5": 0x10065e5, "U00E9": 0xe9,
		"exclam": '!', "Return": KeyReturn, "ISO_Left_Tab": KeyISOLeftTab, "NoSymbol": 0, "": 0,
		"XF86AudioMute": 0, "0xzz": 0} {
		if got := ParseSym(in); got != want {
			t.Errorf("ParseSym(%q) = %#x, want %#x", in, got, want)
		}
	}
}

func TestPlanUsesTheLayout(t *testing.T) {
	l, _ := ParseKeymap(hyprKeymap)
	bs, err := Plan([]Stroke{{Sym: 'h'}, {Sym: 'H'}, {Sym: 0x10065e5}, {Sym: '!'}, {Sym: KeyTab, Shift: true}, {Sym: 'h'}}, l)
	if err != nil || len(bs) != 1 {
		t.Fatalf("%d batches, %v", len(bs), err)
	}
	b := bs[0]
	want := []Press{{43, false, 0}, {43, true, 0}, {11, false, 0}, {10, true, 0}, {23, true, 0}, {43, false, 0}}
	if !slices.Equal(b.Presses, want) {
		t.Fatalf("presses %v\nwant    %v", b.Presses, want)
	}
	if !slices.Equal(b.Extra, []uint32{0x10065e5}) {
		t.Errorf("extra %v", b.Extra)
	}
	wantKeys := []Key{{10, [2]uint32{'1', '!'}}, {11, [2]uint32{0x10065e5}}, {23, [2]uint32{KeyTab, KeyISOLeftTab}}, {43, [2]uint32{'h', 'H'}}}
	if !slices.Equal(b.Keys, wantKeys) {
		t.Errorf("keys %v", b.Keys)
	}
	km := b.Keymap()
	for _, w := range []string{
		`xkb_keycodes "jarvis-agent" {`, "minimum = 8;", "maximum = 43;", "<K43> = 43;", "<K11> = 11;",
		`xkb_types "jarvis-agent" { include "complete" };`,
		`xkb_compatibility "jarvis-agent" { include "complete" };`,
		"key <K43> {[ U0068, U0048 ]};", "key <K11> {[ U65E5 ]};", "key <K23> {[ Tab, ISO_Left_Tab ]};",
		"key <K10> {[ U0031, U0021 ]};",
	} {
		if !strings.Contains(km, w) {
			t.Errorf("keymap lacks %q:\n%s", w, km)
		}
	}
	if strings.Count(km, "{") != strings.Count(km, "}") {
		t.Fatal("unbalanced braces")
	}
	if bs, _ := Plan(nil, l); bs != nil {
		t.Fatal("empty plan")
	}
}

func TestPlanBatchesWhenFreeKeysRunOut(t *testing.T) {
	l := US()
	nFree := len(l.free())
	var strokes []Stroke
	for i := range 300 {
		strokes = append(strokes, Stroke{Sym: uint32(0x1000100 + i)}, Stroke{Sym: 'a'})
	}
	bs, err := Plan(strokes, l)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != (300+nFree-1)/nFree {
		t.Fatalf("%d batches for %d free keys", len(bs), nFree)
	}
	total := 0
	for _, b := range bs {
		if len(b.Extra) > nFree {
			t.Fatalf("%d extra keys in one map", len(b.Extra))
		}
		codes := map[uint32]bool{}
		for _, k := range b.Keys {
			if codes[k.Code] {
				t.Fatal("duplicate keycode")
			}
			codes[k.Code] = true
		}
		for _, p := range b.Presses {
			if !codes[p.Code] {
				t.Fatalf("press %d has no key in its batch", p.Code)
			}
		}
		total += len(b.Presses)
	}
	if total != 600 || bs[0].Presses[1].Code != 38 {
		t.Fatal("order or count lost")
	}
	if _, err := Plan([]Stroke{{Sym: 0x10065e5}}, &Layout{levels: map[uint32][2]uint32{}, pos: map[uint32]pos{}, firstFree: MaxKeycode + 1}); err == nil {
		t.Fatal("no free key, no error")
	}
}

func TestLayoutSym(t *testing.T) {
	us := US()
	// evdev KEY_A is 30: XKB keycode 38.
	if got := us.Sym(38, 0); got != 'a' {
		t.Fatalf("a: %#x", got)
	}
	if got := us.Sym(38, 1); got != 'A' {
		t.Fatalf("A: %#x", got)
	}
	if got := us.Sym(10, 1); got != '!' {
		t.Fatalf("shift+1: %#x", got)
	}
	if got := us.Sym(38, 2); got != 0 {
		t.Fatalf("level 3: %#x", got)
	}
	if got := us.Sym(999, 0); got != 0 {
		t.Fatalf("no key: %#x", got)
	}
	var nilLayout *Layout
	if got := nilLayout.Sym(38, 0); got != 0 {
		t.Fatalf("nil layout: %#x", got)
	}
}
