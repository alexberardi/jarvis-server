//go:build contract

package contract

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A minimal MQTT 3.1.1 client: just enough to play a fake node on the target's broker
// (docs/cc/05 §2.4). It connects, subscribes at QoS 1, records every PUBLISH it receives with
// its fixed-header flags, acks QoS 1 deliveries, and can publish a response. Written against
// the spec in raw bytes so the suite stays dependency-free and black-box.

const (
	EnvMQTTPort     = "JARVIS_CONTRACT_MQTT_PORT"     // broker port on the target host (MBP: 1884)
	EnvMQTTUsername = "JARVIS_CONTRACT_MQTT_USERNAME" // the broker credential (CC's MQTT_USERNAME)
	EnvMQTTPassword = "JARVIS_CONTRACT_MQTT_PASSWORD" // ...and MQTT_PASSWORD
)

const (
	mqttConnect     = 1
	mqttConnack     = 2
	mqttPublish     = 3
	mqttPuback      = 4
	mqttSubscribe   = 8
	mqttSuback      = 9
	mqttPingreq     = 12
	mqttPingresp    = 13
	mqttDisconnect  = 14
	mqttDialTimeout = 5 * time.Second
)

// MQTTMsg is one PUBLISH delivered to the fake node.
type MQTTMsg struct {
	Topic   string
	QoS     byte
	Retain  bool
	Dup     bool
	Payload []byte
}

// JSON decodes the payload with json.Number, like Resp.JSON.
func (m MQTTMsg) JSON() (any, error) {
	dec := json.NewDecoder(bytes.NewReader(m.Payload))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// MQTTClient is the fake node's broker connection.
type MQTTClient struct {
	conn   net.Conn
	wmu    sync.Mutex
	mu     sync.Mutex
	msgs   []MQTTMsg
	notify chan struct{}
	acks   map[uint16]chan byte // SUBACK return code / PUBACK
	nextID uint16
	done   chan struct{}
	err    error
}

// mqttAddr is the broker address, or "" when the target has no broker configured.
func (tg *Target) mqttAddr() string {
	port := os.Getenv(EnvMQTTPort)
	if port == "" {
		return ""
	}
	return net.JoinHostPort(tg.Host, port)
}

// NeedMQTT skips the test unless the broker port is configured.
func (tg *Target) NeedMQTT(t testing.TB) string {
	t.Helper()
	addr := tg.mqttAddr()
	if addr == "" {
		t.Skipf("contract: %s is not set (the target's MQTT broker port)", EnvMQTTPort)
	}
	if _, err := strconv.Atoi(os.Getenv(EnvMQTTPort)); err != nil {
		t.Fatalf("contract: %s=%q is not a port", EnvMQTTPort, os.Getenv(EnvMQTTPort))
	}
	return addr
}

// DialMQTT connects with clean_session=true (so nothing lingers on the broker after the run)
// and the broker credential from the environment; empty means anonymous, which is also what
// GET /node/mqtt-credentials hands out when broker auth is off.
func DialMQTT(t testing.TB, clientID string) *MQTTClient {
	t.Helper()
	tg := T(t)
	addr := tg.NeedMQTT(t)
	c, err := dialMQTT(addr, clientID, os.Getenv(EnvMQTTUsername), os.Getenv(EnvMQTTPassword))
	if err != nil {
		t.Fatalf("mqtt dial %s: %v", addr, err)
	}
	t.Cleanup(c.Close)
	return c
}

// DialMQTTNode connects as node n's fake client ("jarvis-node-{id}"). Python's broker takes
// the shared credential from the environment; jarvisd gives every node its own credential
// and ACLs (D4), handed out by GET /node/mqtt-credentials, so the fake node fetches its own.
func DialMQTTNode(t testing.TB, n *CCNode) *MQTTClient {
	t.Helper()
	if !Jarvisd() {
		return DialMQTT(t, "jarvis-node-"+n.ID)
	}
	tg := T(t)
	addr := tg.NeedMQTT(t)
	cr := tg.Get(t, CommandCenter, "/api/v0/node/mqtt-credentials", n.APIKeyH()).ExpectStatus(200).Object()
	user, _ := cr["username"].(string)
	pass, _ := cr["password"].(string)
	c, err := dialMQTT(addr, "jarvis-node-"+n.ID, user, pass)
	if err != nil {
		t.Fatalf("mqtt dial %s as node %s: %v", addr, n.ID, err)
	}
	t.Cleanup(c.Close)
	return c
}

func dialMQTT(addr, clientID, user, pass string) (*MQTTClient, error) {
	conn, err := net.DialTimeout("tcp", addr, mqttDialTimeout)
	if err != nil {
		return nil, err
	}
	// CONNECT: protocol name "MQTT", level 4, flags, keepalive 60.
	var vh bytes.Buffer
	writeStr(&vh, "MQTT")
	vh.WriteByte(4)
	flags := byte(0x02) // clean session
	if user != "" {
		flags |= 0x80
		if pass != "" {
			flags |= 0x40
		}
	}
	vh.WriteByte(flags)
	vh.Write([]byte{0, 60})
	writeStr(&vh, clientID)
	if user != "" {
		writeStr(&vh, user)
		if pass != "" {
			writeStr(&vh, pass)
		}
	}
	if _, err := conn.Write(packet(mqttConnect<<4, vh.Bytes())); err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(mqttDialTimeout))
	r := bufio.NewReader(conn)
	hdr, body, err := readPacket(r)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNACK: %w", err)
	}
	if hdr>>4 != mqttConnack || len(body) != 2 {
		conn.Close()
		return nil, fmt.Errorf("want CONNACK, got type %d", hdr>>4)
	}
	if body[1] != 0 {
		conn.Close()
		return nil, fmt.Errorf("CONNACK return code %d (4/5 = bad credentials / not authorised)", body[1])
	}
	_ = conn.SetReadDeadline(time.Time{})
	c := &MQTTClient{
		conn:   conn,
		notify: make(chan struct{}, 1),
		acks:   map[uint16]chan byte{},
		done:   make(chan struct{}),
	}
	go c.readLoop(r)
	go c.pingLoop()
	return c, nil
}

