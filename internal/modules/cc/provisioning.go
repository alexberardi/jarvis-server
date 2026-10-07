package cc

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"crypto/rand"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// provisioningTTL: a provisioning token lives 10 minutes (provisioning.py).
const provisioningTTL = 600 * time.Second

func hashToken(raw string) string {
	s := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(s[:])
}

// tokenURLSafe is Python's secrets.token_urlsafe(n).
func tokenURLSafe(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// handleProvisioningToken is POST /provisioning/token (admin key or JWT). D4/D5: a JWT caller
// must be a member of the TARGET household, among all of their memberships.
func (m *Module) handleProvisioningToken(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok {
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	hh, _ := b.str("household_id", true)
	room, hasRoom := b.str("room", false)
	name, hasName := b.str("name", false)
	nodeID, _ := b.str("node_id", false)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	if !auth.admin {
		if err := m.requireRole(ctx, auth.user.ID, hh, authn.RoleMember); err != nil {
			m.writeErr(w, err)
			return
		}
	}
	now := m.now()
	raw := "prov_" + tokenURLSafe(32)
	expires := now.Add(provisioningTTL)
	var createdBy any
	if !auth.admin {
		createdBy = auth.user.ID
	}
	opt := func(s string, has bool) any {
		if !has {
			return nil
		}
		return s
	}
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if nodeID != "" {
			// Refresh: reuse the id, unless the node already registered.
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cc_nodes WHERE node_id = ?`, nodeID).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return fail(http.StatusBadRequest, "Node already registered")
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM cc_provisioning_tokens WHERE node_id = ? AND consumed_at IS NULL`, nodeID); err != nil {
				return err
			}
		} else {
			nodeID = uuid4()
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO cc_provisioning_tokens
			(id, token_hash, node_id, household_id, room, name, created_by_user_id, expires_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid4(), hashToken(raw), nodeID, hh, opt(room, hasRoom), opt(name, hasName), createdBy, dbTime(expires), dbTime(now))
		return err
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"token": raw, "node_id": nodeID, "expires_at": pyNaive(expires), "expires_in": int(provisioningTTL.Seconds()),
	})
}

// handleRegister is POST /nodes/register: the node redeems its provisioning token.
func (m *Module) handleRegister(w http.ResponseWriter, r *http.Request) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	nodeID, _ := b.str("node_id", true)
	tok, _ := b.str("provisioning_token", true)
	room, _ := b.str("room", false)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	var tokenID, hh string
	var tokRoom, tokName sql.NullString
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT id, household_id, room, name FROM cc_provisioning_tokens
		WHERE node_id = ? AND token_hash = ? AND consumed_at IS NULL AND expires_at > ?`,
		nodeID, hashToken(tok), dbTime(m.now())).Scan(&tokenID, &hh, &tokRoom, &tokName)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusUnauthorized, "Invalid or expired provisioning token")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if _, err := m.nodeByID(ctx, nodeID); err == nil {
		detail(w, http.StatusBadRequest, "Node already registered")
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		m.internalError(w, err)
		return
	}
	name := nodeID
	if tokName.Valid && tokName.String != "" {
		name = tokName.String
	}
	key, err := m.registerWithAuth(ctx, nodeID, hh, name)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	// Room precedence: body > token > "default" (§3.1).
	if room == "" {
		room = "default"
		if tokRoom.Valid && tokRoom.String != "" {
			room = tokRoom.String
		}
	}
	err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := m.insertNode(ctx, tx, nodeID, room, "default", "brief", hh); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE cc_provisioning_tokens SET consumed_at = ? WHERE id = ?`, dbTime(m.now()), tokenID)
		return err
	})
	if err != nil {
		_ = m.Nodes.DeactivateNode(ctx, nodeID)
		m.internalError(w, err)
		return
	}
	m.deps.Log.Info("cc: node registered via provisioning", "node", nodeID, "room", room)
	httpx.WriteJSON(w, http.StatusCreated, createdNode(nodeID, room, "default", "brief", key))
}
