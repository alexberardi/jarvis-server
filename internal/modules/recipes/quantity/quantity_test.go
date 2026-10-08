package quantity

import "testing"

func TestParseDisplay(t *testing.T) {
	for in, want := range map[string]string{
		"1": "1.0000", "1/3": "0.3333", "2/3": "0.6667", "1 1/2": "1.5000", "1e3": "1000.0000",
		"1/Infinity": "0.0000", "-0": "0.0000", "0.00005": "0.0001", "-0.00005": "-0.0001", "1_000": "1000.0000",
		"٣": "3.0000", " 1 ": "1.0000",
	} {
		v, ok := ParseDisplay(in)
		if !ok || Wire(v) != want {
			t.Errorf("ParseDisplay(%q) = %v %v, want %s", in, Wire(v), ok, want)
		}
	}
	for _, in := range []string{"", " ", "NaN", "inf", "-Infinity", "sNaN", "1/0", "abc", "1__0", "_1", "1/2 cup", "½", ".", "1e", "Infinity/Infinity", "1 x"} {
		if v, ok := ParseDisplay(in); ok {
			t.Errorf("ParseDisplay(%q) = %v, want none", in, v)
		}
	}
}

func TestPyFloatRepr(t *testing.T) {
	for f, want := range map[float64]string{
		1.5: "1.5", 0.1: "0.1", 1e16: "1e+16", 1.5e-05: "1.5e-05", 0.0001: "0.0001", 123456.789: "123456.789",
		-2.25: "-2.25", 1e15 + 0.5: "1000000000000000.5", 100: "100.0",
	} {
		if got := PyFloatRepr(f); got != want {
			t.Errorf("PyFloatRepr(%v) = %q, want %q", f, got, want)
		}
	}
}

func TestDigitValue(t *testing.T) {
	for r, want := range map[rune]int{'7': 7, '٣': 3, '९': 9, '𝟘': 0, '𝟡': 9, '𝟫': 9, 'a': -1, '½': -1} {
		if got := DigitValue(r); got != want {
			t.Errorf("DigitValue(%q) = %d, want %d", r, got, want)
		}
	}
}
