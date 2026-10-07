package cc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Publisher is the broker side the bus needs: mqtt.Broker implements it in process.
type Publisher interface {
	Publish(topic string, payload []byte, qos byte, retain bool) error
	Request(ctx context.Context, topic string, payload []byte, correlationID string) ([]byte, error)
}

// Timeouts and TTLs (doc 05 §5).
const (
	commandVerifyTTL = 5 * time.Minute // a published command can be verified for this long
	resultTTL        = 5 * time.Minute // an unclaimed result slot lives this long
)

// ErrNoResult is returned when a node doesn't answer before the deadline.
var ErrNoResult = errors.New("cc: node did not respond in time")

// ErrNoBroker is returned when MQTT is not available.
var ErrNoBroker = errors.New("cc: MQTT not available")

// Bus is CC's server→node data plane (doc 05 §11 NodeBus): MQTT publishes to
// jarvis/nodes/{id}/…, the command verify map, the request/response helper, and the
// in-process result slots that replace the legacy /tmp rendezvous files (§3.6).
//
// Every node→CC result is keyed by a request id that CC issued to one node (D4): a result
// only lands when the posting node is the one the id was issued to. A late result (no slot)
// is dropped.
type Bus struct {
	pub  Publisher
	log  *slog.Logger
	now  func() time.Time
	seen func(nodeID string) // liveness hook, called after an MQTT round trip

	mu      sync.Mutex
	pending map[string]pendingCommand // verify map: rid -> issuing node
	slots   map[string]*slot          // result slots: rid -> node + mailbox
}

type pendingCommand struct {
	nodeID  string
	command string
	expires time.Time
}

type slot struct {
	nodeID  string
	ch      chan json.RawMessage // capacity 1: the first result wins
	expires time.Time
}

func newBus(pub Publisher, log *slog.Logger, now func() time.Time) *Bus {
	if log == nil {
		log = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	return &Bus{pub: pub, log: log, now: now, seen: func(string) {},
		pending: map[string]pendingCommand{}, slots: map[string]*slot{}}
}

// Available reports whether MQTT publishing is possible.
func (b *Bus) Available() bool { return b != nil && b.pub != nil }

// NodeTopic is jarvis/nodes/{nodeID}/{sub}.
func NodeTopic(nodeID, sub string) string { return "jarvis/nodes/" + nodeID + "/" + sub }

// Publish sends v as JSON to jarvis/nodes/{nodeID}/{sub} at QoS 1, retain false (§2.4).
func (b *Bus) Publish(nodeID, sub string, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return b.PublishTopic(NodeTopic(nodeID, sub), payload)
}

// PublishTopic publishes raw bytes to any topic (e.g. jarvis/auth/{provider}/ready).
func (b *Bus) PublishTopic(topic string, payload []byte) error {
	if !b.Available() {
		return ErrNoBroker
	}
	return b.pub.Publish(topic, payload, 1, false)
}

// Command publishes [{command: verb, details: {...details, request_id: rid}}] to
// jarvis/nodes/{nodeID}/commands with a fresh rid and records rid for VerifyCommand. Like
// legacy NodeCommandService it never fails: a publish error is logged and rid still returned.
// No `trusted` flag is ever added (D4/D7): per-node broker ACLs make commands authentic, and
// a node still verifies any `action` through POST /commands/{rid}/verify (D48).
func (b *Bus) Command(nodeID, verb string, details map[string]any) string {
	return b.CommandWithID(nodeID, verb, details, uuid4())
}

// CommandWithID is Command with a caller-chosen rid. request_id in details is always rid
// (it overrides a caller value, §7.1).
func (b *Bus) CommandWithID(nodeID, verb string, details map[string]any, rid string) string {
	d := make(map[string]any, len(details)+1)
	for k, v := range details {
		d[k] = v
	}
	d["request_id"] = rid
	now := b.now()
	b.mu.Lock()
	b.gcLocked(now)
	b.pending[rid] = pendingCommand{nodeID: nodeID, command: verb, expires: now.Add(commandVerifyTTL)}
	b.mu.Unlock()

	payload, err := json.Marshal([]map[string]any{{"command": verb, "details": d}})
	if err != nil {
		b.log.Error("cc: encode command", "verb", verb, "err", err)
		return rid
	}
	if err := b.PublishTopic(NodeTopic(nodeID, "commands"), payload); err != nil {
		b.log.Warn("cc: command not delivered", "verb", verb, "node", nodeID, "err", err)
	}
	return rid
}

// CommandAwait publishes a command and waits for the node's result on replyKey (the rid
// itself when replyKey is ""), which the verb's result sink delivers (§3.5 table). The slot
// is opened before publishing so a fast reply can't be missed.
func (b *Bus) CommandAwait(ctx context.Context, nodeID, verb string, details map[string]any, replyKey string) (string, json.RawMessage, error) {
	rid := uuid4()
	key := replyKey
	if key == "" {
		key = rid
	}
	b.Expect(key, nodeID)
	defer b.Drop(key)
	b.CommandWithID(nodeID, verb, details, rid)
	res, err := b.Await(ctx, key)
	return rid, res, err
}

// VerifyCommand is verify_command (§3.5, §7.3): true once for the issuing node within 5 min.
// A node mismatch is false and does not consume the entry; expiry consumes it.
func (b *Bus) VerifyCommand(rid, nodeID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.pending[rid]
	if !ok {
		return false
	}
	if e.nodeID != nodeID {
		b.log.Warn("cc: command verify node mismatch", "request_id", rid, "issued_to", e.nodeID, "caller", nodeID)
		return false
	}
	delete(b.pending, rid)
	return !b.now().After(e.expires)
}

// IssuedTo returns the node a still-pending command rid was issued to.
func (b *Bus) IssuedTo(rid string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.pending[rid]
	if !ok || b.now().After(e.expires) {
		return "", false
	}
	return e.nodeID, true
}

// Expect opens a result slot for rid, owned by nodeID. Only that node's Deliver lands.
func (b *Bus) Expect(rid, nodeID string) {
	b.ExpectFor(rid, nodeID, resultTTL)
}

// ExpectFor is Expect with a custom lifetime (mailboxes polled by mobile).
func (b *Bus) ExpectFor(rid, nodeID string, ttl time.Duration) {
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gcLocked(now)
	b.slots[rid] = &slot{nodeID: nodeID, ch: make(chan json.RawMessage, 1), expires: now.Add(ttl)}
}

// Drop closes rid's slot; a later result is dropped.
func (b *Bus) Drop(rid string) {
	b.mu.Lock()
	delete(b.slots, rid)
	b.mu.Unlock()
}

// DeliverResult is what happened to a posted result.
type DeliverResult int

const (
	Delivered     DeliverResult = iota
	NoSlot                      // unknown, expired or already answered: dropped
	WrongNode                   // the rid was issued to another node: refused (D4)
	AlreadyFilled               // a result is already waiting: dropped
)

// Deliver hands a node's result to rid's slot.
func (b *Bus) Deliver(rid, nodeID string, result json.RawMessage) DeliverResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.slots[rid]
	if !ok || b.now().After(s.expires) {
		return NoSlot
	}
	if s.nodeID != nodeID {
		return WrongNode
	}
	select {
	case s.ch <- result:
		return Delivered
	default:
		return AlreadyFilled
	}
}

