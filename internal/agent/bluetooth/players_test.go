package bluetooth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// players records the node's player restarts, with the default sink at the time of each.
type players struct {
	n     *Node
	mu    sync.Mutex
	pulse *fakePulse
	at    []string // the default sink at each restart
	err   error
}

func (p *players) restart(context.Context) error {
	p.pulse.mu.Lock()
	def := p.pulse.def
	p.pulse.mu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.at = append(p.at, def)
	return p.err
}

// restarts are the restarts so far, once the one running (in the background) is done.
func (p *players) restarts() []string {
	p.n.restarts.Wait()
	return p.seen()
}

// seen are the restarts so far, without waiting (for a test whose events route concurrently).
func (p *players) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.at...)
}

func withPlayers(n *Node, pulse *fakePulse) *players {
	p := &players{n: n, pulse: pulse}
	n.RestartPlayers = p.restart
	return p
}

// #478: shairport-sync's Pulse stream died when the Px7 S2 took the output, and the player stayed
// silent until a manual restart. Every switch of the default sink to or from a speaker restarts
// the players, after the switch.
func TestARouteChangeRestartsTheMusicPlayers(t *testing.T) {
	for name, leave := range map[string]map[string]any{
		"disconnect": nil,
		"forget":     {"forget": true},
	} {
		t.Run(name, func(t *testing.T) {
			n, bus, pulse, api := rig(t)
			p := withPlayers(n, pulse)
			bus.inRange[speaker] = speakerDev()
			send(t, n, api, TopicPair, map[string]any{"request_id": "p1", "mac_address": speaker, "role": "source"})
			if got := p.restarts(); len(got) != 1 || got[0] != speakerSink {
				t.Fatalf("after the pair, restarts at %v; want one, on the speaker's sink", got)
			}
			topic, payload := TopicDisconnect, map[string]any{"mac_address": speaker}
			if leave != nil {
				topic, payload["forget"] = TopicRelease, leave["forget"]
			}
			bus.onDisconnect = nil
			send(t, n, api, topic, payload)
			if got := p.restarts(); len(got) != 2 || got[1] != "jarvis_output" {
				t.Fatalf("after the %s, restarts at %v; want a second, on the node's sink", name, got)
			}
			// Re-pair: the output moves again, and so do the players.
			bus.onDisconnect = func(mac string) { pulse.drop(mac) }
			send(t, n, api, TopicPair, map[string]any{"request_id": "p2", "mac_address": speaker, "role": "source"})
			if got := p.restarts(); len(got) != 3 || got[2] != speakerSink {
				t.Fatalf("after the re-pair, restarts at %v", got)
			}
		})
	}
}

// A restart that fails is logged; the pair still succeeds.
func TestAFailedPlayerRestartIsNotFatal(t *testing.T) {
	n, bus, pulse, api := rig(t)
	p := withPlayers(n, pulse)
	p.err = errors.New("sudo: a password is required")
	bus.inRange[speaker] = speakerDev()
	send(t, n, api, TopicPair, map[string]any{"request_id": "p1", "mac_address": speaker, "role": "source"})
	if api.pairs["p1"]["success"] != true || pulse.def != speakerSink {
		t.Fatalf("pair %v, default %q", api.pairs["p1"], pulse.def)
	}
	if len(p.restarts()) != 1 {
		t.Fatalf("restarts %v", p.restarts())
	}
}

// No switch, no restart: the sink already the default (a reconnect loop finding it routed), a
// default sink Pulse refused, a phone (its loopback leaves the default alone).
func TestNoRestartWithoutASwitch(t *testing.T) {
	t.Run("already routed", func(t *testing.T) {
		n, _, pulse, _ := rig(t)
		p := withPlayers(n, pulse)
		pulse.add(speakerSink, "")
		pulse.def = speakerSink
		n.routeAudio(context.Background(), speaker, "source")
		if got := p.restarts(); len(got) != 0 {
			t.Fatalf("restarts %v", got)
		}
	})
	t.Run("refused", func(t *testing.T) {
		n, _, pulse, _ := rig(t)
		p := withPlayers(n, pulse)
		pulse.add(speakerSink, "")
		n.Pulse = refusing{pulse}
		n.routeAudio(context.Background(), speaker, "source")
		if got := p.restarts(); len(got) != 0 {
			t.Fatalf("restarts %v", got)
		}
	})
	t.Run("phone", func(t *testing.T) {
		n, bus, pulse, api := rig(t)
		p := withPlayers(n, pulse)
		bus.inRange[phone] = phoneDev()
		send(t, n, api, TopicPair, map[string]any{"request_id": "p1", "mac_address": phone, "role": "sink"})
		if got := p.restarts(); len(got) != 0 {
			t.Fatalf("restarts %v", got)
		}
	})
}

