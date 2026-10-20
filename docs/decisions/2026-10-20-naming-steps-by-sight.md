# Naming desktop steps by sight

- **Date:** 2026-10-20 (#319; the split into #340/#342/#343/#344 and this pass is the user's)

A desktop click with no accessibility element (Wayland without AT-SPI, an X11 or macOS lookup
that came back empty) has only its point, app and window. **Before** the learn call,
`nameClicksBySight` (`internal/modules/cc/workflows_learn_sight.go`) shows the vision model
each such click: its keyframe (ringed on a marked frame, otherwise with the click's screen
point in the text) and the crop when there is one. The model also gets the window's class and
title and the events on either side. It answers what the trace would have recorded: the
control's role and visible label, the app, and what the click did. The answer becomes the
event's element, so the learn prompt reads `click button "Send" (seen on the screen) in …`
instead of `click in … at (x,y)`. The learnt step records the element with `source: "vision"`,
the model's `confidence` and no AX path. A replay finds it by sight: the element is the hint
of the step's model round, as before. The label is classified like an AX one, so a step on
"Send" or "Delete" asks first. When the learn call gave a step no intent, "did" ("clicked Send")
becomes its intent. Clicks with an AX element are left alone and their keyframes aren't sent.

- **Why before the learn call, not after:** the first build named the built steps after the
  learn call. A live run on a Files recording showed that this is too late. The text model saw
  three or four identical `click in org.gnome.Nautilus at (339,125)` lines and folded them into
  one step. Naming the clicks first gives the learn model what it needs to split and title the
  steps. It costs vision calls for clicks the learn model may then drop.
- **Which model:** `visionLabel`, so background when it has vision, otherwise live. When
  neither has vision, nothing is guessed. The draft keeps its points, and the learnt card says
  "The model has no vision, so I couldn't name N clicks from the screen". A failed vision call
  doesn't fail the learning either: the card says so. Both are logged.
- **Batching:** at most `sightMaxClicks` (4) clicks per call, so at most 8 images. A small
  context window gets fewer, at roughly 2300 tokens a click after a 2000-token reserve, but
  never fewer than one.
- **The race with the keyframes:** the trace is stored, and learning queued, before the agent
  sends the keyframes (at once, or on its 5-minute sweep). The learn job waits on the server
  side: while a click with no element has a `keyframe_point` (the agent took its keyframe) that
  isn't stored yet, and the recording is younger than `keyframeWait` (10 min), the job returns
  a transient error and the queue retries it. It doesn't wait when there is no queue, no blob
  store or no vision. The job now has 10 attempts with the 2 s doubling backoff capped at 5 min,
  which covers about 13 minutes, past the wait. After the wait it learns without the missing
  keyframes, and their clicks keep their points. A marker from the agent ("last keyframe sent")
  was the other option. It was rejected because it needs an agent change, and an agent that is
  offline would hold the draft for up to a day.
- **Not done:** the server doesn't draw a ring or cut a crop on an unmarked X11 frame; the
  frame goes with the click's screen point in the text. A click whose keyframe the throttle
  skipped stays point-only.
