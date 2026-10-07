# Changes needed outside jarvis-server

**Process (user-approved 2026-10-06):** each client change goes on a branch with a PR in that repo for the user to review before it ships to devices or prod.

jarvisd keeps every client's wire contract, so most clients need no change. A few decisions in
[`docs/cc/QUESTIONS.md`](cc/QUESTIONS.md) do need changes in other repos. They are listed here so
they can be scheduled; none are blockers for porting the server unless marked **before cutover**.

The Python hard stop applies to the **server** repos only. Node, mobile, admin and the installer
keep shipping.

## jarvis-node-setup (Pi node)

| Change | Why | Decision | When |
|---|---|---|---|
| `chat_text()` calls `POST /api/v0/node/llm/chat` (node `X-API-Key`) instead of `/api/v0/chat` | `/api/v0/chat` is unauthenticated and is dropped | D5 | **Before cutover**. Confirm `/node/llm/chat` is the "live" path the user meant. |
| Accept a full routine definition inline in the `routine` MQTT command, falling back to the local copy by slug when none is sent | App and scheduled runs never run a stale copy | D24 | **PR [#135](https://github.com/alexberardi/jarvis-node-setup/pull/135)** (backward compatible) |
| Report installed Pantry routine packages to CC, and stop seeding default routines locally once CC seeds them | CC owns every routine definition; fixes permanent shadowing | D44 | After the routines port |
| Use per-node MQTT broker credentials on fresh installs (no anonymous or shared broker login) | Per-node ACLs replace `trusted:true` | D4, D7 | **Before cutover** for new installs |
| Keep sending `X-API-Key` on Bluetooth result posts, package verify/results and settings snapshots | These routes now require node auth | D4, D5 | Verify only; believed to be sent already |
| Stop calling `/generate/date-context` (optional) | The SDK ignores the result | 03.Q11 (D40) | Optional, later |

## jarvis-node-mobile

| Change | Why | Decision | When |
|---|---|---|---|
| "Delete node" uses the tracked factory-reset flow (`POST /admin/nodes/{id}/factory-reset` + status polling) | Flow 1 (DELETE + untracked verify-reset) is dropped | D10 | Before flow 1 is removed |
| Add `memory.extraction_enabled` (and confirm `memory.enabled`) to the household settings screen | Per-household opt-out of learning from voice | D19 | With the memory port |
| Run-now: a longer timeout for `runRoutineNow` (today the shared `apiClient` gives up at 10 s, `src/api/apiClient.ts:56`), or switch to `202` + polling | Routine composition regularly exceeds 10 s | D40 (08.Q7), D48 | After the routines port |
| Voice enrollment screen says when speaker recognition is off for the household | Recognition is off by default; enrolling doesn't turn it on | D35, M14 | With the voice port |
| Hide the Forge `TestInstallScreen` (share-code test install) | Its four `test-install` routes are not ported | D5 | Before cutover |
| Hide or label "Install to Command Center" for Pantry `prompt_provider` packages | The install route becomes a stub that always fails cleanly | 03.Q2 | Optional |
| Household settings: a write-only "Twilio account" section for `phone.twilio_account_sid`, `phone.twilio_auth_token`, `phone.twilio_from_number` (GET shows `"********"`/null for the SID and token and the household's own from number or null; PUT `""` clears; from number must be E.164) | Multi-tenant installs give each household its own Twilio account | AD6 | Before phone calls go to friends and family |
| (Future) a "leave-by" built-in rule in the automations list; per-node voice enrollment | Deferred product work | D46, D36 | Post-port |

## jarvis-admin (moving into the monorepo, D9)

These now land in `web/admin/` inside jarvis-server once admin is absorbed; no PRs to the old repo.


| Change | Why | Decision |
|---|---|---|
| Model catalog (`src/data/models.ts`): remove `qwen25-7b`, `llama-3.1-8b`, `hermes-3-8b`; remap `qwen3-14b` → `Qwen3_14B_Compressed`; add Qwen3.5-9B (→ `Qwen3_5_9B_Compressed`) and the 27B prod model (→ `Qwen3_14B_Compressed`) | Only providers that ship may be offered; an unknown provider is a hard error | D11, D12 |
| LLM wizard and quick-sets write `llm.prompt_provider` instead of `llm.interface` | Setting renamed (legacy import maps the old key) | D11 |

| Models page + setup step: hardware-aware recommendation, one-click install, install from a Hugging Face repo (pick a GGUF file/quant, see size and VRAM fit), download progress, delete, assign to live/background | First-class model management on jarvisd's model API; no automatic download | docs/llm LD3 |

## jarvis-installer

| Change | Why | Decision |
|---|---|---|
| `LLM_INTERFACE_SEED` → seeds `llm.prompt_provider`; the `llm-interface-select` dropdown lists only kept providers | Setting rename and catalog cleanup | D11, D12 |
| Long term: replaced by the jarvisd install scripts | Single binary | PLAN Phase 6 |

## jarvis-recipes-server (optional add-on, stays Python)

| Change | Why | When |
|---|---|---|
| Image import (`from_image.py`) submits OCR over HTTP (`POST /v1/ocr/jobs` with a `callback_url`) instead of LPUSHing to Redis `jarvis.ocr.jobs` | OCR moved into jarvisd; jarvisd owns the queue and has no Redis | When OCR cuts over to jarvisd |
| Add a callback route that receives the OCR result (app-credential auth) and enqueues it onto recipes' own RQ queue | Replaces the pickled `ocr.completed` RQ job the Python OCR worker produced | Same |

## Command packages (`jarvis-cmd-*`, `jarvis-device-*`)

No changes required. Contracts to keep working:

- `jarvis-cmd-calendar`'s `calendar_alerts` agent: `/signals` ingest of `appt.upcoming`, and the node-local alert path (D46).
- Secrets with `value_type='user'` (member picker) used by commands that act for a person (D41).
- `proposable_actions` and the generic proposal dispatcher (doc 10).
