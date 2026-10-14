# Follow-up gate: "is this speech for me?"

- **Date:** 2026-10-10
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Follow-up gate: "is this speech for me?"** ([agent/go-pi-node.md §3.10](../agent/go-pi-node.md)). jarvisd compares each follow-up's voice with the wake's (ERes2Net, no identification, in memory) and reads the node's `level_vs_wake_db`; a different voice that is also ≥10 dB quieter is dropped before the model, weaker evidence becomes one hint line and skips the not_for_me double-check. Any rejected follow-up is silent, uncommitted and answers `keep_listening` (≤ 3 in a row), so the Go agent's window grew to 8/6/5 s with 1.2 s end-of-answer silence and 10 s answers. Settings `voice.follow_up_gate` (on), `_voice_mismatch` 0.25, `_voice_match` 0.40, `_quiet_db` −10, `_max_ignored` 3. Eval: 0/23 genuine rejected (as today), room speech 12/12 rejected (today 6/12); same-voice-not-for-Jarvis stays a model call (1/5).
