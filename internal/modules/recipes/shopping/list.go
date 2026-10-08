package shopping

import (
	"math"
	"math/big"
	"sort"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/recipes/quantity"
)

// Line is one recipe ingredient on a planned meal inside the shopping range, in plan, meal
// and ingredient order.
type Line struct {
	Text        string
	Quantity    *big.Rat // the parsed quantity_value; nil when it did not parse
	Unit        *string
	RecipeTitle string
}

// Amount is the total for one unit of an item. Lines whose quantity did not parse are kept
// verbatim in Unparsed rather than dropped ("salt and pepper to taste" still belongs on the
// list).
type Amount struct {
	Unit     *string
	Quantity *big.Rat
	Unparsed []string
}

// Item is one shopping-list row.
type Item struct {
	Name     string
	Amounts  []Amount
	Recipes  []string // titles, unique, first-seen order
	IsStaple bool
}

// Build is shopping_list_service.build: lines grouped by NormalizeName, then by the
// stripped, lowercased unit (no unit conversion); parsed quantities summed exactly (legacy
// summed Decimals); items sorted by name; IsStaple set from staples (shopping keys). A staple
// is flagged, never dropped.
func Build(lines []Line, staples map[string]bool) []Item {
	var items []*Item
	byKey := map[string]*Item{}
	for _, l := range lines {
		key := NormalizeName(l.Text)
		it := byKey[key]
		if it == nil {
			it = &Item{Name: key, Amounts: []Amount{}, Recipes: []string{}}
			byKey[key] = it
			items = append(items, it)
		}
		if !contains(it.Recipes, l.RecipeTitle) {
			it.Recipes = append(it.Recipes, l.RecipeTitle)
		}
		var unit *string
		if l.Unit != nil {
			if u := strings.ToLower(quantity.PyStrip(*l.Unit)); u != "" {
				unit = &u
			}
		}
		idx := -1
		for i, a := range it.Amounts {
			if sameUnit(a.Unit, unit) {
				idx = i
				break
			}
		}
		if idx < 0 {
			it.Amounts = append(it.Amounts, Amount{Unit: unit, Unparsed: []string{}})
			idx = len(it.Amounts) - 1
		}
		a := &it.Amounts[idx]
		if l.Quantity == nil {
			a.Unparsed = append(a.Unparsed, l.Text)
			continue
		}
		if a.Quantity == nil {
			a.Quantity = new(big.Rat)
		}
		a.Quantity.Add(a.Quantity, l.Quantity)
	}
	out := make([]Item, 0, len(items))
	for _, it := range items {
		it.IsStaple = staples[it.Name]
		out = append(out, *it)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func sameUnit(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// StoredQuantity is the exact decimal a quantity_value column holds: the module stores
// Numeric(10,4) values as REAL rounded to 4 decimals, so the ten-thousandths are exact.
func StoredQuantity(v float64) *big.Rat {
	return big.NewRat(int64(math.Round(v*10000)), 10000)
}
