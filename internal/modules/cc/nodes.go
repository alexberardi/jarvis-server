package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// onlineThreshold: a node is online when last_seen is within 15 minutes (models.py:16).
const onlineThreshold = 15 * time.Minute

type nodeRow struct {
	nodeID          string
	room            string
	user            sql.NullString
	voiceMode       sql.NullString
	lastSeen        sql.NullString
	householdID     sql.NullString
	lastSeenVersion sql.NullString
	installMode     sql.NullString
	isBusy          bool
	gitSHA          sql.NullString
	isActive        bool
	protocols       sql.NullString
	needsK2         bool
	contacted       bool // false from registration until the node first reaches us (F19)
	nodeKind        sql.NullString
	capabilities    sql.NullString // JSON object, from the heartbeat
	ownerUserID     sql.NullInt64  // who paired it (migration 00180); NULL: the admin key, or unknown
	lastAddr        sql.NullString // the latest heartbeat's TCP peer address (thisdevice.go)
	runtime         sql.NullString // version_info.runtime: "go" for the Go node core, NULL for Python
}

const nodeCols = `node_id, room, "user", voice_mode, last_seen, household_id, last_seen_version,
	install_mode, is_busy, git_sha, is_active, protocols, needs_k2, contacted, node_kind, capabilities,
	owner_user_id, last_addr, runtime`

type scanner interface{ Scan(...any) error }

