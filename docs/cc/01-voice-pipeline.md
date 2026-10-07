# 01: Voice pipeline

Scope: conversation start and end, the voice command routes (blocking, stream, continue, continue-stream, acknowledge, wake-response), `ConversationHandler`, the conversation cache, warmup, wake verification, the transcript shape filters, the per-turn hints, TTS text scrubbing and acknowledgments.

Out of scope, by reference:
- **doc 02**: the tool loop internals (`ToolExecutionEngine`, the parser, the sentinel double-check, the force-tool-calls guard, dedupe, the router classifier).
- **doc 03**: prompt construction (`build_system_prompt`, `core_rules` strings, the speaker block).
- **doc 06**: STT/TTS media proxy, speaker resolution and stickiness.

All paths are relative to `jarvis-command-center/app/` unless prefixed. Node paths are under `jarvis-node-setup/`.

---

## 0. Decisions applied (2026-10-06)

Source: `QUESTIONS.md`. §1–§9 still describe today's Python behaviour; changes are flagged inline as "Changed by D#".

- **D2 / D3 (speaker).** The "last speaker" concept is dropped: `node_context.speaker_user_id` / `speaker_confidence` on `/conversation/start` are accepted and **ignored**. CC's 30 s per-node stickiness is dropped too. Speaker identity is **per conversation only**: it comes from turns identified in this conversation and dies with it. Nothing is keyed per node or survives across conversations.
- **D9 (cuts, with F1).** The fastText router is cut, and with it **path A (the fast stream path)**, **path B (tool-stream)**, the `Router hint:` message, `JARVIS_STREAM_TOOL_*` and `JARVIS_TOOL_CLASSIFIER_*`. `/voice/command/stream` always runs path C. `ambient_grounding.py` is cut.
- **D8.** Internal bugs are fixed (cache pollution §8.3, the `include_thinking` race §8.10). Wire-visible quirks the frozen node depends on (§8.6 status codes and body shapes) are kept and documented.
- **D19.** **Every completed voice turn** (stream, continue/stream, continue, blocking) writes a transcript, **when the speaker is confidently identified**, and only when the household has memory on. Fixes §8.1.
- **D21 / D35 / M14.** An unknown or ambiguous speaker means per-user tools refuse; household-level things still work. Speaker recognition is **off by default**, so every speaker is unknown until a household turns it on; refusals then say "speaker recognition is off".
- **D22.** Both prompt paths ship: text (Qwen3 14B/8B) and native (Qwen3.5-9B, ChatGPT). Providers are ported byte-exact. The native path gets the text path's per-household server-tool gates.
- **D23.** The text-path continue asymmetry is kept: text continue is one formatting call; native continue re-enters the loop.
- **D33.** One `voice.similarity_threshold` plus `voice.min_speaker_margin` decide the turn speaker (owned by 06).
- **D38.** The affect pass is cut. `affect` stays in the request as `null`; the affect-hint code is kept as a no-op. `voice.emotion_*` is dropped.
- **D40 (B defaults).** Q2: sliding idle TTL, sweeper, evict on `/conversation/end`, entry cap. Q6: keep wake verification, computed in-process (verify prod's mode first). Q8: emit the real engine sample rate (verify on a Pi). Q9: accept the empty `X-Assistant-Message` gap. Q10: native warmup `max_tokens=1`.
- **D47 / M4.** `adapter_settings` and `skip_warmup_inference` are dropped from the `/conversation/start` schema, with `JARVIS_TEST_MODE`. Old senders still work: the decoder ignores unknown fields.
- **M5.** `/voice/acknowledge` keyword matching gets word boundaries.
- **D5 (via 02).** `/api/v0/chat` is dropped; this doc's routes are unaffected.

## 1. Purpose

This subsystem turns a transcribed utterance from a Pi node into one of:
- spoken audio (streamed PCM), or
- a JSON instruction for the node: run these client tools, ask this clarifying question, or stay silent (`not_for_me`).

It also keeps the multi-turn conversation state that lets follow-ups ("turn them back off") work.

| Caller | What it uses |
|---|---|
| **Pi node** (`jarvis-node-setup`) | Primary user. Every wake cycle calls `/conversation/start`, `/voice/command/stream`, and optionally `/voice/command/continue[/stream]`, `/voice/command` (follow-ups), `/voice/acknowledge` and `/conversation/end`. It prefetches `/wake-response` off the critical path. |
| **install-e2e** | Phase 2 behaviour suite. Calls blocking `/voice/command` (`install-e2e/test_behavior.py:119`). |
| **Mobile chat** (doc 13), errand executor and workflow engine (doc 09) | Reuse `ConversationHandler.warmup_conversation_with_tools` and `process_voice_command_with_tools` in-process (`api/mobile_chat.py:231,313`, `services/errand_executor.py:247`). The handler is therefore shared infrastructure, not voice-only. |

The latency goal is the reason for most of the complexity. The design keeps llama.cpp's KV prefix cache warm and streams LLM output sentence by sentence into TTS.

## 2. Entry points

All routes are on `v0_router`, mounted at `/api/v0` (`main.py:694,2257`). Auth for all of them is node auth `X-API-Key: node_id:node_key` via `deps.verify_api_key` (`deps.py:162`, doc 00).

| Method and path | Code | Live caller (node unless noted) |
|---|---|---|
| `POST /api/v0/conversation/start` | `main.py:870` | `clients/jarvis_command_center_client.py:702`, from a background thread started at wake (`core/wake_loop.py:721-728`) |
| `POST /api/v0/conversation/end` | `main.py:1100` | `core/follow_up_loop.py:426`, at the end of the wake cycle |
| `POST /api/v0/voice/command` | `main.py:1161` | **Follow-up turns** (`utils/command_execution_service.py:718` via `follow_up_loop.py:342`), `parse_voice_command`, and install-e2e. **Appendix A is wrong** to say the node only uses the stream variants. |
| `POST /api/v0/voice/command/stream` | `main.py:1367` | Every fresh wake turn (`command_execution_service.py:637`) |
| `POST /api/v0/voice/command/continue/stream` | `main.py:2070` | After client tool execution, when no tool asked for `wait_for_input` (`command_execution_service.py:829`) |
| `POST /api/v0/voice/command/continue` | `main.py:2171` | Fallback after a continue-stream 202, after `wait_for_input` tools, and for validation answers (`command_execution_service.py:873`, `jarvis_command_center_client.py:404`) |
| `POST /api/v0/voice/acknowledge` | `main.py:1317` | Only if the main response hasn't landed within `ACK_TIMER_SECONDS = 3.0` (`command_execution_service.py:1445,1466`) |
| `POST /api/v0/wake-response` | `api/wake_response.py:69` | `wake_response_providers/jarvis_tts_wake_response.py:43`. Prefetched for the *next* wake and cached as a WAV on the node. |

- **Background loops:** none. Notably, nothing sweeps the conversation cache (see §8).
- **MQTT:** none.
- **Internal callers of the handler:** `api/mobile_chat.py`, `services/errand_executor.py`, `services/workflow_engine.py`, and `api/test_commands.py` (that route is cut).

## 3. Behaviour

### 3.1 One voice turn, stage by stage

Times are taken from the code comments and are indicative. ⏱ marks a latency-relevant point.

```
NODE                                   CC                                     OTHER
wake fires ──────────────────────────────────────────────────────────────────────────────
 ├─ new conversation_id = uuid4 (wake_loop.py:721)
 ├─ thread: POST /conversation/start ─▶ build node_context (main.py:886-1070)
 │   (predicted speaker = LAST speaker,   ⏱ auth: member names, speaker name ─▶ jarvis-auth (TTL cache)
 │    get_last_speaker())                 ⏱ DB: room hierarchy, memories, characterization, ambient
 │                                        ⏱ GET /v1/adapters/date-keys ───────▶ llm-proxy
 │                                        build system prompt (doc 03), cache.set (handler:499)
 │                                        ⏱ WARMUP inference, live slot ───────▶ llm-proxy (~1.4s cold prefill)
 │                                     ◀─ 200 {status, conversation_id, home_context}
 ├─ records the command concurrently with the warmup
 ├─ POST /media/whisper/transcribe ──▶ (doc 06) ─────────────────────────────▶ whisper (STT + speaker id)
 │   (file, speaker_audio, conv_id)     then fire-and-forget run_wake_verification (media.py:176-186)
 │                                        ─▶ whisper on the leading 2.2s ─▶ cache.set_wake_verification
 ├─ pre-route? (node-side fast paths; may skip CC entirely, command_execution_service.py:589)
 ├─ join the warmup thread (≤10s)
 ├─ ack thread: wait 3s ─▶ POST /voice/acknowledge (only if still waiting)
 └─ POST /voice/command/stream ──────▶ main.py:1367
                                        tools cached? else 400
                                        validate speaker_user_id ∈ household
                                        A) FAST PATH  handler.stream_voice_response (1131)
                                           noise? → None. Router (fastText) ∈ {answer_question,
                                           quick_search} with score ≥0.8 (quick_search also needs web_search)?
                                           ⏱ wake-verify wait ≤1.2s (wake turns, mode≠off)
                                           build per-turn blocks + hints, plain-text override
                                           ⏱ open LLM stream; pre-buffer ≥80 chars, sentinel check
                                           ⏱ GET tts /audio/format
                                     ◀─ 200 audio/raw; LLM tokens → sentences → TTS ─────▶ tts /speak/stream
                                        B) TOOL-STREAM  (env-gated, native-tools only; dead in prod)
                                        C) STANDARD  process_voice_command_with_tools (719)
                                           noise → not_for_me; ⏱ wake-verify; doubt propagation
                                           filter + route, per-turn blocks, ⏱ agent context (opt-in)
                                           ⏱ tool loop (doc 02; may run server tools) ──▶ llm-proxy ×N
                                           sentinel → not_for_me; exchange_complete; filler guard
                                           prose & complete → 200 audio/raw (stream_text_as_audio)
                                     ◀─    else → 202 JSON VoiceCommandResponse
 ├─ 200: play PCM (8s inter-chunk idle timeout, command_execution_service.py:432)
 └─ 202: _run_conversation_loop (≤10 iterations, command_execution_service.py:795)
     ├─ not_for_me → silent, LED, arm cool-down
     ├─ tool_calls → execute client tools on the node
     │    ├─ no wait_for_input → POST /continue/stream ─▶ 200 PCM  |  202 {"fallback":...}
     │    └─ else / fallback   → POST /continue        ─▶ JSON (loop again)
     └─ validation_required → ask the user → POST /continue (tool_result carries the answer)
follow-up window (follow_up_loop.py): per heard utterance
 └─ POST /voice/command (BLOCKING, turn_source=follow_up) → text → node-side TTS
end of cycle → POST /conversation/end
```

### 3.2 `/conversation/start` (`main.py:871-1097`)

1. **Server-trusted `node_context`** (`main.py:886-897`). `room`, `node_id`, `user`, `voice_mode` and `household_id` come from the validated node row. `adapter_hash: None` stays as a dead key.
2. **`home_context`.** `{"location": household_location(hh)}`, or `None` (`905-910`). It is returned to the node and also put in `node_context`.
3. **`household_member_names`.** From `resolve_member_names(auth_url, member_ids)`. Best-effort (`918-929`). It feeds the "addressed to another member" hint (§3.7).
4. **Adapter test-mode override.** An `if False` dead block plus `JARVIS_TEST_MODE` (`935-975`). Cut with LoRA.
5. **`room_hierarchy`.** Included only if any room has a `parent_room_id` (`978-995`).
6. **Fields copied from the client's `node_context`:**
   - `timezone`, into `node_context` too, so tools resolve in local time (`1000-1006`)
   - `agents`, read-only node-agent data (`1008-1010`)
   - `recently_shown_items` (`1016-1017`)
7. **Speaker.**
   - `validated_speaker_user_id` drops IDs that aren't household members (`1023-1028`).
   - If there's no ID, the speaker is inherited from per-node stickiness. A confident ID is recorded (`1032-1054`; doc 06).
   - The display name is resolved (`1056-1070`).

   > **Changed by D2 / D3:** Go ignores the client's `speaker_user_id` / `speaker_confidence` here (it is the node's never-expiring last speaker) and has no stickiness. A conversation starts with **no speaker**; the speaker comes from turns identified in this conversation, and the per-turn speaker change re-resolves name and memories (§7.9).
