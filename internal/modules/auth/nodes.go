package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// --- node rows ---

type node struct {
	nodeID       string
	name         string
	householdID  string
	registeredBy sql.NullInt64
	isActive     bool
	createdAt    string
	updatedAt    string
	lastRotated  sql.NullString
	keyHash      string
}

const nodeCols = `node_id, name, household_id, registered_by_user_id, is_active, created_at, updated_at,
	last_rotated_at, node_key_hash`

func scanNode(sc interface{ Scan(...any) error }) (*node, error) {
	var n node
	if err := sc.Scan(&n.nodeID, &n.name, &n.householdID, &n.registeredBy, &n.isActive, &n.createdAt,
		&n.updatedAt, &n.lastRotated, &n.keyHash); err != nil {
		return nil, err
	}
	return &n, nil
}

func nodeByID(ctx context.Context, q queryer, id string) (*node, error) {
	return scanNode(q.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM auth_node_registrations WHERE node_id = ?`, id))
}

type serviceAccess struct {
	serviceID string
	grantedAt string
	grantedBy sql.NullInt64
}

func nodeServices(ctx context.Context, q queryer, nodeID string) ([]serviceAccess, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT service_id, granted_at, granted_by FROM auth_node_service_access WHERE node_id = ? ORDER BY id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []serviceAccess
	for rows.Next() {
		var s serviceAccess
		if err := rows.Scan(&s.serviceID, &s.grantedAt, &s.grantedBy); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// listItem is NodeListItem (services as ids).
func (n *node) listItem(svcs []serviceAccess) map[string]any {
	ids := make([]string, 0, len(svcs))
	for _, s := range svcs {
		ids = append(ids, s.serviceID)
	}
	return map[string]any{
		"node_id": n.nodeID, "name": n.name, "household_id": n.householdID,
		"registered_by_user_id": nullInt(n.registeredBy), "is_active": n.isActive,
		"created_at": pyTime(n.createdAt), "updated_at": pyTime(n.updatedAt),
		"last_rotated_at": pyTimeNull(n.lastRotated), "services": ids,
	}
}

// listNodes renders NodeListItems for the nodes matching where (after "WHERE"), in order.
func (m *Module) listNodes(ctx context.Context, where, order string, args ...any) ([]map[string]any, error) {
	rows, err := m.deps.DB.Read.QueryContext(ctx,
		`SELECT `+nodeCols+` FROM auth_node_registrations WHERE `+where+` ORDER BY `+order, args...)
	if err != nil {
		return nil, err
	}
	var nodes []*node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		nodes = append(nodes, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		svcs, err := nodeServices(ctx, m.deps.DB.Read, n.nodeID)
		if err != nil {
			return nil, err
		}
		out = append(out, n.listItem(svcs))
	}
	return out, nil
}

// nodeInput is NodeCreateRequest / NodeRegisterInternalRequest.
type nodeInput struct {
	nodeID, householdID, name string
	registeredBy              *int64
	services                  []string
}

func readNodeInput(w http.ResponseWriter, r *http.Request) (nodeInput, bool) {
	b, ok := readBody(w, r, false)
	if !ok {
		return nodeInput{}, false
	}
	var in nodeInput
	in.nodeID, _ = b.str("node_id", true, 0, 0)
	in.householdID, _ = b.str("household_id", true, 0, 0)
	in.name, _ = b.str("name", true, 0, 0)
	in.registeredBy, _ = b.optInt("registered_by_user_id")
	in.services = b.strList("services")
	return in, b.done(w)
}

// insertNode registers a node with a fresh key (bcrypt-hashed, shown once) and its grants.
func insertNode(ctx context.Context, tx *sql.Tx, in nodeInput, hash string, registeredBy *int64, grants []string, grantedBy *int64) (string, error) {
	t := dbTime(now())
	_, err := tx.ExecContext(ctx, `
		INSERT INTO auth_node_registrations (node_id, node_key_hash, name, is_active, household_id,
			registered_by_user_id, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, ?, ?, ?)`, in.nodeID, hash, in.name, in.householdID, registeredBy, t, t)
	if isUnique(err) {
		return "", fail(http.StatusBadRequest, "node_id already exists")
	}
	if err != nil {
		return "", err
	}
	for _, s := range grants {
		// OR IGNORE: a repeated service id in the request is one grant, not a 500.
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO auth_node_service_access (node_id, service_id, granted_at, granted_by)
			VALUES (?, ?, ?, ?)`, in.nodeID, s, t, grantedBy); err != nil {
			return "", err
		}
	}
	return t, nil
}

// createNodeChecked is the admin/internal registration: the node id must be free, the household
// and the registering user must exist.
func (m *Module) createNodeChecked(ctx context.Context, in nodeInput, grants []string) (string, string, error) {
	key := tokenURLSafe(48)
	hash, err := hashSecret(key)
	if err != nil {
		return "", "", err
	}
	var created string
	err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := nodeByID(ctx, tx, in.nodeID); err == nil {
			return fail(http.StatusBadRequest, "node_id already exists")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, found, err := householdExists(ctx, tx, in.householdID); err != nil {
			return err
		} else if !found {
			return fail(http.StatusNotFound, "Household not found")
		}
		if in.registeredBy != nil {
			if n, err := count(ctx, tx, `SELECT COUNT(*) FROM auth_users WHERE id = ?`, *in.registeredBy); err != nil {
				return err
			} else if n == 0 {
				return fail(http.StatusNotFound, "User not found")
			}
		}
		var err error
		created, err = insertNode(ctx, tx, in, hash, in.registeredBy, grants, in.registeredBy)
		return err
	})
	return key, created, err
}

