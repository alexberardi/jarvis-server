# Wayland desktop recording: turn accessibility on, name clicks, mark apps without it

- **Date:** 2026-10-24 (build agent, item #580, reopening #343)
- **Why:** the user's QA on Hyprland (recording `rec_d4e0649f541a3dc9`, foot, a Qt/QML app,
  Omacalc, Nautilus) had no accessibility data on any event, and no `context: none` marker
  either, although the AT-SPI bus was running.

This extends [Desktop recording on Wayland](2026-10-20-desktop-recording-wayland.md).

**Accessibility is switched on at recording start.** `atspi.Enable` reads the session bus's
`org.a11y.Status` and sets `IsEnabled` to true when it is off, as a screen reader (Orca) does.
Qt and Chromium apps only join the accessibility bus when it is on (or with
`QT_LINUX_ACCESSIBILITY_ALWAYS_ON=1` in their own environment, which the agent can't give apps
it didn't start); GTK 4 always joins. It is left on after the recording: switching it off would
drop apps from the bus while the user may rely on it, and a screen reader would leave it on too.
Its cost is the toolkits' accessibility work in each app. An app that only reads the property at
start may need a restart to join.

**The start logs the setup once** (Info, `desktop recording: accessibility`): whether
`DBUS_SESSION_BUS_ADDRESS` is set, where the bus address came from (`AT_SPI_BUS_ADDRESS` or
`org.a11y.Bus`), `IsEnabled` and `ScreenReaderEnabled`, whether the agent turned it on, and the
error when the bus can't be reached (then every event is `context: none`, as before).

**Clicks are named on Wayland.** The click asks AT-SPI for the element under it in the focused
window (the click focused it), with `ATSPI_COORD_TYPE_WINDOW` and the point less the window's
corner from the compositor: a Wayland app doesn't know where its window is on the screen. A click
outside the focused window's box (a popup) is not looked up and stays for the keyframes.

**`context: none` per app.** When the bus is up but the event's app isn't in its registry
(foot, a Qt app that didn't join), that event says `context: none`, so learning knows there was
no element rather than seeing a silent gap; typing into such an app is treated as masked when
learning, as it already was for a recording without the bus. The failure is logged once per app
per recording (Info, `desktop recording: no accessibility for an app`).
