# A desktop replay opens each step's app; learning keeps every event of a step

- **Date:** 2026-10-24 (agent, item #581; the user's QA of recording rec_d4e0649f541a3dc9)

How a recorded desktop task turns into a workflow and replays. It refines
[2026-10-19-learning-workflows.md](2026-10-19-learning-workflows.md) and
[2026-10-20-naming-steps-by-sight.md](2026-10-20-naming-steps-by-sight.md).

- **Every event of a step is a step.** The model groups events into steps ("type the message"
  = the typing, then Enter). Each event becomes its own step with the model's intent. Keeping only
  the last event threw the typed text away.
- **Opening an app is not a learned step: the replay opens it.** On Wayland the launcher (walker,
  wofi, a bar icon) is a layer surface, not a window. Its clicks and typing are recorded against
  whatever window had the focus, so "open Omawrite" can't be replayed from the events. Before the
  first step in an app (the first step, or one whose app differs from the step before), the replay
  observes that app. The agent then opens it from desktop_open's catalogue if it has no window,
  and gives its window the focus (`Desktop.Focus`, the window control of PR #479), so keys and
  typing go to it. The learn prompt tells the model to leave app launches out. We chose this over
  a learned `desktop_open` step: a launcher sequence can't be told apart reliably in a trace, and
  this also covers an app that is already open but in the background.
- **The user's stop isn't a step.** At most the last 3 events are checked: the last click outside
  the window it was recorded in, with only clicks after it, is the status icon and its menu, so
  it and those clicks are dropped. This goes by geometry alone; the trace's stop reason would
  be better if it reached jarvisd.
- **Every click gets a keyframe on Wayland.** The 1 s keyframe throttle no longer applies to
  clicks whose point the platform marks. Quick clicks ("7", "7", "=") are each a step, and the
  keyframe is the only way to name such a click by sight.
- **A click keeps its point in its window** (`StepWindow.Point`: from the window's top-left,
  with the window's size). A replay's model round gets it as a hint. The action keeps the global
  point, as before.
- **A failed act says why at the top** (`error_code`, `error` in computer_act's answer, which
  the tool-call trace reads). A replay that fails names the step: "Step N (intent) failed: …".