func (c *MQTTClient) write(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(mqttDialTimeout))
	_, err := c.conn.Write(b)
	return err
}

func (c *MQTTClient) packetID() (uint16, chan byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	if c.nextID == 0 {
		c.nextID = 1
	}
	ch := make(chan byte, 1)
	c.acks[c.nextID] = ch
	return c.nextID, ch
}

func (c *MQTTClient) waitAck(ch chan byte, what string) (byte, error) {
	select {
	case code := <-ch:
		return code, nil
	case <-c.done:
		return 0, fmt.Errorf("%s: connection closed: %v", what, c.err)
	case <-time.After(mqttDialTimeout):
		return 0, fmt.Errorf("%s: no ack within %s", what, mqttDialTimeout)
	}
}

// Subscribe subscribes to filter at QoS 1 and waits for the SUBACK.
func (c *MQTTClient) Subscribe(t testing.TB, filter string) {
	t.Helper()
	id, ch := c.packetID()
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, id)
	writeStr(&b, filter)
	b.WriteByte(1)
	if err := c.write(packet(mqttSubscribe<<4|0x02, b.Bytes())); err != nil {
		t.Fatalf("mqtt subscribe %s: %v", filter, err)
	}
	code, err := c.waitAck(ch, "SUBACK")
	if err != nil {
		t.Fatalf("mqtt subscribe %s: %v", filter, err)
	}
	if code != 1 {
		t.Fatalf("mqtt subscribe %s: granted QoS %#x, want 1", filter, code)
	}
}

// Publish sends payload at QoS 1, retain=false, and waits for the PUBACK. This is how a node
// answers the two request/response verbs (command-data, context/query).
func (c *MQTTClient) Publish(t testing.TB, topic string, payload []byte) {
	t.Helper()
	id, ch := c.packetID()
	var b bytes.Buffer
	writeStr(&b, topic)
	_ = binary.Write(&b, binary.BigEndian, id)
	b.Write(payload)
	if err := c.write(packet(mqttPublish<<4|0x02, b.Bytes())); err != nil {
		t.Fatalf("mqtt publish %s: %v", topic, err)
	}
	if _, err := c.waitAck(ch, "PUBACK"); err != nil {
		t.Fatalf("mqtt publish %s: %v", topic, err)
	}
}

// Next waits for the next message matching pred that has not been taken yet, and removes it
// from the buffer. It fails the test after timeout, listing what did arrive.
func (c *MQTTClient) Next(t testing.TB, timeout time.Duration, pred func(MQTTMsg) bool) MQTTMsg {
	t.Helper()
	deadline := time.After(timeout)
	for {
		c.mu.Lock()
		for i, m := range c.msgs {
			if pred(m) {
				c.msgs = append(c.msgs[:i], c.msgs[i+1:]...)
				c.mu.Unlock()
				return m
			}
		}
		c.mu.Unlock()
		select {
		case <-c.notify:
		case <-c.done:
			t.Fatalf("mqtt: connection closed while waiting: %v", c.err)
		case <-deadline:
			t.Fatalf("mqtt: no matching message within %s; buffered topics: %v", timeout, c.Topics())
		}
	}
}

