package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// The request/poll jobs (smart_home.py:1473-1963, bluetooth.py): mobile POSTs a request, CC
// stores a pending row (2-minute deadline) and nudges the node over MQTT; the node uploads
// results; mobile polls. D4: uploads are bound to the authenticated node, and every poll
// checks the caller's household (legacy skipped it on the Bluetooth polls).

const jobTTL = 2 * time.Minute

type jobKind struct {
	table string
	noun  string // "Scan request" -> "Scan request not found" / "Scan request expired"
	topic string
}

var (
	scanJob   = jobKind{"cc_device_scan_requests", "Scan request", "device-scan"}
	listJob   = jobKind{"cc_device_list_requests", "Device list request", "device-list"}
	btScanJob = jobKind{"cc_bluetooth_scan_requests", "Scan request", "bluetooth-scan"}
	btPairJob = jobKind{"cc_bluetooth_pair_requests", "Pair request", "bluetooth-pair"}
)

// publishNode is a best-effort nudge: with MQTT down the row stays pending until it expires.
func (m *Module) publishNode(nodeID, sub string, v any) {
	if err := m.bus.Publish(nodeID, sub, v); err != nil {
		m.deps.Log.Warn("cc: node request not published", "node", nodeID, "topic", sub, "err", err)
	}
}

// handleRequestJob creates a job row for the node and publishes its nudge.
func (m *Module) handleRequestJob(k jobKind) provHandler {
	return func(w http.ResponseWriter, r *http.Request, a provAuth) {
		var b *body
		var ok bool
		switch k {
		case btScanJob:
			b, _, ok = readBody(w, r, true)
		case btPairJob:
			b, _, ok = readBody(w, r, false)
		default:
			b, ok = &body{m: map[string]any{}, loc: "body", errs: &[]string{}}, true
		}
		if !ok {
			return
		}
		role, source := "source", "mobile"
		if s, ok := b.str("role", false); ok {
			role = s
		}
		var mac string
		var userID any
		switch k {
		case btScanJob:
			if s, ok := b.str("source", false); ok {
				source = s
			}
			if id, ok := b.integer("user_id", false); ok {
				userID = id
			}
		case btPairJob:
			mac, _ = b.str("mac_address", true)
		}
		if !b.done(w) {
			return
		}
		ctx := r.Context()
		nodeID := r.PathValue("node_id")
		node, err := m.nodeAccess(ctx, a, nodeID)
		if err != nil {
			m.writeErr(w, err)
			return
		}
		now := m.now()
		id, created, expires := uuid4(), dbTime(now), dbTime(now.Add(jobTTL))
		hh := node.householdID.String
		var nudge map[string]any
		switch k {
		case scanJob:
			_, err = m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_device_scan_requests (id, node_id, household_id, status,
				created_at, expires_at) VALUES (?, ?, ?, 'pending', ?, ?)`, id, nodeID, hh, created, expires)
			nudge = map[string]any{"request_id": id}
		case listJob:
			// Every enabled manager on the node is aggregated (device_manager is not consulted).
			_, err = m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_device_list_requests (id, node_id, household_id, status,
				manager_name, created_at, expires_at) VALUES (?, ?, ?, 'pending', 'all', ?, ?)`, id, nodeID, hh, created, expires)
			nudge = map[string]any{"request_id": id, "manager_name": "all"}
		case btScanJob:
			// source/user_id are kept on the row; the voice deep-link push is cut (M7).
			_, err = m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_bluetooth_scan_requests (id, node_id, household_id, status,
				role, source, user_id, created_at, expires_at) VALUES (?, ?, ?, 'pending', ?, ?, ?, ?, ?)`,
				id, nodeID, hh, role, source, userID, created, expires)
			nudge = map[string]any{"request_id": id, "role": role}
		case btPairJob:
			_, err = m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_bluetooth_pair_requests (id, node_id, household_id, mac_address,
				role, status, created_at, expires_at) VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)`, id, nodeID, hh, mac, role, created, expires)
			nudge = map[string]any{"request_id": id, "mac_address": mac, "role": role}
		}
		if err != nil {
			m.internalError(w, err)
			return
		}
		m.publishNode(nodeID, k.topic, nudge)
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{"id": id, "status": "pending", "created_at": pyNaive(parseTS(created))})
	}
}

