package cc

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Smart home (docs/cc/07-smart-home.md): rooms, the device registry, device control and
// state relayed to a node, the scan / device-list / Bluetooth request-poll jobs, provider
// OAuth sessions, and the D29 camera stubs. CC is the household's registry and switchboard;
// every protocol call happens on a node. Config push lives in settingsreq.go (5a).

// Setting keys (settings_definitions.py:521-566). smart_home.use_home_assistant is cut (M7).
const (
	settingDeviceManager      = "smart_home.device_manager"
	settingPrimaryNode        = "smart_home.primary_node_id"
	settingUseExternalDevices = "smart_home.use_external_devices"
	settingOAuthRelayURL      = "oauth.relay_url"
	settingOAuthExternalURL   = "oauth.external_url"
)

func smartHomeDefinitions() []settings.Definition {
	return []settings.Definition{
		{Key: settingDeviceManager, Category: "smart_home", Type: settings.String, Default: "jarvis_direct",
			Description: "Active device manager for device listing", Options: []any{"jarvis_direct", "home_assistant"}},
		{Key: settingPrimaryNode, Category: "smart_home", Type: settings.String, Default: "",
			Description: "Node that handles device management (discovery, listing) for the household"},
		{Key: settingUseExternalDevices, Category: "smart_home", Type: settings.Bool, Default: false,
			Description: "Show devices from the node's device manager instead of the CC database"},
		{Key: settingOAuthRelayURL, Category: "oauth", Type: settings.String, Default: "",
			Description: "Relay URL for OAuth bounce (external providers like Google)", EnvFallback: "JARVIS_RELAY_URL"},
		{Key: settingOAuthExternalURL, Category: "oauth", Type: settings.String, Default: "",
			Description: "Public base URL for this service (used as OAuth redirect URI base)", EnvFallback: "JARVIS_EXTERNAL_URL"},
	}
}

// smartHome is the subsystem's state: the OAuth token cipher and the exchange egress guard
// (both replaceable in tests).
type smartHome struct {
	cipher *tokenCipher
	// guard decides whether the token exchange may connect to ip; local is the
	// provider_base_url (LAN) mode. Default oauthBlocked.
	guard func(ip netip.Addr, local bool) bool
	// tls overrides the exchange client's TLS config (tests trust a local server).
	tls *tls.Config
}

