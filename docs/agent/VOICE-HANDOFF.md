# Voice control of the desktop: handoff (2026-10-28)

Where "deprecate the mouse" stands after four evenings of real use on the Linux dev box
(Omarchy/Hyprland, Firefox), and what is still broken. The design is in
[VOICE-FLOWS.md](VOICE-FLOWS.md) (Q13–Q32), [DESKTOP-COMMANDS.md](DESKTOP-COMMANDS.md) (L1–L3) and
[the 2026-10-28 decision](../decisions/2026-10-28-on-screen-names-first.md). Replace this file
with a fresh one at the next handoff; it is not a log.

## The goal

Quick, accurate voice interactions that replace the mouse and keyboard. Three rules shape
everything:

- **A funnel, cheapest stage first** (Q27). Stage 1 runs on the computer with no model (a small
  grammar over live names, microseconds); stage 2 narrows the tools on the server (embeddings,
  Q28, not built); stage 3 is the model; stage 4 a computer task.
- **Steered steps are direct; delegated work is a goal** (Q32). "Click Images", "scroll down" act
  at once and deterministically. Whenever the model acts, success is the state the tools
  observed, never its reply.
- **What's on screen is matched first** (2026-10-28). The page's elements are a short list that
  can be matched quickly and for certain; a site or app could be called anything, so opening
  one by name is the fallback.

## Where the code is

| What | Where | State |
|---|---|---|
| Tabs/sheets by name (AT-SPI), stage 1 switch/type/scroll, chaining, the settings-gate fix | PR #509 | **merged** 2026-10-26 |
| Voice flows spec Q13–Q28 | PR #500 | **merged** |
| Numbers on the page, web phrasings, Q32 server check, listening thresholds, Q32 spec entry | branch `feat/voice-numbers-q32` (up to `83829d5d`) | pushed, **no PR** (user: don't merge yet) |
| Everything in "Since the last commit" below | worktree `jarvis-server/.claude/worktrees/views` | **uncommitted**, on this disk only |

Agents: work in your own worktree off the branch once the work is committed; never edit this
worktree or switch branches in a shared checkout.

### Since the last commit (uncommitted)

- **Names without an overlay** (`browser/hints.go`, extension snapshot). While a turn listens, the
  extension reads visible controls, roles and viewport positions; it draws nothing. It also
  captures up to 200 characters from the nearest semantic container without `innerText`, and
  skips editable subtrees. Voice snapshots do not read form values, so text the user is drafting
  is not included. Context reads are cached per container.
- **Matching** (`hintState.ref`): exact unique names; after a verb (click, tap, press, open,
  select, choose, pick, go to) or in "type X into Y", also a unique name prefix and a clearly
  distinct sound-alike (`winmatch.Similarity` ≥ 0.8, 0.1 ahead of the next control). Repeated
  duplicate links with the same destination resolve to the first ref because either opens the
  same result; other repeated names are left to contextual matching. "The … box/field/button" is
  normalized. A literal "click" in the final sentence is caught even if earlier speech preceded
  it, and a miss stays on the page handler. Whisper substitutions ("quick", "good", "clique",
  "klick", "clip") only become clicks when a target is found or needs clarification.
- **Video result ordinals** (`hintState.ordinalRef`). "First video" (and, on a recognized video
  results page, "first link/result") counts unique video destinations in visible page order, so a
  thumbnail and title for the same video count once. Other ordinal descriptions go to the picker.
- **The voice funnel's narrow model pick** (`browser/pick.go`). When direct matching cannot settle
  a stage-1 browser click or field, the live model gets the target description and a bounded list of ids, names,
  roles, positions and nearby text, wrapped as untrusted page content, via jarvisd's node chat
  with no tools. The first valid JSON object is parsed even when fenced or surrounded by prose;
  an id and estimated confidence are required, and only a concrete choice at ≥ 0.75 acts. A
  low-confidence concrete choice asks the user to repeat the command with another detail. A
  model answer of `none` stays a miss regardless of confidence, so typing into another app still
  passes through. Dictated field text is never sent to the picker. An id outside the live snapshot
  is ignored. Model-issued `browser_click` and `browser_fill` calls use the normal extension path
  after direct name resolution; they do not start a second model call. The local agent log records
  a bounded list of candidate names, roles and video-link markers on each model pick.
- **Video and audio** (`browser/direct_media.go`): "click the video" toggles, "play", "pause",
  "stop", "mute", "skip ahead 30 seconds", "go back ten seconds", "rewind", "restart the video",
  through the existing `media` command. No player on the page: passed on.
- **Order of stage 1** (`cmd/jarvis-agent/main.go`, `build`): browser first, then desktop
  (tabs/windows/apps), screen, dictation. Within the browser: media, page controls, web, scroll.
- **Google phrasings** (`browser/direct_web.go`): "search Google for X", "open Google (in a new
  tab) and search for X", "go to Google", "open Google".
