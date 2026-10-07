package cc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// The device registry (smart_home.py:688-1052): import (upsert by entity_id), list, PATCH,
// DELETE, the node's /node/devices, and the supported-actions tables.

// sensorDomains are the non-controllable HA domains (_SENSOR_DOMAINS).
var sensorDomains = map[string]bool{
	"sensor": true, "binary_sensor": true, "weather": true, "sun": true, "zone": true, "person": true, "device_tracker": true,
}

// button is a JarvisButtonResponse.
type button struct{ text, action, typ, icon string }

func (b button) json() map[string]any {
	return map[string]any{"button_text": b.text, "button_action": b.action, "button_type": b.typ, "button_icon": b.icon}
}

// protocolActions mirrors each node adapter's DeviceProtocol.supported_actions
// (_PROTOCOL_ACTIONS); protocol beats domain.
var protocolActions = map[string][]button{
	"lifx": {
		{"Turn On", "turn_on", "primary", "lightbulb-on"},
		{"Turn Off", "turn_off", "secondary", "lightbulb-off"},
		{"Toggle", "toggle", "secondary", "lightbulb-outline"},
	},
	"kasa": {
		{"Turn On", "turn_on", "primary", "power"},
		{"Turn Off", "turn_off", "secondary", "power-off"},
		{"Toggle", "toggle", "secondary", "toggle-switch"},
	},
	"govee": {
		{"Turn On", "turn_on", "primary", "power"},
		{"Turn Off", "turn_off", "secondary", "power-off"},
	},
	"nest": {
		{"Set Temperature", "set_temperature", "primary", "thermometer"},
		{"Set Mode", "set_mode", "secondary", "thermostat"},
		{"Turn Off", "turn_off", "secondary", "power-off"},
	},
	"apple": {
		{"Pair", "pair_start", "primary", "link"},
		{"Play", "play", "primary", "play"},
		{"Pause", "pause", "secondary", "pause"},
		{"Power On", "turn_on", "primary", "power"},
		{"Power Off", "turn_off", "destructive", "power-off"},
		{"Vol Up", "volume_up", "secondary", "volume-plus"},
		{"Vol Down", "volume_down", "secondary", "volume-minus"},
	},
	"homeconnect": {
		{"Auto Wash", "start_auto", "primary", "dishwasher"},
		{"Eco 50°", "start_eco", "primary", "leaf"},
		{"Quick 65°", "start_quick", "secondary", "lightning-bolt"},
		{"Glass 40°", "start_glass", "secondary", "glass-fragile"},
		{"Pre-Rinse", "start_prerinse", "secondary", "water"},
		{"Stop", "stop", "destructive", "stop"},
	},
}

// domainActions is the fallback for devices without a known protocol (_DOMAIN_ACTIONS).
var domainActions = map[string][]button{
	"light": {
		{"Turn On", "turn_on", "primary", "lightbulb-on"},
		{"Turn Off", "turn_off", "secondary", "lightbulb-off"},
	},
	"switch": {
		{"Turn On", "turn_on", "primary", "power"},
		{"Turn Off", "turn_off", "secondary", "power-off"},
	},
	"lock": {
		{"Lock", "lock", "primary", "lock"},
		{"Unlock", "unlock", "destructive", "lock-open"},
	},
	"climate": {
		{"Set Temperature", "set_temperature", "primary", "thermometer"},
		{"Set Mode", "set_mode", "secondary", "thermostat"},
		{"Turn Off", "turn_off", "secondary", "power-off"},
	},
	"camera": {
		{"Get Stream", "get_stream", "primary", "video"},
	},
	"kettle": {
		{"Boil", "turn_on", "primary", "kettle"},
		{"Set Temperature", "set_temperature", "secondary", "thermometer"},
		{"Turn Off", "turn_off", "secondary", "power-off"},
	},
	"fan": {
		{"Turn On", "turn_on", "primary", "fan"},
		{"Turn Off", "turn_off", "secondary", "fan-off"},
	},
	"cover": {
		{"Open", "open_cover", "primary", "blinds-open"},
		{"Close", "close_cover", "secondary", "blinds"},
	},
	"media_player": {
		{"Play", "media_play", "primary", "play"},
		{"Pause", "media_pause", "secondary", "pause"},
	},
}

var genericActions = []button{
	{"Turn On", "turn_on", "primary", "power"},
	{"Turn Off", "turn_off", "secondary", "power-off"},
}

// supportedActions is _get_actions_for_raw: nil (JSON null) for a non-controllable device.
func supportedActions(domain, protocol string, controllable bool) []map[string]any {
	if !controllable {
		return nil
	}
	bs, ok := protocolActions[protocol]
	if !ok || protocol == "" {
		if bs, ok = domainActions[domain]; !ok {
			bs = genericActions
		}
	}
	out := make([]map[string]any, len(bs))
	for i, b := range bs {
		out[i] = b.json()
	}
	return out
}

