package cc

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
)

// #288: voice couldn't stop a recording. The agent now sends what Jarvis is doing with its
// computer on every turn (computer_activity); while it records, a stop phrase calls
// stop_recording (never dropped as not for Jarvis), and no reply says it stopped unless
// stop_recording ran.

// recordingConv opens c1 on the owner's computer, recording tools offered.
func recordingConv(t *testing.T) (*voiceEnv, *fakeRecAgent) {
	t.Helper()
	ve, agent, _ := newRecToolEnv(t, prompts.Qwen3_5_9B)
	ve.setComputerControl("off")
	ve.makeDesktop(7)
	ve.start("c1", computerTools)
	return ve, agent
}

var (
	recordingNow     = map[string]any{"kind": "recording", "task": "Watching what you do"}
	recordingStopped = map[string]any{"kind": "recording", "task": "Watching what you do", "stopped": true}
)

// requestText is every message of a model request, joined.
func requestText(req map[string]any) string {
	var b strings.Builder
	for _, m := range messagesOf(req) {
		s, _ := m["content"].(string)
		b.WriteString(s)
		b.WriteString("\n")
	}
	return b.String()
}

// The user's 20:08 turn: a new wake "Hey Jarvis, I'm done." while recording got not_for_me and
// the recording went on. The model's not-for-me is overruled: it is told to call stop_recording.
func TestStopPhraseWhileRecordingCallsStopRecording(t *testing.T) {
	for _, tc := range []struct {
		name, said string
		extra      map[string]any
	}{
		{"wake, stopped here", "Hey Jarvis, I'm done.", map[string]any{"turn_source": "wake", "computer_activity": recordingStopped}},
		{"follow-up, stopped here", "I'm done.", map[string]any{"turn_source": "follow_up", "follow_up_iteration": 1, "computer_activity": recordingStopped}},
		{"an agent that didn't stop it", "Stop recording.", map[string]any{"turn_source": "wake", "computer_activity": recordingNow}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ve, agent := recordingConv(t)
			ve.m.recordingNamed.Store(ve.node.id, namedRecording{id: "rec_00aa11bb22cc33dd", task: "test2"})
			ve.eng.say("<not_for_me/>")
			ve.eng.push(nativeCall("call_s1", toolStopRecording, `{}`))
			ve.eng.say("Saved test2.")
			b := ve.turn("/api/v0/voice/command", "c1", tc.said, tc.extra).want(200).json()
			if b["stop_reason"] == "not_for_me" || b["assistant_message"] != "Saved test2." {
				t.Fatalf("body %v", b)
			}
			reqs := turnRequests(ve, tc.said)
			if len(reqs) != 3 || !strings.Contains(requestText(reqs[0]), "a recording is running on this computer") {
				t.Fatalf("%d model calls; the prompt doesn't say a recording is running", len(reqs))
			}
			if !lastIsNagIn(reqs[1], nagStopRecording) {
				t.Fatalf("no stop_recording nudge after the not-for-me")
			}
			stopsHere := slices.Contains(agent.calls, ve.node.id+" "+computeruse.ToolRecordStop)
			if stopped := tc.extra["computer_activity"].(map[string]any)["stopped"] == true; stopped == stopsHere {
				t.Fatalf("agent calls %v (stopped here: %v)", agent.calls, stopped)
			}
		})
	}
}

// The model still won't call it: jarvisd stops the recording itself and says so.
func TestStopPhraseWhileRecordingFallsBackToTheTool(t *testing.T) {
	ve, agent := recordingConv(t)
	ve.m.recordingNamed.Store(ve.node.id, namedRecording{id: "rec_00aa11bb22cc33dd", task: "test2"})
	ve.m.recordingTasks.Store("rec_00aa11bb22cc33dd", "test2")
	ve.eng.say("Got it. I've recorded the macro called test2.")
	ve.eng.say("Got it. I've recorded the macro called test2.")
	ve.eng.say("Got it. I've recorded the macro called test2.")
	b := ve.turn("/api/v0/voice/command", "c1", "I'm done.", map[string]any{"computer_activity": recordingNow}).want(200).json()
	msg, _ := b["assistant_message"].(string)
	if !strings.Contains(msg, "saved") || !strings.Contains(msg, "test2") {
		t.Fatalf("body %v", b)
	}
	if !slices.Contains(agent.calls, ve.node.id+" "+computeruse.ToolRecordStop) {
		t.Fatalf("the recording wasn't stopped: %v", agent.calls)
	}
}

