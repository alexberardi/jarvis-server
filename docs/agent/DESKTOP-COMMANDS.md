# Desktop commands: voice instead of the mouse

Status: **approach agreed, not started** (2026-10-24). Question rounds are done; one small item is
open at the bottom. Development waits on the prerequisites below.

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
  LLM. Anything that doesn't match exactly goes to the LLM as today, and the LLM tools accept
  the same actions. Repeat mode follows: "keep scrolling" / "stop" and bare follow-ups ("again",
  "more") for a short window after a hot command, without the wake word. The user's reason:
  "that would give the impression of speed". Phrasing rule: no guessing in the grammar; an
  uncertain match goes to the LLM, so a wrong pre-route is rare and harmless.
- **2026-10-24: one tool per layer, the action as a parameter, risky verbs separate** (Q3,
  option B):
  - `desktop_list_windows`: unchanged.
  - `desktop_window {window, action, monitor?, workspace?}`, absorbing focus and minimize.
  - `desktop_navigate {action, count?, window?}`: the same actions the pre-routes call.
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

  Merge gate: the new utterances join the toolbench corpus. 4B and 9B accuracy must stay at or
  above the current baseline, and the desktop node's prompt may grow by at most ~600 tokens
  (starting number, to revisit with measurements).
- **2026-10-24: native actions only, never emulated** (Q4). If a platform has no real version
  of an action, that action isn't in its enum. User: "i don't like the idea of hacking together
  a minimize on a OS that doesn't support it."
  - **minimize / restore:** only where a true minimize exists (X11, macOS, Windows). Not on
    Hyprland or sway: no special-workspace or scratchpad stand-in.
  - **maximize / fullscreen:** native everywhere (Hyprland `fullscreen 1` / `fullscreen 0`).
  - **snap_left / snap_right, move_to_monitor, move_to_workspace:** nice to have, not required.
    Ship each where it's cheap and native (Hyprland `movewindow l/r`, `movewindow mon:…`,
    `movetoworkspacesilent`; Win32 and X11 geometry). Workspaces stay out on macOS (Spaces) and
    Windows (virtual desktops), which have no public API.
  - **keep_on_top:** not in v1.