// Await waits for rid's result until ctx ends (ErrNoResult on deadline).
func (b *Bus) Await(ctx context.Context, rid string) (json.RawMessage, error) {
	b.mu.Lock()
	s, ok := b.slots[rid]
	b.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("cc: no result slot for %s", rid)
	}
	select {
	case r := <-s.ch:
		return r, nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ErrNoResult
		}
		return nil, ctx.Err()
	}
}

// Take returns rid's result without waiting (mobile polls), consuming the slot when found.
func (b *Bus) Take(rid string) (json.RawMessage, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.slots[rid]
	if !ok {
		return nil, false
	}
	select {
	case r := <-s.ch:
		delete(b.slots, rid)
		return r, true
	default:
		return nil, false
	}
}

// Request is the MQTT request/response pattern (§3.6, §7.2): publish payload to
// jarvis/nodes/{nodeID}/{sub} and wait for {topic}/response/{correlation_id}. The payload's
// own correlation_id is used when present; otherwise one is added. A successful round trip
// is proof of life (§3.7).
func (b *Bus) Request(ctx context.Context, nodeID, sub string, payload map[string]any) (json.RawMessage, error) {
	if !b.Available() {
		return nil, ErrNoBroker
	}
	p := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		p[k] = v
	}
	cid, _ := p["correlation_id"].(string)
	if cid == "" || strings.ContainsAny(cid, "/+#") {
		cid = uuid4()
		p["correlation_id"] = cid
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	resp, err := b.pub.Request(ctx, NodeTopic(nodeID, sub), raw, cid)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ErrNoResult
		}
		return nil, err
	}
	b.seen(nodeID)
	return resp, nil
}

func (b *Bus) gcLocked(now time.Time) {
	for k, e := range b.pending {
		if now.After(e.expires) {
			delete(b.pending, k)
		}
	}
	for k, s := range b.slots {
		if now.After(s.expires) {
			delete(b.slots, k)
		}
	}
}
