// Package mqtt is jarvisd's embedded MQTT broker, replacing Mosquitto (PLAN §3.2).
//
// It wraps mochi-mqtt and serves MQTT 3.1.1 and 5 over TCP and WebSocket with the topic
// catalogue of docs/cc/05-nodes.md §2.4. What it adds on top of mochi:
//   - per-node credentials and ACLs (D4): a node subscribes only to its own subtree and
//     jarvis/auth/+/ready, and publishes only its own responses; anonymous clients are
//     refused unless AllowAnonymous is set
//   - in-process publishing for CC via mochi's inline client (no loopback connection)
//   - Request, the subscribe-before-publish request/response helper (doc 05 §7.2)
//
// Sessions of clean_session=false clients are held in memory (D40/Q7), so QoS-1 messages
// queue for an offline node until it reconnects, and are lost on restart, as with Mosquitto.
package mqtt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"

	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

// Default listen addresses (PLAN §3.2).
const (
	DefaultTCPAddr = ":1884"
	DefaultWSAddr  = ":9883"
)

// ErrClosed is returned after Close.
var ErrClosed = errors.New("mqtt: broker closed")

// Options configures a Broker.
type Options struct {
	TCPAddr        string // "" disables the TCP listener
	WSAddr         string // "" disables the WebSocket listener
	AllowAnonymous bool   // legacy/dev only: admit clients without credentials, unrestricted
	Logger         *slog.Logger
}

// DefaultOptions listens on both default addresses and refuses anonymous clients.
func DefaultOptions() Options {
	return Options{TCPAddr: DefaultTCPAddr, WSAddr: DefaultWSAddr}
}

// Broker is the embedded broker.
type Broker struct {
	opts Options
	log  *slog.Logger
	srv  *mochi.Server

	mu      sync.Mutex
	addrs   map[string]string   // listener id -> bound address
	pending map[string]struct{} // response topics with a Request waiting
	subID   int                 // last inline subscription id

	closeOnce sync.Once
	done      chan struct{}
}

// New builds a broker; auth may be nil if only anonymous clients are expected.
func New(auth Authenticator, opts Options) (*Broker, error) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "mqtt")
	srv := mochi.New(&mochi.Options{InlineClient: true, Logger: log})
	if err := srv.AddHook(newAuthHook(auth, opts.AllowAnonymous, log), nil); err != nil {
		return nil, fmt.Errorf("mqtt: add auth hook: %w", err)
	}
	return &Broker{
		opts:    opts,
		log:     log,
		srv:     srv,
		addrs:   map[string]string{},
		pending: map[string]struct{}{},
		done:    make(chan struct{}),
	}, nil
}

// Start binds the listeners and serves in the background until ctx is cancelled or Close
// is called. Both listeners bind before either serves, so a port conflict fails cleanly.
func (b *Broker) Start(ctx context.Context) error {
	if b.closed() {
		return ErrClosed
	}
	var lc net.ListenConfig
	var bound []net.Listener
	fail := func(err error) error {
		for _, ln := range bound {
			_ = ln.Close()
		}
		return err
	}
	var ls []listeners.Listener
	for _, l := range []struct{ id, addr string }{{"tcp", b.opts.TCPAddr}, {"ws", b.opts.WSAddr}} {
		if l.addr == "" {
			continue
		}
		ln, err := lc.Listen(ctx, "tcp", l.addr)
		if err != nil {
			return fail(fmt.Errorf("mqtt: listen %s on %s: %w", l.id, l.addr, err))
		}
		bound = append(bound, ln)
		if l.id == "ws" {
			ls = append(ls, newWSListener(l.id, ln))
		} else {
			ls = append(ls, listeners.NewNet(l.id, ln))
		}
	}
	for _, l := range ls {
		if err := b.srv.AddListener(l); err != nil {
			return fail(fmt.Errorf("mqtt: add listener %s: %w", l.ID(), err))
		}
	}
	if err := b.srv.Serve(); err != nil {
		return fail(fmt.Errorf("mqtt: serve: %w", err))
	}
	b.mu.Lock()
	for _, l := range ls {
		b.addrs[l.ID()] = l.Address()
		b.log.Info("mqtt listening", "listener", l.ID(), "addr", l.Address())
	}
	b.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
			_ = b.Close()
		case <-b.done:
		}
	}()
	return nil
}

// TCPAddr returns the bound TCP address (useful with port 0), or "" if not listening.
func (b *Broker) TCPAddr() string { return b.addr("tcp") }

// WSAddr returns the bound WebSocket address, or "" if not listening.
func (b *Broker) WSAddr() string { return b.addr("ws") }

func (b *Broker) addr(id string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.addrs[id]
}

// Close stops the listeners and disconnects every client. Waiting Requests return ErrClosed.
func (b *Broker) Close() error {
	var err error
	b.closeOnce.Do(func() {
		close(b.done)
		err = b.srv.Close()
	})
	return err
}

func (b *Broker) closed() bool {
	select {
	case <-b.done:
		return true
	default:
		return false
	}
}

// Publish delivers a message from the in-process server client, bypassing ACLs. CC
// publishes with qos 1, retain false (doc 05 §2.4).
func (b *Broker) Publish(topic string, payload []byte, qos byte, retain bool) error {
	if b.closed() {
		return ErrClosed
	}
	if err := b.srv.Publish(topic, payload, retain, qos); err != nil {
		return fmt.Errorf("mqtt: publish %s: %w", topic, err)
	}
	return nil
}

// Request publishes payload to topic (qos 1) and returns the first message on
// {topic}/response/{correlationID}. It subscribes before publishing so a fast reply can't
// be missed (doc 05 §7.2), and returns ctx's error on timeout. Concurrent requests are
// safe; two in flight with the same response topic are refused.
func (b *Broker) Request(ctx context.Context, topic string, payload []byte, correlationID string) ([]byte, error) {
	if correlationID == "" || strings.ContainsAny(correlationID, "/+#") {
		return nil, fmt.Errorf("mqtt: invalid correlation id %q", correlationID)
	}
	if b.closed() {
		return nil, ErrClosed
	}
	respTopic := topic + "/response/" + correlationID

	b.mu.Lock()
	if _, dup := b.pending[respTopic]; dup {
		b.mu.Unlock()
		return nil, fmt.Errorf("mqtt: request already waiting on %s", respTopic)
	}
	b.pending[respTopic] = struct{}{}
	b.subID++
	id := b.subID
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, respTopic)
		b.mu.Unlock()
	}()

	ch := make(chan []byte, 1)
	// Runs on the publisher's goroutine, so it must never block.
	handler := func(_ *mochi.Client, _ packets.Subscription, pk packets.Packet) {
		select {
		case ch <- bytes.Clone(pk.Payload):
		default:
		}
	}
	if err := b.srv.Subscribe(respTopic, id, handler); err != nil {
		return nil, fmt.Errorf("mqtt: subscribe %s: %w", respTopic, err)
	}
	defer func() { _ = b.srv.Unsubscribe(respTopic, id) }()

	if err := b.Publish(topic, payload, 1, false); err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("mqtt: no response on %s: %w", respTopic, ctx.Err())
	case <-b.done:
		return nil, ErrClosed
	}
}
