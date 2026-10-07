package cc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Factory reset (doc 05 §3.4, D10).
//
// The tracked flow is the one kept: POST /admin/nodes/{id}/factory-reset creates a
// factory_reset task (single in flight), stores its reset token on the task row, and
// publishes {request_id: token, node_id, task_id}. The node reports through
// /nodes/factory-reset/{task_id}/status with X-Reset-Token (not consumed, so it can post
// in_progress and then success with one token). Because the token is in SQLite, a node that
// was offline, or a jarvisd restart, still completes the reset (D10 fix of §8.10).
//
// Transition path, until mobile switches and older node builds are gone: DELETE
// /admin/nodes/{id} publishes the reset without a task_id and hard-deletes the node, and the
// node confirms through the unauthenticated POST /nodes/verify-reset, which consumes the
// token. DELETE's token can't live on a task (the row is deleted), so it stays in memory with
// the legacy 300 s TTL. verify-reset also accepts a tracked task's token, for older node
// builds that ignore task_id: consuming it completes the task, since the node wipes on 200.

const (
	deleteResetTTL = 300 * time.Second
	// resetTaskCeiling: a factory reset waits this long for the node (it may be offline),
	// instead of the update tasks' 15 minutes (D10).
	resetTaskCeiling = 7 * 24 * time.Hour
)

type resetStore struct {
	mu sync.Mutex
	m  map[string]time.Time // token -> expiry
}

func newResetStore() *resetStore { return &resetStore{m: map[string]time.Time{}} }

func (s *resetStore) add(tok string, exp time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[tok] = exp
}

func (s *resetStore) consume(tok string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, exp := range s.m {
		if now.After(exp) {
			delete(s.m, k)
		}
	}
	if _, ok := s.m[tok]; !ok {
		return false
	}
	delete(s.m, tok)
	return true
}

// deactivate is _deactivate_node_with_auth: best effort, logged.
func (m *Module) deactivate(ctx context.Context, nodeID string) {
	if err := m.Nodes.DeactivateNode(context.WithoutCancel(ctx), nodeID); err != nil {
		m.deps.Log.Error("cc: deactivate node in auth failed", "node", nodeID, "err", err)
	}
}