// Stopped here (the agent's own stop, which uploads the trace): stop_recording doesn't ask the
// agent again (it would say not_recording) nor for a name, and says it's saved under the name
// the upload gets (the start's task), never a name the model passes now.
func TestStopRecordingStoppedHere(t *testing.T) {
	for _, tc := range []struct {
		name, start, stop, says, stored string
	}{
		{"named at the start", `{"task": "test2"}`, "", `"test2"`, "test2"},
		{"named at the start and the stop", `{"task": "test2"}`, `{"name": "X"}`, `"test2"`, "test2"},
		{"named only at the stop", "", `{"name": "X"}`, "what you showed me", ""},
		{"unnamed", "", "", "what you showed me", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ve, agent, learned := newRecToolEnv(t)
			ve.setComputerControl("on")
			runTool(t, &startRecordingTool{m: ve.m}, tc.start, ve.recTurn(7))
			turn := ve.recTurn(7)
			turn.ComputerStopped = "recording"
			out := runTool(t, &stopRecordingTool{m: ve.m}, tc.stop, turn)
			if msg, _ := out["message"].(string); out["status"] != "saved" || !strings.Contains(msg, tc.says) || strings.Contains(msg, `"X"`) {
				t.Fatalf("stop: %v", out)
			}
			if len(agent.calls) != 1 {
				t.Fatalf("asked the agent again: %v", agent.calls)
			}
			// The agent's own upload, as handleUploadRecording stores it.
			rec, err := ve.m.storeAgentRecording(context.Background(), Recording{ID: "rec_00aa11bb22cc33dd", HouseholdID: voiceHH,
				OwnerID: 7, NodeID: ve.node.id, Mode: "browser", Steps: 2})
			if err != nil {
				t.Fatal(err)
			}
			if rec.Narration != tc.stored || len(*learned) != 1 {
				t.Fatalf("stored as %q, the message says %s", rec.Narration, tc.says)
			}
		})
	}
}

// A reply that says the recording stopped while it runs and stop_recording didn't run is
// nudged once, then replaced: it never reaches the user.
func TestReplyNeverClaimsAStopThatDidNotRun(t *testing.T) {
	ve, agent := recordingConv(t)
	ve.eng.say("I've stopped the recording and saved it.")
	ve.eng.say("Okay, the recording is saved.")
	b := ve.turn("/api/v0/voice/command", "c1", "How's it going?", map[string]any{"computer_activity": recordingNow}).want(200).json()
	msg, _ := b["assistant_message"].(string)
	if strings.Contains(msg, "saved") || !strings.Contains(msg, "still recording") {
		t.Fatalf("body %v", b)
	}
	if !lastIsNag(ve, nagNotStopped) {
		t.Fatalf("no nudge before the second answer")
	}
	if len(agent.calls) != 0 {
		t.Fatalf("agent calls %v", agent.calls)
	}
}

// During a recording, another tool's answer that says "stopped" or "saved" ("I stopped the
// timer", "your note is saved") is not about the recording: it reaches the user, unnudged.
func TestOtherStopsPassDuringARecording(t *testing.T) {
	for _, reply := range []string{"Okay, I stopped the timer.", "Your note is saved.", "Your settings are saved.",
		"The game was saved by the goalie in overtime.", "I stopped the music."} {
		ve, agent := recordingConv(t)
		ve.eng.say(reply)
		b := ve.turn("/api/v0/voice/command", "c1", "What happened?", map[string]any{"computer_activity": recordingNow}).want(200).json()
		if b["assistant_message"] != reply {
			t.Fatalf("%q became %v", reply, b)
		}
		if reqs := turnRequests(ve, "What happened?"); len(reqs) != 1 {
			t.Fatalf("%q: %d model calls, want 1 (no %s nudge)", reply, len(reqs), nagNotStopped)
		}
		if len(agent.calls) != 0 {
			t.Fatalf("agent calls %v", agent.calls)
		}
	}
}

