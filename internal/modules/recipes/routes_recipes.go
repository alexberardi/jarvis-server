package recipes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/recipes/quantity"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Recipe CRUD (§3.1 rows #1–#8, §4.2) and tags (§3.3 #23, #24).

// --- wire shapes ---

type ingredientRead struct {
	ID              int64   `json:"id"`
	Text            string  `json:"text"`
	QuantityDisplay *string `json:"quantity_display"`
	// QuantityValue is the Numeric(10,4) decimal string ("1.5000"), re-parsed by the server
	// from quantity_display; the client's value is ignored.
	QuantityValue *string `json:"quantity_value"`
	Unit          *string `json:"unit"`
}

type stepRead struct {
	ID         int64  `json:"id"`
	StepNumber int64  `json:"step_number"`
	Text       string `json:"text"`
}

type tagRead struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// recipeRead is RecipeRead. prep/cook are real columns now (RD5); legacy always sent null.
type recipeRead struct {
	ID          int64            `json:"id"`
	UserID      string           `json:"user_id"`
	Title       string           `json:"title"`
	Description *string          `json:"description"`
	Servings    *int64           `json:"servings"`
	Prep        *int64           `json:"prep_time_minutes"`
	Cook        *int64           `json:"cook_time_minutes"`
	Total       *int64           `json:"total_time_minutes"`
	SourceType  string           `json:"source_type"`
	SourceURL   *string          `json:"source_url"`
	ImageURL    *string          `json:"image_url"`
	CreatedAt   *string          `json:"created_at"`
	UpdatedAt   *string          `json:"updated_at"`
	Ingredients []ingredientRead `json:"ingredients"`
	Steps       []stepRead       `json:"steps"`
	Tags        []tagRead        `json:"tags"`

	householdID sql.NullString
}

// httpError is a handler failure with a legacy {"detail": ...} body, raised inside a
// transaction so it rolls back.
type httpError struct {
	status int
	detail string
}

func (e *httpError) Error() string { return e.detail }

func (m *Module) writeErr(w http.ResponseWriter, err error) {
	var he *httpError
	if errors.As(err, &he) {
		httpx.Error(w, he.status, he.detail)
		return
	}
	m.internalError(w, err)
}

var errRecipeNotFound = &httpError{http.StatusNotFound, "Recipe not found"}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func nullStr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	return &ns.String
}

func nullInt(ni sql.NullInt64) *int64 {
	if !ni.Valid {
		return nil
	}
	return &ni.Int64
}

const recipeCols = `r.id, r.user_id, r.household_id, r.title, r.description, r.servings, r.prep_time_minutes,
	r.cook_time_minutes, r.total_time_minutes, r.source_type, r.source_url, r.image_url, r.created_at, r.updated_at`

