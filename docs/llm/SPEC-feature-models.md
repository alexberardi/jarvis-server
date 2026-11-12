# Spec: Feature models — a model slot per built-in feature

Status: **approved 2026-11-11** (user, via interview; see
[docs/intent/feature-models.md](../intent/feature-models.md)). Extends
[2026-10-22-cloud-model-slot.md](../decisions/2026-10-22-cloud-model-slot.md), which built the
pattern and applied it to one feature.

## Objective

Every built-in feature's model is hardcoded in Go today: `Label: llm.LabelLive` or
`Label: "background"` at ~30 call sites. That is a maintainer's opinion baked into the code, and a
self-hoster whose hardware or priorities differ cannot change it.

The 2026-10-22 decision already shipped the mechanism: one `llm.SlotSetting` per feature
(`auto` | `live` | `background` | `cloud`), rendered by the admin Models page's **Feature models**
card, with `auto` meaning the feature's own default and never cloud. Exactly one feature uses it
(`computer.model`).

This change applies that pattern to the remaining built-in features. **No new mechanism, no new
slot, no new UI component, no new test fake** — new setting definitions, a shared resolver, call
sites that read it, and rows in the card's feature table.

Success is a superuser picking the slot for each built-in feature on the Models page, with every
default reproducing today's behavior.

### Why now

Code debt, not a routing need. Nothing is currently on the wrong model. The driver is removing the
maintainer's opinion from ~30 call sites and letting the user hold it instead.

### Non-goals

- **New or user-defined slots.** Three stay, permanently: two local models is the hardware ceiling
  on a realistic install ("one large model for critical paths, one fast model for voice"), and
  `cloud` covers the specialist case at no hardware cost. A fourth slot is the edge case, not the
  design.
- **Per-feature model ids.** A feature picks a slot; the slot's model is configured under Labels.
- **Per-household or per-user choice.** Slots are an install-wide resource, so household routing
  would only be a privacy toggle, and it is an access change needing its own ask. These stay
  system-wide superuser settings, as `computer.model` does.
- **A slot choice on the public API.** `NormalizeLabel`/`echoLabel` keep API callers off `cloud`;
  only settings reach it.

## Tech Stack

Go (the version in `go.mod`, currently `mise exec go@1.27`), SQLite settings store with embedded
goose migrations, React 19 + Vite admin SPA. No new dependencies.

## Commands

```
Build:       CGO_ENABLED=0 mise exec go@1.27 -- go build ./cmd/jarvisd ./cmd/jarvis-agent
Gate:        scripts/check --all
Focused Go:  mise exec go@1.27 -- go test ./internal/modules/llm/... ./internal/modules/cc/... ./internal/modules/ocr/... ./internal/modules/recipes/...
SPA:         cd web/admin && npm test && npm run lint && npm run build
Sandbox:     scripts/sandbox up --name featuremodels --llm scripted --ui
Screenshots: scripts/screenshots --sandbox featuremodels
E2E:         scripts/e2e web
```

## Project Structure

| Path | Change |
|---|---|
| `internal/modules/llm/slots.go` | `SlotSetting` gains a default; add the shared resolver |
| `internal/modules/cc/*.go`, `cc/phone/`, `cc/errands/`, `cc/servertools/` | settings registered; hardcoded labels replaced |
| `internal/modules/ocr/engines.go` | the vision pass reads its setting |
| `internal/modules/recipes/` | two legacy keys renamed with a migration; a third folded away |
| `internal/modules/recipes/migrations/` | the goose migration carrying old values over |
| `web/admin/src/components/models/features.ts` | the feature table grows to 13 |
| `web/admin/src/components/models/FeatureModelsCard.tsx` | grouping and the `Auto — {label}` copy |
| `docs/decisions/2026-11-11-feature-models.md` | the decision record |
| `docs/llm/QUESTIONS.md`, `docs/schema/recipes.md` | the answer as an LD; the renamed keys |

## Code Style

The resolver is one function in `llm`, used the same way everywhere:

```go
// FeatureSlot is the label a feature's chat requests go to: its slot setting, or fallback when
// the setting is auto (the feature's own default, which never picks cloud).
func (m *Module) FeatureSlot(ctx context.Context, key, fallback string) string {
	switch v := m.settings.String(ctx, key, settings.Scope{}); v {
	case LabelLive, LabelBackground, LabelCloud:
		return v
	default:
		return fallback
	}
}
```

Call sites change from a constant to a lookup, nothing else:

