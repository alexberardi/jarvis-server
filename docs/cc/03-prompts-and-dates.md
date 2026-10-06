# 03: Prompts and dates

Scope:

- the four kept prompt providers (Appendix B) and their **dropped parent classes**, which they inherit from;
- `shared/` (`core_rules`, `context_builders`, `tool_formatters`, `command_converters`);
- the factory and the interface;
- the final system-prompt assembly in `ConversationHandler._get_system_prompt`;
- the date pipeline (llm-proxy `date_keys` → CC resolution → tool args), `general_context`, and `/generate/date-context`;
- personas;
- the prompt-provider install routes, which are being cut.

All paths are relative to `jarvis-command-center/app/` unless prefixed. Every behaviour claim below was checked against code. The rendered strings in section 3.3 come from running the real providers on 2026-10-06.

---

## 1. Purpose

A **prompt provider** turns three things into one system-prompt string:

- the warmed conversation's context (room, persona, devices, date keys);
- its tool list;
- its command flags.

It also owns the model-family quirks:

- the user-turn suffix (`/no_think`);
- the response format (`{"type":"text"}`);
- `parse_response` (`<tool_call>` → Jarvis JSON);
- `sanitize_text` (strip `<think>`);
- whether tools are sent natively.

**Who uses it:** the voice and chat paths, through `ConversationHandler` (01/02). The provider is chosen by the `llm.interface` setting. Prod uses `Qwen3_14B_Compressed`, which actually serves Qwen3.8-27B through llama-server. Dev uses `Qwen3_8B_Compressed`. The install-e2e behaviour suite uses `ChatGPTOpenAI`.

**Date handling** lets the model emit symbolic keys (`"tomorrow"`, `"this_weekend"`) instead of ISO timestamps. The server then rewrites any `format: date-time` tool parameter into concrete UTC instants in the user's timezone.

---

## 2. Entry points

### Routes

| Method | Path | Auth | Live caller | Notes |
|---|---|---|---|---|
| GET | `/api/v0/generate/date-context?timezone=` | node `X-API-Key` (`date_context.py:17-20`) | node-setup `JarvisCommandCenterClient.get_date_context` (`jarvis-node-setup/clients/jarvis_command_center_client.py:21-35`), on **every** conversation start (`:604-605`) | Returns the raw `generate_date_context_object` dict. The node parses it into a strict pydantic `DateContext`: `timezone.user_timezone: str` and `is_dst: bool` are required. A parse failure is logged and treated as `None`. **Vestigial:** the SDK accepts `date_context` in `to_openai_tool_schema` and `get_command_schema` but never reads it (`jarvis-command-sdk/jarvis_command_sdk/command.py:825,891`). |
| POST | `/api/v0/prompt-providers/install` | `verify_provisioning_auth`: `ADMIN_API_KEY` or any user JWT (`provisioning.py:84-99`) | node-mobile `requestCCInstall` (`jarvis-node-mobile/src/api/packageInstallApi.ts:90-101`), from `StoreDetailScreen` when a Pantry package has a `prompt_provider` component (`StoreDetailScreen.tsx:123,270-287`) | Body `{github_repo_url, git_tag}` → 201 `{id, status:"pending", created_at}` (`api/package_install.py:714-739`). Runs a background clone, then `exec()`s the provider in-process. **Feature is dropped** (PLAN §7). |
| GET | `/api/v0/prompt-providers/install/{request_id}` | same | node-mobile `pollCCInstallStatus` (`packageInstallApi.ts:104-112`), `InstallProgressScreen` in `cc-provider` mode, polling every 750 ms and giving up after 5 consecutive failures (`InstallProgressScreen.tsx:32-33,71,79-87`) | Response `{status: pending|completed|failed|expired, request_id, package_name?, provider_name?, error_message?, details?}` (`package_install.py:670-676,742-799`). |
| GET | `/api/v0/prompt-providers` | same | **none** (Appendix A, cut) | |
| DELETE | `/api/v0/prompt-providers/{name}` | same | **none** (cut) | |
| GET | `/api/v0/mobile/household/{hh}/persona/presets` | user JWT plus household read role | mobile Household Settings (`api/mobile_household_settings.py:179-197`) | Returns `{presets, default_preset_id, default_text, max_chars}`. The route belongs to 13. Its data lives here (`services/persona_presets.py`). |

### Internal callers

| Function | Caller |
|---|---|
| `PromptProviderFactory.create_provider` | `ModelService.__init__` (`core/model_service.py:44-65`). **`ModelService` is constructed per request** (`deps.py:228-247`), so the factory walks both package roots and instantiates every provider class on every request (`prompt_provider_factory.py:80-119`). |
| `ConversationHandler._get_system_prompt` | Warmup only (`core/conversation_handler.py:491-494`). The result is cached as `messages[0]` and never rebuilt per turn, except for the characterization tail swap (`:181-208`). |
| `generate_date_context_object` | `tool_execution_engine` (ISO fix and date injection), `date_detector` (legacy model only), `tools/resolve_relative_date_tool` (disabled tool), `services/errand_executor.py:238`, and the route above. |
| `LLMProxyClient.get_date_keys` | Warmup (`conversation_handler.py:483-489`). It calls llm-proxy `GET /v1/adapters/date-keys` (`core/llm_proxy_client.py:315-325`). |

There are no MQTT topics and no background loops in this subsystem.

---

## 3. Behaviour

### 3.1 Provider selection

1. `llm.interface` is read from settings. On a read failure the factory uses the hard-coded `"JarvisToolModel"` (`prompt_provider_factory.py:178-203`). The settings default is **`Qwen25MediumUntrained`**, a dropped provider (`services/settings_definitions.py:16-22`).
2. `pkgutil.walk_packages` scans `core/prompt_providers/` and then `core/prompt_providers_custom/` (`:26-29`).
   - Every `IJarvisPromptProvider` subclass is instantiated.
   - The first one whose `name` matches case-insensitively wins (`:109-115`).
   - Import errors are swallowed at debug level (`:105-107`).
3. If no provider is found, `ModelService` falls back to the legacy `ModelFactory` model. The handler then uses `model._build_system_prompt`, or finally the literal `"You are a helpful voice assistant."` (`conversation_handler.py:3293-3302`).
4. `requires_reload=True` on `llm.interface` is moot: per-request construction means a change applies to the **next conversation warmup** without a restart.

### 3.2 The kept providers and their inheritance chain

The kept files are thin. Most behaviour comes from parent classes that Appendix B drops. **Port the whole chain's behaviour, not just the four files.**