// registerSmartHome mounts doc 07's routes.
func (m *Module) registerSmartHome(mux *http.ServeMux) {
	if m.smart == nil {
		m.smart = &smartHome{}
	}
	if m.smart.cipher == nil {
		m.smart.cipher = newTokenCipher(m.deps.Config.Home)
	}
	if m.smart.guard == nil {
		m.smart.guard = oauthBlocked
	}
	const v0 = "/api/v0"
	hh := v0 + "/households/{household_id}"
	mux.HandleFunc("GET "+hh+"/smart-home/config", m.prov(m.handleGetSmartHomeConfig))
	mux.HandleFunc("PUT "+hh+"/smart-home/config", m.prov(m.handlePutSmartHomeConfig))

	// Rooms. GET also takes node auth scoped to the node's own household (Q7).
	mux.HandleFunc("GET "+hh+"/rooms", m.handleListRooms)
	mux.HandleFunc("POST "+hh+"/rooms", m.prov(m.handleCreateRoom))
	mux.HandleFunc("PATCH "+hh+"/rooms/{room_id}", m.prov(m.handleUpdateRoom))
	mux.HandleFunc("DELETE "+hh+"/rooms/{room_id}", m.prov(m.handleDeleteRoom))

	// Devices (control-external is cut, D9).
	mux.HandleFunc("GET "+hh+"/devices", m.prov(m.handleListDevices))
	mux.HandleFunc("POST "+hh+"/devices/import", m.prov(m.handleImportDevices))
	mux.HandleFunc("PATCH "+hh+"/devices/{device_id}", m.prov(m.handleUpdateDevice))
	mux.HandleFunc("DELETE "+hh+"/devices/{device_id}", m.prov(m.handleDeleteDevice))
	mux.HandleFunc("POST "+hh+"/devices/{device_id}/control", m.prov(m.handleControlDevice))
	mux.HandleFunc("GET "+hh+"/devices/{device_id}/state", m.prov(m.handleDeviceState))
	mux.HandleFunc("GET "+v0+"/node/devices", m.node(m.handleNodeDevices))
	// /device-control-results and /device-state-results are the shared result sink (cc.go).

	// Request/poll jobs.
	n := v0 + "/nodes/{node_id}"
	mux.HandleFunc("POST "+n+"/device-scan/request", m.prov(m.handleRequestJob(scanJob)))
	mux.HandleFunc("POST "+n+"/device-scan/{request_id}/results", m.node(m.handleUploadDeviceJob(scanJob)))
	mux.HandleFunc("GET "+n+"/device-scan/{request_id}", m.prov(m.handlePollDeviceScan))
	mux.HandleFunc("POST "+n+"/device-list/request", m.prov(m.handleRequestJob(listJob)))
	mux.HandleFunc("POST "+n+"/device-list/{request_id}/results", m.node(m.handleUploadDeviceJob(listJob)))
	mux.HandleFunc("GET "+n+"/device-list/{request_id}", m.prov(m.handlePollDeviceList))

	// Bluetooth (bluetooth.py; release and auto-connect are new, D8).
	mux.HandleFunc("POST "+n+"/bluetooth-scan/request", m.prov(m.handleRequestJob(btScanJob)))
	mux.HandleFunc("POST "+n+"/bluetooth-scan/{request_id}/results", m.node(m.handleUploadBTScan))
	mux.HandleFunc("GET "+n+"/bluetooth-scan/{request_id}", m.prov(m.handlePollBTScan))
	mux.HandleFunc("POST "+n+"/bluetooth/pair", m.prov(m.handleRequestJob(btPairJob)))
	mux.HandleFunc("POST "+n+"/bluetooth/pair/{request_id}/results", m.node(m.handleUploadBTPair))
	mux.HandleFunc("GET "+n+"/bluetooth/pair/{request_id}", m.prov(m.handlePollBTPair))
	mux.HandleFunc("POST "+n+"/bluetooth/disconnect", m.prov(m.handleBTFire("bluetooth-disconnect")))
	mux.HandleFunc("POST "+n+"/bluetooth/discoverable", m.prov(m.handleBTFire("bluetooth-discoverable")))
	mux.HandleFunc("POST "+n+"/bluetooth/release", m.prov(m.handleBTFire("bluetooth-release")))
	mux.HandleFunc("POST "+n+"/bluetooth/auto-connect", m.prov(m.handleBTFire("bluetooth-auto-connect")))
	mux.HandleFunc("GET "+n+"/bluetooth/status", m.prov(m.handleBTStatus))

	// Provider OAuth (oauth.py).
	mux.HandleFunc("POST "+v0+"/oauth/sessions", m.prov(m.handleCreateAuthSession))
	mux.HandleFunc("GET "+v0+"/oauth/callback", m.handleOAuthCallback)
	mux.HandleFunc("POST "+v0+"/oauth/sessions/{session_id}/exchange", m.prov(m.handleExchangeCode))
	mux.HandleFunc("GET "+v0+"/oauth/sessions/{session_id}", m.prov(m.handleAuthSessionStatus))
	mux.HandleFunc("GET "+v0+"/oauth/provider/{provider}/credentials", m.node(m.handleProviderCredentials))

	// Cameras: deferred (D29). Rows stay listed; streaming answers "not available".
	mux.HandleFunc("GET "+hh+"/cameras", m.prov(m.handleListCameras))
	mux.HandleFunc("POST "+hh+"/cameras/{device_id}/stream", m.prov(m.handleStartCameraStream))
	mux.HandleFunc("DELETE "+hh+"/cameras/{device_id}/stream", m.prov(m.handleStopCameraStream))
	mux.HandleFunc("GET "+v0+"/cameras/stream/{stream_name}/{path...}", m.prov(m.handleCameraProxy))
}

// --- verify_provisioning_auth + require_household_access ---

type provHandler func(w http.ResponseWriter, r *http.Request, a provAuth)

// prov wraps a route in verify_provisioning_auth (admin key or user JWT).
func (m *Module) prov(h provHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a, ok := m.authProvisioning(w, r); ok {
			h(w, r, a)
		}
	}
}

// householdAccess is require_household_access: admin-key callers bypass; a JWT caller must
// be a member of hh among all of their memberships (D5); an empty household is 403.
func (m *Module) householdAccess(ctx context.Context, a provAuth, hh string) error {
	if a.admin {
		return nil
	}
	if hh == "" {
		return fail(http.StatusForbidden, "Not authorized")
	}
	return m.requireRole(ctx, a.user.ID, hh, authn.RoleMember)
}

// nodeAccess loads a node (404) and checks the caller against its household.
func (m *Module) nodeAccess(ctx context.Context, a provAuth, nodeID string) (*nodeRow, error) {
	n, err := m.nodeByID(ctx, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fail(http.StatusNotFound, "Node not found")
	}
	if err != nil {
		return nil, err
	}
	if err := m.householdAccess(ctx, a, n.householdID.String); err != nil {
		return nil, err
	}
	return n, nil
}

// --- household nodes ---

