package quantity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The goldens are dumped from the legacy Python code (tools/golden/export_recipes.py).

func loadGolden(t *testing.T, name string, v any) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "fixtures", "golden", "recipes", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

func requireSections(t *testing.T, g map[string][]row, names ...string) {
	t.Helper()
	for _, n := range names {
		if len(g[n]) == 0 {
			t.Fatalf("golden section %s is empty or missing", n)
		}
	}
}

type row struct {
	Input any             `json:"input"`
	Out   json.RawMessage `json:"out"`
	Error string          `json:"error"`
	Wire  json.RawMessage `json:"wire_numeric_10_4"`
	Known *bool           `json:"is_known_unit"`
}

func jsonEq(t *testing.T, what string, in any, got any, want json.RawMessage) {
	t.Helper()
	g, _ := json.Marshal(got)
	var a, b any
	_ = json.Unmarshal(g, &a)
	_ = json.Unmarshal(want, &b)
	ga, _ := json.Marshal(a)
	gb, _ := json.Marshal(b)
	if string(ga) != string(gb) {
		t.Errorf("%s(%#v): got %s, want %s", what, in, ga, gb)
	}
}

func strIn(t *testing.T, v any) *string {
	t.Helper()
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return &x
	}
	t.Fatalf("unexpected input %#v", v)
	return nil
}

func TestGoldenQuantity(t *testing.T) {
	var g map[string][]row
	loadGolden(t, "quantity.json", &g)
	requireSections(t, g, "parse_quantity_display", "normalize_fraction_display", "normalize_unit_token",
		"parse_iso8601_duration", "parse_minutes", "parse_servings", "parse_servings_from_text", "split_qty_unit")

	for _, r := range g["parse_quantity_display"] {
		in := strIn(t, r.Input)
		var got any
		if in != nil {
			if v, ok := ParseDisplay(*in); ok {
				got = Wire(v)
			}
		}
		want := r.Wire
		// B2 (fix): NaN and the infinities are null, where the legacy response model raised.
		var wire any
		_ = json.Unmarshal(r.Wire, &wire)
		if _, isErr := wire.(map[string]any); isErr {
			want = json.RawMessage("null")
		}
		jsonEq(t, "ParseDisplay", r.Input, got, want)
	}
	for _, r := range g["normalize_fraction_display"] {
		jsonEq(t, "NormalizeFractionDisplay", r.Input, NormalizeFractionDisplay(strIn(t, r.Input)), r.Out)
	}
	for _, r := range g["normalize_unit_token"] {
		in := *strIn(t, r.Input)
		jsonEq(t, "NormalizeUnitToken", in, NormalizeUnitToken(in), r.Out)
		if IsKnownUnit(in) != *r.Known {
			t.Errorf("IsKnownUnit(%q) = %v", in, !*r.Known)
		}
	}
	// B25 (fix): a day part counts; legacy ignored the whole duration.
	dayFix := map[string]string{"P1DT2H": "1560", "P1D": "1440", "P0DT0H20M": "20"}
	for _, r := range g["parse_iso8601_duration"] {
		in := strIn(t, r.Input)
		var got *int
		if in != nil {
			got = ParseISODuration(*in)
		}
		want := r.Out
		if in != nil && dayFix[*in] != "" {
			want = json.RawMessage(dayFix[*in])
		}
		jsonEq(t, "ParseISODuration", r.Input, got, want)
	}
	for _, r := range g["parse_minutes"] {
		want := r.Out
		if s, ok := r.Input.(string); ok && dayFix[s] != "" {
			want = json.RawMessage(dayFix[s])
		}
		jsonEq(t, "ParseMinutes", r.Input, ParseMinutes(r.Input), want)
	}
	for _, r := range g["parse_servings"] {
		jsonEq(t, "ParseServings", r.Input, ParseServings(r.Input), r.Out)
	}
	for _, r := range g["parse_servings_from_text"] {
		jsonEq(t, "ParseServingsFromText", r.Input, ParseServingsFromText(*strIn(t, r.Input)), r.Out)
	}
	for _, r := range g["split_qty_unit"] {
		q, u := SplitQtyUnit(*strIn(t, r.Input))
		jsonEq(t, "SplitQtyUnit", r.Input, []*string{q, u}, r.Out)
	}
}

func TestGoldenIngredients(t *testing.T) {
	var g map[string][]row
	loadGolden(t, "ingredients.json", &g)
	requireSections(t, g, "extract_ingredients_line", "extract_ingredients_dict", "extract_ingredients_string_arg",
		"clean_parsed_ingredients")
	for _, r := range g["extract_ingredients_line"] {
		got, err := ExtractIngredients([]any{r.Input})
		if err != nil {
			t.Fatal(err)
		}
		jsonEq(t, "ExtractIngredients", r.Input, got, r.Out)
	}
	for _, r := range g["extract_ingredients_dict"] {
		got, err := ExtractIngredients([]any{r.Input})
		if err != nil {
			t.Fatal(err)
		}
		jsonEq(t, "ExtractIngredients", r.Input, got, r.Out)
	}
	for _, r := range g["extract_ingredients_string_arg"] {
		in := r.Input
		if f, ok := in.(float64); ok {
			in = int(f)
		}
		got, err := ExtractIngredients(in)
		if err != nil {
			t.Fatal(err)
		}
		jsonEq(t, "ExtractIngredients", r.Input, got, r.Out)
	}
	for _, r := range g["clean_parsed_ingredients"] {
		m := r.Input.(map[string]any)
		ing := Ingredient{Text: m["text"].(string)}
		if s, ok := m["quantity_display"].(string); ok {
			ing.QuantityDisplay = &s
		}
		if s, ok := m["unit"].(string); ok {
			ing.Unit = &s
		}
		jsonEq(t, "CleanParsedIngredients", r.Input, CleanParsedIngredients([]Ingredient{ing}), r.Out)
	}
}
