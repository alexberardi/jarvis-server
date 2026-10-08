package recipes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// The planner (§3.4 rows #36–#41, §4.6): commit a plan, the current plan, list/get/delete,
// and moving meals between slots. Plans are household data (visible); staged recipes are
// author-only.

// HouseholdClock names a household's IANA zone ("" = unknown). The cc module implements it
// (the zone its most recently seen node reported).
type HouseholdClock interface {
	HouseholdTimezone(ctx context.Context, householdID string) string
}

// today is "today" for the caller's household: its clock's zone, else the host's (legacy used
// the container's date.today(), B28).
func (m *Module) today(ctx context.Context, c caller) string {
	loc := time.Local
	if m.Clock != nil && c.writeHousehold != "" {
		if tz := m.Clock.HouseholdTimezone(ctx, c.writeHousehold); tz != "" {
			if l, err := time.LoadLocation(tz); err == nil {
				loc = l
			}
		}
	}
	return m.now().In(loc).Format("2006-01-02")
}

type mealPlanItemRead struct {
	ID        int64   `json:"id"`
	Date      string  `json:"date"`
	MealType  string  `json:"meal_type"`
	RecipeID  int64   `json:"recipe_id"`
	Title     *string `json:"title"`
	ImageURL  *string `json:"image_url"`
	TotalTime *int64  `json:"total_time_minutes"`
}

type mealPlanRead struct {
	ID        int64              `json:"id"`
	UserID    string             `json:"user_id"`
	Name      *string            `json:"name"`
	StartDate string             `json:"start_date"`
	Items     []mealPlanItemRead `json:"items"`
}

var errPlanNotFound = &httpError{http.StatusNotFound, "Meal plan not found"}

