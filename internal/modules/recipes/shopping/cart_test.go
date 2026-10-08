package shopping

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

type goldenAmount struct {
	Unit     *string  `json:"unit"`
	Quantity *string  `json:"quantity"`
	Unparsed []string `json:"unparsed"`
}

func (g goldenAmount) amount(t *testing.T) Amount {
	t.Helper()
	a := Amount{Unit: g.Unit, Unparsed: g.Unparsed}
	if g.Quantity != nil {
		q, ok := new(big.Rat).SetString(*g.Quantity)
		if !ok {
			t.Fatalf("bad golden quantity %q", *g.Quantity)
		}
		a.Quantity = q
	}
	return a
}

func amounts(t *testing.T, gs []goldenAmount) []Amount {
	out := []Amount{}
	for _, g := range gs {
		out = append(out, g.amount(t))
	}
	return out
}

func TestGoldenCart(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "fixtures", "golden", "recipes", "cart.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		ParsePackSize []struct {
			Input string     `json:"input"`
			Out   [2]*string `json:"out"`
		} `json:"parse_pack_size"`
		PackQuantity []struct {
			Input struct {
				Amounts  []goldenAmount `json:"amounts"`
				UnitSize *string        `json:"unit_size"`
			} `json:"input"`
			Out int `json:"out"`
		} `json:"pack_quantity"`
		AmountDisplay []struct {
			Input []goldenAmount `json:"input"`
			Out   string         `json:"out"`
		} `json:"amount_display"`
		CartURL []struct {
			Input []struct {
				SKU      string `json:"sku"`
				Quantity int    `json:"quantity"`
			} `json:"input"`
			Out *string `json:"out"`
		} `json:"cart_url"`
		MapKey []struct {
			Input string `json:"input"`
			Out   string `json:"out"`
		} `json:"map_key"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.ParsePackSize) == 0 || len(g.PackQuantity) == 0 || len(g.AmountDisplay) == 0 || len(g.CartURL) == 0 || len(g.MapKey) == 0 {
		t.Fatal("golden sections missing")
	}
	for _, c := range g.ParsePackSize {
		size, unit := ParsePackSize(c.Input)
		var wantSize *big.Rat
		if c.Out[0] != nil {
			wantSize, _ = new(big.Rat).SetString(*c.Out[0])
		}
		if (size == nil) != (wantSize == nil) || (size != nil && size.Cmp(wantSize) != 0) || !reflect.DeepEqual(unit, c.Out[1]) {
			t.Errorf("ParsePackSize(%q) = %v %v, want %v", c.Input, size, deref(unit), c.Out)
		}
	}
	for _, c := range g.PackQuantity {
		if got := PackQuantity(amounts(t, c.Input.Amounts), c.Input.UnitSize); got != c.Out {
			t.Errorf("PackQuantity(%+v, %v) = %d, want %d", c.Input.Amounts, deref(c.Input.UnitSize), got, c.Out)
		}
	}
	for _, c := range g.AmountDisplay {
		if got := AmountDisplay(amounts(t, c.Input)); got != c.Out {
			t.Errorf("AmountDisplay(%+v) = %q, want %q", c.Input, got, c.Out)
		}
	}
	for _, c := range g.CartURL {
		var pairs []CartPair
		for _, p := range c.Input {
			pairs = append(pairs, CartPair{p.SKU, p.Quantity})
		}
		if got := CartURL(pairs); !reflect.DeepEqual(got, c.Out) {
			t.Errorf("CartURL(%v) = %v, want %v", c.Input, deref(got), deref(c.Out))
		}
	}
	for _, c := range g.MapKey {
		if got := MapKey(c.Input); got != c.Out {
			t.Errorf("MapKey(%q) = %q, want %q", c.Input, got, c.Out)
		}
	}
}

func sp(s string) *string { return &s }

// TestBuild is the contract's shopping-list example (TestRecipesShoppingAndCart).
func TestBuild(t *testing.T) {
	q := func(s string) *big.Rat { r, _ := new(big.Rat).SetString(s); return r }
	lines := []Line{
		{"2 lb ground beef", q("2"), sp("lb"), "chili"},
		{"1 cup Olive Oil, divided", q("1"), sp("Cup "), "chili"},
		{"Salt to taste", nil, nil, "chili"},
		{"2 cloves garlic, minced", q("2"), sp("cloves"), "chili"},
		{"1 1/2 lb Ground Beef (93/7 or leaner)", q("1.5"), sp("LB"), "tacos"},
		{"2 tbsp olive oil", q("2"), sp("tbsp"), "tacos"},
		{"salt", nil, sp("  "), "tacos"},
		{"1 lb ground beef", q("1"), sp("lb"), "chili"}, // a second meal of the same recipe
	}
	got := Build(lines, map[string]bool{"salt": true})
	type flat struct {
		name    string
		amounts []string
		recipes []string
		staple  bool
	}
	var out []flat
	for _, it := range got {
		f := flat{name: it.Name, recipes: it.Recipes, staple: it.IsStaple}
		for _, a := range it.Amounts {
			s := deref(a.Unit) + "="
			if a.Quantity != nil {
				s += a.Quantity.FloatString(2)
			}
			for _, u := range a.Unparsed {
				s += "|" + u
			}
			f.amounts = append(f.amounts, s)
		}
		out = append(out, f)
	}
	want := []flat{
		{"garlic", []string{"cloves=2.00"}, []string{"chili"}, false},
		{"ground beef", []string{"lb=4.50"}, []string{"chili", "tacos"}, false},
		{"olive oil", []string{"cup=1.00", "tbsp=2.00"}, []string{"chili", "tacos"}, false},
		{"salt", []string{"=|Salt to taste|salt"}, []string{"chili", "tacos"}, true},
	}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("Build:\n got %+v\nwant %+v", out, want)
	}
	if got := Build(nil, nil); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
}

func TestStoredQuantity(t *testing.T) {
	for v, want := range map[float64]string{0.3333: "3333/10000", 1.5: "3/2", 1000: "1000", 0.1 + 0.2: "3/10"} {
		if got := StoredQuantity(v).RatString(); got != want {
			t.Errorf("StoredQuantity(%v) = %s, want %s", v, got, want)
		}
	}
	// 3 × 0.3333 sums exactly (legacy summed Decimals).
	s := new(big.Rat)
	for i := 0; i < 3; i++ {
		s.Add(s, StoredQuantity(0.3333))
	}
	if f, _ := s.Float64(); f != 0.9999 {
		t.Fatalf("sum %v", f)
	}
}
