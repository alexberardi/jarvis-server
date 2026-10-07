package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Packages and command data (docs/cc/12-packages-and-command-data.md). CC is a broker and
// record-keeper here, never the executor: the node installs, uninstalls, reverts and stores
// command data; CC keeps request rows and relays round trips.
//
// Package install / uninstall / revert share cc_package_install_requests (no action column,
// D48) and the one verify route (the frozen node verifies all three through
// /package-install/{rid}/verify). Forge test install is dropped (D5).

// Request lifetimes (D39): a 5-minute pickup deadline until the node verifies, then
// verify + 15 min; each `restarting` result adds 120 s to the current expiry.
const (
	pkgPickupTTL       = 5 * time.Minute
	pkgVerifiedTTL     = 15 * time.Minute
	pkgRestartExtend   = 120 * time.Second
	pkgRequestRetained = 30 * 24 * time.Hour // D40 12.Q7: request rows are swept after 30 days
)

const (
	settingPantryBaseURL = "pantry.base_url"
	defaultPantryBaseURL = "https://pantry-api.jarvisautomation.io"
)

// packageDefinitions: D39 (12.Q8). The Pantry a household installs from; it must be reachable
// from the node, not just from jarvisd.
func packageDefinitions() []settings.Definition {
	return []settings.Definition{
		{Key: settingPantryBaseURL, Category: "pantry", Type: settings.String, Default: defaultPantryBaseURL,
			EnvFallback: "JARVIS_PANTRY_URL",
			Description: "Base URL of the Pantry package store (the public Pantry by default; point it at a " +
				"private Pantry to install from there). Nodes must be able to reach it."},
	}
}

// PantryBaseURL is the household's Pantry (D39, D48: consumed by the jarvis-pantry entry in
// /services and anything handed to nodes).
func (m *Module) PantryBaseURL(ctx context.Context, householdID string) string {
	if m.settings == nil {
		return defaultPantryBaseURL
	}
	if v := m.settings.String(ctx, settingPantryBaseURL, settings.Scope{HouseholdID: householdID}); v != "" {
		return v
	}
	return defaultPantryBaseURL
}

func (m *Module) registerPackages(mux *http.ServeMux) {
	m.cmdData = newSchemaCache(m.now)

	const v0 = "/api/v0"
	// Package install / uninstall / revert (package_install.py).
	mux.HandleFunc("POST "+v0+"/nodes/{node_id}/package-install", m.handleRequestInstall)
	mux.HandleFunc("GET "+v0+"/nodes/{node_id}/package-install/{request_id}/verify", m.node(m.handleVerifyPackage))
	mux.HandleFunc("GET "+v0+"/nodes/{node_id}/package-install/{request_id}", m.handlePollPackage(opInstall))
	mux.HandleFunc("POST "+v0+"/nodes/{node_id}/package-uninstall", m.handleRequestUninstall)
	mux.HandleFunc("GET "+v0+"/nodes/{node_id}/package-uninstall/{request_id}", m.handlePollPackage(opUninstall))
	mux.HandleFunc("POST "+v0+"/nodes/{node_id}/package-revert", m.handleRequestRevert)
	mux.HandleFunc("GET "+v0+"/nodes/{node_id}/package-revert/{request_id}", m.handlePollPackage(opRevert))
	for _, op := range []pkgOp{opInstall, opUninstall, opRevert} {
		mux.HandleFunc("POST "+v0+"/nodes/{node_id}/package-"+op.name+"/{request_id}/results", m.node(m.handlePackageResults(op)))
	}

	// Mobile command-data browser (mobile_command_data.py).
	const cd = v0 + "/mobile/command-data/nodes"
	mux.HandleFunc("GET "+cd, m.user(m.handleCDListNodes))
	mux.HandleFunc("GET "+cd+"/{node_id}/commands", m.user(m.handleCDCommands))
	mux.HandleFunc("GET "+cd+"/{node_id}/commands/{command_name}/schema", m.user(m.handleCDSchema))
	mux.HandleFunc("GET "+cd+"/{node_id}/commands/{command_name}/records", m.user(m.handleCDList))
	mux.HandleFunc("GET "+cd+"/{node_id}/commands/{command_name}/records/{key}", m.user(m.handleCDGet))
	mux.HandleFunc("POST "+cd+"/{node_id}/commands/{command_name}/records", m.user(m.handleCDCreate))
	mux.HandleFunc("PATCH "+cd+"/{node_id}/commands/{command_name}/records/{key}", m.user(m.handleCDUpdate))
	mux.HandleFunc("DELETE "+cd+"/{node_id}/commands/{command_name}/records/{key}", m.user(m.handleCDDelete))

	// Node tools view (node_tools.py). The node's report lands on the shared result sink,
	// POST /mobile/node-tool-reports/{rid} (node auth, rid bound to the node, D4).
	mux.HandleFunc("GET "+v0+"/mobile/nodes/{node_id}/tools", m.user(m.handleNodeTools))
}

