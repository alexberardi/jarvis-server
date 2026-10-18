package mqtt

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/mochi-mqtt/server/v2/packets"
)

// Anonymous clients (AllowAnonymous, dev only) are read-only: they may subscribe to anything
// but publish only under AnonymousPublishPrefix, so they can't command a node.
func TestAnonymousIsReadOnly(t *testing.T) {
	b := startBroker(t, true)
	seen := collect(t, b)
	c, _ := dial(t, b, connectOpts{clientID: "anon", clean: true})

	for _, topic := range []string{
		"jarvis/nodes/n1/commands",
		"jarvis/nodes/n1/command-data/get/response/c1",
		"jarvis/auth/google/ready",
		"jarvis/devices",
		"jarvis/dev",
	} {
		c.publish(topic, []byte("x"), 0)
	}
	allowed := []string{"jarvis/dev/probe", "jarvis/dev/a/b"}
	for _, topic := range allowed {
		c.publish(topic, []byte("ok"), 0)
	}
	waitFor(t, func() bool { return len(seen()) >= len(allowed) })
	if got := seen(); fmt.Sprint(got) != fmt.Sprint(allowed) {
		t.Fatalf("anonymous publishes routed %v, want only %v", got, allowed)
	}

	// Reading stays open: an anonymous subscriber sees server traffic.
	if code := c.subscribe("jarvis/nodes/+/commands"); code != 1 {
		t.Fatalf("anonymous subscribe code %#x", code)
	}
	if err := b.Publish("jarvis/nodes/n1/commands", []byte("[]"), 1, false); err != nil {
		t.Fatal(err)
	}
	if pk := c.expect(packets.Publish); pk.TopicName != "jarvis/nodes/n1/commands" {
		t.Fatalf("got %s", pk.TopicName)
	}

	// A forbidden QoS-1 publish is NACKed for an MQTT 5 client.
	c5, _ := dial(t, b, connectOpts{clientID: "anon5", clean: true, version: 5})
	c5.publish("jarvis/nodes/n1/commands", []byte("x"), 1)
	if ack := c5.expect(packets.Puback); ack.ReasonCode != packets.ErrNotAuthorized.Code {
		t.Fatalf("puback code %#x, want not-authorized", ack.ReasonCode)
	}
}

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func startLogged(t *testing.T, allowAnon bool) (*Broker, *syncBuf) {
	t.Helper()
	var buf syncBuf
	b, err := New(testCreds, Options{
		TCPAddr: "127.0.0.1:0", WSAddr: "127.0.0.1:0", AllowAnonymous: allowAnon,
		Logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b, &buf
}

func TestAnonymousWarnsAtStart(t *testing.T) {
	b, buf := startLogged(t, true)
	var warn string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, "level=WARN") && strings.Contains(l, "anonymous") {
			warn = l
		}
	}
	if warn == "" {
		t.Fatalf("no anonymous WARN at start:\n%s", buf)
	}
	for _, want := range []string{b.TCPAddr(), b.WSAddr(), "JARVIS_MQTT_ALLOW_ANONYMOUS"} {
		if !strings.Contains(warn, want) {
			t.Errorf("WARN %q doesn't name %q", warn, want)
		}
	}

	_, buf = startLogged(t, false)
	if strings.Contains(buf.String(), "anonymous") {
		t.Fatalf("anonymous mentioned with it off:\n%s", buf)
	}
}

