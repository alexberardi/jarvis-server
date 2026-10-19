package dictate

import (
	"context"
	"fmt"
	"runtime"

	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/portal"
	"github.com/alexberardi/jarvis-server/internal/agent/vkbd"
	"github.com/alexberardi/jarvis-server/internal/agent/x11"
)

// ForOS builds the keyboard and window backends for goos. Nothing is probed here: availability
// is computed per call. macTrusted is the Accessibility check (macax.Trusted).
func ForOS(goos string, h *host.Host, x Exec, macTrusted func() bool) (Keyboard, Windows, error) {
	switch goos {
	case "linux":
		disp := &x11.Display{Getenv: x.Getenv} // the session as Exec sees it (tests: a fake)
		xw := &HostWindows{H: h, SameFn: SameX11, ActiveFn: X11Active(x, disp), HistoryFn: X11Stacking(disp)}
		return &LinuxKeyboard{X: x, Wayland: &vkbd.Keyboard{Getenv: x.Getenv, X11Focused: HyprX11Focused(x)}, X11: &x11.Keyboard{D: disp},
				Portal: &portal.Keyboard{R: &portal.RemoteDesktop{}}},
			&LinuxWindows{X: x, X11: xw, XWM: disp}, nil
	case "darwin":
		return &MacKeyboard{Trusted: macTrusted, Post: NativeMacPoster()},
			&HostWindows{H: h, SameFn: SameMacPid, ActiveFn: MacFrontPid(x), HistoryFn: MacFrontToBack(x)}, nil
	case "windows":
		desktop := func() bool { return h != nil && h.Has(context.Background(), host.CapOpen) }
		return &WinKeyboard{Desktop: desktop, Send: nativeSendInput()},
			&HostWindows{H: h, ActiveFn: nativeForeground(), Why: "switching to a named app isn't supported in this build", HistoryFn: ZOrder}, nil
	}
	return nil, nil, fmt.Errorf("dictate: no backend for platform %q", goos)
}

// Native is ForOS for this OS.
func Native(h *host.Host, macTrusted func() bool) (Keyboard, Windows, error) {
	return ForOS(runtime.GOOS, h, OSExec{}, macTrusted)
}
