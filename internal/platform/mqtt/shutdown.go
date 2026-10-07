package mqtt

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

// Shutdown bounds. Close must never hang (it runs on jarvisd's shutdown path), so every wait
// in it is bounded, and connections are cut rather than waited for.
const (
	// disconnectGrace is how long clients get to be sent DISCONNECT before their
	// connections are cut.
	disconnectGrace = 2 * time.Second
	// drainTimeout bounds the wait for a listener's connection handlers once cut.
	drainTimeout = 3 * time.Second
	// closeTimeout bounds the whole of Broker.Close.
	closeTimeout = 10 * time.Second
)

// Why the listeners don't use mochi's shutdown (mochi v2.7.9):
//
//   - mochi's Close disconnects a listener's clients via Clients.GetByListener, which takes
//     the registry's read lock and then again inside Len. A client registering or leaving
//     between the two queues a writer, which blocks the second read lock while the first
//     blocks the writer: Close deadlocked, and every later registry user with it.
//   - It disconnects only clients already in the registry. A connection still in the CONNECT
//     handshake (which has no read deadline) or mid-registration is never closed, and
//     Listeners.CloseAll then waits for it on ClientsWg forever.
//   - mochi's Net listener starts each connection on a goroutine that calls ClientsWg.Add
//     itself, racing CloseAll's ClientsWg.Wait (the race detector flags it).
//
// So each listener here owns its connections (connSet): Close stops accepting, sends
// DISCONNECT through a single-lock registry snapshot (bounded), cuts every accepted
// connection, and waits (bounded) for its handlers. By the time mochi's CloseAll reaches
// ClientsWg.Wait, no Add can still be in flight and nothing is left to wait for.

// connSet tracks the connections a listener accepted and the handlers serving them.
type connSet struct {
	mu       sync.Mutex
	closed   bool
	conns    map[*trackedConn]struct{}
	handlers sync.WaitGroup
}

func newConnSet() *connSet { return &connSet{conns: map[*trackedConn]struct{}{}} }

// listen wraps ln so every connection it accepts is tracked until closed.
func (s *connSet) listen(ln net.Listener) net.Listener { return &trackedListener{Listener: ln, set: s} }

// begin registers a connection handler; false once the set is shutting down.
func (s *connSet) begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.handlers.Add(1)
	return true
}

func (s *connSet) end() { s.handlers.Done() }

// stop refuses new connections and handlers.
func (s *connSet) stop() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

// cut closes every tracked connection, then waits up to d for the handlers to return.
func (s *connSet) cut(d time.Duration) bool {
	s.mu.Lock()
	conns := make([]*trackedConn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	return waitTimeout(s.handlers.Wait, d)
}

func (s *connSet) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// waitTimeout runs wait and reports whether it returned within d.
func waitTimeout(wait func(), d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

type trackedListener struct {
	net.Listener
	set *connSet
}

func (l *trackedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tc := &trackedConn{Conn: c, set: l.set}
	l.set.mu.Lock()
	defer l.set.mu.Unlock()
	if l.set.closed {
		_ = c.Close()
		return nil, net.ErrClosed
	}
	l.set.conns[tc] = struct{}{}
	return tc, nil
}

type trackedConn struct {
	net.Conn
	set  *connSet
	once sync.Once
}

func (c *trackedConn) Close() error {
	err := net.ErrClosed
	c.once.Do(func() {
		c.set.mu.Lock()
		delete(c.set.conns, c)
		c.set.mu.Unlock()
		err = c.Conn.Close()
	})
	return err
}

// shutdownListener is the listener side of Broker.Close: stop accepting (stopAccept), send
// clients DISCONNECT (bounded), cut every connection, wait for the handlers (bounded).
func (b *Broker) shutdownListener(id string, set *connSet, stopAccept func()) {
	set.stop()
	stopAccept()
	if !waitTimeout(func() { b.disconnectClients(id) }, disconnectGrace) {
		b.log.Warn("mqtt: clients did not take DISCONNECT in time; cutting connections", "listener", id)
	}
	if !set.cut(drainTimeout) {
		b.log.Error("mqtt: connection handlers still running after shutdown", "listener", id)
	}
}

// disconnectClients sends DISCONNECT (server shutting down) to the listener's live clients.
// It snapshots the registry with GetAll, a single read lock, never GetByListener.
func (b *Broker) disconnectClients(listener string) {
	for _, cl := range b.srv.Clients.GetAll() {
		if cl.Net.Listener == listener && !cl.Closed() {
			_ = b.srv.DisconnectClient(cl, packets.ErrServerShuttingDown)
		}
	}
}

// tcpListener serves MQTT over TCP on a pre-bound listener, in place of mochi's Net.
type tcpListener struct {
	id   string
	ln   net.Listener // tracked
	raw  net.Listener
	set  *connSet
	b    *Broker
	log  *slog.Logger
	once sync.Once
}

func newTCPListener(b *Broker, id string, ln net.Listener) *tcpListener {
	set := newConnSet()
	return &tcpListener{id: id, ln: set.listen(ln), raw: ln, set: set, b: b}
}

func (l *tcpListener) ID() string                  { return l.id }
func (l *tcpListener) Address() string             { return l.raw.Addr().String() }
func (l *tcpListener) Protocol() string            { return l.raw.Addr().Network() }
func (l *tcpListener) Init(log *slog.Logger) error { l.log = log; return nil }

func (l *tcpListener) Serve(establish listeners.EstablishFn) {
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			return
		}
		if !l.set.begin() {
			_ = conn.Close()
			return
		}
		go func() {
			defer l.set.end()
			logConnEnd(l.log, l.id, establish(l.id, conn))
		}()
	}
}

// Close ignores mochi's closer (see the note above): the listener disconnects its own clients.
func (l *tcpListener) Close(listeners.CloseFn) {
	l.once.Do(func() {
		l.b.shutdownListener(l.id, l.set, func() { _ = l.ln.Close() })
	})
}

// logConnEnd logs a connection that ended with an error: a plain hang-up at Debug, anything
// else (bad CONNECT, refused credentials, protocol errors) at Warn.
func logConnEnd(log *slog.Logger, listener string, err error) {
	if err == nil || log == nil {
		return
	}
	if isHangUp(err) {
		log.Debug("mqtt client disconnected", "listener", listener, "error", err)
		return
	}
	log.Warn("mqtt connection ended with an error", "listener", listener, "error", err)
}

func isHangUp(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	return isHangUpText(err.Error())
}

func isHangUpText(s string) bool {
	return strings.HasSuffix(s, "EOF") || strings.Contains(s, "use of closed network connection") ||
		strings.Contains(s, "connection reset by peer")
}