```
IJarvisPromptProvider                        (interfaces/ijarvis_prompt_provider.py)
├── ChatGPTOpenAI                            large/untrained/chatgpt_openai.py           [e2e]
└── Qwen25MediumUntrained                    medium/untrained/qwen25_medium_untrained.py [dropped file]
    └── Qwen25_7B_Compressed                 medium/untrained/qwen25_7b_compressed.py    [dropped file]
        ├── Qwen3LargeUntrained              large/untrained/qwen3_large_untrained.py    [dropped file]
        │   └── Qwen3_14B_Compressed         large/untrained/qwen3_14b_compressed.py     [PROD]
        └── Qwen3_8B_Compressed              medium/untrained/qwen3_8b_compressed.py     [DEV]
            └── Qwen3_5_9B_Compressed        medium/untrained/qwen3_5_9b_compressed.py
```

| Property / method | 14B | 8B | 3.5-9B | ChatGPT |
|---|---|---|---|---|
| `name` | `Qwen3_14B_Compressed` | `Qwen3_8B_Compressed` | `Qwen3_5_9B_Compressed` | `ChatGPTOpenAI` |
| `supports_native_tools` | False (`qwen25_medium_untrained.py:241-244`) | False | **True** (`qwen3_5_9b_compressed.py:52-57`) | True (`chatgpt_openai.py:169-171`) |
| `use_tool_classifier` (fastText hint) | True (`qwen25_medium_untrained.py:236-238`) | True | True | **False** (`chatgpt_openai.py:163-167`) |
| `force_tool_calls` | True (`qwen25_7b_compressed.py:50-57`) | True | True (inherited, even on the native path) | absent, so `getattr` gives False (`conversation_handler.py:509`) |
| `get_response_format()` | `{"type":"text"}` (`qwen25_medium_untrained.py:337-339`) | same | same, but unused on the native path | `None`, which means the shared JSON schema (`system_prompt_builder.py:31-67`); unused on the native path |
| `user_message_suffix` | `/think` if `include_thinking`, else `/no_think` (`qwen3_large_untrained.py:69-84`) | same (`qwen3_8b_compressed.py:185-200`) | inherited | `""` |
| `parse_response` | strip `<think>…</think>\s*`, then an unclosed `<think>.*`, then `<message>(.*?)</message>` → inner text; then the Qwen2.5 parser (`qwen3_large_untrained.py:105-110`) | strip think blocks only, **no `<message>` unwrap** (`qwen3_8b_compressed.py:202-206`) | as 8B | default `None` |
| `sanitize_text` | think plus `<message>` strip, then `.strip()` (`qwen3_large_untrained.py:112-129`) | think strip, then `.strip()` (`qwen3_8b_compressed.py:208-216`) | as 8B | identity |
| `build_tools` (native path) | `ToolBuilder.build(tools)` (`qwen25_medium_untrained.py:246-248`) | same | same | same (`chatgpt_openai.py:173-180`) |
| `think_delimiters` | `("<think>","</think>")` (interface default, `ijarvis_prompt_provider.py:201-213`) | same | same | same |
| `get_capabilities()` | qwen/large/untrained | qwen/medium/untrained | `model_family:"qwen3.5"`, medium (`:110-114`) | openai/large |

The Qwen2.5 `parse_response` (`qwen25_medium_untrained.py:341-444`) is the text-path output contract. It is specified in 02. In summary:

- It extracts every `<tool_call>{json}</tool_call>`. Bad JSON is skipped with a warning.
- A string `resolved_datetimes` is wrapped into a one-element list (`:217,366-371`).
- It handles the hybrid envelope: Jarvis JSON with empty `tool_calls` and `<tool_call>` inside `message` (`:384-424`).
- A bare `{"name","arguments"}` object is wrapped.
- Plain text that does not start with `{` becomes `{"message": text, "tool_calls": [], "error": null}`.

### 3.3 System-prompt assembly, layer by layer (byte-exact spec)

The final `messages[0].content` is built in **two layers**:

1. The provider's `build_system_prompt(node_context, timezone, tools, available_command_flags)`.
2. The wrapping in `ConversationHandler._get_system_prompt` (`conversation_handler.py:3270-3331`).

The `timezone` argument is **ignored by every kept provider**. The cached prefix contains no clock time.

#### Shared pieces (all providers)

**`identity`.** Built by `build_context_header` (`ijarvis_prompt_provider.py:36-85`):

```
You are Jarvis, a function calling voice assistant.\nContext: room={room}, style={voice_mode}
```

- `room` defaults to `"unknown"` and `voice_mode` to `"brief"` (`core_rules.py:240-264`).
- If `household_persona.strip()` is non-empty, the header gets `\n\n<personality>\n{PERSONA_FRAME}\n{persona}\n</personality>` appended (`core_rules.py:333-354`, frame at `services/persona_presets.py:26-29`).

**`ANTI_HALLUCINATION_MANDATE`** (`core_rules.py:59-70`) is appended inline to the preamble sentence.

**`dt_keys_line`** (Qwen only). When `node_context["date_keys"]` is non-empty it is:

```
\nDT_KEYS: k1|k2|…\nCRITICAL — resolved_datetimes: … pass ["today"].\n
```

- The full text is identical in three files: `qwen3_14b_compressed.py:83-91`, `qwen3_8b_compressed.py:239-247` and `qwen3_5_9b_compressed.py:82-90`.
- In practice the keys are llm-proxy's **64 static keys, sorted** (`jarvis-llm-proxy-api/services/date_keys.py:98-105,134-142`). The warmup comment says the list is trimmed "to a small high-frequency subset", but the code sends all of them (`conversation_handler.py:478-489`).
- When the fetch fails the list is `[]` and the line is `""`.

**`tool_guidance_section`** (Qwen only; `context_builders.py:27-60`):

- Sources: each tool's top-level `included_system_prompt_text`, stripped and de-duplicated, keeping first-seen order.
- Output: `\nTool Guidance:\n- h1\n- h2\n`, or `""` when there are no hints.
- Only server tools carry it. The ones with non-empty text are `control_device`, `quick_search`, `identify_speaker` and `make_phone_call`.

**`direct_answer_section`** (`context_builders.py:63-119`):

- Commands are split by `allow_direct_answer is True` versus everything else. `None` counts as must-call.
- Output:

  ```
  \nDirect Answer Policy:\n
  - MUST call tools for: {sorted(set)}\n        (if any)
  - Direct answers allowed for: {sorted(set)}\n (if any)
  - BE BRIEF. …\n
  ```

  The section is `""` when there are no commands.

**`agent_context_section`**. Built by `build_agent_context_summary` (`context_builders.py:137-231`):

- It reads `agents.home_assistant` if that key is present, otherwise `agents.device_agent` (`:12-24`).
- **Total devices ≤ 20:** the room-grouped full list from `build_agent_context_by_room` (`:234-340`):
  - Room Groups come first.
  - Then areas sorted alphabetically, with `Unassigned` last.
  - Unavailable devices are skipped.
  - It ends with `Copy entity_id EXACTLY as shown above.` and an optional current-room preference line.
