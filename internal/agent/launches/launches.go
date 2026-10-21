// Package launches remembers the app desktop_open launched last, so "open X and type Y" can
// wait for X's window (DESIGN.md §14 "Open, then type"). A launch is fire-and-forget: the
// typing step is the first to need the window, often before it exists.
package launches

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// SnapshotWait is how long Record waits for the window snapshot by default. The launch waits
// behind it, so it stays short: without a snapshot, typing still finds the window by name.
const SnapshotWait = 300 * time.Millisecond

// Launch is one app launch.
type Launch struct {
	Name string // the catalogue's display name ("Neovim")
	ID   string // its desktop id ("nvim"), "" when it has none
	At   time.Time
	// Before is the window ids open just before the launch; nil when they couldn't be listed
	// in time. A window that isn't in it appeared after the launch (a terminal app's window is
	// the terminal's, so it can't be found by the app's name).
	Before map[string]bool
	// ByOwner: Before holds the Owners source's ids, not the window list's (see NewOwners).
	ByOwner bool
}

// Owned is a window from a fast source whose ids aren't the window list's: Owner is the key the
// window list's ids carry for its app (macOS: a CoreGraphics window number and its app's pid).
type Owned struct{ ID, Owner string }

// Tracker holds the last launch. The zero value works; a nil Tracker records nothing.
type Tracker struct {
	// Windows lists the window ids now, as the window list has them (nil: launches carry no
	// Before).
	Windows func(ctx context.Context) ([]string, error)
	// Owners, when set, is used instead of Windows: a fast source (macOS's window server,
	// milliseconds and no Automation grant, where the window list is AppleScript).
	Owners func(ctx context.Context) ([]Owned, error)
	// Wait bounds the snapshot (0: SnapshotWait).
	Wait time.Duration
	// Now is the clock (nil: time.Now).
	Now func() time.Time
	Log *slog.Logger // nil: no logs

	mu   sync.Mutex
	last *Launch
}

func (t *Tracker) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Tracker) log() *slog.Logger {
	if t.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return t.Log
}

// Record notes a launch about to happen: call it just before launching. It waits at most Wait
// for the window snapshot, so it never holds the launch up; a snapshot that takes longer is
// dropped (it may already list the new window).
func (t *Tracker) Record(ctx context.Context, name, id string) {
	if t == nil {
		return
	}
	l := &Launch{Name: name, ID: id, At: t.now()}
	if t.Windows != nil || t.Owners != nil {
		start := time.Now()
		l.Before, l.ByOwner = t.snapshot(ctx)
		t.log().Debug("launch: window snapshot", "app", name, "took", time.Since(start).Round(time.Millisecond),
			"windows", len(l.Before), "ok", l.Before != nil)
	}
	t.mu.Lock()
	t.last = l
	t.mu.Unlock()
}

func (t *Tracker) snapshot(ctx context.Context) (map[string]bool, bool) {
	wait := t.Wait
	if wait <= 0 {
		wait = SnapshotWait
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	byOwner := t.Owners != nil
	done := make(chan []string, 1) // buffered: a lister that ignores ctx finishes into it later
	go func() {
		ids, err := t.list(ctx)
		if err != nil {
			ids = nil
		} else if ids == nil {
			ids = []string{}
		}
		done <- ids
	}()
	select {
	case ids := <-done:
		if ids == nil {
			return nil, false
		}
		before := make(map[string]bool, len(ids))
		for _, id := range ids {
			before[id] = true
		}
		return before, byOwner
	case <-ctx.Done():
		return nil, false
	}
}

func (t *Tracker) list(ctx context.Context) ([]string, error) {
	if t.Owners == nil {
		return t.Windows(ctx)
	}
	ws, err := t.Owners(ctx)
	ids := make([]string, len(ws))
	for i, w := range ws {
		ids[i] = w.ID
	}
	return ids, err
}

// NewOwners is the owners of the windows the Owners source lists now that weren't there at
// launch l, in the source's order (front first), each once.
func (t *Tracker) NewOwners(ctx context.Context, l Launch) ([]string, error) {
	if t == nil || t.Owners == nil || !l.ByOwner || l.Before == nil {
		return nil, errors.New("no window snapshot by owner")
	}
	ws, err := t.Owners(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for _, w := range ws {
		if !l.Before[w.ID] && !seen[w.Owner] {
			seen[w.Owner] = true
			out = append(out, w.Owner)
		}
	}
	return out, nil
}

// Recent is the last launch if it happened within d.
func (t *Tracker) Recent(d time.Duration) (Launch, bool) {
	if t == nil {
		return Launch{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.last == nil || t.now().Sub(t.last.At) > d {
		return Launch{}, false
	}
	return *t.last, true
}