func TestClaimsStopRE(t *testing.T) {
	for s, want := range map[string]bool{
		"I've stopped the recording and saved it.": true, "Okay, the recording is saved.": true,
		"Got it. I've recorded the macro called test2.": true, "I stopped recording.": true,
		"Saved the macro.": true, "The recording has been stopped.": true, "I've finished recording.": true,
		"Okay, I stopped the timer.": false, "Your note is saved.": false, "Stopped.": false, "Saved!": false,
		"I'm still recording.": false, "The recording keeps going.": false,
	} {
		if got := claimsStopRE.MatchString(s); got != want {
			t.Errorf("claimsStopRE(%q) = %v, want %v", s, got, want)
		}
	}
}

// Not recording, "I'm done with dinner" (or "I'm done") stays the model's not-for-me: nothing
// is nudged or stopped.
func TestStopPhraseWhenIdleStaysNotForMe(t *testing.T) {
	for _, said := range []string{"I'm done with dinner", "I'm done."} {
		ve, agent := recordingConv(t)
		ve.eng.say("<not_for_me/>")
		ve.eng.say("<not_for_me/>")
		b := ve.turn("/api/v0/voice/command", "c1", said, map[string]any{"turn_source": "wake"}).want(200).json()
		if b["stop_reason"] != "not_for_me" || len(agent.calls) != 0 {
			t.Fatalf("%q: body %v, agent calls %v", said, b, agent.calls)
		}
		if reqs := turnRequests(ve, said); len(reqs) == 0 || strings.Contains(requestText(reqs[0]), "a recording is running") {
			t.Fatalf("%q: the prompt says a recording is running", said)
		}
	}
	// Recording, but a near-miss: the model decides as ever.
	ve, agent := recordingConv(t)
	ve.eng.say("<not_for_me/>")
	ve.eng.say("<not_for_me/>")
	b := ve.turn("/api/v0/voice/command", "c1", "I'm done with dinner", map[string]any{"turn_source": "wake", "computer_activity": recordingNow}).want(200).json()
	if b["stop_reason"] != "not_for_me" || len(agent.calls) != 0 {
		t.Fatalf("near-miss while recording: body %v, agent calls %v", b, agent.calls)
	}
}

// Controlling the computer, "stop" the agent already stopped: a not-for-me becomes a short
// "stopped" so the user hears it took.
func TestStopPhraseWhileControllingIsAnswered(t *testing.T) {
	ve, _ := recordingConv(t)
	ve.eng.say("<not_for_me/>")
	ve.eng.say("<not_for_me/>")
	b := ve.turn("/api/v0/voice/command", "c1", "Stop.", map[string]any{"computer_activity": map[string]any{
		"kind": "controlling", "task": "Ordering the blue mug", "stopped": true}}).want(200).json()
	if b["stop_reason"] == "not_for_me" || !strings.Contains(b["assistant_message"].(string), "stopped") {
		t.Fatalf("body %v", b)
	}
}

// Controlling, "That's all." is no stop (a recording's word, not a task's): the model answers.
func TestRecordingWordsDontStopControl(t *testing.T) {
	a := &computerActivity{Kind: "controlling", Task: "Ordering the blue mug"}
	if a.stopAsked("That's all.") || a.stopAsked("I'm done.") || !a.stopAsked("Stop.") {
		t.Fatalf("controlling: stopAsked is wrong")
	}
	if a := (&computerActivity{Kind: "recording"}); !a.stopAsked("That's all.") || !a.stopAsked("I'm done.") {
		t.Fatalf("recording: stopAsked is wrong")
	}
}

