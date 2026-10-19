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

// Recording from voice and from the computer (docs/agent/COMPUTER-CONTROL.md C10, C11, plan P3
// piece 29): "record this macro", "watch me" (start_recording) asks a computer to record what
// the user does in the browser (computer_record_start), and "I'm done" (stop_recording) stops it
// (computer_record_stop) and keeps the trace as the speaker's recording, named. Recording isn't
// gated by computer control: the computer you're at records, else the one you name or your only
// one (pickComputer). A recording stopped on
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

// agentRecordingCall runs one of the agent's computer tools on the node: computer_record_start /
// _stop, computer_switch.
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
// recording and computer control tools are offered then. A failed lookup offers none.
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
		"Call this (never just reply) when the user says 'record this macro', 'record a macro called ...', 'watch me', "+
			"'watch me do this', 'learn how I do this', 'let me show you how to ...': it starts recording a macro, watching what "+
			"they do in the browser on their computer so Jarvis can learn to do it. Saying you're watching without it records "+
			"nothing. Not for reminders, notes or audio. Stop with stop_recording when they say they're done.",
		servertools.Obj("task", strProp("The macro's name or what the user is about to show, in their words (e.g. 'pay the water bill' from 'record a macro called pay the water bill'); empty for 'record this macro' or 'watch me' alone"),
			"computer", computerProp()))
}

func (t *startRecordingTool) Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) (any, error) {
	m := t.m
	if e := toolSpeakerError(turn); e != nil {
		return e, nil
	}
	p, err := m.pickComputer(ctx, turn, argText(call, "computer"), ownComputers, nil)
	if err != nil || p.refusal != nil {
		return p.refusal, err
	}
	nodeID := p.target.row.nodeID
	out := m.agentRecordingCall(ctx, nodeID, computeruse.ToolRecordStart, map[string]any{"mode": "browser"}, turn.Speaker.UserID)
	if !dispatchOK(out) {
		return recordingRefusal(agentCode(out, "start_failed"), "I couldn't start recording: "+agentError(out)), nil
	}
	id, _ := out["recording_id"].(string)
	if task := strings.TrimSpace(argText(call, "task")); task != "" && id != "" {
		m.recordingTasks.Store(id, task)
		m.recordingNamed.Store(nodeID, id)
	} else {
		m.recordingNamed.Delete(nodeID)
	}
	return servertools.Obj("status", "recording", "recording_id", id, "message",
		"I'm watching. Do it in your browser, then say \"I'm done\". I stop by myself after 30 minutes."), nil
}

type stopRecordingTool struct{ m *Module }

func (*stopRecordingTool) Name() string { return toolStopRecording }

func (*stopRecordingTool) Definition() *pyjson.Object {
	return recordingToolDefinition(toolStopRecording,
		"Stop recording the macro (after start_recording) and keep what the user showed, to learn from it. "+
			"Use when the user says 'I'm done', 'that's it', 'stop recording', 'stop watching'. If it answers need_name, "+
			"ask what to call the macro and call it again with the name.",
		servertools.Obj("name", strProp("The macro's name, when the user gave one (e.g. 'pay the water bill')"),
			"computer", computerProp()))
}

func (t *stopRecordingTool) Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) (any, error) {
	m := t.m
	if e := toolSpeakerError(turn); e != nil {
		return e, nil
	}
	p, err := m.pickComputer(ctx, turn, argText(call, "computer"), ownComputers, nil)
	if err != nil || p.refusal != nil {
		return p.refusal, err
	}
	nodeID := p.target.row.nodeID
	name := strings.TrimSpace(argText(call, "name"))
	if name == "" {
		if _, named := m.recordingNamed.Load(nodeID); !named {
			return recordingRefusal("need_name", "What should I call this macro? The recording keeps going until you tell me."), nil
		}
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
	m.recordingNamed.Delete(nodeID)
	rec.HouseholdID, rec.OwnerID, rec.NodeID, rec.Narration = turn.HouseholdID, turn.Speaker.UserID, nodeID, name
	rec, err = m.storeAgentRecording(ctx, rec)
	if err != nil {
		return nil, err
	}
	saved := "what you showed me"
	if rec.Narration != "" {
		saved = `"` + rec.Narration + `"`
	}
	return servertools.Obj("status", "saved", "recording_id", rec.ID, "steps", rec.Steps, "message",
		fmt.Sprintf("Got it: I saved %s (%d steps). I'll learn it as a workflow.", saved, rec.Steps)), nil
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
