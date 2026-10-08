package recipes

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/recipes/quantity"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Stock reference data (§5.3): the recipe editor's ingredient and unit pickers. Legacy seeded
// it through an admin route (#29, CUT); jarvisd upserts the embedded files at Start, only when
// their hash changed. Rows are matched by name case-insensitively, as legacy's ILIKE did.

//go:embed static/ingredients.json static/units_of_measure.json
var static embed.FS

const stockHashKey = "stock_data_sha256"

type stockIngredient struct {
	Name     string   `json:"name"`
	Category *string  `json:"category"`
	Synonyms []string `json:"synonyms"`
	// allergen is ignored, as in legacy.
}

type stockUnit struct {
	Name         string  `json:"name"`
	Abbreviation *string `json:"abbreviation"`
}

func (m *Module) seedStock(ctx context.Context) error {
	ingRaw, err := static.ReadFile("static/ingredients.json")
	if err != nil {
		return err
	}
	unitRaw, err := static.ReadFile("static/units_of_measure.json")
	if err != nil {
		return err
	}
	sum := sha256.Sum256(append(append([]byte{}, ingRaw...), unitRaw...))
	hash := hex.EncodeToString(sum[:])

	var have string
	err = m.deps.DB.Read.QueryRowContext(ctx, `SELECT value FROM recipes_meta WHERE key = ?`, stockHashKey).Scan(&have)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if have == hash {
		return nil
	}
	var ings []stockIngredient
	if err := json.Unmarshal(ingRaw, &ings); err != nil {
		return fmt.Errorf("recipes: stock ingredients: %w", err)
	}
	var units []stockUnit
	if err := json.Unmarshal(unitRaw, &units); err != nil {
		return fmt.Errorf("recipes: stock units: %w", err)
	}
	now := ts(m.now())
	err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		for _, i := range ings {
			var syn []string
			for _, s := range i.Synonyms {
				if s = quantity.PyStrip(s); s != "" {
					syn = append(syn, s)
				}
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO recipes_stock_ingredients (name, category, synonyms, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (name COLLATE NOCASE) DO UPDATE SET name = excluded.name,
					category = excluded.category, synonyms = excluded.synonyms, updated_at = excluded.updated_at`,
				quantity.PyStrip(i.Name), i.Category, strings.Join(syn, ", "), now, now); err != nil {
				return fmt.Errorf("recipes: seed ingredient %q: %w", i.Name, err)
			}
		}
		for _, u := range units {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO recipes_stock_units_of_measure (name, abbreviation, created_at, updated_at)
				VALUES (?, ?, ?, ?)
				ON CONFLICT (name COLLATE NOCASE) DO UPDATE SET name = excluded.name,
					abbreviation = excluded.abbreviation, updated_at = excluded.updated_at`,
				quantity.PyStrip(u.Name), u.Abbreviation, now, now); err != nil {
				return fmt.Errorf("recipes: seed unit %q: %w", u.Name, err)
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO recipes_meta (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, stockHashKey, hash)
		return err
	})
	if err != nil {
		return err
	}
	m.deps.Log.Info("recipes: stock reference data seeded", "ingredients", len(ings), "units", len(units))
	return nil
}

// handleStockIngredients is GET /ingredients/stock (#27): q is a case-insensitive substring of
// the name (legacy ILIKE, wildcards unescaped), limit 1..1000 (default 10), ordered by name.
func (m *Module) handleStockIngredients(w http.ResponseWriter, r *http.Request, _ caller) {
	limit, ok := queryLimit(w, r, "limit", 10, 1, 1000)
	if !ok {
		return
	}
	q := r.URL.Query().Get("q")
	query, args := `SELECT id, name FROM recipes_stock_ingredients`, []any{}
	if q != "" {
		query += ` WHERE name LIKE ?`
		args = append(args, "%"+strings.ToLower(q)+"%")
	}
	rows, err := m.deps.DB.Read.QueryContext(r.Context(), query+` ORDER BY name COLLATE NOCASE, name LIMIT ?`, append(args, limit)...)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			m.internalError(w, err)
			return
		}
		out = append(out, map[string]any{"id": id, "name": name})
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleStockUnits is GET /units/stock (#28): q matches the name or the abbreviation, limit
// 1..100 (default 10).
func (m *Module) handleStockUnits(w http.ResponseWriter, r *http.Request, _ caller) {
	limit, ok := queryLimit(w, r, "limit", 10, 1, 100)
	if !ok {
		return
	}
	q := r.URL.Query().Get("q")
	query, args := `SELECT id, name, abbreviation FROM recipes_stock_units_of_measure`, []any{}
	if q != "" {
		like := "%" + strings.ToLower(q) + "%"
		query += ` WHERE name LIKE ? OR abbreviation LIKE ?`
		args = append(args, like, like)
	}
	rows, err := m.deps.DB.Read.QueryContext(r.Context(), query+` ORDER BY name COLLATE NOCASE, name LIMIT ?`, append(args, limit)...)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var name string
		var abbr sql.NullString
		if err := rows.Scan(&id, &name, &abbr); err != nil {
			m.internalError(w, err)
			return
		}
		var a any
		if abbr.Valid {
			a = abbr.String
		}
		out = append(out, map[string]any{"id": id, "name": name, "abbreviation": a})
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
