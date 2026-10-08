// Package ocrq is the photo-import pipeline's pure parts (legacy ocr_quality.py, ocr_join.py
// and the RecipeDraft half of llm_client.py): the readings' order, the quality gate, the P2/P3
// prompts and the draft coercion. The goldens in fixtures/golden/recipes/ocr.json,
// llm_parse.json and prompts/P2, P3, P1r_json_repair_draft are the contract.
//
// JSON from the model is decoded with pyjson (Python's json.loads types), so the str() and
// repr() the coercion falls back on read as they did in Python.
package ocrq

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/modules/recipes/quantity"
)

// Ingredient is RecipeDraftIngredient.
type Ingredient struct {
	Name     string  `json:"name"`
	Quantity *string `json:"quantity"`
	Unit     *string `json:"unit"`
	Notes    *string `json:"notes"`
}

// Source is RecipeDraftSource.
type Source struct {
	Type             string  `json:"type"`
	OriginalFilename *string `json:"original_filename"`
	OCRTierUsed      *int64  `json:"ocr_tier_used"`
}

// Draft is RecipeDraft, the photo import's result (model_dump order).
type Draft struct {
	Title       string       `json:"title"`
	Description *string      `json:"description"`
	Ingredients []Ingredient `json:"ingredients"`
	Steps       []string     `json:"steps"`
	Prep        int64        `json:"prep_time_minutes"`
	Cook        int64        `json:"cook_time_minutes"`
	Total       int64        `json:"total_time_minutes"`
	Servings    *string      `json:"servings"`
	Tags        []string     `json:"tags"`
	Source      Source       `json:"source"`
}

// ValidateMinimums is RecipeDraft.validate_minimums.
func (d *Draft) ValidateMinimums() error {
	switch {
	case len([]rune(d.Title)) < 3:
		return errors.New("title too short")
	case len(d.Ingredients) < 3:
		return errors.New("not enough ingredients")
	case len(d.Steps) < 2:
		return errors.New("not enough steps")
	}
	return nil
}

// ValidationError is a pydantic ValidationError (the message is not pydantic's text).
type ValidationError struct{ Errs []string }

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%d validation error(s) for RecipeDraft: %s", len(e.Errs), strings.Join(e.Errs, "; "))
}

// JSONError is json.loads failing on the model's reply.
type JSONError struct{ Err error }

func (e *JSONError) Error() string { return e.Err.Error() }
func (e *JSONError) Unwrap() error { return e.Err }

func get(o *pyjson.Object, k string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	return v
}

// truthy is Python truthiness of a pyjson value.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case *big.Int:
		return x.Sign() != 0
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case *pyjson.Object:
		return x.Len() > 0
	}
	return true
}

// or is Python's `a or b`.
func or(vs ...any) any {
	for _, v := range vs {
		if truthy(v) {
			return v
		}
	}
	return vs[len(vs)-1]
}

// --- pydantic lax validation of RecipeDraft ---

type validator struct {
	errs []string
	// fixB5 applies the B5 fixes (the coerced draft's final validation).
	fixB5 bool
}

func (v *validator) fail(loc, msg string) { v.errs = append(v.errs, loc+": "+msg) }

func (v *validator) str(loc string, x any, required bool) *string {
	switch s := x.(type) {
	case string:
		return &s
	case nil:
		if required {
			v.fail(loc, "Input should be a valid string")
		}
		return nil
	}
	if v.fixB5 {
		// B5: a number where a string belongs (Qwen's "servings": 4) is taken as its text.
		switch x.(type) {
		case *big.Int, float64:
			s := pyjson.Str(x)
			return &s
		}
	}
	v.fail(loc, "Input should be a valid string")
	return nil
}

// laxInt is pydantic's lax int: integers, integral floats, numeric strings (surrounding
// whitespace, underscores, "10.0"), booleans. absent is the default.
func (v *validator) laxInt(loc string, x any, def int64, present bool) int64 {
	if !present {
		return def
	}
	switch n := x.(type) {
	case bool:
		if n {
			return 1
		}
		return 0
	case *big.Int:
		if n.IsInt64() {
			return n.Int64()
		}
	case float64:
		if n == math.Trunc(n) && !math.IsInf(n, 0) {
			return int64(n)
		}
		if v.fixB5 && !math.IsNaN(n) && !math.IsInf(n, 0) {
			// B5: fractional minutes are rounded instead of failing the draft.
			return int64(math.Round(n))
		}
		v.fail(loc, "Input should be a valid integer, got a number with a fractional part")
		return 0
	case string:
		s := strings.ReplaceAll(quantity.PyStrip(n), "_", "")
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil && f == math.Trunc(f) && !math.IsInf(f, 0) {
			return int64(f)
		}
		v.fail(loc, "Input should be a valid integer, unable to parse string as an integer")
		return 0
	}
	v.fail(loc, "Input should be a valid integer")
	return 0
}

