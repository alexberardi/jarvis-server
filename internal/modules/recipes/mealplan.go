package recipes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

// AI meal-plan generation (§4.6, R9; meal_plan_service + llm_client.call_meal_plan_select):
// POST /meal-plans/generate/jobs (#31) queues a recipes.mealplan job; GET …/{job_id} (#32)
// polls it. Each slot (days in order, breakfast → dessert) gets up to 25 candidates from the
// household's box and P4 ranks them; the pick and up to two alternatives are returned and the
// app commits the plan itself.
//
// Changes from legacy: B13/RD7 the candidates are the caller's whole box (every household they
// belong to), not only their own recipes; RD2 no "core" stock recipes are added (they had
// placeholder ingredients), so nothing is staged; B18 the tag filter runs in SQL before the
// limit, in random order; B19 any model failure falls back to the first candidate; B20 the
// used-recipe set is keyed by (source, id). RD9: preferences are passed as they were (only
// excluded_ingredients and the soft tags shape the search; the rest reach P4 or nothing).

const (
	mealPlanJobType = "recipes.mealplan"
	p4MaxTokens     = 300
	p4Timeout       = 30 * time.Second
	maxCandidates   = 25
)

var mealOrder = []string{"breakfast", "lunch", "dinner", "snack", "dessert"}

// --- request (MealPlanGenerateRequest) ---

type repeatHint struct {
	Mode  string `json:"mode"`
	Count int64  `json:"count"`
}

type mealSlot struct {
	Servings   int64       `json:"servings"`
	Tags       []string    `json:"tags"`
	Note       *string     `json:"note"`
	IsMealPrep bool        `json:"is_meal_prep"`
	Repeat     *repeatHint `json:"repeat"`
}

type dayInput struct {
	Date  string              `json:"date"`
	Meals map[string]mealSlot `json:"meals"`
}

type hardPrefs struct {
	Allergens           []string `json:"allergens"`
	ExcludedIngredients []string `json:"excluded_ingredients"`
	Diet                *string  `json:"diet"`
}

type softPrefs struct {
	Tags           []string `json:"tags"`
	Cuisines       []string `json:"cuisines"`
	MaxPrepMinutes *int64   `json:"max_prep_minutes"`
	MaxCookMinutes *int64   `json:"max_cook_minutes"`
}

type mealPlanRequest struct {
	Days        []dayInput `json:"days"`
	Preferences struct {
		Hard hardPrefs `json:"hard"`
		Soft softPrefs `json:"soft"`
	} `json:"preferences"`
}

const msgGreaterThan0 = "Input should be greater than 0"

func strListOrEmpty(o *obj, name string) []string {
	l, ok := o.strList(name, false)
	if !ok || l == nil {
		return []string{}
	}
	return l
}

func (o *obj) gt0(name string) int64 {
	n := o.reqInt(name)
	if _, present := o.m[name]; present && o.m[name] != nil && n <= 0 {
		o.fail(msgGreaterThan0, name)
	}
	return n
}

