// Package notifications is the jarvisd notifications module (jarvis-notifications, legacy port
// 7712): the mobile inbox, device push tokens, and push sends.
//
// Two ways in (docs/cc D31):
//   - HTTP, unchanged for mobile (user JWT: inbox, tokens, /me/data), other services
//     (app-to-app: POST/PATCH /inbox, /notify) and operators (X-Api-Key: /admin/*).
//   - In process: Notify and CreateInboxItem / UpdateInboxItem, which take an optional
//     *sql.Tx so a producer writes its inbox row (and queues its push) in the same SQLite
//     transaction as its own state.
//
// Push delivery is a durable queue job (pushJob) with retries, posting to the same relay the
// legacy service used (RELAY_URL → POST /v1/send, which fronts Expo Push). A /notify call
// answers at once with the log row in "pending"; the job fills in the outcome.
package notifications

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
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
const ServiceName = "jarvis-notifications"

const (
	pushJobType    = "notifications.push"
	cleanupJobType = "notifications.cleanup"
)

// UserVerifier verifies a user access token. The auth module implements it (VerifyUser);
// KeysVerifier adapts bare key material. Errors wrapping authn.ErrInvalid, ErrExpired or
// ErrNoSub are a 401; anything else is an internal failure.
type UserVerifier interface {
	VerifyUser(ctx context.Context, token string) (authn.User, error)
}

// KeysVerifier verifies tokens locally with authn.Keys (HS256 and RS256 by algorithm family),
// as the legacy service did with AUTH_SECRET_KEY.
type KeysVerifier struct {
	Keys authn.Keys
	Now  func() time.Time
}

func (k KeysVerifier) VerifyUser(_ context.Context, token string) (authn.User, error) {
	now := time.Now
	if k.Now != nil {
		now = k.Now
	}
	c, err := k.Keys.Verify(token, now())
	if err != nil {
		return authn.User{}, err
	}
	id, err := c.UserID()
	if err != nil {
		return authn.User{}, err
	}
	return authn.User{ID: id, Email: c.Email, IsSuperuser: c.IsSuperuser, HouseholdID: c.HouseholdID}, nil
}

// Module is the notifications module.
type Module struct {
	// Auth validates app-to-app callers (ValidateApp).
	Auth authn.Authority
	// Users verifies mobile users' bearer tokens.
	Users UserVerifier
	// AdminKey guards /api/v0/admin/* (legacy ADMIN_API_KEY, header X-Api-Key). Empty
	// rejects every admin call.
	AdminKey string
	// RelayURL is the push relay (legacy RELAY_URL). Empty disables delivery: sends are logged
	// as "skipped", as before; the inbox and tokens work regardless.
	RelayURL string
	// RelayHouseholdJWT pins the relay JWT (legacy RELAY_HOUSEHOLD_JWT). Normally empty: the
	// module registers each household with the relay on first push and caches the JWT.
	RelayHouseholdJWT string
	// HTTPClient talks to the relay. Default: no client-level timeout (each call sets one).
	HTTPClient *http.Client
	// LogRetentionDays prunes notification_log rows (legacy NOTIFICATION_LOG_RETENTION_DAYS, 30).
	LogRetentionDays int
	// CleanupInterval runs the log/token cleanup (legacy TOKEN_CLEANUP_INTERVAL_HOURS, 24 h).
	// Negative disables it.
	CleanupInterval time.Duration
	// PushRetryDelays are the waits before each push retry (legacy 30 s, 60 s, 120 s).
	PushRetryDelays []time.Duration

	deps  module.Deps
	now   func() time.Time
	dedup *dedupCache
	relay *relayClient
}

func (m *Module) Name() string      { return "notifications" }
func (m *Module) Listener() string  { return pconfig.ListenerNotifications }
func (m *Module) Migrations() fs.FS { return Migrations() }

