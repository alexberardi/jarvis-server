# End-to-end regression (`scripts/e2e`)

`scripts/e2e` runs the regression suites against sandboxes it brings up itself, writes a report,
and takes the sandboxes down again. It is run on demand (the user, or the loop's trigger, E2E
9/9). Why it is built this way: [`../decisions/2026-10-17-e2e-runner.md`](../decisions/2026-10-17-e2e-runner.md).

```sh
scripts/e2e web                       # the admin SPA suite
scripts/e2e                           # every suite there is (all); mobile is "not_run" until #144
scripts/e2e web --filter 'inbox'      # only tests whose title matches (Playwright --grep)
scripts/e2e web --keep                # leave the sandboxes up to poke at afterwards
scripts/e2e web --name loop-142       # pick the sandbox name (default e2e-<run>); never default
```

Exit status: 0 when every test passed, 1 when one failed or a suite could not run, 2 for
`scripts/e2e mobile` (not built yet: E2E 4/9, loop item #144).

## What it brings up

- `<name>`: `scripts/sandbox up --profile rich --llm scripted --ui`: the seeded Avery household,
  a login per role (`SBX_EMAIL`, `SBX_EMAIL_ADMIN`, `_POWER`, `_MEMBER`, one password), the
  seeded nodes, and the scripted model (`docs/decisions/2026-10-17-scripted-llm.md`), built from
  this checkout with the SPA.
- `<name>-setup`: `scripts/sandbox up --profile empty`: no seeding, no superuser, first-run
  setup still to do. `sandbox.env` has `SBX_SETUP_TOKEN` (the wizard link is
  `$SBX_ADMIN/setup#token=$SBX_SETUP_TOKEN`). Only the setup wizard's test uses it.

Both are purged afterwards (`scripts/sandbox down --purge`) unless `--keep`. A run takes about
2 minutes on the dev box (build, seed, ~45 browser tests).

## The report

Everything a run leaves is under `.e2e/<run>/` (gitignored), `<run>` = `YYYYMMDD-HHMMSS-xxxxxx`:

- `report.md`: verdict, counts, a table per test, and per failure the error, the screenshot
  inline, the trace and the page's console errors.
- `report.json`: the same for programs (the loop's trigger, E2E 9/9, and the coverage gate
  read it). Keep its shape stable: add fields, don't rename or remove them.
- `web/results.json`: the web suite's own records (`e2e/web/reporter.ts`), merged into
  `report.json` by `scripts/lib/e2e_report.py`. A new suite writes `<suite>/results.json` in the
  same shape and `scripts/e2e` lists it.
- `web/artifacts/<test>/`: per failed test `test-failed-N.png` and `trace.zip`
  (`cd e2e/web && npx playwright show-trace <path>`).
- `web.log` (Playwright's own output), `sandbox-<name>.log` (sandbox start-up).

`report.json`, schema 1:

```json
{
  "schema": 1,
  "run": "20261017-225237-6caf1c",
  "status": "passed | failed",
  "started_at": "2026-10-17T22:52:37-04:00",
  "finished_at": "2026-10-17T22:54:16-04:00",
  "sandbox": "e2e-20261017-225237-6caf1c",
  "filter": null,
  "git": {"sha": "…", "branch": "…", "dirty": false},
  "counts": {"total": 43, "passed": 43, "failed": 0, "skipped": 0},
  "suites": {
    "web": {"status": "passed | failed | error", "exit_code": 0, "error": null, "duration_ms": 87400},
    "mobile": {"status": "not_run", "exit_code": null, "error": "not built yet", "duration_ms": 0}
  },
  "tests": [
    {
      "suite": "web",
      "id": "…",
      "title": "inbox.spec.ts › deleting from the detail page removes the item",
      "file": "tests/inbox.spec.ts",
      "line": 32,
      "status": "passed | failed | timedOut | skipped | interrupted",
      "duration_ms": 600,
      "error": null,
      "screenshots": ["web/artifacts/…/test-failed-1.png"],
      "trace": "web/artifacts/…/trace.zip",
      "console_errors": ["/inbox/…: Failed to load resource: … 404 (Not Found)"]
    }
  ]
}
```

Paths are relative to `.e2e/<run>/`. A suite with `status: error` did not run (its `error` says
why, e.g. a sandbox that did not come up); the run is then `failed` too. `console_errors` are
recorded, not failures: a refused password or a deleted item's 404 logs one on purpose.

## The web suite (`e2e/web/`)

Playwright Test in TypeScript, pinned to the Playwright version of `tools/screenshots` so the
cached Chromium is shared. One worker: the suite shares one sandbox, one active model script and
one auth rate limit.

| file | covers |
|---|---|
| `setup-wizard.spec.ts` | first-run setup on the empty sandbox: every page leads to the wizard; the link's token; superuser created; every model step skipped (nothing downloaded); the token is spent; the new superuser signs in |
| `auth.spec.ts` | anonymous → login; superuser sign-in; wrong password refused, then the right one; log out (server session revoked, app closed); temporary password forces a change, which then sticks |
| `account.spec.ts` | display name saved (server and page, survives reload); change password (form checks, old one refused, new one works) |
| `chat.spec.ts` | scripted reply shown; a tool call's effect (`remember` saves a memory, checked through the API); device picker (the turn goes through the picked node, remembered across reload); a photo with a vision model (sent, shown, in the model's request) and no photo button without one |
| `inbox.spec.ts` | list with unread marks and the sidebar badge; detail renders markdown and reads it (server and badge); delete from the detail; bulk mark read and delete |
| `addons.spec.ts` | the page's sections; a paired desktop agent is listed; signing it out removes it on the server |
| `household.spec.ts` | the switcher appears with two households; the inbox and the chat devices follow the pick, across a reload; no switcher with one |
| `roles.spec.ts` | the role matrix from `routes.tsx`/`Sidebar.tsx`: superuser sees and opens every admin page; admin, power user and member see Chat/Add-ons/Inbox only, get the notice on every admin URL, and the server answers the admin pages' calls with 403 |

### Writing a test

- Import `test` and `expect` from `helpers/fixtures.ts` (it records console errors).
- Assert behaviour: the data changed on the server (`helpers/api.ts`) and the page shows it, not
  only that a page loaded.
- Signed in as a seeded role: `test.use({ storageState: stateFile('member') })` (written once by
  `global-setup.ts`, through the login form). API calls as that role: `tokenOf('member')`.
- Anything that changes a user or needs data nobody else sees makes its own throwaway user:
  `const u = await newUser('prefix')` (own household, its admin), then `await asUser(u)` for a
  signed-in page. Tests must not depend on order or on another test's leftovers.
- Login, register and refresh share one per-IP limit of 30 a minute and every test comes from
  127.0.0.1. `asUser` starts from the registration's session instead of the form; the form is
  `auth.spec.ts`'s. A 429 waits the minute out once (the test timeout allows it).
- Chat answers come from `e2e/llm/web.json` (`useScript` switches it); add a rule there for a
  new conversation, and match on the user's words.
- Run one file against a kept sandbox while writing it:
  `scripts/e2e web --keep --filter 'my test'`, then
  `cd e2e/web && (source ../../.sandbox/<name>/sandbox.env; E2E_OUT=/tmp/x npx playwright test -g 'my test')`
  (the setup test also needs `E2E_SETUP_ADMIN` and `E2E_SETUP_TOKEN` from `<name>-setup`).
