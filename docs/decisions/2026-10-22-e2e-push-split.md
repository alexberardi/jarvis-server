# #431 split: the mobile push assertion and the overnight lane leave jarvis-server

- **Date:** 2026-10-22 (user, answering #431's triage question)

#431 ("re-open #150: the fake relay passes on web, but the mobile flow never asserts a push and
real FCM has never run") is split into three items, one per repo, and #431 closes as their
parent:

- **jarvis-node-mobile:** a `.maestro/push.yaml` flow that makes the seeded node post an alert
  and asserts a push for `PUSH_TOKEN` on `FAKE_RELAY` (the variables `scripts/e2e mobile`
  already hands the flows).
- **jarvis-loop:** an overnight `scripts/e2e mobile --real-push` lane next to the e2e-all, voice
  and pipeline lanes. It reports *skipped*, not failed, until the harness push.env exists.
- **jarvis-server (this repo):** `push.env.example` and the "what the user must provide"
  checklist in [`../dev/e2e.md`](../dev/e2e.md#push-notifications).

The lane stays skipped until the user creates the push.env file; nothing here is a stub of the
other two items.