func createdNodeResponse(in nodeInput, registeredBy *int64, key, created string, services []string) map[string]any {
	if services == nil {
		services = []string{}
	}
	var reg any
	if registeredBy != nil {
		reg = *registeredBy
	}
	return map[string]any{
		"node_id": in.nodeID, "name": in.name, "household_id": in.householdID,
		"registered_by_user_id": reg, "node_key": key, "created_at": pyTime(created), "services": services,
	}
}

// --- admin_nodes.py ---

func (m *Module) handleAdminCreateNode(w http.ResponseWriter, r *http.Request) {
	in, ok := readNodeInput(w, r)
	if !ok {
		return
	}
	key, created, err := m.createNodeChecked(r.Context(), in, in.services)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, createdNodeResponse(in, in.registeredBy, key, created, in.services))
}

func (m *Module) handleAdminListNodes(w http.ResponseWriter, r *http.Request) {
	out, err := m.listNodes(r.Context(), "1=1", "id")
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) handleAdminGetNode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	n, err := nodeByID(ctx, m.deps.DB.Read, r.PathValue("node_id"))
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Node not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	svcs, err := nodeServices(ctx, m.deps.DB.Read, n.nodeID)
	if err != nil {
		m.internalError(w, err)
		return
	}
	out := n.listItem(nil)
	items := make([]map[string]any, 0, len(svcs))
	for _, s := range svcs {
		items = append(items, map[string]any{
			"service_id": s.serviceID, "granted_at": pyTime(s.grantedAt), "granted_by": nullInt(s.grantedBy),
		})
	}
	out["services"] = items
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleDeactivateNode is DELETE /admin/nodes/{id} and /internal/nodes/{id}: is_active=false,
// keeping the row for audit.
func (m *Module) handleDeactivateNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("node_id")
	res, err := m.deps.DB.Write.ExecContext(r.Context(),
		`UPDATE auth_node_registrations SET is_active = 0, updated_at = ? WHERE node_id = ?`, dbTime(now()), id)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		detail(w, http.StatusNotFound, "Node not found")
		return
	}
	m.verified.invalidate("node", id)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"node_id": id, "is_active": false})
}

func (m *Module) handleRotateNodeKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("node_id")
	key := tokenURLSafe(48)
	hash, err := hashSecret(key)
	if err != nil {
		m.internalError(w, err)
		return
	}
	t := now()
	res, err := m.deps.DB.Write.ExecContext(r.Context(), `UPDATE auth_node_registrations
		SET node_key_hash = ?, last_rotated_at = ?, updated_at = ? WHERE node_id = ?`, hash, dbTime(t), dbTime(t), id)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		detail(w, http.StatusNotFound, "Node not found")
		return
	}
	m.verified.invalidate("node", id)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"node_id": id, "node_key": key, "last_rotated_at": pyTimeOf(t)})
}

// handleGrantService is POST {admin,internal}/nodes/{id}/services.
func (m *Module) handleGrantService(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody(w, r, false)
	if !ok {
		return
	}
	svc, _ := b.str("service_id", true, 0, 0)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	id := r.PathValue("node_id")
	var granted string
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := nodeByID(ctx, tx, id); errors.Is(err, sql.ErrNoRows) {
			return fail(http.StatusNotFound, "Node not found")
		} else if err != nil {
			return err
		}
		granted = dbTime(now())
		_, err := tx.ExecContext(ctx, `INSERT INTO auth_node_service_access (node_id, service_id, granted_at)
			VALUES (?, ?, ?)`, id, svc, granted)
		if isUnique(err) {
			return fail(http.StatusBadRequest, "Node already has access to this service")
		}
		return err
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	m.verified.invalidate("node", id)
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"node_id": id, "service_id": svc, "granted_at": pyTime(granted)})
}

// handleRevokeService is DELETE {admin,internal}/nodes/{id}/services/{service_id}.
func (m *Module) handleRevokeService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("node_id")
	res, err := m.deps.DB.Write.ExecContext(r.Context(),
		`DELETE FROM auth_node_service_access WHERE node_id = ? AND service_id = ?`, id, r.PathValue("service_id"))
	if err != nil {
		m.internalError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		detail(w, http.StatusNotFound, "Service access not found")
		return
	}
	m.verified.invalidate("node", id)
	w.WriteHeader(http.StatusNoContent)
}

// --- internal.py node routes ---

// handleInternalRegisterNode is POST /internal/nodes/register: the calling app is always granted.
func (m *Module) handleInternalRegisterNode(w http.ResponseWriter, r *http.Request, a *appClient) {
	in, ok := readNodeInput(w, r)
	if !ok {
		return
	}
	grants := append([]string{}, in.services...)
	grants = append(grants, a.appID)
	key, _, err := m.createNodeChecked(r.Context(), in, grants)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"node_id": in.nodeID, "node_key": key})
}