// loadRecipes runs a recipes query (selecting recipeCols from recipes_recipes r) and fills in
// ingredients, steps and tags with one query each.
func loadRecipes(ctx context.Context, q queryer, where string, args ...any) ([]recipeRead, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+recipeCols+` FROM recipes_recipes r `+where, args...)
	if err != nil {
		return nil, err
	}
	out := []recipeRead{}
	idx := map[int64]int{}
	for rows.Next() {
		var rr recipeRead
		var desc, srcURL, img, created, updated sql.NullString
		var servings, prep, cook, total sql.NullInt64
		if err := rows.Scan(&rr.ID, &rr.UserID, &rr.householdID, &rr.Title, &desc, &servings, &prep, &cook,
			&total, &rr.SourceType, &srcURL, &img, &created, &updated); err != nil {
			rows.Close()
			return nil, err
		}
		rr.Description, rr.SourceURL, rr.ImageURL = nullStr(desc), nullStr(srcURL), nullStr(img)
		rr.Servings, rr.Prep, rr.Cook, rr.Total = nullInt(servings), nullInt(prep), nullInt(cook), nullInt(total)
		if created.Valid {
			s := pyNaive(created.String)
			rr.CreatedAt = &s
		}
		if updated.Valid {
			s := pyNaive(updated.String)
			rr.UpdatedAt = &s
		}
		rr.Ingredients, rr.Steps, rr.Tags = []ingredientRead{}, []stepRead{}, []tagRead{}
		idx[rr.ID] = len(out)
		out = append(out, rr)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	ids := make([]any, 0, len(out))
	for _, r := range out {
		ids = append(ids, r.ID)
	}
	in := "(?" + strings.Repeat(",?", len(ids)-1) + ")"

	// Ingredients in insertion order.
	rows, err = q.QueryContext(ctx, `SELECT recipe_id, id, text, quantity_display, quantity_value, unit
		FROM recipes_ingredients WHERE recipe_id IN `+in+` ORDER BY id`, ids...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var rid int64
		var ing ingredientRead
		var qd, unit sql.NullString
		var qv sql.NullFloat64
		if err := rows.Scan(&rid, &ing.ID, &ing.Text, &qd, &qv, &unit); err != nil {
			rows.Close()
			return nil, err
		}
		ing.QuantityDisplay, ing.Unit = nullStr(qd), nullStr(unit)
		if qv.Valid {
			s := quantity.Wire(qv.Float64)
			ing.QuantityValue = &s
		}
		out[idx[rid]].Ingredients = append(out[idx[rid]].Ingredients, ing)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Steps by step_number.
	rows, err = q.QueryContext(ctx, `SELECT recipe_id, id, step_number, text FROM recipes_steps
		WHERE recipe_id IN `+in+` ORDER BY recipe_id, step_number`, ids...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var rid int64
		var s stepRead
		if err := rows.Scan(&rid, &s.ID, &s.StepNumber, &s.Text); err != nil {
			rows.Close()
			return nil, err
		}
		out[idx[rid]].Steps = append(out[idx[rid]].Steps, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Tags in the order they were attached.
	rows, err = q.QueryContext(ctx, `SELECT rt.recipe_id, t.id, t.name FROM recipes_recipe_tags rt
		JOIN recipes_tags t ON t.id = rt.tag_id WHERE rt.recipe_id IN `+in+` ORDER BY rt.rowid`, ids...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var rid int64
		var t tagRead
		if err := rows.Scan(&rid, &t.ID, &t.Name); err != nil {
			rows.Close()
			return nil, err
		}
		out[idx[rid]].Tags = append(out[idx[rid]].Tags, t)
	}
	rows.Close()
	return out, rows.Err()
}

// visibleRecipe loads one recipe the caller may see, or errRecipeNotFound.
func (m *Module) visibleRecipe(ctx context.Context, q queryer, c caller, id int64) (recipeRead, error) {
	pred, args := c.visible("r")
	rs, err := loadRecipes(ctx, q, `WHERE r.id = ? AND `+pred, append([]any{id}, args...)...)
	if err != nil {
		return recipeRead{}, err
	}
	if len(rs) == 0 {
		return recipeRead{}, errRecipeNotFound
	}
	return rs[0], nil
}

// --- request bodies ---

type ingredientIn struct {
	text            string
	quantityDisplay *string
	unit            *string
}

type stepIn struct {
	number int64
	text   string
}

// recipeIn is RecipeCreate / RecipeUpdate after validation. For an update, nil means "leave as
// is" (legacy could not null a field), and the slices are nil when absent.
type recipeIn struct {
	title, description, sourceType, sourceURL, imageURL, parseJobID *string
	servings, prep, cook, total                                     *int64
	ingredients                                                     []ingredientIn
	steps                                                           []stepIn
	tags                                                            []string
	hasIngredients, hasSteps, hasTags                               bool
}

// parseRecipe validates a RecipeCreate (create) or RecipeUpdate body.
func parseRecipe(o *obj, create bool) recipeIn {
	var in recipeIn
	if create {
		t := o.str("title")
		in.title = &t
	} else {
		in.title = o.optStr("title")
	}
	in.description = o.optStr("description")
	in.servings = o.optInt("servings")
	in.prep = o.optInt("prep_time_minutes")
	in.cook = o.optInt("cook_time_minutes")
	in.total = o.optInt("total_time_minutes")
	switch st := o.m["source_type"].(type) {
	case nil:
		if create {
			if _, present := o.m["source_type"]; present {
				o.fail(msgSourceType, "source_type")
			}
			manual := "manual"
			in.sourceType = &manual
		}
	case string:
		if st != "manual" && st != "image" && st != "url" {
			o.fail(msgSourceType, "source_type")
		}
		in.sourceType = &st
	default:
		o.fail(msgSourceType, "source_type")
	}
	in.sourceURL = o.optStr("source_url")
	in.imageURL = o.optStr("image_url")

	if l, ok := o.list("ingredients", create, !create); ok {
		in.hasIngredients = true
		in.ingredients = []ingredientIn{}
		for i, e := range l {
			c, ok := o.child(e, "Input should be a valid dictionary or instance of IngredientCreate", "ingredients", i)
			if !ok {
				continue
			}
			in.ingredients = append(in.ingredients, ingredientIn{
				text: c.str("text"), quantityDisplay: c.optStr("quantity_display"), unit: c.optStr("unit"),
			})
			c.optDecimal("quantity_value")
		}
		if create && len(l) == 0 {
			o.fail("Value error, At least one ingredient is required", "ingredients")
		}
	}
	if l, ok := o.list("steps", create, !create); ok {
		in.hasSteps = true
		in.steps = []stepIn{}
		for i, e := range l {
			c, ok := o.child(e, "Input should be a valid dictionary or instance of StepCreate", "steps", i)
			if !ok {
				continue
			}
			in.steps = append(in.steps, stepIn{number: c.reqInt("step_number"), text: c.str("text")})
		}
		if create && len(l) == 0 {
			o.fail("Value error, At least one step is required", "steps")
		}
		seen := map[int64]bool{}
		for _, s := range in.steps {
			if seen[s.number] {
				// Legacy hit UNIQUE(recipe_id, step_number) and answered 500.
				o.fail("Value error, Step numbers must be unique", "steps")
				break
			}
			seen[s.number] = true
		}
	}
	if tags, ok := o.strList("tags", !create); ok {
		in.hasTags = true
		in.tags = tags
	}
	if create {
		in.parseJobID = o.optStr("parse_job_id")
	}
	return in
}

// foldTotal is legacy's prep/cook → total rule: when total is absent and prep or cook is
// given, total = prep + cook (each defaulting to 0).
func (in recipeIn) foldTotal() *int64 {
	if in.total != nil || (in.prep == nil && in.cook == nil) {
		return in.total
	}
	var t int64
	if in.prep != nil {
		t += *in.prep
	}
	if in.cook != nil {
		t += *in.cook
	}
	return &t
}

// --- writes ---

// ensureUser inserts the author's shadow row (legacy user_service.ensure_user).
func ensureUser(ctx context.Context, tx *sql.Tx, uid string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO recipes_users (user_id) VALUES (?) ON CONFLICT DO NOTHING`, uid)
	return err
}

// getOrCreateTag is the global, case-insensitive tag get-or-create. Unlike legacy (B9) the row
// is committed with the caller's transaction.
func getOrCreateTag(ctx context.Context, tx *sql.Tx, name string) (tagRead, error) {
	n := quantity.PyStrip(name)
	if n == "" {
		return tagRead{}, &httpError{http.StatusBadRequest, "Tag name required"}
	}
	t := tagRead{}
	err := tx.QueryRowContext(ctx, `SELECT id, name FROM recipes_tags WHERE name = ? COLLATE NOCASE`, n).Scan(&t.ID, &t.Name)
	if err == nil {
		return t, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return t, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO recipes_tags (name) VALUES (?)`, n)
	if err != nil {
		return t, err
	}
	t.ID, err = res.LastInsertId()
	t.Name = n
	return t, err
}

func replaceIngredients(ctx context.Context, tx *sql.Tx, recipeID int64, ings []ingredientIn) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM recipes_ingredients WHERE recipe_id = ?`, recipeID); err != nil {
		return err
	}
	for _, ing := range ings {
		var qv any
		if ing.quantityDisplay != nil {
			if v, ok := quantity.ParseDisplay(*ing.quantityDisplay); ok {
				qv = v
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO recipes_ingredients (recipe_id, text, quantity_display, quantity_value, unit)
			VALUES (?, ?, ?, ?, ?)`, recipeID, ing.text, ing.quantityDisplay, qv, ing.unit); err != nil {
			return err
		}
	}
	return nil
}