func (m *Module) Register(mux *http.ServeMux, deps module.Deps) {
	m.deps = deps
	if m.now == nil {
		m.now = time.Now
	}
	if m.LogRetentionDays <= 0 {
		m.LogRetentionDays = 30
	}
	if m.CleanupInterval == 0 {
		m.CleanupInterval = 24 * time.Hour
	}
	if len(m.PushRetryDelays) == 0 {
		m.PushRetryDelays = []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second}
	}
	client := m.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	m.dedup = newDedupCache()
	m.relay = &relayClient{url: strings.TrimRight(m.RelayURL, "/"), pinnedJWT: m.RelayHouseholdJWT, http: client, cache: map[string]string{}}

	mux.HandleFunc("GET /info", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"service": ServiceName})
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": ServiceName})
	})

	// Mobile (user JWT).
	mux.HandleFunc("GET /api/v0/inbox", m.user(m.handleListInbox))
	mux.HandleFunc("GET /api/v0/inbox/unread-count", m.user(m.handleUnreadCount))
	mux.HandleFunc("POST /api/v0/inbox/bulk/read", m.user(m.handleBulkRead))
	mux.HandleFunc("POST /api/v0/inbox/bulk/delete", m.user(m.handleBulkDelete))
	mux.HandleFunc("GET /api/v0/inbox/{item_id}", m.user(m.handleGetInbox))
	mux.HandleFunc("PATCH /api/v0/inbox/{item_id}/read", m.user(m.handleMarkRead))
	mux.HandleFunc("DELETE /api/v0/inbox/{item_id}", m.user(m.handleDeleteInbox))
	mux.HandleFunc("POST /api/v0/tokens", m.user(m.handleRegisterToken))
	mux.HandleFunc("DELETE /api/v0/tokens", m.user(m.handleUnregisterToken))
	mux.HandleFunc("GET /api/v0/tokens/me", m.user(m.handleMyTokens))
	mux.HandleFunc("DELETE /api/v0/me/data", m.user(m.handlePurgeMe))

	// Services (app-to-app).
	mux.HandleFunc("POST /api/v0/inbox", m.app(m.handleCreateInbox))
	mux.HandleFunc("PATCH /api/v0/inbox/{item_id}", m.app(m.handleUpdateInbox))
	mux.HandleFunc("POST /api/v0/notify", m.app(m.handleNotify))
	mux.HandleFunc("POST /api/v0/notify/batch", m.app(m.handleNotifyBatch))

	// Operators (X-Api-Key).
	mux.HandleFunc("GET /api/v0/admin/stats", m.admin(m.handleStats))
	mux.HandleFunc("POST /api/v0/admin/cleanup", m.admin(m.handleCleanup))

	if deps.Queue != nil {
		deps.Queue.Register(pushJobType, queue.Handler{
			Run: m.runPush, Concurrency: 2,
			MaxAttempts: len(m.PushRetryDelays) + 1,
			Lease:       2 * time.Minute,
			Backoff: func(attempt int) time.Duration {
				return m.PushRetryDelays[min(attempt, len(m.PushRetryDelays))-1]
			},
		})
		deps.Queue.Register(cleanupJobType, queue.Handler{Run: m.runCleanup})
	}
}

// Start schedules the periodic cleanup. Like the legacy loop, the first run is one interval
// after the trigger is created.
func (m *Module) Start(ctx context.Context) error {
	if m.deps.Scheduler == nil || m.CleanupInterval < 0 {
		return nil
	}
	return m.deps.Scheduler.Ensure(ctx, scheduler.Trigger{
		Name: cleanupJobType, Kind: scheduler.KindInterval, JobType: cleanupJobType,
		Spec: scheduler.Spec{Every: m.CleanupInterval},
	})
}

// --- auth ---

type ctxKey int

const appCtx ctxKey = 0

func (m *Module) internalError(w http.ResponseWriter, err error) {
	m.deps.Log.Error("notifications: internal error", "err", err)
	httpx.Error(w, http.StatusInternalServerError, "Internal Server Error")
}

