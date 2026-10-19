# Who sees and changes a taught workflow and a recording

- **Date:** 2026-10-19 (build agent, plan P3 piece 26; held for the user's review)

Builds on [2026-10-19-taught-workflows.md](2026-10-19-taught-workflows.md).

- **Workflows** (`cc_taught_workflows`): the owner sees and edits theirs; when shared, every
  member of the household sees it. Only the owner or a household admin (role admin or owner) edits
  or deletes it, and an admin also sees an unshared one when they open it by id (to manage it;
  the list shows each member only their own and the shared ones). A member of another household
  gets 404, never 403, so ids don't leak.
- **Recordings** (`cc_recordings`) are their owner's alone, even to a household admin: a recording
  is what was on that person's screen. The owner lists, reads and deletes them; retention deletes
  the rest.
- **Retention** is fixed when a recording is stored (`expires_at` = upload +
  `computer.recording_retention_days`, default 30); changing the setting applies to new
  recordings. The daily sweep deletes the row unconditionally, then asks the agent to delete its
  local copy (`computer_record_delete`); an agent that is offline or doesn't have the tool yet
  keeps its copy, and its own limit (piece 29) applies there.
- Account deletion removes the member's workflows (shared ones too) and recordings.
- `POST …/workflows/{id}/run` is registered now, with its tier, and answers 501 until replay
  (piece 31) lands, so the access table is settled in one review.