- **Onset** (`voice/followup.go`): 3 loud 80 ms chunks among the last 5, not 3 in a row (a
  Bluetooth mic drops chunks mid-word); the no-onset log reports `most_loud` and `span`.

## What is running on the dev box

- **Agent**: built from the uncommitted worktree at 20:22 on 2026-10-28,
  `~/.local/lib/jarvis-agent/jarvis-agent` (user unit `jarvis-agent`). Rebuild from the worktree:
  `CGO_ENABLED=0 mise exec go@1.27 -- go build -o ~/.local/lib/jarvis-agent/jarvis-agent.new ./cmd/jarvis-agent && mv ~/.local/lib/jarvis-agent/jarvis-agent.new ~/.local/lib/jarvis-agent/jarvis-agent && systemctl --user restart jarvis-agent`.
  Rebuilding from anywhere else replaces what the user is testing.
- **Extension**: 0.3.6 as a *temporary* add-on (`about:debugging` → This Firefox → Load Temporary
  Add-on → `internal/agent/browser/extension/manifest.json`; **Reload** after an extension
  change; gone on a Firefox restart). Unsigned: a permanent install needs
  `scripts/browser-ext-sign.sh` (uploads to Mozilla; ask the user first).
- **jarvisd**: release **v0.1.0-rc18.10** at `/usr/local/bin/jarvisd` (system unit, needs sudo).
  It already has #506. Only this branch's Q32 check is missing. To swap, build
  `go build -trimpath -ldflags "-s -w -X main.version=v0.1.0-rc18.10+q32" -o <file> ./cmd/jarvisd`
  and give the user the `sudo install` + `systemctl restart jarvisd` lines; keep a copy of the
  release binary for rollback (no migrations since rc18.10).
- **Native messaging host**: repointed by hand to `~/.local/lib/jarvis-agent/jarvis-agent` in the
  Firefox, Chrome, Chromium and Brave manifests (`.bak` beside each); see issue 9.
- **Microphone**: a **fifine USB mic** (`alsa_input.usb-MV-SILICON_fifine_Microphone…`), the
  default source. The two Bluetooth headsets (Px7 S2, BTD 700) were unusable: HFP/mSBC drops
  chunks mid-word, and the Px7's mic link went fully silent (RMS 1) mid-session.

Evidence is the agent journal: `journalctl --user -u jarvis-agent`, lines `voice turn`,
`direct: done on the node`, `follow-up: no speech onset` (peak, threshold, most_loud),
`follow-up: not for me`, `follow-up turn`, `tool call`.

## Open issues, most blocking first

1. **Follow-ups fail a few turns in.** Pattern on 2026-10-28: a follow-up "click X" is heard as
   "Quick X" / "Good X", misses stage 1, the server's gate calls it not for me, and the window
   keeps only `IgnoredGraceSecs` (2 s), so it closes before the user can repeat. The misheard
   click is now caught in stage 1 (when the rest names something on the page). **Not done:**
   giving the window back its full length after a rejection. Not verified by voice.
2. **Short answers are missed.** A "yes" to a confirmation peaked at 108 but was only 2 loud
   chunks against 3 required, so the window closed with the question pending (the user waited
   30–60 s). Proposed: 2 of 5 for a window after a question or choice. Not done.
3. **Mic level is marginal.** Follow-up speech peaks 42–84 against thresholds of 60–76; wake
   scores 0.42–0.45 against 0.4 on two of three wakes. Raising the gain didn't change it. This
   is the likely root of the misheard "click". Check levels in the log before blaming logic.
4. **jarvisd not swapped**: the Q32 claim check is off, and the model still claims success it
   didn't observe ("Firefox is open and I've searched for dogs").