type refusing struct{ *fakePulse }

func (refusing) SetDefaultSink(context.Context, string) error { return errors.New("pactl: refused") }

// Headphones that power off: Pulse moves the output back by itself, and the players' streams with
// it. The node didn't switch the sink, but it had routed the output there, so it restarts them.
func TestHeadphonesThatGoAwayRestartThePlayers(t *testing.T) {
	n, bus, pulse, _ := rig(t)
	p := withPlayers(n, pulse)
	s := speakerDev()
	s.Paired = true
	bus.devices[speaker] = &s
	_ = n.Store.Paired(speaker, "JBL", "source", time.Now())
	n.FirstReconnect = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n.Start(ctx)
	link := func(up bool) {
		bus.mu.Lock()
		bus.devices[speaker].Connected = up
		bus.mu.Unlock()
		if up {
			bus.onConnect(speaker)
		} else {
			bus.onDisconnect(speaker)
		}
		bus.conns <- Connection{MAC: speaker, Connected: up}
	}
	link(true)
	waitFor(t, func() bool { return len(p.seen()) == 1 })
	link(false)
	waitFor(t, func() bool { return len(p.seen()) == 2 })
	if got := p.seen(); got[0] != speakerSink || got[1] != "jarvis_output" {
		t.Fatalf("restarts at %v", got)
	}
	// Another drop of a device that no longer has the output: nothing to restart.
	bus.conns <- Connection{MAC: speaker, Connected: false}
	time.Sleep(50 * time.Millisecond)
	if got := p.seen(); len(got) != 2 {
		t.Fatalf("restarts %v", got)
	}
}

// A pair's reply doesn't wait on the restarts (up to three units, each with its own timeout): they
// run in the background, after the switch.
func TestAPairDoesNotWaitForTheRestart(t *testing.T) {
	n, bus, pulse, api := rig(t)
	p := withPlayers(n, pulse)
	release := make(chan struct{})
	n.RestartPlayers = func(ctx context.Context) error {
		<-release
		return p.restart(ctx)
	}
	bus.inRange[speaker] = speakerDev()
	done := make(chan struct{})
	go func() {
		defer close(done)
		send(t, n, api, TopicPair, map[string]any{"request_id": "p1", "mac_address": speaker, "role": "source"})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the pair waited for the player restart")
	}
	if api.pairs["p1"]["success"] != true {
		t.Fatalf("pair %v", api.pairs["p1"])
	}
	close(release)
	if got := p.restarts(); len(got) != 1 || got[0] != speakerSink {
		t.Fatalf("restarts at %v", got)
	}
}

// Switches while a restart runs restart the players once more after it, on the last sink: not
// once per switch, and never none for the last.
func TestSwitchesDuringARestartRestartOnceMore(t *testing.T) {
	n, _, pulse, _ := rig(t)
	p := withPlayers(n, pulse)
	started, release := make(chan struct{}, 4), make(chan struct{})
	n.RestartPlayers = func(ctx context.Context) error {
		started <- struct{}{}
		<-release
		return p.restart(ctx)
	}
	pulse.add(speakerSink, "")
	ctx := context.Background()
	switched := make(chan struct{})
	go func() {
		defer close(switched)
		n.routeOutput(ctx, speaker)
		<-started
		n.unrouteOutput(ctx, speaker)
		n.routeOutput(ctx, speaker)
	}()
	select {
	case <-switched:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("a switch waited for the player restart")
	}
	close(release)
	if got := p.restarts(); len(got) != 2 || got[1] != speakerSink {
		t.Fatalf("restarts at %v; want two, the last on the speaker's sink", got)
	}
}
