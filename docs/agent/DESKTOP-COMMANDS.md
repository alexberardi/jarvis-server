# Desktop commands: voice instead of the mouse

Status: **approach agreed, not started** (2026-10-24). All questions are answered. Development
waits on the prerequisites below.

## North star

A user can realistically **talk to Jarvis instead of using their mouse**. Every routine
pointer action on the desktop (switch window, arrange windows, scroll, pick a tab, press a
button, choose a menu item) has a spoken path that is fast and dependable enough to be the
default, not a party trick.

## Decided

- **2026-10-24: first-party Go commands compiled into `jarvis-agent` (option B).** Go/WASM
  Pantry packages (A/C) are deferred. Keep the window API shaped so it could later be exposed to
  packages, but don't build that now.
- **2026-10-24: scope is all three layers (L1 windows, L2 in-app navigation, L3 element
  targeting), built in that order** (Q1, option B). Windows alone won't replace the mouse, and
  the computer-control task loop plans every step with the LLM, which is too slow for "scroll
  down". L3 reuses the AX / AT-SPI / input code `computer` already has. Each layer ships usable
  on its own.
- **2026-10-24: node-side pre-routes plus repeat mode** (Q2, options B + C). A small, exact
  grammar of hot phrases ("scroll down", "page down", "next tab", "tab 3", "go back", "zoom in",
  "show numbers", "click 7", "undo") is matched on the node right after STT and runs without the
  LLM. (The node already gets the transcript back from its own `Transcribe` call before it
  posts the turn to CC, so this is a check between the two.) Anything that doesn't match exactly
  goes to the LLM as today, and the LLM tools accept the same actions. Repeat mode follows:
  "keep scrolling" / "stop" and bare follow-ups ("again", "more") for a short window after a hot
  command, without the wake word. The user's reason:
  "that would give the impression of speed". Phrasing rule: no guessing in the grammar; an
  uncertain match goes to the LLM, so a wrong pre-route is rare and harmless.
- **2026-10-24: one tool per layer, the action as a parameter, risky verbs separate** (Q3,
  option B):
  - `desktop_list_windows`: unchanged.
  - `desktop_window {window, action, monitor?, workspace?}`, absorbing focus and minimize.
  - `desktop_navigate {action, count?, window?}`: the same actions the pre-routes call
    (scroll, page, tabs, back/forward, zoom, undo).
  - `desktop_click {target}`: by name, or an overlay number; "show numbers" included.
  - `desktop_close_window {window, all_of_app?}`: stays its own tool because `Risky` is per tool.

  **The action enum is computed per call from the host's capabilities** (the user's idea): each
  action maps to one capability in the closed set (`windows.maximize`, `windows.snap`,
  `windows.workspace`, `nav.scroll`, …), and `Available()` renders only the actions this host
  can do right now. The model never sees `move_to_workspace` on macOS, and a tool whose enum is
  empty isn't offered at all. `Call` still checks the action against the live set and answers
  `not_supported`, because capabilities can change between rendering and calling (a revoked
  Accessibility grant). Capabilities rarely change, so the per-call enum doesn't churn the
  prompt cache.

  **2026-10-24: an action this host lacks gets a generic island message.** A filtered enum
  means the model can't *call* an action this host lacks, so "minimize Slack" on Hyprland would
  otherwise come back as spoken text and break Q9. Every action enum therefore carries one
  fixed extra value, `unsupported` ("the user asked for an action that isn't listed"). The
  model calls e.g. `desktop_window {action: "unsupported"}`, and the island shows **"Action not
  supported on this OS/Host"** with the error chime. The value is the same on every host, so it
  costs a few tokens and doesn't change with capabilities. `Call` answers `not_supported` with
  the same message when a real action vanished between rendering and calling. The toolbench
  corpus gets "can't do that here" utterances and near-misses, so the gate catches a model
  picking `unsupported` where it should ask or use a real action. (User, 2026-10-24.)

  Merge gate: the new utterances join the toolbench corpus. 4B and 9B accuracy must stay at or
  above the current baseline, and the desktop node's prompt may grow by at most ~600 tokens
  (starting number, to revisit with measurements).
