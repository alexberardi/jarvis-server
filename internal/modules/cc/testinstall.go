package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Forge test install (docs/cc/12-packages-and-command-data.md §3.3; legacy
// jarvis-command-center app/api/test_install.py). A developer in Pantry's AI Forge gets a
// 6-character share code; mobile sends it here with a node; CC validates the code against
// Pantry, keeps a request row and nudges the node with only the request_id. The node verifies
// (the zero-trust gate: it installs only what verify returns, for a row CC created for it),
// downloads the draft from Pantry itself, installs it as a temporary test command and posts
// the result; mobile polls.
//
// Ported after all (user 2026-10-08, reversing D5's drop). The row reuses the package
// request's state machine (verifyTransition / resultTransition / pollTransition) minus
// `restarting`, so it gets the same deliberate fixes: D39 pickup/verify deadlines, D8 sticky
// terminal status, D4 node credential bound to {node_id}, and the household check on the poll.

// pantryDraftTimeout is legacy's httpx.Client(timeout=10.0) for the share-code check.
const pantryDraftTimeout = 10 * time.Second

// opTestInstall labels the result route's 404/410 details ("Test install request …").
var opTestInstall = pkgOp{"test-install", "Test install"}

// tiRequest is a test_install_requests row. The embedded pkgRequest carries the shared
// lifecycle fields (id, node, household, status, results, deadlines); its command/repo/tag
// fields are unused here.
type tiRequest struct {
	pkgRequest
	shareCode, packageName string
}

const tiCols = `id, node_id, household_id, share_code, package_name, status, results_json, error_message,
	created_at, verified_at, expires_at, completed_at`

func scanTI(s scanner) (*tiRequest, error) {
	var t tiRequest
	var created, expires string
	if err := s.Scan(&t.id, &t.nodeID, &t.householdID, &t.shareCode, &t.packageName, &t.status, &t.resultsJSON,
		&t.errorMessage, &created, &t.verifiedAt, &expires, &t.completedAt); err != nil {
		return nil, err
	}
	t.createdAt, t.expiresAt = parseTS(created), parseTS(expires)
	return &t, nil
}

func (m *Module) registerTestInstall(mux *http.ServeMux) {
	const base = "/api/v0/nodes/{node_id}/test-install"
	mux.HandleFunc("POST "+base, m.handleRequestTestInstall)
	mux.HandleFunc("GET "+base+"/{request_id}/verify", m.node(m.handleVerifyTestInstall))
	mux.HandleFunc("POST "+base+"/{request_id}/results", m.node(m.handleTestInstallResults))
	mux.HandleFunc("GET "+base+"/{request_id}", m.handlePollTestInstall)
}

// --- store ---

func loadTI(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, rid, nodeID string) (*tiRequest, error) {
	return scanTI(q.QueryRowContext(ctx, `SELECT `+tiCols+` FROM cc_test_install_requests WHERE id = ? AND node_id = ?`, rid, nodeID))
}

// transitionTI loads a row and applies fn in one write transaction (see transitionPkg).
func (m *Module) transitionTI(ctx context.Context, rid, nodeID string, fn func(*pkgRequest) bool) (*tiRequest, error) {
	var out *tiRequest
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		t, err := loadTI(ctx, tx, rid, nodeID)
		if err != nil {
			return err
		}
		out = t
		if !fn(&t.pkgRequest) {
			return nil
		}
		_, err = tx.ExecContext(ctx, `UPDATE cc_test_install_requests SET status = ?, results_json = ?, error_message = ?,
			verified_at = ?, expires_at = ?, completed_at = ? WHERE id = ?`,
			t.status, t.resultsJSON, t.errorMessage, t.verifiedAt, dbTime(t.expiresAt), t.completedAt, t.id)
		return err
	})
	return out, err
}

// --- Pantry ---

// pantryDraftURL is where a share code's draft lives; the node downloads from the same URL.
func pantryDraftURL(base, code string) string {
	return strings.TrimRight(base, "/") + "/v1/forge/drafts/" + url.PathEscape(code)
}

// fetchDraftPackage is legacy's one CC→Pantry call: GET the draft (unauthenticated, the share
// code is the secret) and take its package_name. Errors are the HTTP answers mobile shows.
func (m *Module) fetchDraftPackage(ctx context.Context, draftURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, pantryDraftTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, draftURL, nil)
	if err != nil {
		return "", fail(http.StatusBadGateway, "Could not reach Pantry service")
	}
	resp, err := m.pantryHTTP.Do(req)
	if err != nil {
		m.deps.Log.Warn("cc: Pantry unreachable for test install", "url", draftURL, "err", err)
		return "", fail(http.StatusBadGateway, "Could not reach Pantry service")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", fail(http.StatusNotFound, "Share code not found or expired")
	case resp.StatusCode != http.StatusOK:
		m.deps.Log.Warn("cc: Pantry refused a test install draft", "url", draftURL, "status", resp.StatusCode)
		return "", fail(http.StatusBadGateway, "Pantry returned an error")
	}
	var draft map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&draft); err != nil {
		m.deps.Log.Warn("cc: Pantry draft is not a JSON object", "url", draftURL, "err", err)
		return "", fail(http.StatusBadGateway, "Pantry returned an error")
	}
	if name, ok := draft["package_name"].(string); ok {
		return name, nil
	}
	return "unknown", nil // draft.get("package_name", "unknown")
}

// --- create (mobile) ---