// replaceSteps deletes then inserts, because of UNIQUE(recipe_id, step_number).
func replaceSteps(ctx context.Context, tx *sql.Tx, recipeID int64, steps []stepIn) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM recipes_steps WHERE recipe_id = ?`, recipeID); err != nil {
		return err
	}
	for _, s := range steps {
		if _, err := tx.ExecContext(ctx, `INSERT INTO recipes_steps (recipe_id, step_number, text) VALUES (?, ?, ?)`,
			recipeID, s.number, s.text); err != nil {
			return err
		}
	}
	return nil
}

func replaceTags(ctx context.Context, tx *sql.Tx, recipeID int64, names []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM recipes_recipe_tags WHERE recipe_id = ?`, recipeID); err != nil {
		return err
	}
	for _, n := range names {
		t, err := getOrCreateTag(ctx, tx, n)
		if err != nil {
			return err
		}
		// A name repeated in the list attaches once (legacy hit the primary key: 500).
		if _, err := tx.ExecContext(ctx, `INSERT INTO recipes_recipe_tags (recipe_id, tag_id) VALUES (?, ?)
			ON CONFLICT DO NOTHING`, recipeID, t.ID); err != nil {
			return err
		}
	}
	return nil
}

// --- handlers ---

// handleListRecipes is GET /recipes (#1): every visible recipe, newest first.
func (m *Module) handleListRecipes(w http.ResponseWriter, r *http.Request, c caller) {
	pred, args := c.visible("r")
	rs, err := loadRecipes(r.Context(), m.deps.DB.Read, `WHERE `+pred+` ORDER BY r.created_at DESC, r.id DESC`, args...)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rs)
}

