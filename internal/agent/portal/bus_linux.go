//go:build linux

package portal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

const (
	busName      = "org.freedesktop.portal.Desktop"
	objPath      = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	ifaceRD      = "org.freedesktop.portal.RemoteDesktop"
	ifaceGS      = "org.freedesktop.portal.GlobalShortcuts"
	ifaceRequest = "org.freedesktop.portal.Request"
	ifaceSession = "org.freedesktop.portal.Session"
)

// dbusBus is Bus over a private session-bus connection (github.com/godbus/dbus/v5, pure Go).
type dbusBus struct {
	c          *dbus.Conn
	obj        dbus.BusObject
	registered error // why Register failed (nil: the portal knows us as AppID)
}

func dialBus() (Bus, error) {
	c, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	return newDbusBus(c), nil
}

func newDbusBus(c *dbus.Conn) *dbusBus {
	b := &dbusBus{c: c, obj: c.Object(busName, objPath)}
	b.registered = b.register()
	return b
}

// register tells the portal this connection is AppID (org.freedesktop.host.portal.Registry,
// xdg-desktop-portal 1.19+). Without it the portal derives the app id from the process's
// systemd scope, which differs between a terminal (KDE's Konsole: org.kde.konsole) and the
// agent's user service, and a restore token granted to one is refused to the other. Must come
// before any other portal call on the connection. Best effort: older portals lack it.
func (b *dbusBus) register() error {
	return b.obj.Call("org.freedesktop.host.portal.Registry.Register", 0, AppID, map[string]dbus.Variant{}).Err
}

func (b *dbusBus) Close() { _ = b.c.Close() }

func (b *dbusBus) prop(ctx context.Context, name string) (uint32, error) {
	return b.propOn(ctx, ifaceRD, name)
}

func (b *dbusBus) propOn(ctx context.Context, iface, name string) (uint32, error) {
	var v dbus.Variant
	err := b.obj.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, iface, name).Store(&v)
	if err != nil {
		return 0, err
	}
	u, ok := v.Value().(uint32)
	if !ok {
		return 0, fmt.Errorf("%s is %s, not a uint32", name, v.Signature())
	}
	return u, nil
}

func (b *dbusBus) Version(ctx context.Context) (uint32, error) { return b.prop(ctx, "version") }
func (b *dbusBus) DeviceTypes(ctx context.Context) (uint32, error) {
	return b.prop(ctx, "AvailableDeviceTypes")
}

func token() string {
	var r [8]byte
	_, _ = rand.Read(r[:])
	return "jarvis" + hex.EncodeToString(r[:])
}

func variants(opts map[string]any) map[string]dbus.Variant {
	out := make(map[string]dbus.Variant, len(opts)+1)
	for k, v := range opts {
		out[k] = dbus.MakeVariant(v)
	}
	return out
}

// requestPath is where the portal will put the Request for handle_token tok
// (/org/freedesktop/portal/desktop/request/SENDER/TOKEN, SENDER the unique name without ':'
// and with '.' as '_'). Subscribing to it before the call means the Response can't be missed.
func (b *dbusBus) requestPath(tok string) dbus.ObjectPath {
	names := b.c.Names()
	sender := ""
	if len(names) > 0 {
		sender = strings.ReplaceAll(strings.TrimPrefix(names[0], ":"), ".", "_")
	}
	return dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + sender + "/" + tok)
}

// request calls a Request-style method (opts last) and waits for the Response.
func (b *dbusBus) request(ctx context.Context, method string, opts map[string]any, args ...any) (uint32, map[string]any, error) {
	return b.requestOn(ctx, ifaceRD, method, opts, args...)
}

// requestOn is request on interface iface.
func (b *dbusBus) requestOn(ctx context.Context, iface, method string, opts map[string]any, args ...any) (uint32, map[string]any, error) {
	tok := token()
	vopts := variants(opts)
	vopts["handle_token"] = dbus.MakeVariant(tok)
	if method == "CreateSession" {
		vopts["session_handle_token"] = dbus.MakeVariant(token())
	}
	sigs := make(chan *dbus.Signal, 16)
	b.c.Signal(sigs)
	defer b.c.RemoveSignal(sigs)
	watch := func(p dbus.ObjectPath) error {
		return b.c.AddMatchSignalContext(ctx, dbus.WithMatchObjectPath(p), dbus.WithMatchInterface(ifaceRequest), dbus.WithMatchMember("Response"))
	}
	want := b.requestPath(tok)
	if err := watch(want); err != nil {
		return 0, nil, err
	}
	defer func() {
		_ = b.c.RemoveMatchSignalContext(context.WithoutCancel(ctx), dbus.WithMatchObjectPath(want), dbus.WithMatchInterface(ifaceRequest), dbus.WithMatchMember("Response"))
	}()
	var handle dbus.ObjectPath
	if err := b.obj.CallWithContext(ctx, iface+"."+method, 0, append(args, vopts)...).Store(&handle); err != nil {
		return 0, nil, err
	}
	if handle != want { // a portal older than 0.9 picks its own path
		if err := watch(handle); err != nil {
			return 0, nil, err
		}
	}
	for {
		select {
		case <-ctx.Done():
			// Tell the portal to drop the dialog, if one is up.
			_ = b.c.Object(busName, handle).CallWithContext(context.WithoutCancel(ctx), ifaceRequest+".Close", 0).Err
			return 0, nil, fmt.Errorf("portal %s: %w", method, ctx.Err())
		case s, ok := <-sigs:
			if !ok {
				return 0, nil, fmt.Errorf("portal %s: the bus connection closed", method)
			}
			if s.Path != handle || s.Name != ifaceRequest+".Response" || len(s.Body) < 2 {
				continue
			}
			code, _ := s.Body[0].(uint32)
			raw, _ := s.Body[1].(map[string]dbus.Variant)
			res := make(map[string]any, len(raw))
			for k, v := range raw {
				res[k] = plain(v.Value())
			}
			return code, res, nil
		}
	}
}

