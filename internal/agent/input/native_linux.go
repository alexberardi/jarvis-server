//go:build linux

package input

import "github.com/alexberardi/jarvis-server/internal/agent/x11"

// Native is the X11 XTEST backend; it is unavailable outside an X11 session.
func Native() Input { return &X11{D: &x11.Display{}} }
