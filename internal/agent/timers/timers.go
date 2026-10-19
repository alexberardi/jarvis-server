// Package timers is the Pi node's timer service: the SDK's TimerBackend, as the built-in
// set_timer, check_timers and cancel_timer commands reach it through the sidecar
// (N:services/timer_service.py). The host owns the timers: they are persisted in the node's
// state dir, re-armed at start (one that ended while the agent was down fires at once), and
// on expiry Fire says "Your pasta timer is done!".
package timers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Timer is one active timer as persisted.
type Timer struct {
	ID       string    `json:"timer_id"`
	Label    *string   `json:"label"`
	Duration int       `json:"duration_seconds"`
	Started  time.Time `json:"started_at"`
	Ends     time.Time `json:"ends_at"`
}

// Service keeps the timers. Set Path and Fire, then call Start once.
type Service struct {
	Path string      // the JSON file the timers survive a restart in
	Fire func(Timer) // the expiry, on its own goroutine; nil does nothing
	Log  *slog.Logger

	// Tests replace these; nil is the real clock and random ids.
	Now       func() time.Time
	AfterFunc func(d time.Duration, f func()) (stop func() bool)
	NewID     func() string

	mu     sync.Mutex
	timers []*entry // in the order they were set: find_by_label's first match, as Python's dict
}

type entry struct {
	Timer
	stop func() bool
}

// Message is what the node says when a timer ends (N:_default_timer_complete_handler).
func Message(label *string) string {
	if label != nil && *label != "" {
		return "Your " + *label + " timer is done!"
	}
	return "Your timer is done!"
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return s.Log
}

// Start re-arms the persisted timers; those that ended while the agent was down fire now. It
// returns how many were re-armed. An unreadable file is logged and starts empty.
func (s *Service) Start() int {
	b, err := os.ReadFile(s.Path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.log().Warn("timers: not restored", "err", err)
	}
	var saved []Timer
	if len(b) > 0 {
		if err := json.Unmarshal(b, &saved); err != nil {
			s.log().Warn("timers: not restored", "err", err)
			saved = nil
		}
	}
	now := s.now()
	var due []Timer
	s.mu.Lock()
	for _, t := range saved {
		if t.ID == "" {
			continue
		}
		if !t.Ends.After(now) {
			due = append(due, t)
			continue
		}
		s.arm(t, t.Ends.Sub(now))
		s.log().Info("timers: restored", "label", label(t.Label), "remaining", t.Ends.Sub(now).Round(time.Second))
	}
	n := len(s.timers)
	if len(due) > 0 {
		s.saveLocked()
	}
	s.mu.Unlock()
	for _, t := range due {
		s.log().Info("timers: ended while the agent was down, firing now", "label", label(t.Label))
		s.fire(t)
	}
	return n
}

func label(l *string) string {
	if l == nil {
		return ""
	}
	return *l
}

// arm schedules t to fire in d. Callers hold mu.
func (s *Service) arm(t Timer, d time.Duration) {
	after := s.AfterFunc
	if after == nil {
		after = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	e := &entry{Timer: t}
	s.timers = append(s.timers, e)
	e.stop = after(d, func() { s.expire(t.ID) })
}

func (s *Service) expire(id string) {
	s.mu.Lock()
	e := s.take(id)
	if e != nil {
		s.saveLocked()
	}
	s.mu.Unlock()
	if e != nil { // nil: cancelled meanwhile
		s.fire(e.Timer)
	}
}

func (s *Service) fire(t Timer) {
	if s.Fire != nil {
		go s.Fire(t)
	}
}

// take removes the timer id and returns it (nil when there is none). Callers hold mu.
func (s *Service) take(id string) *entry {
	for i, e := range s.timers {
		if e.ID == id {
			s.timers = append(s.timers[:i], s.timers[i+1:]...)
			return e
		}
	}
	return nil
}

func (s *Service) newID() string {
	if s.NewID != nil {
		return s.NewID()
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b) // 8 characters, as Python's uuid4()[:8]
}

// Set starts a timer for seconds and returns its id.
func (s *Service) Set(seconds int, label *string) (string, error) {
	if seconds < 1 {
		return "", fmt.Errorf("a timer needs at least one second, not %d", seconds)
	}
	now := s.now()
	d := time.Duration(seconds) * time.Second
	t := Timer{ID: s.newID(), Label: label, Duration: seconds, Started: now, Ends: now.Add(d)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.arm(t, d)
	s.saveLocked()
	return t.ID, nil
}

// Cancel stops the timer id; false when there is no such timer.
func (s *Service) Cancel(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.take(id)
	if e == nil {
		return false
	}
	e.stop()
	s.saveLocked()
	return true
}

// ClearAll cancels every timer and returns how many there were.
func (s *Service) ClearAll() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.timers)
	for _, e := range s.timers {
		e.stop()
	}
	s.timers = nil
	s.saveLocked()
	return n
}

// Active are the timers as the SDK reports them (N:get_active_timers).
func (s *Service) Active() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := []map[string]any{}
	for _, e := range s.timers {
		out = append(out, e.report(now))
	}
	return out
}

// Get is one timer as the SDK reports it, or nil.
func (s *Service) Get(id string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.timers {
		if e.ID == id {
			return e.report(s.now())
		}
	}
	return nil
}

// FindByLabel is the first timer whose label contains label, ignoring case ("" for none).
func (s *Service) FindByLabel(label string) string {
	want := strings.ToLower(label)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.timers {
		if e.Label != nil && *e.Label != "" && strings.Contains(strings.ToLower(*e.Label), want) {
			return e.ID
		}
	}
	return ""
}

// Close stops the scheduled timers and keeps them persisted for the next start.
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.timers {
		e.stop()
	}
	s.timers = nil
}

func (t Timer) report(now time.Time) map[string]any {
	return map[string]any{
		"timer_id": t.ID, "label": t.Label, "duration_seconds": t.Duration,
		"remaining_seconds": int(max(0, t.Ends.Sub(now).Seconds())),
		"started_at":        isoformat(t.Started), "ends_at": isoformat(t.Ends),
	}
}

// isoformat is Python's datetime.isoformat() of t in UTC.
func isoformat(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond()/1000 != 0 {
		return t.Format("2006-01-02T15:04:05.000000+00:00")
	}
	return t.Format("2006-01-02T15:04:05+00:00")
}

// saveLocked writes the timers (atomically); a failure is logged, as the Python node's.
func (s *Service) saveLocked() {
	list := make([]Timer, 0, len(s.timers))
	for _, e := range s.timers {
		list = append(list, e.Timer)
	}
	b, _ := json.MarshalIndent(list, "", "  ")
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".timers-*")
	if err == nil {
		_, err = tmp.Write(b)
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(tmp.Name(), s.Path)
		}
		if err != nil {
			os.Remove(tmp.Name())
		}
	}
	if err != nil {
		s.log().Warn("timers: not persisted", "err", err)
	}
}
