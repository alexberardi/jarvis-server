package admin

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	authmod "github.com/alexberardi/jarvis-server/internal/modules/auth"
	configmod "github.com/alexberardi/jarvis-server/internal/modules/config"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// The Connections page (AD7, inventory §6.2 #8): jarvisd's own listeners (read-only, with
// health), the operator's external registry entries (add/remove), and app clients
// (list/create/rotate/revoke, the key shown once). Calls config and auth in process.

// Registry is the service registry (the config module).
type Registry interface {
	Services(ctx context.Context) ([]configmod.ServiceEntry, error)
	ProbeAll(ctx context.Context, es []configmod.ServiceEntry) map[string]configmod.HealthStatus
	AddService(ctx context.Context, n configmod.NewService) (configmod.ServiceEntry, error)
	RemoveService(ctx context.Context, name string) error
}

// AppClients manages app-to-app credentials (the auth module).
type AppClients interface {
	AppClients(ctx context.Context) ([]authmod.AppClient, error)
	CreateAppClient(ctx context.Context, appID, name string) (authmod.AppClient, string, error)
	RotateAppClient(ctx context.Context, appID string) (key, rotatedAt string, err error)
	RevokeAppClient(ctx context.Context, appID string) error
}

type listenerConn struct {
	Name    string                  `json:"name"`
	URL     string                  `json:"url"`
	Port    int                     `json:"port"`
	Managed string                  `json:"managed"`
	Health  *configmod.HealthStatus `json:"health"`
}

type externalConn struct {
	Name        string                  `json:"name"`
	URL         string                  `json:"url"`
	HealthPath  string                  `json:"health_path"`
	Description string                  `json:"description"`
	Managed     string                  `json:"managed"`
	Removable   bool                    `json:"removable"`
	Health      *configmod.HealthStatus `json:"health"`
}

