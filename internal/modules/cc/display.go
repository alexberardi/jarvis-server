package cc

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/alexberardi/jarvis-server/internal/display"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

var displayWriteMu sync.Mutex

func (m *Module) RegisterDisplay(mux *http.ServeMux) {
	for _, method := range []string{"GET", "PUT"} {
		mux.HandleFunc(method+" /api/v0/display/config/{scope}", m.user(m.HandleDisplaySettings))
	}
	mux.HandleFunc("GET /api/v0/display/assets", m.user(m.HandleDisplayAssets))
	mux.HandleFunc("POST /api/v0/display/assets", m.user(m.HandleDisplayAssets))
	mux.HandleFunc("GET /api/v0/display/assets/{asset_id}", m.user(m.HandleDisplayAssets))
	mux.HandleFunc("DELETE /api/v0/display/assets/{asset_id}", m.user(m.HandleDisplayAssets))
	mux.HandleFunc("GET /api/v0/node/display", m.node(m.HandleDisplayDelivery))
	mux.HandleFunc("POST /api/v0/node/display/media", m.node(m.HandleDisplayMedia))
	mux.HandleFunc("POST /api/v0/node/display/ack", m.node(m.HandleDisplayAck))
	mux.HandleFunc("GET /api/v0/node/display/assets/{asset_id}", m.node(m.HandleDisplayAsset))
	mux.HandleFunc("GET /api/v0/node/display/devices", m.node(m.HandleDisplayDevices))
	mux.HandleFunc("GET /api/v0/node/display/devices/{device_id}/state", m.node(m.HandleDisplayDevice))
	mux.HandleFunc("POST /api/v0/node/display/devices/{device_id}/control", m.node(m.HandleDisplayDevice))
}
func (m *Module) GetDisplayLayer(ctx context.Context, scope string) (map[string]any, string, error) {
	var raw, pin string
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT overrides,verifier FROM cc_display_config WHERE scope=?`, scope).Scan(&raw, &pin)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{}, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var v map[string]any
	err = json.Unmarshal([]byte(raw), &v)
	return v, pin, err
}
func (m *Module) GetDisplayDelivery(ctx context.Context, id string) (display.Delivery, error) {
	defaults, pin, err := m.GetDisplayLayer(ctx, "defaults")
	if err != nil {
		return display.Delivery{}, err
	}
	overrides, np, err := m.GetDisplayLayer(ctx, id)
	if err != nil {
		return display.Delivery{}, err
	}
	if np != "" {
		pin = np
	}
	c, err := display.MergeConfiguration(defaults, overrides)
	if err != nil {
		return display.Delivery{}, err
	}
	if c.Enabled && pin == "" {
		return display.Delivery{}, errors.New("configure an exit PIN before enabling the display")
	}
	return display.Delivery{Configuration: c, Revision: display.GetRevision(c, pin), Verifier: pin}, nil
}
func (m *Module) HandleDisplaySettings(w http.ResponseWriter, r *http.Request, u authn.User) {
	if !u.IsSuperuser {
		detail(w, 403, "Display administration requires a superuser")
		return
	}
	if r.Method == "PUT" {
		displayWriteMu.Lock()
		defer displayWriteMu.Unlock()
	}
	ctx := r.Context()
	scope := r.PathValue("scope")
	if scope != "defaults" {
		if _, err := m.nodeByID(ctx, scope); err != nil {
			m.writeErr(w, fail(404, "Node not found"))
			return
		}
	}
	layer, pin, err := m.GetDisplayLayer(ctx, scope)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if r.Method == "PUT" {
		var body struct {
			Overrides map[string]any `json:"overrides"`
			PIN       *string        `json:"pin"`
		}
		if err = json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&body); err != nil || body.Overrides == nil {
			detail(w, 400, "Expected display overrides object")
			return
		}
		layer = body.Overrides
		// Null removes a sparse override; it never masks an inherited value.
		for k, v := range layer {
			if v == nil {
				delete(layer, k)
			}
		}
		if _, err = display.MergeConfiguration(layer); err != nil {
			detail(w, 400, err.Error())
			return
		}
		if body.PIN != nil {
			if *body.PIN == "" {
				pin = ""
			} else {
				pin, err = display.NewVerifier(*body.PIN)
				if err != nil {
					detail(w, 400, err.Error())
					return
				}
			}
		}
		defaults, dp, err := m.GetDisplayLayer(ctx, "defaults")
		if err != nil {
			m.internalError(w, err)
			return
		}
		if scope == "defaults" {
			defaults, dp = layer, pin
		}
		overrides := layer
		if scope == "defaults" {
			overrides = nil
		}
		effective, err := display.MergeConfiguration(defaults, overrides)
		ep := pin
		if ep == "" {
			ep = dp
		}
		if err != nil || effective.Enabled && ep == "" {
			detail(w, 400, "Configure an exit PIN before enabling the display")
			return
		}
		// Changing inherited defaults must not leave an already enabled node without its PIN.
		if scope == "defaults" {
			rows, e := m.deps.DB.Read.QueryContext(ctx, `SELECT overrides,verifier FROM cc_display_config WHERE scope!='defaults'`)
			if e != nil {
				m.internalError(w, e)
				return
			}
			valid := true
			for rows.Next() {
				var raw, p string
				if e = rows.Scan(&raw, &p); e != nil {
					break
				}
				var l map[string]any
				if e = json.Unmarshal([]byte(raw), &l); e != nil {
					break
				}
				c, ce := display.MergeConfiguration(layer, l)
				if ce != nil || c.Enabled && p == "" && pin == "" {
					valid = false
				}
			}
			re := rows.Err()
			rows.Close()
			if e != nil || re != nil {
				detail(w, 500, "Cannot validate display defaults")
				return
			}
			if !valid {
				detail(w, 400, "An enabled node needs an exit PIN")
				return
			}
		}
		// Asset references are checked at the server boundary, before any node applies them.
		ids := append([]string{}, effective.Photos...)
		if effective.Theme != "builtin" {
			ids = append(ids, effective.Theme)
		}
		for _, id := range ids {
			var kind string
			if e := m.deps.DB.Read.QueryRowContext(ctx, `SELECT kind FROM cc_display_assets WHERE id=?`, id).Scan(&kind); e != nil || id == effective.Theme && kind != "theme" || id != effective.Theme && kind != "photo" {
				detail(w, 400, "Unknown or incompatible display asset")
				return
			}
		}
		raw, _ := json.Marshal(layer)
		if _, err = m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_display_config(scope,overrides,verifier) VALUES(?,?,?) ON CONFLICT(scope) DO UPDATE SET overrides=excluded.overrides,verifier=excluded.verifier`, scope, string(raw), pin); err != nil {
			m.internalError(w, err)
			return
		}
		if scope != "defaults" {
			_ = m.bus.Publish(scope, "display/config", map[string]any{"changed": true})
		}
	}
	id := scope
	if id == "defaults" {
		id = ""
	}
	delivery, err := m.GetDisplayDelivery(ctx, id)
	if err != nil {
		detail(w, 400, err.Error())
		return
	}
	var applied, applyError string
	_ = m.deps.DB.Read.QueryRowContext(ctx, `SELECT revision,error FROM cc_display_ack WHERE node_id=?`, id).Scan(&applied, &applyError)
	// Neither a PIN nor a verifier belongs in an administrative read response.
	httpx.WriteJSON(w, 200, map[string]any{"overrides": layer, "configuration": delivery.Configuration, "revision": delivery.Revision, "applied_revision": applied, "apply_error": applyError, "pin_configured": delivery.Verifier != "", "pin_overridden": pin != "", "inherited": m.GetDisplayInherited(ctx), "devices": m.GetDisplayDeviceOptions(ctx, id)})
}
func (m *Module) HandleDisplayDelivery(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	d, err := m.GetDisplayDelivery(r.Context(), n.ID)
	if err != nil {
		detail(w, 400, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, 200, d)
}
func (m *Module) HandleDisplayAck(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	var body struct {
		Revision string `json:"revision"`
		Error    string `json:"error"`
	}
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); e != nil {
		detail(w, 400, "Invalid acknowledgement")
		return
	}
	d, e := m.GetDisplayDelivery(r.Context(), n.ID)
	if e != nil {
		m.internalError(w, e)
		return
	}
	if d.Revision != body.Revision {
		detail(w, 409, "Display configuration has changed")
		return
	}
	if len(body.Error) > 512 {
		body.Error = "Display application failed"
	}
	if _, e = m.deps.DB.Write.ExecContext(r.Context(), `INSERT INTO cc_display_ack(node_id,revision,error,updated_at) VALUES(?,?,?,?) ON CONFLICT(node_id) DO UPDATE SET revision=excluded.revision,error=excluded.error,updated_at=excluded.updated_at`, n.ID, body.Revision, body.Error, dbTime(m.now())); e != nil {
		m.internalError(w, e)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"ok": true})
}
func (m *Module) HandleDisplayAssets(w http.ResponseWriter, r *http.Request, u authn.User) {
	if !u.IsSuperuser {
		detail(w, 403, "Display administration requires a superuser")
		return
	}
	ctx := r.Context()
	id := r.PathValue("asset_id")
	if id != "" {
		if r.Method == "DELETE" {
			displayWriteMu.Lock()
			defer displayWriteMu.Unlock()
			// Refuse deletion while assigned, including inherited defaults.
			var count int
			if e := m.deps.DB.Read.QueryRowContext(ctx, `SELECT count(*) FROM cc_display_config WHERE instr(overrides,?)>0`, id).Scan(&count); e != nil {
				m.internalError(w, e)
				return
			}
			if count > 0 {
				detail(w, 409, "Remove this asset from display settings first")
				return
			}
			if _, e := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_display_assets WHERE id=?`, id); e != nil {
				m.internalError(w, e)
				return
			}
			httpx.WriteJSON(w, 200, map[string]any{"ok": true})
			return
		}
		m.WriteDisplayAsset(w, r, id)
		return
	}
	if r.Method == "POST" {
		b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, display.MaxAssetBytes))
		if e != nil {
			detail(w, 400, "Asset exceeds 20 MiB")
			return
		}
		kind := r.URL.Query().Get("kind")
		name := r.URL.Query().Get("name")
		if len(name) > 120 {
			name = name[:120]
		}
		if kind == "theme" {
			if _, e = display.ReadTheme(b); e != nil {
				detail(w, 400, e.Error())
				return
			}
		} else if kind == "photo" {
			c, _, e := image.DecodeConfig(bytes.NewReader(b))
			if e != nil || c.Width*c.Height > 24000000 {
				detail(w, 400, "Use a PNG or JPEG with at most 24 million pixels")
				return
			}
		} else {
			detail(w, 400, "Expected a theme or photo")
			return
		}
		id = display.Digest(b)
		if _, e = m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_display_assets(id,kind,name,content) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING`, id, kind, name, b); e != nil {
			m.internalError(w, e)
			return
		}
		httpx.WriteJSON(w, 201, map[string]any{"id": id, "kind": kind, "name": name})
		return
	}
	rows, e := m.deps.DB.Read.QueryContext(ctx, `SELECT id,kind,name,length(content) FROM cc_display_assets ORDER BY name`)
	if e != nil {
		m.internalError(w, e)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, kind, name string
		var size int
		if e = rows.Scan(&id, &kind, &name, &size); e != nil {
			m.internalError(w, e)
			return
		}
		out = append(out, map[string]any{"id": id, "kind": kind, "name": name, "size": size})
	}
	if e = rows.Err(); e != nil {
		m.internalError(w, e)
		return
	}
	httpx.WriteJSON(w, 200, out)
}
func (m *Module) WriteDisplayAsset(w http.ResponseWriter, r *http.Request, id string) {
	var kind string
	var b []byte
	if e := m.deps.DB.Read.QueryRowContext(r.Context(), `SELECT kind,content FROM cc_display_assets WHERE id=?`, id).Scan(&kind, &b); e != nil {
		detail(w, 404, "Asset not found")
		return
	}
	if r.URL.Query().Get("preview") == "1" && kind == "theme" {
		theme, err := display.ReadTheme(b)
		if err != nil {
			detail(w, 400, "Invalid theme")
			return
		}
		preview, err := display.RenderPreview(theme)
		if err != nil {
			detail(w, 400, "Preview failed")
			return
		}
		httpx.WriteJSON(w, 200, map[string]any{"html": preview})
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", `attachment; filename="display-`+id+`"`)
	if kind == "theme" {
		w.Header().Set("Content-Type", "application/zip")
	} else {
		w.Header().Set("Content-Type", http.DetectContentType(b))
	}
	_, _ = w.Write(b)
}
func (m *Module) HandleDisplayAsset(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	d, e := m.GetDisplayDelivery(r.Context(), n.ID)
	if e != nil {
		detail(w, 400, "Display unavailable")
		return
	}
	id := r.PathValue("asset_id")
	allowed := id == d.Configuration.Theme
	for _, p := range d.Configuration.Photos {
		allowed = allowed || id == p
	}
	if !allowed {
		detail(w, 404, "Asset not assigned to this node")
		return
	}
	m.WriteDisplayAsset(w, r, id)
}
func (m *Module) GetDisplayOwner(ctx context.Context, n *nodeCtx) (provAuth, error) {
	id := deviceOwner(n)
	if id == 0 {
		return provAuth{}, fail(403, "Sign this display in to a household member")
	}
	if err := m.requireRole(ctx, id, n.HouseholdID, authn.RoleMember); err != nil {
		return provAuth{}, err
	}
	return provAuth{user: authn.User{ID: id, HouseholdID: n.HouseholdID}}, nil
}
func (m *Module) HandleDisplayDevices(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	if _, e := m.GetDisplayOwner(r.Context(), n); e != nil {
		m.writeErr(w, e)
		return
	}
	devs, e := m.queryDevices(r.Context(), `d.household_id=? AND d.is_active=1`, n.HouseholdID)
	if e != nil {
		m.internalError(w, e)
		return
	}
	out := []map[string]any{}
	for _, d := range devs {
		out = append(out, map[string]any{"id": d.id, "name": d.name, "entity_id": d.entityID, "domain": d.domain, "room_name": d.roomName.String, "supported_actions": supportedActions(d.domain, d.protocol.String, d.controllable())})
	}
	httpx.WriteJSON(w, 200, out)
}
func (m *Module) HandleDisplayDevice(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	a, e := m.GetDisplayOwner(r.Context(), n)
	if e != nil {
		m.writeErr(w, e)
		return
	}
	d, e := m.device(r.Context(), n.HouseholdID, r.PathValue("device_id"))
	if e != nil {
		m.writeErr(w, e)
		return
	}
	r.SetPathValue("household_id", n.HouseholdID)
	if r.Method == "GET" {
		m.handleDeviceState(w, r, a)
		return
	}
	var b struct {
		Action    string         `json:"action"`
		Data      map[string]any `json:"data"`
		Confirmed bool           `json:"confirmed"`
	}
	if e = json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&b); e != nil {
		detail(w, 400, "Invalid device action")
		return
	}
	allowed := false
	for _, v := range supportedActions(d.domain, d.protocol.String, d.controllable()) {
		allowed = allowed || v["button_action"] == b.Action
	}
	if !allowed {
		detail(w, 400, "Action is not advertised by this device")
		return
	}
	if (b.Action == "unlock" || strings.HasPrefix(b.Action, "open")) && !b.Confirmed {
		detail(w, 409, "Confirm opening this access device")
		return
	}
	// A display may supply values, never replace the catalogue's identity or routing fields.
	for k := range b.Data {
		switch k {
		case "temperature", "brightness", "mode", "hvac_mode", "volume", "position", "percentage":
		default:
			detail(w, 400, fmt.Sprintf("Unsupported action field %q", k))
			return
		}
	}
	for key, value := range b.Data {
		if key == "mode" || key == "hvac_mode" {
			v, ok := value.(string)
			if !ok || len(v) > 50 || strings.ContainsAny(v, "\x00\r\n") {
				detail(w, 400, "Invalid device mode")
				return
			}
		} else {
			v, ok := value.(float64)
			if !ok || v < -100 || v > 150 || (key != "temperature" && (v < 0 || v > 100)) {
				detail(w, 400, "Invalid device action value")
				return
			}
		}
	}
	raw, _ := json.Marshal(map[string]any{"action": b.Action, "data": b.Data})
	r.Body = io.NopCloser(bytes.NewReader(raw))
	m.handleControlDevice(w, r, a)
}

