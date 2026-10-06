# llm 04: prompt and response shaping

Everything the proxy does to a request on its way to the model, or to a response on its way back. Here "proxy" means the API on 7704 plus the model service on 7705. The OpenAI-compatible translation to llama-server itself is in [01](01-backends-and-engines.md), and the wire envelope is in [02](02-api-and-streaming.md).

Source: `/home/alex/jarvis/jarvis-llm-proxy-api`, at HEAD `e04d0b1`, which is what prod runs.

**Decisions that apply here:**

- **D9 / D40 (03.Q7–Q9):** date-key extraction is regex only. The fastText fallback and the `[DATE_HINT]` message are cut. Extraction runs once per turn, on the raw transcript.
- **D22:** prompts are byte-exact. CC owns the prompt text, so the llm module must not reshape it.
- **D8:** bugs are fixed by default, and each fix is logged here as an intended difference.

## 1. Purpose

The proxy is mostly a pass-through. It has five places where it changes content:

1. **Date-key extraction.** This is a Jarvis extension: `include_date_context` produces `date_keys`. It is the only one CC depends on semantically.
2. **JSON mode.** `response_format.type=json_object` does three things: injects a system instruction, repairs the output after the fact, validates it against the schema, and retries once with a correction turn.
3. **Thinking control.** `reasoning_budget` becomes `chat_template_kwargs.enable_thinking` on the REST path. On the in-process GGUF path (cut), it became an empty `<think></think>` prefill instead.
4. **Message flattening.** Text parts are joined with a space and images are dropped. This happens on every REST call that carries no image (see 01 §3.3).
5. **Sampling defaults.** The `temperature or 0.7` default turns an explicit `temperature: 0` into 0.7. **This is a bug.**

## 2. Entry points

| Mutation | Where (non-stream) | Where (stream) | Where (queue job) |
|---|---|---|---|
| Date keys | `services/model_service.py:389-408` | `model_service.py:566-578`. Computed, then **thrown away** (§8.2) | Never: `queues/tasks.py:118-129` doesn't pass `include_date_context` |
| JSON mode | `services/chat_runner.py:571-582,692-726` | **Not applied**: the stream builds its own `GenerationParams` without `response_format` (`model_service.py:595-613`) | Applied: the worker forwards `response_format` (`tasks.py:115,126`) |
| Thinking | `backends/rest_backend.py:355-375`, called from `:321` (tools) and `:405` (text) | **Not applied**: `generate_text_chat_stream` never calls `_apply_reasoning` (`rest_backend.py:529-544`) | Applied, through the same non-stream path |
| Flattening | `rest_backend.py:646-655` | `rest_backend.py:524-527` | same as non-stream |
| Temperature default | `chat_runner.py:598` | `model_service.py:596` | `tasks.py:121` (0.7 when absent), then `chat_runner.py:598` again |

## 3. Behaviour

### 3.1 Date keys (`include_date_context`)

The non-stream flow is in `model_service.py:389-422`:

1. **`_get_user_text`** (`:366-380`) picks the text to scan. It takes the **last `role=user` message**. If its content is a string, that string is the text. If it is a list of parts, the **first text part** is used. CC's text includes its appended hints, agent context and `/no_think` (docs/cc/03 §3.5, oddity 9).
2. **Empty text means null.** If that text is empty, `date_keys` stays `None`, even when `include_date_context` was true, so the response carries `date_keys: null` rather than `[]` (§8.4).
3. **Regex extraction.** `extract_date_keys_regex(text)` is `services/date_key_matcher.py:357-420`.
4. **fastText fallback (dead).** If the regex returns `[]`, the code tries `extract_date_keys_fast` (`services/date_keys.py:605-643`). When that returns keys, it appends a `role=system` `[DATE_HINT] …` message to the request before generation (`date_keys.py:646-672`; `model_service.py:406-408`).
   - **This branch is dead in both prod and dev.** The model file `models/date_keys_fasttext.bin` (`date_keys.py:166-173`) is absent from the prod container and from the MBP checkout (checked 2026-10-06), so `predict` returns no keys (`:248-249`) and nothing is appended. Prod logs show 0 fastText or `DATE_HINT` lines in 72 h.
   - **Cut (D9).** No behaviour is lost.
