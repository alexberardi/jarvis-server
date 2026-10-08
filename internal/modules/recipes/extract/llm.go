package extract

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/alexberardi/jarvis-server/internal/modules/recipes/quantity"
)

// The LLM fallback's prompt (P1, extractors/llm.py) and response handling, plus the shared JSON
// helpers of llm_client.py (P1r repair, local repair, control-char strip).

// ErrCorrupted is _build_llm_content's refusal of a page that looks binary.
var ErrCorrupted = errors.New("HTML content appears corrupted - encoding error detected")

// corrupted is the printable/control ratio test on the first 2000 characters.
func corrupted(s string, minPrintable, maxControl float64) bool {
	sample := []rune(s)
	if len(sample) > 2000 {
		sample = sample[:2000]
	}
	if len(sample) == 0 {
		return false
	}
	printable, control := 0, 0
	for _, c := range sample {
		if (c >= 32 && c <= 126) || unicode.IsSpace(c) || quantity.IsPySpace(c) {
			printable++
		}
		if c < 32 && c != '\n' && c != '\r' && c != '\t' {
			control++
		}
	}
	n := float64(len(sample))
	return float64(printable)/n < minPrintable || float64(control)/n > maxControl
}

var blankLines = regexp.MustCompile(`\n{2,}`)

