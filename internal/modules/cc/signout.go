package cc

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Device sign-out (docs/decisions/2026-10-14-device-sign-out.md).
//
// Signing a device out removes the node and revokes its credentials, as DELETE /admin/nodes/{id}
// does, with two differences for a desktop agent:
//
//   - no factory reset. A Pi is still sent one (it wipes itself); a desktop agent must never be
//     told to wipe a person's computer.
//   - the node is revoked first, then told "signed-out" over MQTT. The message carries nothing
//     the agent trusts: it only makes the agent check its key at once (a heartbeat), and the key
//     already fails, so a spoofed message can't sign a working agent out. An agent that was
//     offline finds out at its next connection.
//
// Who may sign a device out: the member who paired it (owner_user_id, while still a member of
// its household) or an admin of its household. The agent itself may sign itself out with its
// own key (the status icon's "Sign out of Jarvis Automation").

const (
	errDesktopNoReset = "A desktop agent has no factory reset. Sign it out instead."
	errSignOutRole    = "Only the member who added this device, or a household admin, can sign it out"
)

// desktop: the node said it is the desktop agent (heartbeat node_kind, or its install mode).
func (n *nodeRow) desktop() bool {
	return (n.nodeKind.Valid && n.nodeKind.String == "desktop") ||
		(n.installMode.Valid && n.installMode.String == "desktop-agent")
}

// removeNode deletes a node and revokes its credentials. A Pi is sent the factory reset first
// (§7.6: publish before revoking); a desktop agent is revoked, then sent "signed-out".
func (m *Module) removeNode(ctx context.Context, n *nodeRow) error {
	id := n.nodeID
	desktop := n.desktop()
	if !desktop {
		tok := randHex(16)
		m.resets.add(tok, m.now().Add(deleteResetTTL))
		if err := m.bus.Publish(id, "factory-reset", map[string]any{"request_id": tok, "node_id": id}); err != nil {
			m.deps.Log.Warn("cc: factory-reset not published; the node will not auto-reset", "node", id, "err", err)
		}
	}
	if err := m.revokeNode(ctx, id); err != nil {
		return err
	}
	if desktop {
		if err := m.bus.Publish(id, "signed-out", map[string]any{"node_id": id}); err != nil {
			m.deps.Log.Info("cc: signed-out not published; the agent finds out at its next connection", "node", id, "err", err)
		}
	}
	return nil
}

// revokeNode deactivates the node in auth (its key stops working for HTTP and the broker) and
// hard-deletes its rows (doc 05 §3.4 step 5).
func (m *Module) revokeNode(ctx context.Context, id string) error {
	m.deactivate(ctx, id)
	return m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			`DELETE FROM cc_auth_sessions WHERE node_id = ?`,
			`DELETE FROM cc_config_pushes WHERE node_id = ?`,
			`DELETE FROM cc_settings_snapshots WHERE node_id = ?`,
			`DELETE FROM cc_settings_requests WHERE node_id = ?`,
			`DELETE FROM cc_nodes WHERE node_id = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// canSignOut: the owner while still a member of the node's household, or a household admin.
// A node without a household is the superuser's (requireNodeAccess's rule, D8).
func (m *Module) canSignOut(ctx context.Context, u authn.User, n *nodeRow) error {
	if !n.householdID.Valid || n.householdID.String == "" {
		if u.IsSuperuser {
			return nil
		}
		return fail(http.StatusForbidden, errSignOutRole)
	}
	role, member, err := m.Auth.HouseholdRole(ctx, u.ID, n.householdID.String)
	if err != nil {
		return err
	}
	if !member {
		return fail(http.StatusForbidden, "User is not a member of this household")
	}
	if n.ownerUserID.Valid && n.ownerUserID.Int64 == u.ID {
		return nil
	}
	if roleRank(role) >= roleRank(authn.RoleAdmin) {
		return nil
	}
	return fail(http.StatusForbidden, errSignOutRole)
}

// handleSignOutNode is POST /nodes/{node_id}/sign-out (user JWT): Add-ons' Sign out.
func (m *Module) handleSignOutNode(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	id := r.PathValue("node_id")
	n, err := m.nodeByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Node not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if err := m.canSignOut(ctx, u, n); err != nil {
		m.writeErr(w, err)
		return
	}
	if err := m.removeNode(ctx, n); err != nil {
		m.internalError(w, err)
		return
	}
	m.deps.Log.Info("cc: node signed out", "node", id, "user_id", u.ID, "desktop", n.desktop())
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"message": "Signed out", "node_id": id})
}

// handleSelfSignOut is POST /node/sign-out (the node's own key): the agent's "Sign out of Jarvis
// Automation". Nothing is published back: the caller is the one leaving.
func (m *Module) handleSelfSignOut(w http.ResponseWriter, r *http.Request, nc *nodeCtx) {
	ctx := r.Context()
	n := nc.row
	if err := m.revokeNode(ctx, n.nodeID); err != nil {
		m.internalError(w, err)
		return
	}
	m.deps.Log.Info("cc: node signed itself out", "node", n.nodeID)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"message": "Signed out", "node_id": n.nodeID})
}
