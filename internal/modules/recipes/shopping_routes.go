package recipes

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/recipes/shopping"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// The shopping list and staples (§3.5 rows #42–#45, §4.7). The list is a view computed per
// request over the visible plans' meals in a date range (RD7: every plan the caller can see,
// whatever household it is in); staples are flagged on it, never hidden.

// shoppingLines loads the ingredient lines of the visible plans' meals between start and end
// (inclusive), in plan, meal and ingredient order, and the number of plans with a meal there.
// Three queries (legacy lazy-loaded N+1).
func shoppingLines(ctx context.Context, q queryer, c caller, start, end string) ([]shopping.Line, int, error) {
	pred, args := c.visible("p")
	rows, err := q.QueryContext(ctx, `SELECT i.meal_plan_id, i.recipe_id FROM recipes_meal_plan_items i
		JOIN recipes_meal_plans p ON p.id = i.meal_plan_id
		WHERE i.date >= ? AND i.date <= ? AND `+pred+` ORDER BY p.id, i.id`, append([]any{start, end}, args...)...)
	if err != nil {
		return nil, 0, err
	}
	var meals []int64
	plans := map[int64]bool{}
	recipes := map[int64]bool{}
	for rows.Next() {
		var pid, rid int64
		if err := rows.Scan(&pid, &rid); err != nil {
			rows.Close()
			return nil, 0, err
		}
		plans[pid] = true
		recipes[rid] = true
		meals = append(meals, rid)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(meals) == 0 {
		return []shopping.Line{}, len(plans), err
	}
	ids := make([]any, 0, len(recipes))
	for id := range recipes {
		ids = append(ids, id)
	}
	in := "(?" + strings.Repeat(",?", len(ids)-1) + ")"

	titles := map[int64]string{}
	rows, err = q.QueryContext(ctx, `SELECT id, title FROM recipes_recipes WHERE id IN `+in, ids...)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var id int64
		var t string
		if err := rows.Scan(&id, &t); err != nil {
			rows.Close()
			return nil, 0, err
		}
		titles[id] = t
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	ings := map[int64][]shopping.Line{}
	rows, err = q.QueryContext(ctx, `SELECT recipe_id, text, quantity_value, unit FROM recipes_ingredients
		WHERE recipe_id IN `+in+` ORDER BY id`, ids...)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var rid int64
		var l shopping.Line
		var qv sql.NullFloat64
		var unit sql.NullString
		if err := rows.Scan(&rid, &l.Text, &qv, &unit); err != nil {
			rows.Close()
			return nil, 0, err
		}
		if qv.Valid {
			l.Quantity = shopping.StoredQuantity(qv.Float64)
		}
		l.Unit = nullStr(unit)
		ings[rid] = append(ings[rid], l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	var lines []shopping.Line
	for _, rid := range meals {
		title, ok := titles[rid]
		if !ok {
			continue
		}
		for _, l := range ings[rid] {
			l.RecipeTitle = title
			lines = append(lines, l)
		}
	}
	return lines, len(plans), nil
}

// shoppingList builds the list for the caller, staples flagged.
func (m *Module) shoppingList(ctx context.Context, c caller, start, end string) ([]shopping.Item, int, error) {
	q := m.deps.DB.Read
	lines, plans, err := shoppingLines(ctx, q, c, start, end)
	if err != nil {
		return nil, 0, err
	}
	staples, err := listStaples(ctx, q, c)
	if err != nil {
		return nil, 0, err
	}
	names := map[string]bool{}
	for _, s := range staples {
		names[s.Name] = true
	}
	return shopping.Build(lines, names), plans, nil
}

// handleShoppingList is GET /shopping-list (#42).
func (m *Module) handleShoppingList(w http.ResponseWriter, r *http.Request, c caller) {
	qv := newQueryVals(r)
	start, end := qv.date("start_date"), qv.date("end_date")
	if !qv.done(w) {
		return
	}
	items, plans, err := m.shoppingList(r.Context(), c, start, end)
	if err != nil {
		m.internalError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		amounts := make([]map[string]any, 0, len(it.Amounts))
		for _, a := range it.Amounts {
			var q any
			if a.Quantity != nil {
				q, _ = a.Quantity.Float64()
			}
			amounts = append(amounts, map[string]any{"unit": a.Unit, "quantity": q, "unparsed": a.Unparsed})
		}
		out = append(out, map[string]any{"name": it.Name, "amounts": amounts, "recipes": it.Recipes, "is_staple": it.IsStaple})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"start_date": start, "end_date": end, "items": out, "plan_count": plans})
}

type stapleRead struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// listStaples is staples_service.list_staples: the visible staples by name, one per name (the
// oldest row, so the id a client holds stays stable).
func listStaples(ctx context.Context, q queryer, c caller) ([]stapleRead, error) {
	pred, args := c.visible("s")
	rows, err := q.QueryContext(ctx, `SELECT s.id, s.name FROM recipes_staples s WHERE `+pred+` ORDER BY s.name, s.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []stapleRead{}
	for rows.Next() {
		var s stapleRead
		if err := rows.Scan(&s.ID, &s.Name); err != nil {
			return nil, err
		}
		if len(out) > 0 && out[len(out)-1].Name == s.Name {
			continue
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// handleListStaples is GET /staples (#43).
func (m *Module) handleListStaples(w http.ResponseWriter, r *http.Request, c caller) {
	out, err := listStaples(r.Context(), m.deps.DB.Read, c)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleAddStaple is POST /staples (#44): the name is stored as its shopping key; adding one
// the caller can already see returns that row. 201 either way.
func (m *Module) handleAddStaple(w http.ResponseWriter, r *http.Request, c caller) {
	o, ok := readBody(w, r)
	if !ok {
		return
	}
	raw := o.boundedStr("name", 1, 200)
	if !o.done(w) {
		return
	}
	name := shopping.NormalizeName(raw)
	if name == "" {
		httpx.Error(w, http.StatusUnprocessableEntity, "A staple needs a name")
		return
	}
	ctx := r.Context()
	var s stapleRead
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		pred, args := c.visible("s")
		err := tx.QueryRowContext(ctx, `SELECT s.id, s.name FROM recipes_staples s WHERE s.name = ? AND `+pred+`
			ORDER BY s.id LIMIT 1`, append([]any{name}, args...)...).Scan(&s.ID, &s.Name)
		if err == nil || !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := ensureUser(ctx, tx, c.uid()); err != nil {
			return err
		}
		// UNIQUE(user_id, name) is per author: the caller's own row for this name can only be
		// invisible if it sits in a household they have left. It moves to their current one
		// rather than failing the request.
		s.Name = name
		return tx.QueryRowContext(ctx, `INSERT INTO recipes_staples (user_id, household_id, name, created_at)
			VALUES (?, ?, ?, ?) ON CONFLICT (user_id, name) DO UPDATE SET (household_id) = (excluded.household_id)
			RETURNING id`, c.uid(), c.hh(), name, ts(m.now())).Scan(&s.ID)
	})
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, s)
}

// handleDeleteStaple is DELETE /staples/{staple_id} (#45): removes every visible row with that
// staple's name (two members may each have added "salt").
func (m *Module) handleDeleteStaple(w http.ResponseWriter, r *http.Request, c caller) {
	id, ok := pathInt(w, r, "staple_id")
	if !ok {
		return
	}
	ctx := r.Context()
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		pred, args := c.visible("s")
		var name string
		err := tx.QueryRowContext(ctx, `SELECT s.name FROM recipes_staples s WHERE s.id = ? AND `+pred,
			append([]any{id}, args...)...).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return &httpError{http.StatusNotFound, "Staple not found"}
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM recipes_staples AS s WHERE s.name = ? AND `+pred, append([]any{name}, args...)...)
		return err
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
