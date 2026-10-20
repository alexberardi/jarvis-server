# Desktop recording on X11: RECORD's data connection spoken by hand, AT-SPI for elements and password focus

- **Date:** 2026-10-19 (build agent, plan P3 piece 37)

On Linux `computer_record_start {mode: desktop}` records an X11 session
(`internal/agent/computer/record_x11.go`, `record_linux.go`). It uses the same `DesktopSource` as
macOS ([decision](2026-10-19-desktop-recording-macos.md)), so the events, grouping, masking and
keyframes are the same; only the tap and the element lookups differ. On Wayland it refuses with
"On Wayland I can only record the browser" (`error_code: unsupported`): Wayland lets no client see
another's input, and XWayland would only see X11 apps (Q8). Without any X display it is the
general "I can't record the desktop on this computer yet".

**RECORD without xgb's cookies.** The RECORD extension answers one `EnableContext` request with a
stream of replies, all with that request's sequence number, until another connection disables the
context. jezek/xgb hands a cookie a single reply and then waits for a newer request, so the rest
of the stream would stall its reader. The control connection (`CreateContext` for core
`KeyPress`..`ButtonPress` device events of all clients, `DisableContext`, `FreeContext`, the
keyboard mapping) is ordinary xgb; the data connection is spoken in `internal/agent/x11/record.go`:
the setup handshake (MIT-MAGIC-COOKIE-1 from the Xauthority file, found as xgb does), the
`EnableContext` request (the opcode xgb's `QueryExtension` gave the control connection) and the
replies, parsed into raw device events. Stop disables the context, waits up to 2 s for
`EndOfData`, then closes the connection; no event is sent after stop returns.

**Keys.** The keymap is read when the recording starts. Modifiers are tracked from their own
presses and releases (RECORD's state field can be empty for device events), and the state bits
count too. Shift and Caps Lock pick the level; AltGr (ISO_Level3_Shift) picks the core mapping's
third level and makes no shortcut. Ctrl, Alt and Super make shortcuts, named `ctrl+s`,
`alt+n`, `ctrl+shift+n`, `super+…` (the X11 input backend's chord names, so a replay presses
them). Keysyms other than Latin-1, Unicode and the euro sign type nothing the recording keeps.
Buttons 1 and 3 are left and right clicks; 4–7 are scrolls with positive `dy` down (what the X11
backend's `Scroll` replays); the middle button is skipped.

**Elements.** A click's element is AT-SPI's `GetAccessibleAtPoint` in the app that owns the
topmost window under the point (`_NET_CLIENT_LIST_STACKING`, minimised windows and other desktops
skipped, its `_NET_WM_PID`); the event's `app` is the window's WM_CLASS class. Typing goes to the
active window's focused element: `atspi.Focused` walks the active window for `STATE_FOCUSED` (at
most 2000 nodes) and keeps the one it found, so later keystrokes cost one `GetState` while it stays
focused. X11 events have no `element.ax_path`.

**Passwords.** Text typed while a `passwordtext` has the focus is dropped by the grouper, as on
macOS: an `input` event with `masked: true` and no value. X11 has no secure-input flag, so this
is only as good as AT-SPI: a password typed into an app that doesn't expose AT-SPI (a terminal's
`sudo` prompt, an app with accessibility off, Tk) is recorded as text. Known limit, as is the late
focus lookup the macOS decision names.

**Verification.** `TestX11ClicksRecorded`, `TestPasswordFocusDropped`,
`TestWaylandDesktopRecordingRefused` and `TestX11KeysDecoded` drive the recorder from a fake RECORD
stream; `TestReadRecorded` and `TestHandshake` the byte-level stream. `TestX11RecordLive`
(`JARVIS_X11_RECORD_LIVE=1`) records a throwaway X server while XTEST clicks, types, presses
Enter and Ctrl+S and scrolls; it passed against a rootful Xwayland under headless sway.