// handleCreateRecipe is POST /recipes (#2). With parse_job_id the job must be the caller's and
// COMPLETE; it is marked COMMITTED in the same transaction.
func (m *Module) handleCreateRecipe(w http.ResponseWriter, r *http.Request, c caller) {
	o, ok := readBody(w, r)
	if !ok {
		return
	}
	in := parseRecipe(o, true)
	if !o.done(w) {
		return
	}
	ctx := r.Context()
	now := ts(m.now())
	var id int64
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if in.parseJobID != nil && *in.parseJobID != "" {
			pred, args := c.authorOnly("")
			var status string
			err := tx.QueryRowContext(ctx, `SELECT status FROM recipes_recipe_parse_jobs WHERE id = ? AND `+pred,
				append([]any{*in.parseJobID}, args...)...).Scan(&status)
			if errors.Is(err, sql.ErrNoRows) {
				return &httpError{http.StatusNotFound, "Parse job not found"}
			}
			if err != nil {
				return err
			}
			if status != "COMPLETE" {
				return &httpError{http.StatusConflict, "Parse job not ready"}
			}
		}
		if err := ensureUser(ctx, tx, c.uid()); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO recipes_recipes (user_id, household_id, title, description,
			image_url, source_type, source_url, servings, prep_time_minutes, cook_time_minutes, total_time_minutes,
			created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.uid(), c.hh(), *in.title, in.description, in.imageURL, *in.sourceType, in.sourceURL, in.servings,
			in.prep, in.cook, in.foldTotal(), now, now)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		if err := replaceIngredients(ctx, tx, id, in.ingredients); err != nil {
			return err
		}
		if err := replaceSteps(ctx, tx, id, in.steps); err != nil {
			return err
		}
		if err := replaceTags(ctx, tx, id, in.tags); err != nil {
			return err
		}
		if in.parseJobID != nil && *in.parseJobID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE recipes_recipe_parse_jobs SET status = 'COMMITTED',
				committed_at = ?, updated_at = ? WHERE id = ?`, now, now, *in.parseJobID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	rec, err := m.visibleRecipe(ctx, m.deps.DB.Read, c, id)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, rec)
}

// handleGetRecipe is GET /recipes/{recipe_id} (#3).
func (m *Module) handleGetRecipe(w http.ResponseWriter, r *http.Request, c caller) {
	id, ok := pathInt(w, r, "recipe_id")
	if !ok {
		return
	}
	rec, err := m.visibleRecipe(r.Context(), m.deps.DB.Read, c, id)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rec)
}

// handleGetOwnRecipe is GET /recipes/user/{recipe_id} (#6): visible and authored by the caller.
func (m *Module) handleGetOwnRecipe(w http.ResponseWriter, r *http.Request, c caller) {
	id, ok := pathInt(w, r, "recipe_id")
	if !ok {
		return
	}
	rec, err := m.visibleRecipe(r.Context(), m.deps.DB.Read, c, id)
	if err == nil && rec.UserID != c.uid() {
		err = errRecipeNotFound
	}
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rec)
}

// handleUpdateRecipe is PATCH /recipes/{recipe_id} (#4): null leaves a field alone;
// ingredients, steps and tags replace wholesale when present; prep/cook fold into total only
// when total is absent.
func (m *Module) handleUpdateRecipe(w http.ResponseWriter, r *http.Request, c caller) {
	id, ok := pathInt(w, r, "recipe_id")
	if !ok {
		return
	}
	o, ok := readBody(w, r)
	if !ok {
		return
	}
	in := parseRecipe(o, false)
	if !o.done(w) {
		return
	}
	ctx := r.Context()
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		pred, args := c.visible("r")
		var found int64
		err := tx.QueryRowContext(ctx, `SELECT r.id FROM recipes_recipes r WHERE r.id = ? AND `+pred,
			append([]any{id}, args...)...).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return errRecipeNotFound
		}
		if err != nil {
			return err
		}
		sets, vals := []string{"updated_at = ?"}, []any{ts(m.now())}
		set := func(col string, v any) {
			sets = append(sets, col+" = ?")
			vals = append(vals, v)
		}
		if in.title != nil {
			set("title", *in.title)
		}
		if in.description != nil {
			set("description", *in.description)
		}
		if in.servings != nil {
			set("servings", *in.servings)
		}
		if in.sourceType != nil {
			set("source_type", *in.sourceType)
		}
		if in.sourceURL != nil {
			set("source_url", *in.sourceURL)
		}
		if in.imageURL != nil {
			set("image_url", *in.imageURL)
		}
		if in.prep != nil {
			set("prep_time_minutes", *in.prep)
		}
		if in.cook != nil {
			set("cook_time_minutes", *in.cook)
		}
		if t := in.foldTotal(); t != nil {
			set("total_time_minutes", *t)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE recipes_recipes SET `+strings.Join(sets, ", ")+` WHERE id = ?`,
			append(vals, id)...); err != nil {
			return err
		}
		if in.hasIngredients {
			if err := replaceIngredients(ctx, tx, id, in.ingredients); err != nil {
				return err
			}
		}
		if in.hasSteps {
			if err := replaceSteps(ctx, tx, id, in.steps); err != nil {
				return err
			}
		}
		if in.hasTags {
			if err := replaceTags(ctx, tx, id, in.tags); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	rec, err := m.visibleRecipe(ctx, m.deps.DB.Read, c, id)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rec)
}

