# ccsuite's macOS desktop lane: the Mac's own session, and Notes.app as the fixture

- **Date:** 2026-10-24 (plan P3 piece 22, build agent; follows
  [2026-10-20-desktop-tasks-attended.md](2026-10-20-desktop-tasks-attended.md))

Piece 22 shipped the macOS desktop executor (#346) but left `tools/ccsuite` unable to run its
`desktop-mac` case: `skipReason` returned "macOS desktop cases are not wired yet (piece 22)"
unconditionally, so even `--include-pending` could not reach `notes_macos`. This records how the
lane is wired now. The suite itself is [../dev/computer-suite.md](../dev/computer-suite.md).

**The suite joins the Mac's own session; it does not make one.** On Linux a desktop case gets a
private world (headless sway or Xvfb under its own `dbus-run-session`, its own
`XDG_RUNTIME_DIR` and HOME), so a run never touches the user's screen. macOS has one window
server per login session and no headless one, so there is nothing equivalent to start: a
`desktop-mac` case runs in the Mac test host's logged-in session, on the screen in front of
whoever is there. That is consistent rather than accidental — C9 already says desktop-app
control is attended in v1 — but it makes the Mac lane a **by-hand lane**: it is not in
`scripts/e2e computer`'s overnight Linux run, and the Mac host is expected to be idle and
watched while a case runs. The `desktop` struct's session process is therefore nil on the Mac,
and tear-down leaves the session alone.

Falling out of that: on macOS the browser, teach, wlroots and x11 cases are skipped ("Linux
only: this is the Mac test host"), because sway, Xvfb and the Linux Chromium build they need are
the dev box's; on Linux a `desktop-mac` case is skipped as before.

**Privacy grants come from the terminal, not from the agent's path.** macOS gives a process
started from a terminal that terminal's TCC grants, so the `jarvis-agent` the suite builds fresh
into `.ccsuite/<run>/bin` needs no grant of its own — which it could not keep anyway, since its
path changes every run. The same already holds for `guard_darwin_test.go`. So the requirement is
on the terminal ccsuite is started in: Accessibility, Screen Recording, and Automation for
Notes. Bring-up probes all three without prompting (`internal/agent/macax`) and refuses with one
line naming what to turn on, rather than letting a case fail ten minutes later with a vague
"the agent could not click".

**The fixture is Apple's Notes.app, not `tools/ccsites/notesapp`.** The point of the Mac case is
a real native app driven through the accessibility tree, and the case has said Notes.app since
it was written. The cost is that the fixture is the user's own data, so the suite is deliberately
narrow with it, through AppleScript (`tools/ccsuite/macnotes.go`):

- before a case it deletes only notes whose name is one the case's `note_exists` checks ask for
  (`Groceries`), and writes down the id of every note that exists;
- afterwards it reads back only notes that were not in that snapshot, so no other note's title
  or text reaches the suite, `state.json` or a report.

`setup.notes_layout` has no meaning there (it belongs to the Tk fixture), and a note's HTML body
is reduced to its text before the checks see it.

**Rejected:** running the Tk fixture app on the Mac instead. It would have reused the existing
reset and state file and avoided Apple Events entirely, but it would have made `notes_macos` a
second `notes_x11` — it would no longer test the macOS accessibility path against a real app,
which is the only reason to have a Mac test host.
