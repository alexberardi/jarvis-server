// Package auth is the jarvisd auth module, ported from jarvis-auth (legacy port 7701): users,
// households and memberships, invites, nodes and their service grants, app-to-app clients,
// access/refresh tokens, and account deletion.
//
// Every route keeps jarvis-auth's path, status codes, JSON shapes and `detail` strings, since
// clients parse them. Deliberate differences are commented where they occur (search for
// "Deviation").
//
// Access tokens: one RS256 key is generated on first start and stored in auth_signing_keys
// (decision log). What to mint is the auth.algorithm setting: RS256 by default, HS256 only if
// AUTH_SECRET_KEY is set, for the Python services that still verify HS256 locally during the
// strangler migration. Verification picks key material by algorithm family, never one shared
// key (root CLAUDE.md).
//
// Other modules use the module in-process through authn.Authority (ValidateNode, ValidateApp,
// HouseholdRole), VerifyUser, and OnUserDeleted.
package auth

import (
	"context"
	"database/sql"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Setting keys (jarvis-auth settings_service.py).
const (
	settingAccessMinutes = "auth.token.access_expire_minutes"
	settingRefreshDays   = "auth.token.refresh_expire_days"
	settingGraceSeconds  = "auth.token.refresh_grace_seconds"
	settingAlgorithm     = "auth.algorithm"
)

var noScope = settings.Scope{}

// Definitions are the module's runtime settings.
func Definitions() []settings.Definition {
	return []settings.Definition{
		{Key: settingAccessMinutes, Category: "auth.token", Type: settings.Int, Default: int64(30),
			Description: "Access token expiration time in minutes", EnvFallback: "ACCESS_TOKEN_EXPIRE_MINUTES"},
		{Key: settingRefreshDays, Category: "auth.token", Type: settings.Int, Default: int64(14),
			Description: "Refresh token expiration time in days", EnvFallback: "REFRESH_TOKEN_EXPIRE_DAYS"},
		{Key: settingGraceSeconds, Category: "auth.token", Type: settings.Int, Default: int64(10),
			Description: "Grace window (s) in which a benign double-submit of a just-rotated refresh token re-gets the cached successor instead of being rejected.",
			EnvFallback: "REFRESH_TOKEN_GRACE_SECONDS"},
		// Deviation: the legacy default was HS256. Fresh installs mint RS256; an imported
		// settings row keeps whatever the old install used.
		{Key: settingAlgorithm, Category: "auth", Type: settings.String, Default: authn.RS256,
			Description: "JWT signing algorithm", EnvFallback: "AUTH_ALGORITHM"},
	}
}

// RateLimit configures the brute-force guard on /auth/{register,login,refresh}. Zero values
// take the legacy defaults.
type RateLimit struct {
	Disabled          bool
	IPPerMinute       int           // default 30
	LoginMaxFailures  int           // default 8
	LoginLockout      time.Duration // default 15 min
	TrustForwardedFor bool          // use the right-most X-Forwarded-For hop
	MaxKeys           int           // default 50 000
}

// UserDeletedHook erases one module's data for a deleted user. It runs inside the account
// deletion's single write transaction (decision D20); a returned error rolls back everything.
// It gets the transaction because jarvisd has one writer connection: a hook writing through
// DB.Write itself would wait on the transaction that is waiting on it.
type UserDeletedHook func(ctx context.Context, tx *sql.Tx, userID int64) error

// Module is the auth module.
type Module struct {
	// AdminToken guards /admin/* (JARVIS_AUTH_ADMIN_TOKEN). Empty rejects every admin call.
	AdminToken string
	// HMACSecret is the legacy AUTH_SECRET_KEY: HS256 is minted and verified only when set.
	HMACSecret string
	// InProcess lists the legacy service names jarvisd serves itself (e.g. "jarvis-logs").
	// Account deletion skips the HTTP purge for these; their OnUserDeleted hooks run instead.
	InProcess []string
	// RateLimit tunes the auth flood guard.
	RateLimit RateLimit
	// RevokeFamilyOnReuse revokes a whole refresh family when a rotated token is replayed
	// outside the grace window (legacy REFRESH_TOKEN_REVOKE_FAMILY_ON_REUSE, default off).
	RevokeFamilyOnReuse bool
	// TempPasswordTTL is the default lifetime of admin-issued temporary passwords (24 h).
	TempPasswordTTL time.Duration
	// PurgeTimeout bounds each legacy downstream purge call (5 s).
	PurgeTimeout time.Duration
	// OnSetupToken is told the first-run setup token at Start while no superuser exists
	// (setuptoken.go), with the file holding it ("" without a data directory), so the
	// operator can be shown it. Set it before serving.
	OnSetupToken func(token, path string)

	deps     module.Deps
	settings *settings.Service
	client   *http.Client
	limiter  *rateLimiter
	grace    *graceCache
	verified *verifiedCache

	keyMu sync.Mutex
	keys  []signingKey

	setupMu   sync.Mutex
	setupHash []byte // SHA-256 of the setup token; nil once a superuser exists

	hookMu         sync.Mutex
	hooks          []UserDeletedHook
	memberHooks    []MemberRemovedHook
	householdHooks []HouseholdDeletedHook
}

var _ authn.Authority = (*Module)(nil)

func (m *Module) Name() string      { return "auth" }
func (m *Module) Listener() string  { return pconfig.ListenerAuth }
func (m *Module) Migrations() fs.FS { return Migrations() }

// OnUserDeleted registers a hook run when a user deletes their account. Call it before serving.
func (m *Module) OnUserDeleted(h UserDeletedHook) {
	m.hookMu.Lock()
	defer m.hookMu.Unlock()
	m.hooks = append(m.hooks, h)
}

// Settings returns the module's settings service (valid after Register).
func (m *Module) Settings() *settings.Service { return m.settings }

func (m *Module) Register(mux *http.ServeMux, deps module.Deps) {
	m.deps = deps
	if m.TempPasswordTTL <= 0 {
		m.TempPasswordTTL = 24 * time.Hour
	}
	if m.PurgeTimeout <= 0 {
		m.PurgeTimeout = 5 * time.Second
	}
	m.client = &http.Client{Timeout: m.PurgeTimeout}
	m.limiter = newRateLimiter(m.RateLimit)
	m.grace = newGraceCache()
	m.verified = newVerifiedCache(60 * time.Second)

	svc, err := settings.New(deps.DB, "auth", Definitions(), deps.Log)
	if err != nil {
		panic(err) // static definitions: a programming error
	}
	m.settings = svc
	if err := svc.Migrate(context.Background()); err != nil {
		deps.Log.Error("auth: settings table migration failed", "err", err)
	}

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	svc.Mount(mux, m.settingsReadGuard, m.settingsWriteGuard)

	// auth.py
	mux.HandleFunc("POST /auth/register", m.limited(m.handleRegister))
	mux.HandleFunc("POST /auth/login", m.limited(m.handleLogin))
	mux.HandleFunc("POST /auth/refresh", m.limited(m.handleRefresh))
	mux.HandleFunc("POST /auth/change-password", m.user(m.handleChangePassword))
	mux.HandleFunc("POST /auth/logout", m.handleLogout)
	mux.HandleFunc("GET /auth/me", m.user(m.handleMe))
	mux.HandleFunc("DELETE /auth/me", m.user(m.handleDeleteMe))
	mux.HandleFunc("GET /auth/public-key", m.handlePublicKey)
	mux.HandleFunc("GET /auth/setup-status", m.handleSetupStatus)
	mux.HandleFunc("POST /auth/setup", m.handleSetup)
	mux.HandleFunc("POST /auth/switch-household", m.user(m.handleSwitchHousehold))

	// admin_app_clients.py
	mux.HandleFunc("POST /admin/app-clients", m.admin(m.handleCreateApp))
	mux.HandleFunc("POST /admin/app-clients/{app_id}/rotate", m.admin(m.handleRotateApp))
	mux.HandleFunc("POST /admin/app-clients/{app_id}/revoke", m.admin(m.handleRevokeApp))
	mux.HandleFunc("GET /admin/app-clients", m.admin(m.handleListApps))

	// admin_nodes.py
	mux.HandleFunc("POST /admin/nodes", m.admin(m.handleAdminCreateNode))
	mux.HandleFunc("GET /admin/nodes", m.admin(m.handleAdminListNodes))
	mux.HandleFunc("GET /admin/nodes/{node_id}", m.admin(m.handleAdminGetNode))
	mux.HandleFunc("DELETE /admin/nodes/{node_id}", m.admin(m.handleDeactivateNode))
	mux.HandleFunc("POST /admin/nodes/{node_id}/rotate-key", m.admin(m.handleRotateNodeKey))
	mux.HandleFunc("POST /admin/nodes/{node_id}/services", m.admin(m.handleGrantService))
	mux.HandleFunc("DELETE /admin/nodes/{node_id}/services/{service_id}", m.admin(m.handleRevokeService))

	// admin_users.py
	mux.HandleFunc("PUT /admin/users/{user_id}/superuser", m.admin(m.handleSetSuperuser))
	mux.HandleFunc("GET /admin/users/{user_id}", m.admin(m.handleAdminGetUser))
	mux.HandleFunc("GET /admin/users/by-email/{email}", m.admin(m.handleAdminGetUserByEmail))

	// households.py
	mux.HandleFunc("POST /households", m.user(m.handleCreateHousehold))
	mux.HandleFunc("GET /households", m.user(m.handleListHouseholds))
	mux.HandleFunc("GET /households/{household_id}", m.user(m.handleGetHousehold))
	mux.HandleFunc("PATCH /households/{household_id}", m.user(m.handleUpdateHousehold))
	mux.HandleFunc("DELETE /households/{household_id}", m.user(m.handleDeleteHousehold))
	mux.HandleFunc("GET /households/{household_id}/members", m.user(m.handleListMembers))
	mux.HandleFunc("POST /households/{household_id}/members", m.user(m.handleAddMember))
	mux.HandleFunc("PATCH /households/{household_id}/members/{user_id}", m.user(m.handleUpdateMember))
	mux.HandleFunc("DELETE /households/{household_id}/members/{user_id}", m.user(m.handleRemoveMember))
	mux.HandleFunc("POST /households/{household_id}/leave", m.user(m.handleLeave))
	mux.HandleFunc("GET /households/{household_id}/nodes", m.user(m.handleListHouseholdNodes))
	mux.HandleFunc("POST /households/{household_id}/nodes", m.user(m.handleRegisterHouseholdNode))

	// invites.py
	mux.HandleFunc("POST /households/{household_id}/invites", m.user(m.handleCreateInvite))
	mux.HandleFunc("GET /households/{household_id}/invites", m.user(m.handleListInvites))
	mux.HandleFunc("DELETE /households/{household_id}/invites/{invite_id}", m.user(m.handleRevokeInvite))
	mux.HandleFunc("GET /invites/{code}/validate", m.handleValidateInvite)
	mux.HandleFunc("POST /households/join", m.user(m.handleJoin))

	// internal.py
	mux.HandleFunc("GET /internal/app-ping", m.app(m.handleAppPing))
	mux.HandleFunc("POST /internal/validate-node", m.app(m.handleValidateNode))
	mux.HandleFunc("POST /internal/nodes/register", m.app(m.handleInternalRegisterNode))
	mux.HandleFunc("POST /internal/nodes/{node_id}/services", m.app(m.handleGrantServiceInternal))
	mux.HandleFunc("DELETE /internal/nodes/{node_id}/services/{service_id}", m.app(m.handleRevokeServiceInternal))
	mux.HandleFunc("DELETE /internal/nodes/{node_id}", m.app(m.handleDeactivateNodeInternal))
	mux.HandleFunc("POST /internal/validate-household-access", m.app(m.handleValidateHouseholdAccess))
	mux.HandleFunc("POST /internal/validate-node-household", m.app(m.handleValidateNodeHousehold))
	mux.HandleFunc("GET /internal/users/batch", m.app(m.handleUsersBatch))

	// superuser_views.py
	mux.HandleFunc("GET /superuser/households", m.superuser(m.handleSuperHouseholds))
	mux.HandleFunc("GET /superuser/users", m.superuser(m.handleSuperUsers))
	mux.HandleFunc("POST /superuser/users/{user_id}/temp-password", m.superuser(m.handleTempPassword))
	mux.HandleFunc("GET /superuser/nodes", m.superuser(m.handleSuperNodes))
}

// Start makes sure the signing key exists before the first login needs it, and prepares the
// setup token while no superuser exists.
func (m *Module) Start(ctx context.Context) error {
	if _, err := m.loadKeys(ctx); err != nil {
		return err
	}
	return m.prepareSetupToken(ctx)
}

func (m *Module) internalError(w http.ResponseWriter, err error) {
	m.deps.Log.Error("auth: internal error", "err", err)
	httpx.Error(w, http.StatusInternalServerError, "Internal Server Error")
}