- **2026-10-24: native actions only, never emulated** (Q4). If a platform has no real version
  of an action, that action isn't in its enum. User: "i don't like the idea of hacking together
  a minimize on a OS that doesn't support it."

  "Native" here means the platform's real window state or command where one exists. For pure
  geometry (halves of the screen, another monitor) a resize counts: it is exactly what the OS
  does behind the scenes (user, 2026-10-24). What stays out is a stand-in that behaves
  differently from the real thing, like a scratchpad workspace posing as minimize.
  - **minimize / unminimize:** only where a true minimize exists (X11, macOS, Windows). Not on
    Hyprland or sway: no special-workspace or scratchpad stand-in.
  - **maximize / fullscreen, and leaving them (`unmaximize`):** a separate action from
    unminimize, because "restore" means both and Hyprland has one but not the other.
    Hyprland and sway: native fullscreen (Hyprland's fullscreen mode 1 is its maximize).
    X11: EWMH `_NET_WM_STATE_MAXIMIZED_*` / `_FULLSCREEN`. Windows: `ShowWindow`
    `SW_MAXIMIZE` / `SW_RESTORE`. macOS: `fullscreen` is `AXFullScreen`, the real
    one that moves the app to its own Space. **macOS has no maximize state**, so `maximize`
    resizes the window to the screen's visible frame (minus menu bar and Dock), which is what
    macOS 15's own Window → Fill does. Not the zoom button: its size is up to each app. The node
    remembers the frame from before, so `unmaximize` restores it; a window Jarvis didn't
    maximize has nothing to restore and answers `not_supported` (user, 2026-10-24).
  - **snap_left / snap_right, move_to_monitor, move_to_workspace:** nice to have, not required.
    Ship each where it's cheap and native: Hyprland `movewindow l/r`, `movewindow mon:…`,
    `movetoworkspacesilent`; sway's equivalents; geometry for snap and
    `move_to_monitor` on Win32, X11 and macOS (AX position and size). Windows' Snap Layouts
    and macOS 15 tiling have no public API, so a half-screen resize is how snap ships there;
    the only visible difference is that Windows' snap assist doesn't offer the other half
    (user, 2026-10-24). Workspaces stay out on macOS (Spaces: no public
    API) and Windows (`IVirtualDesktopManager::MoveWindowToDesktop` is public but can't list or
    create desktops, so "workspace 2" can't be resolved).
  - **keep_on_top:** not in v1.

  On a Hyprland configured in Lua (0.55+, as on this box), `hyprctl dispatch` takes Lua
  (`hl.dsp.*`) and the classic dispatcher strings above are syntax errors. Each Hyprland action
  is tried in the classic form, then the Lua one, as `dictate/linuxwin.go` does for focus.
- **2026-10-24: which window a command means** (Q7). Resolved on the node, in the desktop tool
  layer (it knows focus and what Jarvis just did). The model passes the words as said
  (`window: "it"`) or leaves the field out. `host` stays policy-free. The user asked that these
  rules be documented; the user-facing copy is in "Talking to windows" below.

  | Said | Means |
  |---|---|
  | nothing, "this", "this window", "current" | the focused window |
  | "it", "that" | the last window Jarvis acted on, if within ~60 s; otherwise the focused window. (Its own memory, not the repeat-mode window, which is seconds long.) |
  | "the other one", "the previous one" | the window focused before this one (Hyprland and X11 focus history; macOS and Windows stacking order) |
  | a name ("Slack", "the terminal") | matched against the window list, as today (`winmatch`) |

  Jarvis's own windows (tray, island, setup) never count as focused.
- **2026-10-24: confirmation only for what costs time or money and has no confirmation or undo
  of its own** (Q8). User: "we should limit asking for confirmation on basically anything that
  would cost the user time or money where a confirmation wouldn't typically exist." Apps already
  prompt before discarding unsaved work, and deleting through the UI goes to the trash.

  | Action | Asks? |
  |---|---|
  | window actions, navigation | never |
  | close one window | never. `Risky` comes off `desktop_close_window`'s single-window path |
  | close every window of an app (`all_of_app`) | yes |
  | `desktop_click` from the LLM | only for a risky label, under `--confirm-dictation=submit` |
  | a click from a pre-route ("click 7", "click OK") | never: no LLM, nothing could have steered it |
  | a pre-route click whose label is risky ("click Send", or an overlay number on a "Send" button) | yes, under `submit`: it isn't a pre-route. The grammar hands the turn to the LLM, whose `desktop_click` goes through the confirmer as usual (user, 2026-10-24) |
  | an overlay number with no readable label | never from a pre-route; from the LLM, asks in `submit` mode |

  A spoken "yes" through `desktop_confirm` is enough. Because `Risky` is per tool, `all_of_app`
  is held by the node's confirmer, not by the flag.

  The desktop risky-label list starts from `browser/risk.go` and applies the same rule: pay,
  send/post, share and account changes stay. **Only the plain delete words are dropped for
  desktop clicks** ("delete", "remove", "trash", "discard"), because those usually go to the
  trash or an app's own undo or confirm dialog. The rest of `risk.go`'s `delete` category stays
  risky, because it costs money or can't be undone: "unsubscribe", "deactivate", "cancel
  subscription", "cancel membership", "cancel order", "close account", "empty trash", "erase",
  "wipe", plus "delete permanently" / "delete forever". That means splitting the category, not
  filtering it out whole. (The browser list keeps all of it: a web app's delete is often final.)
- **2026-10-24: desktop commands don't talk; the island shows what happened** (Q9). User: "it
  would get noisy… you'd then have to wait for it to finish speaking to do your next action."
  - Successful actions (pre-route or LLM, window, navigation, click): **no speech.** The island
    briefly shows the action ("Snapped Firefox left", "Tab 3").
  - Failures: **no speech either.** The island shows the reason ("No tab 9", "Action not
    supported on this OS/Host"); the error chime plays under the existing `chimes` setting, since a failure
    shouldn't depend on a glance.
  - Fuzzy matches: the island shows what was chosen ("Firefox (heard 'fire fax')").
  - Ambiguous names: the island lists the candidates, numbered, and the user answers by voice
    ("the second one"). That answer is a follow-up turn, but with its own listen window: the
    3 s follow-up cap (`MaxFollowUpListenSecs`, #296) is too short to read a list and answer.
    **`choice_listen_seconds`**, default 8 s, capped at 30 s, set like
    `follow_up_listen_seconds` (`agent.json`, a flag, the app's node settings). Configurable
    because the right length varies by user (user, 2026-10-24). It opens once the island has
    drawn the list, applies only while the list is up, and closes on the answer.
  - Failures and fuzzy matches also show in text, so a user who isn't looking still has the
    chime as a cue.
  - **Questions are still spoken** ("what's open?", "what's this?", "what's minimized?"),
    because the answer is the point. `desktop_list_windows` returns each window's focused and
    minimized state so these can be answered.
  - Implementation note: a node tool result needs a way to say "the island said it, don't
    speak", so CC ends the turn without a composing LLM call or TTS (like the not_for_me silent
    path and the tool-loop's message fast path, `docs/cc/02-tool-loop.md`). Pre-routes never
    reach CC, so they only need the island.
- **2026-10-24: test plan** (Q10).
  1. **Contract suite** (`host/contract_test.go`) on the Linux, macOS and Windows CI jobs, for
     every new action, including the native-only rule: a backend that lists an action does it;
     one that doesn't returns `not_supported`.
  2. **Unit tests** for the pre-route grammar (exact only; near-misses go to the LLM), the
     this/it/other-one rules, capability-filtered enums and the confirmation table. Fake clock,
     no wall-clock waits.
  3. **Toolbench gate** as in Q3.
  4. **Live, by voice, through the product with the island visible:** this box (Hyprland
     0.56), the MBP (macOS), X11 in a nested Xephyr with an EWMH window manager on this box
     (user, 2026-10-24; an X11 desktop VM if Xephyr proves too annoying), and **Windows 11 in a
     KVM VM on this box**
     (see Prerequisites). The user has a license and chose a VM over a dual-boot partition.


## Talking to windows (user-facing; goes into the user guide when this ships)

- "Maximize this" / "scroll down" act on **the window in front**.
- "it" and "that" mean **the window Jarvis just touched**, for about a minute: "minimize Slack…
  actually bring it back".
- "The other one" means **the window you were in before**, like Alt-Tab.
- Name an app or window ("the terminal", "Firefox") to pick something else.
- Not every computer can do every action. Hyprland has no minimize, and Macs can't move
  windows between Spaces. Jarvis shows that on screen rather than doing something close to it.

## Ideas past this spec

- **Eye tracking** for "this" (user, 2026-10-24: "would be cool… not in scope obviously"). The
  "this" rule above is the place it would plug in: gaze target first, then focus.
- **Named layouts** ("work mode": Chrome left, Slack right, terminal on monitor 2; Q6): not
  in v1. Realistically a workflow built from the window primitives in this spec (snap,
  move_to_monitor, focus, launch), not a feature of its own (user, 2026-10-24).
- **Spoken key combos** ("press control shift T", "press Windows D"): modifiers on
  `desktop_press_key`, which today takes single keys from a fixed list. The key senders exist
  on every platform (SendInput in `dictate`, CGEvent, XTEST / wlroots virtual keyboard). A
  combo can do anything (Ctrl+Enter sends), so it would follow the Q8 confirmation rules. It
  would also give show desktop where its shortcut is fixed (Win+D on Windows); macOS's F11 is
  rebindable and Linux depends on the desktop (user, 2026-10-24).

## Where we are today (main @ 8c58f315)

| Layer | What exists |
|---|---|
| `internal/agent/host` | `Backend` with list / minimize / focus / close, volume, open, launch. Capabilities are a closed set, computed per call. |
| Linux backend | X11 only (built-in EWMH, wmctrl/xdotool fallback). **On Wayland, including Hyprland and sway, it reports no window capabilities at all.** |
| macOS backend | Accessibility API; ids are `pid/index`. |
| Windows backend | Win32 (`HWND`). |
| `internal/agent/desktop` | One node tool per capability: `desktop_list_windows`, `desktop_minimize_window`, `desktop_focus_window`, `desktop_close_window` (Risky), plus open/apps/volume. `window_id` accepts a spoken name, resolved by `winmatch`. |
| Elsewhere | `dictate/linuxwin.go` already focuses windows via `hyprctl`/`swaymsg`. `computer/desktop_linux.go` lists Wayland windows, captures with grim, and reads elements via AT-SPI; `ax` does the same on macOS. These belong to the computer-control task loop (`COMPUTER-CONTROL.md`), not the voice desktop tools. |

Gaps against the north star:

1. **Wayland/Hyprland has zero voice window control**, while the code to do it already sits in
   `dictate` and `computer`.
2. Missing window verbs: unminimize, maximize/unmaximize, fullscreen, snap/tile
   (left/right/halves), move to another monitor, move to a workspace/desktop, always-on-top,
   active window ("what's this"), close every window of an app, show desktop. Q4 covers most of
   these; always-on-top and show desktop are out of v1 (user, 2026-10-24: "no one really uses
   that"), and "what's this" is answered from `desktop_list_windows` (Q9).
3. Nothing inside an app: scroll, tabs, back/forward, menus, "click Send".
4. Token budget: the prompt diet (`docs/decisions/2026-10-20-prompt-diet.md`) halved prompt
   size. One tool per action across all three layers would add back roughly 2–3k tokens on a desktop node.

## Approach

Build order is L1 → L2 → L3, and each layer ships on its own. Within a layer, the shared
plumbing comes first, then the backends.

### Shared plumbing (before L1)

- **Capabilities.** Add the new names to the closed set in `host/host.go` (`windows.maximize`,
  `windows.fullscreen`, `windows.unminimize`, `windows.unmaximize`, `windows.snap`, `windows.monitor`,
  `windows.workspace`, `nav.*`, `click.*`), with one map from action to capability, the same
  shape as `ActionCapability` today.
- **Per-call enums.** `node.Param` already carries `Enum`. `desktop.Tools.Available()` builds
  each tool's action enum from the live capabilities and drops a tool whose enum is empty.
  `Call` re-checks the action.
- **Window references.** A small resolver in `desktop` in front of `resolveWindow`:
  focused, last-acted (60 s), previous-focus, then `winmatch` by name. Backends add `Focused()`
  and a focus-history or stacking order call. Jarvis's own windows are filtered out.
- **Island feedback.** Desktop results go to `island.Note`. A result field tells CC the island
  covered the turn, so CC ends it with no composing call and no TTS. Questions keep speaking.
- **Toolbench.** Add the new utterances to the corpus; the gate is from Q3.

### L1: windows

- **`desktop_window`** replaces `desktop_minimize_window` and `desktop_focus_window`.
  `desktop_close_window` gains `all_of_app` (held by the confirmer) and loses `Risky`.
- **Hyprland and sway move into `host`.** A Wayland-compositor backend inside `Linux`, chosen
  per call by the environment checks `dictate/linuxwin.go` already does (Hyprland via
  `hyprctl -j` and the 0.56 Lua dispatch form; sway via `swaymsg`). Then `dictate` and
  `computer` switch to it, so there is one implementation.
- **Backends get the new actions** where native (the Q4 rule): EWMH states on X11, plus
  geometry for snap and `move_to_monitor`; AX `AXMinimized` / `AXFullScreen`, plus AX position and size for maximize (fill), snap and
  `move_to_monitor`, on macOS; `ShowWindow`, and `SetWindowPos` / `MonitorFromWindow` for snap and
  `move_to_monitor`, on Windows; `fullscreen`, `movewindow`, `movetoworkspacesilent` on
  Hyprland (classic, then Lua form).
- **`desktop_list_windows`** gains each window's focused and minimized state (Q9's spoken
  questions).

### L2: in-app navigation

- **`desktop_navigate`** sends keys or scroll events to the focused window: scroll, page,
  next/previous/go-to tab, back/forward, zoom. It is built on `internal/agent/input` (X11 XTEST,
  wlroots virtual pointer and keyboard, macOS CGEvent).
- **The pre-route grammar** runs on the node right after STT, before the CC request, and calls
  the same actions. Exact phrases only.
- **Repeat mode** comes after pre-routes: "keep scrolling" / "stop", "again" / "more", in a short
  window without the wake word, built on the existing follow-up listening (`voice/followup.go`).
  Two things that code doesn't do today:
  - **A new trigger.** Follow-up opens after a spoken reply, waiting for its tail to end. A
    desktop command has no reply (pre-routes never reach CC; Q9 silences the rest), so the
    window has to open when the action finishes, with no quiet-wait.
  - **False-trigger guard.** "again" and "more" are common words, and a desktop often has a
    video or call playing. In repeat mode, only an exact match against the repeat grammar acts;
    anything else closes the window without going to CC. The node's existing self-hearing
    check still applies, and if speaker ID is on, only the speaker who gave the hot command
    counts.
- **"Keep scrolling"** runs until "stop", the window closing, or a cap (proposed 30 s), so a
  missed "stop" can't scroll forever.

### L3: pointing at things

- **`desktop_click {target}`** finds an element by name in the accessibility tree (AX on macOS,
  AT-SPI on Linux, UI Automation on Windows) and presses it, reusing what `computer` already
  reads.
- **"Show numbers"** draws a numbered overlay over clickable elements, or over a grid when an
  app exposes no tree. "Click 7" clicks at that spot through `input`.
- **Confirmation** follows the Q8 table through the existing `dictate.Confirmer`, with a
  desktop risky-label list derived from `browser/risk.go` minus the plain delete words (Q8).

### Windows has more to build first

On Windows the desktop tools have only the four Win32 window actions today:

- **no island**: `island/run_other.go` is a stub, so the feedback design has nowhere to show on
  Windows;
- **half an input backend**: `dictate` already sends keys with `SendInput`
  (`dictate/sendinput_windows.go`), but `input/other.go` returns nil, so L2's scroll and
  pointer events need `SendInput` mouse input in `input`, and keys should move there too;
- **no UI Automation client**, so L3 by name needs one, plus the overlay.

L1 on Windows is cheap. L2 and L3 on Windows each start with that groundwork, and Windows can
trail the other platforms without holding them back.

## Prerequisites for development

Nothing below has been done yet. The user chose to hold it until development starts
(2026-10-24).

### Windows 11 test VM on the dev box

- **Install** (the user runs it; it needs sudo):
  `sudo pacman -S --needed swtpm passt qemu-audio-pipewire qemu-ui-gtk qemu-hw-display-virtio-gpu`.
  Already present: `qemu-base` 11.1, `qemu-system-x86`, `edk2-ovmf`, and `/dev/kvm`.
- **Media:** the Windows 11 ISO from Microsoft's download page (needs a browser; the user has a
  license) and the virtio-win driver ISO.
- **Shape:** QEMU/KVM as the user's own session (`qemu:///session`), not system libvirt. Its
  audio then goes straight to the user's PipeWire for mic and speaker passthrough, and no group
  change (no re-login) is needed. OVMF with Secure Boot plus `swtpm` for Win11. 8 vCPU / 8 GB /
  80 GB thin disk; the box (i7-13700KF, 24 threads, 31 GB, ~155 GB free) can spare it. **Two
  virtual monitors** so `move_to_monitor` gets tested.
- **Wiring:** the guest's jarvis-agent points at the host's jarvisd by manual config URL; mDNS
  doesn't cross the VM's NAT (same as the Android emulator). jarvisd must listen on an address
  the guest can reach.
- **Reset:** snapshot "clean + agent installed", and revert before each run.
- **Deliverable:** a launch script, plus a short runbook in this doc.

### X11 test session

A nested Xephyr with an EWMH window manager (openbox) on the dev box: `xorg-server-xephyr
openbox`, a couple of apps inside, and the agent pointed at that display. It tests window
actions on X11; voice and the island still come from the real session. If it proves too
annoying, an X11 desktop VM (e.g. Xubuntu) instead (user, 2026-10-24).

## Open

Nothing. Every question is answered and under Decided with its date.

Question rounds: Q1–Q4 and Q7–Q10 are under Decided. Q5 (workspaces and Spaces) was settled by
Q4's native-only rule, and Q6 (named layouts) is under Ideas past this spec. The option-B
decision came before the rounds were numbered. A review round on 2026-10-24 settled the
unsupported-action message, risky pre-route clicks, snap geometry, macOS maximize, show
desktop, `choice_listen_seconds` and the X11 test session.
