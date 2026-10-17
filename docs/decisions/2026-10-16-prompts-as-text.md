# Prompts are text with variables, not providers

- **Date:** 2026-10-16 (user, plan P2)

The system prompt becomes a plain text file with `{{ name }}` variables, filled in by the
server. No provider classes, no interface, no registry. The contract is
[../cc/prompt-contract.md](../cc/prompt-contract.md). What it settles:

- **Text and variables.** A prompt is UTF-8 text, at most 32 KiB, sent verbatim apart from its
  variables. The variables are a fixed list (room, voice mode, persona, tools, date keys,
  devices, rooms and the rest in the contract's §4); an unknown name, an unclosed `{{`, an empty
  file, an oversized file or bad UTF-8 is rejected with `{detail, line, variable}`. No escapes,
  conditionals or loops, so a prompt stays readable and editable by a household admin.
- **Section, inline and trim.** Inline values go in place. A section renders as `"\n\n" + value`
  or nothing, so an empty section leaves no stray blank lines. `{{trim}}` is Python's `rstrip` of
  the output so far. Values are never re-scanned. The `<person_view>` tail stays server-owned,
  appended as `"\n" + section`.
- **Quirks become LLM settings.** What differed between providers besides the text is now two
  settings: `llm.tool_calling` (native or text) and `llm.thinking_switch` (`/think` / `/no_think`
  suffix or none). Their `auto` value is read from the chat template llama-server actually
  renders (`tools` → native, `enable_thinking` → suffix), and both are editable.
- **Precedence:** the superuser setting, then the household prompt's own values, then the chat
  template. A remote label is native with no suffix; anything else is text with no suffix.
- **Prompts carry their own LLM values.** A stored or imported prompt may set `tool_calling` and
  `thinking_switch`, so a prompt written for text tool calling keeps working on a model whose
  template would pick native. Pantry packages carry them in the manifest.
- **The prod 27B goes native.** Its template references `tools`, so with `auto` it uses the
  API's tools; the `Qwen3_14B_Compressed` text renders `{{tools}}` empty there. The catalog's
  `prompt_provider` defaults don't change.
- **Cleanup is always on.** Stripping `<think>`, unwrapping `<message>` and parsing `<tool_call>`
  run for every model; they are not a setting. Response format and force-tools aren't settings
  either.
- **One prompt per household:** a built-in by name (the install default, `llm.prompt_provider`)
  or an edited/imported text, with reset back to the default and a fallback to it when a stored
  prompt stops validating. Edits apply from the next conversation.
- **Import format:** `POST /api/v0/households/{id}/prompt/import?name=&tool_calling=&thinking_switch=`
  with the prompt as a `text/plain` body, validated like a save; a bad query value is a 400.

Why: every new model meant a new Go provider, and the provider design was always going to be
reworked after the byte-exact port. Text a household can edit, plus two settings read from the
model itself, covers what the four providers did without code per model.

This supersedes, for the prompt text, D22's "internal parameterised builder" in
[../cc/03-prompts-and-dates.md](../cc/03-prompts-and-dates.md) §0 and D11/D12's provider
registry in [../cc/02-tool-loop.md](../cc/02-tool-loop.md) (`llm.prompt_provider` remains, as
the name of the default built-in). Their history stays as written.