func (v *validator) strList(loc string, x any) []string {
	l, ok := x.([]any)
	if !ok {
		v.fail(loc, "Input should be a valid list")
		return nil
	}
	out := []string{}
	for i, e := range l {
		if s := v.str(fmt.Sprintf("%s.%d", loc, i), e, true); s != nil {
			out = append(out, *s)
		}
	}
	return out
}

// validate is RecipeDraft.model_validate over a decoded object.
func validate(o *pyjson.Object, fixB5 bool) (*Draft, error) {
	v := &validator{fixB5: fixB5}
	d := &Draft{Ingredients: []Ingredient{}, Steps: []string{}, Tags: []string{}}
	has := func(k string) bool { _, ok := o.Get(k); return ok }
	if t := v.str("title", get(o, "title"), true); t != nil {
		d.Title = *t
	}
	if !has("title") {
		v.fail("title", "Field required")
	}
	d.Description = v.str("description", get(o, "description"), false)
	if !has("ingredients") {
		v.fail("ingredients", "Field required")
	} else if l, ok := get(o, "ingredients").([]any); !ok {
		v.fail("ingredients", "Input should be a valid list")
	} else {
		for i, e := range l {
			loc := fmt.Sprintf("ingredients.%d", i)
			io, ok := e.(*pyjson.Object)
			if !ok {
				v.fail(loc, "Input should be a valid dictionary")
				continue
			}
			ing := Ingredient{}
			if _, ok := io.Get("name"); !ok {
				v.fail(loc+".name", "Field required")
			} else if n := v.str(loc+".name", get(io, "name"), true); n != nil {
				ing.Name = *n
			}
			ing.Quantity = v.str(loc+".quantity", get(io, "quantity"), false)
			ing.Unit = v.str(loc+".unit", get(io, "unit"), false)
			ing.Notes = v.str(loc+".notes", get(io, "notes"), false)
			d.Ingredients = append(d.Ingredients, ing)
		}
	}
	if !has("steps") {
		v.fail("steps", "Field required")
	} else {
		d.Steps = v.strList("steps", get(o, "steps"))
	}
	d.Prep = v.laxInt("prep_time_minutes", get(o, "prep_time_minutes"), 0, has("prep_time_minutes"))
	d.Cook = v.laxInt("cook_time_minutes", get(o, "cook_time_minutes"), 0, has("cook_time_minutes"))
	d.Total = v.laxInt("total_time_minutes", get(o, "total_time_minutes"), 0, has("total_time_minutes"))
	d.Servings = v.str("servings", get(o, "servings"), false)
	if has("tags") {
		d.Tags = v.strList("tags", get(o, "tags"))
	}
	if !has("source") {
		v.fail("source", "Field required")
	} else if so, ok := get(o, "source").(*pyjson.Object); !ok {
		v.fail("source", "Input should be a valid dictionary")
	} else {
		d.Source.Type = "image"
		if _, ok := so.Get("type"); ok {
			if t := v.str("source.type", get(so, "type"), true); t != nil {
				d.Source.Type = *t
			}
		}
		d.Source.OriginalFilename = v.str("source.original_filename", get(so, "original_filename"), false)
		if x := get(so, "ocr_tier_used"); x != nil {
			n := v.laxInt("source.ocr_tier_used", x, 0, true)
			d.Source.OCRTierUsed = &n
		}
	}
	if len(v.errs) > 0 {
		return nil, &ValidationError{Errs: v.errs}
	}
	return d, nil
}

var (
	fenceOpen  = regexp.MustCompile("^```[a-zA-Z0-9_-]*\\s*")
	fenceClose = regexp.MustCompile("\\s*```$")
)

// StripFence removes a leading ```lang and a trailing ``` (llm_client's fence regexes).
func StripFence(s string) string {
	return fenceClose.ReplaceAllString(fenceOpen.ReplaceAllString(s, ""), "")
}