// user authenticates "Authorization: Bearer <jwt>" like FastAPI's HTTPBearer + the legacy
// get_current_user: no or non-bearer credentials are 401 "Not authenticated" (with
// WWW-Authenticate), a bad token is 401 "Invalid or expired token".
func (m *Module) user(h func(http.ResponseWriter, *http.Request, authn.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scheme, tok, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		tok = strings.TrimSpace(tok)
		if !strings.EqualFold(scheme, "Bearer") || tok == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			httpx.Error(w, http.StatusUnauthorized, "Not authenticated")
			return
		}
		u, err := m.Users.VerifyUser(r.Context(), tok)
		if err != nil {
			if errors.Is(err, authn.ErrInvalid) || errors.Is(err, authn.ErrExpired) || errors.Is(err, authn.ErrNoSub) {
				httpx.Error(w, http.StatusUnauthorized, "Invalid or expired token")
				return
			}
			m.internalError(w, err)
			return
		}
		h(w, r, u)
	}
}

func (m *Module) app(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, key, ok := authn.AppCreds(r)
		if !ok {
			httpx.Error(w, http.StatusUnauthorized, "Missing app credentials")
			return
		}
		a, valid, err := m.Auth.ValidateApp(r.Context(), id, key)
		if err != nil {
			m.deps.Log.Error("notifications: app validation failed", "err", err)
			httpx.Error(w, http.StatusBadGateway, "Auth service unavailable: "+err.Error())
			return
		}
		if !valid {
			httpx.Error(w, http.StatusUnauthorized, "Invalid app credentials")
			return
		}
		if a.ID == "" {
			a.ID = id
		}
		h(w, r.WithContext(authn.WithApp(r.Context(), a)))
	}
}

// admin checks X-Api-Key. A missing header is FastAPI's 422 for a required Header(...).
func (m *Module) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vals := r.Header.Values("X-Api-Key")
		if len(vals) == 0 {
			httpx.ValidationError(w, httpx.FieldError{Type: "missing", Loc: []any{"header", "X-Api-Key"}, Msg: "Field required"})
			return
		}
		if !authn.Equal(vals[0], m.AdminKey) {
			httpx.Error(w, http.StatusUnauthorized, "Invalid admin key")
			return
		}
		h(w, r)
	}
}

// sourceService is the calling app's id (legacy request.state.calling_app_id).
func sourceService(r *http.Request) string {
	if a, ok := authn.AppFrom(r.Context()); ok && a.ID != "" {
		return a.ID
	}
	return "unknown"
}

// --- ids and timestamps ---

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

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

// pyNaive renders a UTC time like Python's naive isoformat(): microseconds, omitted when zero.
func pyNaive(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond()/1000 == 0 {
		return t.Format("2006-01-02T15:04:05")
	}
	return t.Format("2006-01-02T15:04:05.000000")
}

// --- request validation (pydantic-shaped 422s) ---

// readObject reads a JSON object body. Failures are FastAPI's 422 shapes.
func readObject(w http.ResponseWriter, r *http.Request) (*obj, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, httpx.MaxBody))
	if err != nil {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "Request body too large")
		return nil, false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		httpx.ValidationError(w, httpx.FieldError{Type: "missing", Loc: []any{"body"}, Msg: "Field required"})
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		httpx.ValidationError(w, httpx.FieldError{Type: "json_invalid", Loc: []any{"body", dec.InputOffset()}, Msg: "JSON decode error", Input: map[string]any{}})
		return nil, false
	}
	m, ok := v.(map[string]any)
	if !ok {
		httpx.ValidationError(w, httpx.FieldError{Type: "model_attributes_type", Loc: []any{"body"},
			Msg: "Input should be a valid dictionary or object to extract fields from", Input: v})
		return nil, false
	}
	return newObj(m, "body"), true
}

