package mqtt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

// creds is a fixed-table Authenticator.
type creds map[string]struct {
	pass string
	p    Principal
}

func (c creds) Authenticate(user, pass string) (Principal, bool) {
	e, ok := c[user]
	if !ok || e.pass != pass {
		return Principal{}, false
	}
	return e.p, true
}

var testCreds = creds{
	"n1": {"k1", Principal{Kind: Node, NodeID: "n1"}},
	"n2": {"k2", Principal{Kind: Node, NodeID: "n2"}},
	"cc": {"s", Principal{Kind: Server}},
}

func startBroker(t *testing.T, allowAnon bool) *Broker {
	t.Helper()
	b, err := New(testCreds, Options{
		TCPAddr:        "127.0.0.1:0",
		WSAddr:         "127.0.0.1:0",
		AllowAnonymous: allowAnon,
		Logger:         slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func node(id string) connectOpts {
	return connectOpts{clientID: NodeClientIDPrefix + id, user: id, pass: "k" + strings.TrimPrefix(id, "n"), clean: true}
}

func TestConnectAuth(t *testing.T) {
	b := startBroker(t, false)
	cases := []struct {
		name string
		o    connectOpts
		ok   bool
	}{
		{"node", node("n1"), true},
		{"server", connectOpts{clientID: "jarvis-cc-1", user: "cc", pass: "s", clean: true}, true},
		{"bad password", connectOpts{clientID: "jarvis-node-n1", user: "n1", pass: "nope", clean: true}, false},
		{"unknown user", connectOpts{clientID: "x", user: "who", pass: "k", clean: true}, false},
		{"anonymous", connectOpts{clientID: "anon", clean: true}, false},
		{"node with another node's client id", connectOpts{clientID: "jarvis-node-n2", user: "n1", pass: "k1", clean: true}, false},
		{"v5 node", connectOpts{clientID: "jarvis-node-n1", user: "n1", pass: "k1", clean: true, version: 5}, true},
		{"websocket node", connectOpts{clientID: "jarvis-node-n1", user: "n1", pass: "k1", clean: true, ws: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, ack := dial(t, b, tc.o)
			if got := ack.ReasonCode == 0; got != tc.ok {
				t.Fatalf("connack code %#x, want accepted=%v", ack.ReasonCode, tc.ok)
			}
			if !tc.ok {
				c.expectClosed()
			}
		})
	}
}

func TestAllowAnonymous(t *testing.T) {
	b := startBroker(t, true)
	c, ack := dial(t, b, connectOpts{clientID: "anon", clean: true})
	if ack.ReasonCode != 0 {
		t.Fatalf("anonymous refused: %#x", ack.ReasonCode)
	}
	if code := c.subscribe("#"); code != 1 {
		t.Fatalf("anonymous subscribe code %#x", code)
	}
	// Bad credentials are still refused.
	c2, ack := dial(t, b, connectOpts{clientID: "x", user: "n1", pass: "bad", clean: true})
	if ack.ReasonCode == 0 {
		t.Fatal("bad credentials accepted with AllowAnonymous")
	}
	c2.expectClosed()
}

func TestNodeSubscribeACL(t *testing.T) {
	b := startBroker(t, false)
	for _, v := range []byte{4, 5} {
		t.Run(fmt.Sprintf("v%d", v), func(t *testing.T) {
			o := node("n1")
			o.version = v
			c, _ := dial(t, b, o)
			for filter, want := range map[string]bool{
				"jarvis/nodes/n1/#":       true,
				"jarvis/auth/+/ready":     true,
				"jarvis/nodes/n2/#":       false,
				"jarvis/nodes/+/commands": false,
				"#":                       false,
			} {
				code := c.subscribe(filter)
				if ok := code < 0x80; ok != want {
					t.Errorf("subscribe %q: code %#x, want allowed=%v", filter, code, want)
				}
				if !want && v == 5 && code != packets.ErrNotAuthorized.Code {
					t.Errorf("v5 subscribe %q: code %#x, want not-authorized", filter, code)
				}
			}
		})
	}
}

// collect records every message the broker routes under jarvis/#.
func collect(t *testing.T, b *Broker) func() []string {
	var mu sync.Mutex
	var got []string
	err := b.srv.Subscribe("jarvis/#", 999, func(_ *mochi.Client, _ packets.Subscription, pk packets.Packet) {
		mu.Lock()
		got = append(got, pk.TopicName)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

func TestNodePublishACL(t *testing.T) {
	b := startBroker(t, false)
	seen := collect(t, b)
	c, _ := dial(t, b, node("n1"))

	for _, topic := range []string{
		"jarvis/nodes/n1/commands",
		"jarvis/nodes/n2/command-data/get/response/c1",
		"jarvis/nodes/n2/commands",
		"jarvis/auth/google/ready",
		"jarvis/nodes/n1/settings/request",
	} {
		c.publish(topic, []byte("x"), 0)
	}
	allowed := []string{"jarvis/nodes/n1/command-data/get/response/c1", "jarvis/nodes/n1/context/query/response/c2"}
	for _, topic := range allowed {
		c.publish(topic, []byte("ok"), 0)
	}
	waitFor(t, func() bool { return len(seen()) >= len(allowed) })
	if got := seen(); fmt.Sprint(got) != fmt.Sprint(allowed) {
		t.Fatalf("routed %v, want only %v", got, allowed)
	}

	// A forbidden QoS-1 publish disconnects a 3.1.1 client (it has no way to NACK) ...
	c.publish("jarvis/nodes/n1/commands", []byte("x"), 1)
	c.expectClosed()

	// ... and is NACKed for an MQTT 5 client.
	o := node("n1")
	o.version = 5
	c5, _ := dial(t, b, o)
	c5.publish("jarvis/nodes/n2/commands", []byte("x"), 1)
	if ack := c5.expect(packets.Puback); ack.ReasonCode != packets.ErrNotAuthorized.Code {
		t.Fatalf("puback code %#x, want not-authorized", ack.ReasonCode)
	}
}

func TestServerPublishReachesNode(t *testing.T) {
	b := startBroker(t, false)
	for _, ws := range []bool{false, true} {
		t.Run(fmt.Sprintf("ws=%v", ws), func(t *testing.T) {
			o := node("n1")
			o.ws = ws
			c, _ := dial(t, b, o)
			c.subscribe("jarvis/nodes/n1/#")
			c.subscribe("jarvis/auth/+/ready")

			if err := b.Publish("jarvis/nodes/n1/commands", []byte(`[{"command":"tts"}]`), 1, false); err != nil {
				t.Fatal(err)
			}
			pk := c.expect(packets.Publish)
			if pk.TopicName != "jarvis/nodes/n1/commands" || string(pk.Payload) != `[{"command":"tts"}]` || pk.FixedHeader.Qos != 1 {
				t.Fatalf("got %s %q qos %d", pk.TopicName, pk.Payload, pk.FixedHeader.Qos)
			}
			if err := b.Publish("jarvis/auth/google/ready", []byte(`{}`), 1, false); err != nil {
				t.Fatal(err)
			}
			if pk := c.expect(packets.Publish); pk.TopicName != "jarvis/auth/google/ready" {
				t.Fatalf("got %s", pk.TopicName)
			}
			// Another node's traffic never arrives.
			_ = b.Publish("jarvis/nodes/n2/commands", []byte(`[]`), 1, false)
			c.expectNothing(100 * time.Millisecond)
			c.disconnect()
		})
	}
}

func TestOfflineQoS1Delivery(t *testing.T) {
	b := startBroker(t, false)
	o := node("n1")
	o.clean = false
	c, _ := dial(t, b, o)
	c.subscribe("jarvis/nodes/n1/#")
	c.disconnect()
	waitFor(t, func() bool {
		cl, ok := b.srv.Clients.Get("jarvis-node-n1")
		return ok && cl.Closed()
	})

	if err := b.Publish("jarvis/nodes/n1/settings/request", []byte(`{"request_id":"r1"}`), 1, false); err != nil {
		t.Fatal(err)
	}

	c2, ack := dial(t, b, o)
	if ack.ReasonCode != 0 || !ack.SessionPresent {
		t.Fatalf("reconnect: code %#x session present %v", ack.ReasonCode, ack.SessionPresent)
	}
	pk := c2.expect(packets.Publish)
	if pk.TopicName != "jarvis/nodes/n1/settings/request" || string(pk.Payload) != `{"request_id":"r1"}` {
		t.Fatalf("got %s %q", pk.TopicName, pk.Payload)
	}
	// The resumed session keeps its subscription without resubscribing.
	_ = b.Publish("jarvis/nodes/n1/commands", []byte(`[]`), 1, false)
	if pk := c2.expect(packets.Publish); pk.TopicName != "jarvis/nodes/n1/commands" {
		t.Fatalf("got %s", pk.TopicName)
	}
}

// respond answers every request the node receives on {topic} with "reply:"+payload on
// {topic}/response/{payload}, the payload being the correlation id. Like a real node it
// ignores response topics, which its own subtree subscription echoes back to it.
func respond(c *testClient) {
	go func() {
		for pk := range c.in {
			if pk.FixedHeader.Type == packets.Publish && !strings.Contains(pk.TopicName, "/response/") {
				c.publish(pk.TopicName+"/response/"+string(pk.Payload), []byte("reply:"+string(pk.Payload)), 1)
			}
		}
	}()
}

func TestRequestRoundTrip(t *testing.T) {
	b := startBroker(t, false)
	c, _ := dial(t, b, node("n1"))
	c.subscribe("jarvis/nodes/n1/#")
	respond(c)

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	got, err := b.Request(ctx, "jarvis/nodes/n1/command-data/get", []byte("c1"), "c1")
	if err != nil || string(got) != "reply:c1" {
		t.Fatalf("got %q, %v", got, err)
	}
	// The response subscription is gone afterwards.
	if subs := b.srv.Topics.Subscribers("jarvis/nodes/n1/command-data/get/response/c1"); len(subs.InlineSubscriptions) != 0 {
		t.Fatalf("response subscription leaked: %v", subs.InlineSubscriptions)
	}
}

func TestRequestTimeout(t *testing.T) {
	b := startBroker(t, false)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err := b.Request(ctx, "jarvis/nodes/n2/context/query", []byte(`{}`), "c1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if subs := b.srv.Topics.Subscribers("jarvis/nodes/n2/context/query/response/c1"); len(subs.InlineSubscriptions) != 0 {
		t.Fatal("response subscription leaked after timeout")
	}
}

func TestConcurrentRequests(t *testing.T) {
	b := startBroker(t, false)
	for _, id := range []string{"n1", "n2"} {
		c, _ := dial(t, b, node(id))
		c.subscribe("jarvis/nodes/" + id + "/#")
		respond(c)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			topic := []string{
				"jarvis/nodes/n1/command-data/get",
				"jarvis/nodes/n2/command-data/get",
				"jarvis/nodes/n1/context/query",
				"jarvis/nodes/n2/context/query",
			}[i%4]
			cid := fmt.Sprintf("cid-%d", i)
			got, err := b.Request(ctx, topic, []byte(cid), cid)
			if err != nil || string(got) != "reply:"+cid {
				t.Errorf("%s %s: got %q, %v", topic, cid, got, err)
			}
		}()
	}
	wg.Wait()
}

func TestRequestDuplicateCorrelationID(t *testing.T) {
	b := startBroker(t, false)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	first := make(chan error, 1)
	go func() {
		_, err := b.Request(ctx, "jarvis/nodes/n1/command-data/get", nil, "dup")
		first <- err
	}()
	waitFor(t, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(b.pending) == 1
	})
	if _, err := b.Request(ctx, "jarvis/nodes/n1/command-data/get", nil, "dup"); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("duplicate request: err = %v, want refusal", err)
	}
	if err := <-first; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first request: %v", err)
	}
	if _, err := b.Request(ctx, "t", nil, "a/b"); err == nil {
		t.Fatal("correlation id with / accepted")
	}
}

func TestNodeRetainIsStripped(t *testing.T) {
	b := startBroker(t, false)
	c, _ := dial(t, b, node("n1"))
	c.send(packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish, Retain: true},
		TopicName:   "jarvis/nodes/n1/context/query/response/stale",
		Payload:     []byte("stale"),
	})
	seen := collect(t, b)
	c.publish("jarvis/nodes/n1/context/query/response/marker", nil, 0)
	waitFor(t, func() bool { return len(seen()) > 0 })

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if got, err := b.Request(ctx, "jarvis/nodes/n1/context/query", nil, "stale"); err == nil {
		t.Fatalf("stale retained response replayed: %q", got)
	}
}

func TestCloseUnblocksRequest(t *testing.T) {
	b := startBroker(t, false)
	errc := make(chan error, 1)
	go func() {
		_, err := b.Request(context.Background(), "jarvis/nodes/n1/command-data/get", nil, "c1")
		errc <- err
	}()
	waitFor(t, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(b.pending) == 1
	})
	_ = b.Close()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("err = %v, want ErrClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Request still blocked after Close")
	}
	if err := b.Publish("x", nil, 1, false); !errors.Is(err, ErrClosed) {
		t.Fatalf("publish after close: %v", err)
	}
}

func TestStartPortConflict(t *testing.T) {
	a := startBroker(t, false)
	b, err := New(testCreds, Options{TCPAddr: "127.0.0.1:0", WSAddr: a.WSAddr(), Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := b.Start(t.Context()); err == nil {
		t.Fatal("Start succeeded on a taken port")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