// Coerce is _coerce_recipe_draft: a string reply is parsed (fences stripped), a {"recipe": …}
// wrapper unwrapped, a meaningful "error" raised; a draft that validates and meets the
// minimums as is is returned unchanged; otherwise the usual alternative shapes are coerced
// (name/label, directions, prepTime…, a quantity with its unit or the name with its quantity)
// and validated again, with the B5 fixes: numeric servings and quantities become strings, a
// quantity object without a value becomes no quantity, and fractional minutes are rounded.
func Coerce(raw any, sourceType string) (*Draft, error) {
	data := raw
	if s, ok := raw.(string); ok {
		cleaned := quantity.PyStrip(s)
		if strings.HasPrefix(cleaned, "```") {
			cleaned = quantity.PyStrip(StripFence(cleaned))
		}
		v, err := pyjson.Loads(cleaned)
		if err != nil {
			return nil, &JSONError{err}
		}
		data = v
	}
	if o, ok := data.(*pyjson.Object); ok {
		if _, has := o.Get("recipe"); has {
			data = get(o, "recipe")
		}
	}
	o, isObj := data.(*pyjson.Object)
	if isObj && truthy(get(o, "error")) {
		switch ev := get(o, "error").(type) {
		case string:
			if t := quantity.PyStrip(ev); t != "" && t != "{" {
				return nil, fmt.Errorf("LLM returned error: %s", ev)
			}
		case *pyjson.Object:
			if truthy(get(ev, "message")) || truthy(get(ev, "code")) {
				return nil, fmt.Errorf("LLM returned error: %s", pyjson.Repr(ev))
			}
		}
	}
	if isObj {
		if d, err := validate(o, false); err == nil && d.ValidateMinimums() == nil {
			return d, nil
		}
	}
	if !isObj {
		return nil, errors.New("LLM response is not a JSON object")
	}

	norm := pyjson.NewObject()
	norm.Set("title", or(get(o, "title"), get(o, "name"), "Untitled"))
	norm.Set("description", get(o, "description"))

	var ings []any
	switch x := or(get(o, "ingredients"), []any{}).(type) {
	case []any:
		ings = x
	case *pyjson.Object, string:
		// iterating a dict or a string yields strings, which are skipped
	default:
		return nil, errors.New("TypeError: ingredients is not iterable")
	}
	type rawIng struct {
		qty, unit, name, notes any
	}
	var collected []rawIng
	for _, e := range ings {
		io, ok := e.(*pyjson.Object)
		if !ok {
			continue
		}
		qty := or(get(io, "quantity"), get(io, "quantity_display"))
		unit := get(io, "unit")
		switch q := qty.(type) {
		case *pyjson.Object:
			unit = or(unit, get(q, "unit"))
			if val := get(q, "value"); val != nil {
				qty = pyjson.Str(val)
			} else {
				qty = nil // B5: legacy kept the dict and failed the whole draft
			}
		case nil, string:
		default:
			qty = pyjson.Str(q)
		}
		collected = append(collected, rawIng{qty: qty, unit: unit, name: or(get(io, "name"), get(io, "label"), ""), notes: get(io, "notes")})
	}

	steps := []any{}
	var stepsIn []any
	switch x := or(get(o, "steps"), get(o, "directions"), []any{}).(type) {
	case []any:
		stepsIn = x
	case *pyjson.Object, string:
	default:
		return nil, errors.New("TypeError: steps is not iterable")
	}
	for _, st := range stepsIn {
		var t any
		switch x := st.(type) {
		case *pyjson.Object:
			t = or(get(x, "text"), get(x, "action"), get(x, "description"), get(x, "label"), "")
		case string:
			t = x
		}
		if s, ok := t.(string); ok {
			if s = quantity.PyStrip(s); s != "" {
				steps = append(steps, s)
			}
		}
	}

	prep := or(get(o, "prep_time_minutes"), get(o, "prepTime"))
	cook := or(get(o, "cook_time_minutes"), get(o, "cookTime"))
	total := or(get(o, "total_time_minutes"), get(o, "totalTime"))
	active := or(get(o, "activeTime"), get(o, "active_time_minutes"))
	if cook == nil {
		cook = or(active, total, new(big.Int))
	}
	if prep == nil {
		prep = new(big.Int)
	}
	if total == nil {
		pf, ok1 := pyFloat(prep)
		cf, ok2 := pyFloat(cook)
		if ok1 && ok2 {
			total = pf + cf
		} else {
			total = new(big.Int)
		}
	}

	var normIngs []any
	for _, ri := range collected {
		qty, unit, name := ri.qty, ri.unit, ri.name
		nameStr, _ := name.(string)
		if qs, ok := qty.(string); ok && qs != "" {
			hasNum := qtyDigits.MatchString(qs)
			hasLetters := asciiLetters.MatchString(qs)
			if hasNum && hasLetters {
				if q, u, rest, ok := extractQty(qs); ok {
					if q != "" {
						qty = q
					}
					if u != "" {
						unit = u
					}
					if rest != "" && nameStr == "" {
						nameStr = rest
					}
				}
			}
		}
		if !truthy(qty) && nameStr != "" {
			if q, u, rest, ok := extractQty(nameStr); ok {
				qty = q
				if !truthy(unit) && u != "" {
					unit = u
				}
				if rest != "" {
					nameStr = rest
				}
			}
		}
		if s, ok := qty.(string); ok && s == "" {
			qty = nil
		}
		if s, ok := unit.(string); ok && s == "" {
			unit = nil
		}
		notes := ri.notes
		if s, ok := notes.(string); ok && s == "" {
			notes = nil
		}
		if n := quantity.PyStrip(nameStr); n != "" {
			io := pyjson.NewObject()
			io.Set("name", n)
			io.Set("quantity", qty)
			io.Set("unit", unit)
			io.Set("notes", notes)
			normIngs = append(normIngs, io)
		}
	}
	if normIngs == nil {
		normIngs = []any{}
	}
	norm.Set("ingredients", normIngs)
	norm.Set("steps", steps)
	norm.Set("prep_time_minutes", prep)
	norm.Set("cook_time_minutes", cook)
	norm.Set("total_time_minutes", total)
	norm.Set("servings", get(o, "servings"))
	norm.Set("tags", or(get(o, "tags"), []any{}))
	src := pyjson.NewObject()
	src.Set("type", sourceType)
	norm.Set("source", src)
	d, err := validate(norm, true)
	if err != nil {
		return nil, err
	}
	if err := d.ValidateMinimums(); err != nil {
		return nil, err
	}
	return d, nil
}