8. **Warmup.** `model_service.warmup_conversation_with_tools(...)` (`1076-1085`). It is **awaited**: the response returns only after the warmup inference, despite the "return immediately" comment at `1088`.
9. **Response.** `{"status":"success","conversation_id":…,"home_context":…|null}`. Any exception gives a 500 with `detail="Failed to start conversation: …"` (`1094-1097`).

**`ConversationHandler.warmup_conversation_with_tools`** (`core/conversation_handler.py:256-566`):

- **Settings resolved:**
  - memory and recall gates (fail open, `3333-3364`)
  - `web_search.enabled` (fail closed, `3367-3400`)
  - household persona into `node_context["household_persona"]` (`300-308`)
- **Tool set.** This is the key branch.
  - **Text-path providers** (`supports_native_tools == False`) get a whitelist of server tools (`314-369`):
    - always: `answer_question`, `make_phone_call`, `run_errand`, `schedule_errand`, `list_scheduled_errands`
    - with web search on: `deep_research`, `quick_search`
    - with an identified speaker and memory on: `remember`, `forget` (plus `recall` if recall is on)
  - **Native providers** get `tool_registry.get_tools_for_model(...)` (`371`).
  - Client tools are appended unfiltered (`375`, `warmup_service.py:133`).
- **Examples and antipatterns.** Merged via `warmup_service` (`383-397`). Antipatterns referencing unknown commands are dropped (`warmup_service.py:91-131`).
- **Loaded into `node_context`:**
  - memories (`399-431`)
  - characterization, if `characterization.injection_enabled` (`439-462`)
  - the ambient bundle, if `ambient_context.enabled` and memory are on (`471-475`). This is a 15-minute-quantised clock plus the latest weather, calendar and reminder household memories plus live Signals (`3192-3268`).
  - `date_keys` from llm-proxy (`483-489`)
- **System prompt.** `_get_system_prompt` (`3270-3330`). It is the provider prompt plus `NOT_FOR_ME_INSTRUCTION`, `EXCHANGE_COMPLETE_INSTRUCTION`, an optional personality reminder and an optional `<person_view>` tail. When characterization injection is on, it stashes `_system_prompt_base`.
- **Cache entry.**
  - `conversation_cache.set(...)` with `messages=[system]` (`496-506`)
  - seed `referenced_items` from `recently_shown_items` (`512-514`)
  - `force_tool_calls` flag (`518-519`)