- **Total devices > 20:** a compact `\nHome Assistant: N lights, …` summary, plus `Floors:`, a `control_device` / `get_device_status` instruction, the room preference, and `build_room_hierarchy_section` (`:343-387`).
- The count includes every domain (sensors too). The summary only labels seven domains. If none of those seven exist, the section is `""` even when devices exist (`:195-196`).

#### Qwen3_14B_Compressed (`qwen3_14b_compressed.py:95-110`)

The 14B builder is a copy of `Qwen3LargeUntrained.build_system_prompt` (`qwen3_large_untrained.py:131-202`).

```
{identity}\n
\n
You are a function calling AI model. You may call one or more functions to assist with the user query. Always include all required parameters — use sensible defaults from context when the user does not state them explicitly. {ANTI_HALLUCINATION_MANDATE}\n
\n
You are provided with function signatures within <tools></tools> XML tags:\n
{tools_block}\n
\n
For each function call, return a json object with function name and arguments within <tool_call></tool_call> XML tags:\n
<tool_call>\n
{"name": "<function-name>", "arguments": {"<arg-name>": "<arg-value>"}, "failure_message": "<brief spoken response if this call fails>"}\n
</tool_call>\n
\n
{rules: "Rules:" + 5 bullets}\n
{dt_keys_line}{tool_guidance_section}{direct_answer_section}\n
{agent_context_section}\n
```

**Rules (5).** `RULE_POPULATE_REQUIRED`, `RULE_ONE_AT_A_TIME`, `RULE_BEST_MATCH_INTENT`, `RULE_EXTRACT_PARAMS`, `RULE_STT_AWARENESS`, with `{terminology}` replaced by `function` (`:75-81`; constants at `core_rules.py:18-57`).

**`tools_block`.** Built by `Qwen3LargeUntrained._build_compressed_tools_block` (`qwen3_large_untrained.py:86-103`):

- Input: `ToolBuilder.build(tools, include_param_descriptions=True, include_format_hints=False, exclude_refinable=True)`.
- Output: `<tools>\n` + one `json.dumps(t, separators=(",",":"))` per line + `\n</tools>`. With no tools it is `<tools>\n</tools>`.
- Parameter descriptions are **kept**.

#### Qwen3_8B_Compressed (`qwen3_8b_compressed.py:255-270`)

The template is byte-identical to 14B except for two inputs:

- **Rules (4).** `Qwen25_7B_Compressed._build_rules_block`: POPULATE_REQUIRED, ONE_AT_A_TIME, BEST_MATCH_INTENT, STT_AWARENESS. There is **no `RULE_EXTRACT_PARAMS`** (`qwen25_7b_compressed.py:80-104`).
- **`tools_block`.** `Qwen25_7B_Compressed._build_compressed_tools_block` with **`include_param_descriptions=False`**: per-property `description` keys are stripped, while the tool-level description is kept (`qwen25_7b_compressed.py:59-78`).

The tools block now comes before the format spec. A comment says this mirrors 14B and fixed selection quality (`:249-254`). The dropped 7B parent put it last.

#### Qwen3_5_9B_Compressed (`qwen3_5_9b_compressed.py:92-99`)

This is the native variant. There is **no `<tools>` block and no `<tool_call>` format text**. The preamble wording differs: "one or more **of the available** functions".

```
{identity}\n\nYou are a function calling AI model. You may call one or more of the available functions … {MANDATE}\n\n{rules(4, as 8B)}\n{dt_keys_line}{tool_guidance_section}{direct_answer_section}\n{agent_context_section}\n
```

Tools go out natively as `provider.build_tools(ToolBuilder.strip_jarvis_extensions(all_tools))`, with `tool_choice="auto"` (`tool_execution_engine.py:612-620,726-737`). Those are the **full** schemas: param descriptions and `format` are kept, while `_refinable` and Jarvis extension keys are removed.

#### ChatGPTOpenAI (`chatgpt_openai.py:205-212`)

```
{identity}\n\nYou are a function-calling voice assistant. Use the provided functions … get_current_time … {MANDATE}\n\n{rules}\n{agent_context_section}\n{fallback}\n{direct_answer_section}
```

- **Rules.** The full `build_rules_block()` (`core_rules.py:527-569`), 7 bullets. It includes `RULE_USE_ACTUAL_PARAM_NAMES` ("from the function schema above", but no schema is above) and `RULE_DATE_PARAMS`, which lists 7 hard-coded keys.
- **No DT_KEYS and no Tool Guidance.**
- **Fallback.** `FALLBACK_BRIEF_REPLY` with `{terminology}`→`tool` (`:572-581`).
- The prompt has **no trailing newline**.

#### Layer 2: the handler wrapper (`conversation_handler.py:3293-3331`)

```
prompt = base.rstrip() + "\n\n" + NOT_FOR_ME_INSTRUCTION + "\n\n" + EXCHANGE_COMPLETE_INSTRUCTION + "\n"
if persona.strip():  prompt += "\n" + build_personality_reminder(persona) + "\n"
if characterization.injection_enabled(household):
    node_context["_system_prompt_base"] = prompt                      # stashed for per-turn swap
    if characterization: prompt += "\n" + build_characterization_section(text)   # "<person_view>…</person_view>"
```

- `NOT_FOR_ME_INSTRUCTION` is at `core_rules.py:77-191`. `EXCHANGE_COMPLETE_INSTRUCTION` is at `:193-209` and ends in `\n`.
- The persona reminder is at `:419-444`. The persona text therefore appears **twice**: in the top `<personality>` block and in the end reminder.
- Because of `rstrip()`, the provider's trailing newlines never reach the wire. The internal `\n\n\n` before an empty agent section does survive in the middle only when later sections are non-empty. Golden fixtures must capture both cases.

#### Per-turn messages (built by 01; listed here because they are prompt text the model sees)

Order after `messages[0]` and the trimmed history (`conversation_handler.py:858-971`). Each item is a separate `role=system` message unless noted:

1. Speaker block (`core_rules.py:280-330`), one of:
   - `You are speaking with {name}.`
   - plus `\n\nUser Profile — these facts… :\n{memories}`
   - or `UNKNOWN_SPEAKER_BLOCK` when neither name nor memories is known. It is **never empty**.
2. `<ambient_context>` block (`core_rules.py:360-392`), only when `ambient_context.enabled`. Its text is frozen at warmup (`:3192-3268`):
   - the clock, quantized to 15 minutes;
   - the latest household weather, calendar and reminder memories;
   - live Signals.
