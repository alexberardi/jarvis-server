# Dictation: misheard app names, and failures the model can't call a success

- **Date:** 2026-10-13
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Dictation: misheard app names, and failures the model can't call a success** ([agent/DESIGN.md §14](../agent/DESIGN.md) "Misheard app names"). Real case: "type into omawrite …" heard as "Omerite" → `window_not_found` → a local 9B said "Done. I've typed that into Omerite." winmatch gains a last fuzzy tier (Damerau-Levenshtein ≤ 2 and ≤ 30 % on compacted names, ≥ 5 letters, exactly one app near, else `ambiguous_window`; reported as `matched_app`/`fuzzy_match`/`heard_as`); dictation failures say "Nothing was typed: … Tell the user …", the island shows them, the agent logs `error_code`; jarvisd adds a transient `[TOOLS_FAILED]` note when every result of a round failed, and continue / `mqtt_tool_*` trace spans record each result's `success`/`error_code`. Verified with unit tests and a nested headless sway (foot `--app-id omawrite`).
