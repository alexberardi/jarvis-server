package cc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/mochi-mqtt/server/v2/packets"
)

// --- fake auth module (authn.Authority + UserVerifier + NodeRegistry) ---

type fakeNode struct {
	key, household string
	active         bool
	services       []string
}

type fakeAuth struct {
	mu    sync.Mutex
	users map[string]authn.User           // token -> user
	roles map[string]map[int64]authn.Role // household -> user -> role
	nodes map[string]*fakeNode            // node id -> node
	keys  int
}

func newFakeAuth() *fakeAuth {
	return &fakeAuth{users: map[string]authn.User{}, roles: map[string]map[int64]authn.Role{}, nodes: map[string]*fakeNode{}}
}

// addUser registers a user with a role in a household; the token is "tok-<id>".
func (a *fakeAuth) addUser(id int64, household string, role authn.Role) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	tok := fmt.Sprintf("tok-%d", id)
	a.users[tok] = authn.User{ID: id, HouseholdID: household}
	if household != "" {
		if a.roles[household] == nil {
			a.roles[household] = map[int64]authn.Role{}
		}
		a.roles[household][id] = role
	}
	return tok
}

func (a *fakeAuth) VerifyUser(_ context.Context, token string) (authn.User, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if token == "expired" {
		return authn.User{}, authn.ErrExpired
	}
	u, ok := a.users[token]
	if !ok {
		return authn.User{}, authn.ErrInvalid
	}
	return u, nil
}

func (a *fakeAuth) ValidateNode(_ context.Context, id, key, svc string) (authn.NodeValidation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	n, ok := a.nodes[id]
	switch {
	case !ok:
		return authn.NodeValidation{Reason: "Node not found"}, nil
	case !n.active:
		return authn.NodeValidation{Reason: "Node is inactive"}, nil
	case n.key != key:
		return authn.NodeValidation{Reason: "Invalid node credentials"}, nil
	}
	for _, s := range n.services {
		if s == svc {
			return authn.NodeValidation{Valid: true, Node: authn.Node{ID: id, HouseholdID: n.household}}, nil
		}
	}
	return authn.NodeValidation{Reason: fmt.Sprintf("Node is not authorized to access service '%s'", svc)}, nil
}

func (a *fakeAuth) ValidateApp(context.Context, string, string) (authn.App, bool, error) {
	return authn.App{}, false, nil
}

func (a *fakeAuth) HouseholdRole(_ context.Context, uid int64, hh string) (authn.Role, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.roles[hh][uid]
	return r, ok, nil
}

type statusError struct {
	status int
	msg    string
}

func (e *statusError) Error() string   { return e.msg }
func (e *statusError) StatusCode() int { return e.status }

func (a *fakeAuth) RegisterNode(_ context.Context, id, hh, _ string, services []string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.nodes[id]; ok {
		return "", &statusError{400, "node_id already exists"}
	}
	if hh == "missing-household" {
		return "", &statusError{404, "Household not found"}
	}
	a.keys++
	key := fmt.Sprintf("key-%d", a.keys)
	a.nodes[id] = &fakeNode{key: key, household: hh, active: true, services: services}
	return key, nil
}

func (a *fakeAuth) DeactivateNode(_ context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n, ok := a.nodes[id]; ok {
		n.active = false
	}
	return nil
}

func (a *fakeAuth) active(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	n, ok := a.nodes[id]
	return ok && n.active
}

// --- environment ---

const testAdminKey = "admin-secret"

type env struct {
	t    *testing.T
	m    *Module
	auth *fakeAuth
	srv  *httptest.Server
	d    *db.DB
	now  time.Time
	nmu  sync.Mutex
}

func (e *env) clock() time.Time {
	e.nmu.Lock()
	defer e.nmu.Unlock()
	return e.now
}

func (e *env) advance(d time.Duration) {
	e.nmu.Lock()
	defer e.nmu.Unlock()
	e.now = e.now.Add(d)
}

type envOpts struct {
	dbPath string
	auth   *fakeAuth
	noMQTT bool
	github string
}

