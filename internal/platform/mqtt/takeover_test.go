package mqtt

import (
	"sync"
	"testing"

	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

// stallHook holds the first client's own teardown (its UnsubscribeClient, which mochi runs
// after deciding the client was not taken over) until release is closed. That is exactly the
// window in which mochi's delete-by-ID used to remove a reconnected client.
type stallHook struct {
	mochi.HookBase
	mu       sync.Mutex
	first    *mochi.Client
	stalled  chan struct{} // closed once the first client's teardown is held
	release  chan struct{}
	relOnce  sync.Once
	stallOne sync.Once
}

func (h *stallHook) ID() string { return "test-stall" }
func (h *stallHook) Provides(b byte) bool {
	return b == mochi.OnSessionEstablished || b == mochi.OnUnsubscribed
}

func (h *stallHook) OnSessionEstablished(cl *mochi.Client, _ packets.Packet) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.first == nil {
		h.first = cl
		return
	}
	// A reconnect registered while its predecessor was still tearing down: let the
	// predecessor finish, so a broker without the fix shows the bug instead of hanging.
	h.unblock()
}

func (h *stallHook) OnUnsubscribed(cl *mochi.Client, pk packets.Packet) {
	h.mu.Lock()
	first := h.first
	h.mu.Unlock()
	// Only the first client's own goroutine unsubscribes with filters; a takeover's call
	// on its behalf has none.
	if cl != first || len(pk.Filters) == 0 {
		return
	}
	h.stallOne.Do(func() {
		close(h.stalled)
		<-h.release
	})
}

func (h *stallHook) unblock() { h.relOnce.Do(func() { close(h.release) }) }

// A clean-session client that reconnects while its old connection is still being torn down
// must end up registered and subscribed, not deleted by the old connection's cleanup.
func TestReconnectDuringTeardown(t *testing.T) {
	for _, v := range []byte{4, 5} {
		t.Run(map[byte]string{4: "v3.1.1", 5: "v5"}[v], func(t *testing.T) {
			b := startBroker(t, false)
			h := &stallHook{stalled: make(chan struct{}), release: make(chan struct{})}
			if err := b.srv.AddHook(h, nil); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(h.unblock)
			b.testTakeoverWait = h.unblock

			o := node("n1")
			o.version = v
			c1, _ := dial(t, b, o)
			c1.subscribe("jarvis/nodes/n1/#")
			h.mu.Lock()
			oldConn := h.first.Net.Conn
			h.mu.Unlock()
			c1.disconnect()
			<-h.stalled

			c2, ack := dial(t, b, o)
			if ack.ReasonCode != 0 {
				t.Fatalf("reconnect refused: %#x", ack.ReasonCode)
			}
			// Let the old connection's handler finish before checking the new one survived it.
			waitFor(t, func() bool {
				b.mu.Lock()
				defer b.mu.Unlock()
				_, serving := b.serving[oldConn]
				return !serving
			})
			c2.subscribe("jarvis/nodes/n1/#")
			if err := b.Publish("jarvis/nodes/n1/commands", []byte(`[]`), 1, false); err != nil {
				t.Fatal(err)
			}
			if pk := c2.expect(packets.Publish); pk.TopicName != "jarvis/nodes/n1/commands" {
				t.Fatalf("got %s", pk.TopicName)
			}
		})
	}
}
