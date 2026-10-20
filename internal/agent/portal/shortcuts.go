package portal

import (
	"context"
	"errors"
	"fmt"
)

// The stop hotkey through org.freedesktop.portal.GlobalShortcuts: the way an ordinary program
// gets a key combination on a Wayland desktop (xdg-desktop-portal-hyprland, -kde, -gnome 48+).
// One session for the agent's life: CreateSession, BindShortcuts with the one shortcut, then its
// Activated signals. The desktop decides the key: GNOME and KDE offer the preferred trigger in a
// dialog the first time; Hyprland binds none itself and leaves it to a hyprland.conf line
// (docs/agent/DESIGN.md).

// ShortcutsBus is the slice of the GlobalShortcuts portal the client uses (godbus on Linux, a
// fake in tests). Request-style methods return the Request's Response, as Bus's do.
type ShortcutsBus interface {
	// ShortcutsVersion is the interface's version property; an error means there is no
	// GlobalShortcuts portal.
	ShortcutsVersion(ctx context.Context) (uint32, error)
	// Activated delivers every shortcut activation on the bus (any session's) until ctx ends
	// or the bus closes.
	Activated(ctx context.Context) (<-chan Activation, error)
	CreateShortcutsSession(ctx context.Context, opts map[string]any) (uint32, map[string]any, error)
	BindShortcuts(ctx context.Context, session string, shortcuts []Shortcut, opts map[string]any) (uint32, map[string]any, error)
	CloseSession(ctx context.Context, session string) error
	Close()
}

// Shortcut is one shortcut to bind.
type Shortcut struct {
	ID, Description string
	// PreferredTrigger is the key combination asked for, in the XDG shortcuts format
	// ("CTRL+ALT+Escape"); the desktop may pick another.
	PreferredTrigger string
}

// Activation is an Activated signal: shortcut ID of the session was pressed.
type Activation struct{ Session, ID string }

// StopShortcutID is the stop hotkey's shortcut id (a Hyprland bind names it:
// `global, <app id>:stop`).
const StopShortcutID = "stop"

var stopShortcut = Shortcut{ID: StopShortcutID, Description: "Stop what Jarvis is doing on the computer",
	PreferredTrigger: "CTRL+ALT+Escape"}

// BindStopShortcut binds the stop shortcut and calls on (on its own goroutine) at each
// activation until ctx ends, then closes the session. label is the key the desktop says it
// bound ("" when it names none, as Hyprland's portal does: the shortcut then fires only from a
// bind in the compositor's config). An error when there is no GlobalShortcuts portal or the
// user declined; nothing is left open then. dial nil: the session bus.
func BindStopShortcut(ctx context.Context, dial func() (ShortcutsBus, error), on func()) (label string, err error) {
	if dial == nil {
		dial = dialShortcutsBus
	}
	b, err := dial()
	if err != nil {
		return "", fmt.Errorf("no D-Bus session bus: %w", err)
	}
	if _, err := b.ShortcutsVersion(ctx); err != nil {
		b.Close()
		return "", errors.New("this desktop has no GlobalShortcuts portal")
	}
	watch, cancel := context.WithCancel(ctx)
	fail := func(err error) (string, error) { cancel(); b.Close(); return "", err }
	acts, err := b.Activated(watch)
	if err != nil {
		return fail(err)
	}
	code, res, err := b.CreateShortcutsSession(ctx, nil)
	if err == nil && code != responseOK {
		err = fmt.Errorf("the portal refused a session (response %d)", code)
	}
	if err != nil {
		return fail(fmt.Errorf("GlobalShortcuts CreateSession: %w", err))
	}
	session, _ := res["session_handle"].(string)
	if session == "" {
		return fail(errors.New("GlobalShortcuts CreateSession: no session handle"))
	}
	end := func() {
		_ = b.CloseSession(context.WithoutCancel(ctx), session)
		b.Close()
	}
	code, res, err = b.BindShortcuts(ctx, session, []Shortcut{stopShortcut}, nil)
	switch {
	case err != nil:
		err = fmt.Errorf("GlobalShortcuts BindShortcuts: %w", err)
	case code == responseCancelled:
		err = errors.New("the stop shortcut was declined in the desktop's dialog")
	case code != responseOK:
		err = fmt.Errorf("the portal refused the stop shortcut (response %d)", code)
	}
	if err != nil {
		cancel()
		end()
		return "", err
	}
	go func() {
		defer end()
		defer cancel()
		for {
			select {
			case <-watch.Done():
				return
			case a, ok := <-acts:
				if !ok {
					return
				}
				if a.Session == session && a.ID == StopShortcutID {
					on()
				}
			}
		}
	}()
	return triggerOf(res, StopShortcutID), nil
}

// triggerOf is the trigger_description BindShortcuts' results give shortcut id (shortcuts:
// a(sa{sv})), "" when there is none.
func triggerOf(res map[string]any, id string) string {
	list, _ := res["shortcuts"].([]any)
	for _, e := range list {
		pair, _ := e.([]any)
		if len(pair) != 2 || pair[0] != id {
			continue
		}
		opts, _ := pair[1].(map[string]any)
		s, _ := opts["trigger_description"].(string)
		return s
	}
	return ""
}
