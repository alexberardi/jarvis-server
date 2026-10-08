// Package logging sets up jarvisd's slog logger. Records go to stderr and, once the logs
// module provides a Sink, into the database in batches (replacing jarvis-log-client's HTTP
// shipping to jarvis-logs). A slow or failing sink never blocks a caller: when the buffer is
// full, records are dropped and counted.
package logging

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Record is one log entry handed to a Sink.
type Record struct {
	Time    time.Time
	Level   slog.Level
	Message string
	Source  string // the "module" attribute, or "jarvisd"
	Attrs   map[string]any
}

// ErrNotReady is a Sink's answer before it can store anything (its module isn't wired yet):
// the shipper keeps the batch and tries again on the next flush.
var ErrNotReady = errors.New("logging: sink not ready")

// Sink stores batches of records (the logs module's table).
type Sink interface {
	Write(ctx context.Context, batch []Record) error
}

// ParseLevel maps debug|info|warn|error to a level; anything else is info.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// Shipper batches records to a Sink in the background.
type Shipper struct {
	ch       chan Record
	dropped  atomic.Int64
	batch    int
	interval time.Duration
	done     chan struct{}
	stopOnce sync.Once
	// mu orders offers against Close: goroutines still logging during shutdown (mDNS,
	// engines) must not send on the closed channel. Their records still reach stderr.
	mu     sync.RWMutex
	closed bool
}

// NewShipper starts shipping to sink. Call Close to flush and stop.
func NewShipper(sink Sink, buffer, batch int, interval time.Duration) *Shipper {
	s := &Shipper{ch: make(chan Record, buffer), batch: batch, interval: interval, done: make(chan struct{})}
	go s.run(sink)
	return s
}

// Dropped reports how many records were discarded because the buffer was full.
func (s *Shipper) Dropped() int64 { return s.dropped.Load() }

func (s *Shipper) offer(r Record) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		s.dropped.Add(1)
		return
	}
	select {
	case s.ch <- r:
	default:
		s.dropped.Add(1)
	}
}

func (s *Shipper) run(sink Sink) {
	defer close(s.done)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	buf := make([]Record, 0, s.batch)
	flush := func() {
		if len(buf) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := sink.Write(ctx, buf) // nowhere to report a logging failure but stderr, which has the record already
		cancel()
		if errors.Is(err, ErrNotReady) {
			// Startup: hold the records, bounded by the buffer size (oldest dropped).
			if over := len(buf) - cap(s.ch); over > 0 {
				s.dropped.Add(int64(over))
				buf = append(buf[:0], buf[over:]...)
			}
			return
		}
		buf = make([]Record, 0, s.batch)
	}
	for {
		select {
		case r, ok := <-s.ch:
			if !ok {
				flush()
				return
			}
			buf = append(buf, r)
			if len(buf) >= s.batch {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}

// Close flushes buffered records and stops the shipper.
func (s *Shipper) Close() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.ch)
		s.mu.Unlock()
	})
	<-s.done
}

// shuttingDown is set once the process has begun to stop (SetShuttingDown).
var shuttingDown atomic.Bool

// SetShuttingDown records that the process is stopping: from then on an ERROR record whose
// error is a cancelled context (context.Canceled or context.DeadlineExceeded, also when only
// its text survived, wrapped by a driver) is logged at DEBUG. Work cut short by the stop
// (a settings read, a queue poll, a request) otherwise fills the log with errors that say
// nothing about a fault.
func SetShuttingDown(v bool) { shuttingDown.Store(v) }

// cancelled reports whether one of the record's attributes (or the logger's) is an error
// that a cancelled context caused.
func (h *handler) cancelled(r slog.Record) bool {
	found := false
	check := func(a slog.Attr) bool {
		if isCancellation(a.Value.Resolve().Any()) {
			found = true
			return false
		}
		return true
	}
	for _, a := range h.attrs {
		if !check(a) {
			return true
		}
	}
	r.Attrs(check)
	return found
}

// isCancellation reports whether v is an error (or an error's text) caused by a cancelled
// or expired context.
func isCancellation(v any) bool {
	var msg string
	switch e := v.(type) {
	case error:
		if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
			return true
		}
		msg = e.Error()
	case string:
		msg = e
	default:
		return false
	}
	return strings.Contains(msg, context.Canceled.Error()) || strings.Contains(msg, context.DeadlineExceeded.Error())
}

// handler writes to the text handler and, if set, offers each record to the shipper.
type handler struct {
	text    slog.Handler
	shipper *Shipper
	level   slog.Leveler
	attrs   []slog.Attr
	group   string
}

// New returns a logger writing text to w at level, also shipping to shipper when non-nil.
func New(w io.Writer, level slog.Leveler, shipper *Shipper) *slog.Logger {
	return slog.New(&handler{
		text:    slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}),
		shipper: shipper,
		level:   level,
	})
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool { return l >= h.level.Level() }

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError && shuttingDown.Load() && h.cancelled(r) {
		// A failure that is only the process stopping under it: not an error worth a page.
		r.Level = slog.LevelDebug
		if r.Level < h.level.Level() {
			return nil
		}
	}
	err := h.text.Handle(ctx, r)
	if h.shipper != nil {
		rec := Record{Time: r.Time, Level: r.Level, Message: r.Message, Source: "jarvisd", Attrs: map[string]any{}}
		add := func(a slog.Attr) {
			key := a.Key
			if h.group != "" {
				key = h.group + "." + key
			}
			if a.Key == "module" {
				rec.Source = a.Value.String()
				return
			}
			rec.Attrs[key] = a.Value.Resolve().Any()
		}
		for _, a := range h.attrs {
			add(a)
		}
		r.Attrs(func(a slog.Attr) bool { add(a); return true })
		h.shipper.offer(rec)
	}
	return err
}

func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	c := *h
	c.text = h.text.WithAttrs(as)
	c.attrs = append(append([]slog.Attr{}, h.attrs...), as...)
	return &c
}

func (h *handler) WithGroup(name string) slog.Handler {
	c := *h
	c.text = h.text.WithGroup(name)
	if c.group != "" {
		c.group += "." + name
	} else {
		c.group = name
	}
	return &c
}