// parseMealPlanRequest validates the body like pydantic (extra keys such as the app's
// pinned_recipe_id and allow_external_recipes are ignored).
func parseMealPlanRequest(o *obj) mealPlanRequest {
	var req mealPlanRequest
	req.Days = []dayInput{}
	if l, ok := o.list("days", true, false); ok {
		if len(l) == 0 {
			o.fail("Value error, days must not be empty", "days")
		}
		for i, e := range l {
			d, ok := o.child(e, "Input should be a valid dictionary or instance of DayInput", "days", i)
			if !ok {
				continue
			}
			day := dayInput{Date: d.date("date"), Meals: map[string]mealSlot{}}
			mv, present := d.m["meals"]
			meals, isObj := mv.(map[string]any)
			switch {
			case !present:
				d.fail(msgMissing, "meals")
			case !isObj:
				d.fail("Input should be a valid dictionary", "meals")
			case len(meals) == 0:
				d.fail("Value error, meals must not be empty", "meals")
			}
			keys := make([]string, 0, len(meals))
			for k := range meals {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if !slices.Contains(mealOrder, k) {
					d.fail(literalMsg(mealOrder), "meals", k, "[key]")
					continue
				}
				s, ok := d.child(meals[k], "Input should be a valid dictionary or instance of MealSlotInput", "meals", k)
				if !ok {
					continue
				}
				slot := mealSlot{Servings: s.gt0("servings"), Tags: strListOrEmpty(s, "tags"), Note: s.optStr("note"),
					IsMealPrep: s.optBool("is_meal_prep", false)}
				if s.has("repeat") {
					if rp, ok := s.child(s.m["repeat"], "Input should be a valid dictionary or instance of RepeatHint", "repeat"); ok {
						h := repeatHint{Count: rp.gt0("count")}
						if m, ok := rp.m["mode"].(string); ok && (m == "same" || m == "similar") {
							h.Mode = m
						} else if _, present := rp.m["mode"]; !present {
							rp.fail(msgMissing, "mode")
						} else {
							rp.fail(literalMsg([]string{"same", "similar"}), "mode")
						}
						slot.Repeat = &h
					}
				}
				day.Meals[k] = slot
			}
			req.Days = append(req.Days, day)
		}
	}
	req.Preferences.Hard = hardPrefs{Allergens: []string{}, ExcludedIngredients: []string{}}
	req.Preferences.Soft = softPrefs{Tags: []string{}, Cuisines: []string{}}
	if o.has("preferences") {
		if p, ok := o.child(o.m["preferences"], "Input should be a valid dictionary or instance of Preferences", "preferences"); ok {
			if p.has("hard") {
				if h, ok := p.child(p.m["hard"], "Input should be a valid dictionary or instance of HardPrefs", "hard"); ok {
					req.Preferences.Hard = hardPrefs{Allergens: strListOrEmpty(h, "allergens"),
						ExcludedIngredients: strListOrEmpty(h, "excluded_ingredients"), Diet: h.optStr("diet")}
				}
			}
			if p.has("soft") {
				if s, ok := p.child(p.m["soft"], "Input should be a valid dictionary or instance of SoftPrefs", "soft"); ok {
					req.Preferences.Soft = softPrefs{Tags: strListOrEmpty(s, "tags"), Cuisines: strListOrEmpty(s, "cuisines"),
						MaxPrepMinutes: s.optInt("max_prep_minutes"), MaxCookMinutes: s.optInt("max_cook_minutes")}
				}
			}
		}
	}
	return req
}

// handleGenerateMealPlan is POST /meal-plans/generate/jobs (#31): 202 {job_id, request_id}.
func (m *Module) handleGenerateMealPlan(w http.ResponseWriter, r *http.Request, c caller) {
	o, ok := readBody(w, r)
	if !ok {
		return
	}
	req := parseMealPlanRequest(o)
	if !o.done(w) {
		return
	}
	ctx := r.Context()
	requestID := newUUID()
	var id string
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = m.createJob(ctx, tx, c, jobTypeMealPlan, mealPlanJobType, map[string]any{"request_id": requestID, "payload": req})
		return err
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	m.deps.Queue.Notify(mealPlanJobType)
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": id, "request_id": requestID})
}

// handleGetMealPlanJob is GET /meal-plans/generate/jobs/{job_id} (#32): author only, and only
// meal-plan jobs (anything else is 404 "Job not found").
func (m *Module) handleGetMealPlanJob(w http.ResponseWriter, r *http.Request, c caller) {
	pred, args := c.authorOnly("")
	var j parseJob
	err := m.deps.DB.Read.QueryRowContext(r.Context(), `SELECT id, status, result_json, error_code, error_message
		FROM recipes_recipe_parse_jobs WHERE id = ? AND job_type = ? AND `+pred,
		append([]any{r.PathValue("job_id"), jobTypeMealPlan}, args...)...).
		Scan(&j.ID, &j.Status, &j.Result, &j.ErrorCode, &j.ErrorMessage)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "Job not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	var result any
	if j.Result.Valid {
		result = json.RawMessage(j.Result.String)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id": j.ID, "status": j.Status, "result": result, "error_code": nullStr(j.ErrorCode), "error_message": nullStr(j.ErrorMessage),
	})
}

// --- candidates ---

// candidate is a search_recipes row (user recipes only: RD2).
type candidate struct {
	ID, Source, Title string
	Description       *string
	Tags              []string
	Prep              int64
	Cook              *int64 // legacy sends the total time as cook_time_minutes
}

