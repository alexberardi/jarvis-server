// Package tray is the desktop agent's status icon and its menu, the standard place a background
// app shows that it runs and how to stop it (DESIGN.md §15). The agent has no window and no Dock
// or taskbar entry, so without it a user could neither see the agent nor quit it.
//
// One menu on every OS, built here as data (Items):
//
//	Jarvis Automation Agent — Running          (a disabled header: the agent's state)
//	─────────────
//	✓ Listen for "Hey Jarvis"                   (voice on/off, live; saved in agent.json)
//	  Open Jarvis Automation                    (the server's web UI, when it is known)
//	  Set up / Settings…                        (the agent's setup page in the browser)
//	─────────────
//	  Quit Jarvis Automation Agent              (the same as `jarvis-agent stop`)
//
// A small backend per OS only shows it: a StatusNotifierItem over D-Bus on Linux (waybar, KDE,
// GNOME with the AppIndicator extension), an NSStatusItem in the macOS menu bar (purego, in the
// island's AppKit loop), a notification-area icon on Windows (Shell_NotifyIcon). A desktop that
// can't show one gets no icon and one log line; the agent never fails because of it.
package tray

import (
	"errors"
	"log/slog"
	"sync"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
)

// State is what the menu shows.
type State struct {
	// Status is the header's state: "Running".
	Status string
	// VoiceAvailable shows the voice toggle (voice works on this OS); Voice is its checkmark.
	VoiceAvailable bool
	Voice          bool
	// OpenURL, when set, shows "Open Jarvis Automation" (the server's web UI).
	OpenURL string
	// Setup shows "Set up / Settings…" (the agent's own setup page).
	Setup bool
}

// Actions are what the menu's items do. Each runs on its own goroutine, never on the OS's UI
// thread, so it may block.
type Actions struct {
	// SetVoice turns listening for the wake word on or off.
	SetVoice func(on bool)
	// Open opens url (State.OpenURL) in the browser.
	Open func(url string)
	// Setup opens the agent's setup page.
	Setup func()
	// Quit stops the agent (exit 0: no supervisor restarts it).
	Quit func()
}

// The menu items' ids (D-Bus menu ids, NSMenuItem tags, Win32 command ids). 0 is the root.
const (
	IDHeader int32 = 1 + iota
	IDVoice
	IDOpen
	IDQuit
	idSep1
	idSep2
	IDSetup
)

// Labels (the naming rule: "Jarvis Automation" for the product, "Jarvis" for the assistant).
const (
	VoiceLabel = `Listen for "Hey Jarvis"`
	OpenLabel  = "Open Jarvis Automation"
	SetupLabel = "Set up / Settings…"
	QuitLabel  = "Quit " + appid.DisplayName
)

// Item is one menu entry.
type Item struct {
	ID        int32
	Label     string
	Enabled   bool
	Toggle    bool // a checkmark item
	Checked   bool
	Separator bool
}

// Header is the menu's first line and the icon's tooltip.
func Header(s State) string {
	st := s.Status
	if st == "" {
		st = "Running"
	}
	return appid.DisplayName + " — " + st
}

// Items is the menu for s.
func Items(s State) []Item {
	items := []Item{{ID: IDHeader, Label: Header(s)}, {ID: idSep1, Separator: true}}
	if s.VoiceAvailable {
		items = append(items, Item{ID: IDVoice, Label: VoiceLabel, Enabled: true, Toggle: true, Checked: s.Voice})
	}
	if s.OpenURL != "" {
		items = append(items, Item{ID: IDOpen, Label: OpenLabel, Enabled: true})
	}
	if s.Setup {
		items = append(items, Item{ID: IDSetup, Label: SetupLabel, Enabled: true})
	}
	if s.VoiceAvailable || s.OpenURL != "" || s.Setup {
		items = append(items, Item{ID: idSep2, Separator: true})
	}
	return append(items, Item{ID: IDQuit, Label: QuitLabel, Enabled: true})
}

// ErrNoHost: nothing on this desktop shows status icons (Linux without a StatusNotifierItem
// host: GNOME without the AppIndicator extension, a bar without a tray module).
var ErrNoHost = errors.New("no status icon host on this desktop")

// ErrUnsupported: no backend for this OS or session.
var ErrUnsupported = errors.New("status icons aren't supported here")

// backend shows the menu. update is called with the whole menu on every change.
type backend interface {
	update(items []Item, tooltip string) error
	close()
}

// Tray is the status icon. A nil *Tray is valid and shows nothing.
type Tray struct {
	log *slog.Logger
	act Actions

	mu     sync.Mutex
	state  State
	be     backend
	closed bool
	wg     sync.WaitGroup // actions in flight
}

func newTray(log *slog.Logger, act Actions, s State) *Tray {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Tray{log: log, act: act, state: s}
}

// attach shows the menu on be.
func (t *Tray) attach(be backend) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.be = be
	return be.update(Items(t.state), Header(t.state))
}

// State is what the menu shows now.
func (t *Tray) State() State {
	if t == nil {
		return State{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// Update changes what the menu shows.
func (t *Tray) Update(f func(*State)) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	f(&t.state)
	if t.be == nil || t.closed {
		return
	}
	if err := t.be.update(Items(t.state), Header(t.state)); err != nil {
		t.log.Warn("tray: couldn't update the menu", "err", err)
	}
}

// clicked runs an item's action on its own goroutine. Backends call it from the OS's thread.
func (t *Tray) clicked(id int32) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	s := t.state
	t.wg.Add(1)
	t.mu.Unlock()
	go func() {
		defer t.wg.Done()
		switch id {
		case IDVoice:
			if s.VoiceAvailable && t.act.SetVoice != nil {
				t.act.SetVoice(!s.Voice)
			}
		case IDOpen:
			if s.OpenURL != "" && t.act.Open != nil {
				t.act.Open(s.OpenURL)
			}
		case IDSetup:
			if s.Setup && t.act.Setup != nil {
				t.act.Setup()
			}
		case IDQuit:
			if t.act.Quit != nil {
				t.log.Info("tray: Quit chosen")
				t.act.Quit()
			}
		}
	}()
}

// Close removes the icon. Actions already started may still be running; Close doesn't wait
// for them (Quit's own action ends in Close).
func (t *Tray) Close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	be := t.be
	t.mu.Unlock()
	if be != nil {
		be.close()
	}
}

// wait waits for the actions started so far (tests).
func (t *Tray) wait() { t.wg.Wait() }

// Options start a Tray.
type Options struct {
	Log     *slog.Logger
	Actions Actions
	State   State
	// MainLoop says the island's AppKit loop runs on the main thread (macOS: the status item
	// lives there; without it there is no icon).
	MainLoop bool
}
