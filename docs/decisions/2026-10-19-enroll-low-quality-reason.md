# Enrollment low_quality carries its reason; the 3.0 s gate stays

- **Date:** 2026-10-19 (build agent, item #293; a product call the user can overrule in review)

First-time node-mic enrollment failed for normal speech: takes measured 2.3 s and 2.8 s of speech
against the 3.0 s D37 gate, and mobile showed a bare `low_quality`, because the V9 relay replaced the
node's `upload_failed` with `error:"low_quality"` and dropped the reason M4 had computed.

- The D37 result now carries `reason`, `speech_seconds`, `detail` and (for `too_little_speech`)
  `min_speech_seconds` everywhere it appears: the stt 422, the M4 422 and the V9→V10 relay
  (`stt.LowQualityError.Wire`). The fields are additive; `error` stays `"low_quality"`, so older
  mobile builds behave as before. This amends D37's "no mobile change"
  ([06](../cc/06-media-and-voice-identity.md)): mobile may now show the reason and retry one take.
- **The gate stays at 3.0 s.** `SpeechSeconds` counts only voiced 20 ms frames, so 3.0 s is about
  4 s of talking; the setting's description now says so. The fix for short takes is on the phone:
  prompts written to take ~4-5 s to read and "say the whole sentence at a normal pace"
  (jarvis-node-mobile follow-up). D33 found ERes2Net's EER flat between 1.5 s and 3 s, so lowering
  the default to 2.0-2.5 s is defensible if longer prompts are not enough; it is per household
  (`voice.enroll_min_speech_seconds`) meanwhile.
