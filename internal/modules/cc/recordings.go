package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Recordings (docs/agent/COMPUTER_CONTROL.md, plan P3 piece 26): the trace an agent uploads when
// the user shows Jarvis a task (piece 29), which a workflow is learnt from (piece 30). A
// recording is its owner's alone (it is what was on their screen): only they list, read or
// delete it. It is kept for computer.recording_retention_days from when it was stored; a daily
// sweep deletes what has expired and asks the agent that recorded it to delete its local copy
// (computer_record_delete, piece 29; an agent without the tool, or offline, keeps its copy).

const (
	settingRecordingRetention = "computer.recording_retention_days"
	defaultRecordingRetention = 30

	recordingSweepJob  = "cc.recording_sweep"
	recordingSweepCron = "17 4 * * *" // daily, UTC

	// recordingDeleteTimeout bounds asking one agent to delete its copy.
	recordingDeleteTimeout = 10 * time.Second
)

func recordingDefinitions() []settings.Definition {
	return []settings.Definition{
		{Key: settingRecordingRetention, Category: "computer", Type: settings.Int, Default: int64(defaultRecordingRetention),
			Description: "Days a recording of a task you showed Jarvis is kept, here and on the computer that recorded it.",
			Validate: func(v any) error {
				if n, ok := v.(int64); ok && (n < 1 || n > 3650) {
					return errors.New("must be 1 to 3650 days")
				}
				return nil
			}},
	}
}

// Recording is one row of cc_recordings.
type Recording struct {
	ID          string          `json:"id"`
	HouseholdID string          `json:"household_id"`
	OwnerID     int64           `json:"owner_id"`
	NodeID      string          `json:"node_id,omitempty"`
	Mode        string          `json:"mode"` // browser | desktop
	Trace       json.RawMessage `json:"trace,omitempty"`
	Narration   string          `json:"narration"`
	Keyframes   int             `json:"keyframes"`
	Steps       int             `json:"steps"` // the trace's events (0 when it isn't a list)
	CreatedAt   time.Time       `json:"created_at"`
	ExpiresAt   time.Time       `json:"expires_at"`
}

// recordingRetention is the household's retention in days (the default when unset or invalid).
func (m *Module) recordingRetention(ctx context.Context, hh string) int {
	if m.settings == nil {
		return defaultRecordingRetention
	}
	n := m.settings.Int(ctx, settingRecordingRetention, settings.Scope{HouseholdID: hh})
	if n < 1 {
		return defaultRecordingRetention
	}
	return int(n)
}

// createRecording stores a recording: its id (unless the agent's is given), created_at and
// expires_at (created_at + the household's retention) are filled in. It returns the stored row.
func (m *Module) createRecording(ctx context.Context, rec Recording) (Recording, error) {
	if rec.HouseholdID == "" || rec.OwnerID == 0 {
		return Recording{}, errors.New("a recording needs a household and an owner")
	}
	if rec.Mode != "browser" && rec.Mode != "desktop" {
		return Recording{}, fmt.Errorf("unknown recording mode %q", rec.Mode)
	}
	if len(rec.Trace) == 0 {
		rec.Trace = json.RawMessage("[]")
	}
	if !json.Valid(rec.Trace) {
		return Recording{}, errors.New("the trace is not JSON")
	}
	switch {
	case rec.ID == "":
		rec.ID = "rec_" + randHex(8)
	case !recordingIDPattern.MatchString(rec.ID): // an agent's own id (recording_tools.go)
		return Recording{}, fmt.Errorf("bad recording id %q", rec.ID)
	}
	now := m.now().UTC().Truncate(time.Millisecond)
	rec.CreatedAt = now
	rec.ExpiresAt = now.AddDate(0, 0, m.recordingRetention(ctx, rec.HouseholdID))
	_, err := m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_recordings (id, household_id, owner_id, node_id,
		mode, trace, narration, keyframes, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.HouseholdID, rec.OwnerID, sql.NullString{String: rec.NodeID, Valid: rec.NodeID != ""},
		rec.Mode, string(rec.Trace), rec.Narration, rec.Keyframes, dbTime(rec.CreatedAt), dbTime(rec.ExpiresAt))
	return rec, err
}