func newEnv(t *testing.T, o ...envOpts) *env {
	t.Helper()
	var opt envOpts
	if len(o) > 0 {
		opt = o[0]
	}
	if opt.dbPath == "" {
		opt.dbPath = filepath.Join(t.TempDir(), "jarvis.db")
	}
	if opt.auth == nil {
		opt.auth = newFakeAuth()
	}
	ctx := context.Background()
	d, err := db.Open(ctx, opt.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, "cc", Migrations()); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, auth: opt.auth, d: d, now: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	m := &Module{
		Auth: opt.auth, Users: opt.auth, Nodes: opt.auth, AdminKey: testAdminKey,
		MQTT:      MQTTOptions{TCPAddr: "127.0.0.1:0", Disabled: opt.noMQTT},
		GitHubAPI: opt.github,
	}
	m.now = e.clock
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	m.Register(mux, module.Deps{DB: d, Log: log})
	sctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	if err := m.Start(sctx); err != nil {
		t.Fatal(err)
	}
	if m.broker != nil {
		t.Cleanup(func() { _ = m.broker.Close() })
	}
	e.m = m
	e.srv = httptest.NewServer(httpx.Middleware(log, "command-center", mux))
	t.Cleanup(e.srv.Close)
	return e
}

type resp struct {
	t      *testing.T
	status int
	header http.Header
	body   []byte
}

func (r *resp) json() map[string]any {
	r.t.Helper()
	var v map[string]any
	if err := json.Unmarshal(r.body, &v); err != nil {
		r.t.Fatalf("not a JSON object (%d): %s", r.status, r.body)
	}
	return v
}

func (r *resp) list() []any {
	r.t.Helper()
	var v []any
	if err := json.Unmarshal(r.body, &v); err != nil {
		r.t.Fatalf("not a JSON array (%d): %s", r.status, r.body)
	}
	return v
}

func (r *resp) want(status int) *resp {
	r.t.Helper()
	if r.status != status {
		r.t.Fatalf("status %d, want %d: %s", r.status, status, r.body)
	}
	return r
}

func (r *resp) detail(status int, d string) {
	r.t.Helper()
	r.want(status)
	if got := r.json()["detail"]; got != d {
		r.t.Fatalf("detail %v, want %q", got, d)
	}
}

type hdr map[string]string

func bearer(tok string) hdr { return hdr{"Authorization": "Bearer " + tok} }
func adminH() hdr           { return hdr{"X-API-Key": testAdminKey} }

func (e *env) do(method, path string, body any, h hdr) *resp {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return &resp{t: e.t, status: res.StatusCode, header: res.Header, body: b}
}

// testNode is a node created through the admin route.
type testNode struct{ id, key string }

func (n testNode) h() hdr { return hdr{"X-API-Key": n.id + ":" + n.key} }

func (e *env) createNode(id, household string) testNode {
	e.t.Helper()
	r := e.do("POST", "/api/v0/admin/nodes", map[string]any{"node_id": id, "household_id": household, "room": "kitchen"}, adminH()).want(200).json()
	return testNode{id: id, key: r["node_key"].(string)}
}

// --- MQTT test client (mochi's packet codec over a raw TCP connection) ---

type mqttClient struct {
	t      *testing.T
	conn   net.Conn
	in     chan packets.Packet
	wmu    sync.Mutex
	nextID uint16
}

// dialMQTT connects; it returns the CONNACK reason code.
func (e *env) dialMQTT(clientID, user, pass string) (*mqttClient, byte) {
	e.t.Helper()
	conn, err := net.Dial("tcp", e.m.broker.TCPAddr())
	if err != nil {
		e.t.Fatal(err)
	}
	c := &mqttClient{t: e.t, conn: conn, in: make(chan packets.Packet, 64)}
	e.t.Cleanup(func() { _ = conn.Close() })
	go c.readLoop()
	c.send(packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Connect}, Connect: packets.ConnectParams{
		ProtocolName: []byte("MQTT"), Clean: true, Keepalive: 60, ClientIdentifier: clientID,
		UsernameFlag: user != "", Username: []byte(user), PasswordFlag: pass != "", Password: []byte(pass),
	}})
	ack := c.expect(packets.Connack)
	return c, ack.ReasonCode
}

// dialNode connects as a node with the credential /node/mqtt-credentials hands out.
func (e *env) dialNode(n testNode) *mqttClient {
	e.t.Helper()
	cr := e.do("GET", "/api/v0/node/mqtt-credentials", nil, n.h()).want(200).json()
	c, code := e.dialMQTT("jarvis-node-"+n.id, cr["username"].(string), cr["password"].(string))
	if code != 0 {
		e.t.Fatalf("CONNACK %d", code)
	}
	return c
}

