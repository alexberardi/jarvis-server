// Package portal types through the xdg-desktop-portal RemoteDesktop interface: the way an
// ordinary program may send keys on GNOME and KDE Wayland (Bazzite's default), where there is
// no virtual-keyboard protocol. The desktop asks the user once, in a system dialog; the portal
// then hands back a restore token, kept 0600 next to the agent's credentials, so later
// sessions start without asking again (persist_mode 2: until the user revokes it).
//
// Each typing call is one short portal session: CreateSession, SelectDevices (keyboard, the
// restore token), Start, NotifyKeyboardKeysym per key, Close. A session isn't kept open between
// calls: GNOME shows a "being remotely controlled" indicator for as long as one is.
//
// There is no cross-client window focus here, so text goes to whatever is in front; naming an
// app stays refused (internal/agent/dictate).
package portal

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

// Bus is the slice of the RemoteDesktop portal the client uses (godbus on Linux, a fake in
// tests). Request-style methods return the Request's Response: code 0 success, 1 the user
// cancelled, 2 anything else.
type Bus interface {
	// Version and DeviceTypes are the interface's properties; an error means there is no
	// RemoteDesktop portal.
	Version(ctx context.Context) (uint32, error)
	DeviceTypes(ctx context.Context) (uint32, error)
	CreateSession(ctx context.Context, opts map[string]any) (uint32, map[string]any, error)
	SelectDevices(ctx context.Context, session string, opts map[string]any) (uint32, map[string]any, error)
	Start(ctx context.Context, session string, opts map[string]any) (uint32, map[string]any, error)
	Keysym(ctx context.Context, session string, keysym int32, pressed bool) error
	CloseSession(ctx context.Context, session string) error
	Close()
}

// Device types and persist modes (org.freedesktop.portal.RemoteDesktop).
const (
	DeviceKeyboard     uint32 = 1
	persistUntilRevoke uint32 = 2

	responseOK        = 0
	responseCancelled = 1
)

// Name is the backend's name in status and logs.
const Name = "desktop portal (RemoteDesktop)"

// AllowCommand is what the user runs to grant typing once.
const AllowCommand = "jarvis-agent dictation allow"

// ErrNotAllowed: the user hasn't granted typing yet (or revoked it).
var ErrNotAllowed = errors.New("typing on this desktop needs a one-time permission: run `" + AllowCommand + "` and allow it in the dialog")

// RemoteDesktop is the portal client. The zero value dials the session bus and keeps the token
// in the default place.
type RemoteDesktop struct {
	Dial   func() (Bus, error) // the session bus when nil
	Tokens *TokenStore         // DefaultTokenStore when nil
	// Timeout bounds each portal step when the caller's context has no deadline: a Start that
	// shows the dialog waits for the user this long. Zero: 2 minutes.
	Timeout time.Duration
}

func (r *RemoteDesktop) dial() (Bus, error) {
	if r.Dial != nil {
		return r.Dial()
	}
	return dialBus()
}

func (r *RemoteDesktop) tokens() (*TokenStore, error) {
	if r.Tokens != nil {
		return r.Tokens, nil
	}
	return DefaultTokenStore()
}

// State is what status reports.
type State struct {
	Portal  bool   // a RemoteDesktop portal with a keyboard that can remember a grant
	Allowed bool   // a restore token is stored
	Why     string // why typing is off ("" when it's on)
}

// Probe checks the portal and the stored grant. It never shows a dialog (properties only).
func (r *RemoteDesktop) Probe(ctx context.Context) State {
	b, err := r.dial()
	if err != nil {
		return State{Why: "no D-Bus session bus: " + err.Error()}
	}
	defer b.Close()
	v, err := b.Version(ctx)
	if err != nil {
		return State{Why: "this desktop has no RemoteDesktop portal (GNOME and KDE ship one: xdg-desktop-portal-gnome / -kde)"}
	}
	if v < 2 {
		return State{Why: fmt.Sprintf("the desktop's RemoteDesktop portal is version %d and can't remember a permission (version 2 needed), so it would ask every time", v)}
	}
	if t, err := b.DeviceTypes(ctx); err != nil || t&DeviceKeyboard == 0 {
		return State{Why: "the desktop's RemoteDesktop portal offers no keyboard"}
	}
	st := State{Portal: true}
	ts, err := r.tokens()
	if err != nil {
		st.Why = err.Error()
		return st
	}
	tok, err := ts.Load()
	if err != nil {
		st.Why = err.Error()
		return st
	}
	if tok == "" {
		st.Why = ErrNotAllowed.Error()
		return st
	}
	st.Allowed = true
	return st
}

