package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
)

// baresip's ctrl_tcp: netstring-framed JSON both ways. A command is
// {"command","params","token"}; its answer {"response":true,"ok","data","token"}; between
// them baresip sends events {"event":true,"type":"CALL_ESTABLISHED","param":...}.

// event is one baresip event, the fields the lane reads.
type event struct {
	Type      string `json:"type"`
	Class     string `json:"class"`
	Direction string `json:"direction"`
	PeerURI   string `json:"peeruri"`
	ID        string `json:"id"`
	Param     string `json:"param"`
}

type ctrlResponse struct {
	OK    bool   `json:"ok"`
	Data  string `json:"data"`
	Token string `json:"token"`
}

// ctrl is a connection to one far end's ctrl_tcp. It keeps every event it receives, so a
// scenario can wait for one and then ask what else happened.
type ctrl struct {
	conn net.Conn

	mu      sync.Mutex
	events  []event
	pos     int // Wait looks from here on
	changed chan struct{}
	resps   map[string]chan ctrlResponse
	token   int
	err     error
}

func dialCtrl(ctx context.Context, addr string) (*ctrl, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("baresip ctrl_tcp %s: %w", addr, err)
	}
	c := &ctrl{conn: conn, changed: make(chan struct{}), resps: map[string]chan ctrlResponse{}}
	go c.read()
	return c, nil
}

func (c *ctrl) Close() error { return c.conn.Close() }

func (c *ctrl) read() {
	r := bufio.NewReader(c.conn)
	for {
		b, err := readNetstring(r)
		if err != nil {
			c.mu.Lock()
			c.err = err
			c.notify()
			c.mu.Unlock()
			return
		}
		var head struct {
			Event    bool `json:"event"`
			Response bool `json:"response"`
		}
		if json.Unmarshal(b, &head) != nil {
			continue
		}
		c.mu.Lock()
		switch {
		case head.Event:
			var e event
			json.Unmarshal(b, &e)
			c.events = append(c.events, e)
		case head.Response:
			var resp ctrlResponse
			json.Unmarshal(b, &resp)
			if ch := c.resps[resp.Token]; ch != nil {
				ch <- resp
				delete(c.resps, resp.Token)
			}
		}
		c.notify()
		c.mu.Unlock()
	}
}

// notify wakes every waiter; c.mu is held.
func (c *ctrl) notify() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// Command runs one baresip command (dial, hangup, accept, ...) and fails when baresip says no.
func (c *ctrl) Command(ctx context.Context, command, params string) error {
	c.mu.Lock()
	c.token++
	tok := strconv.Itoa(c.token)
	ch := make(chan ctrlResponse, 1)
	c.resps[tok] = ch
	c.mu.Unlock()
	b, _ := json.Marshal(map[string]string{"command": command, "params": params, "token": tok})
	if err := writeNetstring(c.conn, b); err != nil {
		return err
	}
	select {
	case r := <-ch:
		if !r.OK {
			return fmt.Errorf("baresip %s %s: %s", command, params, strings.TrimSpace(r.Data))
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("baresip %s %s: no answer: %w", command, params, ctx.Err())
	}
}

// Wait returns the next event of one of the types, after the one the last Wait returned.
func (c *ctrl) Wait(ctx context.Context, types ...string) (event, error) {
	for {
		c.mu.Lock()
		for ; c.pos < len(c.events); c.pos++ {
			for _, t := range types {
				if c.events[c.pos].Type == t {
					e := c.events[c.pos]
					c.pos++
					c.mu.Unlock()
					return e, nil
				}
			}
		}
		changed, err := c.changed, c.err
		c.mu.Unlock()
		if err != nil {
			return event{}, fmt.Errorf("waiting for %s: %w", strings.Join(types, "/"), err)
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return event{}, fmt.Errorf("no %s (saw %s)", strings.Join(types, "/"), strings.Join(c.Seen(), " "))
		}
	}
}

// Count is how many events of the type arrived so far.
func (c *ctrl) Count(typ string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func (c *ctrl) Saw(typ string) bool { return c.Count(typ) > 0 }

// Calls is how many distinct calls in the direction ("incoming", "outgoing") the far end had
// so far. baresip announces an auto-answered call without CALL_INCOMING, so this counts call
// ids, not events.
func (c *ctrl) Calls(direction string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := map[string]bool{}
	for _, e := range c.events {
		if e.Class == "call" && e.Direction == direction && e.ID != "" {
			ids[e.ID] = true
		}
	}
	return len(ids)
}

// Seen lists the call events so far, for a failure message.
func (c *ctrl) Seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var s []string
	for _, e := range c.events {
		if strings.HasPrefix(e.Type, "CALL_") && e.Type != "CALL_LOCAL_SDP" && e.Type != "CALL_REMOTE_SDP" {
			s = append(s, e.Type+strings.TrimSuffix("("+e.Param+")", "()"))
		}
	}
	return s
}

func writeNetstring(w io.Writer, b []byte) error {
	_, err := io.WriteString(w, strconv.Itoa(len(b))+":"+string(b)+",")
	return err
}

func readNetstring(r *bufio.Reader) ([]byte, error) {
	head, err := r.ReadString(':')
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(head[:len(head)-1]))
	if err != nil || n < 0 || n > 1<<20 {
		return nil, fmt.Errorf("netstring: bad length %q", head)
	}
	b := make([]byte, n+1)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	if b[n] != ',' {
		return nil, errors.New("netstring: no trailing comma")
	}
	return b[:n], nil
}
