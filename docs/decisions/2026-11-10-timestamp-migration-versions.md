# New migrations are named by timestamp, not the next counter

- **Date:** 2026-11-10 (user)

Two branches merged on the same night each took cc's next migration number, so `main` had two
`00360` files and jarvisd refused to start ("found duplicate migration version 360"). With several
agents working in parallel, picking "the next counter" will keep colliding, and each branch is green
on its own.

From now on, a new migration is named by the UTC time it was created, `YYYYMMDDHHMMSS_<name>.sql`.
goose versions must be integers, so a GUID isn't possible; a timestamp is the integer equivalent.
The runner already applies versions out of order
([2026-10-07](2026-10-07-out-of-order-migrations-are-applied.md)), so each database runs whatever
it hasn't seen, whichever branch merged first. The existing counters stay; cc's last is 00370.
`TestMigrationVersions` (`internal/platform/db/naming_test.go`) rejects a new counter and any
duplicate version, so the rule holds without anyone remembering it. The add-migration and land-pr
skills and AGENTS.md say the same.

A merge queue (or "require branches to be up to date") would also catch a combined breakage
before main; that is a repository setting left to the user.
