package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// pipeHost runs RunHost with in-memory pipes for the browser side.
type pipeHost struct {
	toHost   *io.PipeWriter // what the extension writes
	fromHost *io.PipeReader // what the extension reads
	done     chan error
}

func runPipeHost(t *testing.T, dial func() (net.Conn, error)) *pipeHost {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &pipeHost{toHost: inW, fromHost: outR, done: make(chan error, 1)}
	go func() {
		p.done <- RunHost(context.Background(), inR, outW, HostConfig{Origin: FirefoxExtensionID, Dial: dial, Retry: 20 * time.Millisecond})
		_ = outW.Close()
	}()
	t.Cleanup(func() { _ = inW.Close() })
	return p
}

func (p *pipeHost) extSend(t *testing.T, m Message) {
	t.Helper()
	b, _ := json.Marshal(m)
	if err := WriteNative(p.toHost, b, maxFromExtension); err != nil {
		t.Fatal(err)
	}
}

func (p *pipeHost) extRead(t *testing.T) Message {
	t.Helper()
	type r struct {
		b   []byte
		err error
	}
	ch := make(chan r, 1)
	go func() { b, err := ReadNative(p.fromHost, maxToExtension); ch <- r{b, err} }()
	select {
	case x := <-ch:
		if x.err != nil {
			t.Fatal(x.err)
		}
		var m Message
		if err := json.Unmarshal(x.b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("nothing reached the extension")
	}
	return Message{}
}

// fakeAgent accepts host connections and records their lines.
type fakeAgent struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
	lines chan Message
}

func newFakeAgent(t *testing.T, path string) *fakeAgent {
	t.Helper()
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	a := &fakeAgent{ln: ln, lines: make(chan Message, 64)}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			a.mu.Lock()
			a.conns = append(a.conns, c)
			a.mu.Unlock()
			go func() {
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					var m Message
					_ = json.Unmarshal(sc.Bytes(), &m)
					a.lines <- m
				}
			}()
		}
	}()
	t.Cleanup(func() { a.stop() })
	return a
}

func (a *fakeAgent) stop() {
	_ = a.ln.Close()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.conns {
		_ = c.Close()
	}
	a.conns = nil
}

func (a *fakeAgent) next(t *testing.T) Message {
	t.Helper()
	select {
	case m := <-a.lines:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("nothing reached the agent")
	}
	return Message{}
}

func (a *fakeAgent) send(t *testing.T, m Message) {
	t.Helper()
	b, _ := json.Marshal(m)
	a.mu.Lock()
	c := a.conns[len(a.conns)-1]
	a.mu.Unlock()
	if _, err := c.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

func TestHostRelaysBothWays(t *testing.T) {
	path := shortSocket(t)
	a := newFakeAgent(t, path)
	p := runPipeHost(t, func() (net.Conn, error) { return Dial(path) })
	if m := a.next(t); m.Type != "host" || m.Origin != FirefoxExtensionID || m.PID == 0 {
		t.Fatalf("intro %+v", m)
	}
	p.extSend(t, Message{Type: "hello", Browser: "Firefox", Protocol: 1})
	if m := a.next(t); m.Type != "hello" || m.Browser != "Firefox" {
		t.Fatalf("hello %+v", m)
	}
	a.send(t, Message{Type: "command", ID: 7, Cmd: "list_tabs"})
	if m := p.extRead(t); m.Type != "command" || m.ID != 7 || m.Cmd != "list_tabs" {
		t.Fatalf("command %+v", m)
	}
	p.extSend(t, Message{Type: "result", ID: 7, OK: true, Result: map[string]any{"n": 1.0}})
	if m := a.next(t); m.Type != "result" || m.ID != 7 || m.Result["n"] != 1.0 {
		t.Fatalf("result %+v", m)
	}
	// The browser closing the port is a clean exit.
	_ = p.toHost.Close()
	select {
	case err := <-p.done:
		if err != nil {
			t.Fatalf("exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("host didn't exit on EOF")
	}
}

func TestHostWaitsForAgentAndReplaysHello(t *testing.T) {
	path := shortSocket(t)
	var mu sync.Mutex
	up := false
	dial := func() (net.Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		if !up {
			return nil, errors.New("agent not running")
		}
		return Dial(path)
	}
	p := runPipeHost(t, dial)
	// The extension says hello while the agent is down.
	p.extSend(t, Message{Type: "hello", Browser: "Chromium", Protocol: 1})
	p.extSend(t, Message{Type: "event", Event: "focus", Focused: true}) // dropped: no agent
	time.Sleep(60 * time.Millisecond)
	a := newFakeAgent(t, path)
	mu.Lock()
	up = true
	mu.Unlock()
	if m := a.next(t); m.Type != "host" {
		t.Fatalf("intro %+v", m)
	}
	if m := a.next(t); m.Type != "hello" || m.Browser != "Chromium" {
		t.Fatalf("replayed hello %+v", m)
	}
	// The agent restarts: the host reconnects and replays the hello again.
	a.stop()
	a2 := newFakeAgent(t, path)
	if m := a2.next(t); m.Type != "host" {
		t.Fatalf("intro after restart %+v", m)
	}
	if m := a2.next(t); m.Type != "hello" || m.Browser != "Chromium" {
		t.Fatalf("hello after restart %+v", m)
	}
}

func TestHostDropsMalformedAndOversize(t *testing.T) {
	path := shortSocket(t)
	a := newFakeAgent(t, path)
	p := runPipeHost(t, func() (net.Conn, error) { return Dial(path) })
	a.next(t) // intro
	if err := WriteNative(p.toHost, []byte("{not json"), maxFromExtension); err != nil {
		t.Fatal(err)
	}
	// Pretty-printed JSON from a browser is compacted onto one line.
	if err := WriteNative(p.toHost, []byte("{\n  \"type\": \"event\",\n  \"event\": \"focus\"\n}"), maxFromExtension); err != nil {
		t.Fatal(err)
	}
	if m := a.next(t); m.Type != "event" || m.Event != "focus" {
		t.Fatalf("got %+v", m)
	}
	// An agent line over the extension's 1 MiB limit is dropped, not sent truncated.
	big := make([]byte, maxToExtension+10)
	for i := range big {
		big[i] = 'x'
	}
	a.send(t, Message{Type: "command", ID: 1, Cmd: string(big)})
	a.send(t, Message{Type: "command", ID: 2, Cmd: "list_tabs"})
	if m := p.extRead(t); m.ID != 2 {
		t.Fatalf("got id %d, want the oversize command dropped", m.ID)
	}
}
