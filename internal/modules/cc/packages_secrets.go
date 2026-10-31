package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Server-managed package secrets (docs/decisions/2026-10-30-server-managed-package-secrets.md).
// The app pushes a node's secrets K2-encrypted, which only the app can do. This adds the
// server's own path, for the admin UI, scripts and AI tools: a household admin sets a secret
// (HA_URL, a Keep token, ...), the server keeps it encrypted at rest, and each target node
// pulls what is meant for it over its authenticated channel after a nudge that carries no
// secret (the same nudge-and-pull as package installs). The two paths write the same node
// store, so either can set or change a secret.

const (
	pkgSecretsTopic    = "package-secrets"
	pkgSecretMaxLen    = 8 << 10
	pkgSecretTombstone = 30 * 24 * time.Hour // a deleted secret's tombstone stays this long for offline nodes
)

// pkgSecretKey is a secret's name as packages declare them (jarvis_package.yaml `secrets`).
var pkgSecretKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

func (m *Module) registerPackageSecrets(mux *http.ServeMux) {
	m.atRest() // create the key now, not under concurrent requests
	const base = "/api/v0/households/{household_id}/package-secrets"
	mux.HandleFunc("GET "+base, m.handleListPkgSecrets)
	mux.HandleFunc("PUT "+base+"/{key}", m.handlePutPkgSecret)
	mux.HandleFunc("DELETE "+base+"/{key}", m.handleDeletePkgSecret)
	mux.HandleFunc("GET /api/v0/nodes/{node_id}/package-secrets", m.node(m.handleNodePkgSecrets))
}

// requireSecretsAdmin: secrets are a household admin's (or the admin key's) to read the names of
// and to change.
func (m *Module) requireSecretsAdmin(ctx context.Context, auth provAuth, hh string) error {
	if auth.admin {
		return nil
	}
	return m.requireRole(ctx, auth.user.ID, hh, authn.RoleAdmin)
}

// atRest is the at-rest cipher shared with the smart-home OAuth tokens.
func (m *Module) atRest() *tokenCipher {
	if m.smart == nil {
		m.smart = &smartHome{}
	}
	if m.smart.cipher == nil {
		m.smart.cipher = newTokenCipher(m.deps.Config.Home)
	}
	return m.smart.cipher
}

// targets decodes a row's node_ids: nil is every node of the household.
func targets(nodeIDs sql.NullString) []string {
	if !nodeIDs.Valid {
		return nil
	}
	var ids []string
	_ = json.Unmarshal([]byte(nodeIDs.String), &ids)
	return ids
}

// handleListPkgSecrets names the household's secrets and where they go. A value is never
// returned, by this or any route.
func (m *Module) handleListPkgSecrets(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok {
		return
	}
	ctx, hh := r.Context(), r.PathValue("household_id")
	if err := m.requireSecretsAdmin(ctx, auth, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT secret_key, node_ids, updated_at FROM cc_package_secrets
		WHERE household_id = ? AND value_enc IS NOT NULL ORDER BY secret_key`, hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var key, updated string
		var ids sql.NullString
		if err := rows.Scan(&key, &ids, &updated); err != nil {
			m.internalError(w, err)
			return
		}
		out = append(out, map[string]any{"key": key, "node_ids": nullable(ids), "updated_at": updated})
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"secrets": out})
}

// handlePutPkgSecret sets one secret: {"value": "...", "node_ids": ["..."]}; node_ids omitted
// means every node of the household.
func (m *Module) handlePutPkgSecret(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok {
		return
	}
	ctx, hh, key := r.Context(), r.PathValue("household_id"), r.PathValue("key")
	if err := m.requireSecretsAdmin(ctx, auth, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	if !pkgSecretKey.MatchString(key) {
		validationError(w, "path -> key: use the secret's name as the package declares it, such as HA_TOKEN")
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	value, _ := b.str("value", true)
	ids, _ := b.strList("node_ids")
	if !b.done(w) {
		return
	}
	if value == "" || len(value) > pkgSecretMaxLen {
		validationError(w, "body -> value: give a value of up to 8 KiB (delete the secret to clear it)")
		return
	}
	for _, id := range ids {
		n, err := m.nodeByID(ctx, id)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && n.householdID.String != hh) {
			detail(w, http.StatusNotFound, "Node not found: "+id)
			return
		}
		if err != nil {
			m.internalError(w, err)
			return
		}
	}
	enc, err := m.atRest().encrypt(value)
	if err != nil {
		m.internalError(w, err)
		return
	}
	nodeIDs := sql.NullString{}
	if len(ids) > 0 {
		j, _ := json.Marshal(ids)
		nodeIDs = sql.NullString{String: string(j), Valid: true}
	}
	if _, err := m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_package_secrets (household_id, secret_key, value_enc, node_ids, updated_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT (household_id, secret_key) DO UPDATE SET value_enc = excluded.value_enc,
		node_ids = excluded.node_ids, updated_at = excluded.updated_at`, hh, key, enc, nodeIDs, dbTime(m.now())); err != nil {
		m.internalError(w, err)
		return
	}
	m.deps.Log.Info("cc: package secret set", "household", hh, "key", key, "nodes", len(ids))
	m.nudgeSecrets(ctx, hh, ids)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "key": key})
}

