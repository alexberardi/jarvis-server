package extract

import (
	"encoding/json"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/recipes/quantity"
)

// Recipe is url_parsing.models.ParsedRecipe.
type Recipe struct {
	Title                string                `json:"title"`
	Description          *string               `json:"description"`
	SourceURL            *string               `json:"source_url"`
	ImageURL             *string               `json:"image_url"`
	Tags                 []string              `json:"tags"`
	Servings             *int                  `json:"servings"`
	EstimatedTimeMinutes *int                  `json:"estimated_time_minutes"`
	Ingredients          []quantity.Ingredient `json:"ingredients"`
	Steps                []string              `json:"steps"`
	Notes                []string              `json:"notes"`
}

func strPtr(s string) *string { return &s }

// decodeJSON is json.loads with numbers kept exact.
func decodeJSON(s string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, &json.SyntaxError{}
	}
	return v, nil
}

// ScriptTexts returns the content of every <script type="application/ld+json"> in markup, in
// order (`script.string or script.get_text()`).
func ScriptTexts(markup string) []string {
	d := parse(markup)
	var out []string
	for _, s := range findAll(d.root, isLDScript) {
		out = append(out, text(s, "", false))
	}
	return out
}

// SchemaOrg is extract_recipe_from_schema_org over the JSON-LD script texts: the first object
// (a top-level object, a list's items, or an @graph's items) whose @type is "recipe" (case
// insensitive; a list of types may contain it) with a title, an ingredient and a step. A
// block that is not JSON is skipped. The error is extract_ingredients' TypeError on a
// non-string ingredient field (legacy let it escape).
func SchemaOrg(scripts []string, url string) (*Recipe, error) {
	for _, raw := range scripts {
		if raw == "" {
			continue
		}
		data, err := decodeJSON(raw)
		if err != nil {
			continue
		}
		var candidates []any
		if m, ok := data.(map[string]any); ok {
			if g, ok := m["@graph"]; ok {
				if gl, ok := g.([]any); ok {
					candidates = append(candidates, gl...)
				}
			}
		}
		switch x := data.(type) {
		case []any:
			candidates = append(candidates, x...)
		case map[string]any:
			candidates = append(candidates, x)
		}
		for _, c := range candidates {
			obj, ok := c.(map[string]any)
			if !ok || !truthy(obj["@type"]) || !isRecipeType(obj["@type"]) {
				continue
			}
			title := quantity.CleanText(strOr(obj["name"]))
			ingredients, err := quantity.ExtractIngredients(orEmptyList(obj["recipeIngredient"]))
			if err != nil {
				return nil, err
			}
			steps := InstructionText(orEmptyList(obj["recipeInstructions"]))
			if title == "" || len(ingredients) == 0 || len(steps) == 0 {
				continue
			}
			var all []any
			if kw := obj["keywords"]; truthy(kw) {
				all = append(all, kw)
			}
			for _, k := range []string{"recipeCategory", "recipeCuisine"} {
				v := obj[k]
				if !truthy(v) {
					continue
				}
				if l, ok := v.([]any); ok {
					all = append(all, l...)
				} else {
					all = append(all, v)
				}
			}
			var kw any
			if len(all) > 0 {
				kw = all
			}
			return &Recipe{
				Title:                title,
				Description:          strPtr(quantity.CleanText(strOr(obj["description"]))),
				SourceURL:            &url,
				ImageURL:             Image(obj["image"]),
				Tags:                 CoerceKeywords(kw, title),
				Servings:             quantity.ParseServings(obj["recipeYield"]),
				EstimatedTimeMinutes: quantity.ParseMinutes(obj["totalTime"]),
				Ingredients:          ingredients,
				Steps:                steps,
				Notes:                []string{},
			}, nil
		}
	}
	return nil, nil
}

// isRecipeType: @type equals "recipe" case-insensitively, or is a list containing it.
func isRecipeType(t any) bool {
	types := []any{t}
	if l, ok := t.([]any); ok {
		types = l
	}
	for _, x := range types {
		if strings.ToLower(pyStr(x)) == "recipe" {
			return true
		}
	}
	return false
}

