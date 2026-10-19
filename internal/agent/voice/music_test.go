package voice

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// fakeMusic records the pipeline's music steps in order.
type fakeMusic struct {
	mu    sync.Mutex
	steps []string
	done  chan struct{} // closed at SetSelfPlaying
}

func (m *fakeMusic) add(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.steps = append(m.steps, s)
}

func (m *fakeMusic) list() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.steps)
}

func (m *fakeMusic) Duck(context.Context) {
	time.Sleep(20 * time.Millisecond) // a slow pactl must not let the restore overtake it
	m.add("duck")
}
func (m *fakeMusic) Restore(context.Context) { m.add("restore") }
func (m *fakeMusic) SetSelfPlaying(_ context.Context, on bool, trigger string) {
	m.add("self_playing:" + trigger)
	close(m.done)
}

// N:core/wake_loop.py: duck at the wake, again before each follow-up listen, restore after the
// turn, then the deferred play, then "the node's own music is playing".
func TestMusicDuckedForTheTurnAndRestoredBeforeTheDeferredPlay(t *testing.T) {
	m := &fakeMusic{done: make(chan struct{})}
	tools := funcTools{"play_music": func(context.Context, map[string]any) map[string]any {
		return map[string]any{"success": true, "message": "Playing jazz.", node.SignalsKey: node.Signals{
			OnResponseComplete: func() error { m.add("play"); return nil },
		}}
	}}
	srv := newServer()
	srv.reply = toolCalls(call("c1", "play_music", `{"genre":"jazz"}`))
	srv.cont = steps(t, complete("Playing jazz."))
	srv.speakPCM = pcmOf(10)
	mic := oneWake()
	r := newRig(t, mic, srv)
	r.p.Tools = tools
	r.p.Music = m
	answer(r, mic) // one follow-up window, nobody answers
	stop := r.run(t)
	r.turn(t)
	_ = stop()
	select {
	case <-m.done:
	case <-time.After(5 * time.Second):
		t.Fatalf("deferred play never reported; steps %v", m.list())
	}
	want := []string{"duck", "duck", "restore", "play", "self_playing:deferred_play"}
	if got := m.list(); !slices.Equal(got, want) {
		t.Fatalf("music steps %v, want %v", got, want)
	}
}

// A turn with no deferred play restores the music and claims nothing about it.
func TestMusicRestoredAfterAPlainTurn(t *testing.T) {
	m := &fakeMusic{done: make(chan struct{})}
	srv := newServer()
	mic := oneWake()
	r := newRig(t, mic, srv)
	r.p.Music = m
	r.p.Cfg.FollowUp.ListenSecs = 0
	stop := r.run(t)
	r.turn(t)
	_ = stop() // Run waits for the background steps
	if got, want := m.list(), []string{"duck", "restore"}; !slices.Equal(got, want) {
		t.Fatalf("music steps %v, want %v", got, want)
	}
}