- **2026-10-24: which window a command means** (Q7). Resolved on the node, in the desktop tool
  layer (it knows focus and what Jarvis just did). The model passes the words as said
  (`window: "it"`) or leaves the field out. `host` stays policy-free. The user asked that these
  rules be documented; the user-facing copy is in "Talking to windows" below.

  | Said | Means |
  |---|---|
  | nothing, "this", "this window", "current" | the focused window |
  | "it", "that" | the last window Jarvis acted on, if within ~60 s (the repeat-mode window); otherwise the focused window |
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
  | a click from a pre-route ("click 7", exact "click Send") | never: no LLM, nothing could have steered it |
  | an overlay number with no readable label | never from a pre-route; from the LLM, asks in `submit` mode |

  A spoken "yes" through `desktop_confirm` is enough. Because `Risky` is per tool, `all_of_app`
  is held by the node's confirmer, not by the flag.

  The desktop risky-label list starts from `browser/risk.go` and applies the same rule: pay,
  send/post, share and account changes stay. **Delete is dropped for desktop clicks**, because
  a UI delete goes to the trash. (The browser list keeps it: a web app's delete is often final.)
- **2026-10-24: desktop commands don't talk; the island shows what happened** (Q9). User: "it
  would get noisy… you'd then have to wait for it to finish speaking to do your next action."
  - Successful actions (pre-route or LLM, window, navigation, click): **no speech.** The island
    briefly shows the action ("Snapped Firefox left", "Tab 3").
  - Failures: **no speech either.** The island shows the reason ("No tab 9", "Hyprland can't
    minimize"); the error chime plays under the existing `chimes` setting, since a failure
    shouldn't depend on a glance.
  - Fuzzy matches: the island shows what was chosen ("Firefox (heard 'fire fax')").
  - Ambiguous names: the island lists the candidates, numbered, and the user answers by voice
    ("the second one"). That answer is a follow-up turn, inside the repeat-mode window.
  - **Questions are still spoken** ("what's open?", "what's minimized?"), because the answer is
    the point.
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
     (proposed; not yet confirmed by the user), and **Windows 11 in a KVM VM on this box**
     (see Prerequisites). The user has a license and chose a VM over a dual-boot partition.


## Talking to windows (user-facing; goes into the user guide when this ships)

- "Maximize this" / "scroll down" act on **the window in front**.
- "it" and "that" mean **the window Jarvis just touched**, for about a minute: "minimize Slack…
  actually bring it back".
- "The other one" means **the window you were in before**, like Alt-Tab.
- Name an app or window ("the terminal", "Firefox") to pick something else.
- Not every computer can do every action. Hyprland has no minimize, and Macs can't move
  windows between Spaces. Jarvis says so rather than doing something close to it.

## Ideas past this spec

- **Eye tracking** for "this" (user, 2026-10-24: "would be cool… not in scope obviously"). The
  "this" rule above is the place it would plug in: gaze target first, then focus.

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
2. Missing window verbs: restore, maximize, fullscreen, snap/tile (left/right/halves), move to
   another monitor, move to a workspace/desktop, always-on-top, active window ("what's this"),
   close every window of an app, show desktop.
3. Nothing inside an app: scroll, tabs, back/forward, menus, "click Send".
4. Token budget: the prompt diet (`docs/decisions/2026-10-20-prompt-diet.md`) halved prompt
   size. One tool per action across all three layers would add back roughly 2–3k tokens on a desktop node.

## Approach

Build order is L1 → L2 → L3, and each layer ships on its own. Within a layer, the shared
plumbing comes first, then the backends.

### Shared plumbing (before L1)

- **Capabilities.** Add the new names to the closed set in `host/host.go` (`windows.maximize`,
  `windows.fullscreen`, `windows.restore`, `windows.snap`, `windows.monitor`,
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
- **Backends get the new actions** where native (the Q4 rule): EWMH states and geometry on
  X11; AX `AXMinimized` / `AXFullScreen` / position and size on macOS; `ShowWindow` /
  `SetWindowPos` / `MonitorFromWindow` on Windows; `fullscreen`, `movewindow`,
  `movetoworkspacesilent` on Hyprland.

### L2: in-app navigation

- **`desktop_navigate`** sends keys or scroll events to the focused window: scroll, page,
  next/previous/go-to tab, back/forward, zoom. It is built on `internal/agent/input` (X11 XTEST,
  wlroots virtual pointer and keyboard, macOS CGEvent).
- **The pre-route grammar** runs on the node right after STT, before the CC request, and calls
  the same actions. Exact phrases only.
- **Repeat mode** comes after pre-routes: "keep scrolling" / "stop", "again" / "more", in a short
  window without the wake word, built on the existing follow-up listening (`voice/followup.go`).

### L3: pointing at things

- **`desktop_click {target}`** finds an element by name in the accessibility tree (AX on macOS,
  AT-SPI on Linux, UI Automation on Windows) and presses it, reusing what `computer` already
  reads.
- **"Show numbers"** draws a numbered overlay over clickable elements, or over a grid when an
  app exposes no tree. "Click 7" clicks at that spot through `input`.
- **Confirmation** follows the Q8 table through the existing `dictate.Confirmer`, with a
  desktop risky-label list derived from `browser/risk.go` without delete.

### Windows has more to build first

On Windows the agent only has the four Win32 window actions today:

- **no island**: `island/run_other.go` is a stub, so the feedback design has nowhere to show on
  Windows;
- **no input backend**: `input/other.go` returns nil, so L2 needs `SendInput`;
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

A nested Xephyr with an EWMH window manager on the dev box (proposed; see Open).

## Open

- **Xephyr for X11 live checks:** proposed, not yet confirmed by the user.
- **Named layouts** ("work mode"): proposed deferred past v1, since snap and move are
  nice-to-haves themselves. Reopen if the user disagrees.

All ten question rounds are answered; each answer is under Decided with its date.
