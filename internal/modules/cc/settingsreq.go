package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Settings request / snapshot (doc 05 §3.2), the K2 relay (§3.3) and the config push relay
// (doc 07, pending/ack per D6). CC only ever stores ciphertext.

const (
	settingsRequestTTL = 5 * time.Minute
	authPushTTL        = 5 * time.Minute
)

// k2Wait: the K2 relay waits 15 s for the node's ack (a var for tests).
var k2Wait = 15 * time.Second

type settingsRequest struct {
	id, nodeID, status   string
	includeValues        bool
	userID               sql.NullInt64
	createdAt, expiresAt string
}

const srCols = `request_id, node_id, status, include_values, user_id, created_at, expires_at`

func scanSR(s scanner) (*settingsRequest, error) {
	var x settingsRequest
	if err := s.Scan(&x.id, &x.nodeID, &x.status, &x.includeValues, &x.userID, &x.createdAt, &x.expiresAt); err != nil {
		return nil, err
	}
	return &x, nil
}

func (s *settingsRequest) response() map[string]any {
	return map[string]any{
		"request_id": s.id, "node_id": s.nodeID, "status": s.status,
		"created_at": pyNaive(parseTS(s.createdAt)), "expires_at": pyNaive(parseTS(s.expiresAt)),
	}
}

// listResponse adds include_values and user_id for the node's reconnect backstop (D40
// 05.Q8): they used to live only in the MQTT payload, so the backstop downgraded secret sync.
func (s *settingsRequest) listResponse() map[string]any {
	out := s.response()
	out["include_values"] = s.includeValues
	if s.userID.Valid {
		out["user_id"] = s.userID.Int64
	} else {
		out["user_id"] = nil
	}
	return out
}

func (s *settingsRequest) expired(now time.Time) bool { return parseTS(s.expiresAt).Before(now) }

func (m *Module) handleCreateSettingsRequest(w http.ResponseWriter, r *http.Request, u authn.User) {
	include, ok := queryBool(w, r, "include_values", false)
	if !ok {
		return
	}
	ctx := r.Context()
	id := r.PathValue("node_id")
	// power_user in the node's household; a household-less node is superuser-only (D8:
	// legacy skipped authz entirely there, §8.6).
	if _, err := m.requireNodeAccess(ctx, u, id, authn.RolePowerUser, "Not authorized"); err != nil {
		m.writeErr(w, err)
		return
	}
	now := m.now()
	sr := &settingsRequest{id: uuid4(), nodeID: id, status: "pending", includeValues: include,
		userID: sql.NullInt64{Int64: u.ID, Valid: true}, createdAt: dbTime(now), expiresAt: dbTime(now.Add(settingsRequestTTL))}
	if _, err := m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_settings_requests (`+srCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sr.id, sr.nodeID, sr.status, sr.includeValues, sr.userID, sr.createdAt, sr.expiresAt); err != nil {
		m.internalError(w, err)
		return
	}
	payload := map[string]any{"request_id": sr.id, "node_id": id, "user_id": u.ID}
	if include {
		payload["include_values"] = true
	}
	if err := m.bus.Publish(id, "settings/request", payload); err != nil {
		m.deps.Log.Warn("cc: settings request not published; node must poll", "node", id, "err", err)
	}
	httpx.WriteJSON(w, http.StatusCreated, sr.response())
}

func (m *Module) handleListSettingsRequests(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	id := r.PathValue("node_id")
	if n.ID != id {
		detail(w, http.StatusForbidden, "Cannot access other node's requests")
		return
	}
	rows, err := m.deps.DB.Read.QueryContext(r.Context(), `SELECT `+srCols+` FROM cc_settings_requests
		WHERE node_id = ? AND status = 'pending' AND expires_at > ? ORDER BY created_at ASC, rowid ASC`, id, dbTime(m.now()))
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		sr, err := scanSR(rows)
		if err != nil {
			m.internalError(w, err)
			return
		}
		out = append(out, sr.listResponse())
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) settingsRequest(ctx context.Context, nodeID, rid string) (*settingsRequest, error) {
	return scanSR(m.deps.DB.Read.QueryRowContext(ctx, `SELECT `+srCols+` FROM cc_settings_requests
		WHERE request_id = ? AND node_id = ?`, rid, nodeID))
}

func (m *Module) handleGetSettingsRequest(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	id := r.PathValue("node_id")
	if n.ID != id {
		detail(w, http.StatusForbidden, "Cannot access other node's requests")
		return
	}
	sr, err := m.settingsRequest(r.Context(), id, r.PathValue("request_id"))
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Request not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if sr.expired(m.now()) {
		detail(w, http.StatusGone, "Request expired")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, sr.response())
}

func (m *Module) handleUploadSnapshot(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	id, rid := r.PathValue("node_id"), r.PathValue("request_id")
	if n.ID != id {
		detail(w, http.StatusForbidden, "Cannot upload to other node's requests")
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	ct, _ := b.str("ciphertext", true)
	nonce, _ := b.str("nonce", true)
	tag, _ := b.str("tag", true)
	sv, _ := b.integer("aad_schema_version", true)
	csv, _ := b.integer("aad_commands_schema_version", true)
	rev, _ := b.integer("aad_revision", true)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	snapID, created := uuid4(), dbTime(m.now())
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		sr, err := scanSR(tx.QueryRowContext(ctx, `SELECT `+srCols+` FROM cc_settings_requests WHERE request_id = ? AND node_id = ?`, rid, id))
		if errors.Is(err, sql.ErrNoRows) {
			return fail(http.StatusNotFound, "Request not found")
		}
		if err != nil {
			return err
		}
		if sr.expired(m.now()) {
			return fail(http.StatusGone, "Request expired")
		}
		if sr.status != "pending" {
			return fail(http.StatusConflict, "Request already "+sr.status)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO cc_settings_snapshots (snapshot_id, node_id, request_id, ciphertext,
			nonce, tag, aad_node_id, aad_schema_version, aad_commands_schema_version, aad_revision, aad_request_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, snapID, id, rid, ct, nonce, tag, id, sv, csv, rev, rid, created); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE cc_settings_requests SET status = 'fulfilled' WHERE request_id = ?`, rid)
		return err
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"snapshot_id": snapID, "node_id": id, "created_at": pyNaive(parseTS(created))})
}

