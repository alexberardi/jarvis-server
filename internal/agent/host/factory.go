package host

import (
	"fmt"
	"runtime"

	"github.com/alexberardi/jarvis-server/internal/agent/x11"
)

// ForOS returns the backend for goos. An unknown platform is an error, never a silent Linux
// default: a wrong backend would shell out to binaries that don't exist and advertise
// capabilities it lacks.
func ForOS(goos string, sys Sys, api Win32) (Backend, error) {
	switch goos {
	case "linux":
		return NewLinux(sys), nil
	case "darwin":
		return NewDarwin(sys), nil
	case "windows":
		return NewWindows(api), nil
	}
	return nil, fmt.Errorf("host: no backend for platform %q", goos)
}

// Native is the backend for the OS this binary runs on. On Linux it gets the built-in X11
// window control (EWMH); ForOS leaves it out so tests never reach a real X server.
func Native() (Backend, error) {
	b, err := ForOS(runtime.GOOS, OSSys{}, NewWin32())
	if l, ok := b.(*Linux); ok {
		l.X = &x11.Display{}
	}
	return b, err
}