- **Warmup inference** (`532-566`). The error is non-fatal.
  - Native providers: the full `chat_completion` with built tools and **no `max_tokens`** (`540-548`).
  - Text providers: `max_tokens=1`, only if `/v1/engine` says `allows_caching` (`554-563`).

**Kept providers.**
- `Qwen3_14B_Compressed` (prod) and `Qwen3_8B_Compressed` (dev) inherit `supports_native_tools = False`, so they use the **text path**.
- `Qwen3_5_9B_Compressed` and `ChatGPTOpenAI` are **native** (`prompt_providers/medium/untrained/qwen3_5_9b_compressed.py:53-57`, `large/untrained/chatgpt_openai.py:56-57`).
- Both branches are therefore live.

### 3.3 `/voice/command/stream`: the three paths (`main.py:1368-1599`)

- **Precondition.** If `conversation_cache.get_tools(cid) is None`, the route returns **400** with `"Conversation not initialized for tool-based flow"` (`1396-1401`).
- **Speaker.** `speaker_user_id` is validated against the household (`1405-1410`).
- **Turn context.** Each path builds its own `turn_context` dict from `turn_source`, `wake_confidence`, `follow_up_iteration`, `self_playback` and `self_playback_kind`.
- **TTS client.** `TTSClient(household_id, node_id, conversation_id)` is built once (`1415-1419`). It uses the `tts.url` setting, then discovery, then a fallback (`core/clients/tts_client.py:62-73`).

> **Changed by D9 (F1):** paths A and B depend on the fastText router, which is off in prod and is cut. Go ports only path C. A and B are described below for reference.

**A. Fast path:** `stream_voice_response` (`conversation_handler.py:1131-1551`).

Gates. Any failure returns `None`, which falls through to the next path:
- not STT noise (`1161`)
- messages are cached (`1172`)
- the router decision exists (`1182`)
- the predicted tool is in `{answer_question, quick_search}` (`1128,1189`)
- if `quick_search`, web search is on (`1201`)
- score ≥ `0.8` (`1129,1209`)
- wake verification is not enforce plus unverified (`1227`)

Steps:
1. Run follow-up doubt propagation (`1239`).
2. **Mutate the cached `messages` list in place:**
   - strip transient system blocks
   - trim history
   - characterization swap
   - append the speaker block, ambient block, recently-shown block, the user message with hints and suffix, and the plain-text override `"Respond naturally in plain text. Do not use JSON format or call any tools. Answer the user's question directly."` (`1248-1350`)
3. Open `chat_completion_stream(max_tokens=512)` with **no tools** (`1398-1402`).
4. **Pre-buffer.**
   - Read events until ≥80 non-think chars.
   - If `<not_for_me/>` appears, return `None` (falls to the blocking path).
   - Wait while the sentinel is partial or a think block is open (`1403-1443`).
5. **Generator.**
   - Chain the buffered and remaining events.
   - Strip complete think blocks and hold while a block is open.
   - Split on `(?<=[.!?])\s+`. Each complete sentence runs through `clean_for_tts`, then `tts_client.speak_stream(sentence)`, and its chunks are yielded.
   - Flush the remainder.
   - Per-sentence TTS errors are logged and skipped (`1451-1531`).
6. **Commit.**
   - Append the assistant message (think stripped) and run `update_messages`.
   - `_record_answered_round` (`1536-1544`).
   - Spans: `llm_stream_first_token` and `llm_stream_total`.
7. **Route returns:** `200 audio/raw` with `X-Audio-Sample-Rate|Channels|Sample-Width` taken from `GET {tts}/audio/format`. If that call fails, the fallback is `16000/1/2`. `X-Assistant-Message: ""` (`1439-1460`).

**B. Tool-stream path:** `stream_voice_response_with_tools` (`1553-1949`).

Gates:
- `JARVIS_STREAM_TOOL_RESPONSES == "true"` (default off, `1575`)
- a native-tools provider (`1604`)
- router score ≥ `JARVIS_STREAM_TOOL_MIN_CONFIDENCE` (0.85)
- the predicted tool is not fast-path-eligible and **is a server tool** (`1630-1640`)

Steps:
1. Work on a **copy** of the messages (`1668`).
2. Iteration 1: blocking `chat_completion(tools, tool_choice="auto", max_tokens=256)` (`1784`).
3. If iteration 1 returns tool calls, run `tool_executor.execute_tool_calls` **synchronously**.
4. Fall back (return `None`) if any client calls, a `request_validation` call, or no results came back (`1837-1851`).
5. Iteration 2 streams to TTS like path A. The cache is committed only if the prose is non-empty.

Because both prod and dev use text-path providers, **this path is unreachable in prod and dev today.**

**C. Standard path:** `process_voice_command_with_tools` (`719-1125`).

1. `is_stt_noise` gives `{"stop_reason":"not_for_me","assistant_message":""}` (`762-775`).
2. Wake verification in enforce mode with an unverified verdict also gives `not_for_me`. In bias mode it sets `turn_context["wake_verified"]=False` (`789-795`, `595-643`).
3. Doubt propagation for follow-ups (`801`, `645-688`). See §3.6.
4. Load the cache. If `messages` is missing, raise `ConversationPreconditionError` (`805-823`).
5. Keyword tool filtering (`830`; it filters the tool list only and never rebuilds the prompt) and router classification (`838`, `2997-3045`; the min-confidence env is `JARVIS_TOOL_CLASSIFIER_MIN_CONFIDENCE`, default 0.6).
6. Per-turn assembly (`858-971`):
   - strip transient blocks, trim to `conversation.max_turns`, characterization swap
   - speaker block, ambient block, recently-shown block
   - `Router hint: likely tool is '<t>'. …` if `used`
   - agent context, only if `model.advanced_context` (`909-912`, `3102-3175`)
   - the user content is `voice_command` + `\n\n`direction hint + `\n\n`affect hint + `\n\n`turn hint + `\n\n`profile hint + `\n\n`agent context + `\n`provider suffix (`/no_think` or `/think`)
7. `ToolExecutionEngine.execute(max_iterations = 10 if native else 3, sentinel_double_check=should_double_check_sentinel(...))` (`977-1005`; doc 02).
8. If the stop reason is `server_tool_complete` (text path), run `_format_tool_result_text_mode` (`1011-1026`, see §3.4).
9. A sentinel anywhere in `assistant_message` gives `not_for_me` and a structured `not_for_me_sentinel` log line (`1032-1101`).
10. Write back with `update_messages`, then `apply_exchange_complete` (strips `<exchange_complete/>` and sets `end_of_exchange`) and `_rewrite_terminal_filler`. Filler like "Task completed" becomes "Sorry, I didn't quite catch that — could you say it again?" (`569-593`, `1104-1115`).
11. `_record_answered_round` unless the stop reason is `not_for_me` or `error`. A 202 tool-calls round counts here once (`1120-1123`).

The route then splits (`1522-1593`):
- **Stop reason `complete` and `assistant_message.strip()` non-empty:**
  - Returns `200 audio/raw` via `stream_text_as_audio(text)` (`core/streaming_handler.py:205`).
  - Up to 2 sentences: per-sentence TTS. More: 4-sentence chunks with the next chunk prefetched into a buffer and yielded in 4096-byte pieces.
  - `X-Assistant-Message` carries the **URL-quoted full text** (`1542`).
  - `clean_for_tts` is not applied here; the engine already sanitised the text.
- **Anything else:** `202` with a JSON `VoiceCommandResponse` (`1567-1593`). This route does **not** strip `[Tool data:` and does not include `reasoning` (it is null).
- **Exceptions**, including `ConversationPreconditionError`: **500** with `detail="Failed to process command: …"` (`1595-1599`).