3. `RECENTLY SHOWN …` block (`core_rules.py:487-524`, at most 8 items).
4. `Router hint: likely tool is '{x}'. …`, when the fastText router fires (`:893-898`).
5. The `user` message:

   ```
   utterance [\n\n direction_hint] [\n\n affect_hint] [\n\n turn_hint] [\n\n profile_hint] [\n\n agent_context] [\n {suffix}]
   ```

   (`:916-970`). The suffix is joined with a **single** `\n`.

`_is_transient_system_block` strips old copies of items 1–4 before re-adding them each turn (`:110-139`). See §8 for a bug in it.

### 3.4 Inputs that vary the prompt (the golden-fixture axis list)

| Input | Source | Affects |
|---|---|---|
| Provider (`llm.interface`) | setting | everything |
| `room` | node row | identity, agent room line |
| `voice_mode` | node row | identity |
| `household_persona` (`persona.household_prompt`, default `DEFAULT_PERSONA`) | setting, per household | `<personality>` block plus end reminder |
| `date_keys` | llm-proxy vocabulary | DT_KEYS (Qwen) |
| Tool list and order | `server_tools + client_tools`, with **no dedup** (`core/warmup_service.py:133-148`) | tools block, Tool Guidance |
| Server tool whitelist (text path) | `answer_question`, `make_phone_call`, `run_errand`, `schedule_errand`, `list_scheduled_errands`; plus `deep_research`/`quick_search` if `web_search.enabled`; plus `remember`/`forget` if a speaker is known and `memory.enabled`; plus `recall` if `memory.recall_enabled` (`conversation_handler.py:311-363`) | tools block, guidance |
| Server tools (native path) | **all** registered tools, ungated (`:364`; `tool_registry.py:89-91`) | native `tools`, guidance |
| `_refinable` params | client tools | removed from the Qwen tools block |
| `available_command_flags` | client commands (`voice_command_helpers.py:67-87`) | Direct Answer Policy |
| `agents.home_assistant` / `agents.device_agent`, `room_hierarchy` | node payload / rooms | agent section |
| `model.include_thinking` | setting, per household | `/think` vs `/no_think` suffix (`:3047-3068`) |
| `characterization.injection_enabled` + characterization row | setting + DB | `<person_view>` tail |
| `ambient_context.enabled`, `memory.enabled` | settings | ambient trailing block |
| speaker / memories / router / turn hints | per turn | trailing messages (01) |
| `LOG_FULL_SYSTEM_PROMPT` env | env | logging only |

### 3.5 Date pipeline end to end

1. **Vocabulary.** At warmup, CC fetches `GET llm-proxy /v1/adapters/date-keys` and reads `static_keys`, the 64 sorted keys (`llm_proxy_client.py:315-325`; `jarvis-llm-proxy-api/api/adapter_routes.py:15-29`). The route lives in llm-proxy's **adapters** router, which is LoRA-adjacent.
2. **Prompting.** Qwen providers inject DT_KEYS (§3.3). ChatGPT relies on `RULE_DATE_PARAMS`.
3. **Extraction on every LLM call.**
   - CC sends `include_date_context=True` on tool-loop calls (`tool_execution_engine.py:733,750`).
   - llm-proxy takes the **last `user` message's text** (`jarvis-llm-proxy-api/services/model_service.py:366-380`). That text includes the appended hints, the agent context and `/no_think`.
   - It runs the regex matcher (`services/date_key_matcher.py`).
   - If that returns nothing, it falls back to a fastText model and may **append a `[DATE_HINT]` system message to the prompt** (`model_service.py:392-409`, `services/date_keys.py:646-672`).
   - It returns `date_keys` in the response (`:416-417`).
4. **CC normalisation.** `normalize_date_key`: strip, lowercase, whitespace→`_`, `:`→`_` (`date_resolution.py:13-29`, `tool_execution_engine.py:796-801`).
5. **ISO guard** (`tool_execution_engine.py:320-386,1260-1279`). On `finish_reason=="tool_calls"`, any ISO strings in `resolved_datetimes` are reverse-mapped to keys:
   - First a multi-value match on the sorted tuple (e.g. `this_weekend`).
   - Then per-value matching.
   - If it cannot map them, CC pops the assistant message and adds one `[ISO_DATE_RETRY]` system nag (at most one per turn).
6. **Injection** (`_inject_date_keys`, `:430-613`). This applies only to parameters whose schema has `format: date-time`, or array items with that format (`date_resolution.py:232-267`). The schemas are looked up in `tools + cached full tool list` (`:1286-1297`).
   - **MCP path.** `jarvis_mcp_client.resolve_date_keys` is tried first (`:72-76,446-458`). The package is not in any requirements file or repo, so `_HAS_MCP_CLIENT` is always False. **The MCP path is dead code today, so nothing needs replacing.**
   - **Local resolution.** `resolve_date_keys(date_keys, generate_date_context_object(tz))` returns `(resolved, unresolved)`.
   - **Unresolved keys** go to a **separate LLM call**, `ResolveRelativeDateTool.resolve_with_llm_fallback`. It uses the `live` model at temperature 0 with a "pick ONE key" prompt (`tools/resolve_relative_date_tool.py:241-343`). Invalid answers fall back to `"today"`.
   - **Parameter empty:** inject all `resolved` (array parameter) or `resolved[0]` (scalar). With no keys at all, inject `today`'s UTC start of day (`:566-586`).
   - **Parameter set:** each non-ISO string is resolved **individually** through the flat map. A list value contributes only its **first element**, so `"this_weekend"` becomes Saturday only. Then the LLM fallback runs (`:490-528,588-606`). An array that resolves to empty is replaced by `resolved_dates`.

### 3.6 The `resolve_date_keys` algorithm (`date_resolution.py:270-348`)

**Normalisation.** Normalize all keys and flatten the context (`flatten_date_context`, `:84-163`):

- `today`;
- `relative_dates.*`;
- the bucket lists `weekend.*`, `weeks.*`, `months.*` and `years.*` → lists;
- `weekdays.*`;
- `this_<day>` from `weeks.this_week`;
- normalized `time_expressions`.

**Per key, in order:**

1. `^in_(\d+)_(minutes|hours|days)(?:_(\d+)_(minutes))?$` gives `current.datetime + offset`, formatted as UTC `…Z` (`:32-81`).
2. Otherwise a flat lookup. Lists are extended in place.
3. Otherwise the key goes to `unresolved`, unless it is a time modifier (`morning`, `afternoon`, `evening`, `night`, `noon`, `midnight`, or `at_*`).

**Combination.** Take the **first** key whose flat value is a string as `date_key`, and the first modifier as `time_key`. If both exist, append `apply_time_modifier(base, mod)`:

