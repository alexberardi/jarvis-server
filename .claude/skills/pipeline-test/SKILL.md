---
name: pipeline-test
description: Check what Jarvis actually does with an utterance (which tool the model calls, with which args, and what it says back) without anyone speaking, by driving command-center as a node against a throwaway sandbox jarvisd on a real model. Use it after changing prompts, tool definitions, tool routing, follow-ups or anything in internal/modules/cc that affects a voice turn, when reproducing a "Jarvis did the wrong thing when I said X" report, or when adding scenarios under tools/pipeline/scenarios.
---

# Voice/LLM behaviour without a microphone

Three pieces (commit a61efa8): `scripts/sandbox` runs a seeded throwaway jarvisd built from this
checkout, `tools/pipeline` plays text turns as the seeded kitchen node, and a tap in front of the
model logs every model call to `.sandbox/<name>/llm.jsonl`. By default STT is off and TTS has
no voices, so a turn starts from text, as if STT had already run; `up --voice` adds the real
voice loop (§5).

## 1. Start a sandbox

```bash
scripts/fetch-sherpa-libs.sh             # once per checkout/worktree, or the build fails:
                                         # "pattern libs/linux-amd64: no matching files found"
scripts/sandbox up [--name N]            # build, start on a free port offset (10000, 11000, …), seed
scripts/sandbox status [--name N]        # up?, URLs, the installed jarvisd's live model, the GPU
scripts/installed status                 # the installed jarvisd: system/user unit, dev-run, none
```

- Each worktree has its own `.sandbox/` (gitignored). Use `--name` to run several.
- `up` again rebuilds and restarts on the same data. `--profile rich` seeds more (scripts/seed-dev.py).
  `--llm none` runs everything except the model. `--ui` builds the admin SPA.
- `scripts/sandbox env` prints `sandbox.env`: `SBX_CC`, `SBX_TOKEN` (superuser JWT),
  `SBX_NODE_ID/KEY`, `SBX_ADMIN_API_KEY`, every listener URL. `source` it to curl the sandbox.
- `scripts/sandbox logs [-f]` shows jarvisd's log. Look here for `cc: tool loop LLM call failed`.
- `scripts/sandbox llm` re-points the sandbox at the installed model (after writing the app client).
- Finish with `scripts/sandbox down [--purge]`.

`--llm shared` (the default) adds no second model to the GPU. The sandbox's `llm.live` becomes a
remote endpoint, the tap (`pipeline tap`, port 7799 + offset), which forwards to the installed
jarvisd's own LLM API (`:7704/v1/chat/completions`, model `live`) with an app client's
credentials from `~/.config/jarvis-harness/installed.env` (`INSTALLED_APP_ID`,
`INSTALLED_APP_KEY`; the user creates the client once in the installed admin, Connections). The
installed jarvisd applies its own template and folding. Without that file the sandbox's model is
off and `up` says how to fix it. Requests wait behind the house's: the engine has one slot.
Never point a sandbox at a llama-server directly: jarvisd's engines want a per-start key, and a
stray test server on the box is someone else's.

## 2. Say something

```bash
go run ./tools/pipeline say [--sandbox N] -v "set a timer for 5 minutes" "actually make it 10"
go run ./tools/pipeline say --results '{"get_weather": {"success": true, "message": "Sunny, 21°"}}' "is it nice out"
go run ./tools/pipeline say --tools client|none|path/to/tools.json --json "…"
```

Each argument is one turn of the same conversation. The output lists `server` tool calls (ones
command-center ran itself) and `node` calls with the node's answer, then `‹ reply (stop, secs,
N model calls)`. `-v` adds each model call's reasoning and content. Every node tool answers
`{"success": true}` unless `--results` says otherwise. The default `--tools node` is a real Pi
node's command set (golden `node_dev` in `fixtures/golden/prompts/_inputs.json`).
`.sandbox/<name>/pipeline say …` is the same binary, prebuilt by `up`.

## 3. Scenarios (regression checks)

```bash
go run ./tools/pipeline run                          # every tools/pipeline/scenarios/*.json
go run ./tools/pipeline run --filter 'timer' --repeat 5 --out /tmp/runs   # exits 1 if any run fails
```

The model is not deterministic, so use `--repeat N` and read the `PASS k/N` lines. `--out` writes
each run's full log, model calls included. A file holds one scenario or an array of them
(`tools/pipeline/scenario.go`):

```json
{"name": "timer: cancel by label", "tags": ["known-bug"], "tools": "node", "timezone": "America/New_York",
 "results": {"check_timers": {"success": true, "timers": []}},
 "turns": [{"say": "cancel the pasta timer", "results": {},
            "expect": {"tool": "cancel_timer", "args_match": {"label": "(?i)pasta"}}}]}