`_end_trace_after_stream` keeps the latency trace open until the generator finishes. It records the `first_audio_byte` checkpoint and the `audio_stream` span (`main.py:1334-1364`).

### 3.4 Continue routes

**`/voice/command/continue/stream`** (`main.py:2071-2169`):
1. `_maybe_push_actions_to_inbox` is **awaited** before streaming (`2117-2121`). The comment says fire-and-forget; it is not. For any `output.context.actions` it pushes a confirmation to the notifications inbox (`2020-2068`; doc 13).
2. `stream_continue_with_tool_results` (`conversation_handler.py:1951-2341`):
   - Stash `referenceable_items` (`1984`).
   - No cached messages → `None`.
   - **Fast path** (`2018-2090`). If not a knowledge delegation and **every** tool result has a non-empty `message` or `response`, speak `clean_for_tts(" ".join(...))` directly with no LLM call. It commits the native shape (keeping `role=tool`) or the text shape (dropping `role=tool`) plus the assistant message.
   - **Otherwise:**
     - Native providers: append `role=tool` messages.
     - Text providers: drop `role=tool` and inject the results as a user message ("Here are the tool results…", or "Answer the question from your own knowledge…" for delegation).
     - Add a plain-text system override to **the LLM copy only** (`2142-2156`).
     - Stream with `temperature=0.7, max_tokens=512`.
     - If zero sentences were spoken (JSON-only, think residue or empty), substitute the tool messages or errors (`2189-2316`).
3. A `None` result gives `202 {"fallback":"use_blocking_continue"}` (`2131-2139`). Success is `200 audio/raw` with an empty `X-Assistant-Message`. An exception gives a 500.
4. There is no `not_for_me` check and no `apply_exchange_complete`. `clean_for_tts` strips the marker, so `end_of_exchange` is lost on this path.

**`/voice/command/continue`** (`main.py:2172-2254`) runs `continue_conversation_with_tool_results` (`conversation_handler.py:2343-2428`):
1. No messages → `ConversationPreconditionError` (route returns 422). Stash items. Append `role=tool` messages.
2. Native providers: `engine.execute(max_iterations=10)`. The engine can itself return `not_for_me` or more `tool_calls`.
3. Text providers: `_format_tool_result_text_mode` (`2430-2625`).
   - If every result has a message, use it with no LLM call and drop `role=tool`.
   - Otherwise inject `The user asked: "<last user msg>"` plus the instructions, the results and `\n/no_think` as a user message, and call `chat_completion(max_tokens=256, temperature=0.7)`.
   - Then strip `<tool_call>` and extract `<think>` into `reasoning`.
   - Then `provider.sanitize_text` and `clean_for_tts`.
   - If a bare JSON tool call came back, use the tool-message fallback, or `"Done."`.
4. `update_messages`, then `apply_exchange_complete`.

The route pushes inbox actions **after** the LLM call (`2203`). `StopReason(...)` is constructed strictly (`2216`): an unknown value raises, which is caught generically. The error body is `200` with `commands[0].errors.type="processing_error"` and **`stop_reason: null`** (`2238-2254`). The node treats that as `"Unknown stop_reason: None"`.

### 3.5 Blocking `/voice/command` (`main.py:1162-1310`)

- **Precondition.** No cached tools gives **422** with `"Conversation not initialized for tool-based flow"` (`1180-1182`). `ConversationPreconditionError` also gives a 422 (`1279-1283`).
- **Speaker.** `process_voice_command_with_tools` is called **without** `speaker_user_id`. The request field is accepted and ignored here (`1186-1198`).
- **`stop_reason == "error"`.** Returns 200 with `commands=[{success:false, errors:{type:"llm_error", message}}]` and `stop_reason:"error"` (`1210-1233`).
- **Otherwise:**
  - Strip a leading `[Tool data: …]\n\n` from the message (`1235-1240`).
  - Include `reasoning` and `end_of_exchange`.
  - An unknown stop reason is logged and becomes `complete` (`1202-1208`).
- **Fire-and-forget `_log_transcript`.** This is the **only voice path that writes transcripts** (`1263-1270`, `1124-1158`). It requires a speaker and a household in the cached `node_context`.

  > **Changed by D19:** in Go every completed voice turn writes one transcript (stream, continue/stream, continue and blocking), only when the speaker was confidently identified and the household's `memory.enabled` is on.
- **Generic exception.** Returns **200** with `commands[0].errors.type="processing_error"` and `stop_reason:"complete"` (`1284-1310`).

### 3.6 Follow-up doubt and answered rounds

- **Wake verdict.** `resolve_wake_verification` (`core/wake_verification.py:267-326`).
  - It applies only when `turn_context.source == "wake"` and `voice.wake_verification_mode ∈ {bias, enforce}`.
  - It polls `cache.get_wake_verification` every 0.1s for up to **1.2s**. A timeout, a missing verdict or `off` all fail open.
- **Verdict producer.** `run_wake_verification` (`218-264`):
  - slices the leading 2.2s of `speaker_audio`
  - runs whisper with `speaker_recognition=False` and a 10s timeout
  - fuzzy-matches the transcript: Damerau-Levenshtein ≤2 per token against `voice.wake_verification_phrase`
  - stores `{verified, verdict, transcript, phrase, similarity, node_id, elapsed_ms}` via `set_wake_verification`
- **Doubted conversations.** For `follow_up` turns where the stored verdict is `unverified`, `turn_context` gets `conversation_wake_verdict`, `doubt_round = answered_rounds` and `doubt_max_rounds` (`voice.followup_doubt_max_rounds`, default 2) (`conversation_handler.py:645-688`). The turn hint then adds a caution or wrap-up lean (`core/turn_context.py:412-431`).

### 3.7 Per-turn hints (all appended to the user message, never to `messages[0]`)

All of these strings are prompt bytes, so they must be ported byte-exact.

| Hint | Module | Emitted when |
|---|---|---|
| `[direction hint: …]` | `core/direction_hint.py:74-192` | Pre-wake VAD seconds: quiet < `1.5`, active > `4.5`, borderline wake confidence < `0.75`. Also multi-speaker dash transcripts and very short fragments. Self-playback (music) makes the VAD reading uninformative. |
| `[voice: …]` | `core/affect_hint.py:251-291` | `affect.confidence ≥ 0.5` and arousal `low` or `high` with a non-empty `read`. |
| `[turn context: …]` | `core/turn_context.py:66-168` | `wake` (unverified / low-confidence / fresh-wake variants, with a media note), `follow_up` (by iteration; strict from iteration 3; doubt and addressed-member notes), `chat` (mobile), or inferred wake when only `pre_wake_speech_seconds` is present. |
| `[profile match: …]` | `core/profile_match.py` | A content word of the utterance (≥3 chars, not a stopword) appears in a `- ` line of the speaker block. At most 3 lines. |

`should_double_check_sentinel` (`turn_context.py:171-258`) decides whether the engine may run a `/think` re-check on a first-look sentinel. The re-check itself is doc 02.

Shape detectors (`core/transcript_filter.py`) classify **form**, not meaning:
- `is_stt_noise` (`525-558`): empty input, bracket-only (`*sniff*`, `[laughter]`), punctuation filler, or no word characters.
- `is_device_command_shaped`, `is_music_control_shaped`, `has_multi_speaker_markers`, `addressed_household_member`, `is_short_non_command_fragment`.
- `is_action_command_shaped`, `is_report_shaped`, `is_question_shaped`, `response_claims_action`: these are also used by doc 02's force-tool guard.

