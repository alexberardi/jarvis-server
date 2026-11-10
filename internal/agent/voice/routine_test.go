package voice

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/builtins"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

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