// --- rows ---

type deviceRow struct {
	id, householdID, entityID, name, domain                         string
	roomID, deviceClass, manufacturer, model, source, haDeviceID    sql.NullString
	protocol, localIP, mac, cloudID, createdAt, updatedAt, roomName sql.NullString
	isControllable, isActive                                        sql.NullBool
}

const deviceCols = `d.id, d.household_id, d.entity_id, d.name, d.domain, d.room_id, d.device_class, d.manufacturer,
	d.model, d.source, d.ha_device_id, d.protocol, d.local_ip, d.mac_address, d.cloud_id, d.created_at, d.updated_at,
	r.name, d.is_controllable, d.is_active`

func (m *Module) queryDevices(ctx context.Context, where string, args ...any) ([]*deviceRow, error) {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT `+deviceCols+` FROM cc_devices d
		LEFT JOIN cc_rooms r ON r.id = d.room_id WHERE `+where+` ORDER BY d.rowid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*deviceRow
	for rows.Next() {
		var d deviceRow
		if err := rows.Scan(&d.id, &d.householdID, &d.entityID, &d.name, &d.domain, &d.roomID, &d.deviceClass,
			&d.manufacturer, &d.model, &d.source, &d.haDeviceID, &d.protocol, &d.localIP, &d.mac, &d.cloudID,
			&d.createdAt, &d.updatedAt, &d.roomName, &d.isControllable, &d.isActive); err != nil {
			return nil, err
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

func (m *Module) device(ctx context.Context, hh, id string) (*deviceRow, error) {
	devs, err := m.queryDevices(ctx, `d.id = ? AND d.household_id = ?`, id, hh)
	if err != nil {
		return nil, err
	}
	if len(devs) == 0 {
		return nil, fail(http.StatusNotFound, "Device not found")
	}
	return devs[0], nil
}

// controllable / active: the columns default to true (a NULL reads as the default).
func (d *deviceRow) controllable() bool { return !d.isControllable.Valid || d.isControllable.Bool }
func (d *deviceRow) active() bool       { return !d.isActive.Valid || d.isActive.Bool }

func (d *deviceRow) sourceOr() string {
	if d.source.Valid {
		return d.source.String
	}
	return "home_assistant"
}

// response is DeviceResponse.
func (d *deviceRow) response() map[string]any {
	return map[string]any{
		"id": d.id, "household_id": d.householdID, "room_id": nullable(d.roomID), "entity_id": d.entityID,
		"name": d.name, "domain": d.domain, "device_class": nullable(d.deviceClass), "manufacturer": nullable(d.manufacturer),
		"model": nullable(d.model), "source": d.sourceOr(), "protocol": nullable(d.protocol), "local_ip": nullable(d.localIP),
		"mac_address": nullable(d.mac), "cloud_id": nullable(d.cloudID), "ha_device_id": nullable(d.haDeviceID),
		"is_controllable": d.controllable(), "is_active": d.active(), "room_name": nullable(d.roomName),
		"created_at": naiveTS(d.createdAt.String), "updated_at": naiveTS(d.updatedAt.String),
		"supported_actions": supportedActions(d.domain, d.protocol.String, d.controllable()),
	}
}

// --- GET /households/{hh}/devices ---

func (m *Module) handleListDevices(w http.ResponseWriter, r *http.Request, a provAuth) {
	recursive, ok := queryBool(w, r, "recursive", false)
	if !ok {
		return
	}
	ctx := r.Context()
	hh := r.PathValue("household_id")
	if err := m.householdAccess(ctx, a, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	q := r.URL.Query()
	where, args := []string{"d.household_id = ?"}, []any{hh}
	if room := q.Get("room_id"); room != "" {
		ids := []string{room}
		if recursive {
			var err error
			if ids, err = descendantRooms(ctx, m.deps.DB.Read, hh, room); err != nil {
				m.internalError(w, err)
				return
			}
		}
		where = append(where, "d.room_id IN ("+strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")+")")
		for _, id := range ids {
			args = append(args, id)
		}
	}
	if domain := q.Get("domain"); domain != "" {
		where, args = append(where, "d.domain = ?"), append(args, domain)
	}
	if source := q.Get("source"); source != "" {
		where, args = append(where, "d.source = ?"), append(args, source)
	}
	devs, err := m.queryDevices(ctx, strings.Join(where, " AND "), args...)
	if err != nil {
		m.internalError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(devs))
	for _, d := range devs {
		out = append(out, d.response())
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// --- POST /households/{hh}/devices/import ---

type importItem struct {
	entityID, name, domain, source                                                        string
	roomID, deviceClass, manufacturer, model, haDeviceID, protocol, localIP, mac, cloudID *string
}

func (m *Module) handleImportDevices(w http.ResponseWriter, r *http.Request, a provAuth) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	var items []importItem
	switch raw, present := b.m["devices"]; {
	case !present:
		b.fail("devices", "Field required")
	default:
		list, isList := raw.([]any)
		if !isList {
			b.fail("devices", "Input should be a valid list")
			break
		}
		for i, e := range list {
			obj, isObj := e.(map[string]any)
			sub := &body{m: obj, loc: fmt.Sprintf("body -> devices -> %d", i), errs: b.errs}
			if !isObj {
				sub.m = map[string]any{}
				*b.errs = append(*b.errs, sub.loc+": Input should be a valid dictionary or object to extract fields from")
				continue
			}
			it := importItem{source: "home_assistant"}
			it.entityID, _ = sub.str("entity_id", true)
			it.name, _ = sub.str("name", true)
			it.domain, _ = sub.str("domain", true)
			if s, ok := sub.str("source", false); ok {
				it.source = s
			}
			it.roomID, _ = sub.optStrPtr("room_id")
			it.deviceClass, _ = sub.optStrPtr("device_class")
			it.manufacturer, _ = sub.optStrPtr("manufacturer")
			it.model, _ = sub.optStrPtr("model")
			it.haDeviceID, _ = sub.optStrPtr("ha_device_id")
			it.protocol, _ = sub.optStrPtr("protocol")
			it.localIP, _ = sub.optStrPtr("local_ip")
			it.mac, _ = sub.optStrPtr("mac_address")
			it.cloudID, _ = sub.optStrPtr("cloud_id")
			items = append(items, it)
		}
	}
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	hh := r.PathValue("household_id")
	if err := m.householdAccess(ctx, a, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	created, updated := 0, 0
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		// Names of devices outside the batch are reserved; every batch device gets a
		// household-unique name, suffixing collisions " (2)", " (3)", ... (case-insensitive).
		batch := map[string]bool{}
		for _, it := range items {
			batch[it.entityID] = true
		}
		assigned := map[string]bool{}
		rows, err := tx.QueryContext(ctx, `SELECT entity_id, name FROM cc_devices WHERE household_id = ?`, hh)
		if err != nil {
			return err
		}
		for rows.Next() {
			var eid, name string
			if err := rows.Scan(&eid, &name); err != nil {
				rows.Close()
				return err
			}
			if !batch[eid] {
				assigned[strings.ToLower(name)] = true
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		uniqueName := func(desired string) string {
			base := strings.TrimSpace(desired)
			if base == "" {
				base = "Device"
			}
			candidate := base
			for i := 2; assigned[strings.ToLower(candidate)]; i++ {
				candidate = fmt.Sprintf("%s (%d)", base, i)
			}
			assigned[strings.ToLower(candidate)] = true
			return candidate
		}
		now := dbTime(m.now())
		for _, it := range items {
			if it.roomID != nil && *it.roomID != "" {
				if ok, err := m.roomInHousehold(ctx, tx, hh, *it.roomID); err != nil {
					return err
				} else if !ok {
					// Legacy hit the FK (500) or, cross-household, linked a foreign room.
					return fail(http.StatusBadRequest, "Room not found in this household")
				}
			}
			controllable := !sensorDomains[it.domain]
			name := uniqueName(it.name)
			var id string
			err := tx.QueryRowContext(ctx, `SELECT id FROM cc_devices WHERE household_id = ? AND entity_id = ?`, hh, it.entityID).Scan(&id)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				var room any
				if it.roomID != nil && *it.roomID != "" {
					room = *it.roomID
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO cc_devices (id, household_id, room_id, entity_id, name, domain,
					device_class, manufacturer, model, source, protocol, local_ip, mac_address, cloud_id, ha_device_id,
					is_controllable, is_active, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
					uuid4(), hh, room, it.entityID, name, it.domain, nullStr(it.deviceClass), nullStr(it.manufacturer),
					nullStr(it.model), it.source, nullStr(it.protocol), nullStr(it.localIP), nullStr(it.mac), nullStr(it.cloudID),
					nullStr(it.haDeviceID), controllable, now, now); err != nil {
					return err
				}
				created++
			case err != nil:
				return err
			default:
				// Every identity field is overwritten; room only when one is supplied.
				q := `UPDATE cc_devices SET name = ?, domain = ?, device_class = ?, manufacturer = ?, model = ?,
					ha_device_id = ?, source = ?, protocol = ?, local_ip = ?, mac_address = ?, cloud_id = ?,
					is_controllable = ?, is_active = 1, updated_at = ?`
				args := []any{name, it.domain, nullStr(it.deviceClass), nullStr(it.manufacturer), nullStr(it.model),
					nullStr(it.haDeviceID), it.source, nullStr(it.protocol), nullStr(it.localIP), nullStr(it.mac),
					nullStr(it.cloudID), controllable, now}
				if it.roomID != nil && *it.roomID != "" {
					q, args = q+", room_id = ?", append(args, *it.roomID)
				}
				if _, err := tx.ExecContext(ctx, q+` WHERE id = ?`, append(args, id)...); err != nil {
					return err
				}
				updated++
			}
		}
		return nil
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	m.deps.Log.Info("cc: device import", "created", created, "updated", updated, "household", hh)
	if created+updated > 0 {
		m.invalidateDeviceCache(ctx, hh)
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"created": created, "updated": updated})
}

// --- PATCH /households/{hh}/devices/{id} ---

func (m *Module) handleUpdateDevice(w http.ResponseWriter, r *http.Request, a provAuth) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	name, _ := b.optStrPtr("name")
	room, hasRoom := b.optStrPtr("room_id") // exclude_unset: null clears the room
	active, hasActive := b.boolean("is_active")
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	hh, id := r.PathValue("household_id"), r.PathValue("device_id")
	if err := m.householdAccess(ctx, a, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cc_devices WHERE id = ? AND household_id = ?`, id, hh).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fail(http.StatusNotFound, "Device not found")
		}
		sets, args := []string{"updated_at = ?"}, []any{dbTime(m.now())}
		if name != nil {
			// Duplicate names are ambiguous for routine and voice device resolution.
			newName := strings.TrimSpace(*name)
			if newName == "" {
				return fail(http.StatusUnprocessableEntity, "Device name cannot be empty")
			}
			var clash int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cc_devices WHERE household_id = ? AND id != ? AND lower(name) = lower(?)`,
				hh, id, newName).Scan(&clash); err != nil {
				return err
			}
			if clash > 0 {
				return fail(http.StatusConflict, fmt.Sprintf("A device named '%s' already exists in this household", newName))
			}
			sets, args = append(sets, "name = ?"), append(args, newName)
		}
		if hasRoom {
			if room != nil {
				if ok, err := m.roomInHousehold(ctx, tx, hh, *room); err != nil {
					return err
				} else if !ok {
					return fail(http.StatusBadRequest, "Room not found in this household")
				}
			}
			sets, args = append(sets, "room_id = ?"), append(args, nullStr(room))
		}
		if hasActive {
			sets, args = append(sets, "is_active = ?"), append(args, active)
		}
		_, err := tx.ExecContext(ctx, `UPDATE cc_devices SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, id)...)
		return err
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	m.invalidateDeviceCache(ctx, hh)
	d, err := m.device(ctx, hh, id)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	// Legacy left supported_actions null here (§8); the full DeviceResponse is returned.
	httpx.WriteJSON(w, http.StatusOK, d.response())
}

// --- DELETE /households/{hh}/devices/{id} ---

func (m *Module) handleDeleteDevice(w http.ResponseWriter, r *http.Request, a provAuth) {
	ctx := r.Context()
	hh, id := r.PathValue("household_id"), r.PathValue("device_id")
	if err := m.householdAccess(ctx, a, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	d, err := m.device(ctx, hh, id)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	// The owning node releases the device (e.g. HomeKit unpair) before the row goes.
	if d.sourceOr() == "direct" && d.protocol.String != "" {
		if n, err := m.pickNode(ctx, hh, d.protocol.String); err == nil {
			m.bus.Command(n.nodeID, "device_removed", map[string]any{
				"entity_id": d.entityID, "protocol": d.protocol.String, "domain": d.domain, "cloud_id": nullable(d.cloudID),
				"local_ip": nullable(d.localIP), "mac_address": nullable(d.mac), "name": d.name,
			})
		} else {
			m.deps.Log.Warn("cc: device_removed not sent", "device", d.entityID, "err", err)
		}
	}
	if _, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_devices WHERE id = ?`, id); err != nil {
		m.internalError(w, err)
		return
	}
	m.invalidateDeviceCache(ctx, hh)
	w.WriteHeader(http.StatusNoContent)
}

// --- GET /node/devices ---

// handleNodeDevices seeds the node's DirectDeviceService: the active devices of the
// node's own household (from auth, never the request).
func (m *Module) handleNodeDevices(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	out := []map[string]any{}
	if n.HouseholdID == "" {
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	devs, err := m.queryDevices(r.Context(), `d.household_id = ? AND d.is_active = 1`, n.HouseholdID)
	if err != nil {
		m.internalError(w, err)
		return
	}
	for _, d := range devs {
		out = append(out, map[string]any{
			"entity_id": d.entityID, "name": d.name, "domain": d.domain, "source": nullable(d.source),
			"protocol": nullable(d.protocol), "local_ip": d.localIP.String, "mac_address": d.mac.String,
			"cloud_id": d.cloudID.String, "model": d.model.String, "room_name": d.roomName.String,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