// handleDeletePkgSecret removes a secret. A tombstone stays so the nodes clear their copy.
func (m *Module) handleDeletePkgSecret(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok {
		return
	}
	ctx, hh, key := r.Context(), r.PathValue("household_id"), r.PathValue("key")
	if err := m.requireSecretsAdmin(ctx, auth, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	var ids sql.NullString
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT node_ids FROM cc_package_secrets
		WHERE household_id = ? AND secret_key = ? AND value_enc IS NOT NULL`, hh, key).Scan(&ids)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Secret not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_package_secrets SET value_enc = NULL, updated_at = ?
		WHERE household_id = ? AND secret_key = ?`, dbTime(m.now()), hh, key); err != nil {
		m.internalError(w, err)
		return
	}
	m.nudgeSecrets(ctx, hh, targets(ids))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// nudgeSecrets tells the nodes to pull: the listed ones, or every active node of the household.
// The message carries nothing secret; a node that is offline pulls when it reconnects.
func (m *Module) nudgeSecrets(ctx context.Context, hh string, ids []string) {
	if len(ids) == 0 {
		rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT node_id FROM cc_nodes WHERE household_id = ? AND is_active = 1`, hh)
		if err != nil {
			m.deps.Log.Warn("cc: package secrets: list nodes", "err", err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
	}
	for _, id := range ids {
		if err := m.bus.Publish(id, pkgSecretsTopic, map[string]any{}); err != nil {
			m.deps.Log.Warn("cc: package secrets not delivered", "node", id, "err", err)
		}
	}
}

// handleNodePkgSecrets is the node's pull: the secrets meant for it, decrypted, and "" for
// each one deleted. It is node-authenticated and bound to the path, like the install verify.
func (m *Module) handleNodePkgSecrets(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	if n.ID != r.PathValue("node_id") {
		detail(w, http.StatusForbidden, "Node mismatch")
		return
	}
	hh := ""
	if n.row != nil && n.row.householdID.Valid {
		hh = n.row.householdID.String
	}
	out := map[string]string{}
	if hh != "" {
		rows, err := m.deps.DB.Read.QueryContext(r.Context(), `SELECT secret_key, value_enc, node_ids FROM cc_package_secrets
			WHERE household_id = ?`, hh)
		if err != nil {
			m.internalError(w, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			var enc, ids sql.NullString
			if err := rows.Scan(&key, &enc, &ids); err != nil {
				m.internalError(w, err)
				return
			}
			if t := targets(ids); t != nil && !slices.Contains(t, n.ID) {
				continue
			}
			if !enc.Valid {
				out[key] = "" // deleted
				continue
			}
			v, err := m.atRest().decrypt(enc.String)
			if err != nil {
				m.deps.Log.Error("cc: package secret unreadable", "key", key, "err", err)
				continue
			}
			out[key] = v
		}
		if err := rows.Err(); err != nil {
			m.internalError(w, err)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"secrets": out})
}

// cleanupPkgSecrets drops tombstones older than pkgSecretTombstone (a node offline longer
// keeps its copy, as a node does for any push it missed).
func (m *Module) cleanupPkgSecrets(ctx context.Context, now time.Time) error {
	_, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_package_secrets WHERE value_enc IS NULL AND updated_at < ?`,
		dbTime(now.Add(-pkgSecretTombstone)))
	return err
}
