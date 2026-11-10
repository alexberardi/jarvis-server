package voice

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/builtins"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

func TestSimpleRoutineUsesOrdinaryVoiceTurn(t *testing.T) {
	srv := newServer()
	srv.text = "Radio time!"
	r := newRig(t, oneWake(), srv)
	r.p.Cfg.FollowUp.ListenSecs = 0
	r.p.Tools = &builtins.Routine{Pantry: func() map[string]map[string]any {
		return map[string]map[string]any{"radio_time": {"mode": "simple", "utterance": "Play the radio", "trigger_phrases": []any{"radio time"}, "steps": []any{}}}
	}}
	stop := r.run(t)
	turn := r.turn(t)
	_ = stop()
	if !slices.Contains(srv.callList(), "stream") {
		t.Fatalf("shortcut bypassed the model: %v", srv.callList())
	}
	srv.mu.Lock()
	b, err := json.Marshal(srv.streamIn)
	srv.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]any
	_ = json.Unmarshal(b, &req)
	if req["routine_name"] != "radio_time" || req["routine_utterance"] != "Play the radio" || req["voice_command"] != "Radio time!" || turn.Transcript != "Radio time!" {
		t.Fatalf("lost original or expanded request: %s, turn=%+v", b, turn)
	}
}

func TestSimpleRoutineContinuesAfterSDKFollowupFlag(t *testing.T) {
	srv := newServer()
	srv.text = "Radio time"
	srv.reply = toolCalls(call("a", "first", "{}"))
	srv.cont = steps(t, &Control{StopReason: "tool_calls", ToolCalls: []ToolCall{call("b", "second", "{}")}}, complete("Both actions finished."))
	r := newRig(t, oneWake(), srv)
	r.p.Cfg.FollowUp.ListenSecs = 0
	played := []string{}
	tools := funcTools{}
	for _, name := range []string{"first", "second"} {
		tools[name] = func(context.Context, map[string]any) map[string]any {
			return map[string]any{"success": true, node.SignalsKey: node.Signals{WaitForInput: true, OnResponseComplete: func() error { played = append(played, name); return nil }}}
		}
	}
	r.p.Tools = node.Multi{&builtins.Routine{Pantry: func() map[string]map[string]any {
		return map[string]map[string]any{"radio_time": {"mode": "simple", "utterance": "Run first and second", "trigger_phrases": []any{"radio time"}}}
	}}, tools}
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if len(tr.Tools) != 2 || !slices.Equal(played, []string{"first", "second"}) || tr.Message != "Both actions finished." {
		t.Fatalf("turn=%+v callbacks=%v", tr, played)
	}
}

func TestSimpleRoutineCanAskForClarification(t *testing.T) {
	srv := newServer()
	srv.text = "Radio time"
	srv.texts = []string{"The kitchen one."}
	srv.reply = func(context.Context) (Reply, error) {
		return Reply{Control: &Control{StopReason: "validation_required", Validation: &ValidationRequest{Question: "Which room?", ParameterName: "room"}}}, nil
	}
	srv.cont = steps(t, complete("Playing in the kitchen."))
	mic := oneWake()
	r := newRig(t, mic, srv)
	r.p.Cfg.FollowUp.ListenSecs = 0
	r.p.Tools = &builtins.Routine{Pantry: func() map[string]map[string]any {
		return map[string]map[string]any{"radio_time": {"mode": "simple", "utterance": "Play the radio", "trigger_phrases": []any{"radio time"}}}
	}}
	answer(r, mic, utterance)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if tr.Err != nil || !slices.Equal(tr.Validations, []Validation{{Question: "Which room?", Answer: "The kitchen one."}}) || tr.Message != "Playing in the kitchen." {
		t.Fatalf("clarification failed: %+v", tr)
	}
	if len(srv.results) != 1 {
		t.Fatalf("shortcut expanded again instead of answering: %+v", srv.results)
	}
}

func TestSimpleRoutinePausesBeforePendingVoiceAction(t *testing.T) {
	srv := newServer()
	srv.text = "Radio time"
	srv.reply = toolCalls(call("a", "pending", "{}"))
	srv.cont = steps(t, &Control{StopReason: "tool_calls", ToolCalls: []ToolCall{call("b", "next", "{}")}}, complete("Done."))
	r := newRig(t, oneWake(), srv)
	r.p.Cfg.FollowUp.ListenSecs = 0
	ran, callback := false, false
	r.p.Tools = node.Multi{&builtins.Routine{Pantry: func() map[string]map[string]any {
		return map[string]map[string]any{"radio_time": {"mode": "simple", "utterance": "Do both actions", "trigger_phrases": []any{"radio time"}}}
	}}, funcTools{
		"pending": func(context.Context, map[string]any) map[string]any {
			return map[string]any{"success": true, "status": "awaiting_confirmation", "question": "Confirm the action?", node.SignalsKey: node.Signals{WaitForInput: true, OnResponseComplete: func() error { callback = true; return nil }}}
		},
		"next": func(context.Context, map[string]any) map[string]any {
			ran = true
			return map[string]any{"success": true}
		},
	}}
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if ran || callback || tr.Message != "Confirm the action?" {
		t.Fatalf("ran=%v callback=%v turn=%+v", ran, callback, tr)
	}
}

func TestRoutinePlaybackStartsAfterSpokenReplyAndMusicRestore(t *testing.T) {
	t.Run("single playback", func(t *testing.T) { testRoutinePlayback(t, false) })
	t.Run("earlier playback fails", func(t *testing.T) { testRoutinePlayback(t, true) })
}

func testRoutinePlayback(t *testing.T, firstFails bool) {
	t.Helper()
	m := &fakeMusic{done: make(chan struct{})}
	srv := newServer()
	srv.text, srv.speakPCM = "radio time", pcmOf(300)
	mic := oneWake()
	r := newRig(t, mic, srv)
	r.p.Music = m
	r.p.Cfg.FollowUp.ListenSecs = 0
	steps := []any{map[string]any{"command": "music"}}
	if firstFails {
		steps = append([]any{map[string]any{"command": "unavailable_speaker"}}, steps...)
	}
	r.p.Tools = &builtins.Routine{CC: func() *node.CC { return nil },
		Pantry: func() map[string]map[string]any {
			return map[string]map[string]any{"radio_time": {
				"trigger_phrases": []any{"radio time"},
				"steps":           steps,
			}}
		},
		Tools: funcTools{
			"unavailable_speaker": func(context.Context, map[string]any) map[string]any {
				return map[string]any{"success": true, node.SignalsKey: node.Signals{
					OnResponseComplete: func() error { return errors.New("speaker unavailable") },
				}}
			},
			"music": func(context.Context, map[string]any) map[string]any {
				return map[string]any{"success": true, "message": "Playing the radio.", node.SignalsKey: node.Signals{
					OnResponseComplete: func() error {
						spoke := false
						for _, out := range r.spk.all() {
							if len(out.pcm)*2 == len(srv.speakPCM) && out.drained {
								spoke = true
							}
						}
						if !spoke {
							t.Error("playback started before the spoken reply finished")
						}
						m.add("play")
						return nil
					},
				}}
			},
		},
	}
	stop := r.run(t)
	turn := r.turn(t)
	_ = stop()
	if turn.Outcome != OutcomeSpoken || turn.Message != "Playing the radio." {
		t.Fatalf("turn = %+v", turn)
	}
	want := []string{"duck", "restore", "play", "self_playing:deferred_play"}
	if got := m.list(); !slices.Equal(got, want) {
		t.Fatalf("music steps = %v, want %v", got, want)
	}
}
