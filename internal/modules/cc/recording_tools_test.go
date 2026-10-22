package cc

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// fakeRecAgent is the agent's computer_record_start / _stop: it records each call and answers
// from answers (by tool), as an agent with a browser recording going on would.
type fakeRecAgent struct {
	mu      sync.Mutex
	calls   []string // node tool
	answers map[string]map[string]any
}

func (f *fakeRecAgent) call(_ context.Context, nodeID, tool string, args map[string]any, uid int64) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := nodeID + " " + tool
	if tool == computeruse.ToolSwitch {
		call += map[bool]string{true: " on", false: " off"}[args["on"] == true]
	}
	f.calls = append(f.calls, call)
	return f.answers[tool]
}

func newRecToolEnv(t *testing.T, provider ...string) (*voiceEnv, *fakeRecAgent, *[]Recording) {
	t.Helper()
	prov := prompts.Qwen3_8B
	if len(provider) > 0 {
		prov = provider[0]
	}
	agent := &fakeRecAgent{answers: map[string]map[string]any{
		computeruse.ToolRecordStart: {"success": true, "recording_id": "rec_00aa11bb22cc33dd", "mode": "browser", "max_minutes": 30.0},
		computeruse.ToolRecordStop: {"success": true, "recording_id": "rec_00aa11bb22cc33dd", "mode": "browser", "events": 2.0,
			"trace": []any{map[string]any{"kind": "navigate", "url": "https://bank.example"}, map[string]any{"kind": "click"}}, "keyframes": 0.0},
	}}
	var learned []Recording
	ve := newVoiceEnv(t, prov, func(m *Module) {
		m.recordingAgent = agent.call
		m.learnRecording = func(_ context.Context, rec Recording) error { learned = append(learned, rec); return nil }
	})
	return ve, agent, &learned
}

func (ve *voiceEnv) recTurn(speaker int64) servertools.Turn {
	return servertools.Turn{ConversationID: "c1", HouseholdID: voiceHH, NodeID: ve.node.id, Speaker: servertools.Speaker{UserID: speaker}}
}