// loadPlan reads one visible plan with its items (id order) and their recipes' display fields.
func loadPlan(ctx context.Context, q queryer, c caller, id int64) (mealPlanRead, error) {
	pred, args := c.visible("p")
	var p mealPlanRead
	var name sql.NullString
	err := q.QueryRowContext(ctx, `SELECT p.id, p.user_id, p.name, p.start_date FROM recipes_meal_plans p
		WHERE p.id = ? AND `+pred, append([]any{id}, args...)...).Scan(&p.ID, &p.UserID, &name, &p.StartDate)
	if errors.Is(err, sql.ErrNoRows) {
		return p, errPlanNotFound
	}
	if err != nil {
		return p, err
	}
	p.Name = nullStr(name)
	rows, err := q.QueryContext(ctx, `SELECT i.id, i.date, i.meal_type, i.recipe_id, r.title, r.image_url,
		r.total_time_minutes FROM recipes_meal_plan_items i LEFT JOIN recipes_recipes r ON r.id = i.recipe_id
		WHERE i.meal_plan_id = ? ORDER BY i.id`, id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	p.Items = []mealPlanItemRead{}
	for rows.Next() {
		var it mealPlanItemRead
		var title, img sql.NullString
		var total sql.NullInt64
		if err := rows.Scan(&it.ID, &it.Date, &it.MealType, &it.RecipeID, &title, &img, &total); err != nil {
			return p, err
		}
		it.Title, it.ImageURL, it.TotalTime = nullStr(title), nullStr(img), nullInt(total)
		p.Items = append(p.Items, it)
	}
	return p, rows.Err()
}

func (m *Module) writePlan(w http.ResponseWriter, ctx context.Context, c caller, id int64) {
	p, err := loadPlan(ctx, m.deps.DB.Read, c, id)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

type planItemIn struct {
	date, mealType string
	recipeID       int64
	source         string
}

// handleCommitPlan is POST /planner/commit (#36): 200, not 201. A source:"stage" item
// materialises the caller's staged recipe into the box (once per stage id); a source:"user"
// item must be a recipe the caller can see (CHANGE #36, B14: legacy accepted any id).
func (m *Module) handleCommitPlan(w http.ResponseWriter, r *http.Request, c caller) {
	o, ok := readBody(w, r)
	if !ok {
		return
	}
	name := o.optStr("name")
	start := o.date("start_date")
	var items []planItemIn
	if l, ok := o.list("items", true, false); ok {
		for i, e := range l {
			it, ok := o.child(e, "Input should be a valid dictionary or instance of MealPlanItemCreate", "items", i)
			if !ok {
				continue
			}
			items = append(items, planItemIn{
				date: it.date("date"), mealType: it.str("meal_type"), recipeID: it.reqInt("recipe_id"),
				source: it.literal("source", "user", "user", "stage"),
			})
		}
	}
	if !o.done(w) {
		return
	}
	ctx := r.Context()
	var id int64
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := ensureUser(ctx, tx, c.uid()); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO recipes_meal_plans (user_id, household_id, name, start_date, created_at)
			VALUES (?, ?, ?, ?, ?)`, c.uid(), c.hh(), name, start, ts(m.now()))
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		materialised := map[int64]int64{}
		pred, args := c.visible("r")
		for _, it := range items {
			recipeID := it.recipeID
			if it.source == "stage" {
				rid, done := materialised[it.recipeID]
				if !done {
					if rid, err = m.materialiseStage(ctx, tx, c, it.recipeID); err != nil {
						return err
					}
					materialised[it.recipeID] = rid
				}
				recipeID = rid
			} else {
				var found int64
				err := tx.QueryRowContext(ctx, `SELECT r.id FROM recipes_recipes r WHERE r.id = ? AND `+pred,
					append([]any{recipeID}, args...)...).Scan(&found)
				if errors.Is(err, sql.ErrNoRows) {
					return errRecipeNotFound
				}
				if err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO recipes_meal_plan_items (meal_plan_id, recipe_id, date, meal_type)
				VALUES (?, ?, ?, ?)`, id, recipeID, it.date, it.mealType); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	m.writePlan(w, ctx, c, id)
}

// materialiseStage copies the caller's staged recipe into the box as a manual recipe
// (planner_service._materialise_stage_recipe). The stage row is kept.
func (m *Module) materialiseStage(ctx context.Context, tx *sql.Tx, c caller, stageID int64) (int64, error) {
	pred, args := c.authorOnly("")
	var title, ingredients, steps string
	var desc sql.NullString
	var prep, cook sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT title, description, prep_time_minutes, cook_time_minutes, ingredients, steps
		FROM recipes_stage_recipes WHERE id = ? AND `+pred, append([]any{stageID}, args...)...).
		Scan(&title, &desc, &prep, &cook, &ingredients, &steps)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, &httpError{http.StatusNotFound, fmt.Sprintf("Staged recipe %d not found", stageID)}
	}
	if err != nil {
		return 0, err
	}
	var total any
	if t := prep.Int64 + cook.Int64; t != 0 {
		total = t
	}
	now := ts(m.now())
	res, err := tx.ExecContext(ctx, `INSERT INTO recipes_recipes (user_id, household_id, title, description, source_type,
		total_time_minutes, created_at, updated_at) VALUES (?, ?, ?, ?, 'manual', ?, ?, ?)`,
		c.uid(), c.hh(), title, nullStr(desc), total, now, now)
	if err != nil {
		return 0, err
	}
	rid, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	var ings []any
	_ = json.Unmarshal([]byte(ingredients), &ings)
	var in []ingredientIn
	for _, e := range ings {
		var ing ingredientIn
		switch x := e.(type) {
		case map[string]any:
			ing.text, _ = x["text"].(string)
			if s, ok := x["quantity_display"].(string); ok {
				ing.quantityDisplay = &s
			}
			if s, ok := x["unit"].(string); ok {
				ing.unit = &s
			}
		default:
			ing.text = pyStr(e)
		}
		if ing.text != "" {
			in = append(in, ing)
		}
	}
	if err := replaceIngredients(ctx, tx, rid, in); err != nil {
		return 0, err
	}
	var stps []any
	_ = json.Unmarshal([]byte(steps), &stps)
	var st []stepIn
	for _, e := range stps {
		var text string
		if mm, ok := e.(map[string]any); ok {
			text, _ = mm["text"].(string)
		} else {
			text = pyStr(e)
		}
		if text != "" {
			st = append(st, stepIn{number: int64(len(st) + 1), text: text})
		}
	}
	return rid, replaceSteps(ctx, tx, rid, st)
}

// handleCurrentPlan is GET /planner/current (#37): among visible plans with a meal today or
// later, the one whose first such meal is soonest (newest first on a tie); {} when none.
func (m *Module) handleCurrentPlan(w http.ResponseWriter, r *http.Request, c caller) {
	ctx := r.Context()
	pred, args := c.visible("p")
	var id int64
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT p.id FROM recipes_meal_plans p
		JOIN recipes_meal_plan_items i ON i.meal_plan_id = p.id
		WHERE i.date >= ? AND `+pred+` GROUP BY p.id ORDER BY MIN(i.date), p.created_at DESC, p.id DESC LIMIT 1`,
		append([]any{m.today(ctx, c)}, args...)...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{})
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	m.writePlan(w, ctx, c, id)
}

