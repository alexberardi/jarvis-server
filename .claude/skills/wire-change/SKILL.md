---
name: wire-change
description: Change or add something a client sees on the wire: an HTTP route, a JSON key or type, a status code, an error detail string, or an MQTT topic or payload that Pi nodes, the mobile app, the admin SPA, the desktop agent or app-to-app callers depend on. Covers the black-box contract suite (contract/, scripts/contract.sh), LEGACY-BUG and Jarvisd() branches, jarvisd-only tests, and running the suite against a local sandbox. Use it for any change to a handler's request or response shape in internal/modules/*.
---

# Changing a wire contract

`contract/` is black-box Go (build tag `contract`, never imports `internal/`). It speaks HTTP and
MQTT to a target, either the legacy Python stack or jarvisd. It freezes **shapes, not data**:
status codes, exact JSON key sets and types, and the `detail` strings clients branch on. Read
`docs/contract/README.md` (harness, fixtures, LEGACY-BUG list, per-module coverage) first.
CLAUDE.md: "A contract test is only valid once it passes against the legacy Python stack."

## Which case is it?

| Change | What to do in `contract/` |
|---|---|
| New key in an existing response | Add it as `Optional(…)` so the legacy shape still passes: `"display_name": Optional(NullOr(String)), // jarvisd addition; legacy omits it` (#81). `Obj` is an exact key set, so an unlisted key fails. |
| jarvisd deliberately differs from legacy (a bug fix, D8) | Mark it `// LEGACY-BUG: <what legacy does>` and branch with `if Jarvisd() { … } else { … }`. Add it to "LEGACY-BUGs" in the README. |
| New route with no legacy equivalent | Skip unless jarvisd: `if !Jarvisd() { t.Skip("contract: <x> is jarvisd-only (no legacy equivalent); set " + EnvImpl + "=jarvisd") }` (`cc_pairing_test.go`, `admin_test.go`). Note it as **jarvisd-only** in the README. |
| Removed or renamed key, route or status | It breaks deployed nodes and phones. Check `docs/EXTERNAL-CHANGES.md` and ask the user first. Record the decision in STATUS's decision log, and the client change in EXTERNAL-CHANGES. |
| MQTT topic or payload | `contract/cc_mqtt*.go` with the fake node (`DialMQTT`, `ExpectMQTT`: QoS 1, retain=false, exact key set). |

Matchers live in `contract/shape.go` (`Obj`, `Open`, `Optional`, `NullOr`, `ArrayOf`, `UUID`,
`TimestampUTC`, `ValidationError`, …) and `resp.go` (`ExpectStatus`, `ExpectShape`,
`ExpectError(status, detail)`). Fixtures (`fixtures.go`, `cc_helpers.go`) create throwaway
`contract-<runid>` users, nodes and apps through the real APIs, and clean them up.

Also unit-test the handler in the module's package. The contract test proves the shape, and the
unit test proves the behaviour.

## Running against a local sandbox (jarvisd)

`scripts/contract.sh` sources `$JARVIS_CONTRACT_ENV_FILE` (default `.contract.env`, untracked)
and runs `go test -tags contract -count=1 ./contract/... -v`. Point it at a sandbox (`pipeline-test`
skill: `scripts/sandbox up`):

```bash
( . .sandbox/default/sandbox.env
  echo JARVIS_CONTRACT_HOST=127.0.0.1; echo JARVIS_CONTRACT_IMPL=jarvisd
  echo JARVIS_CONTRACT_AUTH_ADMIN_TOKEN=$SBX_AUTH_ADMIN_TOKEN
  echo JARVIS_CONTRACT_CC_ADMIN_KEY=$SBX_ADMIN_API_KEY
  echo JARVIS_CONTRACT_MQTT_PORT=$((1884 + JARVIS_PORT_OFFSET))
  for l in CONFIG:7700 AUTH:7701 LOGS:7702 COMMAND_CENTER:7703 LLM:7704 WHISPER:7706 TTS:7707 NOTIFICATIONS:7712 RECIPES:7030 OCR:7031 ADMIN:7710; do
    echo "JARVIS_CONTRACT_PORT_${l%%:*}=$(( ${l#*:} + JARVIS_PORT_OFFSET ))"; done ) > /tmp/sbx.contract.env
JARVIS_CONTRACT_ENV_FILE=/tmp/sbx.contract.env scripts/contract.sh -run 'TestAuth|TestCCNode'
```

- **A sandbox is not fully green.** It has no STT or TTS voices, no embeddings and no push relay,
  so a full run (about 4 min) fails the LLM, whisper, TTS, CC media/voice/provisioning,
  `TestNotificationsNotify` and parts of `TestAdminBFF` for environment reasons (seen
  2026-10-13: 97 pass, 16 fail, 6 skip). Run the same `-run` filter on the base commit first,
  then compare the `--- FAIL` lists.
- Never point the suite at prod. `JARVIS_CONTRACT_HOST` must be a dev or sandbox target.
- Parity against legacy Python needs the MBP stack. `.contract.env.example` and the README's
  "Environment" table list the variables. If none is reachable, say in the PR that the test was
  run against jarvisd only.

## Also

- Prompt or parse behaviour with golden fixtures (`fixtures/golden/`, exporters in
  `tools/golden/`) is checked by the module's golden tests. Regenerating a golden is a
  deliberate act: say why in the commit.
- Admin SPA types (`web/admin/src/api/*.ts`) follow the BFF shape. `scripts/check` runs tsc and
  vitest when `web/admin` changed.
- Update the per-module table in `docs/contract/README.md` when coverage changes.