func (c *mqttClient) readLoop() {
	defer close(c.in)
	r := bufio.NewReader(c.conn)
	for {
		hb, err := r.ReadByte()
		if err != nil {
			return
		}
		var pk packets.Packet
		if pk.FixedHeader.Decode(hb) != nil {
			return
		}
		if pk.FixedHeader.Remaining, _, err = packets.DecodeLength(r); err != nil {
			return
		}
		buf := make([]byte, pk.FixedHeader.Remaining)
		if _, err := io.ReadFull(r, buf); err != nil {
			return
		}
		pk.ProtocolVersion = 4
		switch pk.FixedHeader.Type {
		case packets.Connack:
			err = pk.ConnackDecode(buf)
		case packets.Suback:
			err = pk.SubackDecode(buf)
		case packets.Puback:
			err = pk.PubackDecode(buf)
		case packets.Publish:
			if err = pk.PublishDecode(buf); err == nil && pk.FixedHeader.Qos == 1 {
				c.send(packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Puback}, PacketID: pk.PacketID})
			}
		}
		if err != nil {
			return
		}
		c.in <- pk
	}
}

func (c *mqttClient) send(pk packets.Packet) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	pk.ProtocolVersion = 4
	var buf bytes.Buffer
	var err error
	switch pk.FixedHeader.Type {
	case packets.Connect:
		err = pk.ConnectEncode(&buf)
	case packets.Subscribe:
		err = pk.SubscribeEncode(&buf)
	case packets.Publish:
		err = pk.PublishEncode(&buf)
	case packets.Puback:
		err = pk.PubackEncode(&buf)
	}
	if err != nil {
		c.t.Errorf("encode: %v", err)
		return
	}
	_, _ = c.conn.Write(buf.Bytes())
}

func (c *mqttClient) id() uint16 {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.nextID++
	return c.nextID
}

func (c *mqttClient) expect(typ byte) packets.Packet {
	c.t.Helper()
	select {
	case pk, ok := <-c.in:
		if !ok {
			c.t.Fatalf("connection closed waiting for packet type %d", typ)
		}
		if pk.FixedHeader.Type != typ {
			c.t.Fatalf("got packet type %d, want %d", pk.FixedHeader.Type, typ)
		}
		return pk
	case <-time.After(5 * time.Second):
		c.t.Fatalf("timed out waiting for packet type %d", typ)
	}
	return packets.Packet{}
}

// subscribe returns the SUBACK reason code (0x80 = refused).
func (c *mqttClient) subscribe(filter string) byte {
	c.t.Helper()
	id := c.id()
	c.send(packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Subscribe, Qos: 1}, PacketID: id,
		Filters: packets.Subscriptions{{Filter: filter, Qos: 1}}})
	ack := c.expect(packets.Suback)
	return ack.ReasonCodes[0]
}

func (c *mqttClient) publish(topic string, payload []byte) {
	c.send(packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 1}, TopicName: topic, Payload: payload, PacketID: c.id()})
}

// next returns the next PUBLISH (skipping PUBACKs).
func (c *mqttClient) next() packets.Packet {
	c.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case pk, ok := <-c.in:
			if !ok {
				c.t.Fatal("connection closed")
			}
			if pk.FixedHeader.Type == packets.Publish {
				return pk
			}
		case <-deadline:
			c.t.Fatal("no PUBLISH within 5 s")
		}
	}
}

func (c *mqttClient) nothing(d time.Duration) {
	c.t.Helper()
	deadline := time.After(d)
	for {
		select {
		case pk, ok := <-c.in:
			if ok && pk.FixedHeader.Type == packets.Publish {
				c.t.Fatalf("unexpected publish on %s: %s", pk.TopicName, pk.Payload)
			}
			if !ok {
				return
			}
		case <-deadline:
			return
		}
	}
}

// command decodes a commands-topic payload's single entry.
func command(t *testing.T, pk packets.Packet) (string, map[string]any) {
	t.Helper()
	var arr []struct {
		Command string         `json:"command"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal(pk.Payload, &arr); err != nil || len(arr) != 1 {
		t.Fatalf("commands payload %s: %v", pk.Payload, err)
	}
	return arr[0].Command, arr[0].Details
}

func payload(t *testing.T, pk packets.Packet) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(pk.Payload, &v); err != nil {
		t.Fatalf("payload %s: %v", pk.Payload, err)
	}
	return v
}
