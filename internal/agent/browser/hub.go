package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Errors a Call can return besides the extension's own (*CommandError).
var (
	ErrNotConnected  = errors.New("no browser with the Jarvis extension is connected")
	ErrDisconnected  = errors.New("the browser disconnected before answering")
	ErrNoSuchBrowser = errors.New("that browser is no longer connected")
)

// CommandError is a failure the extension reported (Code: invalid_argument, tab_not_found,
// restricted_page, no_media, blocked, unknown_command, failed).
type CommandError struct {
	Code    string
	Message string
}

func (e *CommandError) Error() string { return e.Message }

// ErrorCode is the extension's code (dictate.Code reads it when a confirmed fill or click fails).
func (e *CommandError) ErrorCode() string { return e.Code }

// BrowserInfo describes one connected browser.
type BrowserInfo struct {
	ID               int       `json:"id"`
	Key              string    `json:"key"` // stable name in tab ids: "firefox", "chrome", "chrome2"...
	Browser          string    `json:"browser"`
	Version          string    `json:"version,omitempty"`
	ExtensionVersion string    `json:"extension_version,omitempty"`
	Origin           string    `json:"origin,omitempty"`
	Focused          bool      `json:"focused"`
	LastFocus        time.Time `json:"last_focus,omitzero"`
	Connected        time.Time `json:"connected"`
	// WebsiteAccess is whether the extension may read and act on pages; nil when it doesn't say.
	WebsiteAccess *bool `json:"website_access,omitempty"`
}

// Hub is the agent's side: it accepts native hosts on the socket, tracks the browsers behind
// them, and runs commands in one of them.
type Hub struct {
	Log *slog.Logger
	Now func() time.Time // tests

	seq     atomic.Int64
	mu      sync.Mutex
	conns   map[int]*hubConn
	nextID  int
	changed chan struct{} // closed and replaced whenever the set of ready browsers changes

	recordSink func(RecordEvent) // SetRecordSink; guarded by mu

	// OnOutdated, when set before Serve, is called once per connection whose extension is older
	// than the one this build can give that browser (Outdated), with the newer version. It runs
	// on its own goroutine.
	OnOutdated func(b BrowserInfo, available string)
}

type hubConn struct {
	c       net.Conn
	wmu     sync.Mutex
	info    BrowserInfo // guarded by Hub.mu
	ready   bool        // hello received; guarded by Hub.mu
	pending map[int64]chan Message
	done    chan struct{}
}

// NewHub returns an empty hub.
func NewHub(log *slog.Logger) *Hub {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Hub{Log: log, conns: map[int]*hubConn{}, changed: make(chan struct{})}
}

func (h *Hub) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// Serve accepts connections until ctx ends or ln fails; it closes ln and every connection
// when ctx ends.
func (h *Hub) Serve(ctx context.Context, ln net.Listener) error {
	var wg sync.WaitGroup
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	defer wg.Wait()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.handle(ctx, c)
		}()
	}
}

