// Package recipes is the jarvisd recipes module (jarvis-recipes-server, legacy port 7030): the
// household's recipe box, meal planner, shopping list and the recipe import pipelines, ported
// into jarvisd (docs/recipes/00-inventory.md; decisions in docs/recipes/QUESTIONS.md).
//
// Its only client is jarvis-recipes-mobile, which calls it with a user JWT. Routes stay
// wire-compatible with the legacy FastAPI service; deliberate changes are marked with the
// spec's route row (CHANGE #n) or bug number (Bn).
//
// Data is household-shared (§4.1, RD7): see auth.go for the one scoping predicate.
package recipes

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns the module's goose migrations, rooted at the migrations directory.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err) // unreachable: the directory is embedded at build time
	}
	return sub
}

// ServiceName is the legacy service name (what /info reports and the registry lists).
const ServiceName = "jarvis-recipes-server"

// UserVerifier verifies a user access token. The auth module implements it (VerifyUser).
// Errors wrapping authn.ErrInvalid, ErrExpired or ErrNoSub are a 401; anything else is a 500.
type UserVerifier interface {
	VerifyUser(ctx context.Context, token string) (authn.User, error)
}

// HouseholdLister lists the households a user belongs to (the auth module's UserHouseholds).
// Reads cover all of them (RD7).
type HouseholdLister interface {
	UserHouseholds(ctx context.Context, userID int64) ([]string, error)
}

// Setting keys (§7.5). The llm.* values are jarvisd LLM labels (live/background).
const (
	SettingFullModel        = "llm.full_model_name"
	SettingLightweightModel = "llm.lightweight_model_name"
	SettingBackgroundModel  = "llm.background_model_name"
	SettingMaxRetries       = "queue.max_retries"
	SettingAbandonMinutes   = "parse_job.abandon_minutes"
	SettingImageMaxBytes    = "image.max_bytes"
	SettingUserAgent        = "scraper.user_agent"
)

// Definitions are the module's settings (legacy settings_service.py; env fallbacks are cut, §7.5).
var Definitions = []settings.Definition{
	{Key: SettingFullModel, Category: "llm", Type: settings.String, Default: "live",
		Description: "Model name used for full recipe extraction and meal planning"},
	{Key: SettingLightweightModel, Category: "llm", Type: settings.String, Default: "live",
		Description: "Model name used for cheap OCR-text structuring passes"},
	{Key: SettingBackgroundModel, Category: "llm", Type: settings.String, Default: "background",
		Description: "Model name used for slow background passes (grocery SKU matching)"},
	{Key: SettingMaxRetries, Category: "queue", Type: settings.Int, Default: int64(3),
		Description: "Maximum retries for a failed recipe parse job"},
	{Key: SettingAbandonMinutes, Category: "parse_job", Type: settings.Int, Default: int64(4320),
		Description: "Minutes before an in-progress parse job is considered abandoned"},
	{Key: SettingImageMaxBytes, Category: "image", Type: settings.Int, Default: int64(10 * 1024 * 1024),
		Description: "Maximum accepted size of a single uploaded recipe image, in bytes"},
	{Key: SettingUserAgent, Category: "scraper", Type: settings.String,
		Default:     "Mozilla/5.0 (Macintosh; Intel Mac OS X 13_6) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36",
		Description: "User-Agent header sent when fetching recipe pages"},
}

// Queue job types.
const blobPurgeJobType = "recipes.blob_purge"

// Module is the recipes module.
type Module struct {
	// Users verifies the mobile app's bearer tokens.
	Users UserVerifier
	// Households resolves the caller's memberships for the RD7 union. Nil trusts the token's
	// household claim alone (tests).
	Households HouseholdLister
	// LLM runs the background passes (the grocery SKU match) in process. Nil: they learn nothing.
	LLM LLM
	// Clock gives "today" for /planner/current in the household's zone (cc's household clock).
	// Nil uses the host's zone.
	Clock HouseholdClock
	// SettingsRead and SettingsWrite guard /settings; both nil leaves them unmounted.
	SettingsRead, SettingsWrite settings.Guard

	deps     module.Deps
	settings *settings.Service
	now      func() time.Time
}

func (m *Module) Name() string      { return "recipes" }
func (m *Module) Listener() string  { return pconfig.ListenerRecipes }
func (m *Module) Migrations() fs.FS { return Migrations() }

// Settings returns the module's settings service (valid after Register).
func (m *Module) Settings() *settings.Service { return m.settings }