// recordingColumns without the trace (lists), and with it (one recording).
const recordingColumns = `id, household_id, owner_id, node_id, mode, narration, keyframes, created_at, expires_at,
	CASE json_type(trace) WHEN 'array' THEN json_array_length(trace) ELSE 0 END`

func scanRecording(sc interface{ Scan(...any) error }, trace bool) (Recording, error) {
	var (
		rec    Recording
		node   sql.NullString
		ca, ea string
		tr     string
	)
	dest := []any{&rec.ID, &rec.HouseholdID, &rec.OwnerID, &node, &rec.Mode, &rec.Narration, &rec.Keyframes, &ca, &ea, &rec.Steps}
	if trace {
		dest = append(dest, &tr)
	}
	if err := sc.Scan(dest...); err != nil {
		return Recording{}, err
	}
	rec.NodeID, rec.CreatedAt, rec.ExpiresAt = node.String, parseTS(ca), parseTS(ea)
	if trace {
		rec.Trace = json.RawMessage(tr)
	}
	return rec, nil
}

// getRecording is one recording with its trace (nil when there is none).
func (m *Module) getRecording(ctx context.Context, id string) (*Recording, error) {
	rec, err := scanRecording(m.deps.DB.Read.QueryRowContext(ctx,
		`SELECT `+recordingColumns+`, trace FROM cc_recordings WHERE id = ?`, id), true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// listRecordings is the owner's recordings in the household, newest first, without traces.
func (m *Module) listRecordings(ctx context.Context, hh string, owner int64) ([]Recording, error) {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT `+recordingColumns+` FROM cc_recordings
		WHERE household_id = ? AND owner_id = ? ORDER BY created_at DESC, id`, hh, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Recording{}
	for rows.Next() {
		rec, err := scanRecording(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// deleteRecordingRow deletes a recording and forgets it on the workflows learnt from it.
func (m *Module) deleteRecordingRow(ctx context.Context, id string) error {
	tx, err := m.deps.DB.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM cc_recordings WHERE id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE cc_taught_workflows SET recording_id = NULL WHERE recording_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteRecording deletes the row, then asks the agent to delete its copy (best effort).
func (m *Module) deleteRecording(ctx context.Context, rec Recording) error {
	if err := m.deleteRecordingRow(ctx, rec.ID); err != nil {
		return err
	}
	m.forgetAgentRecording(ctx, rec)
	return nil
}

// forgetAgentRecording asks the recording's agent to delete its local copy. A failure (no such
// tool before piece 29, the agent offline) is logged, never returned: the server's row is gone.
func (m *Module) forgetAgentRecording(ctx context.Context, rec Recording) {
	if rec.NodeID == "" || m.recordingDeleter == nil {
		return
	}
	if err := m.recordingDeleter(ctx, rec.NodeID, rec.ID); err != nil {
		m.deps.Log.Info("cc: agent kept its copy of a deleted recording", "recording", rec.ID, "node", rec.NodeID, "err", err)
	}
}

// agentDeleteRecording runs computer_record_delete on the node, when it is connected.
func (m *Module) agentDeleteRecording(ctx context.Context, nodeID, recID string) error {
	if !m.bus.Available() {
		return ErrNoBroker
	}
	if up, _ := m.bus.NodeSession(nodeID); !up {
		return errors.New("the agent is offline")
	}
	out := m.dispatchNodeCommand(ctx, nodeID, computeruse.ToolRecordDelete, map[string]any{"recording_id": recID}, nil, recordingDeleteTimeout)
	if !dispatchOK(out) {
		return fmt.Errorf("computer_record_delete: %v", out["error"])
	}
	return nil
}

// sweepRecordings deletes every expired recording (and asks each agent to delete its copy).
func (m *Module) sweepRecordings(ctx context.Context) error {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT `+recordingColumns+` FROM cc_recordings
		WHERE expires_at <= ? ORDER BY expires_at`, dbTime(m.now()))
	if err != nil {
		return err
	}
	var expired []Recording
	for rows.Next() {
		rec, err := scanRecording(rows, false)
		if err != nil {
			rows.Close()
			return err
		}
		expired = append(expired, rec)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, rec := range expired {
		if err := m.deleteRecording(ctx, rec); err != nil {
			return err
		}
	}
	if len(expired) > 0 {
		m.deps.Log.Info("cc: expired recordings deleted", "count", len(expired))
	}
	return nil
}

// --- registration ---

func (m *Module) registerRecordings(mux *http.ServeMux) {
	if m.recordingDeleter == nil {
		m.recordingDeleter = m.agentDeleteRecording
	}
	if m.deps.Queue != nil {
		m.deps.Queue.Register(recordingSweepJob, queue.Handler{Run: func(ctx context.Context, _ queue.Job) ([]byte, error) {
			return nil, m.sweepRecordings(ctx)
		}, Concurrency: 1})
	}
	const v0 = "/api/v0"
	mux.HandleFunc("GET "+v0+"/recordings", m.user(m.handleListRecordings))
	mux.HandleFunc("GET "+v0+"/recordings/{id}", m.user(m.handleGetRecording))
	mux.HandleFunc("DELETE "+v0+"/recordings/{id}", m.user(m.handleDeleteRecording))
}

// startRecordings schedules the daily retention sweep (D27: a trigger, not a loop).
func (m *Module) startRecordings(ctx context.Context) error {
	if m.deps.Scheduler == nil {
		return nil
	}
	return m.deps.Scheduler.Ensure(ctx, scheduler.Trigger{
		Name: recordingSweepJob, Kind: scheduler.KindCron, JobType: recordingSweepJob,
		Spec: scheduler.Spec{Cron: recordingSweepCron},
	})
}

// --- routes ---

func (m *Module) handleListRecordings(w http.ResponseWriter, r *http.Request, u authn.User) {
	hh, ok := householdQuery(w, r)
	if !ok {
		return
	}
	if _, member, err := m.Auth.HouseholdRole(r.Context(), u.ID, hh); err != nil {
		m.internalError(w, err)
		return
	} else if !member {
		detail(w, http.StatusForbidden, "User is not a member of this household")
		return
	}
	recs, err := m.listRecordings(r.Context(), hh, u.ID)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"recordings": recs})
}

