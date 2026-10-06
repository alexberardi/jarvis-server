package mqtt

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mochi-mqtt/server/v2/packets"
)

// testClient is a minimal MQTT client speaking mochi's packet codec over a raw connection.
type testClient struct {
	t       *testing.T
	conn    net.Conn
	version byte
	in      chan packets.Packet // every packet received, closed when the connection ends

	wmu    sync.Mutex
	nextID uint16
}

type connectOpts struct {
	clientID, user, pass string
	clean                bool
	version              byte // 4 (3.1.1, default) or 5
	ws                   bool
}

// dial connects and returns the client plus the CONNACK. On a refused connect the client
// is still returned so the test can check the connection was closed.
func dial(t *testing.T, b *Broker, o connectOpts) (*testClient, packets.Packet) {
	t.Helper()
	if o.version == 0 {
		o.version = 4
	}
	var conn net.Conn
	if o.ws {
		d := websocket.Dialer{Subprotocols: []string{"mqtt"}}
		c, _, err := d.Dial("ws://"+b.WSAddr(), nil)
		if err != nil {
			t.Fatalf("ws dial: %v", err)
		}
		conn = &wsConn{Conn: c.UnderlyingConn(), c: c}
	} else {
		var err error
		if conn, err = net.Dial("tcp", b.TCPAddr()); err != nil {
			t.Fatalf("dial: %v", err)
		}
	}
	c := &testClient{t: t, conn: conn, version: o.version, in: make(chan packets.Packet, 64)}
	t.Cleanup(func() { _ = conn.Close() })
	go c.readLoop()

	c.send(packets.Packet{
		FixedHeader:     packets.FixedHeader{Type: packets.Connect},
		ProtocolVersion: o.version,
		Connect: packets.ConnectParams{
			ProtocolName:     []byte("MQTT"),
			Clean:            o.clean,
			Keepalive:        60,
			ClientIdentifier: o.clientID,
			UsernameFlag:     o.user != "",
			Username:         []byte(o.user),
			PasswordFlag:     o.pass != "",
			Password:         []byte(o.pass),
		},
	})
	return c, c.expect(packets.Connack)
}

func (c *testClient) readLoop() {
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
		pk.ProtocolVersion = c.version
		switch pk.FixedHeader.Type {
		case packets.Connack:
			err = pk.ConnackDecode(buf)
		case packets.Suback:
			err = pk.SubackDecode(buf)
		case packets.Puback:
			err = pk.PubackDecode(buf)
		case packets.Disconnect:
			err = pk.DisconnectDecode(buf)
		case packets.Publish:
			if err = pk.PublishDecode(buf); err == nil && pk.FixedHeader.Qos == 1 {
				c.send(packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Puback}, PacketID: pk.PacketID, ProtocolVersion: c.version})
			}
		}
		if err != nil {
			return
		}
		c.in <- pk
	}
}

func (c *testClient) send(pk packets.Packet) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	pk.ProtocolVersion = c.version
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
	case packets.Disconnect:
		err = pk.DisconnectEncode(&buf)
	}
	if err != nil {
		c.t.Errorf("encode: %v", err)
		return
	}
	_, _ = c.conn.Write(buf.Bytes()) // errors surface as a closed c.in
}

func (c *testClient) id() uint16 {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.nextID++
	return c.nextID
}

// expect returns the next packet, which must be of type typ.
func (c *testClient) expect(typ byte) packets.Packet {
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
	case <-time.After(3 * time.Second):
		c.t.Fatalf("timed out waiting for packet type %d", typ)
	}
	return packets.Packet{}
}

// expectClosed waits for the broker to drop the connection, skipping a DISCONNECT.
func (c *testClient) expectClosed() {
	c.t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case pk, ok := <-c.in:
			if !ok {
				return
			}
			if pk.FixedHeader.Type != packets.Disconnect {
				c.t.Fatalf("expected close, got packet type %d", pk.FixedHeader.Type)
			}
		case <-deadline:
			c.t.Fatal("connection was not closed")
		}
	}
}

// expectNothing asserts no packet arrives within d.
func (c *testClient) expectNothing(d time.Duration) {
	c.t.Helper()
	select {
	case pk, ok := <-c.in:
		if ok {
			c.t.Fatalf("unexpected packet type %d topic %q", pk.FixedHeader.Type, pk.TopicName)
		}
	case <-time.After(d):
	}
}

// subscribe subscribes at QoS 1 and returns the SUBACK reason code.
func (c *testClient) subscribe(filter string) byte {
	c.t.Helper()
	id := c.id()
	c.send(packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Subscribe, Qos: 1},
		PacketID:    id,
		Filters:     packets.Subscriptions{{Filter: filter, Qos: 1}},
	})
	ack := c.expect(packets.Suback)
	if ack.PacketID != id || len(ack.ReasonCodes) != 1 {
		c.t.Fatalf("bad suback %+v", ack)
	}
	return ack.ReasonCodes[0]
}

func (c *testClient) publish(topic string, payload []byte, qos byte) {
	pk := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: qos},
		TopicName:   topic,
		Payload:     payload,
	}
	if qos > 0 {
		pk.PacketID = c.id()
	}
	c.send(pk)
}

func (c *testClient) disconnect() {
	c.send(packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Disconnect}})
	_ = c.conn.Close()
}
