package quantity

import (
	"errors"
	"regexp"
	"strings"
)

// Ingredient is url_parsing.models.ParsedIngredient.
type Ingredient struct {
	Text            string  `json:"text"`
	QuantityDisplay *string `json:"quantity_display"`
	Unit            *string `json:"unit"`
}

// ErrType is returned where the legacy code raised a TypeError on a non-string field.
var ErrType = errors.New("quantity: expected a string")

const qtyClass = `[` + PyDigit + PySpace + `/.\-+` + FractionChars + `]`

var (
	quantityUnitRe = regexp.MustCompile(`^[` + PySpace + `]*(` + qtyClass + `+)[` + PySpace + `]+([A-Za-z][A-Za-z.]*)[` + PySpace + `]+(.*)$`)
	quantityOnlyRe = regexp.MustCompile(`^[` + PySpace + `]*(` + qtyClass + `+)[` + PySpace + `]+(.*)$`)
	parenAside     = regexp.MustCompile(`[` + PySpace + `]*\([^)]*\)[` + PySpace + `]*`)
	closeParen     = regexp.MustCompile(`[` + PySpace + `]*\)[` + PySpace + `]*`)
	openParen      = regexp.MustCompile(`[` + PySpace + `]*\([` + PySpace + `]*`)
)

func strp(s string) *string { return &s }

// cleanName is extract_ingredients' clean_name: drop parentheticals and a "recipe " prefix.
func cleanName(text string) string {
	c := CleanText(text)
	c = parenAside.ReplaceAllString(c, " ")
	c = closeParen.ReplaceAllString(c, " ")
	c = openParen.ReplaceAllString(c, " ")
	c = CleanText(c)
	c = strings.TrimRight(c, " )")
	if strings.HasPrefix(strings.ToLower(c), "recipe ") {
		c = string([]rune(c)[7:])
	}
	return c
}

func splitLine(line string) Ingredient {
	raw := CleanText(line)
	if raw == "" {
		return Ingredient{Text: raw}
	}
	if m := quantityUnitRe.FindStringSubmatch(raw); m != nil {
		unit := CleanText(m[2])
		if IsKnownUnit(unit) {
			return Ingredient{Text: cleanName(m[3]), QuantityDisplay: NormalizeFractionDisplay(strp(CleanText(m[1]))), Unit: &unit}
		}
	}
	if m := quantityOnlyRe.FindStringSubmatch(raw); m != nil {
		return Ingredient{Text: cleanName(m[2]), QuantityDisplay: NormalizeFractionDisplay(strp(CleanText(m[1])))}
	}
	return Ingredient{Text: cleanName(raw)}
}

// truthyStr is Python's `a or b` over optional string fields: the first non-empty string; a
// non-string truthy value is ErrType (the legacy clean_text raised on it).
func truthyStr(m map[string]any, keys ...string) (string, error) {
	for _, k := range keys {
		switch v := m[k].(type) {
		case nil:
		case string:
			if v != "" {
				return v, nil
			}
		case bool:
			if v {
				return "", ErrType
			}
		default:
			if !falsyNumber(v) {
				return "", ErrType
			}
		}
	}
	return "", nil
}

func falsyNumber(v any) bool {
	switch x := v.(type) {
	case float64:
		return x == 0
	case int:
		return x == 0
	case interface{ String() string }:
		s := x.String()
		return s == "0" || s == "0.0"
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// ExtractIngredients is ingredient_parser.extract_ingredients over a decoded JSON value: a list
// of strings and/or {text|name, amount|quantity, unit} objects, or one string. Anything else
// gives no ingredients.
func ExtractIngredients(v any) ([]Ingredient, error) {
	out := []Ingredient{}
	switch x := v.(type) {
	case string:
		if c := CleanText(x); c != "" {
			out = append(out, splitLine(c))
		}
	case []any:
		for _, raw := range x {
			switch e := raw.(type) {
			case string:
				if c := CleanText(e); c != "" {
					out = append(out, splitLine(c))
				}
			case map[string]any:
				text, err := truthyStr(e, "text", "name")
				if err != nil {
					return nil, err
				}
				if text == "" {
					continue
				}
				qtyRaw, err := truthyStr(e, "amount", "quantity")
				if err != nil {
					return nil, err
				}
				unitRaw, err := truthyStr(e, "unit")
				if err != nil {
					return nil, err
				}
				qty, unit := CleanText(qtyRaw), CleanText(unitRaw)
				if qty == "" && unit == "" {
					out = append(out, splitLine(text))
					continue
				}
				ing := Ingredient{Text: CleanText(text)}
				if qty != "" {
					ing.QuantityDisplay = &qty
				}
				if unit != "" {
					ing.Unit = &unit
				}
				out = append(out, ing)
			}
		}
	}
	return out, nil
}

// CleanParsedIngredients is ingredient_parser.clean_parsed_ingredients: pull a quantity and
// unit embedded in the text or the quantity out into their fields, and normalise fractions.
func CleanParsedIngredients(items []Ingredient) []Ingredient {
	out := make([]Ingredient, 0, len(items))
	splitQtyTokens := func(qty string) (*string, string) {
		var nums []string
		unit := ""
		for _, tok := range strings.Split(qty, " ") {
			if tok == "" {
				continue
			}
			if IsKnownUnit(tok) {
				unit = tok
				break
			}
			nums = append(nums, tok)
		}
		if len(nums) == 0 {
			return nil, unit
		}
		return NormalizeFractionDisplay(strp(strings.Join(nums, " "))), unit
	}
	splitFromText := func(text string) (*string, string, string) {
		raw := CleanText(text)
		if raw == "" {
			return nil, "", raw
		}
		if m := quantityUnitRe.FindStringSubmatch(raw); m != nil {
			unit := CleanText(m[2])
			if IsKnownUnit(unit) {
				return NormalizeFractionDisplay(strp(CleanText(m[1]))), unit, CleanText(m[3])
			}
		}
		if m := quantityOnlyRe.FindStringSubmatch(raw); m != nil {
			return NormalizeFractionDisplay(strp(CleanText(m[1]))), "", CleanText(m[2])
		}
		return nil, "", raw
	}
	truthy := func(p *string) bool { return p != nil && *p != "" }
	for _, ing := range items {
		qty := NormalizeFractionDisplay(ing.QuantityDisplay)
		var unit *string
		if truthy(ing.Unit) {
			unit = strp(CleanText(*ing.Unit))
		}
		name := CleanText(ing.Text)

		qd2, unit2, name2 := splitFromText(name)
		if truthy(qd2) && !truthy(qty) {
			qty = qd2
		}
		if unit2 != "" && !truthy(unit) {
			unit = strp(unit2)
		}
		if name2 != "" {
			name = name2
		}

		if truthy(qty) && !truthy(unit) {
			// legacy: qty.split() — Python whitespace
			qdSplit, unitSplit := splitQtyTokens(strings.Join(PySplit(*qty), " "))
			if unitSplit != "" && IsKnownUnit(unitSplit) {
				unit = strp(unitSplit)
			}
			if truthy(qdSplit) {
				qty = qdSplit
			}
		}
		if truthy(qty) && truthy(unit) {
			norm := NormalizeUnitToken(*unit)
			var keep []string
			for _, t := range PySplit(*qty) {
				if NormalizeUnitToken(t) != norm {
					keep = append(keep, t)
				}
			}
			if n := NormalizeFractionDisplay(strp(strings.Join(keep, " "))); truthy(n) {
				qty = n
			}
		}
		out = append(out, Ingredient{Text: name, QuantityDisplay: qty, Unit: unit})
	}
	return out
}
