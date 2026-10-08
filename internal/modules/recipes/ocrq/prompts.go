package ocrq

import (
	"fmt"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// P2 (OCR text → RecipeDraft) and P3 (draft cleanup), byte for byte (llm_client.py).

const p2Base = "Convert OCR text to RecipeDraft JSON. Return ONLY JSON.\n\n" +
	`Schema: {"title":string,"description":string|null,"ingredients":[{"name":string,"quantity":string|null,"unit":string|null,"notes":string|null}],` +
	`"steps":[string],"prep_time_minutes":int,"cook_time_minutes":int,"total_time_minutes":int,"servings":string|number|null,"tags":[string],"source":{"type":"ocr"}}` + "\n\n" +
	"Rules:\n" +
	"- Separate ingredients: 'salt and pepper' = 2 entries\n" +
	"- Extract units from names: '1 cup flour' → quantity:'1', unit:'cup', name:'flour'\n" +
	"- Put prep notes in 'notes' field\n" +
	"- Use 0 for unknown time fields, null for missing description\n" +
	`- If not a valid recipe, return {"error":"garbage_ocr"}` + "\n"

// ensembleRules is _ENSEMBLE_RULES, appended when the model sees more than one reading.
const ensembleRules = "- You are given SEVERAL independent OCR readings of the SAME image, IN ORDER " +
	"OF RELIABILITY. READING 1 is from the most accurate engine.\n" +
	"- Take the LIST of ingredients and steps from READING 1.\n" +
	"- Use the later readings ONLY to settle something READING 1 left unclear: a " +
	"smudged quantity, an ambiguous unit, a word that could be two things. Where " +
	"they agree with READING 1, that is confirmation.\n" +
	"- NEVER add an ingredient or a step that appears only in a later reading. " +
	"The weaker engines often read decoration printed on the page -- tab labels, " +
	"a caption beside an illustration -- more clearly than they read the recipe.\n"

// P2System is the structuring prompt's system message for n readings.
func P2System(n int) string {
	if n > 1 {
		return p2Base + ensembleRules
	}
	return p2Base
}

// P2User is _readings_message: one reading verbatim between markers, or several labelled
// blocks (an unnamed engine is "engine <n>").
func P2User(texts []Text) string {
	if len(texts) == 1 {
		return "OCR TEXT (verbatim):\n<<<OCR_START>>>\n" + texts[0].Text + "\n<<<OCR_END>>>"
	}
	blocks := make([]string, 0, len(texts))
	for i, t := range texts {
		label := t.Provider
		if label == "" {
			label = fmt.Sprintf("engine %d", i+1)
		}
		blocks = append(blocks, fmt.Sprintf("OCR READING %d (%s):\n<<<R%d_START>>>\n%s\n<<<R%d_END>>>", i+1, label, i+1, t.Text, i+1))
	}
	return strings.Join(blocks, "\n\n")
}

// P3System is clean_and_validate_draft's system message.
const P3System = "Clean recipe data. Return ONLY valid JSON matching RecipeDraft schema.\n\n" +
	"Rules:\n" +
	"- Separate ingredients: 'salt and pepper' = 2 entries\n" +
	"- Extract units from names: '1 cup flour' → quantity:'1', unit:'cup', name:'flour'\n" +
	"- Put prep notes in 'notes' field\n" +
	"- Add description if missing (1-2 sentences based on title/ingredients)\n" +
	"- Preserve all valid data, only clean formatting\n"

// P3User is the cleanup's user message: the draft as json.dumps(indent=2).
func P3User(d *Draft) string { return "Clean this recipe:\n" + pyjson.DumpsIndent(d, true, 2) }

// DraftSchemaHint is the schema P1r repairs a RecipeDraft reply against (_parse_with_repair).
const DraftSchemaHint = `{ "title": string, "description": string|null, "ingredients": ` +
	`[{"name": string, "quantity": string|null, "unit": string|null, "notes": string|null}], ` +
	`"steps": [string], "prep_time_minutes": number, "cook_time_minutes": number, ` +
	`"total_time_minutes": number, "servings": string|number|null, "tags": [string], ` +
	`"source": {"type":"image"|"ocr"|"url", "source_url": string|null, "image_url": string|null} }`

// cleanupUnits are the unit words the P3 trigger looks for in an ingredient name (a substring
// test: "egg" contains "g", so it fires almost always — legacy, frozen).
var cleanupUnits = []string{"cup", "cups", "tsp", "teaspoon", "tbsp", "tablespoon", "oz", "ounce", "lb", "pound",
	"g", "gram", "kg", "ml", "liter", "clove", "cloves"}

// NeedsCleanup is the P3 trigger: the draft fails the minimums, an ingredient without a unit
// has a unit word in its name, a name holds " and " or a comma, or there is no description.
func NeedsCleanup(d *Draft) bool {
	if d.ValidateMinimums() != nil {
		return true
	}
	for _, ing := range d.Ingredients {
		name := strings.ToLower(ing.Name)
		if ing.Unit == nil || *ing.Unit == "" {
			for _, u := range cleanupUnits {
				if strings.Contains(name, u) {
					return true
				}
			}
		}
		if strings.Contains(name, " and ") || (strings.Contains(name, ",") && len(strings.Split(name, ",")) > 1) {
			return true
		}
	}
	return d.Description == nil || *d.Description == ""
}