func (m *Module) handleRequestTestInstall(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok {
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	raw, _ := b.str("share_code", true)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	nodeID := r.PathValue("node_id")
	node, err := m.nodeByID(ctx, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Node not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	hh := ""
	if node.householdID.Valid {
		hh = node.householdID.String
	}
	if err := m.requirePkgHousehold(ctx, auth, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	if !m.requirePantry(w, r, hh) {
		return
	}
	code := strings.ToUpper(strings.TrimSpace(raw))
	if len([]rune(code)) != 6 {
		detail(w, http.StatusBadRequest, "Invalid share code")
		return
	}
	pkgName, err := m.fetchDraftPackage(ctx, pantryDraftURL(m.PantryBaseURL(ctx, hh), code))
	if err != nil {
		m.writeErr(w, err)
		return
	}

	now := m.now()
	t := &tiRequest{shareCode: code, packageName: pkgName}
	t.id, t.nodeID, t.householdID, t.status = uuid4(), nodeID, hh, pkgPending
	t.createdAt, t.expiresAt = now, now.Add(pkgPickupTTL)
	if _, err := m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_test_install_requests (id, node_id, household_id,
		share_code, package_name, status, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.id, t.nodeID, t.householdID, t.shareCode, t.packageName, t.status, dbTime(now), dbTime(t.expiresAt)); err != nil {
		m.internalError(w, err)
		return
	}
	m.deps.Log.Info("cc: test install requested", "node", nodeID, "package", pkgName, "request_id", t.id)

	// The nudge carries only the request_id: no code, files or URLs (invariant 11). Like
	// legacy, a missing broker or failed publish is logged and the request still returns 201.
	if err := m.bus.Publish(nodeID, "test-install", map[string]any{"request_id": t.id}); err != nil {
		m.deps.Log.Warn("cc: test install not delivered", "node", nodeID, "err", err)
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"id": t.id, "status": t.status, "package_name": t.packageName, "created_at": pyNaive(t.createdAt)})
}

// --- node callbacks (D4: node auth bound to {node_id}) ---

func (m *Module) handleVerifyTestInstall(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	nodeID := r.PathValue("node_id")
	if n.ID != nodeID {
		detail(w, http.StatusForbidden, "Node mismatch")
		return
	}
	var out pkgOutcome
	t, err := m.transitionTI(r.Context(), r.PathValue("request_id"), nodeID, func(p *pkgRequest) bool {
		out = verifyTransition(p, m.now())
		return out.changed
	})
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Test install request not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	switch out.status {
	case http.StatusOK:
	case http.StatusGone:
		detail(w, out.status, "Test install request expired")
		return
	default:
		detail(w, out.status, out.detail)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"confirmed":           true,
		"package_name":        t.packageName,
		"pantry_download_url": pantryDraftURL(m.PantryBaseURL(r.Context(), t.householdID), t.shareCode),
	})
}

func (m *Module) handleTestInstallResults(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	nodeID := r.PathValue("node_id")
	if n.ID != nodeID {
		detail(w, http.StatusForbidden, "Node mismatch")
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	var res pkgResult // no `restarting` for a test install; extra keys are ignored, as pydantic did
	if !b.has("success") {
		b.fail("success", "Field required")
	} else if v, ok := laxBool(b.m["success"]); ok {
		res.success = v
	} else {
		b.fail("success", "Input should be a valid boolean")
	}
	res.err, _ = b.optStrPtr("error")
	res.details, _ = b.object("details", false)
	if !b.done(w) {
		return
	}
	var out pkgOutcome
	t, err := m.transitionTI(r.Context(), r.PathValue("request_id"), nodeID, func(p *pkgRequest) bool {
		out = resultTransition(p, res, opTestInstall, m.now())
		return out.changed
	})
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Test install request not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if out.status != http.StatusOK {
		detail(w, out.status, out.detail)
		return
	}
	if out.changed {
		m.deps.Log.Info("cc: test install result", "request_id", t.id, "status", t.status, "package", t.packageName)
		if t.sticky() {
			m.cmdData.invalidateNode(nodeID) // the node's command set changed (D40 12.Q10)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// --- mobile poll ---

func (m *Module) handlePollTestInstall(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	rid, nodeID := r.PathValue("request_id"), r.PathValue("node_id")
	t, err := loadTI(ctx, m.deps.DB.Read, rid, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Test install request not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	// Legacy had no household check here (§8); Go applies the same rule as create.
	if err := m.requirePkgHousehold(ctx, auth, t.householdID); err != nil {
		m.writeErr(w, err)
		return
	}
	if t.live() && t.pastExpiry(m.now()) {
		t, err = m.transitionTI(ctx, rid, nodeID, func(p *pkgRequest) bool { return pollTransition(p, m.now()) })
		if err != nil {
			m.internalError(w, err)
			return
		}
	}
	// TestInstallPollResponse: every key present; details only when completed.
	out := map[string]any{"status": t.status, "request_id": t.id, "package_name": t.packageName,
		"error_message": nil, "details": nil}
	switch t.status {
	case pkgExpired:
		out["error_message"] = "Test install request expired — node may be offline"
	case pkgFailed:
		out["error_message"] = nullable(t.errorMessage)
	case pkgCompleted:
		if t.resultsJSON.Valid && t.resultsJSON.String != "" {
			var v any
			if json.Unmarshal([]byte(t.resultsJSON.String), &v) == nil {
				out["details"] = v
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// cleanupTestInstalls sweeps request rows after 30 days, as for package requests (D40 12.Q7).
func (m *Module) cleanupTestInstalls(ctx context.Context, now time.Time) error {
	_, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_test_install_requests WHERE created_at < ?`,
		dbTime(now.Add(-pkgRequestRetained)))
	if err != nil {
		return fmt.Errorf("test install request sweep: %w", err)
	}
	return nil
}
