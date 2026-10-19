package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Recording from voice and from the computer (docs/agent/COMPUTER_CONTROL.md, plan P3 piece
// 29): "Jarvis, watch me" (start_recording) asks the speaker's computer to record what they do
// in the browser (computer_record_start), and "I'm done" (stop_recording) stops it
// (computer_record_stop) and keeps the trace as the speaker's recording. A recording stopped on
// the computer itself (its menu, its CLI, voice "stop", its 30 minutes) is uploaded by the agent
// to POST /api/v0/computer/recordings, owned by whoever is signed in there. Either way the
// recording keeps the agent's id (so computer_record_delete finds its local copy) and is handed
// to learnRecording (piece 30).

const (
	toolStartRecording = "start_recording"
	toolStopRecording  = "stop_recording"

	// recordingCallTimeout bounds asking the agent to start or stop (stop waits for the browser).
	recordingCallTimeout = 20 * time.Second
)

// recordingIDPattern is an agent's recording id (computer.newRecordingID).
var recordingIDPattern = regexp.MustCompile(`^rec_[A-Za-z0-9_-]{1,60}$`)

// agentRecordingCall runs one recording tool on the node: computer_record_start / _stop.
func (m *Module) agentRecordingCall(ctx context.Context, nodeID, tool string, args map[string]any, uid int64) map[string]any {
	if call := m.recordingAgent; call != nil {
		return call(ctx, nodeID, tool, args, uid)
	}
	if !m.bus.Available() {
		return map[string]any{"success": false, "error": ErrNoBroker.Error()}
	}
	if up, _ := m.bus.NodeSession(nodeID); !up {
		return map[string]any{"success": false, "error": "the computer is offline", "error_code": "offline"}
	}
	return m.dispatchNodeCommand(ctx, nodeID, tool, args, &uid, recordingCallTimeout)
}

// hasComputer is whether the household has a computer (a desktop agent), switch on or off: the
// recording tools are offered then (off, they say where to turn it on). A failed lookup offers
// none.
func (m *Module) hasComputer(ctx context.Context, hh string) bool {
	if hh == "" {
		return false
	}
	var one int
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT 1 FROM cc_nodes WHERE household_id = ? AND is_active
		AND (node_kind = 'desktop' OR install_mode = 'desktop-agent') LIMIT 1`, hh).Scan(&one)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		m.deps.Log.Warn("cc: computer lookup", "household", hh, "err", err)
	}
	return err == nil
}

// recordingNode is the computer a turn's recording happens on: the turn's own node when it is a
// computer, else the speaker's computer with computer control on. Not found: what to say.
func (m *Module) recordingNode(ctx context.Context, turn servertools.Turn) (string, string, error) {
	if turn.NodeID != "" {
		n, err := m.nodeByID(ctx, turn.NodeID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", "", err
		}
		if n != nil && n.desktop() {
			if !n.computerOn() {
				return "", `Computer control is off. Turn it on with "Computer control" in the Jarvis Automation menu on this computer.`, nil
			}
			return n.nodeID, "", nil
		}
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT `+nodeCols+` FROM cc_nodes
		WHERE household_id = ? AND is_active AND owner_user_id = ? ORDER BY last_seen DESC, node_id`, turn.HouseholdID, turn.Speaker.UserID)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return "", "", err
		}
		if n.computerOn() {
			return n.nodeID, "", nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	cc, err := m.computerControl(ctx, turn.HouseholdID)
	if err != nil {
		return "", "", err
	}
	if len(cc.on) == 0 {
		return "", cc.offMessage(), nil
	}
	return "", "I can only watch you on your own computer. Ask me there, or sign in to Jarvis Automation on it.", nil
}

// recordingRefusal is a recording tool's answer when it can't go ahead.
func recordingRefusal(code, msg string) *pyjson.Object {
	return servertools.Obj("error", code, "message", msg)
}