```go
-	Label: llm.LabelBackground,
+	Label: m.featureSlot(ctx, SettingMemoryModel, llm.LabelBackground),
```

Computer control's `auto` is not a plain label (it picks the largest local vision model), so it
keeps its own `pickLabel`; the resolver's `fallback` is where that logic is called from.

## The features

Each row is one `llm.SlotSetting`. **`auto` is the stored default and resolves to today's
hardcoded label**, so a fresh install and an upgraded install behave identically. `auto` is kept
deliberately, even where it is a static alias: it is the only way to distinguish "the user chose
background" from "we chose background", which is what makes a default revisable later without
overriding a deliberate choice.

| Feature (card label) | Setting key | `auto` today | Call sites |
|---|---|---|---|
| Computer control | `computer.model` | largest local vision model | *(already shipped)* |
| Conversation | `cc.chat.model` | live | `engine.go`, `voice_routes.go`, `warmup.go`, `refine.go`, `continue.go`, `nodeplugin.go`, `compaction.go` |
| Phone calls | `cc.phone.model` | live | `phone/turn.go`, `phone/inbound_agent.go` |
| Call planning and follow-ups | `cc.phone.planning_model` | background | `phone/plan.go`, `phone/followups.go`, `phone/turn.go:969`, `phone/inbound.go` |
| Memory | `cc.memory.model` | background | `memory_jobs.go` |
| Routines and reactions | `cc.signals.model` | background | `signals_reactions.go` |
| Web watches | `cc.watches.model` | background | `watches_web.go` |
| Errands | `cc.errands.model` | background | `errands/planner.go`, `errands/runner.go` |
| Deep research | `cc.research.model` | background | `servertools/deepresearch.go` |
| Learned workflows | `cc.workflows.model` | background | `workflows_learn.go` |
| Documents and images | `ocr.model` | background | `ocr/engines.go` |
| Recipe import | `recipes.import.model` | live | `recipes/ingest.go` |
| Grocery matching | `recipes.grocery.model` | background | `recipes/grocery_match.go` |

Thirteen rows. None of the twelve new keys collides with an existing registered definition
(verified 2026-11-11).

### The legacy recipes keys

Three existing settings are already model choices, as free-text strings that warn and normalize
anything that is not `live`/`background` — so `cloud` would silently become `live`. They are
Python-era names that would read as label names in the card. They are cleaned up:

| Old key | Becomes |
|---|---|
| `llm.full_model_name` (default `live`) | `recipes.import.model` |
| `llm.background_model_name` (default `background`) | `recipes.grocery.model` |
| `llm.lightweight_model_name` (default `live`) | **folded into `recipes.import.model`** — it is a cheap OCR-text structuring pass inside recipe import, not a feature a user would name |

A goose migration in `internal/modules/recipes/migrations/` carries a non-default old value to the
new key and deletes the old rows. Nothing outside jarvis-server reads these keys — mobile, the
command SDK, the Python node and Pantry were all checked (2026-11-11), and the documented
`JARVIS_*_MODEL_NAME` env fallbacks were cut at the port — so this is a settings migration, not a
wire change. `docs/schema/recipes.md` is updated in the same PR.

### Deliberately out of scope

`llm/jobs.go` forcing `background` on queued jobs (that is the public async-job API, not a built-in
feature), `warm_flight.go` and the `slotVision` probes (they follow whatever slot Conversation
resolved to), and the `embeddings` label.

### The one non-mechanical piece

`compaction.go` reads the **live** endpoint's context window to decide when to compact a
conversation. If Conversation runs on another slot, compaction must read *that* slot's window, or
it compacts at the wrong threshold. Compaction resolves `cc.chat.model` with the conversation, not
separately.

## The UI

The card component stays; its contents change in three ways.

1. **Copy.** The option reads `Auto — background`, not `Auto (background)`, with `live`,
   `background` and `cloud` alongside it as today.
2. **Grouping.** Thirteen bare `<select>`s is a wall. Rows group under headings, and the group
   shape is settled in the design pass, not here.
3. **Vision.** `Documents and images` and `Computer control` need a projector. A slot whose model
   has none is **disabled with the reason shown**, the same shape as the existing
   `Cloud (not set up)` option, rather than letting the feature fail at runtime. The data is
   already available (`slotVision`, and the labels query the card already runs).

Each new row needs one sentence of `notice(provider)` copy saying what leaves the network when
that feature runs on cloud. That copy is the user's call, drafted in the final PR.

## Testing Strategy

