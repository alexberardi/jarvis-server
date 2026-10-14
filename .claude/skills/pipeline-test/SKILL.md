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
scripts/sandbox status [--name N]        # up?, URLs, which llama-server it uses, who holds the GPU
scripts/installed status                 # the installed jarvisd: system/user unit, dev-run, none
```

- Each worktree has its own `.sandbox/` (gitignored). Use `--name` to run several.
- `up` again rebuilds and restarts on the same data. `--profile rich` seeds more (scripts/seed-dev.py).
  `--llm none` runs everything except the model. `--ui` builds the admin SPA.
- `scripts/sandbox env` prints `sandbox.env`: `SBX_CC`, `SBX_TOKEN` (superuser JWT),
  `SBX_NODE_ID/KEY`, `SBX_ADMIN_API_KEY`, every listener URL. `source` it to curl the sandbox.
- `scripts/sandbox logs [-f]` shows jarvisd's log. Look here for `cc: tool loop LLM call failed`.
- `scripts/sandbox llm` re-points the sandbox at the installed engine after that engine restarted.
- Finish with `scripts/sandbox down [--purge]`.

`--llm shared` (the default) adds no second model to the GPU. The sandbox's `llm.live` becomes a
remote endpoint, the tap (`pipeline tap`, port 7799 + offset), which forwards to the installed
jarvisd's running `llama-server`. Requests wait behind the installed instance's own: it has one slot.

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

`tags` are labels only (`needs-ha`, `needs-embeddings`, `known-bug`). `run` does not filter on
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

- **Tools the sandbox doesn't offer:** memory tools need embeddings, and the sandbox sets
  `llm.embeddings.engine` off. Device control needs Home Assistant (`needs-ha`). Check the
  "tools offered" jq before you blame the prompt.
- **Wrong engine.** `scripts/sandbox` and the tap pick the *first* process whose command line
  has `llama-server … --alias`. A stray llama-server, such as a CPU test model left running by
  another session, wins over the installed CUDA one. Calls then take minutes and time out
  (status 502, `context canceled`). Check that the `installed llama-server: :PORT` in
  `scripts/sandbox status` belongs to a process under `/var/lib/jarvisd/` or `~/.jarvisd/`
  (`ss -ltnp | grep PORT`). To pin it, restart the tap by hand:
  `kill $(cat .sandbox/N/tap.pid); setsid .sandbox/N/pipeline tap --listen 127.0.0.1:<7799+offset> --upstream http://127.0.0.1:<port> --log .sandbox/N/llm.jsonl &`
- **401 "Invalid API Key"** in `llm.jsonl`: jarvisd starts its llama-server with a random
  `LLAMA_API_KEY` in the environment (`internal/modules/llm/engine/resolver.go`). The tap sends
  no key, so an engine that jarvisd started rejects every call. (Seen on 2026-10-13. Until the
  harness handles the key, `shared` works only against a keyless llama-server.)
- **GPU busy:** `scripts/installed stop|start|restart` frees or reclaims the GPU. This needs the
  narrow sudoers rule from `scripts/installed sudoers`. A `dev-run` mode is someone's terminal,
  so never stop it from here.
