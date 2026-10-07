package mqtt

import (
	"fmt"
	"log/slog"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// closeWithin fails the test if Close does not return within d (instead of hanging it).
func closeWithin(t *testing.T, b *Broker, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_ = b.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("Close still blocked after %v", d)
	}
}

// openConns is the number of connections the broker's listeners are tracking.
func (b *Broker) openConns() int {
	n := 0
	for _, s := range b.conns {
		n += s.count()
	}
	return n
}

// A client that connected but never sent CONNECT sits in mochi's handshake read with no
// deadline, unknown to mochi's client registry, so mochi's own Close waited for it forever.
func TestCloseWithHalfOpenConnections(t *testing.T) {
	b := startBroker(t, false)
	tcp, err := net.Dial("tcp", b.TCPAddr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tcp.Close() })
	ws, _, err := (&websocket.Dialer{Subprotocols: []string{"mqtt"}}).Dial("ws://"+b.WSAddr(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	waitFor(t, func() bool { return b.openConns() == 2 })

	closeWithin(t, b, 5*time.Second)
	if n := b.openConns(); n != 0 {
		t.Fatalf("%d connections still open after Close", n)
	}
}

// mochi's Clients.GetByListener takes the registry's read lock twice (GetByListener, then
// Len inside it). A client registering or leaving between the two queues a writer, which
// blocks the second read lock while the first blocks the writer: Close deadlocked, and the
// whole broker with it. Close must not go through that path.
func TestCloseUnderClientChurn(t *testing.T) {
	for range 20 {
		b, err := New(testCreds, Options{TCPAddr: "127.0.0.1:0", WSAddr: "127.0.0.1:0", Logger: slog.New(slog.DiscardHandler)})
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		for j := range 4 {
			dial(t, b, connectOpts{clientID: fmt.Sprintf("c%d", j), user: "cc", pass: "s", clean: true, ws: j%2 == 1})
		}
		// Registry writers racing Close, as clients connecting or leaving do.
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for w := range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cl := b.srv.NewClient(nil, "tcp", fmt.Sprintf("churn%d", w), false)
				for {
					select {
					case <-stop:
						return
					default:
					}
					b.srv.Clients.Add(cl)
					b.srv.Clients.Delete(cl.ID)
					runtime.Gosched()
				}
			}()
		}
		func() {
			defer close(stop) // on a hang the deadlocked churners are abandoned
			closeWithin(t, b, 5*time.Second)
		}()
		wg.Wait()
	}
}