func (m *Module) GetDisplayInherited(ctx context.Context) display.Configuration {
	l, _, _ := m.GetDisplayLayer(ctx, "defaults")
	c, _ := display.MergeConfiguration(l)
	return c
}
func (m *Module) HandleDisplayMedia(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	if _, err := m.GetDisplayOwner(r.Context(), n); err != nil {
		m.writeErr(w, err)
		return
	}
	var b struct {
		Action      string  `json:"action"`
		URL         string  `json:"url"`
		Destination string  `json:"destination"`
		Seconds     float64 `json:"seconds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&b); err != nil {
		detail(w, 400, "Invalid media request")
		return
	}
	switch b.Action {
	case "play", "pause", "resume", "seek", "stop", "status":
	default:
		detail(w, 400, "Unsupported media action")
		return
	}
	if b.Action == "play" && !display.WebURL(b.URL) {
		detail(w, 400, "External playback requires a direct HTTP(S) media link")
		return
	}
	if b.Seconds < 0 || b.Seconds > 864000 {
		detail(w, 400, "Invalid seek position")
		return
	}
	devs, err := m.queryDevices(r.Context(), `d.household_id=? AND d.domain='media_player' AND d.is_active=1`, n.HouseholdID)
	if err != nil {
		m.internalError(w, err)
		return
	}
	var selected *deviceRow
	for _, d := range devs {
		if d.id == b.Destination || d.entityID == b.Destination || strings.EqualFold(d.name, b.Destination) {
			if selected != nil {
				detail(w, 400, "Destination name is ambiguous; use its device ID")
				return
			}
			selected = d
		}
	}
	if selected == nil || selected.sourceOr() != "home_assistant" {
		detail(w, 404, "Home Assistant media destination unavailable; choose local playback")
		return
	}
	target, err := m.pickNode(r.Context(), n.HouseholdID, selected.protocol.String)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	rid := uuid4()
	res, ok := m.awaitNode(r.Context(), target.nodeID, rid, controlWait, func() error {
		m.bus.CommandWithID(target.nodeID, "display_media", map[string]any{"entity_id": selected.entityID, "action": b.Action, "url": b.URL, "seconds": b.Seconds, "reply_request_id": rid}, rid)
		return nil
	})
	if !ok {
		detail(w, 504, "Media destination unavailable; choose local playback")
		return
	}
	httpx.WriteJSON(w, 200, res)
}

func (m *Module) GetDisplayDeviceOptions(ctx context.Context, id string) []map[string]any {
	out := []map[string]any{}
	n, err := m.nodeByID(ctx, id)
	if err != nil {
		return out
	}
	devices, err := m.queryDevices(ctx, `d.household_id=? AND d.is_active=1`, n.householdID.String)
	if err != nil {
		return out
	}
	for _, d := range devices {
		out = append(out, map[string]any{"id": d.id, "name": d.name, "room": d.roomName.String})
	}
	return out
}