// The WebSocket listener refuses a browser page on another site (a cross-site page could
// otherwise drive the broker from a LAN user's browser); clients without an Origin (nodes,
// the mobile app) and pages on this host, the LAN or an allowed host are accepted.
func TestWebSocketOrigin(t *testing.T) {
	var buf syncBuf
	b, err := New(testCreds, Options{
		WSAddr: "127.0.0.1:0", AllowedOrigins: []string{"jarvis.example.com"},
		Logger: slog.New(slog.NewTextHandler(&buf, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	for _, tc := range []struct {
		origin string
		ok     bool
	}{
		{"", true},
		{"http://" + b.WSAddr(), true}, // same host
		{"http://127.0.0.1:7710", true},
		{"http://localhost:5173", true},
		{"http://192.168.1.20:7710", true},
		{"http://10.0.0.5", true},
		{"http://jarvis.local:7710", true},
		{"https://jarvis.example.com", true},
		{"https://JARVIS.example.com:8443", true},
		{"https://evil.example", false},
		{"https://jarvis.example.com.evil.example", false},
		{"https://8.8.8.8", false},
		{"null", false},
		{"not a url", false},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			h := http.Header{}
			if tc.origin != "" {
				h.Set("Origin", tc.origin)
			}
			d := websocket.Dialer{Subprotocols: []string{"mqtt"}}
			c, resp, err := d.Dial("ws://"+b.WSAddr(), h)
			if c != nil {
				_ = c.Close()
			}
			if (err == nil) != tc.ok {
				t.Fatalf("upgrade err = %v, want ok=%v", err, tc.ok)
			}
			if !tc.ok && (resp == nil || resp.StatusCode != http.StatusForbidden) {
				t.Fatalf("refusal response %v, want 403", resp)
			}
		})
	}
	if !strings.Contains(buf.String(), "evil.example") {
		t.Fatalf("a refused origin isn't logged:\n%s", buf.String())
	}
}

// Through a tunnel or proxy the Host is the public name, which the page's Origin matches.
func TestOriginSameHost(t *testing.T) {
	for _, tc := range []struct {
		host, origin string
		allowed      []string
		ok           bool
	}{
		{"home.example.net", "https://home.example.net", nil, true},
		{"home.example.net:443", "https://Home.Example.Net", nil, true},
		{"[fd00::1]:9883", "http://[fd00::1]:7710", nil, true},
		{"home.example.net", "https://other.example.net", nil, false},
		{"home.example.net", "https://admin.example.net", []string{"https://admin.example.net:8443"}, true},
		{"home.example.net", "https://admin.example.net", []string{" admin.example.net:443 "}, true},
		{"home.example.net", "https://evil.example", []string{""}, false},
	} {
		r, _ := http.NewRequest(http.MethodGet, "http://"+tc.host+"/", nil)
		r.Header.Set("Origin", tc.origin)
		if got := originAllowed(r, tc.allowed); got != tc.ok {
			t.Errorf("host %s origin %s allowed %v: %v, want %v", tc.host, tc.origin, tc.allowed, got, tc.ok)
		}
	}
}

// A Last Will is published by the broker without an ACL check, so its topic is checked
// against the publish rules at CONNECT.
func TestWillTopicFollowsPublishRules(t *testing.T) {
	b := startBroker(t, true)
	seen := collect(t, b)

	refused := []connectOpts{
		{clientID: "anon-will", clean: true, willTopic: "jarvis/nodes/n1/commands"},
		{clientID: "anon-will-dev", clean: true, willTopic: "jarvis/dev"},
	}
	n1 := node("n1")
	n1.willTopic = "jarvis/nodes/n2/commands"
	refused = append(refused, n1)
	for _, o := range refused {
		c, ack := dial(t, b, o)
		if ack.ReasonCode == 0 {
			t.Fatalf("%s with a will on %s: connected", o.clientID, o.willTopic)
		}
		c.expectClosed()
	}

	c, ack := dial(t, b, connectOpts{clientID: "anon-dev", clean: true, willTopic: "jarvis/dev/x"})
	if ack.ReasonCode != 0 {
		t.Fatalf("anonymous with a will on jarvis/dev/x: connack %#x", ack.ReasonCode)
	}
	_ = c.conn.Close() // ungraceful: the broker sends the will
	waitFor(t, func() bool { return len(seen()) > 0 })
	if got := seen(); fmt.Sprint(got) != "[jarvis/dev/x]" {
		t.Fatalf("routed %v, want only [jarvis/dev/x]", got)
	}
}