// pyFloat is float(x) for a number or a numeric string.
func pyFloat(x any) (float64, bool) {
	switch n := x.(type) {
	case *big.Int:
		f, _ := new(big.Float).SetInt(n).Float64()
		return f, true
	case float64:
		return n, true
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	case string:
		f, err := strconv.ParseFloat(strings.ReplaceAll(quantity.PyStrip(n), "_", ""), 64)
		return f, err == nil
	}
	return 0, false
}

var (
	qtyDigits    = regexp.MustCompile(`[` + quantity.PyDigit + `/` + quantity.FractionChars + `]`)
	asciiLetters = regexp.MustCompile(`[A-Za-z]`)
	qtyChars     = `[` + quantity.PyDigit + quantity.PySpace + `/.\-` + quantity.FractionChars + `]+`
	ws           = `[` + quantity.PySpace + `]`
	qtyUnitRe    = regexp.MustCompile(`^` + ws + `*(` + qtyChars + `)` + ws + `+([A-Za-z][A-Za-z.\-]*)` + ws + `+(.*)$`)
	qtyOnlyRe    = regexp.MustCompile(`^` + ws + `*(` + qtyChars + `)` + ws + `+(.*)$`)
)

// draftUnits is _coerce_recipe_draft's COMMON_UNITS.
var draftUnits = map[string]bool{}

func init() {
	for _, u := range strings.Fields(`tsp teaspoon teaspoons tbsp tablespoon tablespoons cup cups oz ounce ounces lb
		pound pounds g gram grams kg ml l liter litre pint pt quart qt gallon gal stick clove cloves can cans package
		packages slice slices piece pieces inch inches`) {
		draftUnits[u] = true
	}
}

// extractQty is _extract_qty_from_text: a leading quantity, a known unit and the rest; or a
// leading quantity and the rest (no unit).
func extractQty(text string) (qty, unit, rest string, ok bool) {
	if m := qtyUnitRe.FindStringSubmatch(text); m != nil {
		if u := strings.ToLower(quantity.PyStrip(m[2])); draftUnits[u] {
			return quantity.PyStrip(m[1]), u, quantity.PyStrip(m[3]), true
		}
	}
	if m := qtyOnlyRe.FindStringSubmatch(text); m != nil {
		return quantity.PyStrip(m[1]), "", quantity.PyStrip(m[2]), true
	}
	return "", "", "", false
}