func (r *RemoteDesktop) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, cmp.Or(r.Timeout, 2*time.Minute))
}

// session opens a started session with the keyboard (restoring with token when set) and
// returns it with the new restore token.
func (r *RemoteDesktop) session(ctx context.Context, b Bus, token string) (string, string, error) {
	code, res, err := b.CreateSession(ctx, map[string]any{})
	if err != nil {
		return "", "", fmt.Errorf("portal CreateSession: %w", err)
	}
	if code != responseOK {
		return "", "", fmt.Errorf("portal CreateSession: response %d", code)
	}
	session, _ := res["session_handle"].(string)
	if session == "" {
		return "", "", errors.New("portal CreateSession: no session handle")
	}
	opts := map[string]any{"types": DeviceKeyboard, "persist_mode": persistUntilRevoke}
	if token != "" {
		opts["restore_token"] = token
	}
	if code, _, err := b.SelectDevices(ctx, session, opts); err != nil || code != responseOK {
		_ = b.CloseSession(ctx, session)
		if err == nil {
			err = fmt.Errorf("response %d", code)
		}
		return "", "", fmt.Errorf("portal SelectDevices: %w", err)
	}
	code, res, err = b.Start(ctx, session, map[string]any{})
	if err != nil || code != responseOK {
		_ = b.CloseSession(ctx, session)
		switch {
		case err != nil:
			return "", "", fmt.Errorf("portal Start: %w", err)
		case code == responseCancelled:
			return "", "", errCancelled
		}
		return "", "", fmt.Errorf("portal Start: response %d", code)
	}
	if dev, _ := res["devices"].(uint32); dev&DeviceKeyboard == 0 {
		_ = b.CloseSession(ctx, session)
		return "", "", errors.New("the keyboard wasn't allowed")
	}
	newTok, _ := res["restore_token"].(string)
	return session, newTok, nil
}

var errCancelled = errors.New("typing wasn't allowed (the dialog was cancelled)")

// ErrBadToken: the portal refused the restore token (the grant was revoked, or the token is
// from elsewhere). xdg-desktop-portal 1.22 does this in SelectDevices instead of asking again.
// Bus implementations wrap it.
var ErrBadToken = errors.New("the desktop no longer accepts the stored permission")

// Allow asks the user once (the desktop's dialog) and stores the restore token. It is the only
// thing that is meant to show a dialog.
func (r *RemoteDesktop) Allow(ctx context.Context) error {
	ctx, cancel := r.bound(ctx)
	defer cancel()
	ts, err := r.tokens()
	if err != nil {
		return err
	}
	b, err := r.dial()
	if err != nil {
		return fmt.Errorf("no D-Bus session bus: %w", err)
	}
	defer b.Close()
	if st := r.probeBus(ctx, b); st != "" {
		return errors.New(st)
	}
	session, tok, err := r.session(ctx, b, "")
	if err != nil {
		return err
	}
	_ = b.CloseSession(ctx, session)
	if tok == "" {
		return errors.New("the desktop allowed it but returned no restore token, so it would ask again every time; dictation stays off")
	}
	return ts.Save(tok)
}

// probeBus is Probe's portal half on an open bus ("" when usable).
func (r *RemoteDesktop) probeBus(ctx context.Context, b Bus) string {
	v, err := b.Version(ctx)
	switch {
	case err != nil:
		return "this desktop has no RemoteDesktop portal"
	case v < 2:
		return fmt.Sprintf("the desktop's RemoteDesktop portal is version %d and can't remember a permission", v)
	}
	if t, err := b.DeviceTypes(ctx); err != nil || t&DeviceKeyboard == 0 {
		return "the desktop's RemoteDesktop portal offers no keyboard"
	}
	return ""
}

// Revoke forgets the stored grant (the desktop's own permission settings can revoke it too).
func (r *RemoteDesktop) Revoke() error {
	ts, err := r.tokens()
	if err != nil {
		return err
	}
	return ts.Clear()
}