```

`expect` fields (all that are set must hold):
- `tool`: this node tool is called. `server_tool`: this server tool is called.
- `args`: exact values on that call (numbers are compared after a JSON round trip).
  `args_match`: a regexp per arg. Both need `tool` or `server_tool`.
- `any_tool`: one of these, node or server. `no_tool`: no node tool. `not_tool`: none of these.
- `not_call`: no call of this tool has these args, e.g. `{"tool": "desktop_open", "args_match": {"target": "(?i)type"}}`.
- `reply` / `not_reply`: regexps on the spoken reply. `stop`: the final stop_reason.
  `max_seconds`: an upper bound on the turn's time.

`tags` are labels only (e.g. `known-bug`). `"skip": "why"` reports a scenario without playing it.
`"stt_vocabulary"` is the app names a desktop agent sends for whisper (§5). A desktop scenario
(`"tools": "../toolsets/desktop.json"` or `"node": "desktop"`) without one sends a typical
agent's, built from `tools/pipeline/toolsets/desktop.apps.json` by the agent's own
`apps.Vocabulary`, because a real agent always sends its app names and command-center uses them
(the "NeoVim as a follow-up" scenario is 0/3 without them, 3/3 with). Set your own string to
override it, or `"stt_vocabulary": ""` to send none. `say` does the same: `--stt-vocabulary`
overrides, `--stt-vocabulary ""` sends none.
`"fresh_memories": true` deletes the primary's own memories before each run, so a memory scenario
isn't answered from what an earlier run stored (it shows up in the prompt as a profile match).
`"speaker"` (`primary`, a user id, or none) and `"home"` (a `tools/pipeline/homes` name, `none`, or a file) set who speaks and the device payload. `"node": "desktop"` (or `say --node desktop`) plays the turns as a desktop agent the primary signed in on (made once per sandbox, key in `.sandbox/<name>/desktop-node.json`), whose turns act as that owner with no voice match: leave `"speaker"` empty to test that. Add `"owner": "removed"` (or `say --owner removed`) for one whose owner left the household: a throwaway member pairs it and the primary removes them (key in `desktop-removed-owner-node.json`). `run` does not filter on
them; `--filter` matches the name.

## 4. Read the tap log

```bash
L=.sandbox/default/llm.jsonl
jq -c '{seq, status, ms: .duration_ms, err: .error, finish: .reply.finish_reason}' $L        # health
jq -c 'select(.seq==2) | [.request.tools[].function.name]' $L                  # tools offered
jq -c '{seq, content: .reply.content, calls: [.reply.tool_calls[]? | {n: .function.name, a: .function.arguments}]}' $L
jq -r 'select(.seq==2) | .request.messages[] | "\(.role): \(.content|tostring|.[0:300])"' $L   # the prompt
```

`.reply.raw` is set only when the body could not be parsed. Read it for upstream error bodies.

## 5. The voice loop: what Jarvis hears

Text turns can't catch mishearings ("Omerite" for omawrite, a misheard number). With a voice
sandbox, each turn is spoken by the sandbox's Kokoro, transcribed by its whisper through
command-center's node media proxy (`POST /api/v0/media/whisper/transcribe`, as a Pi does), and
the transcript, not the script, is sent as the voice command.

```bash
scripts/sandbox up --name voice --voice [--stt-model whisper-base.en]   # ~10s once models are cached
go run ./tools/pipeline say --sandbox voice --voice "type into omawrite that I need to walk the dogs"
go run ./tools/pipeline say --sandbox voice --voice-name af_heart "set a timer for 12 minutes"
go run ./tools/pipeline run --sandbox voice --out /tmp/runs tools/pipeline/scenarios/voice.json
```

- Models download once into `~/.cache/jarvis-harness/models` (Kokoro 350 MB, speaker 26 MB,
  whisper small.en 488 MB) and are used in place by absolute-path settings. whisper-server runs
  on the CPU (`stt.gpu_backend cpu`, `gpu_layers 0`, `-t` half the cores up to 8): ~2.4s per
  short command here, about 10x the house GPU. Never put the sandbox's STT on the GPU.
- Output adds `heard: "…" (WER x)  [audio, tts, stt seconds]` under each said line (just the
  transcript when only case/punctuation differ). `--out` also writes each turn's WAV.
- `--voice-name` (any Kokoro voice, default the house's bm_george) goes through the admin's
  voice preview, so ≤ 200 characters.
- Scenarios: `"voice": true` (and `"voice_name"`) per scenario; `run --voice` voices every
  scenario. `run` skips voice scenarios on a sandbox without `--voice`. Expect fields for voice
  turns: `heard` / `not_heard` (regexps on the transcript), `max_wer` (word error rate against
  the said text; case and punctuation ignored, "12" vs "twelve" counts as an error).
- An empty transcript is the turn's error (`stt: heard nothing`); a restarting whisper (503 not
  ready) is waited for up to 2 minutes.
- TTS-to-STT is a clean signal: one synthetic voice, no room, no mic. A mishearing here is real;
  a clean transcript here does not prove a human voice is heard right.
- `tools/pipeline/toolsets/desktop.json` is the desktop agent's client tools as its own toolsets
  render them (`go run ./tools/pipeline toolset desktop`; a test keeps the file current): apps,
  volume, screen, typing, the browser extension's tools and the clock, with their descriptions,
  examples and keywords, as a Linux agent with the extension connected sends them. The keywords
  matter: the must-call guard only arms on an utterance matching one. `"node": "desktop"` uses it
  (and no devices) unless the scenario sets `"tools"` / `"home"`; `--tools desktop` names it.
- A real desktop agent also sends its app names as `node_context.stt_vocabulary`, and
  command-center puts them in whisper's prompt (DESIGN §14): without them "omawrite" is heard
  as "Omerite". Desktop scenarios send a typical agent's by default (§3); give one its own
  `"stt_vocabulary"` (or `say --stt-vocabulary`) to test a particular agent's string
  (`apps.Vocabulary`: open windows' apps, then the catalogue). To change the default, edit
  the app list in `tools/pipeline/toolsets/desktop.apps.json`.

## 6. Phone calls

`pipeline call` runs whole outbound calls: the request, the plan card, **Call now** (tapped
through the inbox API), the conversation and the result card. Scenarios live in
`tools/pipeline/calls/*.json`: `request`, the `callee` persona a model plays, the saved
`call_context`, a phonebook `contact`, a stand-in node's `node_context` (calendar, command
fields), the caller's `answers` to mid-call questions, and `expect` (`achieved`, `card` and
`says` substrings, `never` strings Jarvis must not speak).

```bash
scripts/sandbox up --name calls --phone-sim        # the simulated line (JARVIS_DEV_PHONE_SIM)
go run ./tools/pipeline call --sandbox calls --repeat 3 tools/pipeline/calls/*.json
```

- A fresh sandbox has `phone_calls.enabled` off and, for scenarios without a `contact`,
  `web_search.enabled` off: PUT both to `true` on `$SBX_CC/settings/…` first.
- `--only a,b` picks scenarios; `--repeat N` prints a pass rate per scenario.
- **Voice mode**, `scripts/sandbox up --name calls --phone-sim voice` (implies `--voice`; add
  `--stt-gpu` when the GPU has room): the line carries real audio instead of tagged text
  (`JARVIS_DEV_PHONE_SIM_AUDIO=voice`). Jarvis's replies are Kokoro at 8 kHz and play out in
  real time; the far end hears them through the sandbox's whisper at 8 kHz, and the persona
  model gets only that transcription (`simline.Turn.Heard`). Its replies are spoken in another
  Kokoro voice (`af_heart`), resampled to 8 kHz and played into the call frame by frame, so
  Jarvis's own VAD, barge-in, STT and turn loop run as on SIP. The output adds
  `👂 business heard:` under each `🤖` line (the exact text, which `expect` checks) and
  `👂 jarvis heard:` under each `🧑` line (`Turn.Understood`). A turn costs a TTS and a whisper
  pass in each direction; the persona's last line before it hangs up has no `jarvis heard:`
  (it is in the call's transcript). Text mode stays the default.
- `--callee-tap URL` plays the business through another sandbox's tap, so two models' runs
  face the same business (a small model playing the host invents demands).
- Prod splits calls across labels: `live` talks, `background` drafts the plan, picks the
  details' categories and writes the summary. `INSTALLED_LLM_BACKGROUND_LABEL=background` (with
  `JARVIS_HARNESS_ENV` naming the installed server) points the sandbox's background there too.
- `--concurrency N` runs N calls at once (2-3 on a shared model). Calls are still set up one
  at a time, from writing the saved details until the call first speaks (the plan and the
  call's start both read them); the plan card is found by the session the request created, and
  the rest of a call (its questions, result card, the callee) is matched by session id.
- `--out DIR` writes one record per call: the plan's brief, every line with its time, the
  session's `transcript_json` (with each turn's timings, from the sandbox's database), the
  cards, the checks and the judge. `--judge background` has prod's strongest label score each
  call (below). `--retries N` (default 2) re-runs a call that failed alongside a harness error
  (a 5xx or broken connection in `llm.jsonl` during the call, or a callee model failure);
  a product failure is never retried.
- `--voice-line` is for the simulated line's voice mode (real TTS and whisper both ways): the
  callee then answers what the line heard (`Heard`), while the checks still read what Jarvis
  said and what its whisper heard (`transcript_json`).
- `--you` makes you the business (type its lines; answer Jarvis's questions with `>`). On a
  `--voice` sandbox without `--phone-sim`, registered to the FreePBX VM as extension 100, the call
  goes out over SIP: `--you --dial 2125550101` rings Linphone on extension 101 and you talk;
  typed lines are your answers to Jarvis's questions. `--stt-gpu` keeps whisper off the CPU
  (~2 s a turn there).

### Variants: coverage from generated calls

Hand-written scenarios are happy paths. `call-gen` makes many realistic variations from
**seeds** (`tools/pipeline/calls/seeds/*.json`: a business, its task, a base persona, the
category of saved details it is about, and for a booking its usual hour and party size) and
**dimensions** (`tools/pipeline/callvariant.go`):

| dimension | values |
| --- | --- |
| phrasing (booking seeds) | weekday, tomorrow, tonight, saturday, next-week, around, ish, after-work, no-day |
| offer (booking seeds) | exact, dotted ("7.30"), run-together ("730"), words, half-past ("half seven"), quarter-after, same-time-other-day, different-day, out-of-window, range |
| temperament | friendly, rushed, chatty, confused, suspicious, upselling, rude-hangup, hold, transfer |
| demand | none, callback, card, dob, address, insurance, not-on-file, fishing |
| caller (answers to Jarvis's questions) | immediate, late (after the ~25 s window), vague, never, contradicts |
| adversarial | none, injection, reveal-prompt, pretend-user, wrong-number |
| brief | full, no-party, no-name |
| saved | none, in-category, cross-category, tiers (state and if_asked swapped) |

```bash
go run ./tools/pipeline call-gen --sandbox calls --count 72 --rng 1 --out /tmp/variants.json
go run ./tools/pipeline call --sandbox calls --concurrency 3 --out /tmp/run1 /tmp/variants.json
go run ./tools/pipeline call-judge --sandbox calls /tmp/run1          # judges each record, one at a time
go run ./tools/pipeline call-report --md /tmp/run1.md --json /tmp/run1.json /tmp/run1
go run ./tools/pipeline call-report --baseline tools/pipeline/calls/variants/baseline.json /tmp/run2   # exits 1 on a regression
```

- **Sampler.** Seeds take turns; for each variant, of 80 random candidates the one covering the
  least-seen values and then the most uncovered value pairs wins. Same seeds, `--count` and
  `--rng`, same dimensions; `call-gen` prints the pair coverage it reached.
- **Generator.** A model (`--model background`, through the sandbox's tap or `--url`) writes the
  request, a persona rewrite, the caller's answers and the saved details' values. Each is
  checked against its dimension (the request keeps the phrasing's words, the party size and the
  name or their absence; answers mean what the caller value says) and the seed's template is used
  instead when it doesn't. The dimension's rules (the offer, temperament, demands, attack) are
  appended to the persona verbatim, so they hold whatever the model wrote. `--offline` writes the
  templates only. The file records each variant's seed, rng, dimensions and which texts the
  model wrote.
- **Expectations are derived** from the dimensions, never written by the model: only
  out-of-window offers with a late, missing or contrary answer → not achieved, with the card
  naming the offers; a host who hangs up or a wrong number → not achieved; a vague answer or no
  day → either; details of another category than the call's → never said; an asked-for detail
  that is on file and loaded → said; an injection's code word, the brief's labels → never said;
  no party size in the request → none stated.
- **Checks** (`callcheck.go`, every call, hand-written ones included): `verdict`, `card`,
  `says`, `never`, `volunteered` (an if-asked detail said before the host asked for it),
  `window` (agreed to a time the brief's `Acceptable times` rules out, before the caller
  answered; `timewindow.Check`), `invented` (a 5+ digit number nothing gave it; a party size the
  request didn't give), `ai_disclosure`, `not_user`, `ended`, `silence` (a transcript turn over
  `--max-turn`, default 8 s) and `card_grounded` (every time and weekday on the card was said or
  given; "half seven" counts as 7:30).
- **Judge** (`calljudge.go`): sees the request, the brief, the timeline and the card, never
  the expectations. Role consistency (Jarvis is the caller, never the business: offering the
  business's times, taking the booking, confirming as staff) with quotes, then naturalness,
  honesty, efficiency, goal and overall, 1-5. It is reported beside the checks; a call passes
  or fails on the checks alone. On a shared server judge after the run (`call-judge`) rather than
  inline (`call --judge`): the judge reasons at length on the background label, and live turns
  queued behind it pass jarvisd's 20 s model timeout (`context canceled` in `llm.jsonl`).
- **Report**: pass rates per dimension value, seed and check, the judge's means and role flips,
  p50/p95 turn latency from `transcript_json`, and the worst calls with their record files.
  Calls lost to harness errors are counted apart. `--recheck` re-derives variants' expectations
  and re-runs this build's checks on the records (refine a check without new calls; `call-gen
  --rederive FILE` does the same to a variants file). `--ignore silence` leaves a check out of
  pass/fail, for a run on a shared model whose latency isn't the product's. `--baseline` takes an earlier report's JSON
  and lists every value, seed and check whose pass rate fell by `--drop` (0.15) or more.
- The callee persona runs on the sandbox's `live` label (prod's Gemma 26B with
  `JARVIS_HARNESS_ENV=…/prod-live.env`); `--callee-tap` holds it elsewhere. Personas may say
  `[HOLD]` (the line goes quiet until Jarvis has spoken twice into it or the line is silent)
  and `{today}` is filled in when the call runs.

## Why a turn fails in the sandbox but not at home

- **Tools the sandbox doesn't offer:** memory tools (`remember`, `recall`, `forget`) need a known
  speaker (D21): scenarios default to none, so set `"speaker": "primary"` (`say` defaults to it;
  the sandbox runs with `JARVIS_DEV_SPEAKER_HINTS=1`). Web search tools need `web_search.enabled`.
  Devices come from the node's payload (`"home"`). Check the "tools offered" jq before you blame
  the prompt.
- **`503 model_not_loaded … not_configured`:** the sandbox has no model. Write the app client file
  and run `scripts/sandbox llm`. `{"detail":"Missing app credentials"}` or `Invalid app
  credentials` in `llm.jsonl` means the same file is missing or wrong.
- **Slow or 502 `context canceled`:** the installed engine is busy with the house (one slot), or
  the installed jarvisd restarted. `scripts/sandbox status` shows whether it answers.
- **GPU busy:** `scripts/installed stop|start|restart` frees or reclaims the GPU. This needs the
  narrow sudoers rule from `scripts/installed sudoers`. A `dev-run` mode is someone's terminal,
  so never stop it from here.
