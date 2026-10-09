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
| `get_current_time` (`commands/timezone_command.py`): don't fail on a `location` the LLM filled with a non-place. Treat more generic values as local (`today`, `local time`, `home`, `my timezone`, `this location`, …), accept IANA names (`America/New_York`) via `zoneinfo`, and for anything unmatched answer local time with a note instead of `error_response("Unknown location: …")` (`:289-295`). Optionally a date-only answer when the question is about the day. | A10 F20: run headlessly (mobile chat), "date and time" came back `success: False`. On the node, voice questions mostly hit the fast-path regexes and never reach the LLM, so the LLM-argument path is only exercised by server-dispatched calls. jarvisd's dispatch matches legacy's (same payload minus `trusted`, which `handle_tool_call` never reads), the command needs no server context, and weather worked over the same path in the same session, so the cause is the command rejecting the model's argument. jarvisd needs no change; the invented "3:15 PM" with no tool call is the model (no clock in the prompt unless ambient context is on, as in legacy). Confirm from the node log's `tool_call received: get_current_time args=[…]` line. | A10 F20 | Node release, any time |

## jarvis-node-mobile

| Change | Why | Decision | When |
|---|---|---|---|
| "Delete node" uses the tracked factory-reset flow (`POST /admin/nodes/{id}/factory-reset` + status polling) | Flow 1 (DELETE + untracked verify-reset) is dropped | D10 | Before flow 1 is removed |
| Add `memory.extraction_enabled` (and confirm `memory.enabled`) to the household settings screen | Per-household opt-out of learning from voice | D19 | With the memory port |
| Run-now: a longer timeout for `runRoutineNow` (today the shared `apiClient` gives up at 10 s, `src/api/apiClient.ts:56`), or switch to `202` + polling | Routine composition regularly exceeds 10 s | D40 (08.Q7), D48 | After the routines port |
| Voice enrollment screen says when speaker recognition is off for the household | Recognition is off by default; enrolling doesn't turn it on | D35, M14 | With the voice port |
| ~~Hide the Forge `TestInstallScreen` (share-code test install)~~ **No longer needed** | The four `test-install` routes were ported after all (user 2026-10-08); mobile and node clients work unchanged | D5 (reversed) | — |
| Hide or label "Install to Command Center" for Pantry `prompt_provider` packages | The install route becomes a stub that always fails cleanly | 03.Q2 | Optional |
| Household settings: a write-only "Twilio account" section for `phone.twilio_account_sid`, `phone.twilio_auth_token`, `phone.twilio_from_number` (GET shows `"********"`/null for the SID and token and the household's own from number or null; PUT `""` clears; from number must be E.164) | Multi-tenant installs give each household its own Twilio account | AD6 | Before phone calls go to friends and family |
| Pantry privacy toggle: read `pantry.enabled` from household settings, offer the toggle to household admins, and handle 403 `code: "pantry_disabled"` from package install / Forge test install (don't browse the Pantry while it's off) | The household chooses whether the app, nodes and jarvisd talk to the Pantry; default off, so upgraded installs start with it off | 2026-10-09 | With the jarvisd cutover |
| (Future) a "leave-by" built-in rule in the automations list; per-node voice enrollment | Deferred product work | D46, D36 | Post-port |

## jarvis-admin (moved into the monorepo, D9)

The SPA now lives in `web/admin/` inside jarvis-server (A0–A9); no PRs to the old repo, which is
frozen for the legacy stack.

| Change | Why | Decision | Status |
|---|---|---|---|
| Model catalog (`src/data/models.ts`): remove `qwen25-7b`, `llama-3.1-8b`, `hermes-3-8b`; remap `qwen3-14b` → `Qwen3_14B_Compressed`; add Qwen3.5-9B (→ `Qwen3_5_9B_Compressed`) and the 27B prod model (→ `Qwen3_14B_Compressed`) | Only providers that ship may be offered; an unknown provider is a hard error | D11, D12 | **Moot**: the static catalog is deleted (A6); the server catalog `GET /v1/models/catalog` carries `prompt_provider` |
| LLM wizard and quick-sets write `llm.prompt_provider` instead of `llm.interface` | Setting renamed (legacy import maps the old key) | D11 | **Moot**: LLM wizard and Quick Sets are cut (A6); the prompt provider is derived from the live model, with an override on the Models page and in the wizard (AD4) |
| Models page + setup step: hardware-aware recommendation, one-click install, install from a Hugging Face repo (pick a GGUF file/quant, see size and VRAM fit), download progress, delete, assign to live/background | First-class model management on jarvisd's model API; no automatic download | docs/llm LD3 | **Done**: Models page (A6), wizard Hardware + Models steps (A7) |

## jarvis-installer

| Change | Why | Decision |
|---|---|---|
| `LLM_INTERFACE_SEED` → seeds `llm.prompt_provider`; the `llm-interface-select` dropdown lists only kept providers | Setting rename and catalog cleanup | D11, D12 |
| Long term: replaced by the jarvisd install scripts | Single binary | PLAN Phase 6 |

## jarvis-recipes-server (optional add-on, stays Python)

> **Superseded 2026-10-08:** recipes is ported into jarvisd (`docs/recipes/00-inventory.md`, R0–R11); the rows below and PR #39 will not ship. **PR #39 is to be closed unmerged** (still open on 2026-10-08; left for the user). Its `scripts/remap_users.py` refusal rules live on in `jarvisd import-recipes`. Remaining external changes for recipes:
>
> - **install-e2e** (umbrella repo): `test_recipes.py` probes `/planner/plans`, `/shopping-list`, `/grocery/cart` and `/meal-plans/random` for 401 instead of reading `/openapi.json`, which jarvisd does not serve (inventory §11.6). PR [alexberardi/jarvis#37](https://github.com/alexberardi/jarvis/pull/37), open for review.
> - **jarvis-recipes-mobile** (RD3, **before cutover**): resolve a relative `image_url` (`/media/<name>`) against the discovered recipes base URL. PR [#20](https://github.com/alexberardi/jarvis-recipes-mobile/pull/20), merged 2026-10-08; it must be in the release users have at cutover.

| Change | Why | When |
|---|---|---|
| Image import (`from_image.py`) submits OCR over HTTP (`POST /v1/ocr/jobs` with a `callback_url`) instead of LPUSHing to Redis `jarvis.ocr.jobs` | OCR moved into jarvisd; jarvisd owns the queue and has no Redis | When OCR cuts over to jarvisd |
| Add a callback route that receives the OCR result (app-credential auth) and enqueues it onto recipes' own RQ queue | Replaces the pickled `ocr.completed` RQ job the Python OCR worker produced | Same |
| Verify jarvisd tokens: RS256 only, key from `/auth/public-key`, refetched when a token's `kid` differs; HS256 off unless `AUTH_SECRET_KEY` is set | jarvisd never mints HS256; recipes defaulted the secret to `change-me` | Same |
| One-time `python -m scripts.remap_users` after users re-register: legacy user/household ids → jarvisd's by email (cutover runbook Q2) | Clean-start accounts get new ids | Cutover, after sign-ups |
| Register the add-on on the Connections page: external row `jarvis-recipes-server` (a URL jarvisd can reach; the OCR callback is built from it) + app client `jarvis-recipes-server` | Discovery and app-to-app auth | Install |

**Status (2026-10-08):** all five rows are in recipes PR [#39](https://github.com/alexberardi/jarvis-recipes-server/pull/39) (branch `feat/jarvisd-addon`), verified end to end against a throwaway jarvisd: photo → `POST /v1/ocr/jobs` → tesseract → callback → RQ → Qwen3-4B draft.
Shapes as built: the route is `POST /internal/ocr/callback`; recipes sends the images inline as JSON (`images[{content_type, base64}]`, `workflow_id` = `parent_job_id` = its parse-job id, `source`); the envelope is queued unchanged and must carry the `ocr_job_id` recipes got back from the submit. jarvisd side: callbacks need app credentials, which a fresh install did not have (`JARVIS_APP_ID/KEY` unset → unsigned → 401). Fixed on branch `fix/jarvisd-own-app-client`: jarvisd mints its own app client `jarvisd` and keeps the key in `<home>/app-key` (installers §3.1, I3).

## Command packages (`jarvis-cmd-*`, `jarvis-device-*`)

No changes required. Contracts to keep working:

- `jarvis-cmd-calendar`'s `calendar_alerts` agent: `/signals` ingest of `appt.upcoming`, and the node-local alert path (D46).
- Secrets with `value_type='user'` (member picker) used by commands that act for a person (D41).
- `proposable_actions` and the generic proposal dispatcher (doc 10).