// Send types strokes into whatever has focus, in one portal session restored from the stored
// token. A token the desktop no longer honours makes it show its dialog again; a cancel there
// forgets the token.
func (r *RemoteDesktop) Send(ctx context.Context, strokes []xkb.Stroke) error {
	ts, err := r.tokens()
	if err != nil {
		return err
	}
	tok, err := ts.Load()
	if err != nil {
		return err
	}
	if tok == "" {
		return ErrNotAllowed
	}
	ctx, cancel := r.bound(ctx)
	defer cancel()
	b, err := r.dial()
	if err != nil {
		return fmt.Errorf("no D-Bus session bus: %w", err)
	}
	defer b.Close()
	session, newTok, err := r.session(ctx, b, tok)
	if err != nil {
		if errors.Is(err, errCancelled) || errors.Is(err, ErrBadToken) {
			_ = ts.Clear()
			return fmt.Errorf("%w; run `%s` to allow it again", err, AllowCommand)
		}
		return err
	}
	defer func() { _ = b.CloseSession(context.WithoutCancel(ctx), session) }()
	// A restore token is single use: keep the new one (none back means the grant is gone).
	if newTok != tok {
		if newTok == "" {
			_ = ts.Clear()
		} else if err := ts.Save(newTok); err != nil {
			return err
		}
	}
	key := func(sym uint32, down bool) error { return b.Keysym(ctx, session, int32(sym), down) }
	for _, s := range strokes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.Shift {
			if err := key(xkb.KeyShiftL, true); err != nil {
				return err
			}
		}
		if err := key(s.Sym, true); err != nil {
			return err
		}
		if err := key(s.Sym, false); err != nil {
			return err
		}
		if s.Shift {
			if err := key(xkb.KeyShiftL, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// --- the restore token ---

// TokenStore keeps the restore token in one file, 0600 in a 0700 directory, written
// atomically. It refuses a file other users can read.
type TokenStore struct{ Path string }

// DefaultTokenStore is <user config dir>/jarvis-agent/portal-remote-desktop.token, next to
// node.json.
func DefaultTokenStore() (*TokenStore, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	return &TokenStore{Path: filepath.Join(dir, "jarvis-agent", "portal-remote-desktop.token")}, nil
}

// Load is the stored token, or "" when there is none.
func (t *TokenStore) Load() (string, error) {
	st, err := os.Stat(t.Path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is readable by other users (mode %o); delete it and run `%s` again", t.Path, st.Mode().Perm(), AllowCommand)
	}
	b, err := os.ReadFile(t.Path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// Save stores tok.
func (t *TokenStore) Save(tok string) error {
	if tok == "" || strings.ContainsAny(tok, "\n\r") {
		return errors.New("refusing an empty or multi-line restore token")
	}
	dir := filepath.Dir(t.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".portal-*.token")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		f.Close()
		return err
	}
	if _, err := f.WriteString(tok + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), t.Path)
}

// Clear removes the token.
func (t *TokenStore) Clear() error {
	if err := os.Remove(t.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// --- the typing backend for dictation ---

// Keyboard is RemoteDesktop as a dictation typing backend.
type Keyboard struct{ R *RemoteDesktop }

func (k *Keyboard) Name() string { return Name }

// Available: the portal works and the user has allowed typing.
func (k *Keyboard) Available(ctx context.Context) (bool, string) {
	st := k.R.Probe(ctx)
	return st.Portal && st.Allowed, st.Why
}

func (k *Keyboard) Send(ctx context.Context, s []xkb.Stroke) error { return k.R.Send(ctx, s) }

// AppID is the agent's application id (the macOS bundle id too). Every portal connection
// registers as it (org.freedesktop.host.portal.Registry, xdg-desktop-portal 1.19+), so a grant
// made from a terminal and the agent's own service share it. Otherwise the portal derives the
// app id from the process's systemd scope: KDE's Konsole runs children as org.kde.konsole, the
// service has none, and a restore token granted to one is refused to the other.
const AppID = "io.jarvis.agent"

// The portal registers an app id only if a .desktop file of that name exists and its Exec
// program does. desktopEntry is that file: hidden from menus, it only names the app (the
// dialog may show "Jarvis Agent").
func desktopEntry(exe string) string {
	return "[Desktop Entry]\nType=Application\nName=Jarvis Agent\nComment=Jarvis desktop agent\nExec=" + exe +
		" run\nNoDisplay=true\nTerminal=false\n"
}

// DesktopFilePath is $XDG_DATA_HOME/applications/io.jarvis.agent.desktop
// (~/.local/share/applications by default).
func DesktopFilePath(getenv func(string) string) (string, error) {
	base := getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "applications", AppID+".desktop"), nil
}

// WriteDesktopFile writes (or refreshes) the .desktop file for exe, the agent's own path.
func WriteDesktopFile(path, exe string) error {
	want := desktopEntry(exe)
	if b, err := os.ReadFile(path); err == nil && string(b) == want {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".jarvis-*.desktop")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(want); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil && runtime.GOOS != "windows" {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
