package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Node updates (doc 05 §3.8): a task row; the heartbeat response is the dispatch channel and
// the post-upgrade heartbeat version confirms success.

const (
	releaseRepo        = "alexberardi/jarvis-node-setup"
	releaseCacheTTL    = 300 * time.Second
	dispatchGrace      = 30 * time.Second
	updateCeiling      = 15 * time.Minute
	inProgressNoChange = 10 * time.Minute
)

type releaseInfo struct {
	Tag, Version string
	PublishedAt  any
}

type releaseCache struct {
	mu   sync.Mutex
	at   time.Time
	info *releaseInfo
}

// updatesAllowed reads updates.allow_check fail-closed (household scope, or system when "").
func (m *Module) updatesAllowed(ctx context.Context, householdID string) bool {
	v, err := m.settings.Get(ctx, settingUpdatesAllowCheck, settings.Scope{HouseholdID: householdID})
	if err != nil {
		m.deps.Log.Warn("cc: updates.allow_check unreadable, treating as disabled", "err", err)
		return false
	}
	b, _ := v.Value.(bool)
	return b
}

// latestRelease is github_releases.latest_release: gated, cached 5 minutes, nil on failure.
func (m *Module) latestRelease(ctx context.Context, householdID string) *releaseInfo {
	if !m.updatesAllowed(ctx, householdID) {
		return nil
	}
	c := m.releases
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.info != nil && m.now().Sub(c.at) < releaseCacheTTL {
		return c.info
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(m.GitHubAPI, "/")+"/repos/"+releaseRepo+"/releases/latest", nil)
	if err != nil {
		return nil
	}
	resp, err := m.HTTPClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var body struct {
		TagName     string `json:"tag_name"`
		PublishedAt any    `json:"published_at"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil || body.TagName == "" {
		return nil
	}
	c.info = &releaseInfo{Tag: body.TagName, Version: strings.TrimPrefix(body.TagName, "v"), PublishedAt: body.PublishedAt}
	c.at = m.now()
	return c.info
}

func (m *Module) handleLatestRelease(w http.ResponseWriter, r *http.Request) {
	info := m.latestRelease(r.Context(), "")
	if info == nil {
		httpx.WriteJSON(w, http.StatusOK, nil)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"tag": info.Tag, "version": info.Version, "published_at": info.PublishedAt})
}

// compareVersions compares dotted numeric versions ("0.3.10" > "0.3.9"); ok=false if either
// isn't one.
func compareVersions(a, b string) (int, bool) {
	pa, pb := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		var err error
		if i < len(pa) {
			if x, err = strconv.Atoi(pa[i]); err != nil {
				return 0, false
			}
		}
		if i < len(pb) {
			if y, err = strconv.Atoi(pb[i]); err != nil {
				return 0, false
			}
		}
		if x != y {
			if x < y {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func (m *Module) handleRequestUpdate(w http.ResponseWriter, r *http.Request, u authn.User) {
	b, _, ok := readBody(w, r, true)
	if !ok {
		return
	}
	requested, _ := b.str("target_version", false)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	id := r.PathValue("node_id")
	node, err := m.requireNodeAccess(ctx, u, id, authn.RoleMember, "Not authorized")
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if t, err := m.openTask(ctx, id, "update"); err == nil {
		detail(w, http.StatusConflict, map[string]any{
			"message": "An update is already queued for this node.", "task_id": t.id, "state": t.state,
		})
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		m.internalError(w, err)
		return
	}
	if requested == "" {
		requested = "latest"
	}
	var target string
	if strings.EqualFold(requested, "latest") {
		if info := m.latestRelease(ctx, node.householdID.String); info != nil {
			target = info.Version
		}
	} else {
		target = strings.TrimPrefix(requested, "v")
	}
	if target == "" {
		detail(w, http.StatusServiceUnavailable,
			"Could not determine target version (GitHub releases unreachable and no explicit version was requested).")
		return
	}
	// D40 05.Q5: refuse at request time what the node would silently ignore (it only
	// self-updates tarball installs, and never downgrades), instead of stranding the task
	// until the sweeper (§8.9).
	if node.installMode.Valid && node.installMode.String != "" && node.installMode.String != "tarball" {
		detail(w, http.StatusBadRequest, fmt.Sprintf("Node install mode '%s' does not support remote updates (only 'tarball' installs do).", node.installMode.String))
		return
	}
	if node.lastSeenVersion.Valid {
		if c, ok := compareVersions(target, node.lastSeenVersion.String); ok && c <= 0 {
			detail(w, http.StatusBadRequest, fmt.Sprintf("Node is already on version %s; target %s is not newer.", node.lastSeenVersion.String, target))
			return
		}
	}
	tid, now := uuid4(), dbTime(m.now())
	if _, err := m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_node_tasks (id, node_id, kind, target_version, state, created_at, updated_at)
		VALUES (?, ?, 'update', ?, 'pending', ?, ?)`, tid, id, target, now, now); err != nil {
		m.internalError(w, err)
		return
	}
	t, err := taskByID(ctx, m.deps.DB.Read, tid)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, t.response())
}

