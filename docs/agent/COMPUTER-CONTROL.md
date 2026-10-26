# Computer control: Jarvis does tasks on your computer, and learns them from you (plan)

Status: **planned, 2026-10-18; widened 2026-10-19** (general tasks, Teach Jarvis). Nothing here is built yet. This is the input for the loop's
planning flow (brief → blueprint → pieces); the decisions below were made with the user on
2026-10-18, and the open questions carry a recommendation each for the brief to settle.

Paths are relative to the repo root. "The agent" is the Go desktop node (`cmd/jarvis-agent`,
`internal/agent/`, [DESIGN.md](DESIGN.md)); "cc" is `internal/modules/cc/`.

## Goal (user's words, 2026-10-18)

> "Research the best smart watch in my budget and order it for me." It needs to be as autonomous
> as possible. That's the point.

> (2026-10-19) That's just a single use case. I heard a story about somebody using something for
> budgeting. … The ability to screen record you completing an action and then Jarvis learns how
> to do it.

The smart watch is one example, not the scope. Others the design must serve: a monthly budget
(download statements from bank and card sites, categorise them into a spreadsheet, report
overspending), filling forms, file and spreadsheet chores, and tasks inside desktop apps. And a
user who would rather **show** Jarvis a task once than describe it (C10).

What OpenAI ships as "Computer Use" (the ChatGPT/Codex desktop app, macOS and Windows), done
locally: the household's own server plans and drives, the user's own computer executes, and
nothing is hidden from the household.

## Decisions (2026-10-18, user)

