package cc

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

// addComputer adds a desktop agent to the env's household, owned by owner (0: nobody), named
// name, reporting computer control as state ("on" / "off").
func (ve *voiceEnv) addComputer(id string, owner int64, name, state string) {
	ve.t.Helper()
	n := ve.createNode(id, voiceHH)
	ve.computerRow(n.id, owner, name, state)
}

// computerRow makes node id a desktop agent (owner, name, switch state).
func (ve *voiceEnv) computerRow(id string, owner int64, name, state string) {
	ve.t.Helper()
	var o any
	if owner != 0 {
		o = owner
	}
	if _, err := ve.d.Write.Exec(`UPDATE cc_nodes SET node_kind = 'desktop', owner_user_id = ?, capabilities = ? WHERE node_id = ?`,
		o, `{"computer_control":"`+state+`"}`, id); err != nil {
		ve.t.Fatal(err)
	}
	if err := ve.auth.RenameNode(context.Background(), id, name); err != nil {
		ve.t.Fatal(err)
	}
}

func newSwitchEnv(t *testing.T) (*voiceEnv, *fakeRecAgent) {
	ve, agent, _ := newRecToolEnv(t)
	agent.answers[computeruse.ToolSwitch] = map[string]any{"success": true}
	return ve, agent
}

// At a computer, "enable computer control" turns that computer's switch on through the agent
// (the menu's path) and says so; already on, it says that and asks nothing.
func TestEnableComputerControlHere(t *testing.T) {
	ve, agent := newSwitchEnv(t)
	ve.computerRow(ve.node.id, 7, "MacBook", "off")
	enable := &enableComputerControlTool{m: ve.m}
	out := runTool(t, enable, "", ve.recTurn(7))
	if out["status"] != "on" || out["message"] != "Computer control is on for the MacBook." {
		t.Fatalf("enable: %v", out)
	}
	if len(agent.calls) != 1 || agent.calls[0] != ve.node.id+" computer_switch on" {
		t.Fatalf("agent calls %v", agent.calls)
	}
	ve.computerRow(ve.node.id, 7, "MacBook", "on")
	if out := runTool(t, enable, "", ve.recTurn(7)); out["status"] != "on" || !strings.Contains(out["message"].(string), "already on") || len(agent.calls) != 1 {
		t.Fatalf("already on: %v %v", out, agent.calls)
	}
	// The agent refusing or offline is said, not hidden.
	ve.computerRow(ve.node.id, 7, "MacBook", "off")
	agent.answers[computeruse.ToolSwitch] = map[string]any{"success": false, "error": "the computer is offline", "error_code": "offline"}
	if out := runTool(t, enable, "", ve.recTurn(7)); out["error"] != "offline" || !strings.Contains(out["message"].(string), "MacBook") {
		t.Fatalf("offline: %v", out)
	}
	if out := runTool(t, enable, "", ve.recTurn(0)); out["error"] != "no_speaker" {
		t.Fatalf("unknown speaker: %v", out)
	}
	// Never from something that didn't ask for it (C11): "turn on the computer".
	calls := len(agent.calls)
	turn := ve.recTurn(7)
	turn.Utterance = "turn on the computer"
	if out := runTool(t, enable, "", turn); out["error"] != "not_asked" || len(agent.calls) != calls {
		t.Fatalf("not asked: %v %v", out, agent.calls)
	}
	turn.Utterance = "Hey Jarvis, enable computer control"
	if out := runTool(t, enable, "", turn); out["error"] == "not_asked" {
		t.Fatalf("asked: %v", out)
	}
}