func (m *Module) handleDeleteNode(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	id := r.PathValue("node_id")
	if _, err := m.requireNodeAccess(ctx, u, id, authn.RolePowerUser, "Only superusers can delete nodes without a household"); err != nil {
		m.writeErr(w, err)
		return
	}
	// Publish before revoking auth (§7.6).
	tok := randHex(16)
	m.resets.add(tok, m.now().Add(deleteResetTTL))
	if err := m.bus.Publish(id, "factory-reset", map[string]any{"request_id": tok, "node_id": id}); err != nil {
		m.deps.Log.Warn("cc: factory-reset not published; the node will not auto-reset", "node", id, "err", err)
	}
	m.deactivate(ctx, id)
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			`DELETE FROM cc_auth_sessions WHERE node_id = ?`,
			`DELETE FROM cc_config_pushes WHERE node_id = ?`,
			`DELETE FROM cc_settings_snapshots WHERE node_id = ?`,
			`DELETE FROM cc_settings_requests WHERE node_id = ?`,
			`DELETE FROM cc_nodes WHERE node_id = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		m.internalError(w, err)
		return
	}
	m.deps.Log.Info("cc: node deleted", "node", id, "user_id", u.ID)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"message": "Deleted"})
}

// --- tasks ---

type taskRow struct {
	id, nodeID, kind     string
	targetVersion        sql.NullString
	state                string
	errorMessage         sql.NullString
	resetToken           sql.NullString
	resetConsumed        sql.NullString
	createdAt, updatedAt string
	finishedAt           sql.NullString
}

const taskCols = `id, node_id, kind, target_version, state, error_message, reset_token, reset_token_consumed_at,
	created_at, updated_at, finished_at`

func scanTask(s scanner) (*taskRow, error) {
	var t taskRow
	if err := s.Scan(&t.id, &t.nodeID, &t.kind, &t.targetVersion, &t.state, &t.errorMessage, &t.resetToken,
		&t.resetConsumed, &t.createdAt, &t.updatedAt, &t.finishedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

type queryer interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

func taskByID(ctx context.Context, q queryer, id string) (*taskRow, error) {
	return scanTask(q.QueryRowContext(ctx, `SELECT `+taskCols+` FROM cc_node_tasks WHERE id = ?`, id))
}

// response is NodeTaskResponse.
func (t *taskRow) response() map[string]any {
	var fin any
	if t.finishedAt.Valid {
		fin = pyNaive(parseTS(t.finishedAt.String))
	}
	return map[string]any{
		"id": t.id, "node_id": t.nodeID, "kind": t.kind, "target_version": nullable(t.targetVersion),
		"state": t.state, "error_message": nullable(t.errorMessage),
		"created_at": pyNaive(parseTS(t.createdAt)), "updated_at": pyNaive(parseTS(t.updatedAt)), "finished_at": fin,
	}
}

func terminal(state string) bool { return state == "success" || state == "failed" }

func (m *Module) handleFactoryReset(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	id := r.PathValue("node_id")
	if _, err := m.requireNodeAccess(ctx, u, id, authn.RolePowerUser, "Only superusers can factory-reset nodes without a household"); err != nil {
		m.writeErr(w, err)
		return
	}
	tok := randHex(16)
	taskID := uuid4()
	now := dbTime(m.now())
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		existing, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskCols+` FROM cc_node_tasks
			WHERE node_id = ? AND kind = 'factory_reset' AND state IN ('pending', 'dispatched', 'in_progress')
			ORDER BY created_at DESC LIMIT 1`, id))
		if err == nil {
			return fail(http.StatusConflict, map[string]any{
				"message": "A factory reset is already in flight for this node.",
				"task_id": existing.id, "state": existing.state,
			})
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO cc_node_tasks (id, node_id, kind, state, reset_token, created_at, updated_at)
			VALUES (?, ?, 'factory_reset', 'pending', ?, ?, ?)`, taskID, id, tok, now, now)
		return err
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if err := m.bus.Publish(id, "factory-reset", map[string]any{"request_id": tok, "node_id": id, "task_id": taskID}); err != nil {
		m.deps.Log.Warn("cc: factory-reset task stays pending: not published", "node", id, "task", taskID, "err", err)
	} else if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_node_tasks SET state = 'dispatched', updated_at = ?
		WHERE id = ? AND state = 'pending'`, dbTime(m.now()), taskID); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"task_id": taskID, "reset_token": tok})
}

func (m *Module) handleFactoryResetStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tok := r.Header.Get("X-Reset-Token")
	var task *taskRow
	var err error
	if tok != "" {
		task, err = scanTask(m.deps.DB.Read.QueryRowContext(ctx, `SELECT `+taskCols+` FROM cc_node_tasks
			WHERE reset_token = ? AND kind = 'factory_reset'`, tok))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			m.internalError(w, err)
			return
		}
	}
	if task == nil {
		detail(w, http.StatusUnauthorized, "Invalid or expired reset token")
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	state, _ := b.str("state", true)
	errMsg, hasErr := b.str("error_message", false)
	if !b.done(w) {
		return
	}
	if state != "in_progress" && state != "success" && state != "failed" {
		detail(w, http.StatusBadRequest, fmt.Sprintf("state must be one of: in_progress, success, failed (got '%s')", state))
		return
	}
	// The token is bound to its task (legacy accepted any live token for any task id).
	if task.id != r.PathValue("task_id") {
		detail(w, http.StatusNotFound, "Factory-reset task not found")
		return
	}
	if terminal(task.state) {
		httpx.WriteJSON(w, http.StatusOK, task.response()) // idempotent late report
		return
	}
	now := dbTime(m.now())
	var msg any
	if hasErr {
		msg = errMsg
	}
	var fin any
	if terminal(state) {
		fin = now
	}
	err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE cc_node_tasks SET state = ?, error_message = ?, finished_at = ?, updated_at = ?
			WHERE id = ? AND state NOT IN ('success', 'failed')`, state, msg, fin, now, task.id); err != nil {
			return err
		}
		if state == "success" {
			_, err := tx.ExecContext(ctx, `UPDATE cc_nodes SET is_active = 0 WHERE node_id = ?`, task.nodeID)
			return err
		}
		return nil
	})
	if err != nil {
		m.internalError(w, err)
		return
	}
	if state == "success" {
		m.deps.Log.Info("cc: node inactive after factory reset", "node", task.nodeID)
		m.deactivate(ctx, task.nodeID)
	}
	task, err = taskByID(ctx, m.deps.DB.Read, task.id)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, task.response())
}

func (m *Module) handleVerifyReset(w http.ResponseWriter, r *http.Request) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	nodeID, _ := b.str("node_id", true)
	tok, _ := b.str("request_id", true)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	if m.resets.consume(tok, m.now()) {
		m.deps.Log.Info("cc: factory reset verified", "node", nodeID)
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"verified": true})
		return
	}
	// A tracked task's token, from a node build that ignores task_id: consume once and
	// complete the task (the node wipes as soon as this answers 200).
	now := dbTime(m.now())
	var taskNode string
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `UPDATE cc_node_tasks SET reset_token_consumed_at = ?, state = 'success',
				finished_at = ?, updated_at = ?
			WHERE reset_token = ? AND kind = 'factory_reset' AND node_id = ? AND reset_token_consumed_at IS NULL
			  AND state NOT IN ('success', 'failed')
			RETURNING node_id`, now, now, now, tok, nodeID).Scan(&taskNode)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE cc_nodes SET is_active = 0 WHERE node_id = ?`, taskNode)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		m.deps.Log.Warn("cc: factory reset verification rejected", "node", nodeID)
		detail(w, http.StatusNotFound, "Invalid or expired reset token")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	m.deactivate(ctx, taskNode)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"verified": true})
}
