# E2E admin pages: what each page's test asserts, and what it leaves out

- **Date:** 2026-10-17 (user asked for automated E2E regression of the admin web app and the
  mobile app; design by agent, loop item #143, piece 3/9)

The `/admin` pages have one spec each in `e2e/web/tests/admin/` (the runner and its rules:
[`2026-10-17-e2e-runner.md`](2026-10-17-e2e-runner.md); the table of what each covers:
[`../dev/e2e.md`](../dev/e2e.md)). Each test acts in the page and then checks the server's data
through its API, besides what the page shows.

- **Test what the pages do, not what the item imagined.** The item asked for inviting users,
  changing roles, disabling accounts, and renaming or removing nodes. The web admin has none of
  these in v1: `AccountPage.tsx` says invites and households stay in the mobile app, and `/admin/nodes`
  only approves or denies pairings and shows signed-out nodes (sign-out lives in Add-ons). They
  are not built here. Each is a skipped test with the reason (`users.spec.ts`, `nodes.spec.ts`),
  so the report and the coverage gate (E2E 8/9) show the gap. Mobile covers invites and roles (E2E 5-6/9).
  Building them in the web admin would be a separate feature item.
- **Nothing that would end or alter the shared sandbox for good.** The sandbox's jarvisd runs
  unsupervised, so `/api/system/info` reports `restart: false`. The dashboard test checks that
  the Restart button is absent, then opens Stop's dialog and only cancels it.
  `/api/system/stop` is routed to an abort for that test, in case a bug ever clicks through.
  Updates are never applied: `/api/update/apply` is blocked for every update test, and the "update
  available" state is skipped, because it needs jarvisd restarted onto a fake release server
  (`scripts/upgrade-helper-e2e.sh`). Settings, labels and the update switch that a test changes
  are put back in a `finally`/`afterEach`.
- **No downloads.** Catalog installs are opened and closed (no install request may be sent).
  Hugging Face installs are a skipped test until there is a fake Hugging Face.
  "Assign a model to a label" uses the remote live label: nothing is installed in the sandbox,
  and the scripted model answers whatever model name it is sent. Background shares live's
  engine, so it follows.
- **Setup data per test, seed only what no API can make.** Pairings, nodes, app clients, log
  lines, traces (a chat turn), household values and throwaway users come from the test itself.
  Deactivating a user has no API, so `seed-dev.py --profile rich` adds one deactivated account,
  `former@jarvisautomation.io`. It belongs to no household (no role login can pick it) and has
  a password hash that matches nothing.
- **Pairing approvals go to a throwaway household of the superuser's.** The test deletes it
  afterwards, and its nodes go with it. The seeded household's device list, which the chat tests
  count, never changes.
- **Banners are driven from their source.** `setup.completed` switches between the "Finish
  setup" banner and the "Voice isn't ready" banner, and the test restores it. The update and GPU
  banners are checked absent, because their source states (a newer release, a GPU fault) are out of
  the sandbox's reach.
- **The auth rate limit.** The new specs register and sign in users, which ran the suite into
  the 30-a-minute limit. `helpers/login.ts` `pressSignIn` waits the minute out once for a form
  sign-in that should succeed, as `signInUI` and `call()` already did. `auth.spec.ts` uses it
  for its second attempt.