// From another node (a Pi): the computer named, else the speaker's only one, else ask which.
// Someone else's computer isn't turned on from elsewhere.
func TestComputerControlTargeting(t *testing.T) {
	ve, agent := newSwitchEnv(t)
	enable := &enableComputerControlTool{m: ve.m}

	if out := runTool(t, enable, "", ve.recTurn(7)); out["error"] != "no_computer" || !strings.Contains(out["message"].(string), "set it up") {
		t.Fatalf("no computer: %v", out)
	}
	ve.addComputer("node-mbp", 7, "MacBook", "off")
	if out := runTool(t, enable, "", ve.recTurn(7)); out["message"] != "Computer control is on for the MacBook." || agent.calls[0] != "node-mbp computer_switch on" {
		t.Fatalf("the only one: %v %v", out, agent.calls)
	}
	ve.addComputer("node-imac", 7, "iMac", "off")
	out := runTool(t, enable, "", ve.recTurn(7))
	if out["error"] != "which_computer" || !strings.Contains(out["message"].(string), "MacBook") || !strings.Contains(out["message"].(string), "iMac") {
		t.Fatalf("several: %v", out)
	}
	if len(agent.calls) != 1 {
		t.Fatalf("asked an agent while ambiguous: %v", agent.calls)
	}
	if out := runTool(t, enable, `{"computer": "the iMac"}`, ve.recTurn(7)); out["message"] != "Computer control is on for the iMac." || agent.calls[1] != "node-imac computer_switch on" {
		t.Fatalf("named: %v %v", out, agent.calls)
	}
	if out := runTool(t, enable, `{"computer": "the laptop in the attic"}`, ve.recTurn(7)); out["error"] != "which_computer" || !strings.Contains(out["message"].(string), "laptop in the attic") {
		t.Fatalf("unknown name: %v", out)
	}
	// Another member: their own computers only.
	if out := runTool(t, enable, `{"computer": "MacBook"}`, ve.recTurn(8)); out["error"] != "no_computer" || len(agent.calls) != 2 {
		t.Fatalf("someone else's computer: %v %v", out, agent.calls)
	}
}

// "Disable computer control" works from anywhere: the computer named, or the one with it on,
// whoever's it is; it asks the agent (which stops a running task) and says what it did.
func TestDisableComputerControl(t *testing.T) {
	ve, agent := newSwitchEnv(t)
	disable := &disableComputerControlTool{m: ve.m}
	ve.addComputer("node-mbp", 7, "MacBook", "on")
	ve.addComputer("node-imac", 7, "iMac", "off")
	out := runTool(t, disable, "", ve.recTurn(8))
	if out["status"] != "off" || out["message"] != "Computer control is off for the MacBook." || agent.calls[0] != "node-mbp computer_switch off" {
		t.Fatalf("disable: %v %v", out, agent.calls)
	}
	ve.computerRow("node-mbp", 7, "MacBook", "off")
	if out := runTool(t, disable, "", ve.recTurn(8)); out["status"] != "off" || !strings.Contains(out["message"].(string), "already off") || len(agent.calls) != 1 {
		t.Fatalf("nothing on: %v %v", out, agent.calls)
	}
	// Off is the safe direction: a computer last seen off is still asked, in case it came on since.
	if out := runTool(t, disable, `{"computer": "iMac"}`, ve.recTurn(8)); out["message"] != "Computer control is off for the iMac." || len(agent.calls) != 2 || agent.calls[1] != "node-imac computer_switch off" {
		t.Fatalf("last seen off: %v %v", out, agent.calls)
	}
	// One that can't be reached and was last seen off: it says so rather than failing.
	agent.answers[computeruse.ToolSwitch] = map[string]any{"success": false, "error": "the computer is offline", "error_code": "offline"}
	if out := runTool(t, disable, `{"computer": "iMac"}`, ve.recTurn(8)); out["status"] != "off" || !strings.Contains(out["message"].(string), "already off") {
		t.Fatalf("offline, last seen off: %v %v", out, agent.calls)
	}
}

// "Is computer control on?" answers from the heartbeat, for this computer or the speaker's.
func TestComputerControlStatus(t *testing.T) {
	ve, agent := newSwitchEnv(t)
	status := &computerControlStatusTool{m: ve.m}
	ve.addComputer("node-mbp", 7, "MacBook", "on")
	if out := runTool(t, status, "", ve.recTurn(7)); out["message"] != "Computer control is on for the MacBook." {
		t.Fatalf("one: %v", out)
	}
	ve.addComputer("node-imac", 7, "iMac", "off")
	if out := runTool(t, status, "", ve.recTurn(7)); out["message"] != "Computer control is off for the iMac and on for the MacBook." {
		t.Fatalf("several: %v", out)
	}
	ve.computerRow(ve.node.id, 7, "Desk PC", "off")
	if out := runTool(t, status, "", ve.recTurn(7)); out["message"] != "Computer control is off for the Desk PC." {
		t.Fatalf("here: %v", out)
	}
	if len(agent.calls) != 0 {
		t.Fatalf("asked an agent: %v", agent.calls)
	}
}

