package recipes

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// The random "quick plan" (§3.4 rows #33, #34; random_plan_service). Instant and predictable:
// one query per slot, no job. Meal type and tags are a preference, not a filter.

type randomSlot struct {
	Date      *string `json:"date"`
	MealType  string  `json:"meal_type"`
	RecipeID  *int64  `json:"recipe_id"`
	Title     *string `json:"title"`
	ImageURL  *string `json:"image_url"`
	TotalTime *int64  `json:"total_time_minutes"`
	Servings  *int64  `json:"servings"`
}

// pickOne is pick_one: a random visible recipe not in exclude, preferring one tagged with the
// slot's tags (else its meal type), then the meal type alone, then anything. nil when the box
// has nothing left.
func (m *Module) pickOne(ctx context.Context, c caller, mealType string, exclude []int64, tags []string) (*randomSlot, error) {
	wanted := make([]string, 0, len(tags))
	for _, t := range tags {
		wanted = append(wanted, strings.ToLower(t))
	}
	if len(wanted) == 0 && mealType != "" {
		wanted = []string{strings.ToLower(mealType)}
	}
	tries := [][]string{wanted}
	if len(tags) > 0 && mealType != "" {
		tries = append(tries, []string{strings.ToLower(mealType)})
	} else if len(tags) > 0 {
		tries = append(tries, nil) // the meal type alone is no filter at all
	}
	tries = append(tries, nil)
	for _, t := range tries {
		s, err := m.randomRecipe(ctx, c, exclude, t)
		if s != nil || err != nil {
			return s, err
		}
	}
	return nil, nil
}

// randomRecipe draws one visible recipe outside exclude, tagged with one of tags (any recipe
// when tags is empty). Tag names compare lowercased.
func (m *Module) randomRecipe(ctx context.Context, c caller, exclude []int64, tags []string) (*randomSlot, error) {
	pred, args := c.visible("r")
	q := `SELECT r.id, r.title, r.image_url, r.total_time_minutes, r.servings FROM recipes_recipes r WHERE ` + pred
	if len(exclude) > 0 {
		q += ` AND r.id NOT IN (?` + strings.Repeat(",?", len(exclude)-1) + `)`
		for _, id := range exclude {
			args = append(args, id)
		}
	}
	if len(tags) > 0 {
		q += ` AND EXISTS (SELECT 1 FROM recipes_recipe_tags rt JOIN recipes_tags t ON t.id = rt.tag_id
			WHERE rt.recipe_id = r.id AND lower(t.name) IN (?` + strings.Repeat(",?", len(tags)-1) + `))`
		for _, t := range tags {
			args = append(args, t)
		}
	}
	var s randomSlot
	var id int64
	var title string
	var img sql.NullString
	var total, servings sql.NullInt64
	err := m.deps.DB.Read.QueryRowContext(ctx, q+` ORDER BY random() LIMIT 1`, args...).
		Scan(&id, &title, &img, &total, &servings)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.RecipeID, s.Title, s.ImageURL, s.TotalTime, s.Servings = &id, &title, nullStr(img), nullInt(total), nullInt(servings)
	return &s, nil
}

// handleRandomPlan is POST /meal-plans/random (#33): every slot filled from the box without
// repeats; a slot with nothing left comes back empty and the plan is incomplete.
func (m *Module) handleRandomPlan(w http.ResponseWriter, r *http.Request, c caller) {
	o, ok := readBody(w, r)
	if !ok {
		return
	}
	type slotIn struct{ date, meal string }
	var slots []slotIn
	if l, ok := o.list("slots", true, false); ok {
		for i, e := range l {
			s, ok := o.child(e, "Input should be a valid dictionary or instance of RandomSlotRequest", "slots", i)
			if !ok {
				continue
			}
			slots = append(slots, slotIn{s.date("date"), s.str("meal_type")})
		}
	}
	used := o.intList("exclude_recipe_ids")
	if !o.done(w) {
		return
	}
	out := []randomSlot{}
	incomplete := false
	for _, sl := range slots {
		s, err := m.pickOne(r.Context(), c, sl.meal, used, nil)
		if err != nil {
			m.internalError(w, err)
			return
		}
		if s == nil {
			s = &randomSlot{}
			incomplete = true
		} else {
			used = append(used, *s.RecipeID)
		}
		d := sl.date
		s.Date, s.MealType = &d, sl.meal
		out = append(out, *s)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"slots": out, "incomplete": incomplete})
}

// handleReroll is POST /meal-plans/random/reroll (#34): one replacement, date null, meal_type
// echoed (or ""); 409 when nothing else is left.
func (m *Module) handleReroll(w http.ResponseWriter, r *http.Request, c caller) {
	o, ok := readBody(w, r)
	if !ok {
		return
	}
	meal := o.optStr("meal_type")
	exclude := o.intList("exclude_recipe_ids")
	tags, _ := o.strList("tags", false)
	if !o.done(w) {
		return
	}
	mt := ""
	if meal != nil {
		mt = *meal
	}
	s, err := m.pickOne(r.Context(), c, mt, exclude, tags)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if s == nil {
		httpx.Error(w, http.StatusConflict, "No other recipe available to swap in. Add more recipes, or clear a slot.")
		return
	}
	s.MealType = mt
	httpx.WriteJSON(w, http.StatusOK, s)
}
