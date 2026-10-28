# Voice control of the desktop: handoff (2026-10-27)

Where "deprecate the mouse" stands after three evenings of real use on the Linux dev box
(Omarchy/Hyprland, Firefox, a Bluetooth headset), and what is still broken. The design is in
[VOICE-FLOWS.md](VOICE-FLOWS.md) (Q13–Q32) and [DESKTOP-COMMANDS.md](DESKTOP-COMMANDS.md) (L1–L3).
Replace this file with a fresh one at the next handoff; it is not a log.

## The goal

Quick, accurate voice interactions that replace the mouse and keyboard. Two rules from the
decisions shape everything:

- **A funnel, cheapest stage first** (Q27). Stage 1 runs on the computer with no model (a small
  grammar over live names, microseconds, any CPU); stage 2 narrows the tools on the server
  (embeddings, Q28, not built); stage 3 is the model; stage 4 a computer task.
- **Steered steps are direct; delegated work is a goal** (Q32). "Click 7", "scroll down" act at
  once and deterministically. Whenever the model acts, success is the state the tools observed,
  never its reply.

## Where the code is

| What | Where | State |
|---|---|---|
| Tabs/sheets by name (AT-SPI), stage 1 switch/type/scroll, chaining, the settings-gate fix | PR #509 | **merged** 2026-10-26 |
| Voice flows spec Q13–Q28 | PR #500 | **merged** |
| Everything below, plus Q32's spec entry (it missed #500) | branch `feat/voice-numbers-q32`, worktree `.claude/worktrees/views` | pushed, **no PR** (user: don't merge yet) |

On `feat/voice-numbers-q32`:

- **Numbers on the page** (`internal/agent/browser/hints.go`, extension `hints` command, 0.3.5):
  shown automatically while a voice turn listens with the browser in front, redrawn after a step
  changes the page, taken down when the turn ends (`node.TurnWatcher`, forwarded by
  `node.Multi` and `nodesettings.Gate`). "7" / "click 7" presses; "type ABC into 29" fills;
  risky labels and unlabeled elements ask first. `browser_fill` / `browser_click` take a
  `number` so the model can do "fill out 33 with a summary of me".
- **Web in the user's browser** (`browser/direct_web.go`): "search for X on Google / in a new
  tab / in Firefox", "google X", "open a new tab", "go to example.com" open a new tab, no model.
  `desktop_open` sends http(s) links to the extension (the system handler opened new windows).
- **Scrolling** (`browser/direct.go`): stage 1; reports `at_end` instead of a fake success.
- **Q32 server check** (`internal/modules/cc/followthrough.go`, `textfilter/screen.go`): a reply
  claiming an on-screen action with no successful action tool this turn is nudged once.
- **Listening**: one speech threshold per turn from the room's noise (noise × 2.5, 60–1000) for
  the command and every follow-up; onset is 3 chunks in every window; a reply ending in a
  question keeps the window 8 s (else 3 s); a window that hears nothing logs
  `follow-up: no speech onset` with the levels.

## What is running on the dev box

- **Agent**: this branch, `~/.local/lib/jarvis-agent/jarvis-agent` (user unit `jarvis-agent`).
  Rebuild: `CGO_ENABLED=0 mise exec go@1.27 -- go build -o … ./cmd/jarvis-agent`, copy to
  `jarvis-agent.new`, `mv` over, `systemctl --user restart jarvis-agent`.
- **Extension**: 0.3.5 loaded as a *temporary* add-on (`about:debugging` → Load Temporary
  Add-on → `internal/agent/browser/extension/manifest.json`). Gone on a Firefox restart; after an
  extension change click **Reload** there. Unsigned: a permanent install needs
  `scripts/browser-ext-sign.sh` (uploads to Mozilla; ask the user first).
- **jarvisd**: still the release **v0.1.0-rc18.10** at `/usr/local/bin/jarvisd`. The server-side
  pieces of this branch (Q32 check, and #506's cut-off/interjection fixes) are **not live**. A
  build from this branch exists for the user to swap in (no new migrations since rc18.10, so
  rolling back is restoring the binary).
- **Native messaging host**: the rc18.10 installer had registered it at a deleted
  `/tmp/jarvis-agent-install.*` path, so a reloaded extension couldn't connect. Repointed by hand
  to `~/.local/lib/jarvis-agent/jarvis-agent` in the Firefox, Chrome, Chromium and Brave
  manifests (`.bak` beside each).

## Open issues, most blocking first

1. **None of the latest build is verified by voice.** Auto numbers, type-into-number, the new
   threshold and the redraw after a step are tested only with fakes and scripted audio. The
   first test: wake + "search for dogs on Google", then without the wake word "4", "scroll
   down", "12", "type hello into 7". Then grep the agent journal for `direct: done on the node`
   and `follow-up: no speech onset` (verify-production-wiring).
2. **Follow-ups on a quiet mic.** The cause was found (a Bluetooth headset's speech peaked at
   154–240 RMS against a 200 floor; later a short "7" against a 5-chunk onset) and fixed, but not
   confirmed by voice. If it still misses, the onset log says why.
3. **Badge placement unseen.** Iframes, sticky headers and pages that re-render on scroll may put
   numbers in the wrong place or leave them stale; numbers only cover what was on screen when
   drawn.
4. **jarvisd not swapped** (above): the Q32 nudge can't fire until it is.
5. **Installer bug**: the native host path points into the installer's temp dir, and the agent's
   own startup repair (`refreshBrowserHosts`) skips an install under `~/.local/lib` because it
   isn't a bundle. Every Linux user hits this when the extension reconnects.
6. **Model routing misses.** With the browser not connected, "search for dogs on Google" became
   the server's web search, read aloud. "In the message" once resolved to the Google Messages app.
   Stage 1 covers the common phrasings now; stage 2 (Q28) is the real fix.
7. **A second node answers wakes** ("standing down: another node answers this wake"); one test
   wake was lost to it. Q25 (wake arbitration) is the open spec question.
8. **Q32 is half built.** Claims are checked; the short goal loop (act, observe, retry within
   ~3 steps / 10 s) is not.
9. **`scripts/check` tests only changed packages**: it missed a stale
   `tools/pipeline/toolsets/*.json`. Run `scripts/check --all` before pushing tool-spec changes.

## Not started

- Numbers for desktop apps outside the browser (AT-SPI snapshot + overlay; DESKTOP-COMMANDS L3).
- macOS AX and Windows UIA views; Windows has no island or pointer input yet.
- Stage 2 embedding narrowing (Q28); stage-1 actions recorded in the server conversation.
- Spec questions Q25, Q29–Q31, one at a time, at the principle level.
- `chat_template_kwargs` is sent to OpenAI endpoints (`internal/modules/llm/client.go`).

## Lessons from these sessions

- **Verify in the product, not the tests.** Stage 1 never ran for a day because
  `nodesettings.Gate` didn't forward the Director interface; the tests used the bare toolset.
  Every new voice-loop interface gets a compile-time assert on the Gate.
- **Check the environment before the code.** The two worst failures tonight were a deleted
  native-host binary and a quiet Bluetooth mic, not logic.
- **Log what the user can't see.** "The blue ring shows and nothing happens" was undiagnosable
  until the window logged its peak level.