Go tests colocated, `*_test.go`; Vitest for the SPA. **No new test fake:** every affected package
already has a `fakeLLM` that records the `llm.ChatRequest` it receives, and several tests already
assert `req.Label` at exactly these call sites (`recipes/grocery_test.go:236`,
`cc/errands/planner_test.go:134`, `cc/computer/loop_test.go:249`,
`cc/servertools/tools_test.go:303`, `cc/computer/verify_test.go:55`). Those existing assertions
*are* the default-equivalence proof.

1. **Resolver unit test** (`llm/slots_test.go`): `auto`, each valid label, an unknown value and an
   unset setting each resolve as specified. Extends the existing `TestSlotSetting`.
2. **Default-equivalence, per feature:** with the setting unset, the recorded request carries the
   label it carries on `main`. Where a `req.Label` assertion already exists, it stays and counts.
   Where none exists, one is added **before** the call site changes.
3. **Override, per feature:** setting the feature to `live`/`background` moves that feature's
   recorded label and no other's. Table-driven beside the default case.
4. **Anchor depth:** features whose request is built deep in a flow (phone turns, errands) are
   tested at the point the request is constructed, not driven from an inbound call.
5. **Drift guard:** every key in the SPA's `FEATURES` table is a registered definition with
   `SlotChoices` options, and vice versa. This is what stops a row and a setting diverging later.
6. **Migration test:** a non-default `llm.full_model_name` lands on `recipes.import.model`; a
   default one leaves the new key at `auto`; the old rows are gone; the migration is idempotent.
7. **SPA:** extend `FeatureModelsCard.test.tsx` for the grown table, the grouping and the disabled
   no-vision option.
8. **Pipeline scenarios** (`.claude/skills/pipeline-test`) for Conversation and Phone, since those
   touch voice-turn behavior.
9. **Screenshots + Impeccable audit** for the card at phone/tablet/desktop in both themes.
10. No new `/api` route, so the tier gates are untouched — stated in each PR body.

## Boundaries

- **Always:** keep `auto` the stored default on every new setting; have a `req.Label` assertion
  covering a call site before changing it; keep every setting superuser and system-wide; run
  `scripts/check --all`.
- **Ask first:** adding any of these keys to cc's household-settings allowlist (access change);
  changing `NormalizeLabel`/`echoLabel`; splitting or merging a feature row differently from the
  table above; anything that would add a fourth slot.
- **Never:** let an HTTP or MQTT caller select a slot; change what a default install sends over the
  network; regenerate contract goldens to absorb a label change; weaken a tier test.

## Success Criteria

1. A superuser sees 13 grouped feature rows on the Models page, each with `Auto — {label}` plus the
   three slots, the cloud notice, and no-vision slots disabled with a reason.
2. With all settings unset, every feature's chat request carries the label it carries on `main`,
   proven by test, on a fresh install and on an upgrade.
3. Setting a feature to `live`, `background` or `cloud` moves that feature's requests and no
   others', verified for at least Conversation, Memory and Documents in a sandbox.
4. A feature set to `cloud` with the cloud slot unconfigured fails that feature with a clear error
   and leaves the rest of the install working.
5. Conversation on a non-live slot compacts against that slot's context window.
6. An install that had set `llm.full_model_name=background` has `recipes.import.model=background`
   after upgrading, and no `llm.*_model_name` rows.
7. `scripts/check --all`, `scripts/e2e web` and the pipeline scenarios pass; the design audit is in
   the PR body with screenshots.

## Delivery

A stack of four PRs, each green on its own:

| PR | Contents |
|---|---|
| 1 | `llm.FeatureSlot` + resolver tests + the Go↔UI drift guard + the `req.Label` assertions missing at today's call sites (no behavior change) |
| 2 | The cc features: settings registered, call sites read them, compaction follows Conversation |
| 3 | `ocr.model`; the recipes renames, the fold, the migration and `docs/schema/recipes.md` |
| 4 | The card: 13 rows, grouping, `Auto — {label}`, no-vision disable, cloud notices, screenshots and the design audit |

## Open Questions

None. Resolved 2026-11-11 by interview:

- Granularity: the table above (13 rows), over a coarser ~6-row grouping or a per-call-site split.
- A fourth/user-defined slot: no — two local models is the hardware ceiling and `cloud` covers the
  specialist case.
- `auto` vs. a literal default: keep `auto`, displayed as `Auto — {label}`.
- The legacy recipes keys: rename with a migration, and fold `llm.lightweight_model_name` away.
- No-vision slots: disabled in the picker with the reason shown.
- Scope: superuser, system-wide.
- Delivery: the four-PR stack above.