func runTool(t *testing.T, tool servertools.ServerTool, args string, turn servertools.Turn) map[string]any {
	t.Helper()
	call := servertools.Call{Name: tool.Name(), Args: pyjson.NewObject()}
	if args != "" {
		v, err := pyjson.Loads(args)
		if err != nil {
			t.Fatal(err)
		}
		call.Args = v.(*pyjson.Object)
	}
	res, err := tool.Execute(context.Background(), call, turn)
	if err != nil {
		t.Fatal(err)
	}
	b := pyjson.Dumps(res, false)
	var out map[string]any
	if err := json.Unmarshal([]byte(b), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// "Record this macro" on a computer starts the agent's recording, with computer control off as
// well as on (C11); with no computer, or nobody known, it says why and asks nothing of the agent.
func TestStartRecordingTool(t *testing.T) {
	ve, agent, _ := newRecToolEnv(t)
	start := &startRecordingTool{m: ve.m}

	// No computer at all: where to set one up.
	if out := runTool(t, start, "", ve.recTurn(7)); out["error"] != "no_computer" || !strings.Contains(out["message"].(string), "set it up") {
		t.Fatalf("no computer: %v", out)
	}
	if out := runTool(t, start, "", ve.recTurn(0)); out["error"] != "no_speaker" {
		t.Fatalf("unknown speaker: %v", out)
	}
	if len(agent.calls) != 0 {
		t.Fatalf("asked the agent: %v", agent.calls)
	}
	ve.setComputerControl("off")
	out := runTool(t, start, `{"task": "download my bank statement"}`, ve.recTurn(7))
	if out["status"] != "recording" || out["recording_id"] != "rec_00aa11bb22cc33dd" || !strings.Contains(out["message"].(string), "I'm done") {
		t.Fatalf("start: %v", out)
	}
	if len(agent.calls) != 1 || agent.calls[0] != ve.node.id+" computer_record_start" {
		t.Fatalf("agent calls %v", agent.calls)
	}

	// The agent refusing (no browser extension) is said, not hidden.
	agent.answers[computeruse.ToolRecordStart] = map[string]any{"success": false, "error": "no browser with the Jarvis extension is connected"}
	if out := runTool(t, start, "", ve.recTurn(7)); out["error"] != "start_failed" || !strings.Contains(out["message"].(string), "extension") {
		t.Fatalf("agent refused: %v", out)
	}
	// The browser without access to websites: its code, and where to turn the access on.
	agent.answers[computeruse.ToolRecordStart] = map[string]any{"success": false, "error_code": "no_site_access",
		"error": "Jarvis Browser Control doesn't have access to websites, so it can't see what you do. In Firefox, open about:addons, then Jarvis Browser Control, Permissions, and turn on Access your data for all websites."}
	if out := runTool(t, start, "", ve.recTurn(7)); out["error"] != "no_site_access" || !strings.Contains(out["message"].(string), "Access your data for all websites") {
		t.Fatalf("no site access: %v", out)
	}
}

// From a Pi in the room, the speaker's own computer is the one recorded.
func TestStartRecordingFindsTheSpeakersComputer(t *testing.T) {
	ve, agent, _ := newRecToolEnv(t)
	pc := ve.createNode("node-pc", voiceHH)
	if _, err := ve.d.Write.Exec(`UPDATE cc_nodes SET node_kind = 'desktop', owner_user_id = 7,
		capabilities = '{"computer_control":"on"}' WHERE node_id = ?`, pc.id); err != nil {
		t.Fatal(err)
	}
	start := &startRecordingTool{m: ve.m}
	if out := runTool(t, start, "", ve.recTurn(7)); out["status"] != "recording" || agent.calls[0] != "node-pc computer_record_start" {
		t.Fatalf("the speaker's computer: %v %v", out, agent.calls)
	}
	// Someone else's computer isn't watched.
	if out := runTool(t, start, "", ve.recTurn(8)); out["error"] != "no_computer" || !strings.Contains(out["message"].(string), "your own computer") {
		t.Fatalf("another member: %v", out)
	}
}

// "I'm done" stops the agent's recording and keeps the trace as the speaker's recording, under
// the agent's id, with what they said they'd show, and hands it to learning.
func TestStopStoresRecording(t *testing.T) {
	ve, agent, learned := newRecToolEnv(t)
	ve.setComputerControl("on")
	runTool(t, &startRecordingTool{m: ve.m}, `{"task": "download my bank statement"}`, ve.recTurn(7))
	out := runTool(t, &stopRecordingTool{m: ve.m}, "", ve.recTurn(7))
	if out["status"] != "saved" || out["recording_id"] != "rec_00aa11bb22cc33dd" || out["steps"] != 2.0 {
		t.Fatalf("stop: %v", out)
	}
	if agent.calls[1] != ve.node.id+" computer_record_stop" {
		t.Fatalf("agent calls %v", agent.calls)
	}
	rec, err := ve.m.getRecording(context.Background(), "rec_00aa11bb22cc33dd")
	if err != nil || rec == nil {
		t.Fatalf("not stored: %v", err)
	}
	if rec.OwnerID != 7 || rec.HouseholdID != voiceHH || rec.NodeID != ve.node.id || rec.Mode != "browser" || rec.Steps != 2 ||
		rec.Narration != "download my bank statement" || !strings.Contains(string(rec.Trace), "bank.example") {
		t.Fatalf("stored %+v", rec)
	}
	if len(*learned) != 1 || (*learned)[0].ID != rec.ID {
		t.Fatalf("learned %+v", *learned)
	}

	// Nothing going on: said plainly, nothing stored.
	agent.answers[computeruse.ToolRecordStop] = map[string]any{"success": false, "error": "no recording is going on", "error_code": "not_recording"}
	if out := runTool(t, &stopRecordingTool{m: ve.m}, `{"name": "bank statement"}`, ve.recTurn(7)); out["error"] != "not_recording" {
		t.Fatalf("nothing recording: %v", out)
	}
	if len(*learned) != 1 {
		t.Fatalf("learned again: %d", len(*learned))
	}
}

// The recording tools are offered only where a computer could be watched.
func TestRecordingToolsOffered(t *testing.T) {
	ve, _, _ := newRecToolEnv(t)
	ve.start("c0", computerTools)
	if got := offeredNames(ve.m.convs.get("c0")); got[toolStartRecording] || got[toolStopRecording] {
		t.Fatal("offered in a household without a computer")
	}
	ve.setComputerControl("off")
	ve.start("c1", computerTools)
	if got := offeredNames(ve.m.convs.get("c1")); !got[toolStartRecording] || !got[toolStopRecording] {
		t.Fatalf("not offered on a computer: %v", got)
	}
	if prompts.ServerToolAllowed(toolStartRecording, prompts.ToolGates{Computers: true}) {
		t.Fatal("offered with no known speaker")
	}
}

const uploadPath = "/api/v0/computer/recordings"

// A recording stopped on the computer is uploaded with node auth and kept for the member signed
// in there; the same upload twice is one recording.
func TestUploadRecordingRoute(t *testing.T) {
	ve, _, learned := newRecToolEnv(t)
	body := map[string]any{"success": true, "recording_id": "rec_feedface00000001", "mode": "browser", "reason": "stopped_here",
		"trace": []any{map[string]any{"kind": "click"}}, "events": 1, "keyframes": 0}

	// Node auth only.
	ve.do("POST", uploadPath, body, nil).want(400)
	ve.do("POST", uploadPath, body, testNode{id: ve.node.id, key: "wrong"}.h()).want(401)
	// A Pi doesn't record.
	ve.do("POST", uploadPath, body, ve.node.h()).want(403)
	// A computer nobody is signed in on: no owner for it.
	ve.makeDesktop(0)
	ve.do("POST", uploadPath, body, ve.node.h()).want(409)

	ve.makeDesktop(7)
	got := ve.do("POST", uploadPath, body, ve.node.h()).want(200).json()
	if got["id"] != "rec_feedface00000001" || got["steps"] != 1.0 {
		t.Fatalf("upload: %v", got)
	}
	rec, _ := ve.m.getRecording(context.Background(), "rec_feedface00000001")
	if rec == nil || rec.OwnerID != 7 || rec.NodeID != ve.node.id || rec.HouseholdID != voiceHH {
		t.Fatalf("stored %+v", rec)
	}
	ve.do("POST", uploadPath, body, ve.node.h()).want(200)
	if len(*learned) != 1 {
		t.Fatalf("the second upload was learnt again: %d", len(*learned))
	}

	// An id another household's computer already has: refused, and that row is left alone.
	if _, err := ve.m.createRecording(context.Background(), Recording{ID: "rec_feedface00000002", HouseholdID: "hh-other",
		OwnerID: 9, NodeID: "node-other", Mode: "browser"}); err != nil {
		t.Fatal(err)
	}
	theirs := map[string]any{"recording_id": "rec_feedface00000002", "mode": "browser", "trace": []any{map[string]any{"kind": "click"}}}
	if got := ve.do("POST", uploadPath, theirs, ve.node.h()).want(409).json(); got["id"] != nil || got["steps"] != nil {
		t.Fatalf("the conflict leaks the other row: %v", got)
	}
	if rec, _ := ve.m.getRecording(context.Background(), "rec_feedface00000002"); rec == nil || rec.HouseholdID != "hh-other" || rec.NodeID != "node-other" {
		t.Fatalf("the other household's row: %+v", rec)
	}
	if len(*learned) != 1 {
		t.Fatalf("a refused upload was learnt: %d", len(*learned))
	}

	// Malformed: a bad id, an unknown mode, a trace that isn't a list.
	for _, bad := range []map[string]any{
		{"recording_id": "../x", "mode": "browser", "trace": []any{}},
		{"recording_id": "rec_1", "mode": "voice", "trace": []any{}},
		{"recording_id": "rec_2", "mode": "browser", "trace": "clicks"},
	} {
		ve.do("POST", uploadPath, bad, ve.node.h()).want(400)
	}
}

