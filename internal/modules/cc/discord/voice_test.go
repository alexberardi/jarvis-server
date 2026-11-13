package discord_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/discord"
)

type fakeDec struct{}

func (fakeDec) Decode([]byte) ([]int16, error) { return make([]int16, 960*2), nil } // 20 ms
func (fakeDec) Close()                         {}

type fakePlayer struct {
	mu      sync.Mutex
	played  [][][]byte
	stops   int
	playing bool
}

func (p *fakePlayer) Play(pk [][]byte) {
	p.mu.Lock()
	p.played = append(p.played, pk)
	p.playing = true
	p.mu.Unlock()
}
func (p *fakePlayer) Stop()         { p.mu.Lock(); p.stops++; p.playing = false; p.mu.Unlock() }
func (p *fakePlayer) Playing() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.playing }
func (p *fakePlayer) count() int    { p.mu.Lock(); defer p.mu.Unlock(); return len(p.played) }

type voiceEnv struct {
	v      *discord.Voice
	player *fakePlayer
	now    time.Time
	heard  []string
	chats  []string
	said   string
	mu     sync.Mutex
}

func newVoiceEnv(t *testing.T) *voiceEnv {
	t.Helper()
	e := &voiceEnv{player: &fakePlayer{}, now: time.Unix(1_800_000_000, 0)}
	st := newStore(t)
	if err := st.Put(context.Background(), discord.Link{HouseholdID: "hh", DiscordUserID: "alex", UserID: 7, NodeID: "n1"}); err != nil {
		t.Fatal(err)
	}
	e.v = &discord.Voice{
		HouseholdID: "hh", Store: st, Player: e.player,
		Now:        func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now },
		NewDecoder: func() (discord.PCMDecoder, error) { return fakeDec{}, nil },
		WAV:        func(pcm []int16) ([]byte, error) { return make([]byte, len(pcm)*2), nil },
		Hear: func(context.Context, []byte) (string, error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.heard = append(e.heard, "x")
			return "what time is it", nil
		},
		Chat: func(_ context.Context, uid int64, _, node, conv, text string, _ []discord.Image) (string, string, error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.chats = append(e.chats, text+"|"+conv)
			return "It is noon.", "conv-v", nil
		},
		Synth: func(_ context.Context, text string) ([][]byte, error) {
			e.mu.Lock()
			e.said = text
			e.mu.Unlock()
			return [][]byte{{1}, {2}}, nil
		},
	}
	return e
}

func (e *voiceEnv) advance(d time.Duration) { e.mu.Lock(); e.now = e.now.Add(d); e.mu.Unlock() }

// speak feeds n 20 ms frames, moving the clock as real audio would.
func (e *voiceEnv) speak(user string, n int) {
	for i := 0; i < n; i++ {
		e.v.Frame(user, []byte{1})
		e.advance(20 * time.Millisecond)
	}
}

func (e *voiceEnv) waitPlayed(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < 200 && e.player.count() < n; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if e.player.count() != n {
		t.Fatalf("played %d utterances, want %d", e.player.count(), n)
	}
}

func TestVoiceAnswersALinkedSpeakerAfterTheyPause(t *testing.T) {
	e := newVoiceEnv(t)
	e.speak("alex", 50) // a second of speech
	e.v.Tick()
	time.Sleep(30 * time.Millisecond)
	if e.player.count() != 0 {
		t.Fatal("answered while they were still talking")
	}
	e.advance(900 * time.Millisecond) // the pause
	e.v.Tick()
	e.waitPlayed(t, 1)
	if len(e.chats) != 1 || e.chats[0] != "what time is it|" || e.said != "It is noon." {
		t.Fatalf("chats %v said %q", e.chats, e.said)
	}
	// The conversation carries on across utterances.
	e.speak("alex", 50)
	e.advance(900 * time.Millisecond)
	e.v.Tick()
	e.waitPlayed(t, 2)
	if e.chats[1] != "what time is it|conv-v" {
		t.Fatalf("second utterance conversation: %v", e.chats)
	}
}

func TestVoiceIgnoresStrangersAndCoughs(t *testing.T) {
	e := newVoiceEnv(t)
	e.speak("stranger", 100)
	e.advance(time.Second)
	e.v.Tick()
	time.Sleep(30 * time.Millisecond)
	if len(e.heard) != 0 || e.player.count() != 0 {
		t.Fatal("a stranger was heard")
	}
	e.speak("alex", 5) // 100 ms
	e.advance(time.Second)
	e.v.Tick()
	time.Sleep(30 * time.Millisecond)
	if len(e.heard) != 0 {
		t.Fatal("a cough went to speech to text")
	}
}

func TestVoiceSpeakingOverItStopsIt(t *testing.T) {
	e := newVoiceEnv(t)
	e.speak("alex", 50)
	e.advance(900 * time.Millisecond)
	e.v.Tick()
	e.waitPlayed(t, 1)
	if !e.player.Playing() {
		t.Fatal("not playing")
	}
	e.speak("alex", 1) // they start talking again
	if e.player.Playing() || e.player.stops == 0 {
		t.Fatal("playback was not stopped when the person spoke")
	}
}

func TestVoiceCloseDropsEverything(t *testing.T) {
	e := newVoiceEnv(t)
	e.speak("alex", 50)
	e.v.Close()
	e.advance(time.Second)
	e.v.Tick()
	e.v.Frame("alex", []byte{1})
	time.Sleep(30 * time.Millisecond)
	if len(e.heard) != 0 || e.player.stops == 0 {
		t.Fatalf("after close: heard %d, stops %d", len(e.heard), e.player.stops)
	}
}

func TestQueuePlayer(t *testing.T) {
	p := &discord.QueuePlayer{}
	if f, err := p.ProvideOpusFrame(); f != nil || err != nil || p.Playing() {
		t.Fatal("idle player should give nothing")
	}
	p.Play([][]byte{{1}, {2}})
	if !p.Playing() {
		t.Fatal("not playing")
	}
	if f, _ := p.ProvideOpusFrame(); f[0] != 1 {
		t.Fatal("order")
	}
	p.Stop()
	if f, _ := p.ProvideOpusFrame(); f != nil || p.Playing() {
		t.Fatal("stop did not clear the queue")
	}
	_ = strings.TrimSpace
}
