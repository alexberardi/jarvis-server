package recipes

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/recipes/quantity"
	"github.com/alexberardi/jarvis-server/internal/modules/recipes/shopping"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Grocery: the household's ingredient → SKU map (#46–#48) and the Walmart cart (#49)
// (grocery_service, §4.7). Three tiers, cheapest first: the exact key in the map; a
// background LLM pass that matches unmapped ingredients against what the household already
// mapped (grocery_match.go); and the person, via the app's product picker. The model never
// invents a SKU, and a manual mapping always beats an LLM one.

const retailerWalmart = "walmart"

// groceryMatchJobType is the queue type of the background SKU-matching pass.
const groceryMatchJobType = "recipes.grocery_match"

// maxUnmatchedPerPass bounds the names one match pass carries (and the prompt lists).
const maxUnmatchedPerPass = 25

type skuMapping struct {
	ID             int64   `json:"id"`
	Retailer       string  `json:"retailer"`
	IngredientName string  `json:"ingredient_name"`
	SKU            string  `json:"sku"`
	ProductName    *string `json:"product_name"`
	UnitSize       *string `json:"unit_size"`
	Source         string  `json:"source"`
}

const skuCols = `g.id, g.retailer, g.ingredient_name, g.sku, g.product_name, g.unit_size, g.source`

func scanSKU(sc interface{ Scan(...any) error }) (skuMapping, error) {
	var s skuMapping
	var product, size sql.NullString
	err := sc.Scan(&s.ID, &s.Retailer, &s.IngredientName, &s.SKU, &product, &size, &s.Source)
	s.ProductName, s.UnitSize = nullStr(product), nullStr(size)
	return s, err
}