// handleListPlans is GET /planner/plans (#38): summaries, newest first; the span is the items'
// (the stored start_date when a plan has none).
func (m *Module) handleListPlans(w http.ResponseWriter, r *http.Request, c caller) {
	pred, args := c.visible("p")
	rows, err := m.deps.DB.Read.QueryContext(r.Context(), `SELECT p.id, p.name, p.start_date, p.created_at,
		MIN(i.date), MAX(i.date), COUNT(i.id) FROM recipes_meal_plans p
		LEFT JOIN recipes_meal_plan_items i ON i.meal_plan_id = p.id
		WHERE `+pred+` GROUP BY p.id ORDER BY p.created_at DESC, p.id DESC`, args...)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, count int64
		var name, first, last, created sql.NullString
		var start string
		if err := rows.Scan(&id, &name, &start, &created, &first, &last, &count); err != nil {
			m.internalError(w, err)
			return
		}
		s, e := start, start
		if first.Valid {
			s = first.String
		}
		if last.Valid {
			e = last.String
		}
		out = append(out, map[string]any{"id": id, "name": nullStr(name), "start_date": s, "end_date": e,
			"meal_count": count, "created_at": pyNaive(created.String)})
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleGetPlan is GET /planner/plans/{plan_id} (#39).
func (m *Module) handleGetPlan(w http.ResponseWriter, r *http.Request, c caller) {
	id, ok := pathInt(w, r, "plan_id")
	if !ok {
		return
	}
	m.writePlan(w, r.Context(), c, id)
}

// handleDeletePlan is DELETE /planner/plans/{plan_id} (#40): items cascade, recipes stay.
func (m *Module) handleDeletePlan(w http.ResponseWriter, r *http.Request, c caller) {
	id, ok := pathInt(w, r, "plan_id")
	if !ok {
		return
	}
	pred, args := c.visible("p")
	res, err := m.deps.DB.Write.ExecContext(r.Context(), `DELETE FROM recipes_meal_plans AS p WHERE p.id = ? AND `+pred,
		append([]any{id}, args...)...)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		httpx.Error(w, http.StatusNotFound, "Meal plan not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type slotKey struct{ date, meal string }

type planMove struct {
	itemID int64
	to     slotKey
}

// handleMoveItems is PATCH /planner/plans/{plan_id}/items (#41). Moving a meal onto a slot
// held by a meal that is not moving swaps the occupant into the vacated slot; two moves onto
// one slot are a 409; with a repeated item_id the last move wins (in the first one's turn).
// start_date becomes the earliest item's date.
func (m *Module) handleMoveItems(w http.ResponseWriter, r *http.Request, c caller) {
	id, ok := pathInt(w, r, "plan_id")
	if !ok {
		return
	}
	o, ok := readBody(w, r)
	if !ok {
		return
	}
	var moves []planMove
	if l, ok := o.list("moves", true, false); ok {
		for i, e := range l {
			mv, ok := o.child(e, "Input should be a valid dictionary or instance of MealPlanItemMove", "moves", i)
			if !ok {
				continue
			}
			moves = append(moves, planMove{itemID: mv.reqInt("item_id"), to: slotKey{mv.date("date"), mv.str("meal_type")}})
		}
	}
	if !o.done(w) {
		return
	}
	ctx := r.Context()
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		p, err := loadPlan(ctx, tx, c, id)
		if err != nil {
			return err
		}
		items := p.Items
		at := map[int64]int{}
		for i, it := range items {
			at[it.ID] = i
		}
		var order []int64
		requested := map[int64]slotKey{}
		for _, mv := range moves {
			if _, ok := at[mv.itemID]; !ok {
				return &httpError{http.StatusNotFound, fmt.Sprintf("Item %d is not part of this plan", mv.itemID)}
			}
			if _, seen := requested[mv.itemID]; !seen {
				order = append(order, mv.itemID)
			}
			requested[mv.itemID] = mv.to
		}
		if len(order) == 0 {
			return nil
		}
		targets := map[slotKey]bool{}
		for _, t := range requested {
			if targets[t] {
				return &httpError{http.StatusConflict, "Two meals cannot be moved to the same day and meal type"}
			}
			targets[t] = true
		}
		vacated := map[int64]slotKey{}
		for _, iid := range order {
			it := items[at[iid]]
			vacated[iid] = slotKey{it.Date, it.MealType}
		}
		changed := map[int64]bool{}
		for _, iid := range order {
			to := requested[iid]
			for i := range items {
				occ := &items[i]
				if _, moving := requested[occ.ID]; moving || occ.Date != to.date || occ.MealType != to.meal {
					continue
				}
				occ.Date, occ.MealType = vacated[iid].date, vacated[iid].meal
				changed[occ.ID] = true
				break
			}
			it := &items[at[iid]]
			it.Date, it.MealType = to.date, to.meal
			changed[iid] = true
		}
		for _, it := range items {
			if !changed[it.ID] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE recipes_meal_plan_items SET date = ?, meal_type = ? WHERE id = ?`,
				it.Date, it.MealType, it.ID); err != nil {
				return err
			}
		}
		start := items[0].Date
		for _, it := range items {
			if it.Date < start {
				start = it.Date
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE recipes_meal_plans SET start_date = ? WHERE id = ?`, start, id)
		return err
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	m.writePlan(w, ctx, c, id)
}

// pyStr is Python's str() for a JSON scalar inside a staged recipe's lists.
func pyStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	}
	return fmt.Sprint(v)
}