// obj validates one JSON object's fields, collecting errors like pydantic. Nested objects
// (child) share the error list.
type obj struct {
	m    map[string]any
	loc  []any
	errs *[]httpx.FieldError
}

func newObj(m map[string]any, loc ...any) *obj {
	return &obj{m: m, loc: loc, errs: &[]httpx.FieldError{}}
}

func (o *obj) failAt(typ, msg string, input any, loc ...any) {
	*o.errs = append(*o.errs, httpx.FieldError{Type: typ, Loc: append(append([]any{}, o.loc...), loc...), Msg: msg, Input: input})
}

func (o *obj) fail(name any, typ, msg string, input any) { o.failAt(typ, msg, input, name) }

// done writes the collected errors as a 422 and returns false, or returns true if none.
func (o *obj) done(w http.ResponseWriter) bool {
	if len(*o.errs) > 0 {
		httpx.ValidationError(w, *o.errs...)
		return false
	}
	return true
}

// child validates a nested object (a list element) into the same error list.
func (o *obj) child(v any, loc ...any) (*obj, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		o.failAt("model_type", "Input should be a valid dictionary or instance of the model", v, loc...)
		return nil, false
	}
	return &obj{m: m, loc: append(append([]any{}, o.loc...), loc...), errs: o.errs}, true
}

// str reads a string field. Required fields report "missing"; null is only allowed when
// optional. ok is false when absent, null or invalid.
func (o *obj) str(name string, required bool) (string, bool) {
	v, present := o.m[name]
	switch {
	case !present:
		if required {
			o.fail(name, "missing", "Field required", nil)
		}
		return "", false
	case v == nil && !required:
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		o.fail(name, "string_type", "Input should be a valid string", v)
		return "", false
	}
	return s, true
}

// optInt reads an optional, nullable integer (pydantic lax mode: integral numbers and numeric
// strings are accepted).
func (o *obj) optInt(name string) (int64, bool) {
	v, present := o.m[name]
	if !present || v == nil {
		return 0, false
	}
	switch x := v.(type) {
	case json.Number:
		if n, err := strconv.ParseInt(x.String(), 10, 64); err == nil {
			return n, true
		}
		if f, err := x.Float64(); err == nil && f == float64(int64(f)) {
			return int64(f), true
		}
		o.fail(name, "int_from_float", "Input should be a valid integer, got a number with a fractional part", v)
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
			return n, true
		}
		o.fail(name, "int_parsing", "Input should be a valid integer, unable to parse string as an integer", v)
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	default:
		o.fail(name, "int_type", "Input should be a valid integer", v)
	}
	return 0, false
}

// optDict reads an optional, nullable object. present reports a non-null object.
func (o *obj) optDict(name string) (map[string]any, bool) {
	v, ok := o.m[name]
	if !ok || v == nil {
		return nil, false
	}
	d, ok := v.(map[string]any)
	if !ok {
		o.fail(name, "dict_type", "Input should be a valid dictionary", v)
		return nil, false
	}
	return d, true
}

// list reads a required array.
func (o *obj) list(name string) ([]any, bool) {
	v, present := o.m[name]
	if !present {
		o.fail(name, "missing", "Field required", nil)
		return nil, false
	}
	l, ok := v.([]any)
	if !ok {
		o.fail(name, "list_type", "Input should be a valid list", v)
		return nil, false
	}
	return l, true
}

// strList reads a required list of strings.
func (o *obj) strList(name string) []string {
	l, ok := o.list(name)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(l))
	for i, e := range l {
		s, ok := e.(string)
		if !ok {
			o.failAt("string_type", "Input should be a valid string", e, name, i)
			continue
		}
		out = append(out, s)
	}
	return out
}

// marshalJSON encodes a JSON value for a TEXT column.
func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("notifications: encode json: %w", err)
	}
	return string(b), nil
}

// decodeJSONObject decodes a stored JSON object, keeping integers exact. Anything else is nil.
func decodeJSONObject(s string) map[string]any {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
		return nil
	}
	return m
}
