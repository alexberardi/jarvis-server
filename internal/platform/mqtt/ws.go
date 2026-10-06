package mqtt

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mochi-mqtt/server/v2/listeners"
)

// wsListener serves MQTT over WebSocket on a pre-bound net.Listener. mochi's own websocket
// listener binds lazily inside Serve, which hides bind errors and the port-0 address.
type wsListener struct {
	id        string
	ln        net.Listener
	srv       *http.Server
	log       *slog.Logger
	establish listeners.EstablishFn
	upgrader  websocket.Upgrader
	closed    atomic.Bool
}

func newWSListener(id string, ln net.Listener) *wsListener {
	return &wsListener{
		id: id,
		ln: ln,
		upgrader: websocket.Upgrader{
			Subprotocols: []string{"mqtt"},
			CheckOrigin:  func(*http.Request) bool { return true },
		},
	}
}

func (l *wsListener) ID() string       { return l.id }
func (l *wsListener) Address() string  { return l.ln.Addr().String() }
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
	c, err := l.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	if err := l.establish(l.id, &wsConn{Conn: c.UnderlyingConn(), c: c}); err != nil {
		l.log.Debug("mqtt websocket client ended", "error", err)
	}
}

func (l *wsListener) Close(closeClients listeners.CloseFn) {
	if l.closed.CompareAndSwap(false, true) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = l.srv.Shutdown(ctx)
	}
	closeClients(l.id)
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
