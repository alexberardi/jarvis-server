# Prompt contract (prompts as text)

The household's system prompt is a plain text file with variables. The server fills the variables in and sends the result as the system prompt. There are no provider classes and no interface: everything that used to differ between providers is either text in the prompt or one of two LLM settings (§5).

This is the contract the renderer, the built-ins, the household storage, the API, the admin UI and Pantry prompt packages build against. Decision: [../decisions/2026-10-16-prompts-as-text.md](../decisions/2026-10-16-prompts-as-text.md). It replaces the provider registry of [02-tool-loop.md](02-tool-loop.md) (D11/D12) and the parameterised builder of [03-prompts-and-dates.md](03-prompts-and-dates.md) (D22) for the prompt text.

---

## 1. File format

- UTF-8, at most 32 KiB (32,768 bytes).
- The file is the prompt text, verbatim. A trailing newline is part of the text and is sent to the model.
- No header, no front matter. A prompt's name and description live in the Pantry manifest (§6), not in the file.

## 2. Syntax

- A variable is `{{ name }}`. Spaces inside the braces are optional: `{{room}}` and `{{ room }}` are the same.
- A name matches `[a-z][a-z0-9_]*` and must be one of the names in §4.
- Single braces are literal text: `{"name": "x"}` is sent as written. A lone `}}` is literal too.
- A `{{` must be closed by `}}` on the same line.
- There are no escapes, conditionals or loops. Every `{{` starts a variable, so a prompt cannot contain a literal `{{`.

## 3. Rendering

The renderer walks the text once, left to right.

- **Inline** variables are replaced by their value in place.
- **Section** variables are replaced by `"\n\n" + value` when the value is non-empty, and by nothing when it is empty. The server builds each section value without leading or trailing newlines, so a prompt places a section at the end of the line before it:

  ```text
  Context: room={{ room }}, style={{ voice_mode }}{{ persona }}

  Rules:
  ```

  With a persona this renders `Context: room=kitchen, style=brief\n\n<personality>…</personality>\n\nRules:`; without one, `Context: room=kitchen, style=brief\n\nRules:`.