func (m *Module) handleSettingsResult(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	id, rid := r.PathValue("node_id"), r.PathValue("request_id")
	sr, err := m.settingsRequest(ctx, id, rid)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Request not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	// D4: the poll gets the same household check as creating the request (§8.4).
	if _, err := m.requireNodeAccess(ctx, u, id, authn.RolePowerUser, "Not authorized"); err != nil {
		m.writeErr(w, err)
		return
	}
	if sr.status == "pending" {
		if sr.expired(m.now()) {
			detail(w, http.StatusGone, "Request expired")
			return
		}
		// 202 with a body while pending (§7.13).
		httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "pending", "request_id": rid, "message": "Waiting for node response"})
		return
	}
	var snapID, ct, nonce, tag, aadNode, aadRID, created string
	var sv, csv, rev int64
	err = m.deps.DB.Read.QueryRowContext(ctx, `SELECT snapshot_id, ciphertext, nonce, tag, aad_node_id, aad_schema_version,
		aad_commands_schema_version, aad_revision, aad_request_id, created_at FROM cc_settings_snapshots WHERE request_id = ?
		ORDER BY created_at LIMIT 1`, rid).
		Scan(&snapID, &ct, &nonce, &tag, &aadNode, &sv, &csv, &rev, &aadRID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusInternalServerError, "Snapshot missing for fulfilled request")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "fulfilled", "request_id": rid,
		"snapshot": map[string]any{
			"snapshot_id": snapID, "ciphertext": ct, "nonce": nonce, "tag": tag,
			"aad": map[string]any{"node_id": aadNode, "schema_version": sv, "commands_schema_version": csv,
				"revision": rev, "request_id": aadRID},
			"created_at": pyNaive(parseTS(created)),
		},
	})
}

// --- K2 relay ---

// k2Store holds K2 key material for the length of one provision call only, in memory
// (replacing /tmp/jarvis-k2-pending): the node pulls it once over its authenticated channel.
type k2Store struct {
	mu sync.Mutex
	m  map[string]k2Entry
}

type k2Entry struct{ nodeID, k2, kid, createdAt string }

func newK2Store() *k2Store { return &k2Store{m: map[string]k2Entry{}} }

func (s *k2Store) put(rid string, e k2Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[rid] = e
}

// take is the one-time read: the entry goes whether or not the node matches.
func (s *k2Store) take(rid string) (k2Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[rid]
	delete(s.m, rid)
	return e, ok
}

func (s *k2Store) drop(rid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, rid)
}

func (m *Module) handleProvisionK2(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	id := r.PathValue("node_id")
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	k2, _ := b.str("k2", true)
	kid, _ := b.str("kid", true)
	created, _ := b.str("created_at", true)
	if !b.done(w) {
		return
	}
	if _, err := m.requireNodeAccess(ctx, u, id, authn.RoleMember, "Not authorized"); err != nil {
		m.writeErr(w, err)
		return
	}
	if !m.bus.Available() {
		detail(w, http.StatusServiceUnavailable, "MQTT not available")
		return
	}
	rid := uuid4()
	m.k2.put(rid, k2Entry{nodeID: id, k2: k2, kid: kid, createdAt: created})
	defer m.k2.drop(rid) // key material never outlives the call
	m.bus.Expect(rid, id)
	defer m.bus.Drop(rid)
	// A nudge only, never key material (§7.11).
	if err := m.bus.Publish(id, "k2/provision", map[string]any{"request_id": rid}); err != nil {
		m.deps.Log.Warn("cc: k2 nudge not published", "node", id, "err", err)
	}
	wctx, cancel := context.WithTimeout(ctx, k2Wait)
	defer cancel()
	res, err := m.bus.Await(wctx, rid)
	if err != nil {
		if errors.Is(err, ErrNoResult) {
			detail(w, http.StatusGatewayTimeout, "Node did not acknowledge K2 — it may not be online yet. Try again in a few seconds.")
			return
		}
		m.internalError(w, err)
		return
	}
	var ack map[string]any
	_ = json.Unmarshal(res, &ack)
	if success, _ := ack["success"].(bool); success {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "node_id": id, "kid": kid})
		return
	}
	msg, isStr := ack["error"].(string)
	if !isStr || msg == "" {
		msg = "Node failed to store K2"
	}
	detail(w, http.StatusBadGateway, msg)
}