// uploadJob records a node's result: 404 unknown, 410 (and status=expired) past the deadline.
func (m *Module) uploadJob(ctx context.Context, k jobKind, rid, nodeID string, sets []string, args []any) error {
	var expires string
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT expires_at FROM `+k.table+` WHERE id = ? AND node_id = ?`, rid, nodeID).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return fail(http.StatusNotFound, k.noun+" not found")
	}
	if err != nil {
		return err
	}
	now := m.now()
	if parseTS(expires).Before(now) {
		if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE `+k.table+` SET status = 'expired' WHERE id = ?`, rid); err != nil {
			return err
		}
		return fail(http.StatusGone, k.noun+" expired")
	}
	sets = append(sets, "completed_at = ?")
	args = append(args, dbTime(now), rid)
	_, err = m.deps.DB.Write.ExecContext(ctx, `UPDATE `+k.table+` SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	return err
}

// resultDevices reads the upload's devices: a list of objects (required unless optional).
func resultDevices(b *body, required bool) []any {
	v, present := b.m["devices"]
	if !present || v == nil {
		if required {
			b.fail("devices", "Field required")
		}
		return []any{}
	}
	list, ok := v.([]any)
	if !ok {
		b.fail("devices", "Input should be a valid list")
		return nil
	}
	for i, e := range list {
		if _, ok := e.(map[string]any); !ok {
			*b.errs = append(*b.errs, "body -> devices -> "+strconv.Itoa(i)+": Input should be a valid dictionary")
		}
	}
	return list
}

// uploadNode rejects a result posted for another node's path (D4).
func uploadNode(w http.ResponseWriter, r *http.Request, n *nodeCtx) (string, bool) {
	id := r.PathValue("node_id")
	if n.ID != id {
		detail(w, http.StatusForbidden, "Cannot upload to other node's requests")
		return "", false
	}
	return id, true
}

// handleUploadDeviceJob is the device-scan and device-list result upload.
func (m *Module) handleUploadDeviceJob(k jobKind) nodeHandler {
	return func(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
		nodeID, ok := uploadNode(w, r, n)
		if !ok {
			return
		}
		b, _, ok := readBody(w, r, false)
		if !ok {
			return
		}
		devices := resultDevices(b, true)
		errMsg, _ := b.str("error", false)
		manager, _ := b.str("manager_name", false)
		canEdit, hasCanEdit := b.boolean("can_edit_devices")
		if !b.done(w) {
			return
		}
		var sets []string
		var args []any
		if errMsg != "" {
			sets, args = append(sets, "status = 'failed'", "error_message = ?"), append(args, errMsg)
		} else {
			raw, _ := json.Marshal(devices)
			sets, args = append(sets, "status = 'completed'", "results_json = ?", "device_count = ?"), append(args, string(raw), len(devices))
		}
		if k == listJob {
			if manager != "" {
				sets, args = append(sets, "manager_name = ?"), append(args, manager)
			}
			if hasCanEdit {
				sets, args = append(sets, "can_edit_devices = ?"), append(args, canEdit)
			}
		}
		if err := m.uploadJob(r.Context(), k, r.PathValue("request_id"), nodeID, sets, args); err != nil {
			m.writeErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	}
}

func (m *Module) handleUploadBTScan(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	nodeID, ok := uploadNode(w, r, n)
	if !ok {
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	devices := resultDevices(b, false)
	errMsg, _ := b.str("error", false)
	if !b.done(w) {
		return
	}
	sets, args := []string{"status = 'failed'", "error_message = ?"}, []any{errMsg}
	if errMsg == "" {
		raw, _ := json.Marshal(devices)
		sets, args = []string{"status = 'completed'", "results_json = ?", "device_count = ?"}, []any{string(raw), len(devices)}
	}
	if err := m.uploadJob(r.Context(), btScanJob, r.PathValue("request_id"), nodeID, sets, args); err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (m *Module) handleUploadBTPair(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	nodeID, ok := uploadNode(w, r, n)
	if !ok {
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	success, hasSuccess := b.boolean("success")
	if !hasSuccess && !b.has("success") {
		b.fail("success", "Field required")
	}
	name, hasName := b.str("device_name", false)
	errMsg, hasErr := b.str("error", false)
	if !b.done(w) {
		return
	}
	opt := func(s string, has bool) any {
		if has {
			return s
		}
		return nil
	}
	sets, args := []string{"status = 'failed'", "error_message = ?"}, []any{opt(errMsg, hasErr)}
	if success {
		sets, args = []string{"status = 'completed'", "device_name = ?"}, []any{opt(name, hasName)}
	}
	if err := m.uploadJob(r.Context(), btPairJob, r.PathValue("request_id"), nodeID, sets, args); err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// jobState is the shared poll prelude: 404, household check (D4/D5), lazy expiry → 410.
// It returns the status and the row's household.
func (m *Module) jobState(ctx context.Context, a provAuth, k jobKind, rid, nodeID string) (string, string, error) {
	var status, expires, hh string
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT status, expires_at, household_id FROM `+k.table+` WHERE id = ? AND node_id = ?`,
		rid, nodeID).Scan(&status, &expires, &hh)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fail(http.StatusNotFound, k.noun+" not found")
	}
	if err != nil {
		return "", "", err
	}
	if err := m.householdAccess(ctx, a, hh); err != nil {
		return "", "", err
	}
	if status == "pending" && parseTS(expires).Before(m.now()) {
		if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE `+k.table+` SET status = 'expired' WHERE id = ?`, rid); err != nil {
			return "", "", err
		}
		status = "expired"
	}
	if status == "expired" {
		return "", "", fail(http.StatusGone, k.noun+" expired")
	}
	return status, hh, nil
}

// --- raw device helpers (dev.get(k, default) over a node-supplied dict) ---

func getStr(d map[string]any, k, def string) string {
	if s, ok := d[k].(string); ok {
		return s
	}
	return def
}

func getBool(d map[string]any, k string, def bool) bool {
	if v, ok := d[k]; ok {
		if b, ok := laxBool(v); ok {
			return b
		}
	}
	return def
}

func decodeResults(raw sql.NullString) []map[string]any {
	var list []any
	if raw.Valid {
		_ = json.Unmarshal([]byte(raw.String), &list)
	}
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		if o, ok := e.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out
}

// registryMatcher matches raw node devices against the household's active registry:
// entity_id, then cloud_id, then lowercased MAC (§7.8).
type registryMatcher struct {
	entity, cloud, mac map[string]*deviceRow
}

func (m *Module) newRegistryMatcher(ctx context.Context, hh string) (*registryMatcher, error) {
	devs, err := m.queryDevices(ctx, `d.household_id = ? AND d.is_active = 1`, hh)
	if err != nil {
		return nil, err
	}
	rm := &registryMatcher{entity: map[string]*deviceRow{}, cloud: map[string]*deviceRow{}, mac: map[string]*deviceRow{}}
	for _, d := range devs {
		rm.entity[d.entityID] = d
		if d.cloudID.String != "" {
			rm.cloud[d.cloudID.String] = d
		}
		if d.mac.String != "" {
			rm.mac[strings.ToLower(d.mac.String)] = d
		}
	}
	return rm, nil
}

func (rm *registryMatcher) match(dev map[string]any) *deviceRow {
	if d, ok := rm.entity[getStr(dev, "entity_id", "")]; ok {
		return d
	}
	if c := getStr(dev, "cloud_id", ""); c != "" {
		if d, ok := rm.cloud[c]; ok {
			return d
		}
	}
	if mac := getStr(dev, "mac_address", ""); mac != "" {
		if d, ok := rm.mac[strings.ToLower(mac)]; ok {
			return d
		}
	}
	return nil
}

// --- polls ---

func (m *Module) handlePollDeviceScan(w http.ResponseWriter, r *http.Request, a provAuth) {
	ctx := r.Context()
	rid := r.PathValue("request_id")
	status, hh, err := m.jobState(ctx, a, scanJob, rid, r.PathValue("node_id"))
	if err != nil {
		m.writeErr(w, err)
		return
	}
	out := map[string]any{"status": status, "request_id": rid, "devices": nil, "device_count": nil, "error_message": nil}
	var results, errMsg sql.NullString
	if err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT results_json, error_message FROM cc_device_scan_requests WHERE id = ?`, rid).
		Scan(&results, &errMsg); err != nil {
		m.internalError(w, err)
		return
	}
	switch status {
	case "pending":
	case "failed":
		out["error_message"] = nullable(errMsg)
	default:
		rm, err := m.newRegistryMatcher(ctx, hh)
		if err != nil {
			m.internalError(w, err)
			return
		}
		devices := []map[string]any{}
		for _, dev := range decodeResults(results) {
			var existing any
			if d := rm.match(dev); d != nil {
				existing = d.id
			}
			var actions any
			if l, ok := dev["supported_actions"].([]any); ok {
				actions = l
			}
			devices = append(devices, map[string]any{
				"name": getStr(dev, "name", "Unknown"), "domain": getStr(dev, "domain", "unknown"),
				"manufacturer": strOrNil(dev["manufacturer"]), "model": strOrNil(dev["model"]), "protocol": strOrNil(dev["protocol"]),
				"entity_id": getStr(dev, "entity_id", ""), "local_ip": strOrNil(dev["local_ip"]),
				"mac_address": strOrNil(dev["mac_address"]), "cloud_id": strOrNil(dev["cloud_id"]),
				"device_class": strOrNil(dev["device_class"]), "is_controllable": getBool(dev, "is_controllable", true),
				"already_registered": existing != nil, "existing_device_id": existing, "supported_actions": actions,
			})
		}
		out["status"], out["devices"], out["device_count"] = "completed", devices, len(devices)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) handlePollDeviceList(w http.ResponseWriter, r *http.Request, a provAuth) {
	ctx := r.Context()
	rid := r.PathValue("request_id")
	status, hh, err := m.jobState(ctx, a, listJob, rid, r.PathValue("node_id"))
	if err != nil {
		m.writeErr(w, err)
		return
	}
	var results, errMsg, manager sql.NullString
	var canEdit sql.NullBool
	if err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT results_json, error_message, manager_name, can_edit_devices
		FROM cc_device_list_requests WHERE id = ?`, rid).Scan(&results, &errMsg, &manager, &canEdit); err != nil {
		m.internalError(w, err)
		return
	}
	out := map[string]any{"status": status, "request_id": rid, "manager_name": nullable(manager), "can_edit_devices": nil,
		"devices": nil, "device_count": nil, "error_message": nil}
	switch status {
	case "pending":
	case "failed":
		out["error_message"] = nullable(errMsg)
	default:
		rm, err := m.newRegistryMatcher(ctx, hh)
		if err != nil {
			m.internalError(w, err)
			return
		}
		devices := []map[string]any{}
		for _, dev := range decodeResults(results) {
			var existing, roomID, roomName any
			if d := rm.match(dev); d != nil {
				existing = d.id
				if d.roomID.Valid && d.roomName.Valid {
					roomID, roomName = d.roomID.String, d.roomName.String
				}
			}
			domain, protocol := getStr(dev, "domain", "unknown"), strOrNil(dev["protocol"])
			ps, _ := protocol.(string)
			controllable := getBool(dev, "is_controllable", true)
			devices = append(devices, map[string]any{
				"name": getStr(dev, "name", "Unknown"), "domain": domain, "entity_id": getStr(dev, "entity_id", ""),
				"is_controllable": controllable, "manufacturer": strOrNil(dev["manufacturer"]), "model": strOrNil(dev["model"]),
				"protocol": protocol, "local_ip": strOrNil(dev["local_ip"]), "mac_address": strOrNil(dev["mac_address"]),
				"cloud_id": strOrNil(dev["cloud_id"]), "device_class": strOrNil(dev["device_class"]),
				"source": getStr(dev, "source", "direct"), "area": strOrNil(dev["area"]), "state": strOrNil(dev["state"]),
				"already_registered": existing != nil, "existing_device_id": existing, "room_id": roomID, "room_name": roomName,
				// Recomputed by CC, not the node's (§3.3).
				"supported_actions": supportedActions(domain, ps, controllable),
			})
		}
		if canEdit.Valid {
			out["can_edit_devices"] = canEdit.Bool
		}
		out["status"], out["devices"], out["device_count"] = "completed", devices, len(devices)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func btDevice(d map[string]any, paired, connected bool) map[string]any {
	return map[string]any{
		"name": getStr(d, "name", "Unknown"), "mac_address": getStr(d, "mac_address", ""),
		"device_type": getStr(d, "device_type", "unknown"), "paired": paired, "connected": connected,
	}
}

func (m *Module) handlePollBTScan(w http.ResponseWriter, r *http.Request, a provAuth) {
	ctx := r.Context()
	rid := r.PathValue("request_id")
	status, _, err := m.jobState(ctx, a, btScanJob, rid, r.PathValue("node_id"))
	if err != nil {
		m.writeErr(w, err)
		return
	}
	var results, errMsg sql.NullString
	if err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT results_json, error_message FROM cc_bluetooth_scan_requests WHERE id = ?`, rid).
		Scan(&results, &errMsg); err != nil {
		m.internalError(w, err)
		return
	}
	out := map[string]any{"status": status, "request_id": rid, "devices": nil, "device_count": nil, "error_message": nil}
	switch status {
	case "pending":
	case "failed":
		out["error_message"] = nullable(errMsg)
	default:
		devices := []map[string]any{}
		for _, d := range decodeResults(results) {
			devices = append(devices, btDevice(d, getBool(d, "paired", false), getBool(d, "connected", false)))
		}
		out["status"], out["devices"], out["device_count"] = "completed", devices, len(devices)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) handlePollBTPair(w http.ResponseWriter, r *http.Request, a provAuth) {
	ctx := r.Context()
	rid := r.PathValue("request_id")
	status, _, err := m.jobState(ctx, a, btPairJob, rid, r.PathValue("node_id"))
	if err != nil {
		m.writeErr(w, err)
		return
	}
	var name, errMsg sql.NullString
	if err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT device_name, error_message FROM cc_bluetooth_pair_requests WHERE id = ?`, rid).
		Scan(&name, &errMsg); err != nil {
		m.internalError(w, err)
		return
	}
	out := map[string]any{"status": status, "request_id": rid, "device_name": nil, "error_message": nil}
	switch status {
	case "pending":
	case "failed":
		out["error_message"] = nullable(errMsg)
	default:
		out["status"], out["device_name"] = "completed", nullable(name)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleBTFire is the fire-and-forget Bluetooth commands (202): disconnect, discoverable,
// and the two routes mobile already calls that legacy never had (D8, Q4): release and
// auto-connect, whose topics the node already handles.
func (m *Module) handleBTFire(topic string) provHandler {
	return func(w http.ResponseWriter, r *http.Request, a provAuth) {
		var payload map[string]any
		resp := map[string]any{"status": "accepted"}
		if topic == "bluetooth-discoverable" {
			payload = map[string]any{"timeout": 120}
			resp["timeout_seconds"] = 120
		} else {
			b, _, ok := readBody(w, r, false)
			if !ok {
				return
			}
			mac, _ := b.str("mac_address", true)
			payload = map[string]any{"mac_address": mac}
			switch topic {
			case "bluetooth-release":
				forget, _ := b.boolean("forget")
				payload["forget"] = forget
			case "bluetooth-auto-connect":
				enabled, has := b.boolean("enabled")
				if !has && !b.has("enabled") {
					b.fail("enabled", "Field required")
				}
				payload["enabled"] = enabled
			}
			if !b.done(w) {
				return
			}
		}
		nodeID := r.PathValue("node_id")
		if _, err := m.nodeAccess(r.Context(), a, nodeID); err != nil {
			m.writeErr(w, err)
			return
		}
		m.publishNode(nodeID, topic, payload)
		httpx.WriteJSON(w, http.StatusAccepted, resp)
	}
}

// handleBTStatus is not live: it reinterprets the most recent completed scan.
func (m *Module) handleBTStatus(w http.ResponseWriter, r *http.Request, a provAuth) {
	ctx := r.Context()
	nodeID := r.PathValue("node_id")
	if _, err := m.nodeAccess(ctx, a, nodeID); err != nil {
		m.writeErr(w, err)
		return
	}
	out := map[string]any{"available": true, "connected": []map[string]any{}, "paired": []map[string]any{}}
	var results sql.NullString
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT results_json FROM cc_bluetooth_scan_requests
		WHERE node_id = ? AND status = 'completed' ORDER BY completed_at DESC, rowid DESC LIMIT 1`, nodeID).Scan(&results)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		m.internalError(w, err)
		return
	}
	connected, paired := []map[string]any{}, []map[string]any{}
	for _, d := range decodeResults(results) {
		c, p := getBool(d, "connected", false), getBool(d, "paired", false)
		switch {
		case c:
			connected = append(connected, btDevice(d, true, true))
		case p:
			paired = append(paired, btDevice(d, true, false))
		}
	}
	out["connected"], out["paired"] = connected, paired
	httpx.WriteJSON(w, http.StatusOK, out)
}