// handleDeleteRecipe is DELETE /recipes/{recipe_id} (#5). Plan items using the recipe go with it
// (legacy answered 500 for a recipe used by a plan).
func (m *Module) handleDeleteRecipe(w http.ResponseWriter, r *http.Request, c caller) {
	id, ok := pathInt(w, r, "recipe_id")
	if !ok {
		return
	}
	ctx := r.Context()
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		pred, args := c.visible("r")
		res, err := tx.ExecContext(ctx, `DELETE FROM recipes_recipes AS r WHERE r.id = ? AND `+pred,
			append([]any{id}, args...)...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errRecipeNotFound
		}
		return nil
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetStageRecipe is GET /recipes/stage/{stage_id} (#7): an LLM-plan staged recipe, author
// only; 410 once expired. The JSON columns are returned as stored.
func (m *Module) handleGetStageRecipe(w http.ResponseWriter, r *http.Request, c caller) {
	id, ok := pathInt(w, r, "stage_id")
	if !ok {
		return
	}
	pred, args := c.authorOnly("")
	var title, ingredients, steps, tags, notes, expires string
	var desc, yield sql.NullString
	var prep, cook sql.NullInt64
	err := m.deps.DB.Read.QueryRowContext(r.Context(), `SELECT title, description, yield_text, prep_time_minutes,
		cook_time_minutes, ingredients, steps, tags, notes, expires_at FROM recipes_stage_recipes WHERE id = ? AND `+pred,
		append([]any{id}, args...)...).Scan(&title, &desc, &yield, &prep, &cook, &ingredients, &steps, &tags, &notes, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "Not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if !parseTS(expires).After(m.now()) {
		httpx.Error(w, http.StatusGone, "Stage recipe expired")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id": jsonString(id), "title": title, "description": nullStr(desc), "yield": nullStr(yield),
		"prep_time_minutes": nullInt(prep), "cook_time_minutes": nullInt(cook),
		"ingredients": json.RawMessage(ingredients), "steps": json.RawMessage(steps),
		"tags": json.RawMessage(tags), "notes": json.RawMessage(notes),
	})
}

// jsonString renders a stage id as a string (it shares the client's recipe_id space, §8 item 9).
func jsonString(id int64) string { return strconv.FormatInt(id, 10) }

// handleCoreRecipe is GET /recipes/core/{recipe_id} (#8): no auth, always 404 (a stub the
// app still calls; RD2 keeps it on the wire).
func handleCoreRecipe(w http.ResponseWriter, _ *http.Request) {
	httpx.Error(w, http.StatusNotFound, "Core recipe not found")
}

// handleListTags is GET /tags (#23): tags on at least one visible recipe, by name.
func (m *Module) handleListTags(w http.ResponseWriter, r *http.Request, c caller) {
	pred, args := c.visible("r")
	rows, err := m.deps.DB.Read.QueryContext(r.Context(), `SELECT DISTINCT t.id, t.name FROM recipes_tags t
		JOIN recipes_recipe_tags rt ON rt.tag_id = t.id JOIN recipes_recipes r ON r.id = rt.recipe_id
		WHERE `+pred+` ORDER BY t.name COLLATE NOCASE, t.name`, args...)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	out := []tagRead{}
	for rows.Next() {
		var t tagRead
		if err := rows.Scan(&t.ID, &t.Name); err != nil {
			m.internalError(w, err)
			return
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleCreateTag is POST /tags (#24, CHANGE): global, case-insensitive get-or-create, now
// committed (B9: legacy only flushed, so the id was phantom).
func (m *Module) handleCreateTag(w http.ResponseWriter, r *http.Request, _ caller) {
	o, ok := readBody(w, r)
	if !ok {
		return
	}
	name := o.str("name")
	if !o.done(w) {
		return
	}
	var t tagRead
	err := m.deps.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var err error
		t, err = getOrCreateTag(r.Context(), tx, name)
		return err
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, t)
}
