# App names never go into the LLM prompt

- **Date:** 2026-10-14
- **Moved from** the STATUS.md decision log on 2026-10-14.

**App names never go into the LLM prompt** (user). The desktop agent resolves misheard app names itself (spelling, then a sound-alike key) in `desktop_open` and dictation, and the model looks names up on demand (`desktop_list_apps {query}` → `closest`, `best_match`); tool descriptions stay short. Biasing whisper with the computer's app names is a separate input and is allowed, bounded ([agent/DESIGN.md §14](../agent/DESIGN.md) "Launching by a misheard name").