5. **Response.** The response carries `date_keys` only when it is not `None` (`model_service.py:416-417`), and the gateway copies it through (`api/chat_routes.py:370,378`).

**The matcher** (`date_key_matcher.py`) works like this:

- **Lowercase and strip** the input (`:372`).
- **Collect negative spans** from 23 negative regexes (`:274-308`, `:385-388`).
  - A candidate match is rejected if it lies inside a negative span, or if at least 50% of it overlaps one (`:390-401`).
- **51 dynamic regexes first** (`:142-271`), in list order. These produce `in_N_minutes`, `in_N_days`, `at_Xam`, `at_X_YYpm`, `noon`, `midnight` and `day_after_tomorrow`.
- **Then 288 static surface forms** (`:315-350`). Each is wrapped in `(?<!\w)…(?!\w)`. They come from six dicts flattened in insertion order, then **stable-sorted by surface-form length, descending**. A match is taken only if it doesn't overlap an earlier match, and each key is emitted once.
- **Output** is the keys, `sorted()` (`:420`).
- **Corpus result:** 4,987/4,987 of `data/jarvis_training.jsonl` match exactly (re-run 2026-10-06).

**Known false positives.** These were verified by running the matcher, and they are encoded in the corpus as intended:

| Input | Keys returned |
|---|---|
| "call Tom" | `["tomorrow"]` |
| "the sun is out" | `["this_sunday"]` |
| "I sat on it" | `["this_saturday"]` |
| "Christmas eve" | `["evening"]` |
| "we wed in june" | `["this_wednesday"]` |

Removing the ambiguous abbreviations (`tom`, `sun`, `sat`, `wed`, `eve`, `aft`, `mon`, `fri`, `morn`, `thu`, `tue`) breaks **100 corpus rows**, such as "Can we talk tom?" and "last wed aft". That makes it a product trade-off, not a bug. **Port as-is**; tuning comes after the port.

**The vocabulary** is `GET /v1/adapters/date-keys` (`api/adapter_routes.py:15-29`, `date_keys.py:33-151`):

- 64 static keys: 5 relative days, 12 combined, 8 modifiers, 6 meals, 21 weekday keys and 12 periods.
- Two dynamic patterns, a `patterns` doc map, a `notes` map and `adapter_trained`.
  - `adapter_trained` is `Path("adapters/jarvis/adapter_config.json").exists()`, which is always false in prod.

**Vocabulary drift.** The matcher never emits `at_noon` or `at_midnight`, but they are in the vocabulary. CC's resolver can't resolve 3 vocabulary keys (docs/cc/03 §8.7). D40 Q9 fixes this with one shared Go constant.

### 3.2 JSON mode (`response_format`)

`run_chat_completion` (`chat_runner.py:542-752`) handles it as follows.

1. **When it applies.** `response_format.type == "json_object"` sets `requires_json` (`:571-575`). Any other type, including CC's `{"type":"text"}` (shape A, docs/cc/02 §3.7), is a no-op.
2. **System instruction injection** (`inject_json_system_message`, `:129-155`). The text injected is `JSON_SYSTEM_MESSAGE` (`:29-32`).
   - **With no system message,** the instruction is prepended as a new system message.
   - **With system messages,** *every* system message is rewritten. Its text parts are concatenated, each followed by `"\n"`. Then:
     - If the result lacks "valid json" (case-insensitive) or "JSON", the code appends `strip() + "\n\n" + JSON_SYSTEM_MESSAGE`.
     - **Otherwise the text is kept, with the trailing `"\n"` it gained** (§8.6).
3. **Schema.** It is taken from `response_format.json_schema`, if present (`:580-582`).
   - On the **chat** path, `json_object` without a schema is allowed. OCR's LLM vision relies on this (`internal/modules/ocr/engines.go:404`).
   - On the **queue** path, a schema is required (`api/queue_routes.py:83-97`).
4. **Generation.** `response_format` is **not forwarded to llama-server**. `RestClient` never puts it in the payload (`rest_backend.py:276-283,306-320,394-405`), so there is no grammar or schema constraint upstream.
   - Only the dead in-process GGUF backend passed it to llama-cpp-python (`gguf_backend.py:797-798`).
   - `services/json_grammar.py` (GBNF) is imported only by tests: **dead.**
