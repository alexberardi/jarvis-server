# Start enrollment capture at the cue and retry rejected takes

- **Date:** 2026-10-29 (user report; implementation correction)

The enrollment cue says "starting now", so the recording must keep speech from
that point. The inherited 1.5-second post-cue skip discarded the beginning of the
prompt in both enrollment and verification. Remove that skip from voice-profile
takes; the generic recorder still supports a preroll for other callers. A pipeline
regression checks the uploaded audio, including speech immediately after the cue.

The reported failure was `too_little_speech`: 1.8 seconds detected against a
3-second minimum in an 8-second take, despite the user reading the full prompt.
The timing defect can contribute to that result. The original audio was not
retained, so it does not establish how much of this rejection came from timing
versus microphone level or the energy-based speech detector. The quality
thresholds and recording duration remain unchanged.

Two diagnostic questions must be answerable on another attempt: did the recorder
produce the requested duration, and was its input quiet? Each completed take logs
its actual duration and raw PCM RMS, correlated by request ID. It does not log or
retain raw audio. Pair these fields with the upload's existing rejection detail.

Mobile displays the structured quality reason and measured/required speech time
when provided by the server. A rejected take stays in its wizard position, with
an explicit retry of that prompt on that device. Accepted samples are kept; a
member can also finish without retrying. Unknown or older response shapes get
plain-language guidance. This extends
[household voice enrollment](2026-10-29-household-voice-enrollment.md).
