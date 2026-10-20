package portal

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeShortcuts is a GlobalShortcuts portal: it records the calls and sends Activated when the
// test presses a shortcut.
type fakeShortcuts struct {
	noPortal bool
	answer   uint32 // BindShortcuts' response code
	trigger  string // the trigger_description it reports ("" : none)

	mu     sync.Mutex
	log    []string
	acts   chan Activation
	closed bool
}

func (f *fakeShortcuts) rec(s string) { f.mu.Lock(); f.log = append(f.log, s); f.mu.Unlock() }

func (f *fakeShortcuts) ShortcutsVersion(context.Context) (uint32, error) {
	if f.noPortal {
		return 0, errors.New("org.freedesktop.DBus.Error.UnknownInterface")
	}
	return 1, nil
}

func (f *fakeShortcuts) Activated(context.Context) (<-chan Activation, error) {
	f.rec("watch")
	f.acts = make(chan Activation, 4)
	return f.acts, nil
}

func (f *fakeShortcuts) press(session, id string) { f.acts <- Activation{Session: session, ID: id} }

func (f *fakeShortcuts) CreateShortcutsSession(context.Context, map[string]any) (uint32, map[string]any, error) {
	f.rec("create")
	return 0, map[string]any{"session_handle": "/s/1"}, nil
}

func (f *fakeShortcuts) BindShortcuts(_ context.Context, session string, s []Shortcut, _ map[string]any) (uint32, map[string]any, error) {
	f.rec("bind " + session + " " + s[0].ID + " " + s[0].PreferredTrigger + " (" + s[0].Description + ")")
	if f.answer != 0 {
		return f.answer, nil, nil
	}
	opts := map[string]any{"description": s[0].Description}
	if f.trigger != "" {
		opts["trigger_description"] = f.trigger
	}
	return 0, map[string]any{"shortcuts": []any{[]any{s[0].ID, opts}}}, nil
}

func (f *fakeShortcuts) CloseSession(_ context.Context, session string) error {
	f.rec("close " + session)
	return nil
}

func (f *fakeShortcuts) Close() { f.mu.Lock(); f.closed = true; f.mu.Unlock() }

func (f *fakeShortcuts) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.log)
}

func dialShortcuts(f *fakeShortcuts) func() (ShortcutsBus, error) {
	return func() (ShortcutsBus, error) { return f, nil }
}

func TestStopShortcutBindsAndFires(t *testing.T) {
	f := &fakeShortcuts{trigger: "Ctrl+Alt+Escape"}
	ctx, cancel := context.WithCancel(context.Background())
	pressed := make(chan struct{}, 4)
	label, err := BindStopShortcut(ctx, dialShortcuts(f), func() { pressed <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	if label != "Ctrl+Alt+Escape" {
		t.Fatalf("label %q, want the portal's trigger description", label)
	}
	// Activated is watched before the session exists, so no press can be missed.
	want := []string{"watch", "create", "bind /s/1 stop CTRL+ALT+Escape (Stop what Jarvis is doing on the computer)"}
	if got := f.calls(); !slices.Equal(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
	f.press("/s/1", "other")
	f.press("/s/2", StopShortcutID) // another app's session
	f.press("/s/1", StopShortcutID)
	select {
	case <-pressed:
	case <-time.After(5 * time.Second):
		t.Fatal("Activated didn't call on")
	}
	select {
	case <-pressed:
		t.Fatal("another shortcut's (or session's) Activated called on")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(f.calls(), "close /s/1") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	f.mu.Lock()
	closed := f.closed
	f.mu.Unlock()
	if !slices.Contains(f.calls(), "close /s/1") || !closed {
		t.Fatalf("after ctx ended: calls %v, bus closed %v; want the session and bus closed", f.calls(), closed)
	}
}

func TestStopShortcutWithoutTrigger(t *testing.T) {
	// xdg-desktop-portal-hyprland binds it but leaves the key to hyprland.conf: no label, and it
	// still listens.
	f := &fakeShortcuts{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pressed := make(chan struct{}, 1)
	label, err := BindStopShortcut(ctx, dialShortcuts(f), func() { pressed <- struct{}{} })
	if err != nil || label != "" {
		t.Fatalf("label %q, err %v; want none, no error", label, err)
	}
	f.press("/s/1", StopShortcutID)
	select {
	case <-pressed:
	case <-time.After(5 * time.Second):
		t.Fatal("Activated didn't call on")
	}
}

func TestStopShortcutUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    *fakeShortcuts
		want string
	}{
		{"no portal", &fakeShortcuts{noPortal: true}, "GlobalShortcuts"},
		{"declined", &fakeShortcuts{answer: responseCancelled}, "declined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BindStopShortcut(context.Background(), dialShortcuts(tc.f), func() {})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming %q", err, tc.want)
			}
			tc.f.mu.Lock()
			defer tc.f.mu.Unlock()
			if !tc.f.closed {
				t.Fatal("bus left open")
			}
		})
	}
}