### 3.8 `/voice/acknowledge` and `/wake-response`

- **`/voice/acknowledge`** (`services/acknowledgment_service.py:75-91`). It makes no LLM call. It does a first-match regex search (substring, case-insensitive, **not word-bounded**, so "show" matches `how`) over 8 keyword pools and returns `random.choice`. Otherwise it picks from a 6-entry generic pool. The response is `{"text": "..."}`.
- **`/wake-response`** (`api/wake_response.py:69-130`). A system prompt asks for a 1–3 word greeting. The user turn is `"Hello Jarvis"` plus the provider suffix. It calls `chat_completion(temperature=1.1, max_tokens=12, include_date_context=False)`, then `sanitize_text` and `clean_for_tts`. It always returns 200 with `{"text"}` and falls back to `"Yes?"`.

## 4. Data

There are **no tables owned by this subsystem.** Reads go to `rooms`, `user_memories`, `characterizations` and `signals`. The only write is `transcripts`, from blocking `/voice/command`. Traces go to `request_traces` via `latency_logger` (doc 00).

**Conversation cache** (`core/conversation_cache.py`). This is a process-global dict guarded by a `threading.Lock` (`86-93,525`).

| Key | Set by | Meaning |
|---|---|---|
| `messages` | warmup `set`; `update_messages` | The chat history. `[0]` is the cached system prompt. **The same list object is returned by `get_messages` with no copy** (`118-135`). |
| `available_commands` | warmup | Merged command defs (examples, antipatterns), used by keyword filtering |
| `tools` | warmup | Merged server and client OpenAI tool defs |
| `timezone` | warmup | The node's IANA zone |
| `node_context` | warmup | A live dict. It is mutated per turn: speaker, memories, `_system_prompt_base`, `characterization`, `ambient_context`, `date_keys`, `household_member_names`, `home_context`, `agents`, `recently_shown_items`, `household_persona`, `room_hierarchy`. |
| `router_decision` | every turn | `{tool_name, score, used}` or None |
| `referenced_items` | warmup seed; continue paths | The "recently shown" list for `act_on_items` |
| `wake_verification` | media proxy background task | The verdict dict |
| `answered_rounds` | `_record_answered_round` | An int for the doubt cap |
| `issued_client_calls` | the engine (doc 02) | `[{name, args_hash, timestamp}]` for dedupe |
| `force_tool_calls` | warmup | A provider flag |
| `timestamp` | `set` only | Creation time |

**Expiry.**
- The TTL is **10 minutes, absolute from `/conversation/start`** (`89`). `timestamp` is never refreshed by later reads or writes.
- An expired entry is deleted lazily by whichever getter touches it.
- `clear_expired()` is only reachable from `POST /admin/cache/clear`, which is cut.
- `/conversation/end` does **not** evict.
- A restart loses every conversation.

**History trim.** `trim_history_to_max_turns` (`26-83`):
- keeps the leading system prefix
- groups the rest into turns that start at a user message, unless the previous message is a tool result or an assistant message with `tool_calls`
- drops whole turns from the front

The per-turn transient system blocks are recognised by prefix (`conversation_handler.py:110-139`): `You are speaking with `, `User Profile - If user asks`, `Router hint:`, `Respond naturally in plain text`, `RECENTLY SHOWN…` and `<ambient_context>…`.

**Other in-memory state:** `pending_resets` (`core/pending_resets.py`) holds node factory-reset tokens with a 300s TTL. It is used by `admin.py:416,527,587,778`. It is not voice; move it to doc 05.

## 5. Settings

These are read per household unless noted. Definitions are in `services/settings_definitions.py`.

| Key | Default | Effect |
|---|---|---|
| `conversation.max_turns` | 10 | Sliding history window; ≤0 disables it |
| `model.include_thinking` | false | Sets the provider suffix to `/think` or `/no_think`. Synced onto the **shared provider object** every turn (`3047-3068`). |
| `model.advanced_context` | false | Enables agent-context vector retrieval on the blocking path only |
| `memory.enabled` / `memory.recall_enabled` | true / true | Gate the memory tools and memory loading. Fail **open**. |
| `memory.pinned_max_chars` | 500 | Memory text cap. Read **globally**, not per household (`411-412,3601`). |
| `memory.agent_context_enabled` / `_max_results` / `_max_chars` / `_similarity_threshold` | true / 5 / 500 / 0.25 | Agent context retrieval |
| `ambient_context.enabled` | false | Ambient bundle and voice presence Signal |
| `web_search.enabled` | false | `quick_search` and `deep_research` offered; fast-path eligibility. Fail **closed**. |
| `persona.household_prompt` | `DEFAULT_PERSONA` | Personality block and reminder |
| `characterization.injection_enabled` | false | `<person_view>` tail and swap. Fail closed. |
| `voice.wake_verification_mode` | `off` | `off`, `bias` or `enforce`. Unknown values become `off`. Per household and node. |
| `voice.wake_verification_phrase` | `jarvis` | Fuzzy-match target |
| `voice.followup_doubt_max_rounds` | 2 | Doubt wrap-up cap; ≤0 uses the default |
| `tts.url` | (discovery) | TTS base URL override |

Env vars:

| Variable | Default | Effect |
|---|---|---|
| `JARVIS_STREAM_TOOL_RESPONSES` | `false` | Path B on or off |
| `JARVIS_STREAM_TOOL_MIN_CONFIDENCE` | 0.85 | Path B router score threshold |
| `JARVIS_TOOL_CLASSIFIER_MIN_CONFIDENCE` | 0.6 | Router hint `used` threshold |
| `JARVIS_TEST_MODE` | unset | Adapter override; cut |

## 6. Dependencies

**CC subsystems:**
- tool loop, registry, router and executor (doc 02)
- prompt providers and `core_rules` (doc 03)
- memory, characterization, agent context and transcripts (doc 04)
- speaker resolver, stickiness, membership, media proxy and TTS/Whisper clients (doc 06)
- inbox push (doc 13)
- Signals (`record_voice_presence`, doc 10)
- `household_location` (doc 11, `services/phone_number_search.py`)
- `latency_logger` (doc 00)

**Services:**
- **llm-proxy:** `/v1/chat/completions` (live slot `model="live"`, `core/llm_proxy_client.py:54-119`), streaming, `/v1/engine`, `/v1/adapters/date-keys` (a date vocabulary, not LoRA)
- **tts:** `/speak/stream` and `/audio/format`
- **whisper:** via doc 06
- **jarvis-auth:** names

LLM calls per turn, all on the **live slot**:

| Call | Where | Shape |
|---|---|---|
| Warmup | §3.2 | — |
| Fast-path stream | — | no tools, max 512 |
| Tool loop iterations | — | ≤3 text, ≤10 native |
| Text-mode format call | — | max 256, temp 0.7, `/no_think` |
| Continue stream | — | max 512, temp 0.7 |
| Wake greeting | — | max 12, temp 1.1 |

## 7. Invariants and non-obvious behaviour

1. **`messages[0]` must stay byte-stable across turns and across conversations.** This is how llama.cpp keeps its prefix cache warm.
   - Everything per-turn (speaker, ambient, recently-shown, router hint, plain-text override) is a **trailing** system message, stripped and rebuilt each turn (`858`, `110-139`).
   - Hints ride on the user message.
   - Tool pruning was removed for this reason (`843-848`).
   - The characterization swap is a no-op when the prediction was right (`180-210`).
   - Go must reproduce identical bytes, or prod time to first sound regresses by 1–2s.