// pkgOp names an operation for its routes, topics and legacy detail strings.
type pkgOp struct {
	name  string // URL and topic segment: install | uninstall | revert
	label string // "Install" | "Uninstall" | "Revert" (404/410 details)
}

var (
	opInstall   = pkgOp{"install", "Install"}
	opUninstall = pkgOp{"uninstall", "Uninstall"}
	opRevert    = pkgOp{"revert", "Revert"}
)

// --- the request row and its state machine (§3.2) ---

const (
	pkgPending    = "pending"
	pkgRestarting = "restarting"
	pkgCompleted  = "completed"
	pkgFailed     = "failed"
	pkgExpired    = "expired"
)

type pkgRequest struct {
	id, nodeID, householdID, commandName, repoURL string
	gitTag, resultsJSON, errorMessage             sql.NullString
	status                                        string
	createdAt, expiresAt                          time.Time
	verifiedAt, completedAt                       sql.NullString
}

const pkgCols = `id, node_id, household_id, command_name, github_repo_url, git_tag, status, results_json,
	error_message, created_at, verified_at, expires_at, completed_at`

func scanPkg(s scanner) (*pkgRequest, error) {
	var p pkgRequest
	var created, expires string
	if err := s.Scan(&p.id, &p.nodeID, &p.householdID, &p.commandName, &p.repoURL, &p.gitTag, &p.status, &p.resultsJSON,
		&p.errorMessage, &created, &p.verifiedAt, &expires, &p.completedAt); err != nil {
		return nil, err
	}
	p.createdAt, p.expiresAt = parseTS(created), parseTS(expires)
	return &p, nil
}

func (p *pkgRequest) live() bool { return p.status == pkgPending || p.status == pkgRestarting }

// sticky: a completed or failed row never changes again (D8, 12.Q6).
func (p *pkgRequest) sticky() bool { return p.status == pkgCompleted || p.status == pkgFailed }

func (p *pkgRequest) pastExpiry(now time.Time) bool { return p.expiresAt.Before(now) }

// pkgResult is a node's PackageInstallResultUpload.
type pkgResult struct {
	success, restarting bool
	err                 *string
	details             map[string]any
}

// pkgEvent outcomes: the HTTP status and detail the node sees, and whether the row changed.
type pkgOutcome struct {
	status  int
	detail  string
	changed bool
}

// verifyTransition is the verify handshake (§3.2, D39, D8). Verify never changes the status:
// the row stays pending until a result arrives. The first verify of a pending row turns the
// pickup deadline into verify + 15 min; repeats are idempotent (a duplicate QoS-1 nudge gets
// the same answer and no further extension). A completed/failed row is never rewritten.
func verifyTransition(p *pkgRequest, now time.Time) pkgOutcome {
	if p.sticky() {
		return pkgOutcome{status: http.StatusConflict, detail: "Request already " + p.status}
	}
	if p.status == pkgExpired || p.pastExpiry(now) {
		changed := p.status != pkgExpired
		p.status = pkgExpired
		return pkgOutcome{status: http.StatusGone, detail: "Package install request expired", changed: changed}
	}
	if p.status != pkgPending {
		return pkgOutcome{status: http.StatusConflict, detail: "Request already " + p.status}
	}
	if !p.verifiedAt.Valid {
		p.verifiedAt = sql.NullString{String: dbTime(now), Valid: true}
		p.expiresAt = now.Add(pkgVerifiedTTL)
		return pkgOutcome{status: http.StatusOK, changed: true}
	}
	return pkgOutcome{status: http.StatusOK}
}