5. **Native tool calls skip everything below** (`:683-690`).
6. **Repair** (`parse_json_response`, `:245-316`). This is a fixed cascade. Each step returns the first candidate that parses:
   - **Direct parse.** If the input parses, it is re-serialised through the duplicate-key folder (duplicate keys become a list). The result is `json.dumps(…, ensure_ascii=False)`, the **Python separators `", "` and `": "`**, so even valid input is re-serialised whenever duplicates exist.
   - **Unescaped quotes.** On "Expecting … delimiter/property name": `repair_unescaped_quotes`.
   - **Truncation.** On "Unterminated string": `repair_truncated_json`. This cuts at the last `}`/`]`, strips trailing commas and appends the missing closers.
   - **Duplicate keys,** again.
   - **Extract and repair.** `extract_json_from_text` takes the first `{…}` or `[…]` regex match that parses, and the duplicate-key, quote and truncation repairs are retried on the extract.
   - **Whole-text repairs.** Quote repair, then truncation repair on the whole text.
   - **Failure** returns the original content with `valid=False`.
7. **Schema validation** (`validate_json_schema`, `:337-374`). This is a minimal subset: `type` (including lists of types), `required`, `properties` and `items`. It returns the **first** error string, such as `"$.foo is required"` or `"$.a[0] expected type string, got int"`. If repaired JSON fails the schema, `final_content` stays **unrepaired** (`:702-705`).
8. **One retry** (`fix_json_with_retry`, `:430-539`). This happens when the output is invalid or fails the schema.
   - **Correction turn.** The code appends a `user` turn whose text is byte-fixed (`:451-462`). It includes:
     - an optional truncation note (decided by `is_json_truncated`, `:158-174`)
     - a schema summary: `"Required keys: a, b\nAllowed keys: a:string, b:any"` (`:377-389`)
     - an 800-character preview of the bad output
   - **Retry parameters:**
     - temperature `max(0.3, t-0.2)`
     - `max_tokens` = `clamp(max(1.5×orig, est+max(2000, est/2), 8192), …, 16384)`, where `est = len(bad)//3` and `orig` defaults to 4096
     - the same `reasoning_budget` (`:439-483`)
   - **Retry result.** It is parsed, repaired and validated the same way. If it still fails, the response is **500 `invalid_response_error`**: `"Model returned invalid JSON response. <schema_error>"` (`:722-726`).

### 3.3 Thinking control (`reasoning_budget`)

**Precedence.**

- **Request value.** `ChatCompletionRequest.reasoning_budget` (`models/api_models.py:98-100`) wins when it is not null.
- **Slot default.** Otherwise `resolve_slot_reasoning_budget(model)` (`services/settings_helpers.py:132-153`) reads, in order:
  1. `model.<slot>.reasoning_budget`
  2. `model.main.reasoning_budget`, an undefined key with env `JARVIS_REST_REASONING_BUDGET`
  3. `None`
- **Backend default.** `RestClient` resolves its own slot default at construction (`rest_backend.py:135-143`), keyed on the slot the instance was **built** for.

**REST mapping** (`_apply_reasoning`, `rest_backend.py:355-375`):

- `rb is None`: the payload is untouched, so the server's launch flags decide.
- Otherwise the code sets `chat_template_kwargs = {"enable_thinking": rb != 0}` **and** `reasoning_budget = rb`.
- **A cap is not a cap.** A value N>0 means "thinking on, uncapped". The comment at `:361-369` says Qwen3.5 on llama-server ignores both `--reasoning-budget 0` and the request field; only the template kwarg works.

**Prod launch flags** (read 2026-10-06):

| Server | Flags | Thinking default |
|---|---|---|
| live `llama-server` | `--chat-template-kwargs {"enable_thinking": false} --reasoning-budget 0` | off |
| `llama-server-bg` | no thinking flags | on |

Prod settings: `model.live.reasoning_budget=0` and `model.background.reasoning_budget=-1`. A live request is therefore thinking-off three ways over.

**Background jobs.**

CC paths below are in `jarvis-command-center/app/services/`.

- **Sending `reasoning_budget: 0`** (thinking off):
  - memory extraction (`memory_extraction_service.py:229`). This corrects docs/cc/04 §3.3, which says it sends none.
  - the situation matcher (`situation_matcher_service.py:200`)
  - signal automations (`signal_automation_executor.py:142`)
  - phone drafting (`phone_call_service.py:783`)
  - the errand executor (`errand_executor.py:134`, live slot)
  - the proposal matcher (`proposal_matcher.py:41`)
