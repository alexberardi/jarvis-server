package pyjson

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func fixture(t *testing.T, name string) *Object {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "fixtures", "golden", "llm", name))
	if err != nil {
		t.Fatal(err)
	}
	v, err := Loads(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return v.(*Object)
}

func field(o any, k string) (any, bool) {
	v, ok := o.(*Object).Get(k)
	return v, ok
}

// G7: json.loads errors and json.dumps bytes (both ensure_ascii modes) match CPython 3.11.
func TestLoadsDumpsGolden(t *testing.T) {
	g := fixture(t, "pyjson.json")
	rows, _ := g.Get("loads")
	for _, r := range rows.([]any) {
		in, _ := field(r, "input")
		input := in.(string)
		v, err := Loads(input)
		if want, ok := field(r, "error"); ok {
			if err == nil || err.Error() != want.(string) {
				t.Errorf("Loads(%q) error = %v, want %q", input, err, want)
			}
			continue
		}
		if want, ok := field(r, "raises"); ok {
			if err == nil {
				t.Errorf("Loads(%q) succeeded, Python raised %v", input, want)
			}
			continue
		}
		if err != nil {
			t.Errorf("Loads(%q): %v", input, err)
			continue
		}
		want, _ := field(r, "dumps")
		if got := Dumps(v, false); got != want.(string) {
			t.Errorf("Dumps(Loads(%q)) = %q, want %q", input, got, want)
		}
		wantA, _ := field(r, "dumps_ascii")
		if got := Dumps(v, true); got != wantA.(string) {
			t.Errorf("Dumps(Loads(%q), ascii) = %q, want %q", input, got, wantA)
		}
	}
}

func TestFloatReprGolden(t *testing.T) {
	g := fixture(t, "pyjson.json")
	rows, _ := g.Get("float_repr")
	for _, r := range rows.([]any) {
		h, _ := field(r, "hex")
		f := pyFromHex(t, h.(string))
		repr, _ := field(r, "repr")
		js, _ := field(r, "json")
		if got := FloatRepr(f, false); got != repr.(string) {
			t.Errorf("repr(%s) = %q, want %q", h, got, repr)
		}
		if got := FloatRepr(f, true); got != js.(string) {
			t.Errorf("json(%s) = %q, want %q", h, got, js)
		}
	}
}

// pyFromHex parses float.hex() output ("0x1.8p+1", "inf", "-0x0.0p+0").
func pyFromHex(t *testing.T, s string) float64 {
	switch s {
	case "inf":
		return math.Inf(1)
	case "-inf":
		return math.Inf(-1)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return f
}

func TestLoneSurrogateRoundTrip(t *testing.T) {
	v, err := Loads(`"\ud800x"`)
	if err != nil {
		t.Fatal(err)
	}
	if got := Dumps(v, true); got != `"\ud800x"` {
		t.Fatalf("got %s", got)
	}
	if got := Compact(v); got != "\"�x\"" {
		t.Fatalf("compact got %q", got)
	}
}

func TestReprAndTypeName(t *testing.T) {
	v, _ := Loads(`["a", 1, 1.5, true, null, {"k": "it's"}]`)
	if got := Repr(v); got != `['a', 1, 1.5, True, None, {'k': "it's"}]` {
		t.Fatalf("Repr = %s", got)
	}
	for in, want := range map[string]string{`"x"`: "str", "1": "int", "1.0": "float", "true": "bool", "null": "NoneType", "[]": "list", "{}": "dict"} {
		v, _ := Loads(in)
		if got := TypeName(v); got != want {
			t.Errorf("TypeName(%s) = %s, want %s", in, got, want)
		}
	}
}
