package recipes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

// Deletion hooks (§5.2, decision RD4). They have the auth module's hook signatures and run
// inside auth's write transaction, so a failure rolls the account or membership change back.
//
// RD4: the household keeps its shared rows. A leaver's household-scoped recipes, meal plans,
// staples and SKU mappings stay (their user_id becomes a dangling author id nothing displays);
// only their private rows (no household), parse jobs, photo imports and staged recipes go.
// Deleting a household removes everything with its household_id.
//
// Blobs (photo-import originals, editor photos) are deleted by a queue job enqueued in the same
// transaction, so they go only once the rows are gone for good.

// mediaPrefix is how editor uploads appear in image_url (RD3: relative, unchanged on the wire);
// the object lives at mediaBlobPrefix + name.
const (
	mediaPrefix     = "/media/"
	mediaBlobPrefix = "recipes/media/"
)

type blobPurge struct {
	Keys  []string `json:"keys,omitempty"`  // blob keys to delete outright
	Media []string `json:"media,omitempty"` // /media names, deleted unless a remaining recipe uses them
}

// PurgeUser erases a deleted user's recipes data (auth.OnUserDeleted).
func (m *Module) PurgeUser(ctx context.Context, tx *sql.Tx, userID int64) error {
	uid := strconv.FormatInt(userID, 10)
	var p blobPurge
	var err error
	if p.Keys, err = ingestionKeys(ctx, tx, `user_id = ?`, uid); err != nil {
		return err
	}
	if p.Media, err = mediaNames(ctx, tx, `user_id = ? AND household_id IS NULL`, uid); err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM recipes_recipe_parse_jobs WHERE user_id = ?`,
		`DELETE FROM recipes_recipe_ingestions WHERE user_id = ?`,
		`DELETE FROM recipes_stage_recipes WHERE user_id = ?`,
		`DELETE FROM recipes_recipes WHERE user_id = ? AND household_id IS NULL`,
		`DELETE FROM recipes_meal_plans WHERE user_id = ? AND household_id IS NULL`,
		`DELETE FROM recipes_staples WHERE user_id = ? AND household_id IS NULL`,
		`DELETE FROM recipes_grocery_sku_map WHERE user_id = ? AND household_id IS NULL`,
		`DELETE FROM recipes_users WHERE user_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, uid); err != nil {
			return fmt.Errorf("recipes: purge user %d: %w", userID, err)
		}
	}
	return m.enqueuePurge(ctx, tx, p)
}

// PurgeUserHousehold erases what a user who left a household kept only for themselves there:
// their parse jobs, photo imports and staged recipes in it (auth.OnMemberRemoved). Their
// shared rows stay with the household; their private rows stay theirs.
func (m *Module) PurgeUserHousehold(ctx context.Context, tx *sql.Tx, userID int64, householdID string) error {
	uid := strconv.FormatInt(userID, 10)
	keys, err := ingestionKeys(ctx, tx, `user_id = ? AND household_id = ?`, uid, householdID)
	if err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM recipes_recipe_parse_jobs WHERE user_id = ? AND household_id = ?`,
		`DELETE FROM recipes_recipe_ingestions WHERE user_id = ? AND household_id = ?`,
		`DELETE FROM recipes_stage_recipes WHERE user_id = ? AND household_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, uid, householdID); err != nil {
			return fmt.Errorf("recipes: purge user %d in household: %w", userID, err)
		}
	}
	return m.enqueuePurge(ctx, tx, blobPurge{Keys: keys})
}

// PurgeHousehold erases everything recipes holds for a deleted household (auth.OnHouseholdDeleted).
func (m *Module) PurgeHousehold(ctx context.Context, tx *sql.Tx, householdID string) error {
	var p blobPurge
	var err error
	if p.Keys, err = ingestionKeys(ctx, tx, `household_id = ?`, householdID); err != nil {
		return err
	}
	if p.Media, err = mediaNames(ctx, tx, `household_id = ?`, householdID); err != nil {
		return err
	}
	for _, t := range []string{
		"recipes_recipes", "recipes_meal_plans", "recipes_staples", "recipes_grocery_sku_map",
		"recipes_recipe_parse_jobs", "recipes_recipe_ingestions", "recipes_stage_recipes",
	} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+t+` WHERE household_id = ?`, householdID); err != nil {
			return fmt.Errorf("recipes: purge household from %s: %w", t, err)
		}
	}
	return m.enqueuePurge(ctx, tx, p)
}

// ingestionKeys collects the blob keys of the matching photo imports.
func ingestionKeys(ctx context.Context, tx *sql.Tx, where string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT image_s3_keys FROM recipes_recipe_ingestions WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var keys []string
		if json.Unmarshal([]byte(raw), &keys) == nil {
			out = append(out, keys...)
		}
	}
	return out, rows.Err()
}

// mediaNames collects the /media names of the matching recipes' editor photos.
func mediaNames(ctx context.Context, tx *sql.Tx, where string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT image_url FROM recipes_recipes WHERE image_url LIKE '/media/%' AND `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		if name := strings.TrimPrefix(u, mediaPrefix); name != "" && !strings.Contains(name, "/") {
			out = append(out, name)
		}
	}
	return out, rows.Err()
}

func (m *Module) enqueuePurge(ctx context.Context, tx *sql.Tx, p blobPurge) error {
	if (len(p.Keys) == 0 && len(p.Media) == 0) || m.deps.Queue == nil {
		return nil
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = m.deps.Queue.EnqueueTx(ctx, tx, blobPurgeJobType, payload, queue.Options{})
	return err
}

// runBlobPurge deletes the blobs a purge left behind. An editor photo another remaining recipe
// still shows (a copy in another household) is kept.
func (m *Module) runBlobPurge(ctx context.Context, job queue.Job) ([]byte, error) {
	var p blobPurge
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return nil, queue.Permanent(err)
	}
	if m.deps.Blobs == nil {
		return nil, nil
	}
	for _, k := range p.Keys {
		if err := m.deps.Blobs.Delete(ctx, k); err != nil {
			return nil, err
		}
	}
	for _, name := range p.Media {
		var n int
		if err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT COUNT(*) FROM recipes_recipes WHERE image_url = ?`,
			mediaPrefix+name).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			continue
		}
		if err := m.deps.Blobs.Delete(ctx, mediaBlobPrefix+name); err != nil {
			return nil, err
		}
	}
	return nil, nil
}