- **Sending nothing:** these **think unrestricted** on the bg server, which has a 131,072-token context and `-np 1`.
  - deep-research summarisation (`deep_research_service.py:310`)
  - characterization (`characterization_synthesis_service.py:149,311`)
  - the errand planner (`errand_planner.py:403`). It sets max_tokens 6000 and reads `reasoning_content` for diagnostics.
  - recipes' grocery matcher (`jarvis-recipes-server/jarvis_recipes/app/services/llm_client.py:862`)

**The no-think prefill** (`gguf_backend.py:675-754`) applies only to the in-process GGUF backend, **which is cut**. It is a format-agnostic assistant prefill `"<think>\n\n</think>\n\n"`, triggered by `reasoning_budget == 0` **or** by any user message containing `/no_think`. The MBP dev box runs this backend today (Qwen3-8B, in-process Metal). In Go, the mechanism is the template kwarg on every engine:

- jarvisd sends `chat_template_kwargs.enable_thinking=false` when the effective budget is 0, which is what REST does today.
- **Decided: no prefill and no `/no_think` sniffing in jarvisd.** The Qwen3 template already honours `/no_think` in the user turn natively. Qwen3.5 ignores it, and is covered by the kwarg.

**Streams never get thinking control** (§8.1). On the MBP the contract suite saw stream deltas starting with `<think>` despite `reasoning_budget: 0` (docs/contract/README.md, "Also seen, not frozen"). In prod the live server's launch flags hide this.

### 3.4 Message flattening and the content passed through

On the REST text path, each `NormalizedMessage` becomes `{"role", "content": " ".join(text_parts)}` (`rest_backend.py:646-655`; stream `:524-527`). This has four consequences:

- **Native tool turns break.** `tool_calls` on assistant messages and `tool_call_id` on tool messages are **dropped**: only role and text survive. CC's native path (shape B) re-sends history containing tool turns. llama-server sees a `role=tool` message with no `tool_call_id` and an assistant turn without its call (§8.3). Prod's 500s on 2026-10-04, "System message must be at the beginning" from the Jinja template, show that the proxy forwards message order unvalidated.
- **Text parts joined with a space,** not a newline.
- **Images dropped on the text path.** They are only reachable through `generate_vision_chat` (`chat_runner.py:620-638`). That path is taken when any message has an image, and it keeps structured content (`rest_backend.py:752-778`).
- **Extra keys dropped.** Extra message keys are accepted (`extra="allow"`, `api_models.py:8-9`) but dropped.

### 3.5 Sampling defaults

**Request-to-engine translation:**

| Field | Default | Effect on the payload |
|---|---|---|
| `temperature` | `req.temperature or 0.7` (`chat_runner.py:598`, `model_service.py:596`, vision `rest_backend.py:786`) | **0 becomes 0.7** (§8.5) |
| `top_p`, `seed` | none | Forwarded only when set |
| `max_tokens` | none | Forwarded only when set. Absent means llama-server's own limit, which is unbounded up to the context |

Settings `inference.general.{max_tokens,top_p,top_k,repeat_penalty}` are read only by cut backends (GGUF in-process, vLLM, transformers). They are inert on REST.

**Prod.** llama-server's own defaults apply: temperature 1.0, top_p 0.95, top_k 20 (`/props`). They are used only when the proxy sends no value, and it always sends `temperature`.

### 3.6 Response post-processing

- **Text path:** `content.strip()` (`rest_backend.py:451`). On the tools path and the stream path, content is not stripped.
- **`<think>` is never stripped by the proxy.** CC strips it (docs/cc/02 §3.4).
- **`reasoning_content` is dropped.** If llama-server returns it (when `--reasoning-format` separates it), the proxy ignores it. Prod runs `reasoning_format: none`, so think text arrives inline in `content`.
- **Missing `finish_reason`** defaults to `"stop"`, or to `"tool_calls"` when tool calls are present (`services/response_helpers.py:36-37`). The REST text path hard-codes `"stop"` (`rest_backend.py:675`), so **`length` is never reported on the non-stream text path** (§8.7).

## 4. Data

