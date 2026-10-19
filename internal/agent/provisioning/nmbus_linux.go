//go:build linux

package provisioning

import (
	"context"

	"github.com/godbus/dbus/v5"
)

// sysBus is nmBus over a private system-bus connection (github.com/godbus/dbus/v5, pure Go).
type sysBus struct{ c *dbus.Conn }

func dialNMBus() (nmBus, error) {
	c, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	return &sysBus{c: c}, nil
}

func (b *sysBus) Call(ctx context.Context, path dbus.ObjectPath, method string, args ...any) ([]any, error) {
	call := b.c.Object(nmIface, path).CallWithContext(ctx, method, 0, args...)
	return call.Body, call.Err
}

func (b *sysBus) Get(ctx context.Context, path dbus.ObjectPath, iface, prop string) (any, error) {
	var v dbus.Variant
	err := b.c.Object(nmIface, path).CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, iface, prop).Store(&v)
	if err != nil {
		return nil, err
	}
	return v.Value(), nil
}

func (b *sysBus) Close() error { return b.c.Close() }