// Recording isn't gated by computer control (C11): with the switch off the computer you're at
// records, and from elsewhere the one you name or your only one; several: ask which.
func TestRecordingWithComputerControlOff(t *testing.T) {
	ve, agent, _ := newRecToolEnv(t)
	start := &startRecordingTool{m: ve.m}
	ve.addComputer("node-mbp", 7, "MacBook", "off")
	ve.addComputer("node-imac", 7, "iMac", "off")
	if out := runTool(t, start, "", ve.recTurn(7)); out["error"] != "which_computer" || len(agent.calls) != 0 {
		t.Fatalf("several: %v %v", out, agent.calls)
	}
	if out := runTool(t, start, `{"computer": "MacBook", "task": "pay the water bill"}`, ve.recTurn(7)); out["status"] != "recording" || agent.calls[0] != "node-mbp computer_record_start" {
		t.Fatalf("named: %v %v", out, agent.calls)
	}
	ve.computerRow(ve.node.id, 0, "Desk PC", "off")
	if out := runTool(t, start, "", ve.recTurn(8)); out["status"] != "recording" || agent.calls[1] != ve.node.id+" computer_record_start" {
		t.Fatalf("here, switch off: %v %v", out, agent.calls)
	}
}

// "I'm done" without a name for the macro asks for one and leaves the recording going; with
// one (now or from start) it stops and keeps it under that name.
func TestStopRecordingAsksForAName(t *testing.T) {
	ve, agent, _ := newRecToolEnv(t)
	ve.computerRow(ve.node.id, 7, "MacBook", "off")
	runTool(t, &startRecordingTool{m: ve.m}, `{"task": "learn how I do this"}`, ve.recTurn(7)) // the phrase isn't a name
	stop := &stopRecordingTool{m: ve.m}
	out := runTool(t, stop, "", ve.recTurn(7))
	if out["error"] != "need_name" || !strings.Contains(out["message"].(string), "call") || len(agent.calls) != 1 {
		t.Fatalf("no name: %v %v", out, agent.calls)
	}
	out = runTool(t, stop, `{"name": "pay the water bill"}`, ve.recTurn(7))
	if out["status"] != "saved" || !strings.Contains(out["message"].(string), "pay the water bill") {
		t.Fatalf("named: %v", out)
	}
	rec, err := ve.m.getRecording(context.Background(), "rec_00aa11bb22cc33dd")
	if err != nil || rec == nil || rec.Narration != "pay the water bill" {
		t.Fatalf("stored %+v %v", rec, err)
	}
}

// #435: "turn on the computer" got enable_computer_control from the 9B on every run, so the model
// is sent it only on a turn whose words say "control"; the warmup leaves it out like those turns.
// The other switch tools stay.
func TestEnableComputerControlOfferedOnlyWhenAsked(t *testing.T) {
	ve, _ := recordingConv(t)
	if reqs := ve.eng.requests(); len(reqs) == 0 || has(toolNamesOf(reqs[0]), toolEnableComputerControl) {
		t.Fatal("the warmup offers enable_computer_control")
	}
	for _, tc := range []struct {
		said    string
		offered bool
	}{
		{"turn on the computer", false},
		{"find the right type of phone case and order it", false},
		{"enable computer control", true},
		{"turn Control on", true},
	} {
		ve.eng.say("Okay.")
		ve.turn("/api/v0/voice/command", "c1", tc.said, map[string]any{"turn_source": "wake"}).want(200)
		reqs := turnRequests(ve, tc.said)
		if len(reqs) == 0 {
			t.Fatalf("%q: no model request", tc.said)
		}
		names := toolNamesOf(reqs[0])
		if has(names, toolEnableComputerControl) != tc.offered || !has(names, toolDisableComputerControl) || !has(names, toolComputerControlStatus) {
			t.Fatalf("%q: tools %v, want enable_computer_control offered %v", tc.said, names, tc.offered)
		}
	}
}

// The switch tools are offered where the recording tools are: a computer in the household and
// a known speaker.
func TestComputerControlToolsOffered(t *testing.T) {
	for _, name := range []string{toolEnableComputerControl, toolDisableComputerControl, toolComputerControlStatus} {
		if !prompts.ServerToolAllowed(name, prompts.ToolGates{Computers: true, SpeakerKnown: true}) ||
			prompts.ServerToolAllowed(name, prompts.ToolGates{SpeakerKnown: true}) ||
			prompts.ServerToolAllowed(name, prompts.ToolGates{Computers: true}) {
			t.Fatalf("%s gated wrong", name)
		}
	}
	ve, _ := newSwitchEnv(t)
	ve.start("c0", computerTools)
	if got := offeredNames(ve.m.convs.get("c0")); got[toolEnableComputerControl] {
		t.Fatal("offered in a household without a computer")
	}
	ve.setComputerControl("off")
	ve.start("c1", computerTools)
	if got := offeredNames(ve.m.convs.get("c1")); !got[toolEnableComputerControl] || !got[toolDisableComputerControl] || !got[toolComputerControlStatus] {
		t.Fatalf("not offered on a computer: %v", got)
	}
}
