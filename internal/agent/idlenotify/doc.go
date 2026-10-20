// Package idlenotify tells when the user last used the mouse or keyboard on a wlroots
// compositor (Hyprland, sway), through ext-idle-notify-v1: one input-idle notification with a
// 1 s timeout on the default seat. The compositor says "idled" once no input came for 1 s and
// "resumed" at the next input; Since turns that into the time of the last input it knows of.
//
// Our own virtual pointer and keyboard events are input to the compositor too: the caller
// (computer.Guard) tells them apart by time, against its own injection windows.
package idlenotify

import "time"

// Timeout is the notification's idle timeout: input after this much quiet is reported at once
// ("resumed"); input sooner than that only shows once it stops (an "idled" a timeout after it).
const Timeout = time.Second

// Interfaces this package binds.
const (
	IfaceNotifier = "ext_idle_notifier_v1"
	IfaceSeat     = "wl_seat"
)