2. **The warmup tool encoding must equal the inference encoding.** It uses `prompt_provider.build_tools(ToolBuilder.strip_jarvis_extensions(all_tools))` (`539-542`).
3. **The 200 vs 202 split is the contract.**
   - **200 `audio/raw`** means a PCM body and `X-Audio-Sample-Rate`, `X-Audio-Channels` and `X-Audio-Sample-Width` headers, as decimal strings. `X-Assistant-Message` is present but is the **URL-quoted text only on path C**, and `""` elsewhere.
   - **202 `application/json`** means a `VoiceCommandResponse`, or `{"fallback":"use_blocking_continue"}` on continue-stream.
   - The node treats any 2xx other than 200 or 202 as an error, and anything ≥300 as an error (`clients/rest_client.py:154`).
4. **`not_for_me` is terminal on every round trip.** Its wire form is `stop_reason:"not_for_me"`, `assistant_message:""`. The node never speaks and arms a cool-down.
5. **STT noise never reaches the LLM.** All three paths check `is_stt_noise` first. A, B and the noise case all route to C's `not_for_me` (`762`, `1161`, `1580`).
6. **A sentinel in the fast path's first 80 chars aborts streaming.** Think blocks are excluded from the 80-char count (`1405-1440`).
7. **`<exchange_complete/>` is never spoken.** It is stripped by `apply_to_result` on C and continue, and as a backstop by `clean_for_tts` (`core/tts_text.py:115`).
8. **The filler guard** rewrites `_GENERIC_FILLER_RESPONSES` on path C only (`99-107`).
9. **Client-asserted speaker IDs are honoured only if they belong to a household member** (`main.py:1023,1405`). Turn speaker beats warmup speaker. A changed speaker re-resolves name and memories into the **live cached `node_context`**, which also fixes transcript attribution (`3475-3502`).
   > **Changed by D2 / D3 / D21:** the warmup speaker no longer exists (the `/conversation/start` hint is ignored), so the first identified turn sets the speaker for the conversation. An unknown or ambiguous speaker means per-user tools refuse; nothing falls back to the node owner.
10. **Fail directions are deliberate:**
    - memory fails open
    - web search fails closed
    - characterization fails closed
    - persona falls back to the default
    - wake verification fails open in every case (including timeout, which allows ≤1.2s added latency)
11. **The node's PCM reader cuts off after 8s idle between chunks** (`command_execution_service.py:432`). Go must not stall the body longer than that between sentences.
12. **The node defaults to `22050` when the format headers are absent.** CC's fallback is `16000` (`main.py:1444`).

## 8. Oddities

1. **Transcripts are logged only from blocking `/voice/command`** (`main.py:1265`). Wake turns go through `/stream` and are never logged. Passive memory extraction (doc 04) therefore sees only follow-ups and e2e traffic.
2. **Cache TTL and leaks.**
   - The TTL is absolute, so a long follow-up conversation hits 422 or 400 at minute 10 even while active.
   - There is no sweeper, so every wake leaves an entry until the same ID is touched again. In practice that never happens: a new UUID is minted per wake.
   - With a prompt of about 38 KB, this is an unbounded leak until restart.
3. **A fast-path fallback pollutes the cache.** Path A mutates the live cached list (`1248-1350`) before its sentinel or LLM-error bail-out (`1432,1443`). The blocking path then strips the override but keeps the dangling user message and appends a second one. Path B copies the list correctly (`1668`).
4. **Path B double-executes server tools on fallback.** It runs `execute_tool_calls` (`1826`) and can then return `None` (client calls present, or validation), so path C re-runs the whole turn.
5. **Native warmup has no `max_tokens`** (`543-548`). It generates a full completion on the live slot.
6. **Status inconsistencies.**
   - Not-initialized: 422 on blocking, 400 on stream.
   - Precondition errors on stream become 500.
   - Generic errors: blocking returns 200 with an error body; stream returns 500.
   - The continue error body has `stop_reason: null`.
   - Blocking strips `[Tool data:` and returns `reasoning`; the stream 202 does neither.
7. **Speaker IDs on the blocking path.** Blocking `/voice/command` ignores `speaker_user_id`, and the node doesn't send it on follow-ups. Follow-ups therefore always use the warmup or turn-1 speaker.
8. **Continue-stream drops terminal signals.** It neither strips nor reports `end_of_exchange` and has no sentinel check. Inbox actions are pushed synchronously before audio, despite the comment.
9. **Dead code:**
   - `stream_final_response` (`2750`), `_format_tool_result_text_mode_UNUSED` (`2627`) and `_extract_from_tool_results` (`2955`)
   - `prune_tools_by_router_decision` (`voice_command_helpers.py:127`)
   - the unused `server_tool_names` (`826`)
   - the `if False` adapter block and `adapter_hash` plumbing
   - `cleanup_conversation` (only `/test/command`, which is cut)
   - `extract_sentences` imported but unused in path A (`1152`)
   - `ambient_grounding.py` is used only by tests and the offline `tools/ambient_eval.py`
10. **`_sync_include_thinking` mutates the process-wide provider object** (`3066`). Concurrent turns from households with different settings race on `/think` vs `/no_think`.
11. **`latency_logger` traces are keyed by `conversation_id`.** `/conversation/start` (still in flight in a parallel thread) and `/voice/command/stream` can clobber each other's trace. The media proxy already works around this (`api/media.py:110-119`).
12. **The CLAUDE.md is stale.** It claims "node calls `/conversation/start` again and retries" on a cache miss. The node does no such thing: `rest_client` returns None, which becomes an error result. It also calls the fast-path ack "keyword-matched while the pipeline runs"; it is actually gated by a 3s timer. Appendix A's "node uses the stream variants" is also wrong (§2).
13. **`acknowledgment_service` substring matching** means "rain" matches "train" and "how" matches "show". It has no tests.
14. **The kept Qwen3 providers inherit from `Qwen25_7B_Compressed` / `Qwen25MediumUntrained`**, which Appendix B drops. Doc 03 must port those base classes as part of the keepers.

## 9. Tests

Existing unit tests, about 7.1k lines in total:

| Area | Test files |
|---|---|
| Handler | `test_conversation_handler.py` (1053 lines), `test_voice_command_processing.py`, `test_voice_command_precondition_status.py` (status codes), `test_continue_message_field_bypasses_llm.py` |
| Streaming and cache | `test_streaming_handler.py`, `test_history_trim.py`, `test_referenced_items.py`, `test_characterization_injection.py`, `test_latency_spans.py` |
| Wake verification and doubt | `test_followup_doubt.py`, `test_wake_verification.py`, `test_wake_verified_wiring.py`, `test_media_aware_wiring.py` |
| Hints | `test_turn_context.py` (861 lines), `test_direction_hint.py`, `test_affect_hint.py`, `test_profile_match.py` |
| Scrubbing and markers | `test_transcript_filter.py`, `test_tts_text.py`, `test_exchange_complete.py`, `test_not_for_me.py`, `test_think_block_stripper.py` |
| Warmup | `test_warmup_service.py`, `test_web_search_gate.py` |

There are no tests for `/voice/acknowledge` or `/wake-response`.