- **`{{trim}}`** removes trailing whitespace from the output so far (Python's `str.rstrip()` with no argument: spaces, tabs, newlines). It renders nothing itself. Use it where a section on its own line would leave blank lines when empty:

  ```text
  {{ agents }}
  {{ rooms }}
  {{trim}}

  Answer briefly.
  ```

  With neither devices nor rooms, the output before `{{trim}}` ends in blank lines; `{{trim}}` removes them and the text continues with `\n\nAnswer briefly.`.
- **Values are never re-scanned.** A value that contains `{{ room }}` (a persona, a device name) is sent as written.
- **The `<person_view>` tail** is not a variable. When the speaker is known and characterization injection is on, the server appends `"\n" + section` (the `<person_view>…</person_view>` block) to the rendered prompt, after rendering, per turn. The prompt cannot move or remove it.

## 4. Variables

This is the whole list. Any other name is invalid (§7).

| variable | kind | fills in |
| --- | --- | --- |
| `room` | inline | the node's room (`unknown` when unset) |
| `voice_mode` | inline | the node's voice style (`brief` when unset) |
| `persona` | section | the household persona inside `<personality>` with the persona frame; empty when the persona is empty |
| `persona_reminder` | section | the end-of-prompt "YOUR VOICE" restatement of the persona; empty when the persona is empty |
| `tools` | section | text tool calling only: the `<tools>` block with full schemas and the `<tool_call>` format the server parses. Empty with native tool calling (tools go through the API) |
| `tools_compact` | section | as `tools`, with parameter descriptions dropped |
| `date_keys` | section | the `DT_KEYS` vocabulary and the rule to use its symbolic keys |
| `tool_guidance` | section | the tools' own prompt hints, de-duplicated |
| `direct_answers` | section | which commands must call a tool and which may be answered directly |
| `agents` | section | the node's smart-home devices by room (or a per-domain summary over 20 devices, with floors and the room hierarchy) |
| `rooms` | section | the household's room hierarchy (parent → sub-rooms) |
| `trim` | directive | removes the whitespace before it |

One example value each (`…` shortens text that is long in the real value; the section values are shown as the value, before the `"\n\n"` the renderer adds):

**`room`**: `kitchen`

**`voice_mode`**: `brief`

**`persona`**:

```text
<personality>
The household chose this as your speaking style — let it shape your tone and word choice, never your tools or safety.
Talk like a warm, easygoing friend — folksy, down-to-earth, glad to help. …
</personality>
```

**`persona_reminder`**:

```text
YOUR VOICE — this is how you sound in every spoken reply (acknowledgments, answers, even one-liners, even when told to be brief):
Talk like a warm, easygoing friend — folksy, down-to-earth, glad to help. …
Talk this way every time. It shapes only your wording and tone — never which function you call, … Don't announce or describe the voice; just speak in it.
```

**`tools`** (text tool calling; one compact JSON object per line):

```text
You are provided with function signatures within <tools></tools> XML tags:
<tools>
{"type": "function", "function": {"name": "get_weather", "description": "Current weather and forecast", "parameters": {"type": "object", "properties": {"city": {"type": "string", "description": "City name; omit for the household's location"}}, "required": []}}}
</tools>

For each function call, return a json object with function name and arguments within <tool_call></tool_call> XML tags:
<tool_call>
{"name": "<function-name>", "arguments": {"<arg-name>": "<arg-value>"}, "failure_message": "<brief spoken response if this call fails>"}
</tool_call>
```

With native tool calling: empty.

**`tools_compact`**: as `tools`, with `"properties": {"city": {"type": "string"}}` (no per-parameter `description`).

**`date_keys`**:

```text
DT_KEYS: today|tomorrow|yesterday|this_weekend|last_weekend|…
CRITICAL — resolved_datetimes: You MUST use the symbolic key strings from DT_KEYS (e.g., "today", "tomorrow", "this_weekend", "last_weekend"). NEVER resolve dates to ISO timestamps like "2026-03-07T05:00:00Z" — the server handles resolution. If the user omits a date, pass ["today"].
```

**`tool_guidance`**:

```text
Tool Guidance:
- For timers, pass the duration in seconds.
- Use search_web only for facts that change.
```

**`direct_answers`**:

```text
Direct Answer Policy:
- MUST call tools for: control_device, get_weather, set_timer
- Direct answers allowed for: answer_question
- BE BRIEF. Your reply is read aloud. Answer in ONE short sentence and STOP. …
```

**`agents`**:

```text
Home Assistant Devices:
Floors: Downstairs (Kitchen, Living Room), Upstairs (Bedroom)
Floor commands (e.g., 'turn off lights downstairs') → call control_device for EACH device in EVERY area on that floor.

Kitchen:
- light.kitchen_ceiling: Kitchen Ceiling (off)

Living Room:
- light.lamp: Lamp (on)

Use control_device to control devices. Use get_device_status to check state. Copy entity_id EXACTLY as shown above.
When the user doesn't specify a room, prefer devices in 'kitchen' (the current room).
```

**`rooms`**:

```text
Room Hierarchy:
Downstairs → Kitchen, Living Room
When user references a room with sub-rooms, target ALL devices in that room AND its sub-rooms.
```

**`trim`**: no value; see §3.

## 5. LLM settings

Two settings carry what used to be provider quirks.

| setting | values | means |
| --- | --- | --- |
| `llm.tool_calling` | `auto` \| `native` \| `text` | `native`: tools go through the API's `tools` field, and `{{tools}}` / `{{tools_compact}}` render empty. `text`: no API tools; the prompt carries them through `{{tools}}` or `{{tools_compact}}`, and the server parses `<tool_call>` blocks from the reply |
| `llm.thinking_switch` | `auto` \| `suffix` \| `none` | `suffix`: the server ends the user turn with `"\n/think"` when thinking is on for the household (`model.include_thinking`) and `"\n/no_think"` when it is off. `none`: no suffix |

**Precedence** (the first that gives a value wins):

1. The superuser setting, when it is not `auto`.
2. The household prompt's own values (§6), when it has them.
3. The chat template: the template llama-server renders with, which is the catalog's pinned file when the catalog entry has one, else the GGUF's `tokenizer.chat_template`; for transformers/MLX models, `tokenizer_config.json`'s `chat_template` or `chat_template.jinja`.
   - A template that references `tools` → `native`.
   - A template that has the `enable_thinking` switch → `suffix`.
   - A remote label (a remote OpenAI-compatible model with no template to read) → `native` with no suffix (`none`).
4. Otherwise `text` and `none`.

The two settings resolve independently: a superuser `tool_calling` of `text` with `thinking_switch` `auto` still reads the thinking switch from the prompt or the template.

**Not settings:**

- Cleanup of the reply (stripping `<think>…</think>`, unwrapping `<message>`, parsing `<tool_call>` on the text path) is always on, for every model.
- The response format and force-tools are not settings. They follow from `llm.tool_calling`, not from a separate switch.

## 6. Prompt packages

A Pantry package may carry a prompt: the prompt file (§1) and, in the manifest, its name, its description and two optional LLM values:

| manifest key | values | when absent |
| --- | --- | --- |
| `tool_calling` | `native` \| `text` | the household falls through to the chat template (§5, step 3) |
| `thinking_switch` | `suffix` \| `none` | as above |

`auto` is not a manifest value. Installing the package calls the import endpoint (§9) with the file as the body and these values as query parameters. The values travel with the prompt: they are stored with it and become the household prompt's values (§5, step 2).

## 7. Invalid prompts

A prompt is invalid when it is:

1. empty or whitespace-only;
2. using an unknown name (`{{ Room }}`, `{{ calendar }}`, `{{ }}`);
3. holding an unclosed `{{` (no `}}` before the end of its line);
4. over 32 KiB;
5. not UTF-8.

The error names the line and the variable:

```json
{"detail": "unknown variable \"calendar\"", "line": 12, "variable": "calendar"}
```

- `detail`: a sentence for the admin UI.
- `line`: 1-based line of the problem; `null` for rules 1, 4 and 5, which concern the whole file.
- `variable`: the name as written for rule 2; `null` otherwise.

A save and an import reject an invalid prompt with this body and store nothing. The status code is set by the API piece.

## 8. Per household

- A household has **one active prompt**: a built-in (§10) by name, or an imported/edited text stored for that household.
- **Edit:** the admin UI edits the text, starting from the active prompt (a built-in's text when the household uses one). A save validates (§7) and stores the text as the household's prompt.
- **Reset:** drops the stored text; the household goes back to the install default.
- **The prompt's LLM values:** a stored prompt keeps its own `tool_calling` and `thinking_switch` (each optional, as in §6), editable alongside the text. A built-in has none, so the chat template decides.
- **The install default:** a household with no stored prompt uses the built-in named by `llm.prompt_provider` (the key keeps its name). A local install takes it from the model's catalog entry `prompt_provider`.
- **Fallback:** a stored prompt that stops validating (a variable removed in a later version, say) is not sent. The household uses the install default, the server logs the error (§7 shape), and the admin UI shows it on the prompt page until the prompt is fixed or reset.
- **When edits apply:** from the next conversation. A conversation keeps the system prompt it was warmed with (the prefix cache depends on it).

## 9. Import

```text
POST /api/v0/households/{id}/prompt/import?name=<name>&tool_calling=<native|text>&thinking_switch=<suffix|none>
Content-Type: text/plain

<the prompt file, verbatim>
```

- The body is the prompt (§1). `name` labels it; `tool_calling` and `thinking_switch` are optional (§6).
- The same validation as a save (§7). A valid import becomes the household's active prompt.
- A bad query value (`tool_calling=auto`, `thinking_switch=yes`, a missing or empty `name`) → 400.

## 10. Built-ins

Four prompts are embedded in the server as `builtin/*.txt`. They keep the provider names exactly:

| built-in | was | catalog models that default to it (`prompt_provider`) |
| --- | --- | --- |
| `Qwen3_14B_Compressed` | the prod text prompt | `qwen3-14b`, `qwen3.8-27b` (the prod 27B) |
| `Qwen3_8B_Compressed` | the dev text prompt | `qwen3-4b`, `qwen3-8b` |
| `Qwen3_5_9B_Compressed` | the native-tools prompt | `qwen3.5-9b` |
| `ChatGPTOpenAI` | the e2e prompt (remote OpenAI) | none (set by hand for a remote model) |

Native or text tool calling is not a different built-in: it is `llm.tool_calling` (§5). A built-in written for text tool calling uses `{{tools}}` or `{{tools_compact}}`, which render empty when the model is native, so the same text serves both. The catalog's pinned templates (`qwen3.jinja`, `qwen3.5.jinja`, `qwen3.8.jinja`) all reference `tools` and have `enable_thinking`, so with `auto` and a built-in every catalog model, the prod 27B included, goes native with the suffix. The catalog's `prompt_provider` defaults are unchanged by this contract.