func (m *Module) handleFetchK2(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	id := r.PathValue("node_id")
	if n.ID != id {
		detail(w, http.StatusForbidden, "Node mismatch")
		return
	}
	e, ok := m.k2.take(r.PathValue("request_id"))
	if !ok || e.nodeID != id {
		detail(w, http.StatusNotFound, "No pending K2 for this request")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"k2": e.k2, "kid": e.kid, "created_at": e.createdAt})
}

// handleAckK2 delivers the node's ack to the waiting provision call. D8: the path node must
// be the caller (legacy didn't check, §8.5), and the rid must be one issued to it (D4).
func (m *Module) handleAckK2(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	if n.ID != r.PathValue("node_id") {
		detail(w, http.StatusForbidden, "Node mismatch")
		return
	}
	raw, ok := readRawObject(w, r)
	if !ok {
		return
	}
	m.deliver(r.PathValue("request_id"), n.ID, raw)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// --- config push (encrypted mobile→node relay) ---

func (m *Module) handleCreateConfigPush(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok {
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	ctype, _ := b.str("config_type", true)
	ct, _ := b.str("ciphertext", true)
	nonce, _ := b.str("nonce", true)
	tag, _ := b.str("tag", true)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	id := r.PathValue("node_id")
	node, err := m.nodeByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Node not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	// D4: a JWT caller must belong to the node's household.
	if !auth.admin {
		if !node.householdID.Valid || node.householdID.String == "" {
			if !auth.user.IsSuperuser {
				detail(w, http.StatusForbidden, "Not authorized")
				return
			}
		} else if err := m.requireRole(ctx, auth.user.ID, node.householdID.String, authn.RoleMember); err != nil {
			m.writeErr(w, err)
			return
		}
	}
	now := m.now()
	var expires any
	if strings.HasPrefix(ctype, "auth:") {
		expires = dbTime(now.Add(authPushTTL))
	}
	pushID := uuid4()
	if _, err := m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_config_pushes (id, node_id, config_type, ciphertext, nonce, tag,
		status, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, pushID, id, ctype, ct, nonce, tag, dbTime(now), expires); err != nil {
		m.internalError(w, err)
		return
	}
	if err := m.bus.Publish(id, "config/push", map[string]any{"push_id": pushID, "config_type": ctype, "node_id": id}); err != nil {
		m.deps.Log.Warn("cc: config push not published; node must poll", "node", id, "err", err)
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"id": pushID, "node_id": id, "config_type": ctype, "status": "pending", "created_at": pyNaive(now),
	})
}

// handlePendingConfig: node auth bound to the path node (D6).
func (m *Module) handlePendingConfig(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	id := r.PathValue("node_id")
	if n.ID != id {
		detail(w, http.StatusForbidden, "Cannot access other node's config")
		return
	}
	ctx := r.Context()
	now := dbTime(m.now())
	if _, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_config_pushes WHERE node_id = ? AND expires_at IS NOT NULL
		AND expires_at < ?`, id, now); err != nil {
		m.internalError(w, err)
		return
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT id, config_type, ciphertext, nonce, tag, created_at FROM cc_config_pushes
		WHERE node_id = ? AND status = 'pending' AND (expires_at IS NULL OR expires_at >= ?) ORDER BY created_at, rowid`, id, now)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var pid, ctype, ct, nonce, tag, created string
		if err := rows.Scan(&pid, &ctype, &ct, &nonce, &tag, &created); err != nil {
			m.internalError(w, err)
			return
		}
		out = append(out, map[string]any{"id": pid, "config_type": ctype, "ciphertext": ct, "nonce": nonce, "tag": tag,
			"created_at": pyNaive(parseTS(created))})
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) handleAckConfig(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	id, pushID := r.PathValue("node_id"), r.PathValue("push_id")
	if n.ID != id {
		detail(w, http.StatusForbidden, "Cannot access other node's config")
		return
	}
	ctx := r.Context()
	var ctype, status string
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT config_type, status FROM cc_config_pushes WHERE id = ? AND node_id = ?`,
		pushID, id).Scan(&ctype, &status)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Config push not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if status == "consumed" {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "already_consumed"})
		return
	}
	// Auth pushes carry tokens: deleted on ack, never kept.
	if strings.HasPrefix(ctype, "auth:") {
		if _, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_config_pushes WHERE id = ?`, pushID); err != nil {
			m.internalError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "consumed_and_deleted"})
		return
	}
	if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_config_pushes SET status = 'consumed', consumed_at = ? WHERE id = ?`,
		dbTime(m.now()), pushID); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "consumed"})
}