- The modifier **replaces the hour on the UTC instant** (`:197-229`): morning 7, afternoon 13, evening 18, night 21, noon 12, midnight 0.
- `at_*` uses `parse_time_string` (`:166-194`).

**Dedup.** The result is de-duplicated, preserving order. The raw base date is **not** removed. The MCP copy of this function did remove it (`jarvis-mcp/jarvis_mcp/services/datetime_service.py`, the "Remove the raw base date" branch).

### 3.7 `generate_date_context_object(tz)` (`core/general_context.py:33-374`)

**Timezone normalisation** (`:9-30`):

- `UTC±00:00` → `UTC`.
- `UTC±H[:MM]` → `Etc/GMT∓H`. The minutes are dropped.
- An invalid zone falls back to the server's local naive time (`:47-59`).

**Output keys:**

| Key | Contents |
|---|---|
| `current` | `date` (`%A, %B %d %Y`), `date_iso`, `time` (`%I:%M %p`), `datetime` (UTC `Z`), `weekday`, `weekday_number`, `utc_start_of_day` |
| `relative_dates` | tomorrow, yesterday, `last_night` (yesterday 19:00 local, as `datetime`), day_after_tomorrow, day_before_yesterday |
| `weekend` | this, next and last, as `[Sat, Sun]` |
| `weeks` | this, next and last: Sunday→Saturday, 7 entries each |
| `months` | this, next and last: `[first, last]` |
| `years` | this, next and last: `[Jan 1, Dec 31]`, computed via ±365 days |
| `weekdays` | `next_*` / `last_*` |
| `timezone` | |
| `time_expressions` | `this morning` 07, `this afternoon` 14, `this evening` 19, `tonight` 20, meals, `at noon` / `at midnight`; tomorrow and yesterday variants; `at {1..12}am|pm`, `:30`; and `:15` / `:45` for hours 9, 10, 11, 1, 2 and 3 (`:376-453`) |

`utc_start_of_day` is local midnight expressed in UTC. With no timezone it is `YYYY-MM-DDT00:00:00Z`; the `Z` is load-bearing, see the comment at `:63-69`.

**Weekends.** On a Saturday, "this weekend" means today and tomorrow. On a Sunday it means yesterday and today. On weekdays it means the coming Saturday and Sunday (`:116-152`).

---

## 4. Data

| Item | Notes |
|---|---|
| `prompt_provider_install_requests` (`models.py:545-566`) | `id`, `household_id` (NOT NULL), `package_name`, `github_repo_url`, `git_tag`, `status` (pending, completed, failed, expired), `results_json`, `error_message`, `created_at`, `expires_at` (+5 min), `completed_at`. Cut with the feature, unless Q2 keeps a stub. |
| `core/prompt_providers_custom/` | On-disk installed providers, mounted on a docker volume. Dropped. |
| `node_context` keys read by prompts | `room`, `voice_mode`, `household_persona`, `date_keys`, `agents`, `room_hierarchy`, `speaker_name`/`user`, `user_memories`, `characterization`, `_system_prompt_base`, `ambient_context`, `household_id`. These live in the in-process conversation cache (01). |
| Settings rows | per §5 |

The date logic keeps no state: it is recomputed on every call from the current time.

---

## 5. Settings

| Key | Default | Effect | Read at |
|---|---|---|---|
| `llm.interface` | `Qwen25MediumUntrained` (a dropped provider) | Provider selection. The env fallback is `JARVIS_MODEL_INTERFACE`, then `JarvisToolModel`. | per request (`prompt_provider_factory.py:178-203`) |
| `persona.household_prompt` | `DEFAULT_PERSONA` (warm and folksy, `persona_presets.py:36-43`) | `<personality>` block plus end reminder. Empty means no persona. Writes are capped at `PERSONA_MAX_CHARS=2000` (`mobile_household_settings.py:156-165`). On a settings **error** CC uses `DEFAULT_PERSONA`. A `None` value gives `""`, which contradicts the docstring (`conversation_handler.py:3403-3434`). | warmup |
| `model.include_thinking` | False | `/think` vs `/no_think` | each turn (`:3047-3068`) |
| `characterization.injection_enabled` | False | `<person_view>` tail and base stash | warmup and per turn |
| `ambient_context.enabled` | False | ambient trailing block; also requires `memory.enabled` (`:471`) | warmup |
| `web_search.enabled` | False, fail-closed | `quick_search` / `deep_research` and their guidance, **text path only** | warmup |
| `memory.enabled`, `memory.recall_enabled`, `memory.pinned_max_chars` (500) | — | memory tools in the whitelist; memory block size | warmup |
| `prompt.include_antipatterns`, `prompt.include_param_descriptions` | True | **No effect on kept providers.** They are only reached through the legacy `tool_call_parser.format_tools_for_prompt`, and only as the env var `JARVIS_PROMPT_INCLUDE_PARAM_DESCRIPTIONS` (`tool_call_parser.py:351`). The settings rows are never read. | — |
| `model.small_model_mode` | True | Not prompt text for the kept providers (tool engine, 02). | — |
| env `LOG_FULL_SYSTEM_PROMPT` | false | Logs the full prompt. | build |

---

## 6. Dependencies

| Kind | What |
|---|---|
| Other CC subsystems | 01 conversation handler: warmup, per-turn hints and cache. 02 tool engine: parse, inject and ISO guard; ToolBuilder; tool registry. 04 memory: memories, characterization, ambient memories. 07 smart home: agent data and room hierarchy. 10 signals: the ambient Signal lines. |
| llm-proxy, today | `GET /v1/adapters/date-keys` (vocabulary). `include_date_context` → `date_keys` (regex matcher, fastText fallback, `[DATE_HINT]` injection). The engine info `allows_caching` gates warmup. **In `jarvisd` all of these are in-process calls into the `llm` module.** |
| LLM calls owned here | `resolve_with_llm_fallback`: `live` slot, temperature 0, `include_date_context=False`, one-shot prompt (`resolve_relative_date_tool.py:268-293`). |
| Third party | `pytz` (date maths), `zoneinfo` (ambient clock), and `git` plus `exec` (installer, dropped). |
| jarvis-mcp | Nothing in practice; see §3.5. |

---

## 7. Invariants and non-obvious behaviour

1. **Byte stability of `messages[0]` is a latency feature, not cosmetics.** llama.cpp prefix caching relies on a household-stable prefix. These are deliberately kept *out* of the cached prompt (`ijarvis_prompt_provider.py:75-84`, `core_rules.py:244-255`):
   - the speaker's name and memories;
   - ambient time and weather;
   - router hints.

   Never put per-turn or per-speaker data into `messages[0]`. The persona and room may stay there because they are per-household.