func (m *Module) handleGrantServiceInternal(w http.ResponseWriter, r *http.Request, _ *appClient) {
	m.handleGrantService(w, r)
}

func (m *Module) handleRevokeServiceInternal(w http.ResponseWriter, r *http.Request, _ *appClient) {
	m.handleRevokeService(w, r)
}

func (m *Module) handleDeactivateNodeInternal(w http.ResponseWriter, r *http.Request, _ *appClient) {
	m.handleDeactivateNode(w, r)
}

// --- validation (shared by the HTTP route and authn.Authority) ---

// ValidateNode checks a node key and the node's access to serviceID, with the legacy
// /internal/validate-node semantics. Only the bcrypt proof is cached (verifiedCache); the
// active flag, grants and members are read on every call, so revocation is immediate.
func (m *Module) ValidateNode(ctx context.Context, nodeID, key, serviceID string) (authn.NodeValidation, error) {
	q := m.deps.DB.Read
	n, err := nodeByID(ctx, q, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return authn.NodeValidation{Reason: "Node not found"}, nil
	}
	if err != nil {
		return authn.NodeValidation{}, err
	}
	if !n.isActive {
		return authn.NodeValidation{Reason: "Node is inactive"}, nil
	}
	if !m.verified.check("node", n.nodeID, key, n.keyHash) {
		return authn.NodeValidation{Reason: "Invalid node credentials"}, nil
	}
	granted, err := count(ctx, q, `SELECT COUNT(*) FROM auth_node_service_access WHERE node_id = ? AND service_id = ?`, nodeID, serviceID)
	if err != nil {
		return authn.NodeValidation{}, err
	}
	if granted == 0 {
		return authn.NodeValidation{Reason: fmt.Sprintf("Node is not authorized to access service '%s'", serviceID)}, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT user_id FROM auth_household_memberships WHERE household_id = ? ORDER BY id`, n.householdID)
	if err != nil {
		return authn.NodeValidation{}, err
	}
	defer rows.Close()
	members := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return authn.NodeValidation{}, err
		}
		members = append(members, id)
	}
	if err := rows.Err(); err != nil {
		return authn.NodeValidation{}, err
	}
	return authn.NodeValidation{
		Valid: true, Node: authn.Node{ID: n.nodeID, HouseholdID: n.householdID}, HouseholdMemberIDs: members,
	}, nil
}

// handleValidateNode is POST /internal/validate-node. Failures are 200 with valid=false.
func (m *Module) handleValidateNode(w http.ResponseWriter, r *http.Request, _ *appClient) {
	b, ok := readBody(w, r, false)
	if !ok {
		return
	}
	id, _ := b.str("node_id", true, 0, 0)
	key, _ := b.str("node_key", true, 0, 0)
	svc, _ := b.str("service_id", true, 0, 0)
	if !b.done(w) {
		return
	}
	v, err := m.ValidateNode(r.Context(), id, key, svc)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if !v.Valid {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"valid": false, "node_id": nil, "household_id": nil, "household_member_ids": nil, "reason": v.Reason,
		})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"valid": true, "node_id": v.Node.ID, "household_id": v.Node.HouseholdID,
		"household_member_ids": v.HouseholdMemberIDs, "reason": nil,
	})
}

// ValidateApp checks app-to-app credentials (bcrypt proof cached like node keys).
func (m *Module) ValidateApp(ctx context.Context, appID, key string) (authn.App, bool, error) {
	if appID == "" || key == "" {
		return authn.App{}, false, nil
	}
	a, err := m.appByID(ctx, appID)
	if errors.Is(err, sql.ErrNoRows) {
		return authn.App{}, false, nil
	}
	if err != nil {
		return authn.App{}, false, err
	}
	if !a.isActive || !m.verified.check("app", a.appID, key, a.keyHash) {
		return authn.App{}, false, nil
	}
	return authn.App{ID: a.appID}, true, nil
}

// HouseholdRole returns the user's role in the given (target) household.
func (m *Module) HouseholdRole(ctx context.Context, userID int64, householdID string) (authn.Role, bool, error) {
	r, ok, err := membershipRole(ctx, m.deps.DB.Read, householdID, userID)
	return authn.Role(r), ok, err
}

// VerifyUser verifies an access token in-process and returns its principal. The user must
// still exist and be active; HouseholdID is the token's claim.
func (m *Module) VerifyUser(ctx context.Context, token string) (authn.User, error) {
	c, err := m.verify(ctx, token)
	if err != nil {
		return authn.User{}, err
	}
	id, err := c.UserID()
	if err != nil {
		return authn.User{}, err
	}
	u, err := m.userByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !u.isActive) {
		return authn.User{}, authn.ErrInvalid
	}
	if err != nil {
		return authn.User{}, err
	}
	return authn.User{ID: u.id, Email: u.email, IsSuperuser: u.isSuperuser, HouseholdID: c.HouseholdID}, nil
}
