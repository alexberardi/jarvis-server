package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Central package management (docs/decisions/2026-10-30-central-package-assignments.md): a
// household admin says which Pantry package belongs on which nodes (or on all of them) and the
// server installs it there with the ordinary install request, so the app, the admin UI and
// jarvisd's Pantry switch all keep applying. CC stays a broker: it creates the requests and
// reports what the nodes did; the nodes still verify and install.

const (
	assignMaxAttempts = 3         // automatic install requests per node before it waits for a change
	assignRetryAfter  = time.Hour // between automatic attempts; also the reconcile sweep's pace
)

func (m *Module) registerPackageAssignments(mux *http.ServeMux) {
	const base = "/api/v0/households/{household_id}/package-assignments"
	mux.HandleFunc("GET "+base, m.handleListAssignments)
	mux.HandleFunc("PUT "+base+"/{command_name}", m.handlePutAssignment)
	mux.HandleFunc("DELETE "+base+"/{command_name}", m.handleDeleteAssignment)
}

type pkgAssignment struct {
	id, householdID, commandName, repoURL string
	gitTag                                sql.NullString
	allNodes                              bool
	updatedAt                             string
}

// reqStatusAt is a request's status, with a pending or restarting one past its deadline read as
// expired: nothing flips the row if the node never polls it, but it will not be picked up now.
const reqStatusAt = `CASE WHEN r.status IN ('pending', 'restarting') AND r.expires_at < ? THEN 'expired' ELSE r.status END`

const assignCols = `id, household_id, command_name, github_repo_url, git_tag, all_nodes, updated_at`