2. **Tool pruning must not rebuild `messages[0]`.** It was removed on 2026-07-21 because of a ~1.3 s cold prefill (`conversation_handler.py:845-850`).
3. **The native warmup must send exactly the same tool transform as inference** (`build_tools(strip_jarvis_extensions(...))`) for the cache to hit (`:525-536`).
4. **`json.dumps` defaults in the `<tools>` block:**
   - `ensure_ascii=True`: non-ASCII becomes `\uXXXX`, with surrogate pairs above the BMP;
   - `<`, `>` and `&` are **not** escaped;
   - compact separators;
   - dict **insertion order**.

   Go's `encoding/json` differs on all four points: it escapes HTML, emits UTF-8 raw and sorts map keys. Verified: `"Weather é <b>"` is emitted as `"Weather é <b>"`.
5. **`ToolBuilder._strip_*` work on shallow copies.** `strip_jarvis_extensions` **mutates** the nested property dicts: it pops `_refinable` from the cached tools in place (`tool_builder.py:180-186`). On native providers this happens after the prompt is built, so it does not show in the prompt. It does change later lookups.
6. **The persona is fenced twice** and always framed by `PERSONA_FRAME`. Free text never appears outside `<personality>` or the "YOUR VOICE" reminder.
7. **`UNKNOWN_SPEAKER_BLOCK` is load-bearing.** It is a fix for a prod incident on 2026-08-25 (`core_rules.py:267-277,290-298`).
8. **The DT_KEYS string must contain the proxy vocabulary verbatim,** sorted byte-wise and joined with `|`.
9. **`utc_start_of_day` without a timezone must end in `Z`** (`general_context.py:63-69`).
10. **`parse_time_string` never raises.** Unparseable input gives `(0,0)` (`date_resolution.py:166-194`).
11. **The `/think` toggle is per household and read every turn.** The provider instance is per request, so this mutable attribute does not leak across households (`deps.py:241`).

---

## 8. Oddities (bugs and dead code found)

**Date bugs.** All were reproduced on 2026-10-06 (a Tuesday) with `America/New_York`.

1. **`next_<weekday>` / `last_<weekday>` are off by one day.**
   - Cause: `weekday_names` starts at Monday but is offset from a **Sunday** week start (`general_context.py:175-187`).
   - Effect: `next_monday` = 2026-10-11 (a Sunday) and `next_tuesday` = 2026-10-12 (a Monday).
   - `this_<day>`, which comes from `weeks.this_week` labels, is correct.
   - The unit tests use hand-built contexts, so this is never caught.
2. **Time modifiers are applied in UTC, not local time** (`date_resolution.py:224-229`). The tests only pass because their fixtures use `T00:00:00Z` (`tests/test_date_resolution.py:335-344`). The base midnight is also kept in the output:

   | Input keys | Result |
   |---|---|
   | `["morning","next_tuesday"]` | `[Mon 04:00Z, Mon 07:00Z]`, i.e. 3 am local |
   | `["at_3pm"]` | `[19:00Z, 15:00Z]` (`at_3pm` is both a flat date value and a modifier) |
   | `["at_7_30pm","tomorrow"]` | `[today 23:30Z, tomorrow 04:00Z, today 19:30Z]`, and never tomorrow 7:30 pm |
3. **Model-supplied list keys keep only the first date.** In `_resolve_relative_datetime`, `"this_weekend"` in `resolved_datetimes` becomes Saturday only (`tool_execution_engine.py:502-505`).
4. **pytz arithmetic on aware datetimes** (`now + timedelta`, `.replace(hour=0)` on a localized value) keeps the original UTC offset. Across a DST boundary, `utc_start_of_day` is off by one hour.
5. **`UTC+05:30` loses its minutes** (`general_context.py:29`).
6. **`next_year` / `last_year` use ±365 days,** so a date near Dec 31 in a leap year lands on the wrong year.
7. **The vocabulary drifts between the proxy and CC.**
   - `after_dinner`, `during_breakfast` and `during_dinner` are in DT_KEYS but missing from CC's context. They always trigger the extra LLM fallback call.
   - The proxy's dynamic patterns do not list `in_N_hours`. CC resolves it, and the regex emits `in_120_minutes` anyway.
8. **The LLM fallback cannot work for some keys.**
   - `available_keys` includes `now`, which has no flat value.
   - It includes `time_expressions` keys **with spaces**, so the model's normalized `tomorrow_morning` never matches and the fallback degrades to `today`.
   - It omits `this_month` etc. (`resolve_relative_date_tool.py:345-379`).
9. **Date extraction runs on the hint-polluted user message:** turn, direction and affect hints, agent context (calendar or news text) and `/no_think`. A calendar line mentioning "tomorrow" can inject dates.
10. **`resolve_relative_date_term`** normalises with underscores and then looks up `time_expressions` keys with spaces, which never matches (`date_detector.py:203`). It is only used by the legacy `JarvisToolModel`.

**Dead code.**

11. **Dead modules and functions:**
    - `date_replacer.py` (no callers).
    - `date_detector.py`, reached only from legacy `jarvis_tool_model.py:135`.
    - `general_context.get_general_context` and `_format_date_context_for_prompt` (no callers).
    - `system_prompt_builder.build_tool_system_message`, reached only via the never-called `ModelService._build_tool_system_message` (`model_service.py:419-427`). Its `get_response_format` is live.
    - `shared/command_converters.py`, which has no callers; `jarvis_tool_model.py:430` has its own copy.
    - `shared/tool_formatters.py` and `build_agent_context_section`, reached only from dropped providers.
    - `prompt_variant_builder.py`, which is LoRA training only (`adapter_scheduler.py:366`, `scripts/extract_training_data.py`). Cut.
    - `IJarvisPromptProvider.build_training_*`, `lazy_tool_loading` and `think_delimiters` overrides are unused by the kept set.
12. **The MCP date path is dead.** `jarvis_mcp_client` exists nowhere.

**Prompt and assembly issues.**

13. **Transient-block stripping misses two speaker variants.**
    - `_is_transient_system_block` matches `"User Profile - If user asks"`, but the block now starts `"User Profile — these facts"`.
    - It does not match `UNKNOWN_SPEAKER_BLOCK` (`"You do not know who…"`) at all.
    - So with an unknown speaker, or memories but no name, **one extra system message accumulates per turn** (`conversation_handler.py:110-139` vs `core_rules.py:273-330`).
