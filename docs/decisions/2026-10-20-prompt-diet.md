# The prompt diet: half the tokens, measured accuracy held

- **Date:** 2026-10-20 (user, worked through 1:1 with Claude after a 16 GB MacBook Air user found jarvisd slow)

A voice node's "hello" was ~9.5–10k prompt tokens and a desktop node's ~11k: about 3.8k of system
prompt and 6–7.5k of tool schemas. On Apple Silicon that is 12 s of prompt processing for a 4B model
on an M2 Max before the first word whenever nothing is cached, and roughly three times that on an
M3 Air. The user asked for an optimized prompt, measured, without losing tool-calling accuracy.

Decided, and measured with `scripts/toolbench` (the exact captured prompt; the legacy GPU benchmark's
80-utterance corpus from `install-e2e/gpu/bench_corpus.cc.yaml`, plus the desktop toolset's examples
and each pipeline scenario's first turn):

- **No `failure_message` on every native tool.** It was ~110 chars per tool (~1k tokens on a desktop
  node). With native tool calling the model sees a failed result and speaks for it next turn;
  `takeFailureMessage` still strips one a model sends.
- **`not_for_me` is a native tool** (no parameters) with native tool calling, and the six
  not_for_me paragraphs (5.6k chars, 70% of the provider text) became two lines (`{{not_for_me}}`:
  "call not_for_me" natively, "reply with <not_for_me/> alone" in text mode). A turn whose only call
  is not_for_me takes the sentinel's silent path with the plain sentinel in the history; next to real
  calls it is dropped. The `<not_for_me/>` sentinel still works. The user judged the old rules
  ineffective; they also misfired on short commands ("press enter", "scroll down").
- **The text-tool-calling-era rules went** ("populate ALL required parameters", "call ONE function
  at a time", most of "You are a function calling AI model"); three short lines remain (don't
  invent live data or actions; profile facts from memory but actions through tools; speech-to-text
  is charitable). The date-key rule is one line, the end-of-prompt "YOUR VOICE" restatement one line.
- **The must-call list stays, as one line**, and grows by `must_call` (a Jarvis extension key,
  stripped before the model): without it the 9B answered arithmetic, reminders and jokes itself.
  The "direct answers allowed" list and the padding rules went.
- **Tool descriptions are one sentence of purpose plus the boundary against the nearest tool**
  (clock time → reminder vs. duration → set_timer; window vs. screen; browser_fill vs.
  desktop_type_text), parameter descriptions a few words. Examples stay in the stripped `examples`
  key. The 4B's alarm failures came from the reminder description losing exactly that boundary.
- **The G1 prompt goldens are now the Go builder's output.** They were exported from the legacy
  Python stack for parity; this change diverges on purpose. `JARVIS_GOLDEN_DUMP` +
  `tools/golden/rebaseline.py` re-baseline them after a reviewed prompt change.

Results on the captured prompts (experiment, before the port; the final numbers are in the PR):

| | before | after |
|---|---|---|
| 9B, legacy corpus | 98.8 % · 9,685 tok | 98.8 % · 4,790 tok |
| 4B (Qwen3-4B Q4_K_M), legacy corpus | 90.0 % · 9,484 tok | 100 % · 4,619 tok |
| 9B, hard scenarios (tool / +args) | 82.6 / 72.5 % | 91.3 / 79.7 % |
| 4B, hard scenarios (tool / +args) | 81.2 / 68.8 % | 88.4 / 77.5 % |

Cold time to the first tool call on the MBP (M2 Max, 4B): 12.2 s → ~5 s; warm ~0.5 s either way.
Keeping the cache warm (a live slot background calls can't evict, saving the slot across engine
restarts, warming at engine start) is the follow-up that makes cold turns rare.

Supersedes the byte-parity goal for the built-in prompt texts and sections (PLAN Phase 5's "byte-exact
prompts"); `docs/cc/prompt-contract.md` describes the new sections.