- **No tables.** The matcher's pattern tables and the vocabulary are code constants.
- **The corpus** is `data/jarvis_training.jsonl` (4,987 rows of `{text, date_keys}`).

## 5. Settings

| Key | Read by | Effect | Go |
|---|---|---|---|
| `model.live.reasoning_budget`, `model.background.reasoning_budget` | `settings_helpers.py:145`, `rest_backend.py:135` | Slot thinking default | **Keep**, as `llm.<slot>.reasoning_budget` (05 §5) |
| `model.main.reasoning_budget` (undefined) / `JARVIS_REST_REASONING_BUDGET` | `settings_helpers.py:147-149` | Fallback | **Drop** |
| `date_keys.disable_llm`, `date_keys.device_map` | `date_keys.py` (LLM extractor, never called on any route) | none | **Drop** |
| `debug.dump_gbnf_path` | nothing | none | **Drop** |
| `inference.general.*` | cut backends only | none on REST | **Drop** (05 §5) |

## 6. Dependencies

- **CC** (docs/cc/02 §3.7) relies on four things:
  - `date_keys` from shapes A, B, C and G
  - repair for `json_object` jobs
  - `reasoning_budget: 0` on shape K
  - `include_date_context` sent on every call
- **OCR** (`internal/modules/ocr/engines.go:404`) sends `json_object` with no schema, on `model: "background"`, with an image.
- **llama-server** is relied on for `chat_template_kwargs` support. That needs `--jinja`, which prod's launch flags include.

## 7. Invariants (preserve)

1. **Extraction is a pure function of text.** Go reproduces the output for every corpus row **and** for the false-positive table above. This is golden G6.
2. **Matcher ordering is exact.** Dynamic regexes run in list order. Static forms run in a **stable** length-descending order over insertion order, with overlap and negative-span rejection, and sorted unique output. RE2 has no look-around:
   - Python's `(?<!\w)`/`(?!\w)` wrappers become explicit boundary checks.
   - The three look-around negatives (`date_key_matcher.py:277,279,290`) need hand code or `dlclark/regexp2` (pure Go, keeps `CGO_ENABLED=0`).
   - **Decided:** use `regexp2` for those three only, and test them against G6.
   - `\w` and `.lower()` are Unicode in Python. Go must match that for non-ASCII input. G6 includes non-ASCII rows.
3. **The JSON repair cascade and the correction-turn text are byte-exact** (G7). CC parses the repaired string.
4. **`reasoning_budget` semantics:** 0 means off, -1 unrestricted, and N on.
   - The mapping to `chat_template_kwargs.enable_thinking` is the only mechanism that works on Qwen3.5.
   - Field null means "slot default". Slot default blank means "send nothing".
5. **Prompts are not touched.** The proxy never edits system or user text, with two exceptions: the JSON instruction (§3.2) and, today, the dead `[DATE_HINT]`. D22 byte-exactness of CC prompts depends on this.

## 8. Oddities and bugs

1. **BUG: the stream path ignores `reasoning_budget`, `response_format`, `tools` and images.**
   - `model_service.py:595-613` builds `GenerationParams` with none of them.
   - `rest_backend.py:529-544` never calls `_apply_reasoning`.
   - Images are not checked on the stream path (contract `TestLLMImageToTextModel/stream`).
   - **Go:** apply thinking, tools and the image check identically on both paths (D8). Tool calls already merge correctly in the REST stream parser (`rest_backend.py:607-608`, `_merge_tool_call_deltas` `:19-45`); only the plumbing is missing.