// resultTransition applies a node's result (§3.2). Legacy behaviour kept: a result past the
// expiry expires the row with 410; `restarting` is non-terminal, repeatable, and extends the
// expiry by 120 s from the old expiry. D8: a completed/failed row is sticky, so a late or
// duplicate post is acknowledged (200 {"status":"ok"}) and ignored.
func resultTransition(p *pkgRequest, res pkgResult, op pkgOp, now time.Time) pkgOutcome {
	if p.sticky() {
		return pkgOutcome{status: http.StatusOK}
	}
	if p.status == pkgExpired || p.pastExpiry(now) {
		changed := p.status != pkgExpired
		p.status = pkgExpired
		return pkgOutcome{status: http.StatusGone, detail: op.label + " request expired", changed: changed}
	}
	switch {
	case res.restarting:
		p.status = pkgRestarting
		p.expiresAt = p.expiresAt.Add(pkgRestartExtend)
		if len(res.details) > 0 {
			p.resultsJSON = jsonNull(res.details)
		}
	case res.success:
		p.status = pkgCompleted
		if len(res.details) > 0 {
			p.resultsJSON = jsonNull(res.details)
		}
		p.completedAt = sql.NullString{String: dbTime(now), Valid: true}
	default:
		p.status = pkgFailed
		msg := "Unknown error"
		if res.err != nil && *res.err != "" {
			msg = *res.err
		}
		p.errorMessage = sql.NullString{String: msg, Valid: true}
		p.completedAt = sql.NullString{String: dbTime(now), Valid: true}
	}
	return pkgOutcome{status: http.StatusOK, changed: true}
}

// pollTransition is the poll's lazy expiry: only a live row past its expiry flips.
func pollTransition(p *pkgRequest, now time.Time) bool {
	if p.live() && p.pastExpiry(now) {
		p.status = pkgExpired
		return true
	}
	return false
}

func jsonNull(v any) sql.NullString {
	b, err := json.Marshal(v)
	if err != nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(b), Valid: true}
}

// --- store ---

func (m *Module) loadPkg(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, rid, nodeID string) (*pkgRequest, error) {
	return scanPkg(q.QueryRowContext(ctx, `SELECT `+pkgCols+` FROM cc_package_install_requests WHERE id = ? AND node_id = ?`, rid, nodeID))
}

func savePkg(ctx context.Context, tx *sql.Tx, p *pkgRequest) error {
	_, err := tx.ExecContext(ctx, `UPDATE cc_package_install_requests SET status = ?, results_json = ?, error_message = ?,
		verified_at = ?, expires_at = ?, completed_at = ? WHERE id = ?`,
		p.status, p.resultsJSON, p.errorMessage, p.verifiedAt, dbTime(p.expiresAt), p.completedAt, p.id)
	return err
}

// transitionPkg loads a row and applies fn in one write transaction, so concurrent poll,
// verify and results calls can't each commit a different status (§8 lazy-expiry race).
func (m *Module) transitionPkg(ctx context.Context, rid, nodeID string, fn func(*pkgRequest) bool) (*pkgRequest, error) {
	var out *pkgRequest
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		p, err := m.loadPkg(ctx, tx, rid, nodeID)
		if err != nil {
			return err
		}
		out = p
		if fn(p) {
			return savePkg(ctx, tx, p)
		}
		return nil
	})
	return out, err
}

// --- auth (verify_provisioning_auth + require_household_access) ---

// requirePkgHousehold is require_household_access: the admin key bypasses; a JWT caller must
// be a member of the household among all of their memberships (D5). A node with no household
// fails closed for every JWT caller (D40 12.Q5).
func (m *Module) requirePkgHousehold(ctx context.Context, auth provAuth, householdID string) error {
	if auth.admin {
		return nil
	}
	if householdID == "" {
		return fail(http.StatusForbidden, "Not authorized")
	}
	return m.requireRole(ctx, auth.user.ID, householdID, authn.RoleMember)
}

// --- create ---

