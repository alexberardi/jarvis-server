---
name: add-setting
description: Add a runtime setting to a jarvisd module (a settings.Definition), read it, let a household override it, or expose it to the mobile app or the admin. Use it when behaviour should become configurable, or a constant, env var or flag should move into settings, in internal/modules/*. It does not cover the desktop agent's agent.json, which is internal/agent/settings.
---

# Adding a jarvisd setting

Framework: `internal/platform/settings` (read the package doc in `settings.go`). Each module
declares `[]settings.Definition` and gets its own table, `<module>_settings`. A read cascades
user+node+household → user → node → household → system, then `EnvFallback`, then `Default`.
There is no cache: every read sees the latest write.

## Rules (docs/cc/00-platform.md)

- **D11:** declare only keys that code reads. A defined-but-unread key gets deleted.
- **M3:** `EnvFallback` is only for secrets and URLs. Everything else gets a default.
- Keys are dotted and lowercase, prefixed by the subsystem (`cc.compaction.threshold`,
  `pantry.enabled`, `llm.live.engine`).
- Keep a legacy key's name when porting it, because mobile and nodes send it. A rename is a
  decision for the log (`llm.interface` → `llm.prompt_provider`, D11).

## 1. Declare

Next to the code that reads it, in a `xxxDefinitions()` func (cc: `compaction.go`,
`followup_gate.go`, `packages.go`, …):

```go
const settingCompactThreshold = "cc.compaction.threshold"

func compactionDefinitions() []settings.Definition {
	return []settings.Definition{
		{Key: settingCompactThreshold, Category: "conversation", Type: settings.Float, Default: 0.75,
			Validate: floatRange(0.3, 0.95),
			Description: "What it does, the range, and what a user notices. The admin shows this text."},
	}
}
```

- Types are `String`, `Int`, `Float`, `Bool` and `JSON`. Optional fields: `Options` (an enum),
  `IsSecret` (masked in responses), `RequiresReload` (the admin says a restart is needed), and
  `Validate` (its error text is shown to the writer).
- Register the func with the module's definitions: cc in `Definitions()` in
  `internal/modules/cc/cc.go` (the `slices.Concat(...)`), auth in `auth.go`, llm in the
  `Definitions` var in `llm.go`, admin in `update.go`.

## 2. Read

```go
t := m.settings.Float(ctx, settingCompactThreshold, settings.Scope{})                 // system
on := m.settings.Bool(ctx, settingPantryEnabled, settings.Scope{HouseholdID: hh})     // household cascade
```

There are typed getters (`String`, `Bool`, `Int`, `Float`), plus `Get` for the `Value` and
`GetExact` for one scope without the cascade. Read at use time, not once at start, unless the
setting is `RequiresReload`.

## 3. Household-controllable (mobile/admin per household)

- cc: add `{key: …, typ: "bool"|"int"|"string"}` to `householdControllable` in
  `internal/modules/cc/household_settings.go` (`own`: no cascade; `secret`: masked). Its
  definition is then marked `Household` automatically (`markHouseholdControllable`), and the admin
  lists and edits each household's value.
- The mobile app lives in another repo. Record what it needs in `docs/EXTERNAL-CHANGES.md` and
  in `docs/cc/13-mobile-chat-inbox-settings.md` (as #19 `pantry.enabled` did).

## 4. Test

- Unit: set it through the service and assert the behaviour:
  `e.m.settings.Set(ctx, key, value, settings.Scope{HouseholdID: hh})` (`households_test.go`, `nodes_test.go`).
  Also test `Validate` rejections.
- The legacy `/settings` routes (`router.go`: `GET/PUT /settings/{key}` on the module's
  listener) are frozen by `TestSettingsAppAuth/*` in `contract/`. A new key changes no shape.
- Live, on a sandbox (`scripts/sandbox up`, then `source` `scripts/sandbox env`):

```bash
curl -s -X PUT "$SBX_CC/settings/cc.compaction.threshold" -H "Authorization: Bearer $SBX_TOKEN" \
  -H 'Content-Type: application/json' -d '{"value": 0.6}'
curl -s "$SBX_CC/settings/cc.compaction.threshold" -H "Authorization: Bearer $SBX_TOKEN"
```

Use the owning module's listener (`$SBX_LLM` for `llm.*` and `stt.engine`, etc.).
`scripts/sandbox` sets its LLM settings this way.

## 5. Document

Add the key to the subsystem doc (`docs/cc/NN-*.md` or `docs/<module>/…`). A setting that a user
or the wizard must decide on also gets a file in `docs/decisions/`. If the wizard shows it, update
`web/admin/src/components/wizard/` and its tests (#19 privacy step).

## Gotchas

- Adding a setting needs no migration. Upgraded installs read the `Default` until someone writes
  a row ("Upgraded installs start with it off; no migration", #19).
- `Default` must have the declared type's Go shape: a `float64` for `Float`, `int64` for `Int`.
  Validators should accept `int`/`int64`/`float64`, like `floatRange` in `compaction.go`, because
  JSON numbers decode as floats.
- Secrets must be `IsSecret` and must never be logged. A household's own secret uses `own: true`
  so it never falls back to the system value (AD6 Twilio).