var appIDRule = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// handleConnections is GET /api/connections[?health=false]: {listeners, external, apps}.
// Health probes run concurrently (bounded by config's health_check.timeout); health=false
// skips them and every health is null.
func (m *Module) handleConnections(w http.ResponseWriter, r *http.Request) {
	if m.Registry == nil || m.Apps == nil {
		unavailable(w, "config or auth")
		return
	}
	ctx := r.Context()
	svcs, err := m.Registry.Services(ctx)
	if err != nil {
		m.deps.Log.Error("admin: listing the registry failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	var health map[string]configmod.HealthStatus
	if h := r.URL.Query().Get("health"); h != "false" && h != "0" {
		health = m.Registry.ProbeAll(ctx, svcs)
	}
	healthOf := func(name string) *configmod.HealthStatus {
		if h, ok := health[name]; ok {
			return &h
		}
		return nil
	}
	listeners, external := []listenerConn{}, []externalConn{}
	for _, s := range svcs {
		switch s.Managed {
		case configmod.ManagedListener, configmod.ManagedBroker:
			listeners = append(listeners, listenerConn{Name: s.Name, URL: s.URL, Port: s.Port, Managed: s.Managed, Health: healthOf(s.Name)})
		default:
			external = append(external, externalConn{Name: s.Name, URL: s.URL, HealthPath: s.HealthPath,
				Description: s.Description, Managed: s.Managed, Removable: s.Managed == "", Health: healthOf(s.Name)})
		}
	}
	apps, err := m.Apps.AppClients(ctx)
	if err != nil {
		m.deps.Log.Error("admin: listing app clients failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"listeners": listeners, "external": external, "apps": apps})
}

// handleAddService is POST /api/connections/services {name, url, health_path?, description?}:
// 201 with the entry; 422 invalid; 409 taken or a name jarvisd manages.
func (m *Module) handleAddService(w http.ResponseWriter, r *http.Request) {
	if m.Registry == nil {
		unavailable(w, "config")
		return
	}
	var body struct {
		Name        string `json:"name"`
		URL         string `json:"url"`
		HealthPath  string `json:"health_path"`
		Description string `json:"description"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	e, err := m.Registry.AddService(r.Context(), configmod.NewService{Name: body.Name, URL: body.URL,
		HealthPath: body.HealthPath, Description: body.Description})
	var ve *configmod.ValidationError
	switch {
	case errors.As(err, &ve):
		httpx.ValidationError(w, ve.Fields...)
		return
	case errors.Is(err, configmod.ErrServiceExists):
		httpx.Error(w, http.StatusConflict, "Service '"+strings.TrimSpace(body.Name)+"' already exists")
		return
	case errors.Is(err, configmod.ErrServiceManaged):
		httpx.Error(w, http.StatusConflict, "'"+strings.TrimSpace(body.Name)+"' is managed by jarvisd")
		return
	case err != nil:
		m.deps.Log.Error("admin: adding a service failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	m.deps.Log.Info("admin: registry entry added", "name", e.Name, "url", e.URL)
	httpx.WriteJSON(w, http.StatusCreated, externalConn{Name: e.Name, URL: e.URL, HealthPath: e.HealthPath,
		Description: e.Description, Removable: true})
}

// handleRemoveService is DELETE /api/connections/services/{name}: 204; 404 unknown; 409 for a
// row jarvisd manages (its own listeners, the broker, setting-synced rows).
func (m *Module) handleRemoveService(w http.ResponseWriter, r *http.Request) {
	if m.Registry == nil {
		unavailable(w, "config")
		return
	}
	name := r.PathValue("name")
	switch err := m.Registry.RemoveService(r.Context(), name); {
	case errors.Is(err, configmod.ErrServiceNotFound):
		httpx.Error(w, http.StatusNotFound, "Service '"+name+"' not found")
	case errors.Is(err, configmod.ErrServiceManaged):
		httpx.Error(w, http.StatusConflict, "'"+name+"' is managed by jarvisd and can't be removed")
	case err != nil:
		m.deps.Log.Error("admin: removing a service failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "Internal server error")
	default:
		m.deps.Log.Info("admin: registry entry removed", "name", name)
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleCreateApp is POST /api/connections/apps {app_id, name}: 201 {app_id, name, is_active,
// created_at, last_rotated_at, app_key}. The key is shown this once.
func (m *Module) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	if m.Apps == nil {
		unavailable(w, "auth")
		return
	}
	var body struct {
		AppID string `json:"app_id"`
		Name  string `json:"name"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	body.AppID, body.Name = strings.TrimSpace(body.AppID), strings.TrimSpace(body.Name)
	var errs []httpx.FieldError
	if !appIDRule.MatchString(body.AppID) {
		errs = append(errs, httpx.FieldError{Type: "string_pattern_mismatch", Loc: []any{"body", "app_id"},
			Msg: "1-64 letters, digits, '.', '_' or '-', starting with a letter or digit", Input: body.AppID})
	}
	if n := utf8.RuneCountInString(body.Name); n < 1 || n > 128 {
		errs = append(errs, httpx.FieldError{Type: "string_length", Loc: []any{"body", "name"},
			Msg: "String should have between 1 and 128 characters", Input: body.Name})
	}
	if len(errs) > 0 {
		httpx.ValidationError(w, errs...)
		return
	}
	a, key, err := m.Apps.CreateAppClient(r.Context(), body.AppID, body.Name)
	if errors.Is(err, authmod.ErrAppExists) {
		httpx.Error(w, http.StatusConflict, "App '"+body.AppID+"' already exists")
		return
	}
	if err != nil {
		m.deps.Log.Error("admin: creating an app client failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	m.deps.Log.Info("admin: app client created", "app_id", a.AppID)
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"app_id": a.AppID, "name": a.Name, "is_active": a.IsActive,
		"created_at": a.CreatedAt, "last_rotated_at": a.LastRotatedAt, "app_key": key})
}

// handleRotateApp is POST /api/connections/apps/{app_id}/rotate: 200 {app_id, app_key,
// last_rotated_at, is_active: true}. The old key stops working; a revoked client is
// reactivated (the one "reissue" action). The key is shown this once.
func (m *Module) handleRotateApp(w http.ResponseWriter, r *http.Request) {
	if m.Apps == nil {
		unavailable(w, "auth")
		return
	}
	id := r.PathValue("app_id")
	key, rotated, err := m.Apps.RotateAppClient(r.Context(), id)
	if errors.Is(err, authmod.ErrAppNotFound) {
		httpx.Error(w, http.StatusNotFound, "App client not found")
		return
	}
	if err != nil {
		m.deps.Log.Error("admin: rotating an app key failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	m.deps.Log.Info("admin: app key rotated", "app_id", id)
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"app_id": id, "app_key": key, "last_rotated_at": rotated, "is_active": true})
}

// handleRevokeApp is POST /api/connections/apps/{app_id}/revoke: 200 {app_id, is_active: false}.
func (m *Module) handleRevokeApp(w http.ResponseWriter, r *http.Request) {
	if m.Apps == nil {
		unavailable(w, "auth")
		return
	}
	id := r.PathValue("app_id")
	err := m.Apps.RevokeAppClient(r.Context(), id)
	if errors.Is(err, authmod.ErrAppNotFound) {
		httpx.Error(w, http.StatusNotFound, "App client not found")
		return
	}
	if err != nil {
		m.deps.Log.Error("admin: revoking an app client failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	m.deps.Log.Info("admin: app client revoked", "app_id", id)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"app_id": id, "is_active": false})
}
