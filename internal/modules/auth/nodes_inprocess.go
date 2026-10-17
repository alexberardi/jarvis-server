package auth

import (
	"context"
	"strings"
)

// StatusCode lets in-process callers map an auth failure onto the legacy HTTP status (e.g.
// command-center passing /internal/nodes/register's 400 and 404 details through).
func (e *httpErr) StatusCode() int { return e.status }

// RegisterNode is /internal/nodes/register in process: it creates the node in householdID
// with a fresh key (returned once) and grants it services. Client errors carry StatusCode:
// 400 "node_id already exists", 404 "Household not found".
func (m *Module) RegisterNode(ctx context.Context, nodeID, householdID, name string, services []string) (string, error) {
	in := nodeInput{nodeID: nodeID, householdID: householdID, name: name, services: services}
	key, _, err := m.createNodeChecked(ctx, in, services)
	return key, err
}

// DeactivateNode is DELETE /internal/nodes/{id} in process (is_active=false, row kept). A
// node auth doesn't know is not an error, matching the legacy callers that treat 404 as done.
func (m *Module) DeactivateNode(ctx context.Context, nodeID string) error {
	_, err := m.deps.DB.Write.ExecContext(ctx,
		`UPDATE auth_node_registrations SET is_active = 0, updated_at = ? WHERE node_id = ?`, dbTime(now()), nodeID)
	if err != nil {
		return err
	}
	m.verified.invalidate("node", nodeID)
	return nil
}

// RenameNode sets an active node's registered name, for command-center's heartbeat (the
// computer was renamed). The caller has authenticated the node and bounds the name; an
// unknown or deactivated node, or the same name, writes nothing.
func (m *Module) RenameNode(ctx context.Context, nodeID, name string) error {
	_, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE auth_node_registrations SET name = ?, updated_at = ?
		WHERE node_id = ? AND is_active = 1 AND name <> ?`, name, dbTime(now()), nodeID, name)
	return err
}

// NodeNames is each registered node's name, by node id, for command-center's node list. Ids
// auth doesn't know are absent; deactivated registrations still answer (the list can include
// inactive nodes).
func (m *Module) NodeNames(ctx context.Context, nodeIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(nodeIDs))
	if len(nodeIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(nodeIDs))
	for i, id := range nodeIDs {
		args[i] = id
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT node_id, name FROM auth_node_registrations
		WHERE node_id IN (?`+strings.Repeat(", ?", len(nodeIDs)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}