func (h *Hub) handle(ctx context.Context, c net.Conn) {
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64<<10), maxFromExtension)
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if !sc.Scan() {
		return
	}
	var first Message
	if json.Unmarshal(sc.Bytes(), &first) != nil {
		return
	}
	switch first.Type {
	case "status":
		b, _ := json.Marshal(StatusReply{Browsers: h.Browsers()})
		_, _ = c.Write(append(b, '\n'))
		return
	case "host":
	default:
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	hc := &hubConn{c: c, pending: map[int64]chan Message{}, done: make(chan struct{})}
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	hc.info = BrowserInfo{ID: id, Origin: first.Origin, Connected: h.now()}
	h.conns[id] = hc
	h.mu.Unlock()
	h.Log.Debug("browser host connected", "id", id, "origin", first.Origin, "pid", first.PID)
	defer h.drop(id, hc)

	for sc.Scan() {
		var m Message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			h.Log.Warn("browser: bad message", "id", id, "err", err)
			continue
		}
		switch m.Type {
		case "hello":
			if m.Protocol != Protocol {
				h.Log.Warn("browser extension speaks another protocol version", "id", id, "protocol", m.Protocol, "want", Protocol)
			}
			h.mu.Lock()
			hc.info.Browser = m.Browser
			hc.info.Version = m.BrowserVersion
			hc.info.ExtensionVersion = m.ExtensionVersion
			hc.info.Focused = m.Focused
			if m.Focused {
				hc.info.LastFocus = h.now()
			}
			accessChanged := !sameAccess(hc.info.WebsiteAccess, m.WebsiteAccess)
			hc.info.WebsiteAccess = m.WebsiteAccess
			wasReady := hc.ready
			hc.ready = true
			if hc.info.Key == "" {
				hc.info.Key = h.keyFor(m.Browser, id)
			}
			if !wasReady || accessChanged {
				h.notifyLocked()
			}
			info := hc.info
			h.mu.Unlock()
			if !wasReady {
				if available, old := Outdated(info); old {
					h.Log.Warn("browser extension is out of date", "id", id, "browser", m.Browser, "extension", m.ExtensionVersion, "available", available)
					if h.OnOutdated != nil {
						go h.OnOutdated(info, available) // it may start Firefox; keep reading
					}
				}
				h.Log.Info("browser connected", "id", id, "browser", m.Browser, "version", m.BrowserVersion, "extension", m.ExtensionVersion,
					"website_access", accessText(m.WebsiteAccess))
			}
		case "event":
			if m.Event == "record" {
				h.mu.Lock()
				key := hc.info.Key
				h.mu.Unlock()
				h.deliverRecord(sc.Bytes(), key)
			}
			if m.Event == "focus" {
				h.mu.Lock()
				hc.info.Focused = m.Focused
				if m.Focused {
					hc.info.LastFocus = h.now()
				}
				h.mu.Unlock()
			}
			if m.Event == "access" && m.WebsiteAccess != nil {
				h.mu.Lock()
				changed := !sameAccess(hc.info.WebsiteAccess, m.WebsiteAccess)
				hc.info.WebsiteAccess = m.WebsiteAccess
				ready := hc.ready
				if changed && ready {
					h.notifyLocked()
				}
				h.mu.Unlock()
				if changed && ready {
					h.Log.Info("browser website access changed", "id", id, "website_access", accessText(m.WebsiteAccess))
				}
			}
		case "result":
			h.mu.Lock()
			ch := hc.pending[m.ID]
			delete(hc.pending, m.ID)
			h.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
}

func (h *Hub) drop(id int, hc *hubConn) {
	h.mu.Lock()
	wasReady := hc.ready
	delete(h.conns, id)
	close(hc.done)
	if wasReady {
		h.notifyLocked()
	}
	h.mu.Unlock()
	if wasReady {
		h.Log.Info("browser disconnected", "id", id, "browser", hc.info.Browser)
	}
}

func sameAccess(a, b *bool) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// accessText is website access as "on", "off" or "unknown".
func accessText(a *bool) string {
	switch {
	case a == nil:
		return "unknown"
	case *a:
		return "on"
	}
	return "off"
}

// notifyLocked fires Changed. Callers hold mu, so the change and its firing are one step: a
// Changed taken after a change can be seen waits for the next one.
func (h *Hub) notifyLocked() {
	close(h.changed)
	h.changed = make(chan struct{})
}

// keyFor names a browser for tab ids: the last word of its name, lowercased ("Google Chrome" ->
// "chrome"), numbered when that browser is connected twice (two profiles). Callers hold mu.
func (h *Hub) keyFor(name string, id int) string {
	words := strings.Fields(strings.ToLower(name))
	base := ""
	if len(words) > 0 {
		for _, r := range words[len(words)-1] {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				base += string(r)
			}
		}
	}
	base = strings.TrimRight(base, "0123456789")
	if base == "" {
		base = "browser"
	}
	taken := map[string]bool{}
	for cid, c := range h.conns {
		if cid != id {
			taken[c.info.Key] = true
		}
	}
	key := base
	for n := 2; taken[key]; n++ {
		key = base + strconv.Itoa(n)
	}
	return key
}

// Lookup returns the connected browser with key ("" = Default).
func (h *Hub) Lookup(key string) (BrowserInfo, error) {
	if key == "" {
		return h.Default()
	}
	bs := h.Browsers()
	if len(bs) == 0 {
		return BrowserInfo{}, ErrNotConnected
	}
	for _, b := range bs {
		if b.Key == key {
			return b, nil
		}
	}
	return BrowserInfo{}, ErrNoSuchBrowser
}