func (m *Module) householdNodes(ctx context.Context, hh string) ([]*nodeRow, error) {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT `+nodeCols+` FROM cc_nodes
		WHERE household_id = ? AND is_active = 1 ORDER BY rowid`, hh)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*nodeRow
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// pickNode chooses the node that drives a device (M9, one policy for control, state and
// device_removed): nodes reporting the protocol (all active nodes when none does, for old
// nodes with no protocols), online only; the household's primary node first, else the most
// recent last_seen. No node at all is legacy's 400; no online node fails fast with 503
// instead of a 10 s timeout.
func (m *Module) pickNode(ctx context.Context, hh, protocol string) (*nodeRow, error) {
	nodes, err := m.householdNodes(ctx, hh)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fail(http.StatusBadRequest, "No active node available in household")
	}
	candidates := nodes
	if protocol != "" {
		var matching []*nodeRow
		for _, n := range nodes {
			var ps []string
			if n.protocols.Valid && json.Unmarshal([]byte(n.protocols.String), &ps) == nil && slices.Contains(ps, protocol) {
				matching = append(matching, n)
			}
		}
		if len(matching) > 0 {
			candidates = matching
		}
	}
	now := m.now()
	var online []*nodeRow
	for _, n := range candidates {
		if n.online(now) {
			online = append(online, n)
		}
	}
	if len(online) == 0 {
		if protocol != "" {
			return nil, fail(http.StatusServiceUnavailable, fmt.Sprintf("No online node with protocol '%s'", protocol))
		}
		return nil, fail(http.StatusServiceUnavailable, "No online node available in household")
	}
	if primary := m.settings.String(ctx, settingPrimaryNode, settings.Scope{HouseholdID: hh}); primary != "" {
		for _, n := range online {
			if n.nodeID == primary {
				return n, nil
			}
		}
	}
	best := online[0]
	for _, n := range online[1:] {
		if parseTS(n.lastSeen.String).After(parseTS(best.lastSeen.String)) {
			best = n
		}
	}
	return best, nil
}

// broadcastCommand publishes a fire-and-forget command to every active household node.
func (m *Module) broadcastCommand(ctx context.Context, hh, verb string, details map[string]any) int {
	nodes, err := m.householdNodes(ctx, hh)
	if err != nil {
		m.deps.Log.Warn("cc: household nodes lookup failed", "household", hh, "verb", verb, "err", err)
		return 0
	}
	for _, n := range nodes {
		m.bus.Command(n.nodeID, verb, details)
	}
	return len(nodes)
}

// invalidateDeviceCache tells every household node to drop its DirectDeviceService cache,
// after every registry write (§7.12). Best effort: the node's 5-minute refresh catches up.
func (m *Module) invalidateDeviceCache(ctx context.Context, hh string) {
	m.broadcastCommand(ctx, hh, "invalidate_device_cache", map[string]any{})
}

// --- smart-home config ---

func (m *Module) smartHomeConfig(ctx context.Context, hh string) map[string]any {
	sc := settings.Scope{HouseholdID: hh}
	dm := m.settings.String(ctx, settingDeviceManager, sc)
	if dm == "" {
		dm = "jarvis_direct"
	}
	return map[string]any{
		"device_manager":       dm,
		"primary_node_id":      m.settings.String(ctx, settingPrimaryNode, sc),
		"use_external_devices": m.settings.Bool(ctx, settingUseExternalDevices, sc),
	}
}

func (m *Module) handleGetSmartHomeConfig(w http.ResponseWriter, r *http.Request, a provAuth) {
	ctx := r.Context()
	hh := r.PathValue("household_id")
	if err := m.householdAccess(ctx, a, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	nodes, err := m.householdNodes(ctx, hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	now := m.now()
	opts := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		opts = append(opts, map[string]any{"node_id": n.nodeID, "room": n.room, "online": n.online(now), "last_seen": naiveTS(n.lastSeen.String)})
	}
	out := m.smartHomeConfig(ctx, hh)
	out["nodes"] = opts
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) handlePutSmartHomeConfig(w http.ResponseWriter, r *http.Request, a provAuth) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	dm, hasDM := b.str("device_manager", false)
	primary, hasPrimary := b.str("primary_node_id", false)
	external, hasExternal := b.boolean("use_external_devices")
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	hh := r.PathValue("household_id")
	if err := m.householdAccess(ctx, a, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	sc := settings.Scope{HouseholdID: hh}
	if hasDM {
		if err := m.settings.Set(ctx, settingDeviceManager, dm, sc); err != nil {
			m.internalError(w, err)
			return
		}
	}
	if hasPrimary {
		if primary != "" {
			var n int
			if err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT COUNT(*) FROM cc_nodes WHERE node_id = ? AND household_id = ?`,
				primary, hh).Scan(&n); err != nil {
				m.internalError(w, err)
				return
			}
			if n == 0 {
				detail(w, http.StatusBadRequest, "Node not in this household")
				return
			}
		}
		if err := m.settings.Set(ctx, settingPrimaryNode, primary, sc); err != nil {
			m.internalError(w, err)
			return
		}
	}
	if hasExternal {
		if err := m.settings.Set(ctx, settingUseExternalDevices, external, sc); err != nil {
			m.internalError(w, err)
			return
		}
		// external → the Pantry device manager package provides control_device, so the
		// built-in one is disabled on every node; and back.
		n := m.broadcastCommand(ctx, hh, "toggle_command", map[string]any{"command_name": "control_device", "enabled": !external})
		m.deps.Log.Info("cc: toggled built-in control_device", "household", hh, "nodes", n, "enabled", !external)
	}
	httpx.WriteJSON(w, http.StatusOK, m.smartHomeConfig(ctx, hh))
}