func (m *Module) openTask(ctx context.Context, nodeID, kind string) (*taskRow, error) {
	return scanTask(m.deps.DB.Read.QueryRowContext(ctx, `SELECT `+taskCols+` FROM cc_node_tasks
		WHERE node_id = ? AND kind = ? AND state IN ('pending', 'dispatched', 'in_progress')
		ORDER BY created_at DESC LIMIT 1`, nodeID, kind))
}

// handleTaskStatus: a node may only report `failed` for its own update task; success comes
// from the heartbeat version (§7.8), terminal states are immutable (§7.9).
func (m *Module) handleTaskStatus(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	state, hasState := b.str("state", true)
	if hasState && state != "failed" {
		*b.errs = append(*b.errs, "body -> state: Input should be 'failed'")
	}
	msg, _ := b.str("error_message", false)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	tid := r.PathValue("task_id")
	t, err := taskByID(ctx, m.deps.DB.Read, tid)
	if (err == nil && (t.nodeID != n.ID || t.kind != "update")) || errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Task not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if msg == "" {
		msg = "Failed (reported by node)"
	}
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	now := dbTime(m.now())
	res, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_node_tasks SET state = 'failed', error_message = ?, finished_at = ?,
		updated_at = ? WHERE id = ? AND state NOT IN ('success', 'failed')`, msg, now, now, tid)
	if err != nil {
		m.internalError(w, err)
		return
	}
	t, err = taskByID(ctx, m.deps.DB.Read, tid)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		detail(w, http.StatusConflict, "Task is already "+t.state)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, t.response())
}

func (m *Module) handleGetTask(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	t, err := taskByID(ctx, m.deps.DB.Read, r.PathValue("task_id"))
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Task not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if _, err := m.requireNodeAccess(ctx, u, t.nodeID, authn.RoleMember, "Not authorized"); err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, t.response())
}

func (m *Module) handleCancelTask(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	id, tid := r.PathValue("node_id"), r.PathValue("task_id")
	t, err := scanTask(m.deps.DB.Read.QueryRowContext(ctx, `SELECT `+taskCols+` FROM cc_node_tasks WHERE id = ? AND node_id = ?`, tid, id))
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Task not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if _, err := m.requireNodeAccess(ctx, u, id, authn.RoleMember, "Not authorized"); err != nil {
		m.writeErr(w, err)
		return
	}
	now := dbTime(m.now())
	res, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_node_tasks SET state = 'failed', error_message = 'Cancelled by user',
		finished_at = ?, updated_at = ? WHERE id = ? AND state NOT IN ('success', 'failed')`, now, now, tid)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		detail(w, http.StatusConflict, "Task is already "+t.state)
		return
	}
	if t, err = taskByID(ctx, m.deps.DB.Read, tid); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, t.response())
}

