# The text path's continue re-enters the loop while "open X and type Y" owes the typing

- **Date:** 2026-10-16 (agent, loop item #56; held for the user's review)

D23 ([02-tool-loop.md](../cc/02-tool-loop.md) Q4) kept the text path's continue as one
formatting call with no tools. Prod's qwen3.8-27b (provider `Qwen3_14B_Compressed`) runs on the
text path, so after `desktop_open` the continue could only answer "Done.": the
unfinished-request nudge (#159) lives in the loop and never ran there. All five "open X and
type Y" pipeline scenarios failed on prod's model.

Narrow exception, not option (b): when the turn's utterance is "open X and type Y",
`desktop_type_text` is offered and it has not been called this turn, the text path's continue
runs the tool loop (at most 3 iterations) instead of the formatting call, and the streaming
continue falls back to the blocking one (on both paths). Every other text-path continue is
still one formatting call; general chaining waits for the prompt-provider redesign as D23 says.

Also beyond legacy: on the text path's loop, a reply that ends in a bare
`{"name": <offered tool>, "arguments": {…}}` after prose is that call (qwen3.8-27b wrote one
with no `<tool_call>` tags). `parse` and its goldens are unchanged.

Partly supersedes D23.
