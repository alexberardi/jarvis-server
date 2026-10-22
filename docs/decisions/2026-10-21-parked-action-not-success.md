# A parked action is not a success

- **Date:** 2026-10-21 (agent, from #420; the user confirmed the field was never filled)

A desktop node action held for the user's yes (`dictate.Confirmer.Ask`: a browser click, a
submitting fill, Enter after typing) now returns `success: false` with `status:
awaiting_confirmation`. It used to say `success: true`, so a 3 ms `browser_fill` that filled
nothing looked done in the agent log, the trace and to the model. The island shows the question
as "Waiting for your yes: …", not as a step.

cc reads such a result as a question, not a failure: it doesn't add the "every tool failed"
note, doesn't nudge a reply that asks it as a false claim, and an empty reply falls back to the
question. The next turn (only that one, and only within the agent's 60 s) gets a hint naming the
question, its `confirmation_id` and `desktop_confirm`, because a "yes" said after a plain-text
question arrives as a new turn (`internal/modules/cc/confirm_hint.go`).

The browser extension's `fill` and `click` wait for a loading tab (≤ 5 s) and for a missing
element (≤ 2 s) before `field_not_found` / `button_not_found` (extension 0.3.4).
