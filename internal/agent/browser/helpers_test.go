package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// shortSocket is a socket path short enough for every OS (t.TempDir is too long on macOS).
func shortSocket(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "jb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return filepath.Join(d, "s", "b.sock")
}

// startHub serves a hub on a fresh socket until the test ends.
func startHub(t *testing.T) (*Hub, string) {
	t.Helper()
	path := shortSocket(t)
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHub(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = h.Serve(ctx, ln) }()
	t.Cleanup(func() { cancel(); <-done })
	return h, path
}

// fakeExt plays a native host + extension on the agent's socket.
type fakeExt struct {
	c      net.Conn
	mu     sync.Mutex
	cmds   []Message
	handle func(Message) Message
}

func (f *fakeExt) send(m Message) {
	b, _ := json.Marshal(m)
	f.mu.Lock()
	defer f.mu.Unlock()
	_, _ = f.c.Write(append(b, '\n'))
}

func (f *fakeExt) received() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Message(nil), f.cmds...)
}

// connectExt connects a fake browser and waits until the hub lists it. handle answers
// commands (nil: never answers).
func connectExt(t *testing.T, h *Hub, path string, browser string, focused bool, handle func(Message) Message) *fakeExt {
	t.Helper()
	before := len(h.Browsers())
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeExt{c: c, handle: handle}
	t.Cleanup(func() { _ = c.Close() })
	f.send(Message{Type: "host", Origin: "chrome-extension://" + ChromeDevExtensionID + "/"})
	f.send(Message{Type: "hello", Protocol: Protocol, Browser: browser, BrowserVersion: "1", ExtensionVersion: "0.1.0", Focused: focused})
	go func() {
		sc := bufio.NewScanner(c)
		for sc.Scan() {
			var m Message
			if json.Unmarshal(sc.Bytes(), &m) != nil {
				continue
			}
			f.mu.Lock()
			f.cmds = append(f.cmds, m)
			f.mu.Unlock()
			if f.handle != nil {
				r := f.handle(m)
				r.Type, r.ID = "result", m.ID
				f.send(r)
			}
		}
	}()
	waitFor(t, func() bool { return len(h.Browsers()) == before+1 })
	return f
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second) // a hang guard
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in 10s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func ok(result map[string]any) Message { return Message{OK: true, Result: result} }
