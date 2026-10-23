// Package fallback decides, per request, whether a remote speech provider (STT or TTS) is
// asked or the local CPU engine answers instead (fallback=cpu, docs/llm/07-remote-speech.md §2,
// §3), and counts what it decided for the label status.
//
// The rule (docs/decisions/2026-10-22-remote-speech-fallback.md): every request asks the
// remote unless it failed recently. A failure that falls back opens a backoff window (Base,
// doubling per consecutive failure up to Max) during which requests go straight to the local
// engine; the first request after the window probes the remote again, and a success closes it.
// No flapping between paths within one request, and no remote request per turn while it is down.
package fallback

import (
	"net/http"
	"sync"
	"time"
)

// Setting values (the stt label's fallback, tts.fallback).
const (
	ModeOff = "off"
	ModeCPU = "cpu"
)

// Paths that served a request, for logs and the status.
const (
	PathRemote   = "remote"
	PathLocalCPU = "local-cpu"
)

// Default backoff window: the first failure skips the remote for 30 s, doubling to 5 min.
const (
	DefaultBase = 30 * time.Second
	DefaultMax  = 5 * time.Minute
)

// FallsBackOn reports whether a remote failure with this HTTP status falls back: a transport
// error or timeout (0), 401/403 (an expired key shouldn't silence the house), 429, 5xx, anything
// else not about the request itself. A 400, 413 or 422 is our bug or a bad clip or sentence: the
// local engine would only be asked the same thing.
func FallsBackOn(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return false
	}
	return true
}

// Status is what the label status shows about a remote speech label's fallback.
type Status struct {
	// Mode is the setting (off, cpu).
	Mode string `json:"mode"`
	// Fallbacks counts requests the local CPU engine served since jarvisd started.
	Fallbacks int64 `json:"fallbacks"`
	// LastPath is the path that served the latest request (remote, local-cpu); "" before any.
	LastPath          string    `json:"last_path,omitempty"`
	LastRemoteError   string    `json:"last_remote_error,omitempty"`
	LastRemoteErrorAt time.Time `json:"last_remote_error_at,omitzero"`
	// BackoffUntil: until then requests skip the remote (zero when it is being asked).
	BackoffUntil time.Time `json:"backoff_until,omitzero"`
	// Warning says why the fallback can't work as set (fallback=cpu with no local model).
	Warning string `json:"warning,omitempty"`
}

// Gate is one label's fallback state. The zero value works (default window, time.Now).
type Gate struct {
	Base, Max time.Duration
	Now       func() time.Time

	mu        sync.Mutex
	failures  int
	until     time.Time
	fallbacks int64
	lastPath  string
	lastErr   string
	lastErrAt time.Time
}

func (g *Gate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// TryRemote reports whether this request asks the remote (false inside a backoff window).
func (g *Gate) TryRemote() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.now().Before(g.until)
}

// Failed records a remote failure that falls back and opens (or widens) the backoff window.
func (g *Gate) Failed(err error) {
	base, maxW := g.Base, g.Max
	if base <= 0 {
		base = DefaultBase
	}
	if maxW <= 0 {
		maxW = DefaultMax
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	w := base
	for i := 0; i < g.failures && w < maxW; i++ {
		w *= 2
	}
	g.failures++
	g.until = now.Add(min(w, maxW))
	g.lastErr, g.lastErrAt = err.Error(), now
}

// Errored records a remote failure that doesn't fall back (fallback=off, or a 400/413/422)
// for the status, without opening a backoff window.
func (g *Gate) Errored(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastErr, g.lastErrAt = err.Error(), g.now()
}

// Succeeded records a remote success: the window closes and the next failure starts at Base.
func (g *Gate) Succeeded() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failures, g.until = 0, time.Time{}
}

// Served records which path answered a request; local-cpu counts as a fallback.
func (g *Gate) Served(path string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastPath = path
	if path == PathLocalCPU {
		g.fallbacks++
	}
}

// Status reports the gate (Mode and Warning are the caller's).
func (g *Gate) Status() Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := Status{Fallbacks: g.fallbacks, LastPath: g.lastPath, LastRemoteError: g.lastErr, LastRemoteErrorAt: g.lastErrAt}
	if g.now().Before(g.until) {
		st.BackoffUntil = g.until
	}
	return st
}