func scanNode(s scanner) (*nodeRow, error) {
	var n nodeRow
	err := s.Scan(&n.nodeID, &n.room, &n.user, &n.voiceMode, &n.lastSeen, &n.householdID, &n.lastSeenVersion,
		&n.installMode, &n.isBusy, &n.gitSHA, &n.isActive, &n.protocols, &n.needsK2, &n.contacted,
		&n.nodeKind, &n.capabilities, &n.ownerUserID, &n.lastAddr, &n.runtime)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func (m *Module) nodeByID(ctx context.Context, id string) (*nodeRow, error) {
	return scanNode(m.deps.DB.Read.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM cc_nodes WHERE node_id = ?`, id))
}

// online is legacy's is_online: last_seen within the threshold, which registration stamps.
func (n *nodeRow) online(now time.Time) bool {
	return n.lastSeen.Valid && !parseTS(n.lastSeen.String).Before(now.Add(-onlineThreshold))
}

// reachable gates round trips to the node (report_tools, tool calls, callbacks): online and
// heard from at least once. A node registered but never connected would otherwise count as
// online for 15 minutes and every round trip would wait out its timeout (A10 F19).
func (n *nodeRow) reachable(now time.Time) bool {
	return n.contacted && n.online(now)
}

// nodeReachable gates round trips to the node: a live MQTT connection when the broker can
// tell (the embedded one always can), else reachable's last_seen heuristic. last_seen alone
// was wrong: any authenticated HTTP request refreshes it (the dev seeder's key check did), so
// a node with no MQTT session counted as online for 15 minutes and every new chat waited out
// a 10 s report_tools that nothing would answer (the demo dry run).
func (m *Module) nodeReachable(row *nodeRow) bool { return m.nodeReachableAt(row, m.now()) }

func (m *Module) nodeReachableAt(row *nodeRow, now time.Time) bool {
	if up, known := m.bus.NodeSession(row.nodeID); known {
		return up
	}
	return row.reachable(now)
}

func nullableInt(n sql.NullInt64) any {
	if !n.Valid {
		return nil
	}
	return n.Int64
}

func nullable(ns sql.NullString) any {
	if !ns.Valid {
		return nil
	}
	return ns.String
}

// caps is the node's reported capabilities, {} when it reported none.
func (n *nodeRow) caps() map[string]any {
	out := map[string]any{}
	if n.capabilities.Valid {
		_ = json.Unmarshal([]byte(n.capabilities.String), &out)
	}
	return out
}

// response is NodeResponse. adapter_hash is gone with LoRA (D9; mobile's node type never
// read it). node_kind and capabilities are jarvisd additions (install plan 1.8): null and {}
// for a node that never reported them. owner_user_id (who paired it, null when unknown) lets the
// web app offer Sign out to its owner; last_addr is never returned. name is the node's registered
// name (a desktop agent's computer name), null when it was registered without one: auth then
// stores the node id, which says nothing the node_id key doesn't.
func (n *nodeRow) response(now time.Time, name string) map[string]any {
	user := "default"
	if n.user.Valid {
		user = n.user.String
	}
	var lastSeen any
	if n.lastSeen.Valid {
		lastSeen = pyNaive(parseTS(n.lastSeen.String))
	}
	return map[string]any{
		"node_id": n.nodeID, "room": n.room, "user": user, "voice_mode": nullable(n.voiceMode),
		"household_id": nullable(n.householdID), "online": n.online(now), "last_seen": lastSeen,
		"last_seen_version": nullable(n.lastSeenVersion), "install_mode": nullable(n.installMode),
		"git_sha": nullable(n.gitSHA), "is_busy": n.isBusy, "needs_k2": n.needsK2,
		"node_kind": nullable(n.nodeKind), "capabilities": n.caps(), "owner_user_id": nullableInt(n.ownerUserID),
		"runtime": nullable(n.runtime),
		"name":    nodeName(n.nodeID, name),
	}
}

func nodeName(id, name string) any {
	if name == "" || name == id {
		return nil
	}
	return name
}

// nodeNames looks up the registered names of rows; a failure is logged and leaves them null,
// since a name is a label and the list is still right without it.
func (m *Module) nodeNames(ctx context.Context, rows ...*nodeRow) map[string]string {
	ids := make([]string, len(rows))
	for i, n := range rows {
		ids[i] = n.nodeID
	}
	names, err := m.Nodes.NodeNames(ctx, ids)
	if err != nil {
		m.deps.Log.Warn("cc: node names", "err", err)
		return nil
	}
	return names
}

// nodeResponse is one node's response, with its registered name.
func (m *Module) nodeResponse(ctx context.Context, n *nodeRow) map[string]any {
	return n.response(m.now(), m.nodeNames(ctx, n)[n.nodeID])
}

// --- GET /admin/nodes, GET /admin/nodes/{id} ---

func (m *Module) handleListNodes(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	hh := r.URL.Query().Get("household_id")
	inactive, ok := queryBool(w, r, "include_inactive", false)
	if !ok {
		return
	}
	q := `SELECT ` + nodeCols + ` FROM cc_nodes WHERE 1=1`
	var args []any
	if !inactive {
		q += ` AND is_active = 1`
	}
	if hh != "" {
		if !u.IsSuperuser {
			if err := m.requireRole(ctx, u.ID, hh, authn.RoleMember); err != nil {
				m.writeErr(w, err)
				return
			}
		}
		q += ` AND household_id = ?`
		args = append(args, hh)
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, q+` ORDER BY rowid`, args...)
	if err != nil {
		m.internalError(w, err)
		return
	}
	var nodes []*nodeRow
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			rows.Close()
			m.internalError(w, err)
			return
		}
		nodes = append(nodes, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	now := m.now()
	out := make([]map[string]any, 0, len(nodes))
	var names map[string]string
	if len(nodes) > 0 {
		names = m.nodeNames(ctx, nodes...)
	}
	member := map[string]bool{}
	for _, n := range nodes {
		if !u.IsSuperuser && hh == "" {
			// Non-superusers silently skip foreign households and household-less nodes (§7.10).
			if !n.householdID.Valid || n.householdID.String == "" {
				continue
			}
			ok, seen := member[n.householdID.String]
			if !seen {
				_, ok, err = m.Auth.HouseholdRole(ctx, u.ID, n.householdID.String)
				if err != nil {
					m.internalError(w, err)
					return
				}
				member[n.householdID.String] = ok
			}
			if !ok {
				continue
			}
		}
		out = append(out, n.response(now, names[n.nodeID]))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) handleGetNode(w http.ResponseWriter, r *http.Request, u authn.User) {
	n, err := m.requireNodeAccess(r.Context(), u, r.PathValue("node_id"), authn.RoleMember, "Not authorized")
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, m.nodeResponse(r.Context(), n))
}

// --- POST /admin/nodes/heartbeat ---

func (m *Module) handleHeartbeat(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	b, present, ok := readBody(w, r, true)
	if !ok {
		return
	}
	versionInfo, hasVersion := b.object("version_info", false)
	busy, hasBusy := b.boolean("is_busy")
	protocols, hasProtocols := b.strList("protocols")
	needsK2, hasK2 := b.boolean("needs_k2")
	kind, hasKind := b.str("node_kind", false)
	caps, hasCaps := b.object("capabilities", false)
	devID, hasDevID := b.str("device_id", false)
	name, hasName := b.str("name", false)
	b.object("thread_status", false)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	now := m.now()
	sets := []string{"last_seen = ?", "last_addr = ?"}
	args := []any{dbTime(now), peerAddr(r)}
	var reported *string
	if present {
		if hasVersion {
			str := func(k string) any {
				if s, ok := versionInfo[k].(string); ok {
					return s
				}
				return nil
			}
			if s, ok := versionInfo["version"].(string); ok {
				reported = &s
			}
			rt, _ := versionInfo["runtime"].(string)
			sets = append(sets, "last_seen_version = ?", "install_mode = ?", "git_sha = ?", "runtime = ?")
			args = append(args, str("version"), str("install_mode"), str("git_sha"), nullStr(runtimeValue(rt)))
		}
		if hasBusy {
			sets = append(sets, "is_busy = ?")
			args = append(args, busy)
		}
		if hasProtocols {
			p, _ := json.Marshal(protocols)
			sets = append(sets, "protocols = ?")
			args = append(args, string(p))
		}
		if hasK2 {
			sets = append(sets, "needs_k2 = ?")
			args = append(args, needsK2)
		}
		if hasKind {
			sets = append(sets, "node_kind = ?")
			args = append(args, nullStr(nodeKindValue(kind)))
		}
		// An agent enrolled before registration carried device_id gets it on file here, so its
		// next re-enrolment replaces it. A malformed one is ignored, never stored or cleared.
		if d := deviceIDValue(devID); hasDevID && d != nil {
			sets = append(sets, "device_id = ?")
			args = append(args, *d)
		}
		if hasCaps {
			c, _ := json.Marshal(cleanCaps(caps))
			sets = append(sets, "capabilities = ?")
			args = append(args, string(c))
		}
	}
	q := `UPDATE cc_nodes SET `
	for i, s := range sets {
		if i > 0 {
			q += ", "
		}
		q += s
	}
	args = append(args, n.ID)
	if _, err := m.deps.DB.Write.ExecContext(ctx, q+` WHERE node_id = ?`, args...); err != nil {
		m.internalError(w, err)
		return
	}
	// A renamed computer reports its new name (#62). It renames only this node, the one the
	// key authenticated; an empty or overlong name is ignored, so a bad node.json can't stop
	// the heartbeat. A failure is logged: the name is a label, the beat still counts.
	if s := nodeNameValue(name); hasName && s != nil {
		if err := m.Nodes.RenameNode(ctx, n.ID, *s); err != nil {
			m.deps.Log.Warn("cc: rename node", "node", n.ID, "err", err)
		}
	}
	row, err := m.nodeByID(ctx, n.ID)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if err := m.reconcileOpenTask(ctx, n.ID, reported); err != nil {
		m.internalError(w, err)
		return
	}
	resp := map[string]any{"status": "ok"}
	pending, err := m.dispatchPendingTask(ctx, row)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if pending != nil {
		resp["pending_update"] = pending
	}
	if row.followsServer() {
		// A desktop agent or Go Pi node follows this server's release (01-no-cli-plan 1.7,
		// go-pi-node.md §7): it updates itself to the same version, so a household's agents
		// match its jarvisd.
		resp["server_version"] = m.Version
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// Heartbeat capability bounds: a node reports a handful of flags, not a document.
const (
	maxCaps   = 32
	maxCapLen = 64
)

// cleanCaps keeps up to maxCaps scalar capabilities (string, bool, number) with short keys and
// strings; anything else is dropped, so the stored object stays small and flat.
func cleanCaps(in map[string]any) map[string]any {
	out := map[string]any{}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if len(out) >= maxCaps || k == "" || len(k) > maxCapLen {
			continue
		}
		switch v := in[k].(type) {
		case string:
			if len(v) <= maxCapLen {
				out[k] = v
			}
		case bool, json.Number:
			out[k] = v
		}
	}
	return out
}

// nodeKindValue is a reported node_kind, trimmed; empty or overlong clears it.
func nodeKindValue(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxCapLen {
		return nil
	}
	return &s
}

// runtimeValue is a reported runtime, trimmed and lower-cased; empty or overlong is nil.
func runtimeValue(s string) *string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > maxCapLen {
		return nil
	}
	return &s
}

// maxNodeNameLen bounds a name a heartbeat reports, in characters.
const maxNodeNameLen = 100

// nodeNameValue is a reported name, trimmed; empty or overlong is nil (ignored).
func nodeNameValue(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxNodeNameLen {
		return nil
	}
	return &s
}

// --- POST /admin/nodes (admin key) ---

// nodeServices are the grants a registered node gets: jarvis-logs (legacy CC asked for it)
// plus CC itself (legacy auth auto-granted the calling app).
func (m *Module) nodeServices() []string { return []string{"jarvis-logs", m.serviceID()} }

// registerWithAuth is _register_node_with_auth in process: auth's 400/404 details pass through.
func (m *Module) registerWithAuth(ctx context.Context, nodeID, householdID, name string) (string, error) {
	key, err := m.Nodes.RegisterNode(ctx, nodeID, householdID, name, m.nodeServices())
	if err != nil {
		var sc interface{ StatusCode() int }
		if errors.As(err, &sc) {
			return "", fail(sc.StatusCode(), err.Error())
		}
		return "", err
	}
	return key, nil
}

func createdNode(id, room, user, voiceMode, key string) map[string]any {
	return map[string]any{"node_id": id, "room": room, "user": user, "voice_mode": voiceMode, "node_key": key}
}

// insertNode writes the local row; on failure the auth registration is rolled back (legacy
// left it orphaned, §3.1).
// owner is who paired it (the provisioning token's minter), nil for the admin key.
func (m *Module) insertNode(ctx context.Context, tx *sql.Tx, id, room, user, voiceMode, householdID string, owner *int64) error {
	var by any
	if owner != nil {
		by = *owner
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO cc_nodes (node_id, room, "user", voice_mode, household_id, last_seen, contacted, owner_user_id)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?)`, id, room, user, voiceMode, householdID, dbTime(m.now()), by)
	return err
}

func (m *Module) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	id, _ := b.str("node_id", true)
	hh, _ := b.str("household_id", true)
	name, _ := b.str("name", false)
	room, _ := b.str("room", true)
	user, hasUser := b.str("user", false)
	voice, hasVoice := b.str("voice_mode", false)
	if !b.done(w) {
		return
	}
	if !hasUser {
		user = "default"
	}
	if !hasVoice {
		voice = "brief"
	}
	if name == "" {
		name = id
	}
	ctx := r.Context()
	if _, err := m.nodeByID(ctx, id); err == nil {
		detail(w, http.StatusBadRequest, "Node already exists locally")
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		m.internalError(w, err)
		return
	}
	key, err := m.registerWithAuth(ctx, id, hh, name)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error { return m.insertNode(ctx, tx, id, room, user, voice, hh, nil) }); err != nil {
		_ = m.Nodes.DeactivateNode(context.WithoutCancel(ctx), id)
		m.internalError(w, err)
		return
	}
	m.deps.Log.Info("cc: node created", "node", id, "room", room)
	// 200, not 201: the legacy route declared no status code (§7.14).
	httpx.WriteJSON(w, http.StatusOK, createdNode(id, room, user, voice, key))
}

