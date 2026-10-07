package auth

import (
	"context"
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
