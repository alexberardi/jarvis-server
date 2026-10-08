package recipes

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
)

// The cutover export bundle (docs/recipes/00-inventory.md §13, R11): what
// scripts/legacy/recipes-export.sh reads, read-only, out of the legacy stack, and
// `jarvisd import-recipes` reads back. A directory or a tar (optionally gzipped) of:
//
//	manifest.json                    {format, exported_at, source, recipes_alembic_head, files: {path: sha256}}
//	recipes.json … grocery_sku_map.json one json_agg array per legacy recipes table
//	auth_users.json                  legacy jarvis-auth users: [{id, email}] (nothing else)
//	auth_households.json             [{id, name}]
//	auth_household_memberships.json  [{household_id, user_id, role}], oldest first
//	media/<name>                     the editor photos the recipes refer to
//
// Only files the manifest lists (with a matching sha256) are read.

// BundleFormat is the manifest's format tag.
const BundleFormat = "jarvis-recipes-export/1"

// LegacyAlembicHead is the legacy recipes schema the bundle's tables must come from.
const LegacyAlembicHead = "e1f2a3b4c5d6"

// maxBundleFile bounds one file read from a bundle (a photo is at most image.max_bytes on
// legacy's from-image route, but editor uploads were unbounded there).
const maxBundleFile = 64 << 20

var bundleTables = []string{
	"recipes.json", "ingredients.json", "steps.json", "tags.json", "recipe_tags.json",
	"meal_plans.json", "meal_plan_items.json", "staples.json", "grocery_sku_map.json",
	"auth_users.json", "auth_households.json", "auth_household_memberships.json",
}

// BundleManifest is manifest.json.
type BundleManifest struct {
	Format             string            `json:"format"`
	ExportedAt         string            `json:"exported_at"`
	Source             string            `json:"source"`
	RecipesAlembicHead string            `json:"recipes_alembic_head"`
	Files              map[string]string `json:"files"`
}

// Bundle is a loaded, verified export.
type Bundle struct {
	Manifest BundleManifest

	Recipes     []legacyRecipe
	Ingredients []legacyIngredient
	Steps       []legacyStep
	Tags        []legacyTag
	RecipeTags  []legacyRecipeTag
	MealPlans   []legacyMealPlan
	PlanItems   []legacyPlanItem
	Staples     []legacyStaple
	SkuMap      []legacySku
	AuthUsers   []legacyAuthUser
	AuthHouses  []legacyAuthHousehold
	AuthMembers []legacyMembership
	Media       map[string][]byte // name (no directory) → bytes
}

// flexID is an id json_agg rendered as a number (integer columns) or a string (varchar/uuid).
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return errors.New("null id")
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("id %s: %w", b, err)
	}
	*f = flexID(n.String())
	return nil
}

type legacyRecipe struct {
	ID               int64   `json:"id"`
	UserID           flexID  `json:"user_id"`
	HouseholdID      *string `json:"household_id"`
	Title            string  `json:"title"`
	Description      *string `json:"description"`
	ImageURL         *string `json:"image_url"`
	SourceType       string  `json:"source_type"`
	SourceURL        *string `json:"source_url"`
	Servings         *int64  `json:"servings"`
	TotalTimeMinutes *int64  `json:"total_time_minutes"`
	CreatedAt        *string `json:"created_at"`
	UpdatedAt        *string `json:"updated_at"`
}

type legacyIngredient struct {
	ID              int64        `json:"id"`
	RecipeID        int64        `json:"recipe_id"`
	Text            string       `json:"text"`
	QuantityDisplay *string      `json:"quantity_display"`
	QuantityValue   *json.Number `json:"quantity_value"`
	Unit            *string      `json:"unit"`
}

type legacyStep struct {
	ID         int64  `json:"id"`
	RecipeID   int64  `json:"recipe_id"`
	StepNumber int64  `json:"step_number"`
	Text       string `json:"text"`
}

type legacyTag struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type legacyRecipeTag struct {
	RecipeID int64 `json:"recipe_id"`
	TagID    int64 `json:"tag_id"`
}

type legacyMealPlan struct {
	ID          int64   `json:"id"`
	UserID      flexID  `json:"user_id"`
	HouseholdID *string `json:"household_id"`
	Name        *string `json:"name"`
	StartDate   string  `json:"start_date"`
	CreatedAt   *string `json:"created_at"`
}

type legacyPlanItem struct {
	ID         int64  `json:"id"`
	MealPlanID int64  `json:"meal_plan_id"`
	RecipeID   int64  `json:"recipe_id"`
	Date       string `json:"date"`
	MealType   string `json:"meal_type"`
}

type legacyStaple struct {
	ID          int64   `json:"id"`
	UserID      flexID  `json:"user_id"`
	HouseholdID *string `json:"household_id"`
	Name        string  `json:"name"`
	CreatedAt   *string `json:"created_at"`
}

