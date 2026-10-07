package service

import (
	"context"
	"net"
	"os"
	"runtime"
	"strings"
)

// Notify sends a state string such as "READY=1" to systemd over $NOTIFY_SOCKET (the
// sd_notify protocol, one datagram, no cgo). Without NOTIFY_SOCKET, or off Linux, it does
// nothing.
func Notify(state string) error {
	return notify(os.Getenv("NOTIFY_SOCKET"), state)
}

func notify(socket, state string) error {
	if socket == "" || runtime.GOOS != "linux" {
		return nil
	}
	if strings.HasPrefix(socket, "@") {
		socket = "\x00" + socket[1:] // abstract namespace
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(state))
	return err
}

type readyKey struct{}

// WithReady attaches a hook that Ready calls, e.g. the Windows service handler reporting
// Running to the SCM.
func WithReady(ctx context.Context, fn func()) context.Context {
	return context.WithValue(ctx, readyKey{}, fn)
}

// Ready tells the supervisor that jarvisd is serving (every listener bound, modules
// started): READY=1 to systemd, and the WithReady hook if any.
func Ready(ctx context.Context) error {
	if fn, ok := ctx.Value(readyKey{}).(func()); ok && fn != nil {
		fn()
	}
	return Notify("READY=1")
}

// Stopping tells systemd that a graceful shutdown has begun.
func Stopping() error { return Notify("STOPPING=1") }