func (b *dbusBus) CreateSession(ctx context.Context, opts map[string]any) (uint32, map[string]any, error) {
	return b.request(ctx, "CreateSession", opts)
}

func (b *dbusBus) SelectDevices(ctx context.Context, session string, opts map[string]any) (uint32, map[string]any, error) {
	code, res, err := b.request(ctx, "SelectDevices", opts, dbus.ObjectPath(session))
	var de dbus.Error
	if errors.As(err, &de) && opts["restore_token"] != nil && strings.HasSuffix(de.Name, ".InvalidArgument") &&
		strings.Contains(strings.ToLower(fmt.Sprint(de.Body...)), "restore token") {
		return code, res, fmt.Errorf("%w (%v)", ErrBadToken, err)
	}
	return code, res, err
}

func (b *dbusBus) Start(ctx context.Context, session string, opts map[string]any) (uint32, map[string]any, error) {
	return b.request(ctx, "Start", opts, dbus.ObjectPath(session), "") // no parent window
}

func (b *dbusBus) Keysym(ctx context.Context, session string, keysym int32, pressed bool) error {
	state := uint32(0)
	if pressed {
		state = 1
	}
	return b.obj.CallWithContext(ctx, ifaceRD+".NotifyKeyboardKeysym", 0,
		dbus.ObjectPath(session), map[string]dbus.Variant{}, keysym, state).Err
}

func (b *dbusBus) CloseSession(ctx context.Context, session string) error {
	return b.c.Object(busName, dbus.ObjectPath(session)).CallWithContext(ctx, ifaceSession+".Close", 0).Err
}

// plain is a decoded D-Bus value with object paths as strings, variants unwrapped and structs
// and arrays as []any (a(sa{sv}) is []any of []any{string, map[string]any}).
func plain(v any) any {
	switch v := v.(type) {
	case dbus.ObjectPath:
		return string(v)
	case dbus.Variant:
		return plain(v.Value())
	case map[string]dbus.Variant:
		m := make(map[string]any, len(v))
		for k, e := range v {
			m[k] = plain(e.Value())
		}
		return m
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = plain(e)
		}
		return out
	case [][]any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = plain(e)
		}
		return out
	}
	return v
}

// --- GlobalShortcuts ---

func dialShortcutsBus() (ShortcutsBus, error) {
	c, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	return newDbusBus(c), nil
}

func (b *dbusBus) ShortcutsVersion(ctx context.Context) (uint32, error) {
	return b.propOn(ctx, ifaceGS, "version")
}

func (b *dbusBus) Activated(ctx context.Context) (<-chan Activation, error) {
	match := []dbus.MatchOption{dbus.WithMatchObjectPath(objPath), dbus.WithMatchInterface(ifaceGS), dbus.WithMatchMember("Activated")}
	if err := b.c.AddMatchSignalContext(ctx, match...); err != nil {
		return nil, err
	}
	sigs := make(chan *dbus.Signal, 16)
	b.c.Signal(sigs)
	out := make(chan Activation, 4)
	go func() {
		defer close(out)
		defer b.c.RemoveSignal(sigs)
		for {
			select {
			case <-ctx.Done():
				return
			case s, ok := <-sigs:
				if !ok {
					return
				}
				if s.Path != objPath || s.Name != ifaceGS+".Activated" || len(s.Body) < 2 {
					continue
				}
				session, _ := s.Body[0].(dbus.ObjectPath)
				id, _ := s.Body[1].(string)
				select {
				case out <- Activation{Session: string(session), ID: id}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

func (b *dbusBus) CreateShortcutsSession(ctx context.Context, opts map[string]any) (uint32, map[string]any, error) {
	return b.requestOn(ctx, ifaceGS, "CreateSession", opts)
}

// shortcutArg is one (sa{sv}) of BindShortcuts.
type shortcutArg struct {
	ID   string
	Opts map[string]dbus.Variant
}

func (b *dbusBus) BindShortcuts(ctx context.Context, session string, shortcuts []Shortcut, opts map[string]any) (uint32, map[string]any, error) {
	args := make([]shortcutArg, len(shortcuts))
	for i, s := range shortcuts {
		args[i] = shortcutArg{s.ID, map[string]dbus.Variant{
			"description":       dbus.MakeVariant(s.Description),
			"preferred_trigger": dbus.MakeVariant(s.PreferredTrigger),
		}}
	}
	return b.requestOn(ctx, ifaceGS, "BindShortcuts", opts, dbus.ObjectPath(session), args, "") // no parent window
}

// dialBusAt connects to the bus at addr (tests: a private dbus-daemon).
func dialBusAt(addr string) (Bus, error) {
	c, err := dbus.Connect(addr)
	if err != nil {
		return nil, err
	}
	return newDbusBus(c), nil
}
