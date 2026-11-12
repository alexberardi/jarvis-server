package cc

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Send to Forge (2026-11-09): after a test install, the developer sends the install result, what
// the draft's commands did during the node's test window and an optional note back to the Forge
// draft, so the model sees where the bug is. Only the draft's own commands are recorded, only on
// the node that installed it, and nothing leaves jarvisd until the developer sends it.

const (
	// testRunWindow is how long a node serves a test install (agent packages testTTL).
	testRunWindow = 20 * time.Minute
	// maxTestRuns and maxTestRunOutput keep a report inside Pantry's 256 KB limit, with room
	// for the events sent in it (maxReportEvents, each up to maxTestEvent).
	maxTestRuns      = 20
	maxTestRunOutput = 8 << 10
	// maxTestEvents is how many events a test install keeps; the report sends the newest
	// maxReportEvents of them.
	maxTestEvents   = 500
	maxReportEvents = 80
	maxTestEvent    = 2 << 10
)

// recordTestRuns keeps the results of the node's test-installed commands. conv.mu is held.
func (m *Module) recordTestRuns(ctx context.Context, conv *conversation, results []toolResult) {
	if conv.nodeID == "" || len(results) == 0 {
		return
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT id, results_json FROM cc_test_install_requests
		WHERE node_id = ? AND status = ? AND completed_at > ? ORDER BY completed_at`,
		conv.nodeID, pkgCompleted, dbTime(m.now().Add(-testRunWindow)))
	if err != nil {
		m.deps.Log.Warn("cc: test install runs lookup failed", "err", err)
		return
	}
	requestFor := map[string]string{} // command -> request id; the newest install wins
	for rows.Next() {
		var id string
		var res sql.NullString
		if rows.Scan(&id, &res) != nil {
			continue
		}
		var d struct {
			Commands []string `json:"commands"`
		}
		_ = json.Unmarshal([]byte(res.String), &d)
		for _, c := range d.Commands {
			requestFor[c] = id
		}
	}
	rows.Close()
	if len(requestFor) == 0 {
		return
	}
	calls := map[string]parse.ToolCall{}
	for _, msg := range conv.messages {
		for _, c := range msg.ToolCalls {
			calls[c.ID] = c
		}
	}
	for _, r := range results {
		c := calls[r.ToolCallID]
		rid, ok := requestFor[c.Function.Name]
		if !ok {
			continue
		}
		out := outputString(r.Output)
		if len(out) > maxTestRunOutput {
			out = out[:maxTestRunOutput] + "…[truncated]"
		}
		if _, err := m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_test_install_runs (request_id, command, arguments, output, created_at)
			SELECT ?, ?, ?, ?, ? WHERE (SELECT COUNT(*) FROM cc_test_install_runs WHERE request_id = ?) < ?`,
			rid, c.Function.Name, c.Function.Arguments, out, dbTime(m.now()), rid, maxTestRuns); err != nil {
			m.deps.Log.Warn("cc: test install run not recorded", "request_id", rid, "err", err)
		}
	}
}

