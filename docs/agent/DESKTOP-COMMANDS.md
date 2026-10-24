# Desktop commands: voice instead of the mouse

Status: **spec in progress** (question rounds with the user, one at a time; queue at the bottom).

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
   size. One tool per verb would add back roughly 1–2k tokens on a desktop node.

## Draft shape (will change as questions are answered)

- **L1: windows.** Full verb set on all four backends (X11, Hyprland/sway, macOS, Windows),
  with Hyprland/sway moved into `host` so `dictate` and `computer` share one implementation.
- **L2: in-app navigation without targeting.** Scroll, page, tab next/previous/N, back/forward,
  zoom, by sending keys or scroll events to the focused window.
- **L3: targeting elements.** "Click Send", "open the File menu", by name from the
  accessibility tree (AX / AT-SPI / UIA), with a numbered-overlay fallback ("show numbers",
  "click 7") for apps that expose no tree.

## Question queue

1. ~~Scope~~: answered, all three layers (see Decided).
2. ~~Speed~~: answered, pre-routes + repeat mode (see Decided).
3. **Tool shape against the token budget:** one `desktop_window {window, action, …}` tool vs.
   one tool per verb, and how toolbench gates it. *(asked 2026-10-24)*
4. Hyprland tiling semantics: what do "maximize", "snap left" and "move to the other monitor"
   mean in a tiling compositor (fullscreen 1 vs. a float, `movewindow l`)?
5. Workspaces and Spaces: macOS Spaces and Windows virtual desktops have no public API. Ship
   per-platform, or leave them out?
6. Named layouts ("work mode": Chrome left, Slack right on monitor 2): in scope, and where are
   they stored?
7. "This window", "that one", "the other one": pronoun and recency resolution, and whether the
   last-acted window stays sticky.
8. Risk: which verbs need a confirmation (close all of an app, quit), and whether a voice
   confirmation is enough.
9. Feedback: how the user knows it worked without looking: spoken reply, sound, or silent on
   success.
10. Test plan: the contract suite per backend, live tests on this box (Hyprland), MBP and the
    Windows box, and the toolbench corpus for new utterances.