// searchCandidates is search_recipes: the caller's visible recipes matching the note (title or
// description), not titled with an excluded term (first 5 of each), tagged with one of tags
// when there are any (case-insensitive), not already used, random order, at most limit.
func (m *Module) searchCandidates(ctx context.Context, c caller, tags, include, exclude []string, used map[string]bool,
	limit int) ([]candidate, error) {
	pred, args := c.visible("r")
	q := `SELECT r.id, r.title, r.description, r.total_time_minutes FROM recipes_recipes r WHERE ` + pred
	for i, t := range include {
		if i == 5 {
			break
		}
		q += ` AND (r.title LIKE ? ESCAPE '\' OR r.description LIKE ? ESCAPE '\')`
		args = append(args, likeAny(t), likeAny(t))
	}
	for i, t := range exclude {
		if i == 5 {
			break
		}
		q += ` AND r.title NOT LIKE ? ESCAPE '\'`
		args = append(args, likeAny(t))
	}
	var usedIDs []any
	for k := range used {
		if id, ok := strings.CutPrefix(k, "user:"); ok {
			usedIDs = append(usedIDs, id)
		}
	}
	if len(usedIDs) > 0 {
		q += ` AND CAST(r.id AS TEXT) NOT IN (?` + strings.Repeat(",?", len(usedIDs)-1) + `)`
		args = append(args, usedIDs...)
	}
	if len(tags) > 0 {
		q += ` AND EXISTS (SELECT 1 FROM recipes_recipe_tags rt JOIN recipes_tags t ON t.id = rt.tag_id
			WHERE rt.recipe_id = r.id AND lower(t.name) IN (?` + strings.Repeat(",?", len(tags)-1) + `))`
		for _, t := range tags {
			args = append(args, strings.ToLower(t))
		}
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, q+` ORDER BY random() LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	var out []candidate
	idx := map[int64]int{}
	for rows.Next() {
		var id int64
		var cd candidate
		var desc sql.NullString
		var total sql.NullInt64
		if err := rows.Scan(&id, &cd.Title, &desc, &total); err != nil {
			rows.Close()
			return nil, err
		}
		cd.ID, cd.Source, cd.Description, cd.Cook, cd.Tags = fmt.Sprint(id), "user", nullStr(desc), nullInt(total), []string{}
		idx[id] = len(out)
		out = append(out, cd)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}
	ids := make([]any, 0, len(idx))
	for id := range idx {
		ids = append(ids, id)
	}
	rows, err = m.deps.DB.Read.QueryContext(ctx, `SELECT rt.recipe_id, t.name FROM recipes_recipe_tags rt
		JOIN recipes_tags t ON t.id = rt.tag_id WHERE rt.recipe_id IN (?`+strings.Repeat(",?", len(ids)-1)+`) ORDER BY rt.rowid`, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rid int64
		var name string
		if err := rows.Scan(&rid, &name); err != nil {
			return nil, err
		}
		out[idx[rid]].Tags = append(out[idx[rid]].Tags, name)
	}
	return out, rows.Err()
}

// likeAny is %term% with LIKE's wildcards escaped (ILIKE in legacy; SQLite's LIKE ignores ASCII case).
func likeAny(t string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(t) + "%"
}

// --- P4 ---

// p4System is call_meal_plan_select's system prompt, byte for byte.
const p4System = "You are a meal planning assistant that selects and ranks recipes from a provided candidate list. " +
	"Your job is to interpret user intent (notes, tags, preferences), encourage variety using recent meal history, " +
	"and return the TOP 3 RANKED recipes that best fit the slot. " +
	"\n\nRULES:\n" +
	"- Return your TOP 3 recipe choices in ranked order (best first).\n" +
	"- You MUST select recipe_ids from the provided candidates list.\n" +
	"- You MUST NOT invent, create, or select recipes not in the candidates list.\n" +
	"- Prefer to suggest options over nothing - even if not perfect matches.\n" +
	"- ONLY return null for the primary selection if candidates are truly incompatible (e.g., user wants vegan but all candidates have meat).\n" +
	"- Variety is a soft constraint: prefer different recipes/proteins across consecutive days when alternatives exist.\n" +
	"- Interpret free-text notes (e.g., 'something easy', 'I want chicken') as ranking signals, not hard requirements.\n" +
	"- Tags and preferences are guidance, not absolute filters - be flexible and helpful.\n" +
	"- Use confidence scores to indicate match quality: 0.9-1.0 = excellent, 0.7-0.9 = good, 0.5-0.7 = acceptable, <0.5 = poor.\n" +
	"- For each ranked option, provide a brief reason explaining why it's a good choice.\n" +
	"- Return ONLY valid JSON matching this schema:\n" +
	`  { "ranked_recipes": [{"recipe_id": "string", "confidence": 0.0-1.0, "reason": "why this fits"}], ` +
	`"warnings": ["optional warning strings"] }` + "\n" +
	"- The ranked_recipes array should contain 1-3 recipes in priority order.\n" +
	"- If no candidates fit at all, return ranked_recipes as an empty array with warnings explaining why."

// p4User is the user prompt: {slot, preferences, recent_meals, candidates} as json.dumps
// (indent=2), the first 25 candidates summarised (description cut to 100 characters).
func p4User(slot, prefs, recent any, cands []candidate) string {
	sums := []any{}
	for i, c := range cands {
		if i == maxCandidates {
			break
		}
		s := pyjson.NewObject()
		s.Set("recipe_id", c.ID)
		s.Set("title", c.Title)
		tags := make([]any, 0, len(c.Tags))
		for _, t := range c.Tags {
			tags = append(tags, t)
		}
		s.Set("tags", tags)
		s.Set("prep_time", big.NewInt(c.Prep))
		if c.Cook != nil {
			s.Set("cook_time", big.NewInt(*c.Cook))
		} else {
			s.Set("cook_time", nil)
		}
		if c.Description != nil && *c.Description != "" {
			s.Set("summary", truncRunes(*c.Description, 100))
		} else {
			s.Set("summary", nil)
		}
		sums = append(sums, s)
	}
	in := pyjson.NewObject()
	in.Set("slot", slot)
	in.Set("preferences", prefs)
	in.Set("recent_meals", recent)
	in.Set("candidates", sums)
	return "Select the best recipe for this meal slot:\n" + pyjson.DumpsIndent(in, true, 2) +
		"\n\nReturn your selection as JSON only. No prose, no markdown."
}

func truncRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

type ranked struct {
	RecipeID   string  `json:"recipe_id"`
	Confidence float64 `json:"confidence"`
	Reason     any     `json:"reason"`
}

// mealSelect is call_meal_plan_select's result.
type mealSelect struct {
	SelectedID   *string  `json:"selected_recipe_id"`
	Confidence   float64  `json:"confidence"`
	Reason       string   `json:"reason"`
	Warnings     []any    `json:"warnings"`
	Alternatives []ranked `json:"alternatives"`
}

func selectFailure(reason string, warnings ...any) mealSelect {
	return mealSelect{Reason: reason, Warnings: warnings, Alternatives: []ranked{}}
}

// parseSelection reads P4's reply: up to 3 ranked ids that are candidates (ids are strings:
// an integer id never matches), confidence 0.5 when missing or outside 0–1 (true is 1.0).
func parseSelection(content string, cands []candidate) mealSelect {
	if content == "" {
		return selectFailure("LLM returned empty response", "Empty LLM response")
	}
	v, err := pyjson.Loads(content)
	if err != nil {
		return selectFailure("Exception: "+err.Error(), "LLM error")
	}
	obj, ok := v.(*pyjson.Object)
	if !ok {
		return selectFailure("Exception: the reply is not an object", "LLM error")
	}
	ids := map[string]bool{}
	for _, c := range cands {
		ids[c.ID] = true
	}
	rr, _ := obj.Get("ranked_recipes")
	list, _ := rr.([]any)
	wv, has := obj.Get("warnings")
	warnings, ok := wv.([]any)
	if !has || wv == nil {
		warnings = []any{}
	} else if !ok {
		return selectFailure("Exception: warnings is not a list", "LLM error")
	}
	var valid []ranked
	for i, e := range list {
		if i == 3 {
			break
		}
		ro, ok := e.(*pyjson.Object)
		if !ok {
			return selectFailure("Exception: a ranked recipe is not an object", "LLM error")
		}
		idv, _ := ro.Get("recipe_id")
		id, isStr := idv.(string)
		if !isStr || id == "" || !ids[id] {
			continue
		}
		conf := 0.5
		cv, present := ro.Get("confidence")
		if !present {
			cv = 0.5
		}
		switch x := cv.(type) {
		case float64:
			conf = x
		case *big.Int:
			f, _ := new(big.Float).SetInt(x).Float64()
			conf = f
		case bool:
			conf = 0
			if x {
				conf = 1
			}
		default:
			conf = 0.5
		}
		if conf < 0 || conf > 1 {
			conf = 0.5
		}
		reason, has := ro.Get("reason")
		if !has {
			reason = ""
		}
		valid = append(valid, ranked{RecipeID: id, Confidence: conf, Reason: reason})
	}
	if len(valid) == 0 {
		return selectFailure("No valid recipes returned by LLM", append(warnings, "LLM returned no valid selections")...)
	}
	reason, _ := valid[0].Reason.(string)
	return mealSelect{SelectedID: &valid[0].RecipeID, Confidence: valid[0].Confidence, Reason: reason,
		Warnings: warnings, Alternatives: valid[1:]}
}

// --- generation ---

type alternative struct {
	Source      string   `json:"source"`
	RecipeID    string   `json:"recipe_id"`
	Title       string   `json:"title"`
	Confidence  float64  `json:"confidence"`
	Reason      any      `json:"reason"`
	MatchedTags []string `json:"matched_tags"`
}

type selection struct {
	Source       string        `json:"source"`
	RecipeID     *string       `json:"recipe_id"`
	Confidence   *float64      `json:"confidence"`
	MatchedTags  []string      `json:"matched_tags"`
	Warnings     []any         `json:"warnings"`
	Alternatives []alternative `json:"alternatives"`
}

type slotResult struct {
	mealSlot
	Selection *selection `json:"selection"`
}

type dayResult struct {
	Date  string                `json:"date"`
	Meals map[string]slotResult `json:"meals"`
}

// generateMealPlan is generate_meal_plan: the plan and the number of slots left unfilled.
func (m *Module) generateMealPlan(ctx context.Context, c caller, req mealPlanRequest) ([]dayResult, int, error) {
	days := append([]dayInput(nil), req.Days...)
	sort.SliceStable(days, func(i, j int) bool { return days[i].Date < days[j].Date })
	used := map[string]bool{} // B20: keyed by source:id
	failures := 0
	label := m.label(ctx, SettingFullModel)
	out := []dayResult{}
	for _, day := range days {
		dr := dayResult{Date: day.Date, Meals: map[string]slotResult{}}
		for _, meal := range mealOrder {
			slot, ok := day.Meals[meal]
			if !ok {
				continue
			}
			tags := slot.Tags
			if len(tags) == 0 {
				tags = req.Preferences.Soft.Tags
			}
			var include []string
			if slot.Note != nil && *slot.Note != "" {
				include = []string{*slot.Note}
			}
			exclude := req.Preferences.Hard.ExcludedIngredients
			cands, err := m.searchCandidates(ctx, c, tags, include, exclude, used, maxCandidates)
			if err != nil {
				return nil, 0, err
			}
			var sel *selection
			if len(cands) > 0 {
				res := m.selectRecipe(ctx, label, day.Date, meal, slot, req, cands)
				var cand *candidate
				alts := res.Alternatives
				conf := &res.Confidence
				warnings := res.Warnings
				llmFailed := slices.ContainsFunc(warnings, func(w any) bool {
					s, _ := w.(string)
					return s == "LLM error" || s == "LLM unavailable" || s == "Empty LLM response" || s == "LLM proxy error"
				})
				switch {
				case res.SelectedID != nil:
					for i := range cands {
						if cands[i].ID == *res.SelectedID {
							cand = &cands[i]
						}
					}
					if cand == nil {
						failures++
					}
				case llmFailed:
					// B19: any model failure (legacy missed "LLM proxy error") falls back.
					cand, conf = &cands[0], nil
					warnings = append(warnings, "LLM unavailable, using deterministic selection")
					alts = nil
					for _, a := range cands[1:min(3, len(cands))] {
						alts = append(alts, ranked{RecipeID: a.ID, Confidence: 0.5, Reason: "Alternative option"})
					}
				default:
					failures++ // the model found no good fit
				}
				if cand != nil {
					altList := []alternative{}
					for _, a := range alts {
						for _, ac := range cands {
							if ac.ID == a.RecipeID {
								altList = append(altList, alternative{Source: "user", RecipeID: ac.ID, Title: ac.Title,
									Confidence: a.Confidence, Reason: a.Reason, MatchedTags: ac.Tags})
								break
							}
						}
					}
					id := cand.ID
					sel = &selection{Source: "user", RecipeID: &id, Confidence: conf, MatchedTags: cand.Tags,
						Warnings: warnings, Alternatives: altList}
					used["user:"+id] = true
				}
			} else {
				failures++
				// "Nothing matches" vs "the matches are already on this plan".
				match, err := m.searchCandidates(ctx, c, tags, include, exclude, nil, 1)
				if err != nil {
					return nil, 0, err
				}
				if len(match) > 0 {
					sel = &selection{Source: "user", MatchedTags: []string{}, Alternatives: []alternative{},
						Warnings: []any{"already_used: every recipe matching these tags is already on this plan"}}
				}
			}
			if sel != nil && sel.Warnings == nil {
				sel.Warnings = []any{}
			}
			dr.Meals[meal] = slotResult{mealSlot: slot, Selection: sel}
		}
		out = append(out, dr)
	}
	return out, failures, nil
}

// selectRecipe runs P4 for one slot.
func (m *Module) selectRecipe(ctx context.Context, label, date, meal string, slot mealSlot, req mealPlanRequest,
	cands []candidate) mealSelect {
	s := pyjson.NewObject()
	s.Set("date", date)
	s.Set("meal_type", meal)
	s.Set("servings", big.NewInt(slot.Servings))
	tags := []any{}
	for _, t := range slot.Tags {
		tags = append(tags, t)
	}
	s.Set("tags", tags)
	if slot.Note != nil {
		s.Set("notes", *slot.Note)
	} else {
		s.Set("notes", nil)
	}
	s.Set("is_meal_prep", slot.IsMealPrep)
	p := pyjson.NewObject()
	p.Set("diet", nil)
	excl := []any{}
	for _, e := range req.Preferences.Hard.ExcludedIngredients {
		excl = append(excl, e)
	}
	p.Set("excluded_ingredients", excl)
	opt := func(v *int64) any {
		if v == nil {
			return nil
		}
		return big.NewInt(*v)
	}
	p.Set("max_prep_minutes", opt(req.Preferences.Soft.MaxPrepMinutes))
	p.Set("max_cook_minutes", opt(req.Preferences.Soft.MaxCookMinutes))
	reply, err := m.chatJSON(ctx, label, 0.2, p4MaxTokens, p4Timeout, p4System, p4User(s, p, []any{}, cands))
	if err != nil {
		m.deps.Log.Warn("recipes: meal plan selection failed", "err", err)
		return selectFailure("Exception: "+err.Error(), "LLM error")
	}
	return parseSelection(reply, cands)
}

func (m *Module) runMealPlan(ctx context.Context, qj queue.Job) ([]byte, error) {
	j, run, err := m.claimJob(ctx, qj)
	if err != nil || !run {
		return nil, err
	}
	var data struct {
		RequestID string          `json:"request_id"`
		Payload   mealPlanRequest `json:"payload"`
	}
	if err := json.Unmarshal([]byte(j.JobData.String), &data); err != nil {
		return nil, m.markError(ctx, j.ID, "invalid_payload", err.Error())
	}
	c, err := m.jobCaller(ctx, j)
	if err != nil {
		return nil, m.markError(ctx, j.ID, "generation_failed", err.Error())
	}
	days, failures, err := m.generateMealPlan(ctx, c, data.Payload)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		m.deps.Log.Error("recipes: meal plan job failed", "parse_job_id", j.ID, "job_type", j.JobType, "err", err)
		return nil, m.markError(context.WithoutCancel(ctx), j.ID, "generation_failed", err.Error())
	}
	m.deps.Log.Info("recipes: meal plan job done", "parse_job_id", j.ID, "job_type", j.JobType, "outcome", "complete",
		"slot_failures", failures)
	return nil, m.markComplete(ctx, j.ID, map[string]any{"result": map[string]any{"days": days}, "slot_failures_count": failures})
}