func recordingToolDefinition(name, description string, properties *pyjson.Object) *pyjson.Object {
	return watchToolDefinition(name, description, properties)
}

type startRecordingTool struct{ m *Module }

func (*startRecordingTool) Name() string { return toolStartRecording }

func (*startRecordingTool) Definition() *pyjson.Object {
	return recordingToolDefinition(toolStartRecording,
		"Start watching what the user does in their browser on their computer, so Jarvis can learn to do it. "+
			"Use when the user says 'watch me', 'watch how I do this', 'let me show you how to ...'. "+
			"Stop with stop_recording when they say they're done.",
		servertools.Obj("task", strProp("What the user is about to show, in their words, if they said (e.g. 'download my bank statement')")))
}

func (t *startRecordingTool) Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) (any, error) {
	m := t.m
	if e := toolSpeakerError(turn); e != nil {
		return e, nil
	}
	nodeID, why, err := m.recordingNode(ctx, turn)
	if err != nil {
		return nil, err
	}
	if nodeID == "" {
		return recordingRefusal("computer_control_off", why), nil
	}
	out := m.agentRecordingCall(ctx, nodeID, computeruse.ToolRecordStart, map[string]any{"mode": "browser"}, turn.Speaker.UserID)
	if !dispatchOK(out) {
		return recordingRefusal(agentCode(out, "start_failed"), "I couldn't start recording: "+agentError(out)), nil
	}
	id, _ := out["recording_id"].(string)
	if task := strings.TrimSpace(argText(call, "task")); task != "" && id != "" {
		m.recordingTasks.Store(id, task)
	}
	return servertools.Obj("status", "recording", "recording_id", id, "message",
		"I'm watching. Do it in your browser, then say \"I'm done\". I stop by myself after 30 minutes."), nil
}

type stopRecordingTool struct{ m *Module }

func (*stopRecordingTool) Name() string { return toolStopRecording }

func (*stopRecordingTool) Definition() *pyjson.Object {
	return recordingToolDefinition(toolStopRecording,
		"Stop watching the user (after start_recording) and keep what they showed, to learn from it. "+
			"Use when the user says 'I'm done', 'that's it', 'stop watching'.",
		servertools.Obj())
}

func (t *stopRecordingTool) Execute(ctx context.Context, _ servertools.Call, turn servertools.Turn) (any, error) {
	m := t.m
	if e := toolSpeakerError(turn); e != nil {
		return e, nil
	}
	nodeID, why, err := m.recordingNode(ctx, turn)
	if err != nil {
		return nil, err
	}
	if nodeID == "" {
		return recordingRefusal("computer_control_off", why), nil
	}
	out := m.agentRecordingCall(ctx, nodeID, computeruse.ToolRecordStop, map[string]any{}, turn.Speaker.UserID)
	if !dispatchOK(out) {
		if agentCode(out, "") == "not_recording" {
			return recordingRefusal("not_recording", "I wasn't watching: nothing is being recorded on your computer."), nil
		}
		return recordingRefusal(agentCode(out, "stop_failed"), "I couldn't stop recording: "+agentError(out)), nil
	}
	rec, err := recordingFromAgent(out)
	if err != nil {
		return nil, err
	}
	rec.HouseholdID, rec.OwnerID, rec.NodeID = turn.HouseholdID, turn.Speaker.UserID, nodeID
	rec, err = m.storeAgentRecording(ctx, rec)
	if err != nil {
		return nil, err
	}
	return servertools.Obj("status", "saved", "recording_id", rec.ID, "steps", rec.Steps, "message",
		fmt.Sprintf("Got it: I saved what you showed me (%d steps). I'll learn it as a workflow.", rec.Steps)), nil
}

// agentCode is a failed agent output's error_code (dflt when it has none).
func agentCode(out map[string]any, dflt string) string {
	if c, _ := out["error_code"].(string); c != "" {
		return c
	}
	if b, _ := out["timeout"].(bool); b {
		return "timeout"
	}
	return dflt
}

