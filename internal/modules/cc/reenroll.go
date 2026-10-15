package cc

import (
	"context"
	"database/sql"
	"regexp"
)

// Re-enrolling a computer replaces its node (docs/decisions/2026-10-15-reenroll-replaces-node.md).
//
// The desktop agent sends device_id, its stable identity for one computer and OS user, when it
// registers and in heartbeats (cc migration 00190). When a desktop registers, its owner's older
// desktop nodes in the same household with the same device_id are removed the way Sign out and
// Delete remove a node (removeNode: revoked in auth, rows hard-deleted, "signed-out" published),
// so purging and setting up the agent again doesn't leave a dead node in every picker.
//
// The scope is what keeps a forged device_id harmless: only nodes the same member paired
// (owner_user_id, the provisioning token's minter) in the same household, and only desktop
// nodes. That member could sign those nodes out anyway. A Pi, a node created with the admin key
// (no owner), another member's computer and another household's node are never touched.

// deviceIDPattern bounds a reported device_id: the agent sends 64 hex characters; anything that
// isn't a short opaque token is ignored rather than stored.
var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// deviceIDValue is a reported device_id when it is well formed, else nil (not stored).
func deviceIDValue(s string) *string {
	if !deviceIDPattern.MatchString(s) {
		return nil
	}
	return &s
}

// retirePreviousNodes removes the nodes newID replaces: the owner's other desktop nodes in hh
// with the same device id. Best effort: newID is already registered and its key handed out, so
// a failure here is logged and the old node stays, as it did before device ids.
func (m *Module) retirePreviousNodes(ctx context.Context, newID, hh string, owner int64, deviceID string) {
	ctx = context.WithoutCancel(ctx)
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT `+nodeCols+` FROM cc_nodes
		WHERE household_id = ? AND device_id = ? AND owner_user_id = ? AND node_id != ?`, hh, deviceID, owner, newID)
	if err != nil {
		m.deps.Log.Error("cc: looking for the node a re-enrolment replaces", "node", newID, "err", err)
		return
	}
	var old []*nodeRow
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			m.deps.Log.Error("cc: looking for the node a re-enrolment replaces", "node", newID, "err", err)
			break
		}
		if n.desktop() {
			old = append(old, n)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		m.deps.Log.Error("cc: looking for the node a re-enrolment replaces", "node", newID, "err", err)
	}
	for _, n := range old {
		if err := m.removeNode(ctx, n); err != nil {
			m.deps.Log.Error("cc: removing the node a re-enrolment replaces", "node", n.nodeID, "new_node", newID, "err", err)
			continue
		}
		m.deps.Log.Info("cc: node replaced by a re-enrolment of the same computer", "node", n.nodeID, "new_node", newID, "user_id", owner)
	}
}

// setNodeIdentity stores what a registering node said about itself.
func setNodeIdentity(ctx context.Context, tx *sql.Tx, nodeID string, deviceID, kind *string) error {
	if deviceID == nil && kind == nil {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE cc_nodes SET device_id = ?, node_kind = ? WHERE node_id = ?`,
		nullStr(deviceID), nullStr(kind), nodeID)
	return err
}
