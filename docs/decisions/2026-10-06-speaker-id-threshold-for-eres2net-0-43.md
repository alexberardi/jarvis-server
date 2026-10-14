# Speaker-ID threshold for ERes2Net: 0.43

- **Date:** 2026-10-06
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Speaker-ID threshold for ERes2Net: 0.43** (D33 calibration). One real speaker (user, 14 clips from the <dev-node> Seeed mic: 3 enrollment lines, 8 near + 3 far commands) vs 684 impostors (LibriSpeech 40 speakers + 28 Kokoro voices saying commands): genuine min 0.475 (far), median 0.603; impostor max 0.387 (TTS max 0.326). Zero errors anywhere in 0.40–0.45; 0.43 is the midpoint. Caveat: impostors are not same-room household members; the margin gate covers multi-member homes, and the setting stays tunable. Recording kept locally (spikes/, not in the public repo). Re-check once more household voices are enrolled.
