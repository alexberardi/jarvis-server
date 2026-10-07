package cc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/mqtt"
)

// Auth modes (doc 00 §3.1). Every guard keeps the legacy status codes and detail strings,
// which the frozen node and mobile clients see.

// nodeCtx is an authenticated node (verify_api_key's NodeContextProvider).
type nodeCtx struct {
	ID                 string
	HouseholdID        string // from auth, as legacy
	HouseholdMemberIDs []int64
	Key                string // the node key it authenticated with (its broker password, D4)
	row                *nodeRow
}

type (
	nodeHandler func(w http.ResponseWriter, r *http.Request, n *nodeCtx)
	userHandler func(w http.ResponseWriter, r *http.Request, u authn.User)
)

// livenessDebounce: an authenticated request refreshes last_seen at most once a minute (§3.7).
const livenessDebounce = 60 * time.Second

// authNode is verify_api_key: X-API-Key "node_id:node_key", validated in process against the
// auth module for CC's service grant, plus a local cc_nodes row. No positive cache (D49), no
// bare-key fallback (D40 05.Q10).
func (m *Module) authNode(w http.ResponseWriter, r *http.Request) (*nodeCtx, bool) {
	vals, present := r.Header["X-Api-Key"]
	if !present || len(vals) == 0 {
		validationError(w, "header -> x-api-key: Field required")
		return nil, false
	}
	id, key, ok := authn.NodeKey(vals[0])
	if !ok {
		detail(w, http.StatusUnauthorized, "Invalid API Key")
		return nil, false
	}
	ctx := r.Context()
	v, err := m.Auth.ValidateNode(ctx, id, key, m.serviceID())
	if err != nil {
		m.internalError(w, err)
		return nil, false
	}
	if !v.Valid {
		reason := v.Reason
		if reason == "" {
			reason = "Invalid API Key"
		}
		detail(w, http.StatusUnauthorized, reason)
		return nil, false
	}
	row, err := m.nodeByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusUnauthorized, "Node not configured locally")
		return nil, false
	}
	if err != nil {
		m.internalError(w, err)
		return nil, false
	}
	m.touchLastSeen(ctx, row)
	return &nodeCtx{ID: id, HouseholdID: v.Node.HouseholdID, HouseholdMemberIDs: v.HouseholdMemberIDs, Key: key, row: row}, true
}

func (m *Module) node(h nodeHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if n, ok := m.authNode(w, r); ok {
			h(w, r, n)
		}
	}
}

