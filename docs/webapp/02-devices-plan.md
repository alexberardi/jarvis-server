# Member Devices implementation

The user approved the scoped spec and requested implementation on 2026-11-10.
The existing `tasks/` files belong to unfinished Simple routines work; this feature's
plan and checklist stay together here under the repo's web-app documentation.

## Contract and layout

- Optional top-level `node_id` on device import binds direct devices to a household
  node. New nullable `cc_devices.node_id` is returned in the device response. Imports
  without it preserve bindings and cannot replace bound connection fields; re-import
  cannot move a binding. Missing or
  offline bound nodes fail explicitly, with no fallback to another network.
- An accepted pairing start pins an unbound device to the chosen node atomically. User control
  follow-ups use a bounded, expiring in-memory session binding to household/device,
  node and advertised follow-up action; restart requires pairing again. SDK wire fields
  stay intact. No protocol implementation or new agent listener.
- Explicit member gateway routes retain CC authorization and strip extra credentials.
- Operate surface: existing PageHeader, searchable device rows and a single Add devices
  action; discovery and detail dialogs reuse existing focus handling and theme tokens.
  Household-keyed component lifetimes discard prompts and late results when switching.
- Only verified PIN input is submitted. Unsupported text/confirmation prompts explain
  that setup must be completed through the integration's supported surface.

## Ordered slices and checks

- [x] Persist node binding and prove import validation, immutable re-import, offline and
  cross-household behavior. Files: migration, device registry, routing, focused Go tests.
- [x] Keep pairing on its node and validate session follow-ups. Files: control handler,
  session helper/Module field, focused Go tests. Check wrong device/action, expiry and
  changed primary/heartbeat order before proceeding.
- [x] Expose member routes with tier expectations and integration access tests. Files:
  gateway, tier tests and gateway/CC integration tests. Verify every household role,
  unauthenticated callers and cross-household resources.
- [x] Build typed client/polling with bounded waits and cancellation. Verify failures,
  expiry and authorization stop conditions in Vitest.
- [x] Build member list/navigation and external-manager read-only mode. Verify list,
  empty/error states, role visibility and household switching.
- [x] Build discovery selection/import and device name/room/removal dialogs. Verify
  selection, duplicate handling, late responses and ambiguous import recovery.
- [x] Build pairing state/prompts/retry. Verify leading-zero PIN, repeated prompts,
  success versus failure, cancellation and stale household callbacks.
- [x] Add additive wire contract cases, E2E member flow and coverage registration.
  Run focused contracts on an isolated sandbox and browser tests with fake devices.
- [x] Run `scripts/check --all`, SPA build, `scripts/e2e web`, screenshots in both themes
  at all three widths, and design/code review. Update spec/decision with the built
  contract and evidence. Landing remains subject to the access-rule review hold.

Tests are written and observed failing before each behavior is implemented. Production
and real hardware are not used. Schema changes are additive; preserve older clients.
The required access review applies to landing the completed PR, not to preparing it.