// ownRecording loads the path's recording: 404 unless the caller owns it and is still a member
// of its household.
func (m *Module) ownRecording(ctx context.Context, u authn.User, id string) (*Recording, error) {
	rec, err := m.getRecording(ctx, id)
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.OwnerID != u.ID {
		return nil, fail(http.StatusNotFound, "Recording not found")
	}
	if _, member, err := m.Auth.HouseholdRole(ctx, u.ID, rec.HouseholdID); err != nil {
		return nil, err
	} else if !member {
		return nil, fail(http.StatusNotFound, "Recording not found")
	}
	return rec, nil
}

func (m *Module) handleGetRecording(w http.ResponseWriter, r *http.Request, u authn.User) {
	rec, err := m.ownRecording(r.Context(), u, r.PathValue("id"))
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rec)
}

func (m *Module) handleDeleteRecording(w http.ResponseWriter, r *http.Request, u authn.User) {
	rec, err := m.ownRecording(r.Context(), u, r.PathValue("id"))
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if err := m.deleteRecordingRow(r.Context(), rec.ID); err != nil {
		m.internalError(w, err)
		return
	}
	// The agent's copy goes in the background: an agent can take a while to answer.
	go m.forgetAgentRecording(context.WithoutCancel(r.Context()), *rec)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "deleted", "id": rec.ID})
}
