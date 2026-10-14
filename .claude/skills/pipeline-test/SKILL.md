---
name: pipeline-test
description: Check what Jarvis actually does with an utterance (which tool the model calls, with which args, and what it says back) without anyone speaking, by driving command-center as a node against a throwaway sandbox jarvisd on a real model. Use it after changing prompts, tool definitions, tool routing, follow-ups or anything in internal/modules/cc that affects a voice turn, when reproducing a "Jarvis did the wrong thing when I said X" report, or when adding scenarios under tools/pipeline/scenarios.
---

# Voice/LLM behaviour without a microphone

Three pieces (commit a61efa8): `scripts/sandbox` runs a seeded throwaway jarvisd built from this
checkout, `tools/pipeline` plays text turns as the seeded kitchen node, and a tap in front of the
model logs every model call to `.sandbox/<name>/llm.jsonl`. STT is off and TTS has no voices in
a sandbox, so a turn starts from text, as if STT had already run.

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
- `reply` / `not_reply`: regexps on the spoken reply. `stop`: the final stop_reason.
  `max_seconds`: an upper bound on the turn's time.

`tags` are labels only (e.g. `known-bug`). `"skip": "why"` reports a scenario without playing it.
`"speaker"` (`primary`, a user id, or none) and `"home"` (a `tools/pipeline/homes` name, `none`, or a file) set who speaks and the device payload. `run` does not filter on
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
