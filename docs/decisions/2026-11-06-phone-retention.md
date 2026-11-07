# Phone call recordings, transcripts and details expire

- **Date:** 2026-11-06 (agent; the user asked for the details and the `audio_available` fix
  in review of PR #577)

`phone_calls.audio_retention_days` now does what its description promises. It supersedes
D42 ("no retention job, `audio_retention_days` stays inert") in
[the phone doc](../cc/11-phone.md). That doc's question 8 offered this as option (b), as a
daily job; it runs hourly here.

An hourly sweep (`cc.phone_retention`, `internal/modules/cc/phone/retention.go`) finds every
call that ended more than N days ago, N being the call's household's value (default 30), and
deletes the recordings, transcripts and the personal details a call carried:

- **Outbound** (`cc_phone_call_sessions`):
  - the recording blob and `audio_object_key`, with the outcome's `audio_available` set to false;
  - `transcript_json`;
  - `details`, the plan's brief: what the user asked for plus the call context (insurance
    numbers, callback numbers) it was allowed to use;
  - `node_context_json`, the give-if-asked fields the household's commands supplied.

  `details` is nullable, so it becomes NULL.
- **Inbound** (`cc_phone_inbound_calls`): the recording blob and `audio_object_key`. Inbound
  calls keep no transcript and carry none of the household's call context.

**Kept, and why:**

- The row itself, with `duration_seconds`, `created_at`/`ended_at` and the state, because
  `calls_per_day` and `monthly_minutes_cap` count past calls.
- `goal`: the short purpose of the call ("order a pizza"), the user's history of what was done.
  The column is NOT NULL.
- `contact_name`, `contact_address`, `resolved_number`, `dialed_number`: the business that was
  called, copied from the household's phonebook. This is not personal call context, and it
  stays in the phonebook anyway.
- `outcome_json`: the summary and the facts the call reported.
- Inbound `caller_number`, `caller_id_name`, `caller_name`, `reason`, `message` and `summary`:
  the record of who called and the message Jarvis took. They are the substance of the
  notification card, which shows the same text, so clearing the row would not remove them
  from the user's view.
- The inbox cards. A card is the user's own record of what the call achieved and was posted to
  them. It never carried the transcript or the audio, only the summary and the blob's key in
  its metadata, which then points at nothing (no server route or client serves audio from that
  key). Cards are not updated: a card has no link back from the call row, and finding it means
  querying the notifications module's table by metadata. An inbound card's stale
  `audio_available` is not read by any client.

**0 or negative keeps nothing once the call has ended.** Its recording, transcript and details
are deleted at the next sweep, within an hour. The description says "days to keep", and 0 days
means none. Reading it as "forever" would silently break the privacy promise for anyone who set
0 to mean "don't keep it". The mobile app has no special handling for the value. Values are not
range-checked on write, so mobile's writes keep working.

**Failures:** a blob that fails to delete keeps its key, and `audio_available` stays true, so
the next sweep retries it. The transcript and details are cleared regardless. A database error
ends the pass and fails the job; the next hourly run retries everything still due. Only calls
that have ended (`ended_at` set, any terminal state, including expired and declined drafts)
are touched; a live call never is.