**Golden fixtures**, as pure functions dumped from Python:
- `is_stt_noise` and every `transcript_filter` shape detector
- `build_direction_hint`, `build_affect_hint`, `build_turn_hint`, `should_double_check_sentinel` and `build_profile_match_hint`, over a grid of inputs
- `contains_sentinel`, `apply_exchange_complete`, `clean_for_tts` and `ThinkBlockStripper` (streaming chunk sequences)
- `trim_history_to_max_turns`
- `_is_transient_system_block`
- `wake_phrase_present` and `wake_phrase_similarity`
- the sentence splitter and `stream_text_as_audio` chunk grouping (2 vs 4)
- `_rewrite_terminal_filler`
- `_format_tool_result_text_mode` prompt bytes
- the full assembled `messages` array per path for representative turns. This is the most valuable fixture: it pins prefix-cache bytes.

**Black-box contract tests** (fake LLM, fake TTS returning known PCM):
- `/conversation/start` response shape
- each `/stream` outcome: 200 headers and body bytes, and the 202 body for tool calls, validation, `not_for_me` and error
- 400 vs 422 preconditions
- continue-stream 202 fallback
- `/acknowledge` text drawn from the expected pool
- `/wake-response` fallback `"Yes?"`
- follow-up via blocking `/voice/command` with `turn_source=follow_up`

Shadow replay (PLAN §4 layer 4) must ignore `X-Audio-*` values if Go emits the real Kokoro rate.

## 10. Questions for the user

1. **[behaviour] Should wake turns write transcripts?** Today only blocking `/voice/command` logs them (§8.1), so passive memory extraction never sees the first utterance of a wake cycle, only follow-ups.
   - *Why it matters:* this decides what memory extraction learns from. Fixing it multiplies the transcript volume, which is privacy-relevant.
   - *Options:* (a) port as-is; (b) log every completed voice turn (stream, continue and blocking) with a consistent record.
   - **My recommendation:** (b). It looks accidental. Gate it on the existing memory-extraction setting.
   - **Decided (D19):** (b). Every completed voice turn writes a transcript, only when the speaker is confidently identified and the household has memory on. With memory off, nothing is logged or extracted.
2. **[behaviour] Conversation lifetime.** Today it is a 10-minute absolute TTL with no sweeper, no eviction on `/conversation/end`, and a leak (§8.2).
   - *Why it matters:* a long, active follow-up conversation dies mid-exchange. Memory also grows unbounded in a process that is now everything.
   - *Options:* (a) keep the absolute 10 minutes; (b) a sliding idle TTL (10 minutes since last touch) plus a periodic sweeper plus eviction on `/conversation/end`; (c) (b) with a hard cap on entry count.
   - **My recommendation:** (c). Is there any reason the absolute expiry was deliberate, for example bounding stale `node_context`?
   - **Decided (D40 default):** (c). Sliding idle TTL, a sweeper, eviction on `/conversation/end`, and an entry cap.
3. **[scope] Port path B (tool-stream)?** It is env-gated off, requires a native provider (prod and dev are both text-path), and double-executes tools on fallback.
   - *Options:* (a) cut it; (b) port it behind a flag and fix the double execution.
   - **My recommendation:** (a). Revisit if prod moves to Qwen3.5-9B native.
   - **Decided (D9):** (a), cut. The fast path A goes too, since it depends on the cut fastText router (F1).
4. **[behaviour] Fix or replicate the known bugs: §8.3 cache pollution, §8.4 double execution, §8.10 the provider `include_thinking` race and §8.6 status inconsistencies?**
   - *Why it matters:* shadow replay and contract tests treat Python as the oracle.
   - *Options:* (a) bug-for-bug; (b) fix the internal bugs (8.3, 8.10) while keeping the wire-visible quirks (8.6) exact.
   - **My recommendation:** (b). Keep the status codes and body shapes byte-identical, because the frozen node depends on them. Record the fixes as deliberate shadow-replay diffs.
   - **Decided (D8):** (b). Fix the internal bugs; keep the wire quirks the frozen node depends on, and document why. §8.4 is moot (path B cut, D9).
5. **[scope] Which prompt path is the Go target: text (Qwen3 14B/8B), native (Qwen3.5-9B, ChatGPT), or both?** Nearly every stage branches on `supports_native_tools`: the server-tool whitelist, the warmup `max_tokens`, the iteration cap, the continue shapes and `server_tool_complete`.
   - **My recommendation:** both, since prod is text and e2e is native. If you plan to move prod to native soon, the text branch could be ported later.
   - **Decided (D22):** both ship, providers ported byte-exact; the native path gets the text path's server-tool gates.
6. **[behaviour] Wake verification in Go.**
   - Today it is a second whisper call on a 2.2s slice, fired after STT returns, with a ≤1.2s wait on the turn.
   - With STT in-process, Go could verify inside the same transcribe call, with no race and no wait.
   - What is prod's `voice.wake_verification_mode` today, and do you still want this feature?
   - **My recommendation:** keep it. Compute the verdict synchronously in the STT module and store it on the conversation, so the 1.2s poll disappears.
   - **Decided (D40 default):** keep it, in-process. Verify prod's `voice.wake_verification_mode` first.
7. **[scope] Affect hint.** It depends on whisper's `affect` (`voice.emotion_enabled`), which the greenfield STT may not produce.
   - *Options:* keep the field and hint (no-op without a producer); port the affect model; drop it.
   - **My recommendation:** keep the request field and the hint code; it is tiny. Defer the producer to Phase 4's "if still wanted".
   - **Decided (D38):** the affect pass is cut (no producer). `affect` stays in the request as `null`; the hint code is kept as a no-op.
8. **[behaviour] Audio format headers.** Kokoro in Go emits its native rate (probably 24 kHz). CC's fallback today is 16000; the node's is 22050.
   - Can every node (aplay on a Pi Zero) take 24 kHz mono s16?
   - **My recommendation:** emit the real engine format from in-process knowledge, with no `/audio/format` round trip. Verify on jarvis-dev.
   - **Decided (D40 default):** emit the real engine sample rate; verify on a Pi.
9. **[behaviour] `X-Assistant-Message` is empty on paths A and B and on continue-stream.** The node then can't run its follow-up self-echo detection (`follow_up_loop.py:306`), or show text, for those turns.
   - Headers must precede the body and Python `requests` can't read trailers, so a fix would need a node change.
   - Is the gap acceptable, or should Go hold path A's headers until the first sentence and send that sentence?
   - **My recommendation:** accept the gap. Python is frozen, and a partial header would mislead the echo check.
   - **Decided (D40 default):** accept the gap. With paths A and B cut (D9), it remains only on continue-stream.
10. **[scope] Native warmup generates a full completion with no `max_tokens`** (§8.5). Is that intended, for example to prime the tool-call template, or an omission?
    - **My recommendation:** add `max_tokens=1` in Go. It frees the live slot sooner. Check on Qwen3.5 that the cached-prefix hit rate is unchanged.
    - **Decided (D40 default):** `max_tokens=1`.
11. **[minor] `/conversation/start` request fields `adapter_settings` and `skip_warmup_inference`.** Accept and ignore them, so old nodes and the eval harness don't get 422s?
    - **My recommendation:** yes, accept and ignore. Drop `JARVIS_TEST_MODE`.
    - **Decided (M4/D47):** overridden. Drop both fields from the schema, and `JARVIS_TEST_MODE`. Old senders still work because the decoder ignores unknown fields (no `DisallowUnknownFields` on this route).