func scanAssignment(s scanner) (*pkgAssignment, error) {
	var a pkgAssignment
	if err := s.Scan(&a.id, &a.householdID, &a.commandName, &a.repoURL, &a.gitTag, &a.allNodes, &a.updatedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

// requireHouseholdAdmin: changing what runs on the household's nodes is an admin's call.
func (m *Module) requireHouseholdAdmin(ctx context.Context, auth provAuth, hh string) error {
	if auth.admin {
		return nil
	}
	return m.requireRole(ctx, auth.user.ID, hh, authn.RoleAdmin)
}

// assignNodeStatus is what the app shows for a node: waiting (no request yet, or the node is
// offline), skipped (the server can't install it there, see note), installing, installed
// or failed, from the latest install request's status.
func assignNodeStatus(requestID, reqStatus sql.NullString, note string) string {
	switch {
	case !requestID.Valid && note != "":
		return "skipped"
	case !requestID.Valid || !reqStatus.Valid:
		return "waiting"
	case reqStatus.String == pkgCompleted:
		return "installed"
	case reqStatus.String == pkgFailed || reqStatus.String == pkgExpired:
		return "failed"
	}
	return "installing"
}

func (m *Module) assignmentJSON(ctx context.Context, a *pkgAssignment) (map[string]any, error) {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT n.node_id, n.request_id, `+reqStatusAt+`, r.error_message, n.attempts,
		COALESCE(n.note, '') FROM cc_package_assignment_nodes n
		LEFT JOIN cc_package_install_requests r ON r.id = n.request_id
		WHERE n.assignment_id = ? ORDER BY n.node_id`, dbTime(m.now()), a.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := []any{}
	for rows.Next() {
		var id, note string
		var reqID, status, reqErr sql.NullString
		var attempts int
		if err := rows.Scan(&id, &reqID, &status, &reqErr, &attempts, &note); err != nil {
			return nil, err
		}
		n := map[string]any{"node_id": id, "status": assignNodeStatus(reqID, status, note), "attempts": attempts,
			"request_id": nullable(reqID)}
		switch {
		case reqErr.Valid && reqErr.String != "":
			n["error"] = reqErr.String
		case note != "":
			n["error"] = note
		}
		nodes = append(nodes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"command_name": a.commandName, "github_repo_url": a.repoURL, "git_tag": nullable(a.gitTag),
		"all_nodes": a.allNodes, "updated_at": a.updatedAt, "nodes": nodes}, nil
}

func (m *Module) handleListAssignments(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok || !m.pathHouseholdAccess(w, r, auth) {
		return
	}
	ctx, hh := r.Context(), r.PathValue("household_id")
	rows, err := m.deps.DB.Read.QueryContext(ctx,
		`SELECT `+assignCols+` FROM cc_package_assignments WHERE household_id = ? ORDER BY command_name`, hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	var as []*pkgAssignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			rows.Close()
			m.internalError(w, err)
			return
		}
		as = append(as, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	out := []any{}
	for _, a := range as {
		j, err := m.assignmentJSON(ctx, a)
		if err != nil {
			m.internalError(w, err)
			return
		}
		out = append(out, j)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"assignments": out})
}

// handlePutAssignment creates or replaces the household's assignment of one package: the
// repository (which must be the Pantry's own for that package, so an admin can't point every
// node at an arbitrary repository), an optional version, and either all nodes or a list.
func (m *Module) handlePutAssignment(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok {
		return
	}
	ctx, hh, name := r.Context(), r.PathValue("household_id"), r.PathValue("command_name")
	if err := m.requireHouseholdAdmin(ctx, auth, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	repo, _ := b.str("github_repo_url", true)
	tag, _ := b.optStrPtr("git_tag")
	all, _ := b.boolean("all_nodes")
	ids, _ := b.strList("node_ids")
	if !b.done(w) {
		return
	}
	if !all && len(ids) == 0 {
		validationError(w, "body -> node_ids: give the nodes, or set all_nodes")
		return
	}
	if !m.requirePantry(w, r, hh) {
		return
	}
	var d pantryCommand
	switch err := m.pantryGet(ctx, hh, "/v1/commands/"+url.PathEscape(name), &d); {
	case errors.Is(err, errPantryNoPackage):
		detailCode(w, http.StatusUnprocessableEntity, "The Pantry has no package "+name, packageNotFoundCode)
		return
	case err != nil:
		detailCode(w, http.StatusServiceUnavailable, "The Pantry package store can't be reached to check "+name+". Try again later.", pantryUnavailableCode)
		return
	}
	if !sameRepo(repo, d.GithubRepoURL) {
		detailCode(w, http.StatusUnprocessableEntity, "That repository isn't the one the Pantry lists for "+name, repoMismatchCode)
		return
	}
	for _, id := range ids {
		n, err := m.nodeByID(ctx, id)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && n.householdID.String != hh) {
			detail(w, http.StatusNotFound, "Node not found: "+id)
			return
		}
		if err != nil {
			m.internalError(w, err)
			return
		}
	}
	a, err := m.saveAssignment(ctx, hh, name, strings.TrimSpace(d.GithubRepoURL), tag, all, ids)
	if err != nil {
		m.internalError(w, err)
		return
	}
	m.reconcileAssignments(ctx, hh)
	j, err := m.assignmentJSON(ctx, a)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, j)
}

// saveAssignment upserts the assignment and makes its node rows match. A changed repository
// or version starts every node's install again; unchanged nodes keep their request.
func (m *Module) saveAssignment(ctx context.Context, hh, name, repo string, tag *string, all bool, ids []string) (*pkgAssignment, error) {
	tx, err := m.deps.DB.Write.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := dbTime(m.now())
	tagVal := sql.NullString{}
	if tag != nil && strings.TrimSpace(*tag) != "" {
		tagVal = sql.NullString{String: strings.TrimSpace(*tag), Valid: true}
	}
	prev, err := scanAssignment(tx.QueryRowContext(ctx,
		`SELECT `+assignCols+` FROM cc_package_assignments WHERE household_id = ? AND command_name = ?`, hh, name))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		prev = nil
	case err != nil:
		return nil, err
	}
	id := uuid4()
	if prev != nil {
		id = prev.id
		if _, err := tx.ExecContext(ctx, `UPDATE cc_package_assignments SET github_repo_url = ?, git_tag = ?, all_nodes = ?,
			updated_at = ? WHERE id = ?`, repo, tagVal, all, now, id); err != nil {
			return nil, err
		}
		if prev.repoURL != repo || prev.gitTag != tagVal {
			if _, err := tx.ExecContext(ctx, `UPDATE cc_package_assignment_nodes SET request_id = NULL, attempts = 0,
				last_attempt_at = NULL, note = NULL WHERE assignment_id = ?`, id); err != nil {
				return nil, err
			}
		}
	} else if _, err := tx.ExecContext(ctx, `INSERT INTO cc_package_assignments (id, household_id, command_name, github_repo_url,
		git_tag, all_nodes, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, hh, name, repo, tagVal, all, now, now); err != nil {
		return nil, err
	}
	if !all {
		// Only the listed nodes stay; the rest were targeted before and no longer are.
		if _, err := tx.ExecContext(ctx, `DELETE FROM cc_package_assignment_nodes WHERE assignment_id = ?`+
			` AND node_id NOT IN (SELECT value FROM json_each(?))`, id, jsonStrings(ids)); err != nil {
			return nil, err
		}
		for _, n := range ids {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO cc_package_assignment_nodes (assignment_id, node_id)
				VALUES (?, ?)`, id, n); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &pkgAssignment{id: id, householdID: hh, commandName: name, repoURL: repo, gitTag: tagVal, allNodes: all, updatedAt: now}, nil
}

// handleDeleteAssignment stops managing a package. The nodes keep it unless ?uninstall=true,
// which asks every node that finished installing it to uninstall it.
func (m *Module) handleDeleteAssignment(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok {
		return
	}
	ctx, hh, name := r.Context(), r.PathValue("household_id"), r.PathValue("command_name")
	if err := m.requireHouseholdAdmin(ctx, auth, hh); err != nil {
		m.writeErr(w, err)
		return
	}
	uninstall, ok := queryBool(w, r, "uninstall", false)
	if !ok {
		return
	}
	a, err := scanAssignment(m.deps.DB.Read.QueryRowContext(ctx,
		`SELECT `+assignCols+` FROM cc_package_assignments WHERE household_id = ? AND command_name = ?`, hh, name))
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Assignment not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if uninstall {
		m.uninstallAssigned(ctx, a)
	}
	if _, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_package_assignments WHERE id = ?`, a.id); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// uninstallAssigned asks each node that installed the package to uninstall it. A node that is
// offline or fails to be asked is logged and left as it is.
func (m *Module) uninstallAssigned(ctx context.Context, a *pkgAssignment) {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT n.node_id FROM cc_package_assignment_nodes n
		JOIN cc_package_install_requests r ON r.id = n.request_id
		WHERE n.assignment_id = ? AND r.status = ?`, a.id, pkgCompleted)
	if err != nil {
		m.deps.Log.Warn("cc: package assignment uninstall: list nodes", "command", a.commandName, "err", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		p, err := m.insertPkg(ctx, id, a.householdID, a.commandName, "", nil)
		if err != nil {
			m.deps.Log.Warn("cc: package assignment uninstall", "command", a.commandName, "node", id, "err", err)
			continue
		}
		m.publishPkg(id, opUninstall, map[string]any{"request_id": p.id, "command_name": p.commandName})
	}
}

// reconcileAssignments makes the nodes match the assignments of one household (hh), or of all
// of them (""). For each node that should have a package and doesn't, and is online, it makes
// an install request. It is safe to run any time: a node with a live or finished request is
// left alone, and a failed one is tried again at most assignMaxAttempts times, an hour apart,
// until the assignment changes.
func (m *Module) reconcileAssignments(ctx context.Context, hh string) {
	q, args := `SELECT `+assignCols+` FROM cc_package_assignments`, []any{}
	if hh != "" {
		q, args = q+` WHERE household_id = ?`, append(args, hh)
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, q, args...)
	if err != nil {
		m.deps.Log.Warn("cc: package assignments: list", "err", err)
		return
	}
	var as []*pkgAssignment
	for rows.Next() {
		if a, err := scanAssignment(rows); err == nil {
			as = append(as, a)
		}
	}
	rows.Close()
	for _, a := range as {
		m.reconcileAssignment(ctx, a)
	}
}

func (m *Module) reconcileAssignment(ctx context.Context, a *pkgAssignment) {
	log := m.deps.Log.With("command", a.commandName, "household", a.householdID)
	if a.allNodes {
		if _, err := m.deps.DB.Write.ExecContext(ctx, `INSERT OR IGNORE INTO cc_package_assignment_nodes (assignment_id, node_id)
			SELECT ?, node_id FROM cc_nodes WHERE household_id = ? AND is_active = 1`, a.id, a.householdID); err != nil {
			log.Warn("cc: package assignments: add household nodes", "err", err)
			return
		}
	}
	if !m.pantryEnabled(ctx, a.householdID) {
		return // installs are refused while the household has the Pantry off; wait
	}
	now := m.now()
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT n.node_id, n.request_id, `+reqStatusAt+`, n.attempts, n.last_attempt_at
		FROM cc_package_assignment_nodes n LEFT JOIN cc_package_install_requests r ON r.id = n.request_id
		WHERE n.assignment_id = ?`, dbTime(now), a.id)
	if err != nil {
		log.Warn("cc: package assignments: list nodes", "err", err)
		return
	}
	var due []string
	for rows.Next() {
		var node string
		var reqID, status, last sql.NullString
		var attempts int
		if err := rows.Scan(&node, &reqID, &status, &attempts, &last); err != nil {
			continue
		}
		retryable := !reqID.Valid || (status.Valid && (status.String == pkgFailed || status.String == pkgExpired))
		waited := !last.Valid || parseTS(last.String).Before(now.Add(-assignRetryAfter))
		if retryable && attempts < assignMaxAttempts && waited {
			due = append(due, node)
		}
	}
	rows.Close()
	for _, id := range due {
		node, err := m.nodeByID(ctx, id)
		if err != nil || !node.reachable(now) {
			continue // gone, or offline: the next pass tries again, with no attempt used
		}
		repo, tag := a.repoURL, a.tagPtr()
		var note string
		if node.desktop() {
			var f *pinFailure
			if repo, tag, f = m.pinDesktopInstallCheck(ctx, a.householdID, a.commandName, repo, tag); f != nil {
				note = f.detail
			}
		}
		if note == "" {
			p, err := m.insertPkg(ctx, id, a.householdID, a.commandName, repo, tag)
			if err != nil {
				log.Warn("cc: package assignments: create request", "node", id, "err", err)
				continue
			}
			m.publishInstall(ctx, p)
			_, err = m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_package_assignment_nodes SET request_id = ?, attempts = attempts + 1,
				last_attempt_at = ?, note = NULL WHERE assignment_id = ? AND node_id = ?`, p.id, dbTime(now), a.id, id)
			if err != nil {
				log.Warn("cc: package assignments: record request", "node", id, "err", err)
			}
			continue
		}
		if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_package_assignment_nodes SET request_id = NULL, attempts = attempts + 1,
			last_attempt_at = ?, note = ? WHERE assignment_id = ? AND node_id = ?`, dbTime(now), note, a.id, id); err != nil {
			log.Warn("cc: package assignments: record skip", "node", id, "err", err)
		}
	}
}

func (a *pkgAssignment) tagPtr() *string {
	if !a.gitTag.Valid {
		return nil
	}
	return &a.gitTag.String
}

// cleanupAssignments is the hourly pass (with the request sweep): new household nodes and
// nodes that came back online get their packages.
func (m *Module) cleanupAssignments(ctx context.Context, _ time.Time) error {
	m.reconcileAssignments(ctx, "")
	return nil
}

// jsonStrings is ids as a JSON array, for SQLite's json_each in an IN list.
func jsonStrings(ids []string) string {
	b, _ := json.Marshal(ids)
	return string(b)
}
