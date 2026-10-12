package mqtt

import (
	"net"
	"time"

	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

// Why the broker serialises a reconnect behind the old connection's teardown (mochi v2.7.9):
//
// When a client whose session expires on disconnect (MQTT 3.1.1 clean session, or MQTT 5
// with a zero session expiry) goes away, mochi's connection goroutine checks that the client
// was not taken over and then, without a lock, unsubscribes it and deletes it from the client
// registry *by client ID*. A new connection with the same ID can take the session over in
// between: it registers itself, and the old goroutine's delete then removes the new client
// (deliveries are routed by looking subscribers up by ID, so they are silently dropped) or
// its fresh subscriptions. The node looks connected but never hears from CC again until it
// reconnects. It happens whenever a node reconnects while its old connection is still being
// torn down, e.g. after a Wi-Fi blip, when the broker only notices the old connection died
// because the new one took it over.
//
// takeoverHook closes that window: once a new connection has authenticated, and before mochi
// looks for a session to inherit, it disconnects any previous connection with the same ID
// whose session would expire and waits for that connection's goroutine to finish. Mochi then
// finds no session to inherit (correct: it was going to be discarded) and the new client
// registers after the old one is gone. Persistent sessions are left to mochi, whose
// takeover of them does not race.

// takeoverWait bounds how long a reconnect waits for its predecessor's teardown, which only
// runs hooks and in-memory cleanup once its connection is closed.
const takeoverWait = 5 * time.Second

type takeoverHook struct {
	mochi.HookBase
	b *Broker
}

func (h *takeoverHook) ID() string { return "jarvis-takeover" }

func (h *takeoverHook) Provides(b byte) bool { return b == mochi.OnSessionEstablish }

func (h *takeoverHook) OnSessionEstablish(cl *mochi.Client, _ packets.Packet) {
	h.b.awaitPredecessor(cl)
}

// sessionExpires reports whether mochi discards cl's session when it disconnects, using
// mochi's own rule (attachClient).
func sessionExpires(cl *mochi.Client) bool {
	if cl.Properties.ProtocolVersion == 5 {
		return cl.Properties.Props.SessionExpiryInterval == 0
	}
	return cl.Properties.Clean
}

// awaitPredecessor disconnects the registered client with cl's ID, if its session expires,
// and waits (bounded) for its connection handler to return.
func (b *Broker) awaitPredecessor(cl *mochi.Client) {
	old, ok := b.srv.Clients.Get(cl.ID)
	if !ok || old == cl || old.Net.Inline || !sessionExpires(old) {
		return
	}
	b.mu.Lock()
	done, tracked := b.serving[old.Net.Conn]
	b.mu.Unlock()
	if !tracked {
		return // its handler already returned
	}
	if !old.Closed() {
		_ = b.srv.DisconnectClient(old, packets.ErrSessionTakenOver) // [MQTT-3.1.4-3]
	}
	if b.testTakeoverWait != nil {
		b.testTakeoverWait()
	}
	t := time.NewTimer(takeoverWait)
	defer t.Stop()
	select {
	case <-done:
	case <-b.done:
	case <-t.C:
		b.log.Warn("mqtt: previous connection still tearing down; taking its session over anyway",
			"client", cl.ID, "timeout", takeoverWait)
	}
}

// serve runs mochi's connection handler for conn, recording it as being served until the
// handler returns (awaitPredecessor waits on that).
func (b *Broker) serve(establish listeners.EstablishFn, listener string, conn net.Conn) error {
	done := make(chan struct{})
	b.mu.Lock()
	b.serving[conn] = done
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.serving, conn)
		b.mu.Unlock()
		close(done)
	}()
	return establish(listener, conn)
}