// listMap is list_map: the visible mappings for a retailer, by ingredient name.
func listMap(ctx context.Context, q queryer, c caller, retailer string) ([]skuMapping, error) {
	pred, args := c.visible("g")
	rows, err := q.QueryContext(ctx, `SELECT `+skuCols+` FROM recipes_grocery_sku_map g WHERE g.retailer = ? AND `+pred+`
		ORDER BY g.ingredient_name, g.id`, append([]any{retailer}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []skuMapping{}
	for rows.Next() {
		s, err := scanSKU(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// upsertMapping is upsert_mapping: name is a shopping key, stored stripped and lowercased. An
// existing visible mapping is updated in place, except that an LLM guess never overwrites a
// manual choice (the existing row is returned unchanged).
func (m *Module) upsertMapping(ctx context.Context, tx *sql.Tx, c caller, name, sku, retailer string,
	product, unitSize *string, source string) (skuMapping, error) {
	name = strings.ToLower(quantity.PyStrip(name))
	pred, args := c.visible("g")
	existing, err := scanSKU(tx.QueryRowContext(ctx, `SELECT `+skuCols+` FROM recipes_grocery_sku_map g
		WHERE g.retailer = ? AND g.ingredient_name = ? AND `+pred+` ORDER BY g.id LIMIT 1`,
		append([]any{retailer, name}, args...)...))
	now := ts(m.now())
	switch {
	case err == nil:
		if source == "llm" && existing.Source == "manual" {
			return existing, nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE recipes_grocery_sku_map SET sku = ?, product_name = ?, unit_size = ?,
			source = ?, updated_at = ? WHERE id = ?`, sku, product, unitSize, source, now, existing.ID); err != nil {
			return existing, err
		}
		existing.SKU, existing.ProductName, existing.UnitSize, existing.Source = sku, product, unitSize, source
		return existing, nil
	case !errors.Is(err, sql.ErrNoRows):
		return existing, err
	}
	if err := ensureUser(ctx, tx, c.uid()); err != nil {
		return skuMapping{}, err
	}
	s := skuMapping{Retailer: retailer, IngredientName: name, SKU: sku, ProductName: product, UnitSize: unitSize, Source: source}
	err = tx.QueryRowContext(ctx, `INSERT INTO recipes_grocery_sku_map (user_id, household_id, retailer, ingredient_name, sku,
		product_name, unit_size, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		c.uid(), c.hh(), retailer, name, sku, product, unitSize, source, now).Scan(&s.ID)
	return s, err
}

// handleListSKUMap is GET /grocery/sku-map (#46).
func (m *Module) handleListSKUMap(w http.ResponseWriter, r *http.Request, c caller) {
	qv := newQueryVals(r)
	retailer := qv.literal("retailer", retailerWalmart, retailerWalmart)
	if !qv.done(w) {
		return
	}
	out, err := listMap(r.Context(), m.deps.DB.Read, c, retailer)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handlePutSKUMap is PUT /grocery/sku-map (#47): always source "manual" (only a person picking
// a product reaches it). raw=true normalises a recipe line into its shopping key first.
func (m *Module) handlePutSKUMap(w http.ResponseWriter, r *http.Request, c caller) {
	o, ok := readBody(w, r)
	if !ok {
		return
	}
	name := o.boundedStr("ingredient_name", 1, 255)
	raw := o.optBool("raw", false)
	sku := o.boundedStr("sku", 1, 64)
	retailer := o.literal("retailer", retailerWalmart, retailerWalmart)
	product := o.optBoundedStr("product_name", 512)
	size := o.optBoundedStr("unit_size", 64)
	if !o.done(w) {
		return
	}
	if raw {
		name = shopping.MapKey(name)
	}
	var s skuMapping
	err := m.deps.DB.Tx(r.Context(), func(tx *sql.Tx) error {
		var err error
		s, err = m.upsertMapping(r.Context(), tx, c, name, sku, retailer, product, size, "manual")
		return err
	})
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s)
}

// handleDeleteSKUMap is DELETE /grocery/sku-map/{mapping_id} (#48).
func (m *Module) handleDeleteSKUMap(w http.ResponseWriter, r *http.Request, c caller) {
	id, ok := pathInt(w, r, "mapping_id")
	if !ok {
		return
	}
	pred, args := c.visible("g")
	res, err := m.deps.DB.Write.ExecContext(r.Context(), `DELETE FROM recipes_grocery_sku_map AS g WHERE g.id = ? AND `+pred,
		append([]any{id}, args...)...)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		httpx.Error(w, http.StatusNotFound, "Mapping not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type cartItem struct {
	IngredientName string  `json:"ingredient_name"`
	SKU            string  `json:"sku"`
	Quantity       int     `json:"quantity"`
	ProductName    *string `json:"product_name"`
	UnitSize       *string `json:"unit_size"`
	Source         string  `json:"source"`
}

type unmatchedItem struct {
	IngredientName string   `json:"ingredient_name"`
	AmountDisplay  string   `json:"amount_display"`
	Recipes        []string `json:"recipes"`
}

// handleCart is POST /grocery/cart (#49): the shopping list minus staples, matched exactly
// against the map, as a cart link, plus what did not match. When something is unmatched and
// the map has entries to learn from, a background match pass is queued (match_job_id); the
// cart does not wait for it.
func (m *Module) handleCart(w http.ResponseWriter, r *http.Request, c caller) {
	qv := newQueryVals(r)
	start, end := qv.date("start_date"), qv.date("end_date")
	retailer := qv.literal("retailer", retailerWalmart, retailerWalmart)
	if !qv.done(w) {
		return
	}
	if end < start {
		httpx.Error(w, http.StatusUnprocessableEntity, "end_date must not be before start_date")
		return
	}
	ctx := r.Context()
	items, _, err := m.shoppingList(ctx, c, start, end)
	if err != nil {
		m.internalError(w, err)
		return
	}
	mapping, err := listMap(ctx, m.deps.DB.Read, c, retailer)
	if err != nil {
		m.internalError(w, err)
		return
	}
	byName := map[string]skuMapping{}
	for _, s := range mapping {
		byName[s.IngredientName] = s // legacy's dict: the last of a duplicated name wins
	}
	matched, unmatched := []cartItem{}, []unmatchedItem{}
	var pairs []shopping.CartPair
	var names []string
	for _, it := range items {
		if it.IsStaple {
			continue // a cart is an order: the point of a staple is not to buy it again
		}
		s, ok := byName[it.Name]
		if !ok {
			unmatched = append(unmatched, unmatchedItem{it.Name, shopping.AmountDisplay(it.Amounts), it.Recipes})
			names = append(names, it.Name)
			continue
		}
		q := shopping.PackQuantity(it.Amounts, s.UnitSize)
		matched = append(matched, cartItem{it.Name, s.SKU, q, s.ProductName, s.UnitSize, s.Source})
		pairs = append(pairs, shopping.CartPair{SKU: s.SKU, Quantity: q})
	}
	var jobID *string
	if len(names) > 0 && len(mapping) > 0 && m.deps.Queue != nil {
		if len(names) > maxUnmatchedPerPass {
			names = names[:maxUnmatchedPerPass]
		}
		var id string
		err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
			var err error
			id, err = m.createJob(ctx, tx, c, "grocery_match", groceryMatchJobType,
				map[string]any{"retailer": retailer, "unmatched": names})
			return err
		})
		if err != nil {
			m.internalError(w, err)
			return
		}
		m.deps.Queue.Notify(groceryMatchJobType)
		jobID = &id
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"retailer": retailer, "url": shopping.CartURL(pairs), "items": matched, "unmatched": unmatched, "match_job_id": jobID,
	})
}