14. **ChatGPT's prompt** says "the function schema above" with no schema present, uses the stale 7-key `RULE_DATE_PARAMS`, and gets no Tool Guidance. That is acceptable for a test-only provider.
15. **The native path (9B, ChatGPT) gets every server tool, ungated** by `web_search.enabled`, speaker or memory settings (`conversation_handler.py:364`). It relies on the `execute()`-time rechecks. The Tool Guidance for `quick_search`, `control_device` and `identify_speaker` therefore appears on 9B even when the household disabled web search.
16. **8B, 9B and 14B differ subtly.** 14B unwraps `<message>` and 8B/9B do not. 14B keeps param descriptions and 8B does not. 9B is forced to call tools (`force_tool_calls=True`) while using native tools. 9B has no `<message>` unwrap even though the docstrings imply parity.
17. **Factory cost.** `walk_packages` plus instantiating every class per request (§3.1). `llm.interface`'s default names a dropped provider, and the factory's last fallback is the legacy `JarvisToolModel`.
18. **The warmup comment says DT_KEYS are trimmed; they are not** (`conversation_handler.py:478-489`).
19. **`merge_tools` does not dedup** server and client tools of the same name.

**Install routes.**

20. **`/prompt-providers/install` lets any household JWT make CC `git clone` and `exec()` arbitrary Python** (`prompt_provider_installer.py:236-302`). This is effectively remote code execution for any user. Also, admin-key auth leaves `household_id=None`, which a NOT NULL column rejects with a 500.

---

## 9. Tests

| File | Covers | Go use |
|---|---|---|
| `tests/test_date_resolution.py` (509 lines) | normalize, flatten, `parse_time_string`, modifiers, relative-time, `resolve_date_keys` | Port as table tests. Note they encode the UTC-modifier bug (Q1). |
| `tests/test_date_detector.py` | relative-offset regexes | Only if `date_detector` is kept; it should be dropped (§8.11). |
| `tests/test_core_rules.py` (482), `tests/test_persona.py` (252), `tests/test_ambient_context.py`, `tests/test_not_for_me.py`, `tests/test_characterization_injection.py`, `tests/test_characterization_wiring.py` | rule text, speaker, persona and ambient blocks, wrapper ordering | Port; convert to golden fixtures. |
| `tests/test_qwen3_5_9b_compressed.py` (4 tests), `tests/test_chatgpt_openai_provider.py`, `tests/test_thinking_context_split.py` | native flags, `/think` | Port. **There is no direct test of the 14B or 8B prompt text.** |
| `tests/test_qwen25_medium_untrained.py` | the `parse_response` contract the kept Qwens inherit | Port the parse cases into 02's fixtures. |
| `tests/test_tool_builder.py`, `tests/test_prompt_provider_factory.py`, `tests/test_tool_execution_engine.py` (date injection, ISO guard) | | Port the relevant parts. |
| `tests/test_system_prompt_builder.py` | dead `build_tool_system_message` | Drop. |
| `jarvis-llm-proxy-api/tests/test_date_key_matcher.py` plus the 4,987-example corpus (PLAN §4) | regex extractor | Phase 3 golden. Needed here as the input side. |

**Golden fixtures to export (Phase 0, exporter beside the Python code).**

- **G1, prompts.** Freeze the clock. For each of {14B, 8B, 9B, ChatGPT}, cover:
  - no tools and no commands;
  - real prod tool set (export the warmed `all_tools` and command flags from dev);
  - `_refinable` param, non-ASCII description, `<`, `&`, nested arrays;
  - date_keys present / absent;
  - persona default, custom and empty;
  - HA agents with ≤20 devices, >20 devices plus floors plus hierarchy, only non-summary domains;
  - characterization on and off;
  - `include_thinking` on and off.

  Dump the full `messages[0]`, the native `tools` JSON for 9B and ChatGPT, and `user_message_suffix`. Include the per-turn speaker, ambient and recently-shown blocks as separate fixtures.
- **G2, date context.** `generate_date_context_object` for:
  - zones: UTC, America/New_York, Asia/Kolkata, `UTC+05:30`, none, invalid;
  - each weekday;
  - month and year ends;
  - Feb 29;
  - both DST transition days.

  Monkeypatch `datetime.now`.
- **G3, resolution.** `resolve_date_keys` over all 64 vocabulary keys, plus `in_*`, plus combinations from the matcher corpus. Run each through `_inject_date_keys` for scalar and array params, empty and pre-filled.
- **G4, ISO guard.** `_try_fix_iso_dates` cases: clean, fixed-single, fixed-multi, bad.
- **G5, parse and sanitize.** Think and `<message>` cases per provider.
- **Contract test.** `GET /generate/date-context` shape against node-setup's `DateContext` model.

---

## 10. Questions for the user

**Q1 `[behaviour]` Fix the date bugs in Go, or keep them byte-for-byte?**
§8.1–8.6 mean that "next Tuesday", "tomorrow at 7:30 pm", "Friday morning" and DST-week dates resolve wrong today. Off-by-one weekdays and 3 am "mornings" reach reminders, calendars and timers.

*Why it matters:* golden parity versus correctness. Shadow replay (PLAN §4 L4) would flag every fixed case as a diff.

*Options:*
- (a) Bug-for-bug parity first, fix in a follow-up.
- (b) Fix in Go from day one, with the golden fixtures regenerated from a corrected spec.
- (c) Fix only the weekday off-by-one and local-time modifiers.

**My recommendation: (b).** Write the corrected spec now: local-time modifiers, a single combined instant (no stray midnight), correct `next_<day>`, DST-correct zones, minute offsets. Mark G2 and G3 as "intentional divergence". The Python fixtures are still useful for the cases that are already correct.

**Q2 `[scope]` What should `/prompt-providers/install` and its poll route do in Go?**
Mobile still shows "Install to Command Center" for Pantry packages that have a `prompt_provider` component.

*Options:*
- (a) Don't port them; mobile gets a 404 and shows "Install Error".
- (b) Port a stub: install returns 201 with an id, and poll returns `{status:"failed", error_message:"Prompt-provider packages are not supported by this server"}`.
- (c) Also hide such packages in Pantry or mobile.

**My recommendation: (b) plus (c).** Zero mobile change, a clear message, and no `exec`. Separately, confirm that you're aware the current route is remote code execution for any household JWT (§8.20). Do you want it disabled in Python prod now, despite the freeze?

**Q3 `[behaviour]` How should a provider name the Go binary does not ship be handled?**
`llm.interface` defaults to the dropped `Qwen25MediumUntrained`. Imported legacy settings may name Gemma, Llama and others.

*Options:*
- (a) Hard error at startup.
- (b) Map known-dropped names to the nearest kept one (Qwen2.5 → `Qwen3_8B_Compressed`) and log a warning.
- (c) Always fall back to one default.

**My recommendation:** make `Qwen3_8B_Compressed` the default, use (b) for names in the drop list, and use (a) for unknown names (`doctor` reports it). `JarvisToolModel` and legacy models disappear entirely.

