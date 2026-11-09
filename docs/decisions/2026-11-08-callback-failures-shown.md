# A failed card-button tap is shown: an inbox card and an error toast

- **Date:** 2026-11-08 (user)

Testing call follow-ups end to end, "Add to Calendar" on a node without iCloud credentials failed
on the node, yet the button turned into a check mark and nothing reached the inbox: legacy (and
the port) fanned a `new_notification` tap's result out to the inbox only on success, and the app
marked the chip sent as soon as the server accepted the tap. The user: "failures should display
to the user in a clear and obvious way. maybe even show an error toast with it describing the
issue".

Decided:

- **Server** (`recordCallbackResult`): a failed `new_notification` result that carries
  `context_data` posts a card too: its own `context_data.inbox` when titled (the proposable-action
  dispatcher's "Couldn't finish that" cards), else "Couldn't finish that" with the error or the
  result's `message`. A failure without `context_data` (unreachable, swept) keeps its own
  "Couldn't reach …" card or none, so nothing is posted twice. Supersedes legacy's success-only
  fan-out.
- **Mobile** (`InteractiveElementsSection`): a `new_notification` chip waits for the job, as the
  stacked result screen does: a check mark only on success; on failure an error toast with the
  reason and a chip that can be tapped again.