func (m *Module) handleRequestInstall(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok {
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	name, _ := b.str("command_name", true)
	repo, _ := b.str("github_repo_url", true)
	tag, _ := b.optStrPtr("git_tag")
	if !b.done(w) {
		return
	}
	p, ok := m.createPkg(w, r, auth, name, repo, tag)
	if !ok {
		return
	}
	// The node ignores the repo info here and installs only what verify returns; it is kept
	// for wire parity (D40 12.Q12).
	m.publishPkg(p.nodeID, opInstall, map[string]any{
		"request_id": p.id, "command_name": p.commandName, "github_repo_url": p.repoURL, "git_tag": nullStr(tag),
		"pantry_url": m.PantryBaseURL(r.Context(), p.householdID),
	})
	writeCreated(w, p)
}

func (m *Module) handleRequestUninstall(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok {
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	name, _ := b.str("command_name", true)
	ctype, _ := b.str("component_type", true)
	if !b.done(w) {
		return
	}
	p, ok := m.createPkg(w, r, auth, name, "", nil)
	if !ok {
		return
	}
	payload := map[string]any{"request_id": p.id, "command_name": p.commandName}
	if ctype != "" {
		payload["component_type"] = ctype // the node treats it as a narrowing hint only
	}
	m.publishPkg(p.nodeID, opUninstall, payload)
	writeCreated(w, p)
}

func (m *Module) handleRequestRevert(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok {
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	cmd, _ := b.optStrPtr("command_name")
	pkg, _ := b.optStrPtr("package_name")
	if !b.done(w) {
		return
	}
	var name string
	switch {
	case cmd != nil && *cmd != "":
		name = *cmd
	case pkg != nil && *pkg != "":
		name = *pkg
	default:
		detail(w, http.StatusUnprocessableEntity, "command_name or package_name is required")
		return
	}
	p, ok := m.createPkg(w, r, auth, name, "", nil)
	if !ok {
		return
	}
	m.publishPkg(p.nodeID, opRevert, map[string]any{"request_id": p.id, "command_name": name, "package_name": name})
	writeCreated(w, p)
}

// createPkg checks the node and household (from the node row, never the caller) and inserts
// a pending row with the 5-minute pickup deadline.
func (m *Module) createPkg(w http.ResponseWriter, r *http.Request, auth provAuth, name, repo string, tag *string) (*pkgRequest, bool) {
	ctx := r.Context()
	nodeID := r.PathValue("node_id")
	node, err := m.nodeByID(ctx, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Node not found")
		return nil, false
	}
	if err != nil {
		m.internalError(w, err)
		return nil, false
	}
	hh := ""
	if node.householdID.Valid {
		hh = node.householdID.String
	}
	if err := m.requirePkgHousehold(ctx, auth, hh); err != nil {
		m.writeErr(w, err)
		return nil, false
	}
	now := m.now()
	p := &pkgRequest{id: uuid4(), nodeID: nodeID, householdID: hh, commandName: name, repoURL: repo,
		status: pkgPending, createdAt: now, expiresAt: now.Add(pkgPickupTTL)}
	if tag != nil {
		p.gitTag = sql.NullString{String: *tag, Valid: true}
	}
	if _, err := m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_package_install_requests (id, node_id, household_id,
		command_name, github_repo_url, git_tag, status, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.id, p.nodeID, p.householdID, p.commandName, p.repoURL, p.gitTag, p.status, dbTime(now), dbTime(p.expiresAt)); err != nil {
		m.internalError(w, err)
		return nil, false
	}
	m.deps.Log.Info("cc: package request created", "op", "package", "node", nodeID, "command", name, "request_id", p.id)
	return p, true
}

// publishPkg nudges the node on jarvis/nodes/{id}/package-{op}. Like legacy, a missing broker
// or failed publish is logged and the request still returns 201 (it then expires).
func (m *Module) publishPkg(nodeID string, op pkgOp, payload map[string]any) {
	if err := m.bus.Publish(nodeID, "package-"+op.name, payload); err != nil {
		m.deps.Log.Warn("cc: package request not delivered", "op", op.name, "node", nodeID, "err", err)
	}
}

func writeCreated(w http.ResponseWriter, p *pkgRequest) {
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"id": p.id, "status": p.status, "created_at": pyNaive(p.createdAt)})
}

// --- node callbacks (D4: node auth bound to {node_id}) ---

// handleVerifyPackage is the zero-trust gate for all three operations: the node installs
// only what this returns, for a row CC created for this node.
func (m *Module) handleVerifyPackage(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	nodeID := r.PathValue("node_id")
	if n.ID != nodeID {
		detail(w, http.StatusForbidden, "Node mismatch")
		return
	}
	var out pkgOutcome
	p, err := m.transitionPkg(r.Context(), r.PathValue("request_id"), nodeID, func(p *pkgRequest) bool {
		out = verifyTransition(p, m.now())
		return out.changed
	})
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Package install request not found")
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
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"confirmed": true, "command_name": p.commandName, "github_repo_url": p.repoURL, "git_tag": nullable(p.gitTag),
		"pantry_url": m.PantryBaseURL(r.Context(), p.householdID), // additive (D48): the household's Pantry
	})
}

