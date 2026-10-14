// Package launches remembers the app desktop_open launched last, so "open X and type Y" can
// wait for X's window (DESIGN.md §14 "Open, then type"). A launch is fire-and-forget: the
// typing step is the first to need the window, often before it exists.
package launches

import (
	"context"
	"sync"
	"time"
)

// Launch is one app launch.
type Launch struct {
	Name string // the catalogue's display name ("Neovim")
	ID   string // its desktop id ("nvim"), "" when it has none
	At   time.Time
	// Before is the window ids open just before the launch; nil when they couldn't be listed.
	// A window that isn't in it appeared after the launch (a terminal app's window is the
	// terminal's, so it can't be found by the app's name).
	Before map[string]bool
}

// Tracker holds the last launch. The zero value works; a nil Tracker records nothing.
type Tracker struct {
	// Windows lists the window ids now (nil: launches carry no Before).
	Windows func(ctx context.Context) ([]string, error)
	// Now is the clock (nil: time.Now).
	Now func() time.Time

	mu   sync.Mutex
	last *Launch
}

func (t *Tracker) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// Record notes a launch about to happen: call it just before launching.
func (t *Tracker) Record(ctx context.Context, name, id string) {
	if t == nil {
		return
	}
	l := &Launch{Name: name, ID: id, At: t.now()}
	if t.Windows != nil {
		if ids, err := t.Windows(ctx); err == nil {
			l.Before = make(map[string]bool, len(ids))
			for _, id := range ids {
				l.Before[id] = true
			}
		}
	}
	t.mu.Lock()
	t.last = l
	t.mu.Unlock()
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