// --- PATCH /admin/nodes/{id} (admin key) ---

func (m *Module) handlePatchNode(w http.ResponseWriter, r *http.Request) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	var sets []string
	var args []any
	if b.has("room") {
		if room, ok := b.str("room", true); ok {
			sets, args = append(sets, "room = ?"), append(args, room)
		}
	}
	for _, f := range []struct{ key, col string }{{"user", `"user"`}, {"voice_mode", "voice_mode"}} {
		if v, ok := b.optStrPtr(f.key); ok {
			sets, args = append(sets, f.col+" = ?"), append(args, nullStr(v))
		}
	}
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	id := r.PathValue("node_id")
	if _, err := m.nodeByID(ctx, id); errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Node not found")
		return
	} else if err != nil {
		m.internalError(w, err)
		return
	}
	if len(sets) > 0 {
		q := `UPDATE cc_nodes SET `
		for i, s := range sets {
			if i > 0 {
				q += ", "
			}
			q += s
		}
		if _, err := m.deps.DB.Write.ExecContext(ctx, q+` WHERE node_id = ?`, append(args, id)...); err != nil {
			m.internalError(w, err)
			return
		}
	}
	n, err := m.nodeByID(ctx, id)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, m.nodeResponse(ctx, n))
}

// --- GET /node/mqtt-credentials ---

// handleMQTTCredentials hands the node its own broker credential (D4): username = node_id,
// password = the node key it just authenticated with (see brokerAuth). Nulls (connect
// anonymously) only when jarvisd runs without a broker.
func (m *Module) handleMQTTCredentials(w http.ResponseWriter, _ *http.Request, n *nodeCtx) {
	if m.broker == nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"username": nil, "password": nil})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"username": n.ID, "password": n.Key})
}
