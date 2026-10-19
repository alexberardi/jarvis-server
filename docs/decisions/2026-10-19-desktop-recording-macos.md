# Desktop recording on macOS: the tap on its own thread, secure input dropped in the tap

- **Date:** 2026-10-19 (build agent, plan P3 piece 36)

The blueprint put the listen-only `CGEventTap` on the island's run loop. It runs on a run loop
of its own instead: a goroutine locked to a new OS thread (`internal/agent/computer/record_darwin.go`).
The island's main thread isn't always running AppKit's loop (a CLI process, a test), a slow
callback there would stall the island and the status icon, and an event tap works on any thread's
run loop. Stopping is `CFRunLoopStop` plus a flag the loop checks every 0.25 s, and the stop
waits for the thread to end, so no event arrives after a recording stopped.

The callback only decodes the event and queues it. Accessibility lookups (the element under a
click, the focused element) and keyframes run on a worker goroutine, so an app that doesn't
answer AX can't make macOS disable the tap; a tap disabled by timeout is re-enabled anyway.

Secure input is dropped at two points. While `IsSecureEventInputEnabled()` is on, the callback
never reads a key's characters (only Enter, Tab and Escape are named). Text typed into an
`AXSecureTextField` is dropped by the grouper before it reaches the trace. Both cases leave an
`input` event with `masked: true` and no value.

Desktop events keep the browser trace's fields (`seq`, `ts`, `kind` click/input/key/scroll,
`element` role and name, `value`, `masked`) and add `app` (bundle id), `element.ax_path`, the
click's or scroll's `x`/`y`, `button: right` and the scroll's `dx`/`dy`. Shortcuts are `key`
events named like `cmd+n`. Keyframes are `<id>.<seq>.jpg` next to the trace (0600), at most one
every 2 s, and `Delete` removes them with the trace.

Input Monitoring is a fifth grant on the setup page. Like Screen Recording, macOS applies it
only to a fresh process, so its status comes from `jarvis-agent permission input-monitoring`.