func (m *Module) Register(mux *http.ServeMux, deps module.Deps) {
	m.deps = deps
	if m.now == nil {
		m.now = time.Now
	}
	svc, err := settings.New(deps.DB, "recipes", Definitions, deps.Log)
	if err != nil {
		panic(err) // static definitions
	}
	m.settings = svc
	if m.SettingsRead != nil && m.SettingsWrite != nil {
		svc.Mount(mux, m.SettingsRead, m.SettingsWrite)
	}

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("GET /info", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"service": ServiceName})
	})

	// §3.1 recipes. GET /recipes/jobs (#16) and /recipes/stock (#9) are deliberately not
	// registered: they fall through to /recipes/{recipe_id} and answer its 422, as legacy's
	// shadowed route did.
	mux.HandleFunc("GET /recipes", m.user(m.handleListRecipes))
	mux.HandleFunc("POST /recipes", m.user(m.handleCreateRecipe))
	mux.HandleFunc("GET /recipes/{recipe_id}", m.user(m.handleGetRecipe))
	mux.HandleFunc("PATCH /recipes/{recipe_id}", m.user(m.handleUpdateRecipe))
	mux.HandleFunc("DELETE /recipes/{recipe_id}", m.user(m.handleDeleteRecipe))
	mux.HandleFunc("GET /recipes/user/{recipe_id}", m.user(m.handleGetOwnRecipe))
	mux.HandleFunc("GET /recipes/stage/{stage_id}", m.user(m.handleGetStageRecipe))
	mux.HandleFunc("GET /recipes/core/{recipe_id}", handleCoreRecipe) // #8: no auth, always 404

	// §3.3 tags and stock.
	mux.HandleFunc("GET /tags", m.user(m.handleListTags))
	mux.HandleFunc("POST /tags", m.user(m.handleCreateTag))
	mux.HandleFunc("GET /ingredients/stock", m.user(m.handleStockIngredients))
	mux.HandleFunc("GET /units/stock", m.user(m.handleStockUnits))

	// §3.4 planner and the random quick plan (R4).
	mux.HandleFunc("POST /planner/commit", m.user(m.handleCommitPlan))
	mux.HandleFunc("GET /planner/current", m.user(m.handleCurrentPlan))
	mux.HandleFunc("GET /planner/plans", m.user(m.handleListPlans))
	mux.HandleFunc("GET /planner/plans/{plan_id}", m.user(m.handleGetPlan))
	mux.HandleFunc("DELETE /planner/plans/{plan_id}", m.user(m.handleDeletePlan))
	mux.HandleFunc("PATCH /planner/plans/{plan_id}/items", m.user(m.handleMoveItems))
	mux.HandleFunc("POST /meal-plans/random", m.user(m.handleRandomPlan))
	mux.HandleFunc("POST /meal-plans/random/reroll", m.user(m.handleReroll))

	// §3.5 shopping list and staples (R4).
	mux.HandleFunc("GET /shopping-list", m.user(m.handleShoppingList))
	mux.HandleFunc("GET /staples", m.user(m.handleListStaples))
	mux.HandleFunc("POST /staples", m.user(m.handleAddStaple))
	mux.HandleFunc("DELETE /staples/{staple_id}", m.user(m.handleDeleteStaple))

	// §3.5 grocery SKU map and cart (R5), and the job status poll (#13) its match job needs.
	mux.HandleFunc("GET /grocery/sku-map", m.user(m.handleListSKUMap))
	mux.HandleFunc("PUT /grocery/sku-map", m.user(m.handlePutSKUMap))
	mux.HandleFunc("DELETE /grocery/sku-map/{mapping_id}", m.user(m.handleDeleteSKUMap))
	mux.HandleFunc("POST /grocery/cart", m.user(m.handleCart))
	mux.HandleFunc("GET /recipes/jobs/{job_id}", m.user(m.handleGetJob))

	// §3.2 the job list and cancel (R6).
	mux.HandleFunc("GET /recipes/parse-url/jobs", m.user(m.handleListJobs))
	mux.HandleFunc("POST /recipes/jobs/{job_id}/cancel", m.user(m.handleCancelJob))

	// §3.2 editor photos (R3).
	mux.HandleFunc("POST /recipes/import/image", m.user(m.handleImportImage))
	mux.HandleFunc("GET /media/{name...}", m.handleMedia) // #22: no auth

	if deps.Queue != nil {
		deps.Queue.Register(blobPurgeJobType, queue.Handler{Run: m.runBlobPurge, MaxAttempts: 5, Lease: time.Minute})
		// §6: one attempt (legacy never retried); a model failure completes with nothing learned.
		deps.Queue.Register(groceryMatchJobType, queue.Handler{Run: m.runGroceryMatch, Concurrency: 1, MaxAttempts: 1,
			Lease: matchTimeout + time.Minute})
		deps.Queue.Register(cleanupJobType, queue.Handler{Run: m.runCleanup, MaxAttempts: 1, Lease: 10 * time.Minute})
	}
}

// Start migrates the settings table, upserts the embedded stock reference data and schedules
// the hourly cleanup.
func (m *Module) Start(ctx context.Context) error {
	if err := m.settings.Migrate(ctx); err != nil {
		return err
	}
	if err := m.seedStock(ctx); err != nil {
		return err
	}
	return m.startCleanup(ctx)
}

// --- timestamps ---

// tsFormat is how this module stores timestamps: UTC, microseconds (the legacy precision).
const tsFormat = "2006-01-02T15:04:05.000000Z"

func ts(t time.Time) string { return t.UTC().Format(tsFormat) }

func parseTS(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// pyNaive renders a stored timestamp like Python's naive isoformat(): microseconds, omitted
// when zero (legacy columns were naive UTC; the wire stays zone-less, B28).
func pyNaive(s string) string {
	t := parseTS(s)
	if t.Nanosecond()/1000 == 0 {
		return t.Format("2006-01-02T15:04:05")
	}
	return t.Format("2006-01-02T15:04:05.000000")
}
