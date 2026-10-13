package xkb

import (
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

func TestPlanBatchesDistinctKeysyms(t *testing.T) {
	var strokes []Stroke
	for i := range 300 {
		strokes = append(strokes, Stroke{Sym: uint32(0x1000100 + i)}, Stroke{Sym: 0x61})
	}
	bs := Plan(strokes, 0)
	if len(bs) != 2 {
		t.Fatalf("%d batches", len(bs))
	}
	total := 0
	for _, b := range bs {
		if len(b.Syms) > MaxKeysPerMap {
			t.Fatalf("%d keys in one map", len(b.Syms))
		}
		if MinKeycode+len(b.Syms)-1 > 255 {
			t.Fatal("keycode above 255")
		}
		seen := map[uint32]bool{}
		for _, s := range b.Syms {
			if seen[s] {
				t.Fatal("duplicate keysym")
			}
			seen[s] = true
		}
		for _, s := range b.Strokes {
			if b.Keycode(s.Sym) == 0 {
				t.Fatalf("stroke %#x has no key in its batch", s.Sym)
			}
		}
		total += len(b.Strokes)
	}
	if total != 600 || bs[0].Strokes[0].Sym != 0x1000100 || bs[1].Strokes[len(bs[1].Strokes)-1].Sym != 0x61 {
		t.Fatal("order or count lost")
	}
	if got := Plan(strokes[:4], 1); len(got) != 4 {
		t.Fatalf("maxKeys 1: %d batches", len(got))
	}
	if Plan(nil, 0) != nil {
		t.Fatal("empty plan")
	}
}

func TestKeymap(t *testing.T) {
	b := Plan([]Stroke{{Sym: 'h'}, {Sym: 0x10065e5}, {Sym: KeyTab, Shift: true}, {Sym: 'h'}}, 0)[0]
	km := b.Keymap()
	for _, want := range []string{
		"minimum = 8;", "maximum = 12;", "<K1> = 9;", "<K3> = 11;",
		`xkb_types "(unnamed)" { include "complete" };`,
		`xkb_compatibility "(unnamed)" { include "complete" };`,
		"key <K1> {[ U0068 ]};", "key <K2> {[ U65E5 ]};", "key <K3> {[ Tab, ISO_Left_Tab ]};",
	} {
		if !strings.Contains(km, want) {
			t.Errorf("keymap lacks %q:\n%s", want, km)
		}
	}
	if strings.Count(km, "{") != strings.Count(km, "}") {
		t.Fatal("unbalanced braces")
	}
	if b.Keycode('h') != 9 || b.Keycode(KeyTab) != 11 || b.Keycode('z') != 0 {
		t.Fatal("keycodes")
	}
}
