# Desktop computer tasks: what "attended" means, and the macOS path first

- **Date:** 2026-10-20 (plan P3 piece 22, build agent; the attended rule itself is the user's C9)

How [COMPUTER-CONTROL.md](../agent/COMPUTER-CONTROL.md) C9 ("desktop-app control is attended in
v1") and Q4 are enforced:

- **On the computer (the agent decides, C8).** A desktop run starts, or resumes after a pause,
  only when the screen is unlocked (macOS `CGSessionCopyCurrentDictionary`: not locked, on the
  console) and the user used the mouse or keyboard in the last 5 minutes (the HID idle time).
  Once started, a run is not refused for idle time (the user may be watching it work), but a
  locked screen refuses every batch. The guard watches the whole run and pauses it on the
  user's input; `computer_act {resume: true}` resumes it. Each failure is `not_attended`.
- **On the server.** An errand whose plan has a desktop computer task runs on its own (autorun)
  only when the user asked for it in a turn on a computer that can run it, at most 5 minutes
  before the plan was made (`DraftRequest.TurnAt`), and then it runs on that computer. A
  scheduled errand, or one asked for on a speaker or the phone, gets its plan card and waits for
  Run. A started errand (Run, Continue) is a live start.
- **A new app (Q4)** is an app the run hadn't been in yet (its own app is known from the start):
  allowed, reported as `new_app` on the action's result, said in the run's history ("ok (first
  use of Finder)") and in the audit row's `new_app`. It is per run, not per computer: the agent
  keeps no list of apps across runs.
- **The clipboard (C7)** is two actions, `clipboard_read` and `clipboard_write`, in desktop tasks
  only (a browser task refuses them like any non-element action). What `clipboard_read` read is
  shown to the model once, inside the page markers, and never stored in the run's history.
- **Scope split.** The user's addition of 2026-10-20 (replay steps whose element came from vision
  on Wayland: grim keyframe, vision model, wlroots click, check) depends on #318/#319 and on the
  replay of #265, none of which is on main yet. It is a follow-up item; this piece is the macOS
  accessibility path, where `NativeDesktop` is nil off macOS.
