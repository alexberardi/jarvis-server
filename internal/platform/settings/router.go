package settings

import (
	"context"
	"net/http"
	"strconv"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Guard authorizes a request, writing the error response itself and returning false to stop.
type Guard func(w http.ResponseWriter, r *http.Request) bool

// Mount serves the legacy /settings API (jarvis-settings-client's create_settings_router) for
// this service. read guards GETs; write guards PUT and POST.
func (s *Service) Mount(mux *http.ServeMux, read, write Guard) {
	guard := func(g Guard, h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if g(w, r) {
				h(w, r)
			}
		}
	}
	mux.HandleFunc("GET /settings", guard(read, s.handleList))
	mux.HandleFunc("GET /settings/{$}", guard(read, s.handleList))
	mux.HandleFunc("GET /settings/categories", guard(read, s.handleCategories))
	mux.HandleFunc("GET /settings/{key...}", guard(read, s.handleGet))
	mux.HandleFunc("PUT /settings/{key...}", guard(write, s.handlePut))
	mux.HandleFunc("POST /settings/sync-from-env", guard(write, s.handleSyncFromEnv))
	mux.HandleFunc("POST /settings/invalidate-cache", guard(write, s.handleInvalidate))
}

// apiError is the settings router's structured error body.
func apiError(w http.ResponseWriter, status int, typ, msg, code string) {
	httpx.Error(w, status, map[string]any{"error": map[string]any{"type": typ, "message": msg, "code": code}})
}

func scopeFrom(w http.ResponseWriter, r *http.Request) (Scope, bool) {
	q := r.URL.Query()
	sc := Scope{HouseholdID: q.Get("household_id"), NodeID: q.Get("node_id")}
	if u := q.Get("user_id"); u != "" {
		id, err := strconv.ParseInt(u, 10, 64)
		if err != nil {
			httpx.Error(w, http.StatusUnprocessableEntity, "user_id must be an integer")
			return Scope{}, false
		}
		sc.UserID = id
	}
	return sc, true
}

type settingResponse struct {
	Key            string `json:"key"`
	Value          any    `json:"value"`
	ValueType      Type   `json:"value_type"`
	Category       string `json:"category"`
	Description    string `json:"description"`
	RequiresReload bool   `json:"requires_reload"`
	IsSecret       bool   `json:"is_secret"`
	EnvFallback    any    `json:"env_fallback"`
	FromDB         bool   `json:"from_db"`
	Options        []any  `json:"options"`
}

func (s *Service) render(ctx context.Context, def Definition, sc Scope) (settingResponse, error) {
	v, err := s.Get(ctx, def.Key, sc)
	if err != nil {
		return settingResponse{}, err
	}
	val := v.Value
	if def.IsSecret && truthy(val) {
		val = "********"
	}
	var envFallback any
	if def.EnvFallback != "" {
		envFallback = def.EnvFallback
	}
	return settingResponse{
		Key: def.Key, Value: val, ValueType: def.Type, Category: def.Category,
		Description: def.Description, RequiresReload: def.RequiresReload, IsSecret: def.IsSecret,
		EnvFallback: envFallback, FromDB: v.FromDB, Options: def.Options,
	}, nil
}

// handleList resolves every setting at the requested scope. The Python client ignored the
// scope here and showed whichever row it met last; this uses the same cascade as a single get
// (docs/cc D8: fix bugs).
func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	sc, ok := scopeFrom(w, r)
	if !ok {
		return
	}
	category := r.URL.Query().Get("category")
	out := []settingResponse{}
	for _, def := range s.Definitions() {
		if category != "" && def.Category != category {
			continue
		}
		resp, err := s.render(r.Context(), def, sc)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "internal_error", "Failed to list settings", "list_failed")
			return
		}
		out = append(out, resp)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"settings": out, "total": len(out)})
}

func (s *Service) handleCategories(w http.ResponseWriter, r *http.Request) {
	cats := s.Categories()
	if cats == nil {
		cats = []string{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"categories": cats})
}

func notFound(w http.ResponseWriter, key string) {
	apiError(w, http.StatusNotFound, "not_found", "Setting not found: "+key, "not_found")
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	def, ok := s.defs[key]
	if !ok {
		notFound(w, key)
		return
	}
	sc, ok := scopeFrom(w, r)
	if !ok {
		return
	}
	resp, err := s.render(r.Context(), def, sc)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "internal_error", "Failed to read setting: "+key, "read_failed")
		return
	}
	resp.Options = nil // the single-get response never carried options
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (s *Service) handlePut(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	def, ok := s.defs[key]
	if !ok {
		notFound(w, key)
		return
	}
	sc, ok := scopeFrom(w, r)
	if !ok {
		return
	}
	var body map[string]any
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	value, present := body["value"]
	if !present { // pydantic: `value: Any` is required (null is allowed)
		httpx.ValidationError(w, httpx.FieldError{Type: "missing", Loc: []any{"body", "value"}, Msg: "Field required", Input: body})
		return
	}
	if err := s.Set(r.Context(), key, value, sc); err != nil {
		s.log.Error("settings: update failed", "key", key, "err", err)
		apiError(w, http.StatusInternalServerError, "internal_error", "Failed to update setting: "+key, "update_failed")
		return
	}
	var msg any
	if def.RequiresReload {
		msg = "Service restart required for this change to take effect"
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true, "key": key, "requires_reload": def.RequiresReload, "message": msg,
	})
}

// handleSyncFromEnv writes each env-fallback value that is set into the system scope.
func (s *Service) handleSyncFromEnv(w http.ResponseWriter, r *http.Request) {
	synced := map[string]bool{}
	n := 0
	for _, def := range s.Definitions() {
		v, ok := lookupEnv(s.getenv, def.EnvFallback)
		if def.EnvFallback == "" || !ok {
			synced[def.Key] = false
			continue
		}
		if err := s.Set(r.Context(), def.Key, s.coerce(v, def.Type, def), Scope{}); err != nil {
			synced[def.Key] = false
			continue
		}
		synced[def.Key] = true
		n++
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"synced": synced, "total_synced": n, "total_skipped": len(synced) - n,
	})
}

// handleInvalidate is kept for client compatibility; jarvisd has no settings cache.
func (s *Service) handleInvalidate(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		key = "all"
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "invalidated": key})
}