// touchLastSeen is touch_node_last_seen: best effort, debounced, never fails the request.
func (m *Module) touchLastSeen(ctx context.Context, row *nodeRow) {
	now := m.now()
	if row.lastSeen.Valid && now.Sub(parseTS(row.lastSeen.String)) < livenessDebounce {
		return
	}
	if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_nodes SET last_seen = ? WHERE node_id = ?`, dbTime(now), row.nodeID); err != nil {
		m.deps.Log.Debug("cc: liveness write failed", "node", row.nodeID, "err", err)
		return
	}
	row.lastSeen = sql.NullString{String: dbTime(now), Valid: true}
}

// recordSeen refreshes last_seen for a node with no request context (MQTT round trips).
func (m *Module) recordSeen(nodeID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	row, err := m.nodeByID(ctx, nodeID)
	if err == nil {
		m.touchLastSeen(ctx, row)
	}
}

// authUser is verify_user_jwt, verified in process by the auth module (HS256 and RS256 by
// algorithm family, D49 00.Q2).
func (m *Module) authUser(w http.ResponseWriter, r *http.Request) (authn.User, bool) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		w.Header().Set("WWW-Authenticate", "Bearer")
		detail(w, http.StatusUnauthorized, "Missing or invalid Authorization header")
		return authn.User{}, false
	}
	u, err := m.Users.VerifyUser(r.Context(), h[len("Bearer "):])
	switch {
	case err == nil:
		return u, true
	case errors.Is(err, authn.ErrExpired):
		detail(w, http.StatusUnauthorized, "Token has expired")
	case errors.Is(err, authn.ErrNoSub):
		detail(w, http.StatusUnauthorized, "Invalid token: missing user ID")
	case errors.Is(err, authn.ErrInvalid):
		detail(w, http.StatusUnauthorized, "Invalid token")
	default:
		m.internalError(w, err)
	}
	return authn.User{}, false
}

func (m *Module) user(h userHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u, ok := m.authUser(w, r); ok {
			h(w, r, u)
		}
	}
}

// admin is verify_admin_key: X-API-Key == ADMIN_API_KEY, constant time; unset rejects all.
func (m *Module) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vals, present := r.Header["X-Api-Key"]
		if !present || len(vals) == 0 {
			validationError(w, "header -> x-api-key: Field required")
			return
		}
		if !authn.Equal(vals[0], m.AdminKey) {
			detail(w, http.StatusUnauthorized, "Invalid Admin API Key")
			return
		}
		h(w, r)
	}
}

// provAuth is verify_provisioning_auth: the admin key, or a user JWT.
type provAuth struct {
	admin bool
	user  authn.User
}

func (m *Module) authProvisioning(w http.ResponseWriter, r *http.Request) (provAuth, bool) {
	if key := r.Header.Get("X-API-Key"); key != "" {
		if authn.Equal(key, m.AdminKey) {
			return provAuth{admin: true}, true
		}
		detail(w, http.StatusUnauthorized, "Invalid API key")
		return provAuth{}, false
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		u, err := m.Users.VerifyUser(r.Context(), h[len("Bearer "):])
		if err == nil {
			return provAuth{user: u}, true
		}
		if !errors.Is(err, authn.ErrInvalid) && !errors.Is(err, authn.ErrExpired) && !errors.Is(err, authn.ErrNoSub) {
			m.internalError(w, err)
			return provAuth{}, false
		}
		detail(w, http.StatusUnauthorized, "Invalid or expired JWT")
		return provAuth{}, false
	}
	detail(w, http.StatusUnauthorized, "Authentication required")
	return provAuth{}, false
}

// --- household roles (verify_household_role, in process) ---

func roleRank(r authn.Role) int {
	switch r {
	case authn.RoleOwner:
		return 3
	case authn.RoleAdmin:
		return 2
	case authn.RolePowerUser:
		return 1
	}
	return 0
}

// statusErr is a legacy HTTPException: status + detail.
type statusErr struct {
	status int
	detail any
}

func (e *statusErr) Error() string { return fmt.Sprint(e.detail) }

func fail(status int, d any) error { return &statusErr{status, d} }

// requireRole checks the user's role in the TARGET household, among all of their memberships
// (D5), with jarvis-auth's validate-household-access reasons.
func (m *Module) requireRole(ctx context.Context, userID int64, householdID string, req authn.Role) error {
	role, member, err := m.Auth.HouseholdRole(ctx, userID, householdID)
	if err != nil {
		return err
	}
	if !member {
		return fail(http.StatusForbidden, "User is not a member of this household")
	}
	if roleRank(role) < roleRank(req) {
		return fail(http.StatusForbidden, fmt.Sprintf("User has %s role, requires %s or higher", role, req))
	}
	return nil
}

// requireNodeAccess loads a node and checks the user's role in its household (404 for an
// unknown node). noHousehold is the 403 detail for a node without a household, which only a
// superuser may touch (D8: no authz skip on a NULL household).
func (m *Module) requireNodeAccess(ctx context.Context, u authn.User, nodeID string, req authn.Role, noHousehold string) (*nodeRow, error) {
	n, err := m.nodeByID(ctx, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fail(http.StatusNotFound, "Node not found")
	}
	if err != nil {
		return nil, err
	}
	if !n.householdID.Valid || n.householdID.String == "" {
		if u.IsSuperuser {
			return n, nil
		}
		return nil, fail(http.StatusForbidden, noHousehold)
	}
	if err := m.requireRole(ctx, u.ID, n.householdID.String, req); err != nil {
		return nil, err
	}
	return n, nil
}

// writeErr writes a statusErr, or a 500.
func (m *Module) writeErr(w http.ResponseWriter, err error) {
	var se *statusErr
	if errors.As(err, &se) {
		detail(w, se.status, se.detail)
		return
	}
	m.internalError(w, err)
}

func (m *Module) internalError(w http.ResponseWriter, err error) {
	m.deps.Log.Error("cc: internal error", "err", err)
	detail(w, http.StatusInternalServerError, "Internal Server Error")
}

// --- broker credentials (D4) ---

// brokerAuth authenticates MQTT clients. Decision (doc 05 §11, D4): a node's broker
// credential is username = its node_id, password = its node_key, checked in process against
// the auth module with CC's service grant. Nothing new is stored: revocation, key rotation
// and deactivation apply to the broker immediately on the next connect, and a node holding a
// stale password self-heals by re-fetching /node/mqtt-credentials (its CONNACK 4/5 path).
// The key already travels on every HTTP request the node makes, so this adds no exposure.
type brokerAuth struct {
	auth      authn.Authority
	serviceID string
}

func (a brokerAuth) Authenticate(username, password string) (mqtt.Principal, bool) {
	if username == "" || password == "" {
		return mqtt.Principal{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v, err := a.auth.ValidateNode(ctx, username, password, a.serviceID)
	if err != nil || !v.Valid {
		return mqtt.Principal{}, false
	}
	return mqtt.Principal{Kind: mqtt.Node, NodeID: v.Node.ID}, true
}
