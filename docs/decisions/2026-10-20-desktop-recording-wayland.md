# Desktop recording on Wayland (wlroots): evdev input, compositor context, marked keyframes

- **Date:** 2026-10-20 (build agent, item #318)
- **Asked by:** the user, 2026-10-20: "I'm on Linux so we need to solve it." They approved the
  approach: record what Wayland allows and let the vision model make sense of it, with the same
  trace (`mode: desktop`), upload and Recordings page as macOS and X11.

On a Wayland session `computer_record_start {mode: desktop}` (and `jarvis-agent computer record
start --desktop`) now records Hyprland and sway (`internal/agent/computer/record_wayland.go`,
wired in `record_linux.go`, which picks it whenever `WAYLAND_DISPLAY` is set). It uses the same
`DesktopSource` as macOS and X11 ([macOS](2026-10-19-desktop-recording-macos.md),
[X11](2026-10-19-desktop-recording-x11.md)), so grouping, masking and the trace are the same;
what differs is where the input and the context come from. This replaces the Wayland refusal of
the X11 decision and settles COMPUTER-CONTROL.md Q8 for wlroots compositors.

**Input from evdev, behind the input group.** Wayland shows no client another's input, so the
keys and buttons come from the kernel: `internal/agent/evdev` lists `/sys/class/input/event*`,
keeps the devices whose key capabilities have the letters and Enter (keyboards) or a left button
(mice, clickpads), and reads their `input_event`s with the kernel's timestamps. The nodes are
`root:input 0660`, so the agent's user must be in the `input` group: Linux's counterpart of
macOS's Input Monitoring. The agent never asks for sudo: the refusal (`ErrInputGroup`), the setup
page's "Keyboard and mouse (the input group)" step and `jarvis-agent permission
input-monitoring` all give the one command the user runs, `sudo usermod -aG input $USER`, then
log out and back in (a group applies to a new login). Being in `input` lets every process of
that user read every keyboard, so it stays opt-in and is only asked for desktop recording.

**Keys.** The desktop's layout is read from the compositor (the `wl_keyboard.keymap` vkbd
already reads, `vkbd.Keyboard.Layout`; US when it can't), and evdev codes go through it as XKB
keycodes (code + 8, `xkb.Layout.Sym`). Modifiers are tracked from their own presses; Shift and
Caps Lock pick the level, autorepeat types again. Ctrl, Alt and Super make shortcuts named like
X11's (`ctrl+n`, `ctrl+shift+t`, `super+r`); right Alt is AltGr when the layout makes it
ISO_Level3_Shift, and since the layout keeps levels 1 and 2 only, an AltGr character is `other`.
Enter, Tab, Escape, `shift+tab` and Backspace are as on X11. Left and right buttons are clicks
(button + timestamp); the wheel is scrolls; the middle button, touch and motion are nothing.
Typing is grouped per focus exactly as on macOS and X11.

**Where: the compositor.** Each click and scroll asks for the cursor at once: Hyprland's
`hyprctl cursorpos`; sway has no IPC for the cursor (`get_seats` gives no position), so there a
click has no `x`/`y` and its keyframe no point. Each event carries the focused window
(`window: {class, title, x, y, w, h}` from `hyprctl -j activewindow` or sway's focused node;
`dictate.LinuxWindows.FocusedWindow`), and `app` is its class. A click has no `element`: AT-SPI's
GetAccessibleAtPoint takes screen coordinates that Wayland apps don't know, so the click is
named by the window and the marked keyframe instead (the vision model's job, #319).

**What: AT-SPI per typing group.** The focused window's process is asked for its focused
element (`atspi.Focused`); its role and name name the typing group, and a password text
(`Secure`) masks it: the text never leaves the recorder, the event says `masked: true`. When the
accessibility bus can't be reached at the start, every event says `context: "none"`, nothing can
be masked, and the start says so (`warning` in `computer_record_start`'s output, and on the CLI's
line): typed text is kept as it is, passwords too.

**Keyframes.** grim through the screen toolset (the focused monitor), at most one a second (macOS
and X11 keep two; `DeskPlatform.KeyframeEvery`), throttled on the inputs' own timestamps. A
click's keyframe is taken as soon as the worker gets the click (right after the press: evdev is
read before the app has redrawn much, but "just before" isn't possible without a continuous
capture); a click that ends a typing group takes the keyframe of that moment instead of the
typing. Each keyframe marks the click (else the cursor, when known) with a red ring, the event
gets `keyframe_point: {x, y}` in the sent JPEG's pixels (after the 1568 px scaling), and a
256 px crop at full resolution is kept next to it as `<id>.<seq>.crop.jpg`. Both are 0600 and go
with the recording (Delete globs `<id>.*.jpg`; LoadTrace finds the crop again). The upload still
attaches at most four keyframes (jarvisd takes four images per tool result); the crops stay on
the computer until #319 decides how the vision model gets them. (Superseded: each click's
keyframe and crop now go up after the trace, [2026-10-20-recording-keyframes.md](2026-10-20-recording-keyframes.md).)

**GNOME and KDE refuse for now.** No wlr-screencopy for grim and no compositor IPC for the
cursor or the focused window: `ErrWaylandRecording` ("On Wayland I can record the desktop on
Hyprland and sway; on GNOME and KDE I can only record the browser for now", `unsupported`). The
portals (ScreenCast, and GNOME's introspection) are the later path.

**Not covered.** Touchpad taps (libinput makes them clicks; evdev only shows the touch), so
tap-to-click users' clicks are missed unless they press the pad. A session lock or a
layer-shell launcher isn't a window AT-SPI or the compositor names, so text typed there is
attributed to the last focused window and is not masked; a recording should be stopped before
locking the screen. Typing into XWayland apps works (evdev sees all keys); their AT-SPI focus
works as on X11.

**Tests.** `record_wayland_test.go`: fake evdev streams (levels, Caps Lock, autorepeat,
shortcuts, clicks, wheel), fake hyprctl and swaymsg output through the real `LinuxWindows`,
grouping per focused element and window, the password mask, no AT-SPI (`context: none` and the
warning), keyframe throttling and the click's priority, marked frames and crops (point mapping
through the capture's geometry and the scaling), sway without a cursor, the GNOME refusal and the
refusal without the input group. `evdev_test.go`: frame parsing, capability bitmaps, device
listing from a fake sysfs, open errors. `TestWaylandRecordLive` (`JARVIS_RECORD_WAYLAND_LIVE=1`)
records the real desktop while the person at it clicks and types.