12. **[minor] `/voice/acknowledge`.** Port the keyword pools as they are (substring matching, random choice), or fix the word boundaries ("show" → "how")?
    - **My recommendation:** add word boundaries. Nothing tests the exact pick, and the output is random anyway.
    - **Decided (M5/D47):** add word boundaries.

## 11. Go port notes

**Package shape.** `internal/modules/cc/voice`:
- `routes.go`: the 8 handlers, strict `httpx` DTOs mirroring the Pydantic models, including null-vs-missing.
- `handler.go`: `Process` (path C) and `Continue` / `ContinueStream`, sharing **one** `assembleTurn(ctx, conv, utterance, turnCtx) []Message` function. Golden-test the shared function. There is no `StreamFast`: paths A and B, the router hint and the router decision are cut (D9). `/voice/command/stream` runs `Process` and streams the result via the `stream_text_as_audio` grouping.
- `convcache.go`: the conversation cache.
- `hints/`: direction, affect (a no-op while `affect` is always `null`, D38), turn, profile and doubt hints.
- `textfilter/`: `transcript_filter`, `tts_text`, `not_for_me`, `exchange_complete` and `think`.
- `ack.go` and `wake_response.go`.

**Conversation cache.**
- Use a `map[string]*Conversation` with a per-conversation mutex. That gives serialized turns per ID and fixes the in-place-aliasing hazards.
- Take copy-on-write snapshots of `Messages`; commit with `conv.Commit(msgs)` only on success. That alone fixes §8.3.
- Use a sliding idle TTL (10 minutes since last touch), evict on `/conversation/end`, and add a sweeper goroutine and an entry cap (D40).
- **Speaker is conversation state** (D2/D3): it starts empty, is set by identified turns, and is dropped with the conversation. No per-node map, no stickiness. Ignore `speaker_user_id` / `speaker_confidence` on `/conversation/start`.
- Keep the cache in memory only; restart semantics are unchanged. Persisting to SQLite is not worth it.

**Streaming.**
- Use an `io.Pipe` or channel from the LLM token stream into a sentence splitter, then in-process Kokoro synthesis per sentence (`internal/voice/kokoro`), then `http.Flusher`.
- Set the `X-Audio-*` headers from the engine config; don't fetch them.
- Overlap synthesis of sentence N+1 with writing sentence N. Python serialises this on paths A, B and continue, and prefetches only in `stream_text_as_audio`.
- Watch the node's 8s idle cutoff. If synthesis or the LLM might exceed it, there is no in-band keepalive in raw PCM. A short buffer of silence is possible but audible, so add it only if needed.
- Close the latency trace when the writer finishes (`_end_trace_after_stream` semantics).
- Use a per-request `context.Context` so a node disconnect cancels the LLM stream and TTS. Python leaks these until the generator is garbage-collected.

**In-process replacements.**

| Today | In `jarvisd` |
|---|---|
| auth name lookups | auth module interface |
| `/v1/adapters/date-keys` | one shared Go date-key constant (D40 via 03.Q9) |
| `/v1/engine` | LLM module call |
| TTS HTTP | Kokoro in-binary |
| whisper verification task | STT module, synchronous verdict (Q6) |
| inbox push | notifications module call. Make it a real goroutine on continue-stream so audio isn't delayed. |

**LLM slot discipline.** Warmup, the turn and the continue all hit the live `llama-server` slot. With the embedded queue capping background LLM jobs at one, voice keeps priority. Make sure warmup can't starve a turn: it is cancellable when a turn for the same conversation arrives.

**Provider state.** Pass `includeThinking` as a per-call argument rather than mutating a shared provider (§8.10).

**Transcripts (D19).** After every completed turn on any of the four voice routes, write one transcript row when the speaker is confidently identified and the household's `memory.enabled` is on. Write it after the response is committed, off the audio path.

**Request DTOs (D47).** `/conversation/start` has no `adapter_settings` / `skip_warmup_inference`; do not enable `DisallowUnknownFields` on it.

**Acknowledge (M5).** Match keywords on word boundaries.

**Risks.**
- **Byte-exact prompt assembly is the top risk.** Any drift in hint strings, block ordering or whitespace silently costs prefix-cache hits. Shadow replay on the assembled `messages` is mandatory.
- **FastAPI response semantics.** `response_model` serialisation emits every optional field as `null` (`end_of_exchange: null` is handled by the node, `tool_calling_response.py:85-96`). The 422 body shape uses the custom handler at `main.py:111`.
- **The single-process blast radius**, now that the cache lives in the same process as everything else. Recover per request.

## 12. Trace spans (as built, 2026-10-07)

Request traces (`cc_request_traces.spans_json`, read by the admin `/api/traces/{id}`) carry per-step spans named as legacy's `latency_logger` names them, so a legacy and a jarvisd trace of the same turn line up. The span shape is unchanged: `{name, service, start_ms, end_ms, duration_ms, status, metadata}`, sorted by start. Code: `reqTrace` in `internal/modules/cc/traces.go`; it rides the request context (`withTrace` / `traceFrom`), and a nil trace records nothing.

| Route (`request_type`) | Spans (nesting by indent) |
|---|---|
| `/conversation/start` (`warmup`) | `auth_complete` (checkpoint); `warmup_conversation_with_tools` > `warmup_inference` (llm_proxy, jarvisd-only) |
| `/media/whisper/transcribe` (`stt`) | `stt_transcribe` (whisper; `audio_bytes`, `speaker_audio_bytes`). The speaker pass runs inside it, as in legacy's single whisper-api call. |
| `/voice/command` (`voice_command`) | `auth_complete`; `cache_get_tools`; `process_voice_command_with_tools` > [turn]; `build_response` |
| `/voice/command/stream` (`voice_command_stream`) | `auth_complete`; `process_voice_command_with_tools` > [turn]; on 200 also `audio_stream` > per spoken piece `tts_first_chunk` and `tts_stream_total` (tts; `text_chars`, `audio_bytes`), plus the `first_audio_byte` checkpoint |
| `/voice/command/continue` (`voice_command_continue`) | `auth_complete`; `continue_conversation` > [loop or `final_response_generation`]; `inbox_actions_push` (legacy didn't trace this route) |
| `/voice/command/continue/stream` (`voice_command_continue`) | `auth_complete`; `continue_stream_dispatch`; `audio_stream` > `llm_stream_first_token`, `llm_stream_total` (llm_proxy; `chars`), TTS spans as above |
| mobile chat (`mobile_chat`) | `warmup`; `process_command` > [turn]; `mqtt_tool_<name>` (node); `continue_conversation` |

[turn] is `cache_lookups`; `speaker_resolve` (voice only, jarvisd-only); `agent_context` (only when advanced context is on); `tool_execution_loop` > per iteration `llm_call_iter_N` (llm_proxy; `prompt_tokens`, `completion_tokens`, `finish_reason`) and, when it ran tools, `tool_exec_iter_N` (`tools`: every call's name) > `server_tool_<name>` (jarvisd-only); then `final_response_generation` (llm_proxy) when the text path formats server-tool results.

Model time is the sum of the llm_proxy, tts and whisper spans; server overhead is `total_duration_ms` minus that. First audio is the `first_audio_byte` checkpoint.

No jarvisd equivalent: `tool_filtering` and `tool_routing` (keyword filter and router cut, D9); the fast-path `llm_stream_*` spans of `/voice/command/stream` (paths A/B cut, D9; the continue stream keeps them); `inbox_actions_push` on the continue stream (a background goroutine here, so it isn't on the request's critical path). The blocking LLM calls have no time-to-first-token (they don't stream).
