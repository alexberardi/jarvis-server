# Node settings verification

Implementation: [NODE-SETTINGS.md](NODE-SETTINGS.md). Verified on Linux using
disposable agents and scripted jarvisd sandboxes; no installed agent or production writes.

## Automated checks

- `scripts/check --all --base main`: all green, including the full Go race suite,
  Linux/macOS/Windows vet, admin lint/typechecking and 72 UI test files (532 tests).
  The sandbox build compiles the admin bundle; E2E TypeScript checking also passes.
- Shared schema tests reject secret, transport, identity and consent keys, malformed
  types and out-of-range values. Inverting the unsupported-key condition makes the
  secret-exclusion tests fail; the original implementation was restored.
- Agent tests cover persistence before live tuning, persistence failure, unsupported
  controls, command-line overrides, startup handoff, concurrent process writes and
  preservation of unknown settings. Audio tests exercise saved preferences and mixer
  failures with fakes.
- CC integration tests verify household isolation, request/node binding and removal
  of secret fields and arbitrary device error messages. Existing encrypted-settings
  and mobile update tests remain in the suite.
- Admin component tests cover edited-key payloads, failure preservation, offline
  nodes, read-only fields and legacy updates with blank current values and explicit
  unconfirmed status.
- `scripts/e2e web --name node-settings-e2e --filter 'node settings save and reload' --keep`
  passes for both desktop and Pi profiles using real headless Go agents paired
  without K2. The test edits follow-up duration, saves and reloads it.
  Final report and screenshots: `.e2e/20261101-224917-9c60ae/report.md`.
- `JARVIS_SETTINGS_BROWSER=1 go test ./cmd/jarvis-agent -run '^TestLocalSettingsBrowser$'`
  passes against the protected local HTTP page and a disposable settings file:
  chime/follow-up/desktop saves, reload, invalid audio-device rejection, error recovery,
  both themes at 390/820/1440 widths, no page errors or horizontal overflow.

Use the toolchain and cache configuration in the repository guide. Browser integration
requires `npm ci` in `e2e/web` and its Playwright Chromium installation.

## UI audit

Scope: NodeSettings, NodesPage, AddonsPage, and the local Settings section. Nodes is
the existing superuser page; Add-ons uses the existing member-accessible page.
The editor uses the shared Dialog, Field, Switch, Select and Button components.

Rendered checks: `scripts/screenshots --sandbox node-settings-e2e --pages nodes,addons`
and `--user member --pages addons`, each at phone/tablet/desktop in light/dark.
All 18 page captures report zero console errors, failed requests, HTTP errors and
horizontal overflow. The E2E reports retain modal screenshots; the local browser
fixture accepts `JARVIS_SETTINGS_SHOTS` for its six screenshots.

Impeccable audit score: accessibility 3/4, performance 3/4, theming 4/4,
responsive layout 4/4, implementation integrity 4/4: **18/20**. No remaining
blocking findings in the verified scope. Fixed a phone device row that squeezed
the device name, grouped desktop options together, and reused the local page's
44px checkbox-label rows. Inputs are labeled, help is associated, errors and saves
are announced, drafts survive failures, and existing dialog focus/escape behavior
is retained. No UI dependencies or new motion were added.

Loading/empty states, keyboard focus and hover styles were inspected in code;
screen-reader use, measured interaction performance, physical Pi LEDs/speakers,
native macOS/Windows execution, and a live Python node were **not verified**.
The Python compatibility mode uses its existing command without claiming readback
or confirmation. Windows has no voice controls until its voice runtime supports them.

## Review findings and release boundary

Adversarial review findings were addressed: save/mixer errors, serialized
configuration changes, cross-process writes, startup handoff, platform capability
filtering and explicit command-line overrides. A pre-existing installer test
used the same account for its account-mismatch case; its fixture now uses a different
account while retaining the rejection assertion. The node-simulator launcher also
needed its shell function moved outside `env` to run at all.

New API registrations require user review under the owning repository's merge
policy. No merge, release, install or production update is part of this verification.
