# 02 — Tool loop

Scope: the multi-turn LLM ⇄ tool loop (`ToolExecutionEngine`), the server-tool registry and executor, the client/server split, tool-call parsing and repair, param validation/refinement, the fastText routing gate, `ModelService`/`ModelFactory`, the llm-proxy client and the exact request shapes CC sends, `/api/v0/chat`, the node-tools view, and the five generic server tools.

Not here: voice route plumbing, streaming/TTS paths, the conversation cache (01); prompt text, date context and date-key resolution internals (03); the memory/errand/phone/research/device tools (04, 07, 09, 11).

All paths are relative to `jarvis-command-center/app/` unless stated otherwise.

---

## 0. Decisions applied (2026-10-06)

Source: `QUESTIONS.md`. §1–§9 still describe today's Python behaviour; changes are flagged inline as "Changed by D#".

- **D9 (cuts, with F1).** The fastText router is cut: the classifier, `/tool-router/train`, the router decision, the **`Router hint:` message**, the **must-call guard**, the fast stream path and tool-stream path B (the PLAN's pure-Go fastText port is dropped too). Also cut: the legacy `IModelInterface`/`ModelFactory`/`JarvisToolModel`, the malformed-JSON extractor and `json_schema.py`, the prune helpers, `/lightweight/chat`, the disabled `ControlDeviceTool`. The force-tools guard is **not** cut.
- **D4 / D5.** `/api/v0/chat` is **dropped** (D5 supersedes D4's "add node auth"): node `chat_text()` moves to the node-authed `/api/v0/node/llm/chat`, a node-setup change. `/mobile/node-tool-reports/{id}` and `/device-control-results/{id}` need node auth, and the replying node must be the one the request was addressed to. `trusted:true` leaves the `tool_call` payload; MQTT trust comes from per-node broker credentials and ACLs.
- **D8.** Known bugs are fixed by default and logged as intended differences: native transcript validity (Q6) and the `_refinable` aliasing (Q7).
- **D11 / D12.** `llm.interface` becomes `llm.prompt_provider`, set at install time; an unknown provider name is a hard error. The dead `tool_classifier.*` keys are dropped. The admin catalog offers only models with a kept provider.
- **D21 / M14.** The server passes the speaker identity (or its absence) to **every** command and server tool, and each decides what to refuse. Per-user tools (memory, phone, a user's errands) refuse an unknown or ambiguous speaker; when recognition is off the refusal says "speaker recognition is off".
- **D22.** Both prompt paths ship. **The native path gets the text path's per-household server-tool gates** (Q3). Nag and hint strings are byte-exact (Q8).
- **D23.** The text-path continue stays a single formatting call; native continue re-enters the loop (Q4).
- **D40 (B defaults).** Q9: natural iteration-limit fallback, error code kept in traces. Q10: date extraction once per turn. Q12: moot, the date-key LLM fallback is dropped (03.Q9).
- **M2.** The `llm_trace.log` file is dropped; metrics JSONL only behind a debug setting with a size cap.

## 1. Purpose

When a user speaks to a node (or types in mobile chat), CC asks the LLM what to do. The model either:

- answers in prose,
- calls a **server tool**, which CC runs in-process, or
- calls a **client tool**, which the node runs: a Pi command plugin, smart-home or Spotify, for example.

The tool loop keeps calling the LLM until one of four things happens:

- it gets a final answer,
- it needs the node to run something (the node's work comes back via `/voice/command/continue`),
- it needs a clarification, or
- it runs out of iterations.

On every iteration it repairs the small model's malformed output and guards against known failure modes. The failures it guards against are:

- prose where a tool was needed
- ISO dates where date keys were expected
- duplicate calls
- bad param types
- false "not for me" silences

Users of the loop:

- **Node:** voice, through `conversation_handler.process_voice_command_with_tools` and `continue_conversation_with_tool_results`.
- **Mobile:** SSE chat. It uses the same handler, and client calls go to the node over MQTT.
- **Errands and workflows:** reuse `tool_registry.has_tool` and `execute_tool`.
- **Node plugins:** use `/api/v0/chat` as a raw LLM passthrough.

## 2. Entry points

### Routes

| Method | Path | Auth | Live caller (PLAN App. A) | Disposition |
|---|---|---|---|---|
| POST | `/api/v0/chat` | **none** (`chat.py:16`) | node `chat_text()`/`chat()` (`jarvis-node-setup/clients/jarvis_command_center_client.py:472,492`): jokes, what's-up, routines | **Dropped (D5).** The node moves to `/api/v0/node/llm/chat`. |
| POST | `/api/v0/lightweight/chat` | none (`chat.py:28`) | none | Cut |
| POST | `/api/v0/tool-router/train` | node `X-API-Key` (`main.py:1602`) | `jarvis-node-setup/scripts/train_tool_router.py` | **Cut (D9)** |
| POST | `/api/v0/test/command` | app-to-app (`api/test_commands.py:167`) | jarvis-mcp only | Cut |
| GET | `/api/v0/mobile/nodes/{node_id}/tools` | user JWT + household `member` (`api/node_tools.py:28-47`) | mobile node-tools view | Keep (also see doc 12) |
| POST | `/api/v0/mobile/node-tool-reports/{request_id}` | **none** (`api/node_tools.py:105`) | node's reply to the `report_tools` MQTT verb | Keep, with node auth bound to the request (D4) |
| POST | `/api/v0/device-control-results/{request_id}` | **none** (`api/smart_home.py:1200`) | node's reply to the `tool_call` MQTT verb (owned by doc 07; listed because headless tool calls depend on it) | Keep, with node auth bound to the request (D4) |

`/voice/command`, `/voice/command/stream`, `/voice/command/continue[/stream]` and `/api/v0/mobile/chat` are documented in docs 01 and 13. This doc specifies the loop they call.

### MQTT (outbound, request/response over HTTP reply)

- `jarvis/nodes/{node_id}/commands`, verb `tool_call`: the headless execution of a client tool. Used by mobile chat, the errand executor and workflows (`services/node_command_service.py:64-68,145-196`).
- `jarvis/nodes/{node_id}/commands`, verb `report_tools`: fetches the node's `client_tools` and `available_commands` (`api/node_tools.py:64-102`).
- `jarvis/nodes/{node_id}/context/query`, with the response on `…/response/{correlation_id}`: plan-time context queries (`services/context_provider_client.py:91-110`). Its one consumer is `phone_call_service.py:720`.

### Server tools: registry contents

The registry auto-discovers every `IServerTool` subclass under `core/tools/` and `core/tools/custom/` that has `enabled == True` (`core/tool_registry.py:34-82`).

**Enabled (12):**

| Tool | Owner doc |
|---|---|
| `request_validation` | this doc |
| `identify_speaker` | this doc |
| `remember`, `forget`, `recall` | 04 |
| `deep_research`, `quick_search` | 04 |
| `get_ha_entities` | 07 |
| `run_errand`, `schedule_errand`, `list_scheduled_errands` | 09 |
| `make_phone_call` | 11 |

**Disabled, so never registered:**

- `answer_question` (`answer_question_tool.py:20`, "moved to node-side command")
- `get_command_utterance_examples` (`get_command_examples_tool.py:21`)
- `resolve_relative_date` (`resolve_relative_date_tool.py:36`). Its static helpers `get_available_keys` and `resolve_with_llm_fallback` are still used by the engine.
- `control_device` (`control_device_tool.py:34`)

PLAN §5b says "the 17 server tools"; the code count is 16 classes, of which 12 are live.

### Internal callers of the engine

`ToolExecutionEngine(...)` is constructed in only two places:

- `core/conversation_handler.py:983` (new turn)
- `core/conversation_handler.py:2402` (native continue)

## 3. Behaviour

### 3.1 Turn setup (handler, before the engine)

`process_voice_command_with_tools` (`core/conversation_handler.py:719`) does the following, in order:

1. Calls `_apply_tool_filtering`, which is a no-op. `filter_tools_for_utterance` returns all tools (`core/tool_routing.py:104-123`).
2. Runs the router (§3.6) and caches the router decision.
3. Strips the per-turn transient system blocks. Then it appends:
   - the speaker block
   - the ambient block
   - the "recently shown" block
   - **if `router_decision.used`:** `{"role":"system","content":"Router hint: likely tool is '<t>'. Use it if it matches intent; otherwise choose the best tool."}` (`:895-901`)
4. Builds the user message. It is the transcript, plus these hints separated by `\n\n`, in order: direction, affect, turn, profile-match, agent context. Finally the provider suffix is appended after `\n` (`:914-971`). The suffix is `/no_think` or `/think` for Qwen3, chosen by `model.include_thinking`.
5. Sets `max_iters = 10` if the provider is native (`supports_native_tools`), else **3** (`:978`).
6. Calls `engine.execute(...)`. `sentinel_double_check` comes from `should_double_check_sentinel(...)` (`:983-1002`).
7. **Post-processing:**
   - `server_tool_complete` → `_format_tool_result_text_mode` (§3.4).
   - A sentinel in the assistant message → `{"stop_reason":"not_for_me","assistant_message":""}`.
   - Then: `update_messages`, `apply_exchange_complete`, `_rewrite_terminal_filler`, and answered-round accounting (`:1006-1126`).

**Native vs text.** Of the kept providers:

- **Text path:** `Qwen3_14B_Compressed` (prod) and `Qwen3_8B_Compressed` (dev). `supports_native_tools` is False by default (`interfaces/ijarvis_prompt_provider.py:229-239`).
- **Native path:** `Qwen3_5_9B_Compressed` (`qwen3_5_9b_compressed.py:53`) and `ChatGPTOpenAI` (`chatgpt_openai.py:56`).

All three Qwen providers inherit `force_tool_calls = True` from `Qwen25_7B_Compressed` (`qwen25_7b_compressed.py:51`). At warmup this is stored as `conversation_cache.set_force_tool_calls` (`conversation_handler.py:518-519`).

**Which server tools a turn sees depends on the path** (`conversation_handler.py:313-371`):

- **Text path:** a whitelist.
  - `answer_question`. It is disabled, so it silently drops.
  - `make_phone_call` (always).
  - `run_errand`, `schedule_errand`, `list_scheduled_errands`.
  - `deep_research` and `quick_search`, only if `web_search.enabled`.
  - `remember` and `forget`, only with a speaker **and** memory enabled; `recall` additionally needs recall enabled.
- **Native path:** **every** registered tool, ungated (`get_tools_for_model` returns all, `tool_registry.py:273-275`).

> **Changed by D22:** in Go both paths get the text path's per-household gates: web search off means no `quick_search`/`deep_research`, and the memory tools need a speaker plus the setting.

Server and client tools are concatenated without dedupe (`warmup_service.py:133-148`).

### 3.2 The engine algorithm (`core/tool_execution_engine.py:192-1511`)

The pseudo-code below is the spec. Line refs are given per step.

```
execute(conversation_id, messages, tools, max_iterations=10, user_utterance, agent_context_chars, sentinel_double_check)

0. Scrub stale steering nags from messages[1:]: system msgs whose content STARTS WITH
   "[MUST_CALL_RETRY]" or "[TOOL_DEDUPE]" (prefix match, never substring — messages[0]
   mentions the tag) (:235-242, _is_retry_nag :121-132).
1. adapter_settings = {"hash":h,"enabled":True} if node_context.adapter_hash  (:245-250)  → CUT.
2. use_native = provider.supports_native_tools; if so native_tools =
   provider.build_tools(ToolBuilder.strip_jarvis_extensions(tools)) (:621-630).
3. State: popped_prose=None, double_checked=False, dedupe_nudged_keys={}, next_max_tokens=256.
4. for iteration in 0..max_iterations-1:
   a. iter_max_tokens = next_max_tokens; next_max_tokens = 256.
   b. LLM call (§3.7 shapes A/B), temperature 0.4. Exception → return
      {"stop_reason":"error","error":str(e)} (:756-763).
   c. Extract content defensively: choices[0].message may be dict or bare string (:772-789).
      finish_reason defaults "stop".
   d. Collect <think>…</think> bodies into reasoning_parts (:792-794).
   e. date_keys = normalize_date_key(k) for k in response["date_keys"] (Jarvis extension) (:797-801).
   f. Tool calls:
        native: if finish_reason=="tool_calls" and message.tool_calls → _normalize_native_tool_calls,
                else stop with content as the answer (:821-832).
        text:   content' = provider.parse_response(raw) or raw; tool_call_parser.parse_response(content')
                → (finish_reason, tool_calls, assistant_message) (:833-842).
   g. Append {"role":"assistant","content":raw_content[,"tool_calls"]} (:875-878).
      If double_checked: remove every msg containing "[NOT_FOR_ME_DOUBLE_CHECK]" (:884-888).
      If a dedupe nudge is pending: remove "[TOOL_DEDUPE]" nags (:894-898).
   h. SENTINEL (<not_for_me/> outside <think>, in raw or parsed message) — outranks every guard (:909-994):
        - popped_prose set → pop sentinel reply + trailing [MUST_CALL_RETRY] nags, restore popped prose
          (sanitize_text + clean_for_tts), return complete.
        - elif sentinel_double_check and not double_checked and iteration+1 < max:
          pop reply, append USER msg "[NOT_FOR_ME_DOUBLE_CHECK] …reason it through… /think",
          next_max_tokens=1536, continue.
        - else return {"stop_reason":"not_for_me","assistant_message":raw_content}.
   i. finish_reason == "stop" (:997-1258):
        has_terminal_sentinel = <exchange_complete/> or <not_for_me/> in raw/parsed (outside think).
        FORCE-TOOLS GUARD — armed iff force_tools ∧ retry_count<2 ∧ ¬double_checked ∧ ¬terminal
          ∧ utterance present, then disarmed when:
            utterance is question-shaped, OR
            ¬(utterance action-shaped ∨ report-shaped) ∧ ¬reply_claims_action, OR
            keyword pool non-empty ∧ no keyword match (pool = keywords from available_commands, cached
            tools, tools; match text = utterance, plus reply text when only the reply armed it).
          (No utterance → legacy always-retry.)
          If still armed: popped_prose = assistant_message; pop; append system
          "[MUST_CALL_RETRY] You MUST call a tool. Do NOT answer directly. Pick the best matching
          function from the available tools. (attempt N/2)"; continue. (:1069-1163)
        MUST-CALL GUARD — must_call_tools (commands/tools with allow_direct_answer==False) ∧
          router_decision.used ∧ retry_count<1 ∧ ¬double_checked ∧ ¬terminal ∧ ¬question →
          pop; append "[MUST_CALL_RETRY] Direct answers are not allowed for this request. You must
          call exactly one tool next. Tools that require a call: a, b."; continue. (:1168-1203)
        Otherwise: sanitize (provider.sanitize_text then clean_for_tts); return
          {"stop_reason":"complete","assistant_message":…, "end_of_exchange":True if marker}.
        retry_count = number of messages that are [MUST_CALL_RETRY] nags (shared by both guards).
   j. finish_reason == "tool_calls" (:1260-1487):
        1. ISO date guard on args.resolved_datetimes: reverse-map ISO → date key (multi-value tuple
           first, then each single); "fixed" → patch silently; "bad" and no prior [ISO_DATE_RETRY] →
           pop, append "[ISO_DATE_RETRY] …Use date KEY STRINGS only…", continue.
        2. Date injection (§3.5) using schemas from tools + FULL cached tools.
        3. tool_executor.execute_tool_calls → (server_results as role=tool msgs, client_calls) (§3.3).
        4. messages += server_results.
        5. "get_command_examples" called → continue   [dead: real tool name differs and is disabled].
        6. request_validation + any other call → continue.
           request_validation alone → return {"stop_reason":"validation_required",
           "validation_request":{question, parameter_name, options}}.
        7. server_results AND client_calls → continue (client calls are DROPPED; the model must re-issue).
        8. client_calls only:
             - force_tools ∧ utterance → refine_params (§3.5).
             - DEDUPE: a client call whose (name, sha256(sorted-key JSON args)) matches an issued call
               within voice.tool_dedupe_window_seconds (120) and not already nudged this turn → pop,
               append system "[TOOL_DEDUPE] The results of X with those arguments are already in this
               conversation above — answer from them concisely; …", continue. Fail-open everywhere.
             - Param validation (types + enums from available_commands); invalid and retries <
               JARVIS_INVALID_PARAM_RETRY_MAX (default 1 if model.small_model_mode else 2) → append
               "[INVALID_PARAM_RETRY n/max] … Invalid: a.b expected datetime, …"; continue
               (assistant message NOT popped). Exhausted → issue anyway.
             - record_issued_client_call for each; return {"stop_reason":"tool_calls",
               "tool_calls":client_calls, "assistant_message":…}.
        9. server_results only: text path → return {"stop_reason":"server_tool_complete",
           "server_tool_results":…} (handler formats); native path → loop again.
   k. any other finish_reason → return complete with assistant_message.
5. Loop exhausted → {"stop_reason":"complete","assistant_message":"Maximum tool execution
   iterations reached.","error":"max_iterations_exceeded"} (:1502-1511). This string is SPOKEN.
Every return path attaches "reasoning" (joined think blocks) when any were captured.
```

> **Changed by D9:** the must-call guard (step i) and step 1 are not ported; the router decision no longer exists. **Changed by D40 (Q9):** step 5 speaks a natural fallback instead of "Maximum tool execution iterations reached.", keeping `error:"max_iterations_exceeded"` for traces. **Changed by D8 (Q6):** on the invalid-param retry (j.8) and the server+client drop (j.7), Go no longer leaves an assistant `tool_calls` message without matching tool replies.

### 3.3 Client vs server split

`ToolExecutor.execute_tool_calls` (`core/tool_executor.py:96-171`) splits calls by name:

- **`tool_registry.has_tool(name)` true:** a server tool. Parse the arguments (invalid JSON gives `{}`) and add the kwargs `conversation_id` and `user_utterance`. `registry.execute_tool` catches every exception and returns `{"error":"execution_error","message":…}` (`tool_registry.py:289-317`). The result is compacted (only `resolve_relative_date`, which is dead) and serialized as `{"role":"tool","tool_call_id","name","content":json}`.
- **Otherwise:** a client tool, passed through untouched.

`has_tool` checks the **global registry**, not the tools offered in this turn. A model that names a server tool that was never offered (for example `identify_speaker` on the text path) still runs it.

A server tool's `execute()` is **synchronous**. It runs inline on the event loop.

### 3.4 Returning client calls and resuming (voice)

**The 202.** When the engine returns `tool_calls`, the stream route answers **202** with a `VoiceCommandResponse` JSON body (`main.py:1560-1593`; model in `response_models/voice_command_response.py`):

```json
{"commands":[],"request_information":{"voice_command":"…","conversation_id":"…"},
 "stop_reason":"tool_calls","assistant_message":"<parsed message, often empty>",
 "end_of_exchange":false,
 "tool_calls":[{"id":"call_ab12cd34ef56","type":"function",
                "function":{"name":"get_weather","arguments":"{\"resolved_datetimes\":[\"2026-10-06T00:00:00-04:00\"]}"},
                "failure_message":"Sorry, I couldn't get the weather."}],
 "validation_request":null,"reasoning":null}
```

`arguments` is always a **JSON string**. `validation_required`, `error` and an empty `complete` take the same 202 path. A non-empty `complete` streams 200 `audio/raw`.

**The resume.** The node runs the calls, then POSTs `/voice/command/continue[/stream]` with `{"conversation_id","tool_results":[{"tool_call_id","output":<any>}]}` (`request_models/tool_result_request.py`). Then `continue_conversation_with_tool_results` (`conversation_handler.py:2343-2428`) runs:

1. Stashes the referenced items.
2. Appends each result as `{"role":"tool","tool_call_id","content":output-as-JSON-string}`.
3. Branches by path:
   - **Native:** re-enters the engine with `max_iterations=10` and `user_utterance` set to the last user message. That message includes all the appended hints, not the bare transcript. The model may chain further client calls, which produce another 202.
   - **Text:** makes **no tool loop**; it calls `_format_tool_result_text_mode` once (`:2430-2625`):
     - **Fast path:** if every result has a non-empty `message`/`response` string, join them and return without an LLM call. The `role=tool` messages are dropped and an assistant message is appended.
     - **Otherwise:** drop the `role=tool` messages and append a user message. It is `The user asked: "<last user msg>"` followed by either the "Answer the question from your own knowledge…" instruction (the knowledge-delegation heuristic) or "Tool results below. Answer … DIRECTLY …\n\n<results>". It ends with `\n/no_think`. Then one LLM call (shape C) and the scrub: `</?tool_call>` removed, `sanitize_text`, `clean_for_tts`. If the result is a bare JSON object with `name`, fall back to the tool results' `message`/`response` (or the `error` of a `success:false` result), else "Done.".
   - **Consequence:** a text-path model can never chain a second client tool after a client result.
4. Calls `apply_exchange_complete`.

The same formatter handles `server_tool_complete` on new turns.

### 3.5 Headless client calls over MQTT (mobile chat, errands, workflows)

When no node HTTP round trip is in flight, a client call becomes `dispatch_node_command` (`services/node_command_service.py:145-196`). It publishes to `jarvis/nodes/{node_id}/commands`:

```json
[{"command":"tool_call","details":{"command_name":"…","arguments":{…}|"<json str>",
  "tool_call_id":"…","reply_request_id":"<uuid>","trusted":true,"user_id":1,"voice_command":"…",
  "request_id":"<uuid>"}}]
```

> **Changed by D4:** `trusted:true` is removed; the per-node broker ACLs make the command authentic. The reply post needs node auth from the addressed node.

The node POSTs its reply to `/api/v0/device-control-results/{request_id}`. CC writes the reply to `<tmp>/jarvis-device-control/{id}.json` and polls every 0.1 s, with a 10 s timeout. It returns `output`, or `{"success":false,"error":"the node didn't respond in time","timeout":true}`.

Mobile chat loops `MAX_TOOL_ITERATIONS = 5` rounds of tool calls, MQTT dispatch and `continue_conversation_with_tool_results` (`api/mobile_chat.py:28,333,397-456`).

### 3.6 Param validation, date injection, refinement

**Validation** (`core/param_validation.py`):

- **Type sources:** each `available_commands[].parameters[{name,type,enum_values}]` (`tool_execution_engine.py:399-423`).
- **Type spellings:** `array<t>`, `array[t]` and `t[]` all denote arrays (`:14-45`).
- **Scalars:**
  - `string`
  - `int`/`integer` (bool excluded)
  - `float`/`number`/`double`
  - `bool`/`boolean`
  - `date` (`^\d{4}-\d{2}-\d{2}$`)
  - `datetime` (has `T`, `fromisoformat` after a `Z`→`+00:00` swap, and **tz required**)
  - Unknown types pass.
- **Rules:** absent params are skipped. Enum validation uses `str(value) in allowed`.
- **Error strings** (these become prompt text): `"{tool}.{param} expected {type}"` and `"{tool}.{param} must be one of: a, b"`.

**Date injection** (`tool_execution_engine.py:430-615`). For every param whose schema `is_datetime_param`:

1. **Resolving the turn's dates:** the resolved dates come from MCP `resolve_date_keys` when `jarvis_mcp_client` imports, else the local date context. Unresolved keys go to `ResolveRelativeDateTool.resolve_with_llm_fallback` (LLM shape E). It picks one of the available keys, then a fuzzy match ignoring underscores, then defaults to `today`.
2. **Empty value:** inject the resolved dates, or `today` when there are none. Array params get the list; scalar params get the first.
3. **Non-ISO string:** look it up in the flattened context, then try the LLM fallback, then use the first resolved date.
4. **Array:** non-ISO items are resolved one by one. If nothing survives, the array becomes the resolved dates.

Output values are ISO strings.

**Refinement** (`core/param_refinement.py`), text path only in practice:

- **Why:** the compressed providers strip `_refinable` params from the prompt schema (`ToolBuilder.build(exclude_refinable=True)`; marker set in `prompt_providers/shared/command_converters.py:96-97`).
- **Which calls:** each client call except `control_device` and `get_device_status`.
- **Prompt:** one LLM call (shape D) built from the param lines, `Already selected: {args}`, few-shot examples from `tool.examples[].expected_parameters`, and `Return ONLY JSON: {…}`.
- **Parsing:** code fences are stripped, then non-null keys are merged into the args. **Any** key is merged, not only the refinable ones.

### 3.7 llm-proxy request shapes CC sends

**Common to every call:**

- **Target:** `POST {llm}/v1/chat/completions`.
- **Headers:** `X-Jarvis-App-Id` and `X-Jarvis-App-Key` (`core/utils/rest_client.py:6`).
- **Timeout:** 30 s through the shared `httpx` client; a non-2xx response raises.
- **URL:** from discovery, else `JARVIS_LLM_PROXY_API_URL`, default `:7704` (`core/llm_proxy_client.py:33-42`).

**Body defaults:**

- `include_date_context` is **always** sent (default true).
- `adapter_settings` is sent only when given.
- `conversation_id` is sent but **the proxy has no such field** (`jarvis-llm-proxy-api/models/api_models.py:84-100`), so it is ignored.

**Shapes:**

| # | Use | Body (beyond `model`, `messages`) |
|---|---|---|
| A | Engine iteration, text path | `"model":"live","temperature":0.4,"conversation_id","response_format":{"type":"text"},"include_date_context":true,"max_tokens":256` (1536 on the double-check pass). The Qwen25 base returns `{"type":"text"}` (`qwen25_medium_untrained.py:162-164`). The fallback JSON schema is `system_prompt_builder.get_response_format()`. |
| B | Engine iteration, native | `"model":"live","temperature":0.4,"conversation_id","tools":[{"type":"function","function":{name,description,parameters}}],"tool_choice":"auto","include_date_context":true,"max_tokens":256` |
| C | Text-mode tool-result formatting | `"model":"live","temperature":0.7,"conversation_id","include_date_context":true,"max_tokens":256` (`conversation_handler.py:2545-2552`) |
| D | Param refinement | `"model":"live","temperature":0,"messages":[{"role":"user",…}],"include_date_context":false,"max_tokens":256` |
| E | Date-key LLM fallback | `"model":"live","temperature":0,"include_date_context":false`, with **no max_tokens and no /no_think** (`resolve_relative_date_tool.py:289-293`) |
| F | `/api/v0/chat` passthrough | The client body is forwarded verbatim: `{model?="live", temperature?=0.7, messages}`. The upstream status and JSON are mirrored, with a 100 s timeout (`llm_proxy_client.py:262-289`). |
| G | Streaming (doc 01 paths) | `"stream":true,"include_date_context"` plus optional `max_tokens`. The response is SSE `data:` lines of **Jarvis frames** `{"delta":…}` … `{"done":true,"content","usage",…}`, not OpenAI chunks (`:121-177`). |
| H | Warmup | `"temperature":0,"stream":false`, 120 s timeout (`:196-248`) |
| I | Embeddings | `POST /v1/embeddings {"input":[…]}`, async and sync variants |
| J | Discovery | `GET /v1/engine` (`allows_caching`), `GET /v1/adapters/date-keys` (`static_keys`; note the adapter path) |
| K | Background slot (other subsystems) | `"model":"background"` plus `extra_body={"reasoning_budget":0}`: phone, errand planner/executor, signal automations, proposal/situation matcher, memory extraction, characterization, deep research (§6) |

**Response fields read:**

- `choices[0].message.content` (or a bare string)
- `choices[0].finish_reason`
- `choices[0].message.tool_calls`
- `usage.{prompt,completion,total}_tokens`
- the top-level `date_keys` (Jarvis extension, set when `include_date_context`; regex extraction with fastText fallback in the proxy, `jarvis-llm-proxy-api/services/model_service.py:393-408`)

The proxy may also **append a date-hint system message** to the request.

### 3.8 fastText routing gate

**What it is:** a k=1 intent classifier over the raw utterance, mapping it to a tool name. It never selects or prunes tools. Pruning was removed on 2026-07-21 to preserve the KV prefix (`conversation_handler.py:845-850`).

**When it is enabled** (`core/tool_routing.py:25-47`, `conversation_handler.py:2997-3045`). All of these must hold:

- env `JARVIS_TOOL_CLASSIFIER_ENABLED ∈ {1,true,yes}` (default **false**)
- env `JARVIS_TOOL_CLASSIFIER_MODEL_PATH` is non-empty
- `provider.use_tool_classifier` is true: true for the Qwen providers (interface default `ijarvis_prompt_provider.py:135-142`), false for ChatGPT

The settings `tool_classifier.enabled` (default True) and `tool_classifier.min_confidence` (`services/settings_definitions.py:34-50`) are **never read**.

**Prediction:**

1. `label.replace("__label__","")` gives the tool name, and the score is a float.
2. Any load or predict error gives `(None, 0.0)` (`core/tool_router/classifier.py:44-60`).
3. A prediction for a tool not in this turn's tool list is discarded (`tool_routing.py:76-101`).

The decision is `{"tool_name","score","used": score >= JARVIS_TOOL_CLASSIFIER_MIN_CONFIDENCE (0.6)}`. It is cached per conversation (`voice_command_helpers.py:90-124`).

**Who consumes the decision:**

1. **Fast stream path** (`conversation_handler.py:1177-1213`): taken when the tool is in `{"answer_question","quick_search"}`, the score is ≥ **0.8** (class constant), and for `quick_search`, `web_search.enabled` is set. Otherwise the turn falls back to blocking.
2. **Tool-stream path** (`:1575,1612-1640`): taken when `JARVIS_STREAM_TOOL_RESPONSES=true`, the provider is native, the score is ≥ `JARVIS_STREAM_TOOL_MIN_CONFIDENCE` (0.85), and the tool is a server tool outside the fast set.
3. **Router hint** system message (§3.1), when `used`.
4. **Must-call guard** prerequisite (§3.2 i).

**Fallback:** no decision means the blocking path, no hint and no must-call guard. Nothing fails.

> **Changed by D9 (F1):** the router is off in prod and is cut entirely, with all four consumers and the training route. Go behaves as today's "no decision" case.

**Training:**

- **Route:** `POST /tool-router/train` (`main.py:1602-1649`). The body is `ToolRouterTrainingRequest{available_commands, extra_training_jsonl?, extra_training?[{utterance,tool_name}], output_model_path?, save_training_jsonl=True, epoch=25, lr=0.5, word_ngrams=2}`.
- **Training sources** (`core/tool_router/training.py:107-131`), concatenated:
  - `temp/test_results.json`
  - a regex over `temp/test_command_parsing.py` (`CommandTest("utt","tool"`)
  - command `examples[].voice_command`
  - the extra JSONL and examples
- **Training run:** `fasttext.train_supervised` runs synchronously inside the async handler. It writes `temp/tool_classifier.bin`, or `output_model_path`, which is **any path the caller names**. It returns `{status, examples, model_path, training_jsonl_path}`.
- **Reload:** the loaded singleton is never reloaded, so a retrained model needs a restart.

## 4. Data

- **No tables.** All state lives in the per-conversation cache entry (doc 01; 10 min TTL):
  - `messages` (mutated in place by the engine)
  - `tools`
  - `available_commands`
  - `router_decision`
  - `force_tool_calls`
  - `issued_client_calls[{name,args_hash,timestamp}]`, unbounded within the TTL (`core/conversation_cache.py:318-355`)
  - `node_context` (`adapter_hash`, timezone, speaker)
- **In-process singletons:**
  - `tool_registry` and `tool_executor` (built at import)
  - the classifier singleton
  - the shared `httpx` client (50 connections)
  - `malformed_json_extractor` (`main.py:104`, unused)
  - `ModelService()` is **rebuilt per request** by `deps.get_model_service` (`deps.py:228-247`). Each rebuild re-runs provider and model discovery, and builds a new `ConversationHandler` and `LLMProxyClient`.
- **Files:**
  - **Usage, trace and metrics logs.** Appended forever to `/app/temp/llm_usage.log`, `llm_trace.log` (full prompts and responses) and `llm_metrics.log` (`core/usage_logging.py:33-130`; dev's trace log is 1.5 MB).
  - **MQTT reply mailboxes** in `<tmp>/jarvis-device-control/` and `/tmp/jarvis-node-tools/`.
  - **fastText files:** `temp/tool_classifier.bin` and `temp/tool_router_training.jsonl`.

## 5. Settings

| Key / env | Default | Effect |
|---|---|---|
| `llm.interface` (env `JARVIS_MODEL_INTERFACE`) | `Qwen25MediumUntrained` (`settings_definitions.py:16-19`), **a provider being cut** | Selects the prompt provider. Prod is `Qwen3_14B_Compressed`, dev is `Qwen3_8B_Compressed`. |
| `model.small_model_mode` (env `JARVIS_SMALL_MODEL_MODE`) | True | Invalid-param retry max: 1 when true, else 2. Also sets examples per command (`tool_call_parser.py:417-430`). |
| `voice.tool_dedupe_window_seconds` | 120.0 | Client-call dedupe window. ≤0 disables it. |
| `model.include_thinking` | False | `/think` vs `/no_think` suffix (doc 03) |
| `web_search.enabled`, memory/recall gates | per household | Text-path server-tool whitelist (§3.1) |
| env `JARVIS_INVALID_PARAM_RETRY_MAX` | as above | Overrides the retry max |
| env `JARVIS_TOOL_CLASSIFIER_ENABLED` / `_MODEL_PATH` / `_MIN_CONFIDENCE` / `_EXTRA_TRAINING_PATH` | false / "" / 0.6 / "" | Router gate (§3.8) |
| env `JARVIS_STREAM_TOOL_RESPONSES` / `JARVIS_STREAM_TOOL_MIN_CONFIDENCE` | false / 0.85 | Tool-stream path |
| env `JARVIS_PROMPT_INCLUDE_PARAM_DESCRIPTIONS`, `_TRIM_TOOL_DESCRIPTIONS`, `_MAX_TOOL_DESC_CHARS`, `JARVIS_EXAMPLES_PER_COMMAND` | true, true, 180, "" | Text tool formatting helpers in `tool_call_parser.py:325-512` (check doc 03 for whether kept providers use them) |
| env `JARVIS_LLM_USAGE_LOG_PATH` etc. | `/app/temp/*.log` | Log file paths |
| env `LIGHTWEIGHT_MODEL` | `mistral-7b-q2` | Dead (only the unused extractor reads it) |

## 6. Dependencies

- **CC subsystems:**
  - conversation cache, handler and streaming (01)
  - prompt providers: `parse_response`, `sanitize_text`, `build_tools`, `get_response_format`, `user_message_suffix`, `force_tool_calls`, `supports_native_tools`, `use_tool_classifier` (03)
  - date context, `normalize_date_key`, `resolve_date_keys`, `flatten_date_context` (03)
  - `transcript_filter` shape classifiers `is_question_shaped`, `is_action_command_shaped`, `is_report_shaped`, `response_claims_action` (01)
  - `not_for_me`, `exchange_complete`, `tts_text.clean_for_tts`
  - `latency_logger` and usage logging (00)
  - node MQTT command service (05)
  - the tools' owning subsystems
- **llm-proxy:** every call in §3.7. The live slot is used for all loop calls. The background slot is used only by other subsystems.
- **jarvis-mcp (optional):** `jarvis_mcp_client.resolve_date_keys`. Its absence falls back to local resolution (`tool_execution_engine.py:72-76,446-465`). mcp is being dropped.
- **Third party:** `fasttext` (pyproject), `httpx`.

## 7. Invariants and non-obvious behaviour

1. **Sentinel precedence.** `<not_for_me/>` is checked before every retry guard. `<exchange_complete/>` exempts a turn from both guards. Both are matched **outside `<think>`**, and an unclosed think runs to end-of-text (`tool_execution_engine.py:27-35,909,1018-1036`).
2. **Nag hygiene.**
   - Nags are standalone `system` messages identified by a **prefix**.
   - `[MUST_CALL_RETRY]` and `[TOOL_DEDUPE]` from earlier turns are scrubbed at entry. `messages[0]` is never touched.
   - `[NOT_FOR_ME_DOUBLE_CHECK]` and `[TOOL_DEDUPE]` are scrubbed as soon as the model replies.
   - `[INVALID_PARAM_RETRY]` and `[ISO_DATE_RETRY]` are **not** scrubbed, so they persist in history. `[ISO_DATE_RETRY]` is counted by substring (`:312-318`).
3. **Retry budgets.**
   - Force-tools allows 2 retries; must-call allows 1. They share one counter.
   - ISO-date allows 1, counted conversation-wide because it is never scrubbed.
   - Invalid-param allows 1–2, counted conversation-wide.
   - Double-check allows 1 per turn, and only while `iteration+1 < max_iterations`.
4. **Popped-prose restore.** A sentinel after a guard popped real prose restores that prose. It is never treated as silence (`:918-943`).
5. **Date schemas use the full cached tool list**, not a filtered one (`:1293-1297`). Keep this so validation and injection agree.
6. **Tool calls leave the engine with `arguments` as a JSON string** and ids of the form `call_<12 hex>`. Native ids are preserved.
7. **Server+client in one response:** the server results are appended and the loop continues. The client calls are discarded until re-issued (`:1363-1365`).
8. **Text-mode server-only results** return `server_tool_complete`, which goes to a separate formatting call. Text models never see `role=tool` again in that turn (`:1469-1486`).
9. **The double-check pass** is a **user** message ending `/think`, with `max_tokens` 1536 (`:958-975`).
10. **Dedupe** applies to client calls only. Args are canonicalized with `sort_keys` and `(",",":")` separators, then SHA-256. Unparseable args always pass. A second identical request after a nudge passes through (`:142-160,666-709`).
11. **Max-iteration text** "Maximum tool execution iterations reached." is returned as `complete`, so it is spoken.
12. **`/api/v0/chat` is a pure passthrough.** The node reads `choices[0].message.content`. Its system-role-only message shape must keep working.

## 8. Oddities

1. **The router gate is probably dormant everywhere.** The code default is off, and no compose, env file, installer or `~/.jarvis` file sets `JARVIS_TOOL_CLASSIFIER_ENABLED`. With it off:
   - the fast stream path and tool-stream path are unreachable
   - the router hint never appears
   - the must-call guard never fires

   The settings that look like they control it are dead.
2. **Dead code:**
   - `MalformedJsonExtractorService` (instantiated at `main.py:104`, never called) together with all of `core/json_schema.py`
   - `prune_tools_by_confidence` (`tool_routing.py:209`) and `prune_tools_by_router_decision` (`voice_command_helpers.py:127`)
   - `filter_tools_for_utterance` (identity)
   - `ToolExecutor._compact_tool_result` (only for the disabled date tool)
   - the engine's `get_command_examples` branch (wrong name, tool disabled)
   - `_format_tool_result_text_mode_UNUSED`
   - the legacy `IModelInterface` stack (`model_factory.py`, `models/jarvis_tool_model.py` with its own `_tool_execution_loop`, `ModelService.warmup_conversation`/`process_voice_command`/`health_check`/`get_model_info`). `ConversationHandler` uses `self.model` only for `.name` and fallbacks that never trigger with a provider present.
   - the adapter models (cut)
3. **The native path ignores the per-household gates.** It offers every registered tool, including `quick_search`/`deep_research` with `web_search` off and memory tools without a speaker. Only the tools' own `execute()` re-checks remain. The text path whitelists.
4. **`strip_jarvis_extensions` mutates the cached tool schemas.** It pops `_refinable` from the shared property dicts (`tool_builder.py:440-446`), and `get_tools` returns the live list (`conversation_cache.py:431`). On the native path the marker is gone before `refine_params` runs, so refinement silently never happens there. The refinable params are already in the native schema.
5. **Native transcript validity.** On invalid-param retry the assistant message carrying `tool_calls` is kept, with no `role=tool` reply (`:1420-1433`). The same applies to dropped client calls in "server+client" responses. A strict OpenAI backend (the ChatGPT provider) rejects an assistant `tool_calls` message that has no matching tool message.
6. **`has_tool` uses the global registry:**
   - any server-tool name runs, even if it was not offered
   - a client tool that shares a server tool's name is shadowed by the server tool
   - duplicate names are sent to the model
7. **Unauthenticated routes:**
   - `/api/v0/chat` (an open LLM relay on the LAN; the node already sends `X-API-Key`)
   - `/lightweight/chat`
   - `/mobile/node-tool-reports/{id}` and `/device-control-results/{id}`, so anyone who learns a request id can forge a node reply
   - `/tool-router/train` is node-authed, but it writes a model to a caller-chosen filesystem path and blocks the event loop while training
8. **Parser:**
   - a tool call whose `arguments` is a non-JSON string is **silently dropped** (`tool_call_parser.py:158-168`)
   - a top-level JSON array or string raises `AttributeError`, so the whole output becomes the spoken message (`:129-132`)
   - `#`-comment stripping is per line and resets string state at each newline
9. **The date LLM fallback** has no `max_tokens` and no `/no_think`, so a thinking model can burn a long budget on a one-word answer. It reads `response["message"]` before `choices`.
10. **`include_date_context=true` on every loop call** makes the proxy run its date extractor over the whole prompt's user text on every iteration. It can also append a hint message.
11. **CC CLAUDE.md is stale.** Invariant #14 says mobile chat never runs server tools. In fact mobile chat calls the same engine, so whitelisted server tools run in-process and only client calls go over MQTT. `llm.interface` defaults to a cut provider. CLAUDE.md also says `/tool-router/train` "gates the fast voice path", but the gate is env-only.
12. **`ModelService` is constructed on every request**, re-scanning the provider and model modules.
13. **`answer_question`** is a disabled server tool, yet it is in the text whitelist and the fast-stream set. It works only if a node offers a client tool of that name. The node-setup `answer_question` command is presumably that tool.

## 9. Tests

**Existing:**

| File | Tests | Covers |
|---|---|---|
| `tests/test_tool_execution_engine.py` | 65 tests, 3.2k lines | The guards, sentinel, dedupe, ISO and date injection |
| `test_param_validation.py` | 54 | Param validation |
| `test_tool_routing.py` | 25 | Routing |
| `test_native_tool_calling.py` | 10 | The native path |
| `test_tool_builder.py` | 10 | `ToolBuilder` |
| `test_tool_call_parser_bare.py` | 5 | Bare tool-call recovery |
| `tests/integration/test_tool_call_parsing.py` plus `fixtures/mock_llm_responses.py` | 23 | Tool-call parsing |
| `test_identify_speaker_tool.py`, `test_capability_registry.py`, `test_context_provider_client.py`, `test_chatgpt_openai_provider.py`, `test_mobile_chat_voice_command_plumbing.py`, `test_exchange_complete.py`, `test_followup_doubt.py` | — | As named |

**Gaps:** `param_refinement.py` has no tests, and nothing covers `/api/v0/chat` or `llm_proxy_client` request bodies.

**Golden fixtures** (input → output JSON, dumped from Python):

1. **Provider `parse_response`** for each kept Qwen provider:
   - closed think, unclosed think, `<message>` wrap (14B only)
   - multiple `<tool_call>` blocks, one malformed among them
   - a string `resolved_datetimes` wrapped into an array
   - an envelope with empty `tool_calls` plus `<tool_call>` embedded in `message`
   - a bare `{"name","arguments"}`
   - plain text, which gets wrapped
   - text starting with `{` that is invalid JSON, which gives None
2. **`ToolCallParser.parse_response`:**
   - `#` comments outside and inside strings
   - `tool_calls` array, singular `tool_call`, bare call
   - OpenAI `{"function":{…}}` form
   - string args, valid and invalid
   - `null` args
   - nested `resolved_datetimes` objects, with key normalization (`"Next Week"`→`next_week`, `:`→`_`)
   - `failure_message` passthrough
   - prose around JSON (balanced extraction), multiple objects (first valid wins), `{…}` slice fallback, unparseable text, top-level array
   - Ids are random; fixtures must mask them.
3. **`_normalize_native_tool_calls`:** dict args, non-string args, missing id, missing name.
4. **`find_invalid_params`/`is_iso_datetime`/`normalize_param_type`:** the full matrix, including `Z`, missing tz and bool-as-int.
5. **Engine ISO reverse-map and date injection:** frozen clock and timezone; scalar, array, empty and relative inputs.
6. **`_canonical_args_key`** digests.
7. **`utterance_matches_keywords`** and `collect_tool_keywords`.
8. **fastText predictions** over the corpus, only if the gate is kept (see Q1).

**Black-box contract tests**, using the fake LLM (PLAN §4.1):

- The scripted multi-iteration loop for each `stop_reason`.
- The 202 body shape.
- `continue` on native vs text.
- The exact request bodies A–F as the fake LLM records them.
- `/api/v0/chat` passthrough status mirroring.
- MQTT `tool_call` payload via the fake node, plus the reply mailbox.

## 10. Questions for the user

1. **[scope] Is the fastText router actually on anywhere?** Nothing in the repo or `~/.jarvis` sets `JARVIS_TOOL_CLASSIFIER_ENABLED` or `_MODEL_PATH`, and the code default is off. If it is off in prod, then the fast stream path, the tool-stream path, the router hint and the must-call guard are all dormant. PLAN §3.3 budgets a pure-Go fastText port and a corpus-parity test for it.
   - *Why it matters:* this is the biggest "port it or drop it" lever in this chapter.
   - *Options:*
     - (a) Port it byte-for-byte as planned.
     - (b) Drop the classifier. Keep the stream-path *code shape* but gate it on something else, such as the native model's first-token tool call.
     - (c) Keep the interface, ship it disabled, and port the inference later.
   - **Recommendation:** check prod logs for `Router predicted`. If it is absent, choose (c). Port nothing but the `RouterDecision` interface and the hint/must-call plumbing, behind a nil classifier.
   - **Decided (D9, F1):** the router is off in prod; **cut it entirely**, including the `RouterDecision` interface, the hint, the must-call guard and both stream paths. No pure-Go fastText.
2. **[scope] If the router stays, what happens to `/tool-router/train`?** Training is Python-only, writes to any path, blocks the loop and never hot-reloads.
   - *Options:*
     - (a) Keep the route, but have Go shell out to an offline Python trainer.
     - (b) Cut the route and make training an offline script that drops a `.bin` into `~/.jarvis/models/`, which `jarvisd` reloads on change.
     - (c) Keep the route as-is.
   - **Recommendation:** (b). The only caller is a dev script.
   - **Decided (D9):** cut the route; there is no router to train.
3. **[behaviour] Which per-household gates must the native path honour?** The text path whitelists server tools: web-search on/off, memory needs a speaker and the setting. The native path offers all 12 ungated. Since the Go port will run Qwen3.5-9B natively, this becomes the default path.
   - *Options:*
     - (a) Apply the text whitelist to both paths.
     - (b) Keep the native path ungated and rely on the `execute()` re-checks.
   - **Recommendation:** (a). It is fail-closed for egress, as CLAUDE.md #13 intends.
   - **Decided (D22):** (a). Web search off means no egress; memory tools need a speaker plus the setting (D21).
4. **[behaviour] Should a text-path (Qwen3 14B/8B) continue be allowed to chain another client tool?** Today it is a single formatting call, so "check the calendar then text Mom" cannot complete in one exchange. Native continue re-enters the loop.
   - *Options:*
     - (a) Preserve the asymmetry exactly.
     - (b) Unify on the loop for both paths.
   - **Recommendation:** (a) for parity in Phase 5, and revisit once native Qwen3.5 is the default.
   - **Decided (D23):** (a). Chaining on the text path is fixed in the post-port prompt-provider redesign.
5. **[behaviour] `/api/v0/chat` is unauthenticated.** The node already sends `X-API-Key`.
   - *Options:*
     - (a) Require node auth (or app/JWT) in Go.
     - (b) Keep it open for parity.
   - **Recommendation:** (a). It is zero client change, and it closes an open LLM relay on the LAN. The same question applies to the unauthenticated MQTT reply mailboxes. I'd require the replying node's key, and that the replying node is the one the request was addressed to.
   - **Decided (D5, D4):** neither option. `/api/v0/chat` is **dropped**; node `chat_text()` switches to the node-authed `/api/v0/node/llm/chat` (node-setup change). The reply mailboxes get node auth, with the replying node bound to the request.
6. **[behaviour] Native transcript validity.** On invalid-param retry, and when server and client calls are mixed, the assistant `tool_calls` message is left with no `role=tool` reply. That is fine for llama-server, but a strict OpenAI backend (the e2e ChatGPT provider) rejects it.
   - *Options:*
     - (a) Preserve it.
     - (b) In Go, pop the assistant message (or synthesize `{"error":"not executed"}` tool replies) before nagging or continuing.
   - **Recommendation:** (b), with a contract test.
   - **Decided (D8):** (b), fix it, with a contract test.
7. **[behaviour] The `_refinable` mutation bug.** On the native path, param refinement never runs, because the marker is stripped from the cached schemas first. Do you want refinement on native (Qwen3.5-9B)?
   - *Options:*
     - (a) Preserve today's effective behaviour: no refinement on native, with the refinable params left in the schema.
     - (b) Fix it so refinement runs on both paths.
   - **Recommendation:** (a). Native models see the full enum anyway, so a second LLM call is pure latency. Make it explicit in Go rather than accidental.
   - **Decided (D8, P2):** fix the aliasing bug (no in-place mutation of cached schemas). Applied as the recommendation: refinement runs on the text path only, by explicit rule rather than by accident. P2 says "fix" without choosing between (a) and (b); flag this if (b) was meant.
8. **[behaviour] Should the router-hint and must-call text be byte-exact?** PLAN demands byte-exact prompts, and the nag strings ([MUST_CALL_RETRY] ×2 variants, [ISO_DATE_RETRY], [INVALID_PARAM_RETRY], [TOOL_DEDUPE], [NOT_FOR_ME_DOUBLE_CHECK], Router hint) and the formatting-call user message are prompt text too.
   - *Options:*
     - (a) Treat them all as golden strings.
     - (b) Only treat the system prompt as golden.
   - **Recommendation:** (a). They are model-tuned and cheap to freeze.
   - **Decided (D22, settled in B triage):** (a). The Router hint and both `[MUST_CALL_RETRY]` must-call variants are cut with the router (D9); the force-tools `[MUST_CALL_RETRY]` string stays.
9. **[behaviour] When the iteration limit is hit, users hear "Maximum tool execution iterations reached."** Is that intended?
   - *Options:*
     - (a) Keep it.
     - (b) Use a natural fallback ("Sorry, I got stuck on that.").
     - (c) Return `not_for_me`-style silence.
   - **Recommendation:** (b), keeping `error:"max_iterations_exceeded"` for traces.
   - **Decided (D40 default):** (b).
10. **[scope] Do the jarvis-mcp date resolution and `include_date_context` on every loop iteration stay?** mcp is dropped, so MCP date resolution goes. The proxy's date-key extraction runs per iteration over the full user text. In-process, CC could call the date extractor once per turn.
    - *Options:*
      - (a) Keep the per-call flag semantics.
      - (b) Extract once per turn in the tool-loop module and stop sending the flag.
    - **Recommendation:** (b). Keep the `date_keys` contract only on the external `/v1/chat/completions` surface.
    - **Decided (D40 default):** (b), extract once per turn, from the raw transcript (03.Q8). The MCP path is dead and not ported.
11. **[minor] What should `llm.interface` default to?** The default is `Qwen25MediumUntrained`, which is cut, and the providers' `force_tool_calls` and native flags hang off inheritance from cut Qwen2.5 classes.
    - **Recommendation:** default to `Qwen3_8B_Compressed`, flatten the kept providers' flags into explicit Go values, and fail loudly on unknown names.
    - **Decided (D11, D12):** no default. The key becomes `llm.prompt_provider`, set at install time; an unknown name is a hard error. Flatten the flags into explicit Go values.
12. **[minor] What happens to the date-key LLM fallback** (live slot, no `max_tokens`, no `/no_think`, an `available_keys` list of roughly 60–100 entries)?
    - **Recommendation:** keep it, but cap it at `max_tokens:16` with `reasoning_budget:0`. Otherwise it is a latent multi-second stall on thinking models.
    - **Decided (D40 default):** moot. The fallback is dropped (03.Q9): the vocabulary gap is closed and unknown keys become `today`.

## 11. Go port notes

- **Package shape.** `internal/cc/toolloop`:
  - `Engine.Execute(ctx, conv *Conversation, in TurnInput) (Result, error)`, where `Result` has a typed `StopReason` (complete | tool_calls | validation_required | server_tool_complete | not_for_me | error).
  - A `Registry` of `ServerTool` interfaces: `Name`, `Description`, `Params`, `Enabled(ctx, hh)`, `PromptText`, `Risky`, `Execute(ctx, Call) (map[string]any, error)`. Tools are registered explicitly at init. No reflection discovery.
  - `Call` carries the speaker identity, or an explicit "unknown" (D21). Each tool decides what to refuse; the framework imposes no policy. Per-user tools refuse an unknown or ambiguous speaker, with the M14 wording when recognition is off.
  - `Enabled(ctx, hh)` (plus the speaker) applies to **both** paths (D22).
  - A `Parser` (pure functions, golden-tested).
  - A `Guards` set, where each guard is a small function over `(state, response)`, with the nag strings as constants (byte-exact, D22). No must-call guard and no router decision (D9).
  - An `LLM` interface implemented in-process by the llm-proxy module, so there is no HTTP. Shapes A–E map to one `ChatRequest{Slot, Temperature, MaxTokens, Tools, ToolChoice, ResponseFormat, ReasoningBudget, WantDateKeys}`. External `/v1/chat/completions` keeps the JSON extensions.
- **Context-aware server tools.** Python tools are sync on the event loop. In Go every tool gets a `ctx` with a deadline, and independent server calls in one response can run concurrently. Concurrency is optional; result order must stay the call order.
- **Explicit plane routing.** Compute the turn's offered-tool set once, with the same per-household gates on both paths (D22). Dispatch a call to the server plane only if the call name is in the offered server set. Reject or flag unknown names instead of running unoffered tools (Oddity 6).
- **Continue.** Text path: one formatting call, as today (D23). Native: re-enter the engine.
- **Iteration limit.** Speak a natural fallback; keep `error:"max_iterations_exceeded"` in the trace (D40).
- **Native transcript validity.** Never leave an assistant `tool_calls` message without matching tool replies (D8, Q6).
- **Refinement.** Text path only, by explicit rule; never mutate cached schemas (D8, Q7).
- **Dates.** Extract date keys once per turn from the raw transcript; no `include_date_context` per call and no LLM fallback (D40, 03.Q8/Q9).
- **Headless MQTT calls.** Replace the tmp-file mailboxes with an in-memory `map[requestID]chan Reply` (with the embedded broker the reply can even arrive over MQTT). Keep `POST /device-control-results/{id}` and `/mobile/node-tool-reports/{id}` as thin adapters that feed the channel, now node-authed with the replying node bound to the request (D4). Drop `trusted:true` from the payload (D4). Keep the 10 s timeout and the synthetic failure dicts.
- **One `ModelService` per provider.** Build it per provider change (the setting has a 60 s cache), not per request. Drop `IModelInterface`, `ModelFactory`, `JarvisToolModel`, the adapter models, `MalformedJsonExtractor`, `json_schema.py`, the prune helpers, `/lightweight/chat`, `/api/v0/chat` (D5), the fastText classifier and `/tool-router/train` (D9). Drop `/test/command` with jarvis-mcp.
- **Cache mutation.** `messages` is mutated in place and shared with the cache. In Go, have the engine own a working copy and commit it back once on return. This avoids the `_refinable`-style aliasing bugs, and makes the "transcript only changes on success" behaviour explicit. Today a mid-turn exception leaves a half-mutated history in the cache.
- **Risks:**
  - Byte-exact parity of the repair pipeline. Python `json.loads` accepts `NaN` and `Infinity` and duplicate keys (last wins), and `json.dumps` uses `", "` / `": "` separators. Arguments strings that reach the node and the dedupe hash must match. Use a Python-compatible encoder for `arguments`.
  - Regex differences: `re.DOTALL`, and `(?:</think>|\Z)` vs Go's RE2 `\z`.
  - `str(value) in allowed` enum coercion (`True`→`"True"`).
- **Logging (M2).** No `llm_trace.log`. Keep the metrics JSONL only behind a debug setting with a size cap; full prompts hold household data.
