package authn

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
)

// User is an authenticated person (user JWT).
type User struct {
	ID          int64
	Email       string
	IsSuperuser bool
	// HouseholdID is the token's current household. A user may belong to several
	// households; membership checks must use the target household, not this (docs/cc D5).
	HouseholdID string
}

// Node is an authenticated node (X-API-Key: node_id:node_key).
type Node struct {
	ID          string
	HouseholdID string
}

// App is an authenticated app-to-app caller (X-Jarvis-App-Id / X-Jarvis-App-Key).
type App struct {
	ID string
}

type ctxKey int

const (
	userKey ctxKey = iota
	nodeKey
	appKey
)

func WithUser(ctx context.Context, u User) context.Context { return context.WithValue(ctx, userKey, u) }
func WithNode(ctx context.Context, n Node) context.Context { return context.WithValue(ctx, nodeKey, n) }
func WithApp(ctx context.Context, a App) context.Context   { return context.WithValue(ctx, appKey, a) }

func UserFrom(ctx context.Context) (User, bool) { u, ok := ctx.Value(userKey).(User); return u, ok }
func NodeFrom(ctx context.Context) (Node, bool) { n, ok := ctx.Value(nodeKey).(Node); return n, ok }
func AppFrom(ctx context.Context) (App, bool)   { a, ok := ctx.Value(appKey).(App); return a, ok }

// BearerToken returns the token from "Authorization: Bearer <token>", or "".
func BearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	scheme, tok, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(tok)
}

// NodeKey parses "node_id:node_key". The legacy bare-key form (no colon) is not accepted
// (docs/cc D40, 05.Q10).
func NodeKey(header string) (nodeID, key string, ok bool) {
	nodeID, key, ok = strings.Cut(header, ":")
	if !ok || nodeID == "" || key == "" {
		return "", "", false
	}
	return nodeID, key, true
}

// AppCreds returns the app-to-app headers, or ok=false if either is missing.
func AppCreds(r *http.Request) (id, key string, ok bool) {
	id, key = r.Header.Get("X-Jarvis-App-Id"), r.Header.Get("X-Jarvis-App-Key")
	return id, key, id != "" && key != ""
}

// Equal compares secrets in constant time. An empty expected value never matches, so an
// unset admin token rejects everything.
func Equal(got, expected string) bool {
	if expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

// Role is a household membership role.
type Role string

const (
	RoleMember Role = "member"
	RoleAdmin  Role = "admin"
	RoleOwner  Role = "owner"
)

// Authority is what modules need from the auth module. It is implemented in-process by the
// auth module (Phase 1); modules depend on this interface, not on auth's internals.
type Authority interface {
	// ValidateNode checks a node key and returns the node; ok=false for unknown or bad keys.
	ValidateNode(ctx context.Context, nodeID, key string) (Node, bool, error)
	// ValidateApp checks app-to-app credentials.
	ValidateApp(ctx context.Context, appID, key string) (App, bool, error)
	// HouseholdRole returns the user's role in a household, or ok=false if not a member.
	HouseholdRole(ctx context.Context, userID int64, householdID string) (Role, bool, error)
}
