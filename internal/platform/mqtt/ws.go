package mqtt

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mochi-mqtt/server/v2/listeners"
)

// wsListener serves MQTT over WebSocket on a pre-bound net.Listener. mochi's own websocket
// listener binds lazily inside Serve, which hides bind errors and the port-0 address.
// Its connections are tracked for shutdown like the TCP listener's (shutdown.go).
type wsListener struct {
	id        string
	ln        net.Listener // tracked
	raw       net.Listener
	set       *connSet
	b         *Broker
	srv       *http.Server
	log       *slog.Logger
	establish listeners.EstablishFn
	upgrader  websocket.Upgrader
	closed    atomic.Bool
}

func newWSListener(b *Broker, id string, ln net.Listener) *wsListener {
	set := newConnSet()
	l := &wsListener{
		id:  id,
		ln:  set.listen(ln),
		raw: ln,
		set: set,
		b:   b,
	}
	l.upgrader = websocket.Upgrader{Subprotocols: []string{"mqtt"}, CheckOrigin: l.checkOrigin}
	return l
}

// checkOrigin refuses (403) a browser page from another site, which could otherwise drive
// the broker from a LAN user's browser.
func (l *wsListener) checkOrigin(r *http.Request) bool {
	if originAllowed(r, l.b.opts.AllowedOrigins) {
		return true
	}
	l.log.Warn("mqtt websocket origin refused", "listener", l.id, "origin", r.Header.Get("Origin"), "remote", r.RemoteAddr)
	return false
}

// originAllowed admits a request without an Origin (nodes and the mobile app aren't
// browsers), and one whose Origin host is the request's own Host, localhost, a loopback,
// private or link-local IP, an mDNS .local name, or one of allowed (host names, or URLs).
func originAllowed(r *http.Request, allowed []string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == originHost(r.Host) || host == "localhost" || strings.HasSuffix(host, ".local") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
		return true
	}
	for _, a := range allowed {
		if a = strings.TrimSpace(a); strings.Contains(a, "://") {
			if au, err := url.Parse(a); err == nil {
				a = au.Host
			}
		}
		if a != "" && host == originHost(a) {
			return true
		}
	}
	return false
}

// originHost is the lower-cased host of a host[:port], without IPv6 brackets.
func originHost(hostport string) string {
	return strings.ToLower((&url.URL{Host: hostport}).Hostname())
}

func (l *wsListener) ID() string       { return l.id }
func (l *wsListener) Address() string  { return l.raw.Addr().String() }
func (l *wsListener) Protocol() string { return "ws" }

func (l *wsListener) Init(log *slog.Logger) error {
	l.log = log
	l.srv = &http.Server{Handler: http.HandlerFunc(l.handle), ReadHeaderTimeout: 10 * time.Second}
	return nil
}

func (l *wsListener) Serve(establish listeners.EstablishFn) {
	l.establish = establish
	if err := l.srv.Serve(l.ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !l.closed.Load() {
		l.log.Error("mqtt websocket listener failed", "listener", l.id, "error", err)
	}
}

func (l *wsListener) handle(w http.ResponseWriter, r *http.Request) {
	if !l.set.begin() {
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	defer l.set.end()
	// The hijacked connection is the tracked one the listener accepted, so shutdown can cut it.
	c, err := l.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	logConnEnd(l.log, l.id, l.b.serve(l.establish, l.id, &wsConn{Conn: c.UnderlyingConn(), c: c}))
}

// Close ignores mochi's closer (see shutdown.go): the listener disconnects its own clients.
func (l *wsListener) Close(listeners.CloseFn) {
	if l.closed.CompareAndSwap(false, true) {
		// http.Server.Close closes the listener and any connection not yet upgraded; it
		// doesn't wait. Upgraded (hijacked) ones are cut by shutdownListener.
		l.b.shutdownListener(l.id, l.set, func() { _ = l.srv.Close() })
	}
}

// wsConn presents a websocket as a byte stream: one MQTT write per binary message, reads
// spanning message boundaries. Adapted from mochi's listeners.wsConn (MIT).
type wsConn struct {
	net.Conn
	c *websocket.Conn
	r io.Reader // current message, or nil
}

var errNotBinary = errors.New("mqtt: websocket message is not binary")

func (ws *wsConn) Read(p []byte) (int, error) {
	if ws.r == nil {
		op, r, err := ws.c.NextReader()
		if err != nil {
			return 0, err
		}
		if op != websocket.BinaryMessage {
			return 0, errNotBinary
		}
		ws.r = r
	}
	n := 0
	for n < len(p) {
		m, err := ws.r.Read(p[n:])
		n += m
		if err != nil {
			ws.r = nil // end of this message (or a broken one)
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return n, err
		}
	}
	return n, nil
}

func (ws *wsConn) Write(p []byte) (int, error) {
	if err := ws.c.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (ws *wsConn) Close() error { return ws.Conn.Close() }
