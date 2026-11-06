# Phone call recordings and transcripts expire

- **Date:** 2026-11-06 (agent, pending the user's review of the PR)

`phone_calls.audio_retention_days` now does what its description promised. It supersedes
D42 ("no retention job, `audio_retention_days` stays inert") in
[the phone doc](../cc/11-phone.md); option (b) of its question 8, run hourly instead of daily.

An hourly sweep (`cc.phone_retention`, `internal/modules/cc/phone/retention.go`) finds every
call that ended more than N days ago, N being the call's household's value (default 30), and:

- **Outbound** (`cc_phone_call_sessions`): deletes the recording blob, clears
  `audio_object_key` and clears `transcript_json`.
- **Inbound** (`cc_phone_inbound_calls`): deletes the recording blob and clears
  `audio_object_key`. Inbound calls keep no transcript.

**Kept:** the row itself, with `duration_seconds`, `created_at`/`ended_at` and the state, because
`calls_per_day` and `monthly_minutes_cap` count past calls; the outbound `outcome_json` (the
summary and the facts the call reported) and the inbound caller name, reason, message and
summary; and the inbox card. The card is the user's own record of what the call achieved and
was posted to them; it never carried the transcript or the audio, only the summary and the
blob's key in its metadata, which then points at nothing (no server route or client serves
audio from that key). The setting promises to remove audio and transcript, and those are what
go. Removing the summary too would be a different setting.

**0 or negative keeps nothing once the call has ended:** its recording and transcript are
deleted at the next sweep (within an hour). The description says "days to keep", and 0 days
means none; reading it as "forever" would silently break the privacy promise for anyone who
set 0 to mean "don't keep it". The mobile app has no special handling for the value. Values
are not range-checked on write, so mobile's writes keep working.

**Failures:** a blob that fails to delete keeps its key, so the next sweep retries it; the
transcript is cleared regardless. A database error ends the pass and fails the job; the next
hourly run retries everything still due. Only calls that have ended (`ended_at` set) are
touched; a live call never is.