// Browsers lists the connected browsers (hello received), by id.
func (h *Hub) Browsers() []BrowserInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []BrowserInfo
	for _, c := range h.conns {
		if c.ready {
			out = append(out, c.info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Changed returns a channel that is closed the next time a browser connects or disconnects, or
// a connected browser's website access changes.
func (h *Hub) Changed() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.changed
}

// WebsiteAccess is the connected browsers' website access: off when any has it off, on when
// some have it on and none off, nil when none says (or none is connected).
func (h *Hub) WebsiteAccess() *bool {
	var out *bool
	for _, b := range h.Browsers() {
		if a := b.WebsiteAccess; a != nil && (out == nil || !*a) {
			v := *a
			out = &v
		}
	}
	return out
}

// Connected reports whether any browser is connected.
func (h *Hub) Connected() bool { return len(h.Browsers()) > 0 }

// WaitConnected waits until a browser is connected or ctx ends.
func (h *Hub) WaitConnected(ctx context.Context) bool {
	for {
		h.mu.Lock()
		ch := h.changed
		h.mu.Unlock()
		if h.Connected() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ch:
		}
	}
}

// Default is the browser commands go to when none is named: the one focused now (most recently
// if several claim it), else the one focused last, else the newest connection.
func (h *Hub) Default() (BrowserInfo, error) {
	bs := h.Browsers()
	if len(bs) == 0 {
		return BrowserInfo{}, ErrNotConnected
	}
	best := bs[0]
	rank := func(b BrowserInfo) (bool, time.Time, time.Time) { return b.Focused, b.LastFocus, b.Connected }
	for _, b := range bs[1:] {
		f1, l1, c1 := rank(b)
		f0, l0, c0 := rank(best)
		switch {
		case f1 != f0:
			if f1 {
				best = b
			}
		case !l1.Equal(l0):
			if l1.After(l0) {
				best = b
			}
		case c1.After(c0) || (c1.Equal(c0) && b.ID > best.ID):
			best = b
		}
	}
	return best, nil
}

// Call runs cmd in browser id (0: Default) and waits for its result until ctx ends.
func (h *Hub) Call(ctx context.Context, id int, cmd string, args map[string]any) (map[string]any, BrowserInfo, error) {
	if id == 0 {
		b, err := h.Default()
		if err != nil {
			return nil, BrowserInfo{}, err
		}
		id = b.ID
	}
	h.mu.Lock()
	hc := h.conns[id]
	if hc == nil || !hc.ready {
		none := len(h.conns) == 0
		h.mu.Unlock()
		if none {
			return nil, BrowserInfo{}, ErrNotConnected
		}
		return nil, BrowserInfo{}, ErrNoSuchBrowser
	}
	info := hc.info
	seq := h.seq.Add(1)
	ch := make(chan Message, 1)
	hc.pending[seq] = ch
	h.mu.Unlock()
	cancel := func() {
		h.mu.Lock()
		delete(hc.pending, seq)
		h.mu.Unlock()
	}
	b, err := json.Marshal(Message{Type: "command", ID: seq, Cmd: cmd, Args: args})
	if err == nil && len(b) > maxToExtension {
		err = fmt.Errorf("%w: command of %d bytes", ErrTooLarge, len(b))
	}
	if err != nil {
		cancel()
		return nil, info, err
	}
	hc.wmu.Lock()
	_ = hc.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = hc.c.Write(append(b, '\n'))
	hc.wmu.Unlock()
	if err != nil {
		cancel()
		return nil, info, ErrDisconnected
	}
	select {
	case m := <-ch:
		if !m.OK {
			code := m.Code
			if code == "" {
				code = "failed"
			}
			return nil, info, &CommandError{Code: code, Message: m.Error}
		}
		if m.Result == nil {
			m.Result = map[string]any{}
		}
		return m.Result, info, nil
	case <-hc.done:
		return nil, info, ErrDisconnected
	case <-ctx.Done():
		cancel()
		return nil, info, ctx.Err()
	}
}

// StatusReply answers a "status" connection: what a running agent sees.
type StatusReply struct {
	Browsers []BrowserInfo `json:"browsers"`
}

// QueryStatus asks the agent listening at path which browsers are connected.
func QueryStatus(path string) (StatusReply, error) {
	c, err := Dial(path)
	if err != nil {
		return StatusReply{}, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write(append(marshal(Message{Type: "status"}), '\n')); err != nil {
		return StatusReply{}, err
	}
	var r StatusReply
	if err := json.NewDecoder(c).Decode(&r); err != nil {
		return StatusReply{}, err
	}
	return r, nil
}