// splitLines is str.splitlines.
func splitLines(s string) []string {
	f := func(r rune) bool {
		switch r {
		case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			return true
		}
		return false
	}
	var out []string
	start := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if f(rs[i]) {
			out = append(out, string(rs[start:i]))
			if rs[i] == '\r' && i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

// LLMContent is _build_llm_content: the page's title and the text the model sees — ingredient
// and instruction lists from the main node (padded with up to 200 lines of its text when they
// come to under 500 characters), at most 10 000 characters; or, with no main node, the JSON-LD
// texts (2000 each, 6000 in all).
func LLMContent(markup string) (title *string, content string, err error) {
	if len([]rune(markup)) > 100 && corrupted(markup, 0.5, 0.15) {
		return nil, "", ErrCorrupted
	}
	d := parse(markup)
	if t := d.title(); t != nil {
		s := quantity.CleanText(text(t, "", false))
		title = &s
	}
	var scripts []string
	for _, sc := range findAll(d.root, isLDScript) {
		if t := text(sc, "", true); t != "" {
			scripts = append(scripts, truncRunes(t, 2000))
		}
	}
	cleanForContent(d)
	main := findMainNode(d)
	if main == nil {
		return title, truncRunes(strings.Join(scripts, "\n"), 6000), nil
	}
	var parts []string
	if ing := ingredientItems(main); len(ing) > 0 {
		parts = append(parts, "Ingredients:\n"+strings.Join(ing, "\n"))
	}
	if ins := instructionItems(main); len(ins) > 0 {
		parts = append(parts, "Instructions:\n"+strings.Join(ins, "\n"))
	}
	if len([]rune(strings.Join(parts, "\n\n"))) < 500 {
		t := blankLines.ReplaceAllString(text(main, "\n", true), "\n")
		lines := splitLines(t)
		if len(lines) > 200 {
			lines = lines[:200]
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	combined := strings.Join(kept, "\n\n")
	if title != nil && *title != "" {
		combined = *title + "\n" + combined
	}
	return title, truncRunes(combined, 10000), nil
}

// P1 is the URL/HTML extraction prompt, byte for byte.
const (
	P1System = `Extract recipe from HTML text. Return ONLY valid JSON matching the schema. If invalid, return {"error":"invalid"}.`
	p1Schema = `{"title":string,"description":string|null,"source_url":string|null,"image_url":string|null,` +
		`"tags":["string"],"servings":number|null,"estimated_time_minutes":number|null,` +
		`"ingredients":[{"text":string,"quantity_display":string|null,"unit":string|null}],` +
		`"steps":["string"],"notes":["string"]}` + "\n\n"
	p1Rules = "Rules:\n" +
		"- Separate ingredients: 'salt and pepper' = 2 entries\n" +
		"- Extract units: '1 cup flour' → text:'flour', quantity_display:'1', unit:'cup'\n" +
		"- Tags: general categories only (e.g., 'chicken', 'dinner'), not recipe names\n"
	// P1RecipeSchemaHint is the schema P1r repairs a ParsedRecipe reply against.
	P1RecipeSchemaHint = `{ "title": string, "description": string|null, "source_url": string|null, ` +
		`"image_url": string|null, "tags": [string], "servings": number|null, ` +
		`"estimated_time_minutes": number|null, "ingredients": [` +
		`{"text": string, "quantity_display": string|null, "unit": string|null}], ` +
		`"steps": [string], "notes": [string] }`
	// P1rSystem is _repair_json_via_full_llm's system prompt.
	P1rSystem = "Repair malformed JSON to match the schema. Return ONLY valid JSON."
)

// P1User is the user prompt for url and the content LLMContent built.
func P1User(url string, title *string, content string) string {
	t := "Unknown"
	if title != nil && *title != "" {
		t = *title
	}
	return "URL: " + url + "\nTitle: " + t + "\nContent:\n" + content + "\n\n" + p1Schema + p1Rules
}

// P1rUser is the repair prompt's user message.
func P1rUser(schemaHint, broken string) string {
	return "Schema: " + schemaHint + "\nMalformed JSON:\n" + broken
}

var (
	controlChars = regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F]`)
	fenceOpen    = regexp.MustCompile(`^` + "```" + `[a-zA-Z0-9_-]*`)
	fenceOpenWS  = regexp.MustCompile(`^` + "```" + `[a-zA-Z0-9_-]*\s*`)
	fenceCloseWS = regexp.MustCompile(`\s*` + "```" + `$`)
)

// StripControlChars is _strip_invalid_control_chars.
func StripControlChars(s string) string { return controlChars.ReplaceAllString(s, "") }

// ParseJSONContent is _parse_llm_json_content: the reply as JSON, else with a code fence
// stripped and cut to the outermost braces.
func ParseJSONContent(raw string) (any, error) {
	if v, err := decodeJSON(raw); err == nil {
		return v, nil
	}
	cleaned := quantity.PyStrip(raw)
	if strings.HasPrefix(cleaned, "```") {
		cleaned = quantity.PyStrip(fenceOpen.ReplaceAllString(cleaned, ""))
		if strings.HasSuffix(cleaned, "```") {
			cleaned = quantity.PyStrip(cleaned[:len(cleaned)-3])
		}
	}
	start, end := strings.Index(cleaned, "{"), strings.LastIndex(cleaned, "}")
	if start != -1 && end != -1 && end > start {
		if v, err := decodeJSON(cleaned[start : end+1]); err == nil {
			return v, nil
		}
	}
	return nil, errors.New("LLM response was not valid JSON")
}

// LocalRepair is _try_local_json_repair: the reply without control characters and code
// fences, if that is a JSON object, else its outermost {...} if that parses; ok is false
// otherwise.
func LocalRepair(raw string) (string, bool) {
	cleaned := quantity.PyStrip(StripControlChars(raw))
	cleaned = fenceOpenWS.ReplaceAllString(cleaned, "")
	cleaned = fenceCloseWS.ReplaceAllString(cleaned, "")
	if strings.HasPrefix(cleaned, "{") && strings.HasSuffix(cleaned, "}") {
		if _, err := decodeJSON(cleaned); err == nil {
			return cleaned, true
		}
	}
	start, end := strings.Index(cleaned, "{"), strings.LastIndex(cleaned, "}")
	if start != -1 && end != -1 && end > start {
		snippet := cleaned[start : end+1]
		if _, err := decodeJSON(snippet); err == nil {
			return snippet, true
		}
	}
	return "", false
}

// RepairReply is what _repair_json_via_full_llm keeps of the model's answer: the content with
// fences stripped, when it parses.
func RepairReply(content string) (string, bool) {
	r := quantity.PyStrip(content)
	r = fenceOpenWS.ReplaceAllString(r, "")
	r = fenceCloseWS.ReplaceAllString(r, "")
	if _, err := decodeJSON(r); err != nil {
		return "", false
	}
	return r, true
}

// RecipeFromJSON is ParsedRecipe(**parsed_json) (notes null → []), with the source URL
// defaulted to url. B5 widened to this path: a numeric quantity_display or unit is taken as
// its string (Qwen returns numbers), and a servings/time that is not a whole number is
// dropped instead of failing the whole recipe.
func RecipeFromJSON(v any, url string) (*Recipe, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("LLM response is not a JSON object")
	}
	r := &Recipe{Tags: []string{}, Ingredients: []quantity.Ingredient{}, Steps: []string{}, Notes: []string{}}
	t, ok := m["title"].(string)
	if !ok {
		return nil, errors.New("LLM response is not a recipe: title missing")
	}
	r.Title = t
	var err error
	field := func(name string) *string {
		if err != nil {
			return nil
		}
		s, e := optStr(m[name])
		if e != nil {
			err = fmt.Errorf("%s: %w", name, e)
		}
		return s
	}
	r.Description, r.SourceURL, r.ImageURL = field("description"), field("source_url"), field("image_url")
	if err != nil {
		return nil, err
	}
	r.Servings, r.EstimatedTimeMinutes = wholeNumber(m["servings"]), wholeNumber(m["estimated_time_minutes"])
	if r.Tags, err = strList(m["tags"], "tags"); err != nil {
		return nil, err
	}
	if r.Steps, err = strList(m["steps"], "steps"); err != nil {
		return nil, err
	}
	if m["notes"] != nil {
		if r.Notes, err = strList(m["notes"], "notes"); err != nil {
			return nil, err
		}
	}
	switch ings := m["ingredients"].(type) {
	case nil:
	case []any:
		for i, e := range ings {
			im, ok := e.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("ingredients.%d: not an object", i)
			}
			txt, ok := im["text"].(string)
			if !ok {
				return nil, fmt.Errorf("ingredients.%d.text: not a string", i)
			}
			ing := quantity.Ingredient{Text: txt}
			if ing.QuantityDisplay, err = numOrStr(im["quantity_display"]); err != nil {
				return nil, fmt.Errorf("ingredients.%d.quantity_display: %w", i, err)
			}
			if ing.Unit, err = numOrStr(im["unit"]); err != nil {
				return nil, fmt.Errorf("ingredients.%d.unit: %w", i, err)
			}
			r.Ingredients = append(r.Ingredients, ing)
		}
	default:
		return nil, errors.New("ingredients: not a list")
	}
	if r.SourceURL == nil || *r.SourceURL == "" {
		r.SourceURL = &url
	}
	return r, nil
}

var errNotString = errors.New("not a string")

func optStr(v any) (*string, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		return &x, nil
	}
	return nil, errNotString
}

func numOrStr(v any) (*string, error) {
	if n, ok := v.(json.Number); ok {
		s := n.String()
		return &s, nil
	}
	return optStr(v)
}

func strList(v any, name string) ([]string, error) {
	out := []string{}
	switch x := v.(type) {
	case nil:
		return out, nil
	case []any:
		for i, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("%s.%d: not a string", name, i)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s: not a list", name)
}

// wholeNumber is a lax pydantic int that reads as nil instead of failing.
func wholeNumber(v any) *int {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			n := int(i)
			return &n
		}
		if f, err := x.Float64(); err == nil && f == float64(int64(f)) {
			n := int(f)
			return &n
		}
	case string:
		if n, ok := quantity.PyInt(quantity.PyStrip(x)); ok {
			return &n
		}
	}
	return nil
}