func TestComputerActivityParse(t *testing.T) {
	ve, _ := recordingConv(t)
	ve.turn("/api/v0/voice/command", "c1", "hi", map[string]any{"computer_activity": "recording"}).want(400)
}

// turnRequests are the model requests of the turn that said said (the warm-up's left out).
func turnRequests(ve *voiceEnv, said string) []map[string]any {
	reqs := ve.eng.requests()
	for i, r := range reqs {
		if strings.Contains(requestText(r), said) {
			return reqs[i:]
		}
	}
	return nil
}

// lastIsNagIn reports whether a model request ends with a system nudge tagged tag.
func lastIsNagIn(req map[string]any, tag string) bool {
	msgs := messagesOf(req)
	last := msgs[len(msgs)-1]
	s, _ := last["content"].(string)
	return last["role"] == "system" && strings.HasPrefix(s, tag)
}

// The follow-up gate drops a different, quiet voice before the model. While recording, "I'm
// done." skips it (a weaker voice is still the user ending their recording); any other
// follow-up with the same evidence is dropped as ever, and so is a follow-up "Cancel that."
// while Jarvis controls the computer (a follow-up never stops a control task).
func TestStopPhraseWhileRecordingSkipsTheFollowUpGate(t *testing.T) {
	controlling := map[string]any{"kind": "controlling", "task": "Ordering the blue mug"}
	for _, tc := range []struct {
		name, said string
		activity   map[string]any
		stop       bool
	}{
		{"recording, I'm done", "I'm done.", recordingNow, true},
		{"recording, other", "and the score is now three to one", recordingNow, false},
		{"controlling, cancel that", "Cancel that.", controlling, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ve, agent := recordingConv(t)
			ve.m.recordingNamed.Store(ve.node.id, namedRecording{id: "rec_00aa11bb22cc33dd", task: "test2"})
			ve.stt.voices = []stt.Voice{voiceAt(1, 0)}
			ve.transcribe("c1", true)
			ve.eng.say("I'm watching. Say I'm done when you're finished.")
			ve.turn("/api/v0/voice/command", "c1", "record a macro called test2", map[string]any{"turn_source": "wake",
				"wake_confidence": 0.9, "computer_activity": recordingNow}).want(200)
			calls := len(ve.eng.requests())

			ve.stt.voices = []stt.Voice{voiceAt(0, 1)} // a different voice, and quiet: the gate rejects
			ve.transcribe("c1", true)
			ve.eng.push(nativeCall("call_s1", toolStopRecording, `{}`))
			ve.eng.say("Saved test2.")
			b := followUp(ve, tc.said, map[string]any{"level_vs_wake_db": -14.5, "computer_activity": tc.activity})
			stopped := slices.Contains(agent.calls, ve.node.id+" "+computeruse.ToolRecordStop)
			if !tc.stop {
				if b["stop_reason"] != "not_for_me" || stopped || len(ve.eng.requests()) != calls {
					t.Fatalf("an ordinary follow-up passed the gate: body %v, agent calls %v", b, agent.calls)
				}
				return
			}
			if b["stop_reason"] == "not_for_me" || !stopped {
				t.Fatalf("the gate dropped the stop: body %v, agent calls %v", b, agent.calls)
			}
		})
	}
}