**Q4 `[behaviour]` Should the 14B / 8B / 9B differences be kept, or should the providers be unified?**
The intentional-looking differences are 5 vs 4 rules and param descriptions on or off. The likely-accidental ones are the `<message>` unwrap only on 14B, and `force_tool_calls` on native 9B.

*Options:*
- (a) Port exactly.
- (b) Port exactly, plus add the `<message>` unwrap to the 8B/9B sanitize.
- (c) Collapse to one parameterised Qwen builder.

**My recommendation: (a)** for prompt bytes, with an internal parameterised builder (rules count, descriptions flag, native flag), and **(b)** for sanitize, because a `<message>` wrapper spoken by TTS on 8B is a plain bug.

**Q5 `[behaviour]` Should the native path keep receiving all server tools, ungated?**
On 9B and ChatGPT, `quick_search`, `deep_research`, `remember` and `recall` are offered (with guidance) even when `web_search.enabled=false`, no speaker is known, or memory is off (§8.15).

*Options:*
- (a) Keep the current behaviour.
- (b) Apply the same whitelist and gates to both paths.

**My recommendation: (b).** The CLAUDE.md says "toggle off means no egress" is the intent.

**Q6 `[behaviour]` Fix the accumulating speaker blocks?**
Unknown-speaker and memories-only turns add one more system message per turn (§8.13).

*Options:*
- (a) Bug-for-bug.
- (b) Fix: tag transient blocks structurally (an internal marker field, not a content prefix).

**My recommendation: (b).** It only grows the prompt, and shadow replay should ignore the count.

**Q7 `[scope]` Keep llm-proxy's fastText date-key fallback and its `[DATE_HINT]` message?**
This is a second fastText model on top of the regex matcher, which already covers the 4,987-example corpus. It mutates the prompt server-side.

*Options:*
- (a) Keep it.
- (b) Drop it; regex only.

**My recommendation: (b),** unless you know of real utterances the regex misses. It removes a model download and a hidden prompt mutation.

**Q8 `[behaviour]` Should date extraction see only the raw utterance?**
Today it scans the user message *after* hints and agent context are appended (§8.9).

*Options:*
- (a) Keep it.
- (b) Extract from the raw transcript, which `jarvisd` can pass in-process.

**My recommendation: (b).**

**Q9 `[behaviour]` What should happen to the LLM fallback for unresolved date keys?**
It costs a whole extra `live`-slot LLM call on the hot path. It fires for 3 vocabulary keys CC can't resolve, and its key list is malformed (§8.7–8.8).

*Options:*
- (a) Keep it.
- (b) Close the vocabulary gap (add `after_dinner` etc.), make the vocabulary one shared Go constant used by both prompt and resolver, and drop the fallback. Unknown keys become `today`.
- (c) Keep the fallback but fix its key list.

**My recommendation: (b).**

**Q10 `[behaviour]` Is the doubled persona (top `<personality>` plus end "YOUR VOICE" reminder) still wanted for the 14B/27B prod model?**
It was added for small-model recency.

*Options:* keep for all; or keep only for the 8B/9B tier.

**My recommendation:** keep as-is for parity. Revisit after the port.

**Q11 `[minor]` Should `/generate/date-context` stay?**
The node calls it on every conversation start, but the SDK ignores the result.

*Options:* keep it as is; or keep it but stop the node calling it later.

**My recommendation:** keep it, but generate it from the *corrected* date module, keeping the strict `DateContext` shape with `user_timezone` as a non-null string and `is_dst` as a bool. The node rejects `null` there today when no timezone is sent, so always fill both.

**Q12 `[minor]` Can the vestigial prompt settings be dropped from the definitions?**
`prompt.include_antipatterns`, `prompt.include_param_descriptions` and `model.small_model_mode` (as prompt text) have no effect on kept providers.

*Options:* drop them; or keep them as inert rows for import compatibility.

**My recommendation:** drop them from the definitions, and have `import-legacy` ignore them.

---

## 11. Go port notes

**Package shape.** Use `internal/modules/cc/prompts`:

- An `interface Provider { Name(); BuildSystemPrompt(ctx PromptContext, tools []Tool, flags []CommandFlag) string; NativeTools() bool; UseClassifier() bool; ForceToolCalls() bool; ResponseFormat() *RespFmt; UserSuffix(includeThinking bool) string; ParseResponse(string) (string, bool); Sanitize(string) string; BuildTools([]Tool) []json.RawMessage }`.
- Pass `include_thinking` as an argument instead of a mutable field.
- Use a typed `PromptContext` struct in place of the `node_context` dict; there are about 14 keys (§4).
- Keep a static registry map. There is no discovery and no custom root. Resolve the provider once per warmup.

**Text constants.** Copy every rule and instruction string into one `rules.go`, verbatim, with `{terminology}` substitution. Generate them from Python with an exporter so typos are impossible. Assert with G1.

**Ordered JSON encoder** for the `<tools>` block and for native `tools`:

- preserve insertion order (`[]kv` or `json.RawMessage` built in order);
- compact separators;
- `SetEscapeHTML(false)`;
- escape non-ASCII to `\uXXXX`, with UTF-16 surrogate pairs for code points above U+FFFF.

Fuzz-test it against Python's `json.dumps` on the G1 tool corpus. Tools arrive from node JSON, so keep them as ordered raw objects end to end; never round-trip them through `map[string]any`.

**`rstrip`.** `strings.TrimRightFunc(s, unicode.IsSpace)` matches Python for this content.

**Dates.** Implement `datectx` on `time.LoadLocation`, with `time.Date(y,m,d,0,0,0,0,loc)` for local midnight. That gives DST-correct results for free, but only if Q1 says fix.

- Embed tzdata (`time/tzdata`) so Windows works.
- Inject the clock (`func() time.Time`) for fixtures.
- The vocabulary becomes one Go slice shared by the DT_KEYS prompt, the regex matcher (Phase 3 `llm` module) and the resolver. That removes the HTTP `date-keys` fetch, the drift (§8.7) and the MCP branch.
- `include_date_context` / `date_keys` become an in-process call `dates.Extract(rawUtterance)` made by the tool loop, rather than a response field from llm-proxy.

**Persona.** `persona_presets` is a static table and the presets route serves it unchanged. Enforce the 2000-character cap on write.

**Install routes.** Per Q2: stub handlers, no table, no git or exec.

**Risk: byte-exactness across the entire assembled conversation** (`messages[0]` plus trailing blocks plus user suffix). Prefix-cache behaviour depends on it, and so does the prod 27B model's tuned behaviour. G1 must be the gate for Phase 5b. Run shadow replay (L4) with the LLM fake keyed on a hash of the full message list, so any drift shows up as a cache miss in the fake.