// NextTopic is Next for an exact topic.
func (c *MQTTClient) NextTopic(t testing.TB, timeout time.Duration, topic string) MQTTMsg {
	t.Helper()
	return c.Next(t, timeout, func(m MQTTMsg) bool { return m.Topic == topic })
}

// Drain discards buffered messages, waiting quiet first so stragglers are included.
func (c *MQTTClient) Drain(quiet time.Duration) []MQTTMsg {
	time.Sleep(quiet)
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.msgs
	c.msgs = nil
	return out
}

// Topics lists the buffered (untaken) topics, for failure messages.
func (c *MQTTClient) Topics() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.msgs))
	for i, m := range c.msgs {
		out[i] = m.Topic
	}
	return out
}

// Close sends DISCONNECT and closes the socket.
func (c *MQTTClient) Close() {
	_ = c.write([]byte{mqttDisconnect << 4, 0})
	_ = c.conn.Close()
	<-c.done
}

func (c *MQTTClient) pingLoop() {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-tick.C:
			_ = c.write([]byte{mqttPingreq << 4, 0})
		}
	}
}

func (c *MQTTClient) readLoop(r *bufio.Reader) {
	defer close(c.done)
	for {
		hdr, body, err := readPacket(r)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				c.err = err
			}
			return
		}
		switch hdr >> 4 {
		case mqttPublish:
			qos := (hdr >> 1) & 0x03
			if len(body) < 2 {
				c.err = errors.New("short PUBLISH")
				return
			}
			tl := int(binary.BigEndian.Uint16(body))
			if len(body) < 2+tl {
				c.err = errors.New("short PUBLISH topic")
				return
			}
			topic := string(body[2 : 2+tl])
			rest := body[2+tl:]
			if qos > 0 {
				if len(rest) < 2 {
					c.err = errors.New("short PUBLISH packet id")
					return
				}
				id := rest[:2]
				rest = rest[2:]
				if qos == 1 {
					_ = c.write([]byte{mqttPuback << 4, 2, id[0], id[1]})
				}
			}
			m := MQTTMsg{Topic: topic, QoS: qos, Retain: hdr&0x01 != 0, Dup: hdr&0x08 != 0,
				Payload: append([]byte(nil), rest...)}
			c.mu.Lock()
			c.msgs = append(c.msgs, m)
			c.mu.Unlock()
			select {
			case c.notify <- struct{}{}:
			default:
			}
		case mqttSuback, mqttPuback:
			if len(body) < 2 {
				continue
			}
			id := binary.BigEndian.Uint16(body)
			code := byte(0)
			if hdr>>4 == mqttSuback && len(body) >= 3 {
				code = body[2]
			}
			c.mu.Lock()
			ch := c.acks[id]
			delete(c.acks, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- code
			}
		case mqttPingresp:
		}
	}
}

func readPacket(r *bufio.Reader) (byte, []byte, error) {
	hdr, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	n, mult := 0, 1
	for i := 0; ; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		n += int(b&0x7f) * mult
		if b&0x80 == 0 {
			break
		}
		if i == 3 {
			return 0, nil, errors.New("malformed remaining length")
		}
		mult *= 128
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return hdr, body, nil
}

func packet(hdr byte, body []byte) []byte {
	out := []byte{hdr}
	n := len(body)
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			break
		}
	}
	return append(out, body...)
}

func writeStr(b *bytes.Buffer, s string) {
	_ = binary.Write(b, binary.BigEndian, uint16(len(s)))
	b.WriteString(s)
}

// ExpectMQTT asserts a delivered message's flags (QoS 1, retain=false: every CC publish) and
// its JSON payload shape, and returns the decoded payload.
func ExpectMQTT(t testing.TB, m MQTTMsg, shape Matcher) any {
	t.Helper()
	if m.QoS != 1 || m.Retain {
		t.Fatalf("mqtt %s: want QoS 1 retain=false, got QoS %d retain=%v", m.Topic, m.QoS, m.Retain)
	}
	v, err := m.JSON()
	if err != nil {
		t.Fatalf("mqtt %s: payload is not JSON: %v\npayload: %s", m.Topic, err, m.Payload)
	}
	if errs := shape.Match("$", v); len(errs) > 0 {
		t.Fatalf("mqtt %s: payload shape mismatch:\n  %s\npayload: %s", m.Topic, strings.Join(errs, "\n  "), m.Payload)
	}
	return v
}