type legacySku struct {
	ID             int64   `json:"id"`
	UserID         flexID  `json:"user_id"`
	HouseholdID    *string `json:"household_id"`
	Retailer       string  `json:"retailer"`
	IngredientName string  `json:"ingredient_name"`
	Sku            string  `json:"sku"`
	ProductName    *string `json:"product_name"`
	UnitSize       *string `json:"unit_size"`
	Source         string  `json:"source"`
	CreatedAt      *string `json:"created_at"`
	UpdatedAt      *string `json:"updated_at"`
}

type legacyAuthUser struct {
	ID    flexID `json:"id"`
	Email string `json:"email"`
}

type legacyAuthHousehold struct {
	ID   flexID `json:"id"`
	Name string `json:"name"`
}

type legacyMembership struct {
	HouseholdID flexID `json:"household_id"`
	UserID      flexID `json:"user_id"`
	Role        string `json:"role"`
}

// LoadBundle reads and verifies a bundle: a directory, or a .tar / .tar.gz / .tgz file.
func LoadBundle(path string) (*Bundle, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	var files map[string][]byte
	if st.IsDir() {
		files, err = readBundleDir(path)
	} else {
		files, err = readBundleTar(path)
	}
	if err != nil {
		return nil, fmt.Errorf("bundle %s: %w", path, err)
	}
	return parseBundle(files)
}

func cleanBundlePath(p string) (string, bool) {
	p = strings.TrimPrefix(filepath.ToSlash(p), "./")
	if p == "" || p == "." {
		return "", false
	}
	c := pathpkg.Clean(p)
	if c != p || strings.HasPrefix(c, "/") || c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}

func readBundleDir(root string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() || !de.Type().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		name, ok := cleanBundlePath(rel)
		if !ok {
			return nil
		}
		data, err := readCapped(p)
		if err != nil {
			return err
		}
		files[name] = data
		return nil
	})
	return files, err
}

func readCapped(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return capRead(f, p)
}

func capRead(r io.Reader, name string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBundleFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBundleFile {
		return nil, fmt.Errorf("%s is larger than %d MiB", name, maxBundleFile>>20)
	}
	return data, nil
}

func readBundleTar(p string) (map[string][]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	var r io.Reader = br
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		r = gz
	}
	tr := tar.NewReader(r)
	files := map[string][]byte{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name, ok := cleanBundlePath(h.Name)
		if !ok {
			return nil, fmt.Errorf("unsafe path %q in the archive", h.Name)
		}
		data, err := capRead(tr, name)
		if err != nil {
			return nil, err
		}
		files[name] = data
	}
}

func parseBundle(files map[string][]byte) (*Bundle, error) {
	raw, ok := files["manifest.json"]
	if !ok {
		return nil, errors.New("no manifest.json (not an export from scripts/legacy/recipes-export.sh?)")
	}
	b := &Bundle{Media: map[string][]byte{}}
	if err := json.Unmarshal(raw, &b.Manifest); err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	m := b.Manifest
	if m.Format != BundleFormat {
		return nil, fmt.Errorf("manifest format %q, want %q", m.Format, BundleFormat)
	}
	if m.RecipesAlembicHead != LegacyAlembicHead {
		return nil, fmt.Errorf("the legacy recipes schema is at %q; this import reads %q only", m.RecipesAlembicHead, LegacyAlembicHead)
	}
	names := make([]string, 0, len(m.Files))
	for name := range m.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data, ok := files[name]
		if !ok {
			return nil, fmt.Errorf("manifest lists %s, which the bundle lacks", name)
		}
		sum := sha256.Sum256(data)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), m.Files[name]) {
			return nil, fmt.Errorf("%s: sha256 does not match the manifest", name)
		}
		if media, ok := strings.CutPrefix(name, "media/"); ok {
			if media == "" || strings.Contains(media, "/") {
				return nil, fmt.Errorf("unexpected media path %s", name)
			}
			b.Media[media] = data
		}
	}
	targets := map[string]any{
		"recipes.json": &b.Recipes, "ingredients.json": &b.Ingredients, "steps.json": &b.Steps,
		"tags.json": &b.Tags, "recipe_tags.json": &b.RecipeTags, "meal_plans.json": &b.MealPlans,
		"meal_plan_items.json": &b.PlanItems, "staples.json": &b.Staples, "grocery_sku_map.json": &b.SkuMap,
		"auth_users.json": &b.AuthUsers, "auth_households.json": &b.AuthHouses,
		"auth_household_memberships.json": &b.AuthMembers,
	}
	for _, name := range bundleTables {
		if _, listed := m.Files[name]; !listed {
			return nil, fmt.Errorf("manifest does not list %s", name)
		}
		data := bytes.TrimSpace(files[name])
		if len(data) == 0 || string(data) == "null" { // json_agg over no rows
			continue
		}
		if err := json.Unmarshal(data, targets[name]); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	return b, nil
}