// pyStr is str(x) for the JSON scalars that matter here.
func pyStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	return ""
}

// strOr is `x or ""` for a value expected to be a string (anything else reads as "").
func strOr(v any) string {
	s, _ := v.(string)
	return s
}

func orEmptyList(v any) any {
	if !truthy(v) {
		return []any{}
	}
	return v
}

// truthy is Python truthiness of a decoded JSON value.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, err := x.Float64()
		return err != nil || f != 0
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// Image is parsing_utils.extract_image: a string, or a list's first string.
func Image(v any) *string {
	switch x := v.(type) {
	case string:
		return &x
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				return &s
			}
		}
	}
	return nil
}

// InstructionText is parsing_utils.extract_instruction_text: strings, and objects' text or
// description (a HowToSection, which has neither, is dropped).
func InstructionText(v any) []string {
	steps := []string{}
	add := func(s string) {
		if c := quantity.CleanText(s); c != "" {
			steps = append(steps, c)
		}
	}
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			switch y := e.(type) {
			case string:
				add(y)
			case map[string]any:
				t := y["text"]
				if !truthy(t) {
					t = y["description"]
				}
				add(strOr(t))
			}
		}
	case string:
		add(x)
	}
	return steps
}

var keywordCategories = []string{
	"free", "friendly", "diet", "cuisine", "course", "meal", "type", "vegetarian", "vegan", "gluten",
	"dairy", "nut", "paleo", "keto", "breakfast", "lunch", "dinner", "dessert", "appetizer", "snack",
	"american", "italian", "mexican", "asian", "french", "indian", "chinese", "quick", "slow", "cooker",
	"instant", "one-pot", "sheet-pan",
}

// CoerceKeywords is parsing_utils.coerce_keywords: comma-split keywords (a string, or a list
// whose string items are split; other items are skipped), keeping general categories and
// dropping tags that echo the recipe title, deduplicated case-insensitively.
func CoerceKeywords(v any, title string) []string {
	var raw []string
	split := func(s string) {
		for _, kw := range strings.Split(s, ",") {
			if k := quantity.PyStrip(kw); k != "" {
				raw = append(raw, k)
			}
		}
	}
	switch x := v.(type) {
	case string:
		split(x)
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				split(s)
			}
		}
	}
	if len(raw) == 0 {
		return []string{}
	}
	titleLower := strings.ToLower(title)
	titleWords := map[string]bool{}
	if title != "" {
		norm := titleLower
		for _, w := range []string{"recipe", "recipes", "how to", "how to make", "easy", "best", "homemade"} {
			norm = strings.ReplaceAll(norm, w, "")
		}
		for _, w := range quantity.PySplit(norm) {
			if len([]rune(w)) > 3 {
				titleWords[w] = true
			}
		}
	}
	var kept []string
	for _, tag := range raw {
		tl := quantity.PyStrip(strings.ToLower(tag))
		if tl == "" {
			continue
		}
		words := quantity.PySplit(tl)
		if len(titleWords) > 0 {
			overlap := 0
			seen := map[string]bool{}
			for _, w := range words {
				if titleWords[w] && !seen[w] {
					overlap++
				}
				seen[w] = true
			}
			if overlap >= 2 || strings.Contains(titleLower, tl) || strings.Contains(tl, titleLower) {
				continue
			}
		}
		if title != "" && len(words) >= 3 {
			skip := false
			for w := range titleWords {
				if len([]rune(w)) > 4 && strings.Contains(tl, w) {
					skip = true
					break
				}
			}
			if skip {
				continue
			}
		}
		if len(words) <= 2 {
			kept = append(kept, tag)
			continue
		}
		for _, c := range keywordCategories {
			if strings.Contains(tl, c) {
				kept = append(kept, tag)
				break
			}
		}
	}
	out := []string{}
	seen := map[string]bool{}
	for _, t := range kept {
		if l := strings.ToLower(t); !seen[l] {
			seen[l] = true
			out = append(out, t)
		}
	}
	return out
}
