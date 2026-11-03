# Node settings in admin and on desktop

Status: implemented on `feat/node-settings-ui`; new API routes require user review
before merge. Production is unchanged.

## Objective

Let users edit ordinary node settings without a phone or K2: from a node's
settings page in the admin panel, and from Settings in the desktop agent's menu.
Admin must support desktop agents and regular Pi nodes, including Python nodes.
The local desktop page must remain usable when jarvisd is unreachable.

This is one capability with two interfaces to the same non-secret settings.
Values, validation and persistence must agree between those interfaces and the
existing mobile app.

## Starting point

- [command-center's config handler](../../internal/modules/cc/commands.go)
  already sends `update_node_config` with plain JSON settings after checking node
  access. It does not need K2. Its response means **sent**, not applied.
- The [Go Pi handler](../../internal/agent/pinode/pinode.go) applies ordinary
  voice settings live and persists them in `agent.json`. It rejects identity,
  endpoints, broker settings and update-consent changes on this channel.
- [Agent startup](../../cmd/jarvis-agent/main.go) currently wires that handler
  only for the Pi profile before this change. Both profiles now use it.
- The mobile app reads current values through the encrypted settings snapshot.
  That snapshot includes more than ordinary configuration. The browser must not
  gain access to its secrets merely to display voice settings.
- The desktop [local setup page](../../internal/agent/firstrun/assets/index.html)
  already provides sign-in, permissions, microphone testing and a listening
  switch. It is the entry point to extend, using its existing loopback security.
- The Go runtime currently caps follow-up listening at **3 seconds**, with zero
  disabling it. Mobile's older slider permits 15 seconds. The new UI must show
  each runtime's actual supported range, rather than silently accept a value
  the node clamps.

Sources: [agent design §16](DESIGN.md#16-node-settings-and-k2-2026-10-14),
[Pi settings behavior](go-pi-node.md#314-parity-54-node-settings-led-preview-ambient-noise-2026-10-17),
and [persisted desktop settings](../../internal/agent/settings/settings.go).

## Controls

Common voice controls in admin and desktop Settings:

| Control | Behavior |
| --- | --- |
| Wake acknowledgment audio | Toggle the existing wake feedback; label the Go chime behavior accurately, including its existing coupling to error chimes. Custom sound uploads are outside this change. |
| Wake sensitivity | Show the current threshold and explain that lower means more sensitive. |
| Automatic wake sensitivity | Offer only where the runtime supports calibration; explain the manual threshold's role when enabled. |
| Follow-up duration | Zero means off; enforce the runtime's actual maximum. |
| End-of-speech pause | Silence duration that ends the initial command. |
| Follow-up end-of-speech pause | Silence duration that ends a follow-up reply. |
| Advanced voice tuning | Silence threshold, minimum follow-up recording after speech starts, and minimum follow-up speech length. |

Additional desktop-local controls use existing settings where supported:

- Listen for the wake word; microphone and speaker names on Linux. macOS and
  Windows use their system default devices, selected in the OS settings.
- Show the on-screen indicator and status icon.
- Browser integration, dictation confirmation, and time to answer an on-screen
  list of choices.
- Existing computer-control consent and automatic-update preferences remain
  local controls with their existing semantics; the remote voice editor must
  not introduce a new way to bypass their consent rules.

Additional Pi controls in admin, when supported by the device:

- Speaker volume and mute.
- LEDs and brightness when the hardware is available.
- Maintenance restart enablement, local time and memory ceiling.

Do not expose settings that the runtime cannot actually apply. In particular,
Pi hardware and maintenance controls do not appear on desktops. New wake models,
new barge-in tuning and new audio processing behavior are outside this change.

## Read, save and compatibility requirements

1. Provide a deliberately allowlisted view of non-secret current settings,
   runtime capabilities and supported ranges. It must work without K2 and must
   never reuse a complete decrypted snapshot as its response.
2. Reuse the existing config-update path where compatible. Make the desktop
   agent accept the supported voice subset and persist it with the same local
   settings mechanism used by the desktop page.
3. Save only edited fields. Preserve unrelated settings and concurrent changes;
   refreshes must not overwrite an unsaved form.
4. Apply supported voice changes live without interrupting an active voice
   turn. For local settings that require restarting the agent, show that fact
   and offer an explicit restart action. Keep documented command-line override
   behavior and distinguish persisted values from effective values when needed.
5. Report validation, delivery, apply and persistence failures accurately. A
   successful publish is not proof the node saved the values. Reloading must
   show the node's reported values, including any normalization.
6. Regular Python Pi nodes retain their current update compatibility. Reading
   non-secret values needs explicit support in that runtime too; do not assume
   the encrypted snapshot becomes readable by the browser. Older nodes must
   show an upgrade/unavailable state, not invented current values. If updates
   are offered without readback, clearly label them as unconfirmed and send only
   the values the user explicitly edited.
7. Local desktop Settings must support both a running and stopped agent, using
   the existing protected local page and user-only control mechanism. Saving
   while stopped persists choices for the next start.

## Access and boundaries

- Always preserve node/household isolation and existing server authorization.
  Opening an editor in admin does not authorize operating another household's
  node. Hide or explain controls the signed-in user cannot use.
- Keep K2, API keys, package secrets, node credentials, URLs and broker settings
  out of the non-secret view. Preserve the encrypted mobile workflows.
- Retain the local setup page's token, origin and loopback protections. No new
  LAN listener, generic config-file editor or arbitrary file path access.
- Follow the repository's user-review hold for any new API route or change to
  access rules. This is a merge hold, not authorization to alter production.
- Use isolated sandboxes and disposable audio/desktop fixtures. Never restart
  the installed assistant or exercise the user's microphone to verify this work.
- Add no UI dependencies and make no mobile UI or production changes as part
  of this feature. If Python runtime work is required, use its own checkout,
  instructions, tests and merge policy.

## Structure and conventions

The server and Go-agent implementation belongs in `jarvis-server`:

- `internal/modules/cc/`: node config transport and result handling.
- `internal/modules/admin/`: gateway registration only if needed.
- `internal/agent/pinode/`, `settings/`, `voice/` and `cmd/jarvis-agent/`:
  validation, persistence and live application.
- `internal/agent/firstrun/`: local page and its handlers/assets.
- `web/admin/src/components/nodes/`, `pages/`, `api/`: remote editor.
- Adjacent Go/Vitest tests, `e2e/web/` and `e2e/coverage.json`: verification.

Follow the existing typed Go and React patterns; use `settings.Update` for
serialized file updates and return errors to callers. For example:

```go
err := settings.Update(path, func(s *settings.Settings) error {
    s.Chimes = &enabled
    return nil
})
```

Read the scoped guides and required design skills before editing interfaces.
Update the design's blanket statement that web has no settings view to clarify
the non-secret exception when the feature is implemented.

## Verification and commands

Write failing behavioral tests first. Cover validation boundaries, unsupported
keys, cross-household access, secret exclusion, no-K2 operation, persistence,
stopped/offline nodes, concurrent edits and live voice changes. Add component
tests for dirty fields, errors, supported controls and save confirmation.

From the server checkout:

```bash
CGO_ENABLED=0 mise exec go@1.27 -- go test ./internal/agent/pinode ./internal/agent/settings ./internal/agent/firstrun ./cmd/jarvis-agent ./internal/modules/cc ./internal/modules/admin
npm --prefix web/admin test
npm --prefix web/admin run build
scripts/check --all
scripts/sandbox up --name node-settings --llm scripted --ui
scripts/screenshots --sandbox node-settings
scripts/e2e web
```

Use the repository's browser harness for a real edit/save/reload round trip on
both forms, with light/dark and narrow-screen checks. Use fakes or disposable
agents for audio behavior. Record what was actually run; hardware validation
must not be claimed from mocks. Register any new admin page in the coverage
manifest and include the required design audit in a UI PR.

## Acceptance

- A desktop user changes wake feedback, sensitivity and follow-up duration from
  Settings without editing a file or using the mobile app; changes survive a
  restart and the form accurately reflects their application state.
- An authorized admin changes the applicable settings for a desktop or Pi node
  without provisioning K2. Other households' nodes remain inaccessible.
- The UI exposes supported desktop controls and supported Pi hardware controls
  appropriately, with honest offline and older-runtime states.
- Secret settings remain on their existing encrypted path, and existing mobile
  configuration updates continue to work.

## Review assumptions

"Wake word audio" means the existing acknowledgment/chime toggle. The desktop
page extends the local Settings entry already reachable from the tray/menu.
The current Go follow-up cap remains three seconds; changing that voice policy
would be a separate explicit decision.