func (m *Module) handleListTasks(w http.ResponseWriter, r *http.Request, u authn.User) {
	limit, ok := queryInt(w, r, "limit", 20)
	if !ok {
		return
	}
	ctx := r.Context()
	id := r.PathValue("node_id")
	if _, err := m.requireNodeAccess(ctx, u, id, authn.RoleMember, "Not authorized"); err != nil {
		m.writeErr(w, err)
		return
	}
	limit = max(1, min(limit, 100))
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT `+taskCols+` FROM cc_node_tasks WHERE node_id = ?
		ORDER BY created_at DESC, rowid DESC LIMIT ?`, id, limit)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			m.internalError(w, err)
			return
		}
		out = append(out, t.response())
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// dispatchPendingTask hands the oldest pending update to a node that isn't busy (§3.8, §7.7).
func (m *Module) dispatchPendingTask(ctx context.Context, n *nodeRow) (map[string]any, error) {
	if n.isBusy {
		return nil, nil
	}
	var tid string
	var target sql.NullString
	err := m.deps.DB.Write.QueryRowContext(ctx, `UPDATE cc_node_tasks SET state = 'dispatched', updated_at = ?
		WHERE id = (SELECT id FROM cc_node_tasks WHERE node_id = ? AND kind = 'update' AND state = 'pending'
		            ORDER BY created_at ASC, rowid ASC LIMIT 1)
		RETURNING id, target_version`, dbTime(m.now()), n.nodeID).Scan(&tid, &target)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"task_id": tid, "target_version": nullable(target)}, nil
}

// reconcileOpenTask matches a heartbeat's version against the open update task: the target
// version → success; dispatched ≥30 s with the old version → in_progress once. It never
// bumps updated_at for an in_progress task still on the old version (§7.7: the sweeper
// relies on it).
func (m *Module) reconcileOpenTask(ctx context.Context, nodeID string, reported *string) error {
	if reported == nil {
		return nil
	}
	t, err := scanTask(m.deps.DB.Read.QueryRowContext(ctx, `SELECT `+taskCols+` FROM cc_node_tasks
		WHERE node_id = ? AND kind = 'update' AND state IN ('dispatched', 'in_progress') ORDER BY created_at DESC LIMIT 1`, nodeID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	now := m.now()
	if t.targetVersion.Valid && t.targetVersion.String != "" && *reported == t.targetVersion.String {
		_, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_node_tasks SET state = 'success', finished_at = ?, updated_at = ?
			WHERE id = ?`, dbTime(now), dbTime(now), t.id)
		return err
	}
	if t.state == "dispatched" && now.Sub(parseTS(t.updatedAt)) >= dispatchGrace {
		_, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_node_tasks SET state = 'in_progress', updated_at = ? WHERE id = ?`,
			dbTime(now), t.id)
		return err
	}
	return nil
}

// runTaskSweep fails dead tasks every 120 s. Update tasks: 15 min since creation in any open
// state, or 10 min in_progress with no transition. Factory-reset tasks wait for an offline
// node for up to 7 days (D10). Messages are kind-specific (D8, §8.8).
func (m *Module) runTaskSweep(ctx context.Context, _ queue.Job) ([]byte, error) {
	_, err := m.sweepTasks(ctx)
	return nil, err
}

func (m *Module) sweepTasks(ctx context.Context) (int64, error) {
	now := m.now()
	n1, err := m.execCount(ctx, `UPDATE cc_node_tasks SET state = 'failed',
			error_message = COALESCE(error_message, 'Timeout: no heartbeat confirming ' || COALESCE(target_version, 'the update')),
			finished_at = ?, updated_at = ?
		WHERE kind = 'update' AND state IN ('pending', 'dispatched', 'in_progress')
		  AND (created_at < ? OR (state = 'in_progress' AND updated_at < ?))`,
		dbTime(now), dbTime(now), dbTime(now.Add(-updateCeiling)), dbTime(now.Add(-inProgressNoChange)))
	if err != nil {
		return 0, err
	}
	n2, err := m.execCount(ctx, `UPDATE cc_node_tasks SET state = 'failed',
			error_message = COALESCE(error_message, 'Timeout: the node did not complete the factory reset'),
			finished_at = ?, updated_at = ?
		WHERE kind = 'factory_reset' AND state IN ('pending', 'dispatched', 'in_progress') AND created_at < ?`,
		dbTime(now), dbTime(now), dbTime(now.Add(-resetTaskCeiling)))
	if err != nil {
		return 0, err
	}
	if n1+n2 > 0 {
		m.deps.Log.Info("cc: stale node tasks failed", "update", n1, "factory_reset", n2)
	}
	return n1 + n2, nil
}

func (m *Module) execCount(ctx context.Context, q string, args ...any) (int64, error) {
	res, err := m.deps.DB.Write.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
