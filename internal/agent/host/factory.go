package host

import (
	"fmt"
	"runtime"
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

// Native is the backend for the OS this binary runs on.
func Native() (Backend, error) {
	return ForOS(runtime.GOOS, OSSys{}, NewWin32())
}