// #435: with nothing recording, "record a macro called test2" and "stop recording" got "I'm
// recording a macro called test2." / "Alright, recording's stopped." and no call. Such a reply
// is nudged once to make the call, and the retry requires one. A request that isn't for a
// recording ("record a reminder") is not.
func TestRecordingRequestAnsweredInProseIsNudged(t *testing.T) {
	for _, tc := range []struct {
		said, claim, tool string
		nudged            bool
	}{
		{"record a macro called test2", "Alright, I'm recording a macro called test2.", toolStartRecording, true},
		{"learn how I do this", "I'm ready to learn how you do things, go ahead and show me.", toolStartRecording, true},
		{"stop recording", "Alright, recording's stopped. <exchange_complete/>", toolStopRecording, true},
		{"record a reminder to buy milk", "When should I remind you?", "", false},
	} {
		t.Run(tc.said, func(t *testing.T) {
			ve, _ := recordingConv(t)
			ve.eng.say(tc.claim)
			ve.eng.say(tc.claim) // past the must-call retry, when the force guard asks first
			if tc.nudged {
				ve.eng.push(nativeCall("call_r1", tc.tool, `{}`))
				ve.eng.say("Done.")
			}
			ve.turn("/api/v0/voice/command", "c1", tc.said, map[string]any{"turn_source": "wake"}).want(200)
			nudge := -1
			for i, r := range turnRequests(ve, tc.said) {
				if lastIsNagIn(r, nagRecordingNotRun) {
					nudge = i
					if r["tool_choice"] != "required" {
						t.Errorf("the retry after the nudge: tool_choice %v", r["tool_choice"])
					}
				}
			}
			if (nudge >= 0) != tc.nudged {
				t.Fatalf("nudged at request %d, want nudged %v", nudge, tc.nudged)
			}
		})
	}
}

// #435: "I'm done." in the follow-up ran stop_recording, then the model called not_for_me and
// the turn went silent. A turn a tool already ran in was for Jarvis: the not-for-me gets one
// nudge to answer.
func TestNotForMeAfterAToolRanIsNudged(t *testing.T) {
	ve, _ := recordingConv(t)
	ve.eng.push(nativeCall("call_s1", toolStopRecording, `{}`))
	ve.eng.push(nativeCall("call_n1", "not_for_me", `{}`))
	ve.eng.say("What should I call this macro?")
	b := ve.turn("/api/v0/voice/command", "c1", "I'm done.", map[string]any{"turn_source": "follow_up",
		"follow_up_iteration": 1, "computer_activity": recordingNow}).want(200).json()
	if b["stop_reason"] == "not_for_me" || b["assistant_message"] != "What should I call this macro?" {
		t.Fatalf("body %v", b)
	}
	reqs := turnRequests(ve, "I'm done.")
	if len(reqs) != 3 || !lastIsNagIn(reqs[2], nagAnswerAfterTool) {
		t.Fatalf("%d model calls; no answer nudge", len(reqs))
	}
}

// #435: "turn off computer control" got "Got it, computer control is off now." and no call. A
// "computer control" request answered in prose is nudged once to call a switch tool, and the
// retry requires one. A turn that ran one, or didn't ask, is not.
func TestSwitchRequestAnsweredInProseIsNudged(t *testing.T) {
	for _, tc := range []struct {
		said, ran, tool string
		nudged          bool
	}{
		{"turn off computer control", "", toolDisableComputerControl, true},
		{"Hey Jarvis, enable computer control", "", toolEnableComputerControl, true},
		{"is computer control on?", toolComputerControlStatus, "", false},
		{"what's the weather like", "", "", false},
	} {
		t.Run(tc.said, func(t *testing.T) {
			ve, _ := recordingConv(t)
			if tc.ran != "" {
				ve.eng.push(nativeCall("call_s0", tc.ran, `{}`))
			}
			claim := "Got it, computer control is off now."
			ve.eng.say(claim)
			ve.eng.say(claim) // past the must-call retry, when the force guard asks first
			if tc.nudged {
				ve.eng.push(nativeCall("call_s1", tc.tool, `{}`))
				ve.eng.say("Done.")
			}
			ve.turn("/api/v0/voice/command", "c1", tc.said, map[string]any{"turn_source": "wake"}).want(200)
			nudge := -1
			for i, r := range turnRequests(ve, tc.said) {
				if lastIsNagIn(r, nagSwitchNotRun) {
					nudge = i
					if r["tool_choice"] != "required" {
						t.Errorf("the retry after the nudge: tool_choice %v", r["tool_choice"])
					}
				}
			}
			if (nudge >= 0) != tc.nudged {
				t.Fatalf("nudged at request %d, want nudged %v", nudge, tc.nudged)
			}
		})
	}
}
