# The E2E coverage gate: every page and screen has a test, or a stated reason

- **Date:** 2026-10-18 (user asked for the gate in the E2E regression plan, piece 8/9, loop item
  #148; the format and the checks by agent)

A new admin page or mobile screen can't land without an E2E test or a reason it has none, the
way `TestEveryAPIRouteHasATier` keeps every `/api` route classified. How to use it:
[`../dev/e2e.md`](../dev/e2e.md#the-coverage-gate-e2ecoveragejson). It builds on
[`2026-10-17-e2e-runner.md`](2026-10-17-e2e-runner.md) and
[`2026-10-18-e2e-mobile-runner.md`](2026-10-18-e2e-mobile-runner.md).

- **One file, `e2e/coverage.json`, both apps.** `web` is keyed by the route's full path as the
  router has it (`/admin/traces/:id`, `/admin/*`, `/*`), `mobile` by
  `<navigator file>/<screen name>` (`RoutinesStackNavigator/ScheduleList`): screen names repeat
  across navigators (`DeviceDiscovery`, `IntegrationAuth`). Each entry is exactly one of
  `tests` (web specs under `e2e/web/tests/`), `flows` (paths in the mobile repo, `.maestro/…`),
  `manual` (why an automated test can't reach it here) or `pending` (which item adds its test).
  `pending` is separate from `manual` so "not written yet" never reads as "checked by hand";
  the report lists both. Redirect routes are routes too: they get the test that follows them.
- **The route list comes from the code, not a copy of it.** Web: a Vitest test
  (`web/admin/src/coverage.test.tsx`, so it runs in CI's `admin` job) walks the route elements
  with React Router's own `createRoutesFromChildren` over `appRoutes()` (`routes.tsx`, moved
  there out of `AppRoutes` in `App.tsx`, which renders it; `shellRoutes()` included). Mobile: jarvis-server
  can't import the mobile app, so `tools/e2emobile` reads the `<X.Screen name=… component=…>`
  registrations in `src/navigation/*.tsx` as text; a registration whose component is another
  navigator file there (Root's `Inbox` is `InboxStackNavigator`) is a container and is left
  out, since its screens are listed themselves. A screen rendered outside a navigator
  (`ForcePasswordChangeScreen`, the temporary-password gate in `RootNavigator`) is not seen.
- **Both directions fail**, as in the tiers test: a route or screen with no entry, an entry for
  one that is gone, an entry that isn't exactly one kind, and a named spec or flow file that
  doesn't exist. Only existence is checked, not that the spec visits the path: specs often get
  to a page by clicking (the trace detail), and the review stage judges whether a test covers
  what it says.
- **Web in CI, mobile in the mobile suite.** The web half needs nothing but the SPA's code. The
  mobile half needs the mobile repo, which CI doesn't check out, so it is the first test of
  `scripts/e2e mobile` (`coverage: …`), against the same commit as the APK and the flows
  (`src/navigation/` comes out of the same `git archive`). A coverage failure fails the suite
  but the flows still run. `e2emobile coverage` runs it alone. A mobile PR that adds a screen
  therefore turns the next mobile run red until jarvis-server's `coverage.json` has the entry:
  the gate sits on the server side because the entries name the server's sandbox setup.
- **report.md's Coverage section** counts, per side, the entries by kind, the missing and stale
  ones (mobile: from the suite's `mobile/coverage.json`; web: CI's), and lists the manual and
  pending ones grouped by reason. `report.json` gains `coverage` (an added field, schema 1).
- **Starting state (mobile `origin/main` bf03b79, 64 screens):** every one of the 34 web routes
  is covered; `roles.spec.ts` now also follows every old top-level path, `/admin`,
  `/admin/services` and both 404 routes. On mobile only `ScheduleList` has a flow on main
  (`schedules.yaml`); the screens #145's flows reach are `pending: "#145: <flow>"` until it
  lands (it also replaces `login.yaml` with `subflows/login.yaml`, so no entry names that file),
  the Home Assistant, pairing and discovery screens wait for E2E 7/9's fakes, and the camera,
  microphone, WebView, OAuth and fake-node-lane screens are `manual`. The rest are `pending`
  on a later mobile flows item.
- **Merged flows can't stay `pending` (2026-10-18, loop item #228).** #145's flows merged in
  jarvis-node-mobile while 51 screens still said `pending` and the gate passed: nothing tied
  an entry to the flows that exist. The gate can't know whether an item merged (no network in
  CI or the sandbox), so it uses what the checkout shows: a `pending` text that names an
  existing flow file is invalid ("list it under flows"), and so is a flow the suite runs (a
  top-level `.maestro/` file not tagged `skip`; subflows are run by the others) that no
  entry lists. A new flow therefore needs its screens in `coverage.json` before the mobile
  suite is green again, the same both-directions rule as screens. `pending` stays free text
  (no required `#N`): some screens have no item yet, and an invented one would be worse. The
  map was redone from mobile `5ee0208`'s flows and its `.maestro/COVERAGE.md`, and checked
  again at `8c09db0` (mobile #121 adds `household-watches-errands.yaml`, on HouseholdEdit): 22 covered,
  15 manual (the Store screens other than the Pantry tab are packages, manual by the user's
  2026-10-17 call), 27 pending (Home Assistant's screens on its own item split out of #154,
  `AddComputer`'s approval flow, the Devices, Routines and Inbox screens COVERAGE.md puts
  later on #145, and the CommandData-side ones it leaves out of piece 6 with no item yet).
