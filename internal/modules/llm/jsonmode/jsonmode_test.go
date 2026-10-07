package jsonmode

import (
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

func fixture(t *testing.T, name string) any {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "fixtures", "golden", "llm", name))
	if err != nil {
		t.Fatal(err)
	}
	v, err := pyjson.Loads(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func fget(o any, k string) any {
	v, _ := o.(*pyjson.Object).Get(k)
	return v
}

func fhas(o any, k string) bool {
	_, ok := o.(*pyjson.Object).Get(k)
	return ok
}

// optional compares a Go (string, ok) result with a fixture value that is a string or null.
func optional(t *testing.T, what, input string, got string, ok bool, want any) {
	t.Helper()
	if want == nil {
		if ok {
			t.Errorf("%s(%q) = %q, want None", what, input, got)
		}
		return
	}
	if !ok || got != want.(string) {
		t.Errorf("%s(%q) = %q (ok=%v), want %q", what, input, got, ok, want)
	}
}

// G7: parse_json_response and every repair helper over the malformed-JSON corpus.
func TestParseGolden(t *testing.T) {
	for _, r := range fixture(t, "json_parse.json").([]any) {
		in := fget(r, "input").(string)
		p := fget(r, "parse_json_response")
		gotC, gotV := Parse(in)
		if gotC != fget(p, "content").(string) || gotV != fget(p, "valid").(bool) {
			t.Errorf("Parse(%q) = %q, %v; want %q, %v", in, gotC, gotV, fget(p, "content"), fget(p, "valid"))
		}
		s, ok := RepairDuplicateKeys(in)
		optional(t, "RepairDuplicateKeys", in, s, ok, fget(r, "repair_duplicate_keys"))
		s, ok = RepairUnescapedQuotes(in)
		optional(t, "RepairUnescapedQuotes", in, s, ok, fget(r, "repair_unescaped_quotes"))
		s, ok = ExtractJSON(in)
		optional(t, "ExtractJSON", in, s, ok, fget(r, "extract_json_from_text"))
		s, ok = RepairTruncated(in)
		optional(t, "RepairTruncated", in, s, ok, fget(r, "repair_truncated_json"))
		if got := IsTruncated(in); got != fget(r, "is_json_truncated").(bool) {
			t.Errorf("IsTruncated(%q) = %v", in, got)
		}
	}
}

func TestAugmentSystemGolden(t *testing.T) {
	for _, c := range fixture(t, "json_inject.json").([]any) {
		in := fget(c, "input").([]any)
		out := fget(c, "output").([]any)
		var got []any
		hasSystem := false
		for _, m := range in {
			if fget(m, "role") == "system" {
				hasSystem = true
			}
		}
		if !hasSystem {
			got = append(got, []string{SystemMessage})
		}
		for _, m := range in {
			var parts []string
			for _, p := range fget(m, "parts").([]any) {
				parts = append(parts, p.(string))
			}
			if fget(m, "role") == "system" {
				got = append(got, []string{AugmentSystem(parts)})
			} else {
				got = append(got, parts)
			}
		}
		if len(got) != len(out) {
			t.Fatalf("message count %d, want %d", len(got), len(out))
		}
		for i, m := range out {
			want := fget(m, "parts").([]any)
			g := got[i].([]string)
			if len(g) != len(want) {
				t.Errorf("case %v msg %d: parts %q, want %v", in, i, g, want)
				continue
			}
			for j := range g {
				if g[j] != want[j].(string) {
					t.Errorf("msg %d part %d = %q, want %q", i, j, g[j], want[j])
				}
			}
		}
	}
}

func TestSchemaGolden(t *testing.T) {
	g := fixture(t, "json_schema.json")
	for _, c := range fget(g, "validate").([]any) {
		res := fget(c, "result")
		got, err := Validate(fget(c, "value"), fget(c, "schema"))
		if fhas(res, "raises") {
			if err == nil || err.Error() != fget(res, "raises").(string) {
				t.Errorf("Validate(%s, %s) = %q, %v; want raise %q", pyjson.Dumps(fget(c, "value"), true), pyjson.Dumps(fget(c, "schema"), true), got, err, fget(res, "raises"))
			}
			continue
		}
		want := fget(res, "ok")
		if err != nil || (want == nil && got != "") || (want != nil && got != want.(string)) {
			t.Errorf("Validate(%s, %s) = %q, %v; want %v", pyjson.Dumps(fget(c, "value"), true), pyjson.Dumps(fget(c, "schema"), true), got, err, want)
		}
	}
	for _, c := range fget(g, "summarize").([]any) {
		res := fget(c, "result")
		got, err := Summarize(fget(c, "schema"))
		if err != nil || got != fget(res, "ok").(string) {
			t.Errorf("Summarize(%s) = %q, %v; want %v", pyjson.Dumps(fget(c, "schema"), true), got, err, fget(res, "ok"))
		}
	}
}

// G7: the full correction turn and the retry parameters fix_json_with_retry sends.
func TestRetryGolden(t *testing.T) {
	for _, c := range fixture(t, "json_retry.json").([]any) {
		cs := fget(c, "case")
		invalid := fget(cs, "invalid").(string)
		msgs := fget(c, "messages").([]any)
		last := fget(msgs[len(msgs)-1], "parts").([]any)[0].(string)
		turn, err := CorrectionTurn(invalid, fget(cs, "schema"))
		if err != nil || turn != last {
			t.Errorf("CorrectionTurn(%.40q):\n got %q\nwant %q", invalid, turn, last)
		}
		p := fget(c, "params")
		temp := toFloat(fget(cs, "temperature"))
		if got := RetryTemperature(temp); got != toFloat(fget(p, "temperature")) {
			t.Errorf("RetryTemperature(%v) = %v, want %v", temp, got, fget(p, "temperature"))
		}
		orig := 0
		if mt := fget(cs, "max_tokens"); mt != nil {
			orig = int(mt.(*big.Int).Int64())
		}
		if got := RetryMaxTokens(orig, invalid); int64(got) != fget(p, "max_tokens").(*big.Int).Int64() {
			t.Errorf("RetryMaxTokens(%d, len %d) = %d, want %v", orig, len(invalid), got, fget(p, "max_tokens"))
		}
		// The repaired retry reply is what the job returns (or None → invalid JSON).
		reply := fget(cs, "reply").(string)
		fixed, valid := Parse(reply)
		if valid && fget(cs, "schema") != nil {
			v, _ := pyjson.Loads(fixed)
			if e, _ := Validate(v, fget(cs, "schema")); e != "" {
				valid = false
			}
		}
		want := fget(c, "result")
		if (want == nil) == valid || (want != nil && fixed != want.(string)) {
			t.Errorf("retry result for %q = %q (valid %v), want %v", reply, fixed, valid, want)
		}
	}
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case *big.Int:
		f, _ := new(big.Float).SetInt(x).Float64()
		return f
	}
	return 0
}
