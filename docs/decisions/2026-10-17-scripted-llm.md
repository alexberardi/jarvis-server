# A scripted model for UI tests (sandbox --llm scripted)

- **Date:** 2026-10-17 (user asked for E2E regression; design by agent, loop item #133, piece 1/9)

The E2E regression suite (admin web app and mobile app, run on a manual trigger) must assert
what a page shows after a chat turn. A real model words things differently each run and
sometimes picks another tool, so those tests would flake. `scripts/sandbox up --llm scripted`
gives the sandbox a model that answers from a script instead: `tools/scriptllm`, a small
OpenAI-compatible server (Go). The real-model pipeline scenarios (`tools/pipeline/scenarios`,
`--llm shared`) stay as they are; they are what checks the model's behaviour. The scripted model
only checks that everything around the model works.

## Wiring

- `scriptllm` listens on `7798 + offset`, behind the LLM tap (`7799 + offset`), so its calls
  are logged in `llm.jsonl` like the real model's and `pipeline say|run` show them.
- `llm.live` is engine `remote`, `remote_api openai`, model `scripted`, context 32768, no vision,
  no folding (as for `--llm shared`). It needs no installed jarvisd and no app client.
- `llm.tool_calling native` and `llm.thinking_switch none`: cc sends the tools and reads the
  OpenAI `tool_calls` the script returns, so no prompt format is involved. `llm.prompt_provider`
  is fixed to `Qwen3_5_9B_Compressed` (a remote model derives none and a voice turn needs one).
- `sandbox.env` exports `SBX_SCRIPTLLM`, the server's base URL. `scripts/sandbox llm` re-applies
  the mode `up` chose (kept in `.sandbox/<name>/llm-mode`).

## Script format

Scripts live with the tests in `e2e/llm/*.json`; the sandbox starts with `e2e/llm/default.json`.

```json
{
  "fallback": "Scripted model: no rule matched that.",
  "rules": [
    {"match": "(?i)turn on the kitchen lights",
     "tool_calls": [{"name": "control_device", "arguments": {"device_name": "Kitchen Lights", "action": "turn_on"}}]},
    {"after_tool": "control_device", "match": "\"success\":\\s*true", "reply": "Done."},
    {"match": "(?i)\\bhello\\b", "reply": "Hello from the scripted model."}
  ]
}
```

- Rules are tried in order; the first that applies answers. `reply` is the text, `tool_calls`
  the calls (ids are generated; `finish_reason` is `tool_calls`). A rule may have both.
- The turn is the request's **last message that is not a system message**. cc appends system
  notes after a tool result (`[TOOLS_FAILED]`, `[TOOL_DEDUPE]`) and context around the user's
  words, so those are skipped. Write `match` unanchored.
- A rule without `after_tool` answers a user message whose text matches `match` (a Go regexp).
- A rule with `after_tool: NAME` answers only a tool result for NAME (found through the
  `tool_call_id`), with `match` on the result's text when set. That is how a round trip ends with
  a reply; without one, the tool result gets the fallback, which is a plain reply, so cc never
  loops on a call.
- Unknown keys are an error, as are a bad regexp, a rule with no condition or nothing to say.
- A request with no user or tool message (cc's prompt-cache warm-up) gets an empty reply.
- No rule: the `fallback` reply (default `scripted: no rule matched`), and the turn is recorded
  in `.sandbox/<name>/scriptllm-unmatched.jsonl` and at `GET /control/unmatched`, so a test can
  fail on an unscripted turn instead of asserting on the fallback text.
- Both streaming (SSE: content word by word, each tool call in two fragments, the usage chunk
  when `stream_options.include_usage`, `[DONE]`) and plain JSON answers.

## Control API (`$SBX_SCRIPTLLM`)

- `POST /control/script` `{"path": "e2e/llm/x.json"}` (relative to the repo root) or
  `{"script": {...}}` switches the active script for every later call; an empty body reloads the
  current file. A broken script is refused (400) and the active one stays.
- `GET /control/script`: the active script's name and rule count.
- `GET /control/unmatched`, `DELETE /control/unmatched`: the unmatched turns, and forgetting them.

One script is active per sandbox, so tests sharing a sandbox switch scripts one at a time.

## Scenarios against it

`e2e/llm/scenarios/` holds pipeline scenarios for the scripted model, outside
`tools/pipeline/scenarios` so neither run picks up the other's:
`.sandbox/<name>/pipeline run --sandbox <name> e2e/llm/scenarios/scripted_tools.json`.