5. **The server's model misroutes what reaches it.** "Click line over and say hello" became a
   smart-home device and then "the Living Room Lights aren't responding" for four turns; "open
   Google" became the Google Contacts/Maps/Messages/Photos apps. Stage 1 now keeps clicks and
   Google phrasings away from it; the server side is unchanged (stage 2, Q28, is the real fix).
6. **Nearby context is unverified on real pages.** The picker sees bounded text from semantic
   containers; layouts without article/list/group structure may not distinguish repeated controls.
7. **Confidence is a model estimate.** The 0.75 cutoff is an initial heuristic and needs voice
   evaluation against wrong-action and unnecessary-clarification rates.
8. **Browser autoplay rules refuse `play()` started by voice** (2026-10-28, YouTube Shorts):
   Firefox's default blocks audible autoplay, and an extension's call is not a user gesture, so
   "play the video" failed until the user set YouTube's Autoplay permission to "Allow Audio and
   Video" (address-bar site info). No code can grant it; a new install needs the same setting,
   and setup or the island's failure text should say so. `browser: media control failed` logs
   the code.
9. **Installer bug**: the rc18.10 installer registers the native host at its temp dir; the
   agent's startup repair (`refreshBrowserHosts`) only repoints app bundles, by design, so the
   installer is what needs the fix. Every Linux user hits this when the extension reconnects.
10. **A second node answers wakes** ("standing down: another node answers this wake"); Q25 (wake
    arbitration) is the open spec question.
11. **Q32 is half built.** Claims are checked (once jarvisd is swapped); the short goal loop
    (act, observe, retry within ~3 steps / 10 s) is not.
12. **`scripts/check` tests only changed packages**: run `scripts/check --all` before pushing
    tool-spec changes (it missed a stale `tools/pipeline/toolsets/*.json` once). Toolset JSONs
    may need regeneration after the browser tool descriptions change.

## Testing

- The agent reported `CGO_ENABLED=0 mise exec go@1.27 -- go test ./internal/agent/... ./cmd/jarvis-agent`
  passing before this review's edits. Those tests have not been rerun; run them and
  `CGO_ENABLED=1 … go test -race ./...` before committing.
- Extension: `node --test internal/agent/browser/testdata/background_test.mjs` (also run by
  `TestBackgroundJS`).
- Tests built from real transcripts: `TestClickByPartOrSoundOfAName`, `TestModelPicksWhenNoNameMatches`,
  `TestAnswerBrokenByDropoutsIsHeard`, `TestWebRequest`, `TestMediaRequest`. Add the next miss
  the same way: its exact transcript as a case.
- By voice (the only real check): wake + "search Google for dogs", then without the wake word
  "click images", "go to videos", "click the video", "pause", "type cats into the search box",
  "yes". Then grep the journal as above.

## Not started

- **Open mic** (user idea, 2026-10-28; parked until the steered loop is reliable): a menu choice
  exclusive with "Listen for 'Hey Jarvis'", no wake word or follow-up needed. First question
  asked, not answered: what decides it was for Jarvis (on-computer commands only / those then the
  server's not-for-me gate, recommended, with no verb-less control selection / everything to the model).
- Names for desktop apps outside the browser (AT-SPI snapshot; DESKTOP-COMMANDS L3).
- macOS AX and Windows UIA views; Windows has no island or pointer input yet.
- Stage 2 embedding narrowing (Q28); stage-1 actions recorded in the server conversation.
- Spec questions Q25, Q29–Q31, one at a time, at the principle level.
- `chat_template_kwargs` is sent to OpenAI endpoints (`internal/modules/llm/client.go`): a bug,
  low priority.

## Lessons from these sessions

- **Check the environment before the code.** The worst failures were a dead Bluetooth mic link,
  a quiet mic and a deleted native-host binary, not logic. `pactl get-default-source` and a
  3-second `parecord` RMS check take a minute.
- **Every miss that reaches the model is a risk.** Unmatched clicks used to become device control.
  Keep stage-1 misses on the island or in a narrow, tool-less pick.
- **Verify in the product, not the tests.** Stage 1 never ran for a day because
  `nodesettings.Gate` didn't forward the Director interface. Every new voice-loop interface gets
  a compile-time assert on the Gate.
- **Log what the user can't see.** "The blue ring shows and nothing happens" was undiagnosable
  until the window logged its peak level.