// --- retention (D27/D40 Q10), run by the hourly cc.cleanup job ---

const (
	jobRetention          = time.Hour      // request rows, past their expiry
	consumedPushRetention = 24 * time.Hour // consumed non-auth config pushes
	authSessionRetention  = time.Hour      // consumed / expired OAuth sessions
)

func (m *Module) cleanupSmartHome(ctx context.Context, now time.Time) error {
	jobCut := dbTime(now.Add(-jobRetention))
	for _, s := range []struct {
		q    string
		args []any
	}{
		{`DELETE FROM cc_device_scan_requests WHERE expires_at < ?`, []any{jobCut}},
		{`DELETE FROM cc_device_list_requests WHERE expires_at < ?`, []any{jobCut}},
		{`DELETE FROM cc_bluetooth_pair_requests WHERE expires_at < ?`, []any{jobCut}},
		// Bluetooth status reads the newest completed scan per node: keep it.
		{`DELETE FROM cc_bluetooth_scan_requests WHERE expires_at < ? AND id NOT IN (
			SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY node_id ORDER BY completed_at DESC, rowid DESC) AS rn
			FROM cc_bluetooth_scan_requests WHERE status = 'completed') WHERE rn = 1)`, []any{jobCut}},
		{`DELETE FROM cc_config_pushes WHERE (status = 'consumed' AND consumed_at < ?)
			OR (expires_at IS NOT NULL AND expires_at < ?)`, []any{dbTime(now.Add(-consumedPushRetention)), dbTime(now)}},
		{`DELETE FROM cc_auth_sessions WHERE (status IN ('consumed', 'expired') AND COALESCE(completed_at, created_at) < ?)
			OR (status = 'pending' AND expires_at < ?)`, []any{dbTime(now.Add(-authSessionRetention)), dbTime(now.Add(-authSessionRetention))}},
	} {
		if _, err := m.deps.DB.Write.ExecContext(ctx, s.q, s.args...); err != nil {
			return err
		}
	}
	return nil
}

// --- cameras (D29: deferred) ---

func (m *Module) handleListCameras(w http.ResponseWriter, r *http.Request, a provAuth) {
	ctx := r.Context()
	hh := r.PathValue("household_id")
	if err := m.householdAccess(ctx, a, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	devs, err := m.queryDevices(ctx, `d.household_id = ? AND d.domain = 'camera' AND d.is_active = 1`, hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(devs))
	for _, d := range devs {
		out = append(out, map[string]any{
			"device_id": d.id, "entity_id": d.entityID, "name": d.name, "protocol": nullable(d.protocol),
			"cloud_id": nullable(d.cloudID), "room_name": nullable(d.roomName), "is_streaming": false,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// errCamerasDeferred is the "not available" answer for every streaming route (D29).
const errCamerasDeferred = "Camera streaming is not available in this version"

func (m *Module) handleStartCameraStream(w http.ResponseWriter, r *http.Request, a provAuth) {
	ctx := r.Context()
	hh := r.PathValue("household_id")
	if err := m.householdAccess(ctx, a, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	var n int
	if err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT COUNT(*) FROM cc_devices WHERE id = ? AND household_id = ? AND domain = 'camera'`,
		r.PathValue("device_id"), hh).Scan(&n); err != nil {
		m.internalError(w, err)
		return
	}
	if n == 0 {
		detail(w, http.StatusNotFound, "Camera not found")
		return
	}
	detail(w, http.StatusNotImplemented, errCamerasDeferred)
}

func (m *Module) handleStopCameraStream(w http.ResponseWriter, r *http.Request, a provAuth) {
	if err := m.householdAccess(r.Context(), a, r.PathValue("household_id")); err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "not_streaming"})
}

func (m *Module) handleCameraProxy(w http.ResponseWriter, _ *http.Request, _ provAuth) {
	detail(w, http.StatusNotFound, "Stream not found")
}