| # | Decision |
|---|---|
| C1 | **General computer tasks, errand-shaped.** v1 targets multi-step tasks of any kind (shopping, budgeting and finance chores, forms, file and spreadsheet work, app tasks), not only short "open X and do Y" commands, and not shopping only. Research runs server-side with the existing web tools; acting happens on the computer. |
| C2 | **Autonomy-first.** Inside a computer task everything is allowed by default. The household narrows it with an **editable list** (allow / ask / block per action, app or site), stored in the existing rule store (`cc_tool_rules`, `policy.Decide`). Defaults: a short deny list and confirmation only at real-risk moments (below); both editable. |
| C3 | **Browser work goes through the existing extension** (WebExtension + native messaging into the user's real, signed-in browser), not a separate Jarvis browser profile or CDP. A task that targets a browser window acts through the extension, never through desktop clicks, so per-site rules can't be bypassed. Flatpak/Snap browsers stay unsupported as today. |
| C4 | **Verify, don't trust "done".** After the model says a task is done, Jarvis checks the result itself: a fresh screenshot, an accessibility/DOM read-back, a file that exists, an order confirmation page. |
| C5 | **No Docker sandbox target in v1.** Most users won't have Docker. A disposable VM is for our own eval suite (P0), not a product feature. |
| C6 | **Computer control is a tool set inside the errand runtime** (`cc/errands`: plan, checkpoints, the "Ran on its own" card, autonomy log), not a separate system. |
| C7 | **Inside a computer task** the agent may screenshot after every action, use modifier shortcuts and use the clipboard (read and write). This lifts agent invariant 25 (screenshot only for the turn that asked), the dictation clipboard ban and the no-shortcuts rule **for computer tasks only**; dictation and plain voice turns keep them. |
| C8 | **Split of duties:** jarvisd runs the loop and owns policy; the agent executes batched actions and **enforces** the deny list, per-app/per-site rules and confirmations locally, so a bad model output can't bypass them. Approvals are minted by cc, bound to the target, and re-checked at execution, never granted by model text. |
| C9 | **Attended vs unattended:** browser errands (through the extension, in a background tab, by element) may run unattended. Desktop-app control (real cursor and keyboard) is attended in v1: it runs with the user present and pauses on their input. |
| C10 | **Teach Jarvis (record and replay).** The user can demonstrate a task once; Jarvis records what they did (structured actions, not just video), learns a reusable, parameterised **workflow**, shows it for review, and replays it on demand, from a routine or on a schedule, verifying every run (C4). Recordings stay on the user's computer and household server with a retention setting; passwords are never captured. (OpenAI shipped the same idea as Record & Replay, macOS, 2026-06.) |
| C11 | **A per-computer opt-in, default off** (user, 2026-10-19). Jarvis never takes over a computer implicitly from something you said: each computer turns computer control on with **"Computer control"** in the agent's menu, right after "Listen for "Hey Jarvis"" (also `jarvis-agent computer on\|off`, the control socket). Off on every install and after an upgrade; saved in agent.json (`computer_control`). The agent reports it to jarvisd (heartbeat capability `computer_control`) and **enforces** it (C8): while off it refuses every computer action, browser and desktop target alike, a macro's replay included (`computer.Switch.Allow`); turning it off mid-task stops the task as "Stop Jarvis" does. jarvisd offers a computer's computer tools only while its switch is on and sends computer-task errands only to such computers; with none on, a request that needs one gets "Computer control is off. Turn it on with "Computer control" in the Jarvis Automation menu on <computer>", never an errand or another fallback (with phone calls on, such a goal still goes to the errand planner, which may do it by phone with `make_phone_call` and never by computer). The admin's Nodes page shows the state, read-only. **Recording is not gated by the switch** (user, 2026-10-19): "Record a macro…", right below "Computer control" in the menu ("Stop recording" while it runs), works with the switch off and doesn't turn it on, because each recording is an explicit act and the indicator shows while it runs; turning the switch off doesn't end a recording ("Stop Jarvis" does). **By voice** (#273): `enable_computer_control`, `disable_computer_control` and `computer_control_status` ("enable computer control", "is computer control on?"), and `start_recording` / `stop_recording` ("record this macro", "record a macro called pay the water bill", "watch me do this", "learn how I do this", "I'm done", "stop recording"). They act on the computer you're talking to; from another device (a Pi, the phone) on the computer you name, else your only one, and ask which when you have several. Turning control on and recording reach only the speaker's own computers (the member signed in there) from elsewhere; turning it off works from anywhere, on any of the household's computers, and stops a running task. The switch changes through the agent (node tool `computer_switch`, the menu's own path: agent.json, the tray, the heartbeat), and Jarvis says what it did ("Computer control is on for the MacBook."). `stop_recording` without a name for the macro (none from the start either) asks for one and leaves the recording going. "Teach Jarvis" stays the feature's name in docs and the admin. |

## What exists (survey, 2026-10-18)

- **Screen capture** `desktop_read_screen {scope, window?}`: Hyprland/sway (grim) and macOS
  (screencapture + CGWindowList). None on GNOME, KDE, X11 or Windows. Images ride in the tool
  result and are dropped after the turn (decision 2026-10-12).
- **Keyboard** `desktop_type_text`, `desktop_press_key`, `desktop_confirm`: wlroots virtual keyboard,
  GNOME/KDE RemoteDesktop portal (keyboard only), X11, macOS, Windows SendInput. A short fixed key
  set; no modifier shortcuts by design.
- **Pointer input: none on any platform.**
- **Window control / app launch:** X11 EWMH, macOS Accessibility (AX), Windows user32; `desktop_open`
  with a catalogue policy. Generic Wayland has no window control.
- **Browser:** 11 `browser_*` tools (tabs, navigate, read_page with untrusted-text markers, scroll,
  find, fill, click by label). DESIGN §10 "basic first, agentic later" has the element-ref plan.
- **Accessibility trees:** not read anywhere (macOS AX only for the trust check).
- **Policy:** `policy.Decide` (goal/watch rule → household rule → the tool's
  `run_without_confirmation` → ask) applies to background work only (A2, A7); the live tool loop
  ignores `is_risky`. `cc_autonomy_events` logs autonomous actions.
- **Errands:** plan → card → run → step checkpoint → replan → done; autorun through rules with the
  "Ran on its own" card. Errands don't lift tool images today (`errands.go`, `signals_reactions.go`).
- **Loop budget:** the live loop is sized for voice (256 output tokens/step, 5–10 rounds,
  10–25 s per tool call).
- **Models:** Qwen3.5-9B and Qwen3.8-27B with vision (mmproj) in the catalogue; remote models via
  the existing remote support.
- **macOS:** signed, notarized `.app` bundles with TCC checks for Screen Recording and Accessibility
  (the Agent DMG, release rc16).

## Design

### Loop (cc)

A **computer task** is a durable errand step kind: the planner emits it for goals that need the
computer; the runner drives it as a loop on the durable queue:

1. observe: a screenshot plus, where available, an element snapshot (accessibility tree / page
   snapshot with element refs);
2. ask the model for the next **batch of actions** with a one-line `intent` each;
3. policy check per action (C2, C8); confirmation cards at the point of risk;
4. send the batch to the agent; it executes in order and stops at the first failure;
5. repeat until the model reports done, then **verify** (C4); budget and stop limits apply.

Step budget, wall-clock budget, image-history pruning (keep the last N screenshots), stop/cancel,
and a progress stream (app, island) are part of the runtime. Background errands must lift tool
images (today they don't).

### Action schema (agent)

One internal schema close to OpenAI's batched `actions[]`: `click {x,y,button,keys?}`,
`double_click`, `triple_click`, `drag {path}`, `move`, `scroll {x,y,dx,dy}`, `keypress {keys[]}`,
`type {text}`, `wait {ms}`, `screenshot`, `zoom {region}`; plus element actions `press {ref}`,
`set_value {ref, value}`, `navigate {url}`; every action carries `intent`. A small adapter per
model maps its coordinate convention (absolute pixels, 0–999, 0–1000, 0–1) to the capture.
**The agent owns the coordinate math:** every capture returns its origin, logical size, scale
factor (HiDPI, fractional) and monitor, so a point on the downscaled image maps back to global
input coordinates.

### Grounding

Structure first, pixels as the fallback: element refs from the page (extension) or the
accessibility tree (macOS AX first, then Windows UIA, Linux AT-SPI) are far more reliable for an
8–27B local model than coordinate guessing. Vision covers canvas apps and anything without
accessibility data.

### Browser (extension)

The DESIGN §10 "agentic later" plan: `browser_snapshot` with element ids, actions by id, tab
screenshots (`captureVisibleTab`) for the vision fallback, per-site allow/ask/block using the same
rule store as desktop (C2), and logins through the browser's saved sessions; Jarvis never types a
password the model can see.

### Safety

- **Default deny list** (editable, C2): system lock/logout/shutdown/quit-all; OS security, admin
  and password prompts (never answered); driving a terminal; driving Jarvis itself.
- **Point-of-risk confirmation by default** (editable, C2), from OpenAI's documented tiers:
  deleting data; changing sharing, permissions or API keys; CAPTCHAs; installing or running
  downloaded software; sending, posting or submitting to a third party; subscribing or
  unsubscribing; financial transactions; OS security/VPN/password settings. "Typing sensitive
  data into a form counts as sending it." Group imminent risky actions into one confirmation.
- **Pre-approved by the request itself:** logging into a site the user named (saved session),
  browser permission prompts, age gates and "are you sure?" dialogs, uploads/moves/renames.
- **Screen and page content is untrusted;** only the user's own words grant permission. On
  suspected injection: stop and ask.
- **While controlling the desktop (C9):** an always-visible "Jarvis is controlling" indicator
  (island + tray), auto-pause the moment the user touches mouse or keyboard, "stop" by voice and
  by hotkey. On macOS (`computer.Guard`): HID activity outside our own injection windows
  (+500 ms: macOS 26 counts our private-source events as HID activity, stamped late), or the
  pointer > 3 px from where our last action left it or sent it (a move may land after the read
  that follows it), polled every 100 ms,
  pauses the task; ⌃⌥⎋ (Carbon hotkey, named in the tray tooltip) stops it. On Linux: idle-notify
  (wlroots) or the X server's idle time, and Ctrl+Alt+Esc (X11 grab, GlobalShortcuts portal, or a
  Hyprland bind); DESIGN.md §17.
- **Voice stop (#288):** the agent stops the computer itself, at once, when the whole command is a
  stop phrase: `computeruse.IsStopPhrase` ("stop", "cancel", …) for a control task, in a wake
  turn; `computeruse.IsRecordingStopPhrase` (that list plus "stop recording", "I'm done",
  "that's it", "done", …) for a recording, in a wake turn or a follow-up. A follow-up never stops
  a control task: it stays held through the follow-ups, where "cancel that" answers the
  conversation. Either way the turn still goes to jarvisd with `computer_activity`
  (`{kind, task, stopped}`, sent with every voice turn). While it says recording, the prompt says
  so, a recording stop phrase is never dropped as not for Jarvis (the follow-up gate is skipped
  for it only while recording) and ends with `stop_recording` (jarvisd calls it if the model
  doesn't), and no reply may claim the recording stopped unless it did. The island shows the task
  as a coloured dot beside any voice state
  ([decision](../decisions/2026-10-19-voice-stop-recording.md)).
- **Audit:** every action (intent, target, result) in `cc_autonomy_events`; screenshots kept
  locally with a retention setting.

### Teach Jarvis (C10)

1. **Record.** "Jarvis, record this macro", a hotkey or the tray's "Record a macro…". The indicator shows recording. The user
   does the task once, ideally narrating ("now I'm downloading last month's statement"); the
   narration is transcribed and is the best signal of *why* each step happens.
   Built (piece 29, #273): `start_recording` / `stop_recording` ("record this macro", "watch me", "I'm
   done"), the menu's "Record a macro…" / "Stop recording" (not gated by computer control, C11) and `jarvis-agent computer record start|stop`; browser
   only for now, 30 minutes at most; a recording stopped on the computer is uploaded to
   `POST /api/v0/computer/recordings` ([decision](../decisions/2026-10-19-agent-recording.md)).
   Desktop on macOS (piece 36): `computer_record_start {mode: desktop}` or `jarvis-agent computer
   record start --desktop`, through a listen-only event tap that needs the Input Monitoring grant
   (the setup page asks for it) ([decision](../decisions/2026-10-19-desktop-recording-macos.md)).
   Desktop on X11: built (piece 37): the same, through the RECORD extension and AT-SPI, with no
   grant to ask for ([decision](../decisions/2026-10-19-desktop-recording-x11.md)).
   Desktop on Wayland, Hyprland and sway (#318): keys and buttons from evdev (the user in the
   `input` group: the setup page and `jarvis-agent permission input-monitoring` give the
   `sudo usermod -aG input $USER && jarvis-agent restart` step, and a start without it refuses with
   it; no logout: a `run` whose user is in the group but whose process isn't re-execs itself
   through `newgrp input`, #571), the cursor and
   focused window from the compositor, AT-SPI's focused element per typing group (a password text
   is masked; without AT-SPI `context: none` and a warning that typed text is kept), and a
   keyframe at most every second with the click marked and a 256 px crop. GNOME and KDE refuse
   (`unsupported`, Q8) ([decision](../decisions/2026-10-20-desktop-recording-wayland.md)).
   jarvisd's `start_recording` still starts the browser. Desktop traces are learnt into steps
   that target the recorded app and window (#342); each step records its AX/AT-SPI element
   (`source: ax`) when the event had one (#343).
2. **Capture structure, not just pixels.**
   - Browser (extension): each element acted on (role, accessible name, a stable selector),
     values typed (password and sensitive fields masked, never stored), navigations, downloads
     and uploads.
   - Desktop (agent): app and window, the element under each click from the accessibility tree,
     keystrokes grouped into text, a keyframe screenshot per step. Secure input fields are
     never captured. Built on macOS (piece 36): events `click`, `input`, `key`, `scroll` with
     `app` (bundle id) and `element.ax_path`; keyframes at most every 2 s. Built on X11 (piece
     37): the same events, `app` the window's WM_CLASS, AT-SPI roles, no `ax_path`.
   - Stored on the computer and the household server only, with retention; viewable and deletable.
3. **Learn.** The model turns the trace (plus narration) into a workflow: named steps with an
   intent and a target each; **parameters** for what varies (dates, file names, amounts), which
   it confirms with the user ("which of these change each time?"); the **success check** (what
   the end looked like); and risk points (send, pay, delete) that get confirmations per C2.
4. **Review.** The workflow is shown as a readable step list; the user renames, edits, adds or
   removes steps, and saves it.
5. **Replay.** By voice ("do my monthly budget"), from a routine or on a schedule (the existing
   scheduler). Replay acts on the recorded elements first (fast and deterministic); when the UI
   changed, it falls back to the model and vision, asks when unsure, and verifies against the
   success check. A successful run can update the stored targets (the workflow heals itself).

Workflows are household data like routines: owned by the member who taught them, shareable
within the household, and governed by the same rules (C2) as any computer task.

## Open questions (each with a recommendation)

| # | Question | Recommendation |
|---|---|---|
| Q1 | What happens at checkout by default? | One confirmation on the phone at the very end (product, price, seller, ship-to, card) with Approve/Cancel; plus an optional editable auto-approve spend limit per household (off by default). |
| Q2 | Default model for computer tasks? | The largest local vision model the household has loaded (Qwen3.8-27B on prod), Qwen3.5-9B as the fallback, and a cloud model as an easy per-errand opt-in. Re-decide with P0's numbers. |
| Q3 | Do computer tasks started live (voice/chat) go through `policy.Decide` too? | Yes: a computer task is an autonomous loop wherever it starts, so the same rules apply; A2's "live turns aren't gated" stays true for ordinary single tool calls. |
| Q4 | First use of a new desktop app: ask or allow? | Allow (C2, autonomy-first), with the app shown in the progress stream and the audit log; the household can switch it to ask. |
| Q5 | Platform order for desktop control? | macOS first (signed app, TCC, Mac mini/MBP test hosts), then Linux wlroots and X11 (this box), then Windows, then GNOME/KDE (no pure-Go PipeWire client). Foreground cursor everywhere in v1; no private macOS background APIs. |
| Q7 | Where do workflows live, and can they be shared? | In jarvisd (cc), as household data next to routines: owned by the member who taught them, shareable within the household, exportable later (Pantry is a possible home for public ones; not v1). |
| Q8 | Desktop recording on Wayland? | Browser recording first everywhere. Desktop recording on macOS (AX notifications + event tap under the existing grants), then X11 and Windows. On Wayland (user, 2026-10-20): record what Wayland allows and let the vision model make sense of it: on Hyprland and sway, evdev input behind the `input` group, compositor context, AT-SPI and marked keyframes ([decision](../decisions/2026-10-20-desktop-recording-wayland.md)); GNOME and KDE browser-only until the portals. |
| Q6 | Go building blocks? | robotgo v1.3.6 pure-Go tags and vcaesar/screenshot (ScreenCaptureKit via purego), pinned and wrapped behind our own interface; purego AX; go-ole UIA (own layer); godbus AT-SPI (own client). No cgo (decision 2026-10-06). |

## Phases (suggested pieces; the blueprint decides)

- **P0 Measure first:** a repeatable task suite on a throwaway VM, a test store and a test bank
  site (search, compare, add to cart, check out with a test card; download a statement and
  categorise it into a spreadsheet; fill a form; a desktop app task; record a task, then replay
  it with a changed parameter and a moved button), scored per model, run on demand and overnight. Gives Q2 real numbers and catches regressions.
- **P1 Browser errands (unattended):** `browser_snapshot` + actions by ref + tab screenshots +
  per-site rules; the computer-task loop in cc using only the browser; verify step; Q1's checkout
  confirmation and spend limit; progress in the app; audit log. Delivers the smart-watch example.
- **P2 Desktop executor (macOS):** pointer input, coordinate model, full key set and shortcuts,
  clipboard inside computer tasks, macOS AX element snapshot, indicator, auto-pause on input,
  stop hotkey/voice.
- **P3 Desktop breadth:** Linux wlroots + X11 (capture, pointer, AT-SPI), then Windows (capture,
  pointer, UIA; never elevated windows), then GNOME/KDE.
- **P4 Rules UI:** the editable allow/ask/block list (actions, apps, sites), spend limit and
  retention in the admin and the mobile app (with the watches/autonomy switches, loop #225/#226).
- **P5 Teach Jarvis (C10):** browser recording through the extension → learn a workflow
  (parameters, success check, risk points) → review UI → replay by voice/routine/schedule with
  self-healing; then desktop recording (macOS first, Q8). Builds on P1 (element refs) and P2.

## Benchmarks, for expectations

OSWorld-Verified (369 real desktop tasks, strict final-state checks; humans ~72%): Qwen3.5-9B
~42%, Qwen3.5-27B ~56%, the best cloud models ~70–75% (research report, 2026-10-18, not re-run
by us). Short, well-defined tasks do much better than the average; long chains are where the
larger model or a cloud model earns its keep. P0 replaces these with our own numbers.

## Out of scope for v1

Unattended desktop-app control (C9), a Docker sandbox target (C5), macOS background control via
private SkyLight APIs, elevated Windows windows (UIPI/uiAccess), and Linux Flatpak/Snap browsers.