// testRuns are a request's recorded runs, oldest first; JSON arguments and outputs are decoded.
func (m *Module) testRuns(ctx context.Context, rid string) ([]any, error) {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT command, arguments, output, created_at
		FROM cc_test_install_runs WHERE request_id = ? ORDER BY id`, rid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var command, at string
		var args, output sql.NullString
		if err := rows.Scan(&command, &args, &output, &at); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"command": command, "arguments": jsonOrText(args),
			"output": jsonOrText(output), "at": pyNaive(parseTS(at))})
	}
	return out, rows.Err()
}

// handleTestInstallEvents keeps what a test install did on the node (its sidecar's log lines,
// scans and device controls, secrets already masked there), for the poll and Send to Forge. Only
// the node that runs it, only while the test window lasts, at most maxTestEvents.
func (m *Module) handleTestInstallEvents(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	nodeID := r.PathValue("node_id")
	if n.ID != nodeID {
		detail(w, http.StatusForbidden, "Node mismatch")
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	list, _ := b.m["events"].([]any)
	if !b.has("events") {
		b.fail("events", "Field required")
	}
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	t, err := loadTI(ctx, m.deps.DB.Read, r.PathValue("request_id"), nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Test install request not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if t.status != pkgCompleted || !t.completedAt.Valid || parseTS(t.completedAt.String).Before(m.now().Add(-testRunWindow-time.Minute)) {
		detail(w, http.StatusConflict, "The test window is over")
		return
	}
	now := dbTime(m.now())
	kept := 0
	for _, e := range list {
		ev, isObj := e.(map[string]any)
		if !isObj {
			continue
		}
		kind, _ := ev["kind"].(string)
		raw, _ := json.Marshal(ev)
		if len(raw) > maxTestEvent {
			ev["message"], ev["data"], ev["error"] = "[too long: cut]", nil, nil
			raw, _ = json.Marshal(ev)
		}
		res, err := m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_test_install_events (request_id, kind, event, created_at)
			SELECT ?, ?, ?, ? WHERE (SELECT COUNT(*) FROM cc_test_install_events WHERE request_id = ?) < ?`,
			t.id, kind, string(raw), now, t.id, maxTestEvents)
		if err != nil {
			m.internalError(w, err)
			return
		}
		if c, _ := res.RowsAffected(); c > 0 {
			kept++
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"kept": kept})
}

// testEvents are a request's events, oldest first, as the node sent them.
func (m *Module) testEvents(ctx context.Context, rid string) ([]any, error) {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT event FROM cc_test_install_events WHERE request_id = ? ORDER BY id`, rid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var v any
		if json.Unmarshal([]byte(raw), &v) == nil {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}

func jsonOrText(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	var v any
	if json.Unmarshal([]byte(s.String), &v) == nil {
		return v
	}
	return s.String
}

// handleTestInstallFeedback is Send to Forge: POST the report to the draft on the household's
// Pantry. The caller is checked as for the poll; the share code addresses the draft.
func (m *Module) handleTestInstallFeedback(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok {
		return
	}
	b, _, ok := readBody(w, r, true)
	if !ok {
		return
	}
	note, _ := b.optStrPtr("note")
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	t, err := loadTI(ctx, m.deps.DB.Read, r.PathValue("request_id"), r.PathValue("node_id"))
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Test install request not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if err := m.requirePkgHousehold(ctx, auth, t.householdID); err != nil {
		m.writeErr(w, err)
		return
	}
	if !m.requirePantry(w, r, t.householdID) {
		return
	}
	if t.live() {
		detail(w, http.StatusConflict, "The test install hasn't finished yet")
		return
	}
	runs, err := m.testRuns(ctx, t.id)
	if err != nil {
		m.internalError(w, err)
		return
	}
	events, err := m.testEvents(ctx, t.id)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if len(events) > maxReportEvents { // the newest: what happened last is what needs fixing
		events = events[len(events)-maxReportEvents:]
	}
	poll := tiPoll(t)
	report := map[string]any{"install": map[string]any{"package_name": t.packageName, "status": t.status,
		"error": poll["error_message"], "details": poll["details"]}, "runs": runs, "events": events}
	if note != nil && *note != "" {
		report["note"] = *note
	}
	raw, _ := json.Marshal(report)
	pantry, err := m.pantryURL(ctx, t.householdID)
	if err != nil {
		noPantryStore(w, err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		pantryDraftURL(pantry, t.shareCode)+"/feedback", bytes.NewReader(raw))
	if err != nil {
		detail(w, http.StatusBadGateway, "Could not reach Pantry service")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.pantryHTTP.Do(req)
	if err != nil {
		m.deps.Log.Warn("cc: Pantry unreachable for Send to Forge", "err", err)
		detail(w, http.StatusBadGateway, "Could not reach Pantry service")
		return
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		detail(w, http.StatusNotFound, "The Forge draft is gone")
		return
	case resp.StatusCode != http.StatusCreated:
		m.deps.Log.Warn("cc: Pantry refused Send to Forge", "status", resp.StatusCode)
		detail(w, http.StatusBadGateway, "Pantry returned an error")
		return
	}
	m.deps.Log.Info("cc: sent to Forge", "request_id", t.id, "runs", len(runs))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "sent", "runs": len(runs)})
}
