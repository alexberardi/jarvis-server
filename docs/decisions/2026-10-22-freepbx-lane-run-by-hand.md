# FreePBX L2 lane: run by hand on Thursdays, no systemd timer

- **Date:** 2026-10-22 (user, after the first real run of `scripts/freepbx-lane`)

The L2 lane ([2026-10-22-sip-l2-freepbx-lane.md](2026-10-22-sip-l2-freepbx-lane.md)) ships a
systemd user timer (`scripts/freepbx-lane timer`, Sundays 03:30). The user chose not to enable
it: the drift check is **run by hand, once a week on Thursdays**, on the dev box:

```sh
scripts/freepbx-lane weekly        # revert to the bare install, update, provision 100, the calls, diff against L1
cat ~/.cache/jarvis-harness/freepbx/runs/latest/drift.txt
cat ~/.cache/jarvis-harness/freepbx/runs/latest/calls/results.md
```

Since #490 `weekly` also places the calls and checks the lockout
([2026-10-22-sip-l2-calls-and-lockout.md](2026-10-22-sip-l2-calls-and-lockout.md)); a failed call
is filed like drift.

The agent working with the user on a Thursday reminds them (or runs it, when asked). Drift is
filed as a loop item to bring L1 back in line, as #489 was for the first run's 9 settings.

Why not the timer: it is a recurring job on the user's own machine, and `loginctl enable-linger`
(needed for it to run while logged out) keeps every user service running, not just this one.
The units stay in `scripts/freepbx-lane.d/` for anyone who wants them later.