func (m *Module) handlePackageResults(op pkgOp) nodeHandler {
	return func(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
		nodeID := r.PathValue("node_id")
		if n.ID != nodeID {
			detail(w, http.StatusForbidden, "Node mismatch")
			return
		}
		b, _, ok := readBody(w, r, false)
		if !ok {
			return
		}
		var res pkgResult
		if !b.has("success") {
			b.fail("success", "Field required")
		} else if v, ok := laxBool(b.m["success"]); ok {
			res.success = v
		} else {
			b.fail("success", "Input should be a valid boolean")
		}
		res.err, _ = b.optStrPtr("error")
		res.details, _ = b.object("details", false)
		res.restarting, _ = b.boolean("restarting")
		if !b.done(w) {
			return
		}
		var out pkgOutcome
		p, err := m.transitionPkg(r.Context(), r.PathValue("request_id"), nodeID, func(p *pkgRequest) bool {
			out = resultTransition(p, res, op, m.now())
			return out.changed
		})
		if errors.Is(err, sql.ErrNoRows) {
			detail(w, http.StatusNotFound, op.label+" request not found")
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
			m.deps.Log.Info("cc: package result", "op", op.name, "request_id", p.id, "status", p.status, "command", p.commandName)
			if p.sticky() {
				// D40 12.Q10: the node's commands and FieldSpecs may have changed.
				m.cmdData.invalidateNode(nodeID)
			}
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	}
}

// --- mobile poll ---

func (m *Module) handlePollPackage(op pkgOp) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth, ok := m.authProvisioning(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		rid, nodeID := r.PathValue("request_id"), r.PathValue("node_id")
		p, err := m.loadPkg(ctx, m.deps.DB.Read, rid, nodeID)
		if errors.Is(err, sql.ErrNoRows) {
			detail(w, http.StatusNotFound, op.label+" request not found")
			return
		}
		if err != nil {
			m.internalError(w, err)
			return
		}
		if err := m.requirePkgHousehold(ctx, auth, p.householdID); err != nil {
			m.writeErr(w, err)
			return
		}
		if p.live() && p.pastExpiry(m.now()) {
			// The poll persists the flip, as legacy did; re-read under the write lock so a
			// result that landed meanwhile wins.
			p, err = m.transitionPkg(ctx, rid, nodeID, func(p *pkgRequest) bool { return pollTransition(p, m.now()) })
			if err != nil {
				m.internalError(w, err)
				return
			}
		}
		httpx.WriteJSON(w, http.StatusOK, pollBody(p, op))
	}
}

// pollBody is PackageInstallPollResponse; every key is present (invariant 6). The install
// poll branches by status (details only when completed, a fixed message on expiry); the
// uninstall and revert polls return the row as is.
func pollBody(p *pkgRequest, op pkgOp) map[string]any {
	out := map[string]any{"status": p.status, "request_id": p.id, "command_name": p.commandName,
		"error_message": nil, "details": nil}
	details := func() any {
		if !p.resultsJSON.Valid || p.resultsJSON.String == "" {
			return nil
		}
		var v any
		if json.Unmarshal([]byte(p.resultsJSON.String), &v) != nil {
			return nil
		}
		return v
	}
	if op == opInstall {
		switch p.status {
		case pkgExpired:
			out["error_message"] = "Install request expired — node may be offline"
		case pkgFailed:
			out["error_message"] = nullable(p.errorMessage)
		case pkgCompleted:
			out["details"] = details()
		}
		return out
	}
	if p.status == pkgFailed || p.status == pkgExpired {
		out["error_message"] = nullable(p.errorMessage)
	}
	out["details"] = details()
	return out
}

// cleanupPackages is the 30-day request-row sweep (D40 12.Q7), run by the hourly cleanup.
func (m *Module) cleanupPackages(ctx context.Context, now time.Time) error {
	_, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_package_install_requests WHERE created_at < ?`,
		dbTime(now.Add(-pkgRequestRetained)))
	if err != nil {
		return fmt.Errorf("package request sweep: %w", err)
	}
	return nil
}