2. **Stream `date_keys` are computed and discarded.** The fastText branch can even mutate the stream request. **Go:** the stream final frame keeps no `date_keys`, per the frozen contract, and in-process CC calls `dates.Extract` itself (D40).
3. **BUG: native tool history is flattened away** (§3.4). This affects only native-path providers (Qwen3.5-9B, ChatGPT e2e). **Go:** forward `tool_calls`/`tool_call_id` and structured content verbatim to OpenAI-compatible engines.
4. **`include_date_context:true` with an empty last user text returns `date_keys: null`, not `[]`.** **Go:** return `[]` whenever the flag is true. The contract test covers only non-empty text.
5. **BUG: `temperature: 0` is sent as 0.7 everywhere.** This includes CC's refinement (D), date fallback (E), warmup (H), signal automations and the contract tests. **Go:** honour 0 (D8). Absent means the 0.7 default is kept.
6. **`inject_json_system_message` adds a trailing `"\n"`** to system messages it does not augment, and augments *every* system message. It is ported byte-exact because it feeds the KV prefix cache. Whether the trailing newline is a bug is moot: CC's JSON jobs have one system message.
7. **The REST non-stream text path always reports `finish_reason: "stop"`**, even when the server said `length`. **Go:** pass the engine's `finish_reason` through (D8). CC's double-check pass reads it (docs/cc/02 §3.3).
8. **Dead code here:**
   - `services/json_repair_service.py` (699 lines, only imported by the test-only `message_service.py`)
   - `services/json_grammar.py`
   - `date_keys.py` `LLMDateKeyExtractor`/`HybridDateKeyExtractor` (`:285-577`, no route calls them)
   - `services/date_key_adapter.py` (LoRA)
   - `scripts/*fasttext*`

## 9. Tests and golden fixtures

**Existing tests:**

| Area | Tests |
|---|---|
| Date-key matcher | `tests/test_date_key_matcher.py` |
| Thinking | `test_no_think_prefill.py` (cut path), `test_slot_budget_resolution.py` |
| JSON | `test_json_grammar.py` (dead) |
| Native tools | `test_native_tool_calling.py`, `test_rest_backend_tools.py` |
| REST streaming | `test_rest_backend_streaming.py` |

**Golden fixtures to export.** The exporters go in `tools/golden/`, because the Python repos are frozen.

- **G6, date keys.** `extract_date_keys` over the 4,987-row corpus, plus an edge file with these cases:
  - the false positives above
  - text with negatives (`set a timer for 10 minutes`, `play Yesterday`, `night shift`)
  - non-ASCII text and multi-line CC-style text with `/no_think`
  - empty and whitespace-only input
  
  The vocabulary response `GET /v1/adapters/date-keys` goes in as `vocabulary.json`, minus `adapter_trained`, which becomes the constant `false`.
- **G7, JSON shaping.** These cover `chat_runner.py`:
  - `parse_json_response` over a malformed-JSON corpus: truncated, unescaped quotes, duplicate keys, prose-wrapped, fenced, arrays, and non-ASCII
  - `inject_json_system_message` for 0, 1 and 2 system messages, with and without the word JSON
  - `summarize_json_schema`
  - `validate_json_schema` error strings
  - `is_json_truncated`
  - the full correction-turn text and retry params from `fix_json_with_retry` (with the backend mocked to capture its input)
- **G8, request translation** (owned by 01, listed here). The `RestClient` payload for each CC shape A–K and for OCR is captured through a mock transport. It covers reasoning precedence (request, slot, unset) × backend slot.

## 10. Questions for the user

None from this doc. Technical calls are made in §3.3 (no prefill), §7.2 (regexp2) and §8 (D8 fixes). The matcher's false positives are a corpus-encoded behaviour: ported as-is, and listed for post-port tuning.

## 11. Go port notes

**Package layout.**

- **`internal/modules/llm/dates`:**
  - `Extract(text string) []string`, a pure function
  - `Vocabulary`, one shared slice, which also feeds CC's DT_KEYS and resolver (D40 Q9)
  
  CC calls `Extract` in-process once per turn on the raw transcript. The HTTP `include_date_context` extension stays on the external `/v1/chat/completions` only. External callers such as recipes and the contract suite still send it.
- **`internal/modules/llm/jsonmode`:** `Inject`, `Repair`, `Validate`, `CorrectionTurn`, each byte-exact (G7). Python `json.dumps` separators and `ensure_ascii=False` re-serialisation need a small custom encoder. Go's `encoding/json` uses `","` and `":"` and escapes `<>&`. Write `pyjson.Dumps` once and reuse it (CC's G5 needs it too).

**Thinking.**

- One function: `effectiveBudget(req, slot)` returns `*int`, then `applyThinking(payload)`.
- It is called on **every** path: non-stream, stream, tools, vision and queue.

**Grammar.** **Not** forwarding `response_format` to llama-server keeps parity. Grammar-constrained decoding interacts badly with inline `<think>` on the bg server. It is post-port work, and the jarvisd engine driver should leave a seam for it.