func agentError(out map[string]any) string {
	if e, _ := out["error"].(string); e != "" {
		return e
	}
	return "the computer didn't say why"
}

// recordingFromAgent reads computer_record_stop's output (computer.TraceOutput): the id, the
// mode, the trace (the events) and how many keyframes there are.
func recordingFromAgent(out map[string]any) (Recording, error) {
	id, _ := out["recording_id"].(string)
	if !recordingIDPattern.MatchString(id) {
		return Recording{}, fmt.Errorf("bad recording id %q", id)
	}
	mode, _ := out["mode"].(string)
	if mode != "browser" && mode != "desktop" {
		return Recording{}, fmt.Errorf("unknown recording mode %q", mode)
	}
	events, ok := out["trace"].([]any)
	if out["trace"] == nil {
		events, ok = []any{}, true
	}
	if !ok {
		return Recording{}, errors.New("the trace is not a list of events")
	}
	trace, err := json.Marshal(events)
	if err != nil {
		return Recording{}, err
	}
	rec := Recording{ID: id, Mode: mode, Trace: trace, Steps: len(events)}
	switch k := out["keyframes"].(type) {
	case float64:
		rec.Keyframes = int(k)
	case json.Number:
		n, _ := k.Int64()
		rec.Keyframes = int(n)
	}
	return rec, nil
}

// storeAgentRecording keeps an agent's recording under the agent's id and hands it to
// learnRecording. One already stored (the same trace uploaded twice) is returned as it is.
func (m *Module) storeAgentRecording(ctx context.Context, rec Recording) (Recording, error) {
	if old, err := m.getRecording(ctx, rec.ID); err != nil {
		return Recording{}, err
	} else if old != nil {
		if old.HouseholdID != rec.HouseholdID || old.NodeID != rec.NodeID {
			return Recording{}, fail(http.StatusConflict, "Recording id already in use")
		}
		return *old, nil
	}
	if task, ok := m.recordingTasks.LoadAndDelete(rec.ID); ok && rec.Narration == "" {
		rec.Narration, _ = task.(string)
	}
	steps := rec.Steps
	stored, err := m.createRecording(ctx, rec)
	if err != nil {
		return Recording{}, err
	}
	stored.Steps = steps
	m.deps.Log.Info("cc: recording stored", "recording", stored.ID, "node", stored.NodeID, "steps", steps)
	if m.learnRecording != nil {
		if err := m.learnRecording(ctx, stored); err != nil {
			m.deps.Log.Warn("cc: couldn't queue learning from a recording", "recording", stored.ID, "err", err)
		}
	}
	return stored, nil
}

// handleUploadRecording: POST /api/v0/computer/recordings, node auth. A computer's agent sends a
// recording the user stopped there (computer.TraceOutput: recording_id, mode, trace, keyframes);
// it is kept for the member signed in on that computer.
func (m *Module) handleUploadRecording(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	raw, ok := readRawObjectLimit(w, r, toolResultBodyMax)
	if !ok {
		return
	}
	if n.row == nil || !n.row.desktop() {
		detail(w, http.StatusForbidden, "Only a computer's agent uploads recordings")
		return
	}
	owner := deviceOwner(n)
	if owner == 0 {
		detail(w, http.StatusConflict, "Nobody is signed in on this computer: sign in again from the Jarvis Automation menu")
		return
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		validationError(w, "body: Input should be a valid dictionary")
		return
	}
	rec, err := recordingFromAgent(out)
	if err != nil {
		validationError(w, "body: "+err.Error())
		return
	}
	rec.HouseholdID, rec.OwnerID, rec.NodeID = n.HouseholdID, owner, n.ID
	stored, err := m.storeAgentRecording(r.Context(), rec)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "stored", "id": stored.ID, "steps": stored.Steps})
}
