// Package shopping holds the recipes module's shopping-list helpers, ported from
// jarvis-recipes-server's shopping_list_service. NormalizeName is the grouping key for the
// shopping list, staples and SKU mappings; fixtures/golden/recipes/normalize_name.json is its
// contract (docs/recipes/00-inventory.md §4.7).
package shopping

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/modules/recipes/quantity"
)

const (
	ws    = `[` + quantity.PySpace + `]`
	digit = quantity.PyDigit
)

var (
	// _ASIDE: parenthetical asides (alternatives, optional notes, approximate counts, metric
	// weights, fat ratios); a parenthetical that identifies the ingredient stays.
	aside = regexp.MustCompile(`(?i)` + ws + `*\((?:` +
		`or` + ws + `[^)]*` +
		`|optional[^)]*` +
		`|about` + ws + `[^)]*|approx[^)]*` +
		`|store-bought[^)]*` +
		`|fresh,[^)]*` +
		`|[` + digit + `.,/` + quantity.PySpace + `]+(?:g|kg|ml|l|oz|lb|lbs)?` +
		`|` + digit + `+/` + digit + `+[^)]*` +
		`)\)`)
	// _PREP_SUFFIX: ", finely chopped…" and the rest of the line.
	prepSuffix = regexp.MustCompile(`(?i),` + ws + `*(?:finely` + ws + `+|thinly` + ws + `+|roughly` + ws + `+)?` +
		`(?:chopped|diced|sliced|minced|grated|shredded|crumbled|cubed|quartered|halved` +
		`|melted|softened|drained|rinsed|peeled|beaten|sifted|leveled|divided|to taste)[^\n]*(?:\n?$)`)
	trailingToTaste = regexp.MustCompile(`(?i)` + ws + `+to taste` + ws + `*$`)
)

// units is _UNITS, in order: the first alternative that matches (with a word boundary) wins.
var units = []string{
	"lbs", "lb", "pounds", "pound", "ounces", "ounce", "oz",
	"cups", "cup", "tablespoons", "tablespoon", "tbsp", "teaspoons", "teaspoon", "tsp",
	"grams", "gram", "g", "kilograms", "kilogram", "kg",
	"milliliters", "milliliter", "ml", "liters", "liter", "l",
	"cloves", "clove", "cans", "can", "bunches", "bunch", "pints", "pint",
	"slices", "slice", "packages", "package", "pkg", "sprigs", "sprig",
	"quarts", "quart", "gallons", "gallon", "sticks", "stick", "pinch", "dash",
}

// isWord is Python's \w for str patterns.
func isWord(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsNumber(r)
}

// isQtyChar is the class of _LEADING_QTY: [\d¼-¾⅐-⅞./\s-].
func isQtyChar(r rune) bool {
	return quantity.DigitValue(r) >= 0 || (r >= '¼' && r <= '¾') || (r >= '⅐' && r <= '⅞') ||
		r == '.' || r == '/' || r == '-' || quantity.IsPySpace(r)
}

// stripLeadingQty is _LEADING_QTY.sub("", s): ^\s*[class]+(?=\s|$). The class includes
// whitespace, so the match is the longest run of class characters that ends the string or is
// followed by whitespace ("2% milk" keeps its 2).
func stripLeadingQty(s string) string {
	run := 0
	for i, r := range s {
		if !isQtyChar(r) {
			break
		}
		run = i + utf8.RuneLen(r)
	}
	if run == 0 {
		return s
	}
	if run == len(s) {
		return ""
	}
	// Backtrack to the last whitespace inside the run with at least one character before it.
	for i := run - 1; i > 0; i-- {
		r, _ := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError || !utf8.RuneStart(s[i]) {
			continue
		}
		if quantity.IsPySpace(r) {
			return s[i:]
		}
	}
	return s
}

// stripLeadingUnit is _LEADING_UNIT.sub("", s): ^(?:units)\b\.?\s*, case-insensitively.
func stripLeadingUnit(s string) string {
	for _, u := range units {
		if len(s) < len(u) || !strings.EqualFold(s[:len(u)], u) {
			continue
		}
		rest := s[len(u):]
		if r, _ := utf8.DecodeRuneInString(rest); rest != "" && isWord(r) {
			continue // no word boundary
		}
		rest = strings.TrimPrefix(rest, ".")
		return strings.TrimLeftFunc(rest, quantity.IsPySpace)
	}
	return s
}

// NormalizeName is shopping_list_service.normalize_name: the grouping key for an ingredient
// line. It strips parenthetical asides, a trailing prep clause, a trailing "to taste", a leading
// quantity and a leading unit, and lowercases; if nothing is left, the original lowercased.
func NormalizeName(text string) string {
	c := quantity.PyStrip(aside.ReplaceAllString(text, ""))
	c = quantity.PyStrip(prepSuffix.ReplaceAllString(c, ""))
	c = quantity.PyStrip(trailingToTaste.ReplaceAllString(c, ""))
	c = quantity.PyStrip(stripLeadingQty(c))
	c = quantity.PyStrip(stripLeadingUnit(c))
	if c = strings.ToLower(c); c != "" {
		return c
	}
	return strings.ToLower(quantity.PyStrip(text))
}
