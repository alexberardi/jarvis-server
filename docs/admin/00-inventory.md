# admin 00 — absorbing jarvis-admin into jarvisd (Phase 6, D9)

Source: `/home/alex/jarvis/jarvis-admin` (read-only). SPA in `src/` (React 19, Vite 8, TanStack Query,
Axios, Tailwind v4), Fastify backend in `server/src/`. Paths below are relative to that repo unless they
start with `internal/`, `cmd/` or `docs/` (this repo).

Decision being implemented: **D9** (PLAN §9, STATUS decision log 2026-10-06). The SPA moves to
`web/admin/`, is built in CI, embedded in jarvisd with `go:embed` and served on the legacy port **7710**
(a new `admin` listener). The Fastify backend's compose/Docker/installer machinery is dropped. What is
left becomes Go endpoints: the model manager (`docs/llm/06-model-manager-api.md`, already built on the
llm listener) replaces the LLM wizard; settings come from each module's `/settings`; users, nodes and
households from the auth module; traces from cc; logs from the logs module. Related: LD3 (model manager),
LD5 (prod cutover starts clean), EXTERNAL-CHANGES "jarvis-admin" rows, STATUS install-UX finding 1
(`jarvisd doctor --json` should surface in admin).

**Fate vocabulary.** **KEEP**: the feature stays and needs no SPA change, or only a trivial one, behind a
same-path Go endpoint. **CHANGE**: the feature stays, but its backend or shape changes and the SPA must be
edited. **CUT**: not ported, with the reason. **NEW**: does not exist today.

---

## 0. Summary

| | SPA areas (§2.1) | Fastify routes (§2.2) |
|---|---|---|
| KEEP | 6 | 12 |
| CHANGE | 7 | 18 |
| CUT | 6 | 39 |
| NEW | 1 (Logs page) | n/a; 11 new Go pieces (§6.2) |
| Total | 19 existing areas | 69 routes |

In one paragraph: about half of the admin is Docker/compose/launchd orchestration, and that half
disappears. The other half is a thin proxy over auth, config-service, llm-proxy and command-center.
jarvisd already has most of those endpoints, but they are scattered across listeners (7701, 7703, 7704)
and some sit behind credentials a browser must never hold (cc's `ADMIN_API_KEY`, logs' app credentials).
**Recommendation (§3.2):** the admin listener is a **same-origin gateway**. It serves the SPA,
superuser-gates every `/api/*`, passes an allow-list of existing module routes through **in process**, and
adds a small set of Go "BFF" endpoints where the browser needs something no module exposes to a user JWT.
There is no CORS, no proxying over loopback, and no secrets in the browser.

---

## 1. Purpose

The admin UI is the operator console for one Jarvis install. A superuser uses it to:

- install the system (today: a Docker stack; under jarvisd: create the first superuser and install models)
- pick and download models
- edit system settings
- manage users (temporary password reset) and browse households and nodes
- read request traces
- watch health

It is used only by the operator, a superuser, from a browser on the LAN. Mobile and nodes never call it.
Today it is a separate container (`jarvis-admin`, :7710) that also owns stack orchestration
(CLAUDE.md "Identity rule").

Under jarvisd the orchestration role is gone: there is one process, no containers, and engines are
supervised by the llm module. The admin becomes a **view and control surface over jarvisd's modules**,
plus the **first-run experience** that LD5 wants to dogfood at the prod cutover.

---

## 2. Entry points

### 2.1 SPA pages and component areas

The routes are in `src/App.tsx:61-87` and the sidebar is in `src/components/layout/Sidebar.tsx:13-30`.
"Calls" lists the Fastify paths the area uses today. "jarvisd" is the replacement, written as
admin-listener path → module route.

| # | Area (route, files) | What it does | Calls today | Fate | jarvisd |
|---|---|---|---|---|---|
| S1 | **App shell and routing** (`App.tsx:29-90`, `AppShell`, `Header`, `Sidebar`) | Boot gate: `getInstallStatus()` (`App.tsx:34-50`) sends the user to `/setup` when nothing is installed, or when in compose-export "deployed-needs-account" state. It also defines the nav. | `/api/install/status` | **CHANGE** | The gate becomes `GET /api/setup/state` (NEW, §6.2 #6): `needs_superuser` → `/setup`, models not configured → a dashboard banner. The nav loses Quick Sets, Native, Reconcile and Containers, and gains Logs. Header and theme are unchanged. |
| S2 | **Login** (`pages/LoginPage.tsx`, `auth/AuthContext.tsx`, `api/auth.ts`, `api/client.ts`) | Email and password login. It refuses non-superusers client-side (`AuthContext.tsx:135`). Tokens are kept in `localStorage` (`jarvis-admin:{access_token,refresh_token,user}`, `AuthContext.tsx:33-35`), with a 10-minute proactive refresh (`:37`, `:119-126`) and refresh-on-401 (`client.ts:41-61`). Before login, LoginPage probes `/api/setup/status` (`LoginPage.tsx:24-35`), then `/api/auth/setup-status` to choose between the login and first-user modes (`:39-42`). | `/api/auth/{login,refresh,setup,setup-status}`, `/api/setup/status` | **KEEP** | `/api/auth/*` passes through to the auth module's `/auth/*` (7701 routes, same shapes: `access_token`, `refresh_token`, `user.is_superuser`; `internal/modules/auth/tokens.go:84`). One edit: drop the `/api/setup/status` probe (`LoginPage.tsx:24-35`), because jarvisd has no "service URLs not configured" state, or point it at `/api/setup/state`. |
| S3 | **Setup wizard** (`/setup`, `pages/SetupWizard.tsx`, `context/WizardContext.tsx`, `components/wizard/*`) | Seven steps (`SetupWizard.tsx:13-21`): **Welcome** (Docker reachable?, `WelcomeStep.tsx:12`); **Hardware** (a hard-coded GPU dropdown, `HardwareStep.tsx:8-30`, plus `/api/install/hardware`); **Services** (module checkboxes from `service-registry.json`); **Review**; **Install** (compose generate, then SSE `pull`/`start`, native launchd installs on macOS, `InstallStep.tsx:87,119,124`); **Account** (first superuser, `AccountStep.tsx:26`); **Models** (`LlmStep.tsx`: static catalog `data/models.ts`, HF token, docker-exec download, whisper autodownload, `:96-113`). | `/api/install/*` (12 routes), `/api/native-services/:id/install`, `/api/auth/setup`, `/api/llm-setup/{configure,download}`, `/api/models/{download,whisper-autodownload}` | **CHANGE** (rewrite) | The new wizard is **Check → Account → Models → Done** (§3.3). Welcome/Services/Review/Install are **CUT**: there is nothing to install and no modules to choose. Hardware merges into Models, backed by `/api/llm/v1/hardware`. Account is **kept**. Models is rewritten on `/api/llm/v1/models/*`. |
| S4 | **LLM setup wizard** (`/llm-setup`, `pages/LlmSetupWizard.tsx`) | A standalone "pick a model" flow from the dashboard banner: the static catalog, then a docker-exec HF download, then a settings write plus a container restart (`:6,13-14,63-64`). | `/api/llm-setup/{configure,download}` | **CUT** | Replaced by the Models page (S8) and the wizard's Models step. LD3: one model manager. |
| S5 | **Dashboard** (`/dashboard`, `pages/DashboardPage.tsx`, `components/dashboard/*`) | Container grid with restart (`:17-18,145-160`), an "LLM not configured" banner (`:19,32-33`), a compose-export notice (`:42-113`) and a "Reconcile" link (`:215`). `UpdateBanner` shows when an update is available. | `/api/containers`, `/api/containers/:id/restart`, `/api/llm-setup/status`, `/api/install/status`, `/api/update/check` | **CHANGE** | Module health via `/api/config/services/health` (config module `GET /services/health`). Engines and labels come from `/api/llm/v1/models/labels`, and the banner fires when `live` is `not_configured`. Doctor summary from `GET /api/doctor` (NEW). Update banner per AQ5. The container controls are cut. |
| S6 | **Settings** (`/settings`, `pages/SettingsPage.tsx`, `components/settings/{ServiceCard,CategoryGroup,SettingRow,SettingEditor}`) | Aggregated settings per service with search. Edit a value, then a toast with "Restart" when `requires_reload` (`SettingRow.tsx:40-44`); `ServiceCard` restarts the service's container (`ServiceCard.tsx:16-31`). | `GET /api/settings/`, `PUT /api/settings/:service/:key`, `/api/containers*` | **KEEP** | Same paths and same `AggregatedSettingsResponse` shape (`src/types/settings.ts`), served by a NEW Go aggregator over every module's `settings.Service` (§6.2 #2). Edits: drop the container restart (AQ8), and hide `llm.<label>.*` keys here (§7 I4). |
| S7 | **Service credentials card** (`components/settings/ServiceEnvCard.tsx`, in SettingsPage `:112-124`) | Edits registry-declared, user-supplied `.env` vars, which today means only the phone gateway's Twilio credentials (`server/src/routes/service-env.ts:9-30`), then recreates the container. | `/api/service-env`, `PUT /:id`, `POST /:id/apply` | **CUT** | jarvisd has no `.env` the admin could write. Twilio creds are env-only today (`cmd/jarvisd/phone.go:76-91`). Recommended: make them secret cc settings with env fallback, so they show up as ordinary write-only rows in S6 (AQ6). |
| S8 | **Models** (`/models`, `pages/ModelsPage.tsx`, `hooks/useModels.ts`, `src/data/models.ts`) | Lists files in the llm-proxy container's `.models/` (docker exec), downloads by repo/filename with an optional HF token (`ModelsPage.tsx:9-22`), deletes, and shows the static catalog (`:111-138`). | `/api/models/{installed,download}`, `DELETE /api/models/:name` | **CHANGE** (rewrite) | On the model manager via `/api/llm/v1/*` pass-through: catalog with fit, HF repo browser, installs with progress, installed list with delete/force, labels editor (assign, GPU devices, context, remote LD2), hardware/engines panel. Shapes are in `docs/llm/06-model-manager-api.md` §5; nothing new is needed on the Go side. This is EXTERNAL-CHANGES' "Models page + setup step" row. |
| S9 | **Static model catalog** (`src/data/models.ts`) | Six hard-coded models with HF repos, sizes, VRAM and `promptProvider` (ids at `:20,35,50,65,80,95`). | none | **CUT** | Server catalog `GET /v1/models/catalog` (D12 list, pinned commits, fit). EXTERNAL-CHANGES' catalog-cleanup row becomes moot because the file is deleted. |
| S10 | **Quick Sets** (`/quick-sets`, `pages/QuickSetsPage.tsx`) | Presets of `chat_format` / prompt provider / backend / context window written to llm-proxy `model.*` keys and CC `llm.interface`, followed by a container restart (`server/src/routes/quick-sets.ts:138-270`). Custom presets live in `~/.jarvis/custom-quick-sets.json` (`:54`). | `/api/quick-sets*` | **CUT** | Every key it writes is gone: `model.live.*` and `chat_format` give way to `llm.live.*` labels plus the catalog's `context_default`, and `llm.interface` is now `llm.prompt_provider` (D11). The catalog entry carries `prompt_provider` (06 §5), so a preset is now just "install or assign a catalog model" (AQ4). |
| S11 | **Services** (`/services`, `pages/ServicesPage.tsx`, `components/services/ServiceRegistrationRow.tsx`) | config-service registry: register every service, probe health, add/delete entries, rotate an app key (with an `.env` write-back, `server/src/routes/services.ts:76-113`), and suggestions from `service-registry.json`. | `/api/services/{registry,register,rotate-key,probe,suggestions}`, `DELETE /api/services/:name` | **CHANGE** | Becomes **"Connections"**: a read-only list of jarvisd's own listeners, plus **external** entries (the recipes add-on, a remote GPU satellite) with add/remove, plus **app clients** (list, create, rotate, revoke) for external callers (PLAN §3.1). Backed by NEW `/api/connections/*` (§6.2 #8), because config `/services` writes and auth `/admin/app-clients` require admin tokens that the browser must not hold. Register-all, probe and suggestions are cut (AQ7). |
| S12 | **Traces** (`/traces`, `/traces/:id`, `pages/TracesPage.tsx`, `pages/TraceDetailPage.tsx`, `api/traces.ts`) | A filterable trace list (status, source, household, node) with household/node dropdowns from `/api/admin/{households,nodes}` (`TracesPage.tsx:75-91`), and a span waterfall detail. | `/api/traces`, `/api/traces/:id`, `/api/admin/{households,nodes}` | **KEEP** | Same paths and shapes. NEW Go BFF `/api/traces*` calls cc in process (§6.2 #3). cc's own route needs `X-API-Key: ADMIN_API_KEY` (`internal/modules/cc/cc.go:324-325`, `authz.go:142-155`), which a fresh install doesn't even set. The cc list returns `{traces,total}` with limit/offset (`internal/modules/cc/traces.go:115-176`), matching `TraceListResponse`. |
| S13 | **Nodes** (`/nodes`, `pages/NodesPage.tsx`, `api/nodes.ts`) | Lists the **caller's own** households (auth `/households`) and their nodes, plus a **Train Adapter** button (`NodesPage.tsx:64,74-77`; backend sends a `train_adapter` MQTT command through CC with the admin key, `server/src/routes/nodes.ts:36-52`). | `/api/nodes`, `/api/nodes/:hh/nodes`, `POST /api/nodes/:id/train-adapter` | **CHANGE** | List every household and node through `/api/admin/{households,nodes}` (auth `/superuser/*`; the superuser is the operator of every node, STATUS 5c follow-ups "Superuser on a no-household node"). Optional: last-seen from cc `GET /api/v0/admin/nodes` (user JWT) via pass-through. **Train Adapter is CUT** (LoRA, PLAN §7). |
| S14 | **Users** (`/users`, `pages/UsersPage.tsx`, `api/admin.ts`) | Every user with households and roles; a show-once temporary password reset (`UsersPage.tsx:138,174`). | `/api/admin/users`, `POST /api/admin/users/:id/temp-password` | **KEEP** | `/api/admin/*` passes through to auth `/superuser/*` (`internal/modules/auth/auth.go:222-225`), the same mapping Fastify did (`server/src/routes/admin.ts:13-60`). |
| S15 | **Native services** (`/native-services`, `pages/NativeServicesPage.tsx`) | macOS launchd lifecycle for Python services (install by clone + `deploy-launchd.sh`, restart, stop, uninstall, log files; `server/src/routes/native-services.ts`). | `/api/native-services*` | **CUT** | There are no Python services. jarvisd runs Metal engines itself (PLAN §3.3), and its own logs are in the logs module. |
| S16 | **Reconcile** (`/reconcile`, `pages/ReconcilePage.tsx`) | Regenerates `docker-compose.yml`, adds missing workers and services, recreates changed ones (`ReconcilePage.tsx:83,137,213`). | `/api/install/reconcile*`, `/api/install/regenerate-download`, `GET /api/models` | **CUT** | No compose. |
| S17 | **Updates** (`/update`, `pages/UpdatePage.tsx`, `components/dashboard/UpdateBanner.tsx`, `api/update.ts`) | Opt-in toggle (`allowUpdates`, default off), check against GitHub releases of `alexberardi/jarvis-admin` (`server/src/services/update-checker.ts:3,50`), and an SSE "apply" that self-updates the stack (`UpdatePage.tsx:153`) then polls `/health` (`:403`). Never claims "up to date" when checks are off (`api/update.ts:10-18`). | `/api/update/{settings,check,status,apply}`, `/health` | **CHANGE** | jarvisd version check against `alexberardi/jarvis-server` releases, with the same opt-in gate (default off; core principle 1). Apply: AQ5 (recommended: check plus instructions first, verified self-update later). |
| S18 | **System info bar** (`components/layout/SystemInfoBar.tsx`, `hooks/useSystem.ts`) | Hostname, platform, CPUs, RAM, version, uptime (`server/src/routes/system.ts:9-18`). | `/api/system/info` | **KEEP** | NEW Go `GET /api/system/info`, same shape (`src/types/system.ts`) plus additive fields (§6.2 #5). |
| S19 | **Theme, NotFound, lib** (`theme/*`, `pages/NotFoundPage.tsx`, `lib/*`) | Light/dark tokens, 404 page. | none | **KEEP** | None needed. |
| S20 | **Logs** (new page) | None today. Logs live in Grafana or in `docker logs` (and `InstallStep.tsx:209` fetches a `/api/containers/:id/logs` route that doesn't exist). | none | **NEW** | NEW `GET /api/logs`, `GET /api/logs/services`, `GET /api/logs/stream` over the logs module (§6.2 #7). Filter by service (module), level, time and text; live tail. |

### 2.2 Fastify routes (69)

Registered in `server/src/app.ts:154-170`. Auth is `requireSuperuser` (`middleware/auth.ts:18-53`, which
round-trips to auth `/auth/me`) unless noted. "Bootstrap-open" means `requireSuperuserIfInstalled`
(`middleware/auth.ts:68-74`): unauthenticated until `~/.jarvis/admin.json` records an install.

| Fastify route | Line | Auth | Fate | jarvisd (admin listener → module) |
|---|---|---|---|---|
| `GET /health` | `routes/health.ts:5` | none | KEEP | admin listener `GET /health` (`{status, version}`; jarvisd version) |
| `GET /api/auth/setup-status` | `routes/auth.ts:5` | none | KEEP | pass-through → auth `GET /auth/setup-status` |
| `POST /api/auth/setup` | `routes/auth.ts:14` | none | KEEP | pass-through → auth `POST /auth/setup` (creates "My Home" too, `internal/modules/auth/tokens.go:305-340`); see AQ2 |
| `POST /api/auth/login` | `routes/auth.ts:24` | none | KEEP | pass-through → auth `POST /auth/login` |
| `POST /api/auth/refresh` | `routes/auth.ts:38` | none | KEEP | pass-through → auth `POST /auth/refresh` |
| `GET /api/settings/` | `routes/settings.ts:8` | superuser | CHANGE | NEW aggregator `GET /api/settings[?service=]`, same shape (config-service's gateway is not ported) |
| `PUT /api/settings/:service/:key` | `routes/settings.ts:23` | superuser | CHANGE | NEW `PUT /api/settings/{service}/{key...}` → that module's `settings.Service` |
| `GET /api/services/registry` | `routes/services.ts:49` | superuser | CHANGE | `GET /api/connections` (NEW) built from config module's registry + `/services/health` |
| `POST /api/services/register` | `routes/services.ts:62` | superuser | CUT | jarvisd self-registers its listeners (STATUS Phase 1); externals are added one at a time below |
| `POST /api/services/rotate-key` | `routes/services.ts:76` | superuser | CHANGE | `POST /api/connections/apps/{app_id}/rotate` (NEW) → auth app-client rotate in process; no `.env` write-back (`routes/services.ts:9-44` disappears) |
| `POST /api/services/probe` | `routes/services.ts:115` | superuser | CUT | external entries are health-probed by config's `/services/health` |
| `GET /api/services/suggestions` | `routes/services.ts:129` | superuser | CUT | `service-registry.json` is not ported |
| `DELETE /api/services/:name` | `routes/services.ts:148` | superuser | CHANGE | `DELETE /api/connections/services/{name}` (NEW), external entries only |
| `GET /api/containers` | `routes/containers.ts:7` | superuser | CUT | no containers; engines are in `/v1/hardware.engines` |
| `GET /api/containers/:id` | `routes/containers.ts:32` | superuser | CUT | same |
| `POST /api/containers/:id/restart` | `routes/containers.ts:49` | superuser | CUT | engines restart themselves (06 §4); see AQ8 for jarvisd restart |
| `GET /api/system/info` | `routes/system.ts:9` | superuser | KEEP | NEW Go handler, same shape |
| `GET /api/nodes` | `routes/nodes.ts:10` | superuser | CHANGE | SPA switches to `/api/admin/households` (all households, not just the caller's) |
| `GET /api/nodes/:hh/nodes` | `routes/nodes.ts:21` | superuser | CHANGE | SPA switches to `/api/admin/nodes` (all nodes; filter client-side) |
| `POST /api/nodes/:id/train-adapter` | `routes/nodes.ts:36` | superuser + CC admin key | CUT | LoRA cut (PLAN §7) |
| `GET /api/setup/status` | `routes/setup.ts:33` | none | CHANGE | `GET /api/setup/state` (NEW) |
| `POST /api/setup/probe` | `routes/setup.ts:60` | none | CUT | no service URLs to discover; no SPA caller today either |
| `POST /api/setup/configure` | `routes/setup.ts:136` | bootstrap-open | CUT | same (no SPA caller) |
| `GET /api/llm-setup/status` | `routes/llm-setup.ts:38` | superuser | CHANGE | `/api/llm/v1/models/labels` (live label `state`) |
| `POST /api/llm-setup/configure` | `routes/llm-setup.ts:95` | superuser | CUT | `PUT /api/llm/v1/models/labels`; its keys (`model.live.*`, `inference.vllm.*`, `llm.interface`; `:104-123`) no longer exist |
| `POST /api/llm-setup/download` | `routes/llm-setup.ts:215` | superuser | CUT | `POST /api/llm/v1/models/install` (resumable, verified, queued) |
| `GET /api/quick-sets` | `routes/quick-sets.ts:86` | superuser | CUT | see S10 |
| `POST /api/quick-sets/apply` | `routes/quick-sets.ts:138` | superuser | CUT | see S10 |
| `POST /api/quick-sets/custom` | `routes/quick-sets.ts:271` | superuser | CUT | see S10 |
| `DELETE /api/quick-sets/custom/:id` | `routes/quick-sets.ts:307` | superuser | CUT | see S10 |
| `GET /api/models/installed` | `routes/models.ts:154` | superuser | CHANGE | `/api/llm/v1/models/installed` (every model kind, with labels and disk use) |
| `POST /api/models/download` | `routes/models.ts:196` | superuser | CHANGE | `/api/llm/v1/models/install` (202 + poll `installs/{id}`) |
| `POST /api/models/whisper-autodownload` | `routes/models.ts:275` | superuser | CUT | STT is the `stt` label; install `whisper-*` from the catalog |
| `DELETE /api/models/:name` | `routes/models.ts:319` | superuser | CHANGE | `DELETE /api/llm/v1/models/installed/{id}[?force=true]` (409 lists labels using it) |
| `GET /api/update/settings` | `routes/update.ts:48` | none | CHANGE | `GET /api/update` includes `updates_enabled` |
| `POST /api/update/settings` | `routes/update.ts:66` | superuser | CHANGE | `PUT /api/update/settings` → admin module setting (NEW) |
| `GET /api/update/check` | `routes/update.ts:82` | none | CHANGE | `GET /api/update` (superuser; NEW) |
| `POST /api/update/check` | `routes/update.ts:88` | none | CHANGE | `POST /api/update/check` (force; NEW) |
| `GET /api/update/status` | `routes/update.ts:94` | none | CUT | the upgrade marker belongs to the stack updater; revisit with AQ5 (b) |
| `POST /api/update/apply` | `routes/update.ts:99` | superuser | CUT (for now) | AQ5 |
| `GET /api/install/status` | `routes/install.ts:48` | none | CHANGE | `GET /api/setup/state` |
| `GET /api/install/preflight` | `routes/install.ts:180` | none | CUT | ports/Docker preflight; listener and firewall checks are `GET /api/doctor` |
| `GET /api/install/hardware` | `routes/install.ts:387` | none | CHANGE | `/api/llm/v1/hardware` (real detection and proposal, 06 §5) |
| `POST /api/install/generate` | `routes/install.ts:580` | bootstrap-open | CUT | compose |
| `GET /api/install/pull` (SSE) | `routes/install.ts:622` | bootstrap-open | CUT | image pull |
| `GET /api/install/start` (SSE) | `routes/install.ts:662` | bootstrap-open | CUT | tiered `docker compose up` |
| `POST /api/install/register` | `routes/install.ts:777` | bootstrap-open | CUT | self-registration |
| `GET /api/install/health` | `routes/install.ts:808` | none | CUT | `/api/config/services/health` |
| `POST /api/install/account` | `routes/install.ts:830` | bootstrap-open | CUT | duplicate of `/api/auth/setup`; no SPA caller (AccountStep uses `api/auth.setup`) |
| `GET /api/install/registry` | `routes/install.ts:894` | none | CUT | `service-registry.json` |
| `GET /api/install/defaults` | `routes/install.ts:903` | none | CUT | module selection |
| `GET /api/install/reconcile/options` | `routes/install.ts:916` | superuser | CUT | compose |
| `POST /api/install/regenerate-download` | `routes/install.ts:965` | superuser | CUT | compose |
| `POST /api/install/reconcile` | `routes/install.ts:999` | superuser | CUT | compose |
| `GET /api/native-services` | `routes/native-services.ts:148` | none | CUT | launchd |
| `GET /api/native-services/:id/install` (SSE) | `routes/native-services.ts:183` | **none** (§8 O6) | CUT | launchd |
| `POST /api/native-services/:id/restart` | `routes/native-services.ts:257` | superuser | CUT | launchd |
| `POST /api/native-services/:id/stop` | `routes/native-services.ts:277` | superuser | CUT | launchd |
| `POST /api/native-services/:id/uninstall` | `routes/native-services.ts:294` | superuser | CUT | launchd |
| `GET /api/native-services/:id/logs` | `routes/native-services.ts:319` | superuser | CUT | `/api/logs` |
| `GET /api/traces` | `routes/traces.ts:20` | superuser + CC admin key | KEEP | NEW Go BFF `GET /api/traces`, same shape |
| `GET /api/traces/:id` | `routes/traces.ts:37` | superuser + CC admin key | KEEP | NEW Go BFF `GET /api/traces/{id}` |
| `GET /api/admin/households` | `routes/admin.ts:13` | superuser | KEEP | pass-through → auth `GET /superuser/households` |
| `GET /api/admin/nodes` | `routes/admin.ts:23` | superuser | KEEP | pass-through → auth `GET /superuser/nodes` |
| `GET /api/admin/users` | `routes/admin.ts:33` | superuser | KEEP | pass-through → auth `GET /superuser/users` |
| `POST /api/admin/users/:id/temp-password` | `routes/admin.ts:43` | superuser | KEEP | pass-through → auth `POST /superuser/users/{id}/temp-password` |
| `GET /api/service-env` | `routes/service-env.ts:115` | superuser | CUT | AQ6 (secret settings) |
| `PUT /api/service-env/:id` | `routes/service-env.ts:131` | superuser | CUT | AQ6 |
| `POST /api/service-env/:id/apply` | `routes/service-env.ts:213` | superuser | CUT | AQ6 |

Two SPA calls hit routes that do not exist (§8 O2): `GET /api/containers/:id/logs` (`InstallStep.tsx:209`)
and `GET /api/models` (`ReconcilePage.tsx:104`). Both are cut with their pages.

---

## 3. Behaviour

### 3.1 Auth today

1. **Login.** `POST /api/auth/login` is forwarded verbatim to jarvis-auth (`server/src/routes/auth.ts:24-36`).
   The SPA rejects `user.is_superuser == false` (`AuthContext.tsx:135`) and stores the tokens and the user in
   `localStorage` (`:144-146`). Axios adds `Authorization: Bearer` from memory or `localStorage`
   (`client.ts:30-39`). The setup wizard's AccountStep writes the **un-namespaced** keys
   `access_token`/`refresh_token` (`AccountStep.tsx:28-29`), which the interceptor also reads as a fallback.
2. **Every `/api/*` call** is re-validated by the backend with a round trip to auth `/auth/me`
   (`middleware/auth.ts:31-46`), so the superuser check happens twice: in the SPA and on the server.
3. **Refresh** runs every 10 minutes (`AuthContext.tsx:37,119-126`) and on any 401 except `/api/auth/*`
   (`client.ts:46-58`). jarvisd access tokens live 30 minutes (STATUS decision log).
4. **Calls on to other services:** Fastify forwards the user's JWT to auth (`/superuser/*`, `/households`),
   config-service (`/v1/settings`, `/v1/services/*`) and llm-proxy (`/settings`). For CC it uses its **own
   secret** `COMMAND_CENTER_ADMIN_KEY` (`services/commandCenter.ts:11-27`; traces and train-adapter). So
   Fastify is a backend-for-frontend that holds secrets the browser must not hold.
5. **Origin.** The SPA is served by Fastify on :7710 and every call is same-origin. CORS is off unless
   `JARVIS_ADMIN_CORS_ORIGINS` is set (`app.ts:98-105`). The CSP is `connect-src 'self'`
   (`app.ts:113-130`), so the SPA cannot call other ports even if CORS allowed it.

### 3.2 Auth and origin under jarvisd (recommended design)

**The problem.** The routes the SPA needs live on four listeners: auth 7701, config 7700, cc 7703 and
llm 7704. Two of them are unusable from a browser:

- cc traces need `X-API-Key: ADMIN_API_KEY` (`internal/modules/cc/authz.go:142-155`), and a fresh jarvisd
  install has no reason to set that variable.
- logs queries need app credentials (`internal/modules/logs/logs.go:103-107,151-170`).

**Options.**

- **(A) The SPA calls the module ports directly.** This needs CORS on every listener and a CSP that lists
  every port. Traces and logs can't be reached without putting secrets in the browser, and the SPA would
  need the host's other ports (a JARVIS_PORT override breaks it).
- **(B) The admin listener reverse-proxies over loopback** to `127.0.0.1:<port>`. This is same-origin, but
  it still can't reach the admin-key and app-credential routes, adds a network hop to itself, and breaks
  if a listener is disabled or re-ported.
- **(C, recommended) A same-origin in-process gateway on the admin listener:**
  - **Static.** `GET /` and every non-`/api` path serve the embedded SPA, with an `index.html` fallback
    for client routes (today: `app.ts:172-183`).
  - **One gate.** Every `/api/*` request passes `settings.SuperuserGuard(auth.VerifyUser)`
    (`internal/platform/settings/guards.go:19-28`). The token is verified in process, with no `/auth/me`
    round trip. The exceptions are `POST /api/auth/login`, `POST /api/auth/refresh`,
    `GET /api/auth/setup-status`, `POST /api/auth/setup` (AQ2) and a reduced `GET /api/setup/state`.
  - **Pass-through, allow-listed.** A prefix table maps admin paths onto **another listener's handler, in
    process**. The request is cloned with its path rewritten and `ServeHTTP` is called on that listener's
    mux. The module's own guard still runs, so this is defence in depth, not a bypass. Response shapes are
    exactly the module's, so the SPA uses the documented contracts (06 §5, auth, config):

    | Admin prefix | Target listener and path | Used by |
    |---|---|---|
    | `/api/auth/{login,refresh,setup,setup-status,me,logout,change-password}` | auth `/auth/…` | S2, S3 |
    | `/api/admin/{households,users,nodes,users/{id}/temp-password}` | auth `/superuser/…` | S12–S14 (keeps today's SPA paths) |
    | `/api/llm/v1/hardware[/engines]`, `/api/llm/v1/models/{catalog,hf/…,install,installs[/…],installed[/…],labels}` | llm `/v1/…` | S3, S5, S8 |
    | `/api/config/services`, `/api/config/services/health` (GET only) | config `/services…` | S5, S11 |
    | `/api/cc/api/v0/admin/nodes` (GET only; optional) | cc `/api/v0/admin/nodes` (user JWT) | S13 liveness |

  - **BFF endpoints** in Go, where no module exposes what the operator needs to a user JWT. These call
    modules through **Go interfaces**, not HTTP: settings, traces, logs, doctor, system, setup state,
    connections, update (§6.2).

**Why C.**

- No CORS and no change to the CSP.
- No secret leaves the server, and nothing depends on `ADMIN_API_KEY` being configured.
- The SPA keeps its current `/api/auth/*`, `/api/admin/*`, `/api/settings/*`, `/api/traces*` and
  `/api/system/info` paths unchanged.
- Each module stays the single owner of its routes: model-manager routes are not duplicated, only
  allow-listed.

*As built (A2, `internal/modules/admin/gateway.go`):* the bootstrap list is login, refresh, **logout**
(it only revokes the refresh token it is sent, and an expired session must still be able to log out),
setup-status and setup. *A3:* `GET /api/setup/state` is always open (reduced view unless a superuser
token is sent) and `GET /api/doctor` is open only while no superuser exists. The table is exact method+path patterns,
not prefixes (the llm rows are the model manager's 13 routes; `/api/cc/api/v0/admin/nodes` is included).
Any other `/api/*` is gated first (401 anonymous, 403 non-superuser) and then a JSON 404, so the route
surface can't be probed anonymously. A target listener that is not served answers 503. The empty-body
rule (I9) is middleware on every `/api` route, turning a zero-length `application/json`
POST/PUT/PATCH body into `{}`.

**Mechanism needed.** Today the runner builds one `ServeMux` per listener and keeps them private
(`internal/platform/module/runner.go:73-80`). Add a lookup that a module can call at request time, e.g.
`Deps.Handler(listener) http.Handler` filled in by the runner after every `Register`. Requests then reach
the target mux without its `httpx.Middleware` (`runner.go:124`); the admin listener's own middleware
covers recover and logging.

For a **remote GPU satellite** (PLAN Phase 6, llm+stt only), the same table can later point `llm` at the
registry URL over HTTP instead of in process. That needs the satellite to trust the main box's RS256 key
(it fetches `/auth/public-key`). It is out of scope for the first cut and is noted in §11.

**Token storage** stays in `localStorage`. This is acceptable for a LAN operator console, and Bearer
tokens make CSRF moot because there are no cookies. One fix: AccountStep should use the namespaced keys
through `AuthContext.setup` instead of writing raw `access_token` (`AccountStep.tsx:26-29`).

**Error shape.** Fastify sent `{error}` and jarvisd sends `{detail}` (httpx), or
`{"error":{"message"}}` from the settings router (`internal/platform/settings/router.go:33-36`), or 422
arrays. The SPA reads both `detail` (`AuthContext.tsx:159,190`) and `error` (`AccountStep.tsx:33`), so add
one `errorMessage(err)` helper that handles all three.

**Streams.** `EventSource` cannot send `Authorization`. That is why the Fastify install SSE routes were left
unauthenticated (`native-services.ts:140-145,183`). The admin's streaming endpoints (logs tail; a future
update apply) use `fetch()` with a `ReadableStream` reader and the Bearer header. No query-string tokens.

### 3.3 First boot

**Today** (CLAUDE.md lifecycle §1, `SetupWizard.tsx:13-21`):

1. Is Docker reachable? (Welcome)
2. Hand-pick a GPU from a static list (`HardwareStep.tsx:8-30`).
3. Tick modules from `service-registry.json`, then review.
4. Generate compose (`compose-generator.ts`; macOS drops GPU services at `:96`).
5. Pull images, then `docker compose up` tier by tier (`services/orchestrator.ts:8-60`), with launchd
   installs on macOS.
6. Register services, which writes app keys into `.env`.
7. Create the first superuser.
8. Pick a model from the static catalog, `docker exec` a `huggingface_hub` download, write `model.*`
   settings and `llm.interface`, then restart the container. The download is synchronous with no
   progress (`llm-setup.ts:215-285`).

**What a fresh jarvisd install needs** (LD3: no automatic download; LD5: prod goes through it).

The install script (Phase 6) installs the binary and service unit, starts jarvisd, runs
`jarvisd doctor`, offers to apply the firewall fix, and prints `http://<lan-ip>:7710/`. The SPA's
`/api/setup/state` then drives four steps:

1. **Check.** `GET /api/doctor`: each listener answers; host firewall per LAN subnet with the exact
   allow command and a copy button (`internal/doctor`, STATUS install-UX 1). jarvisd runs unprivileged and
   can't apply a firewall rule itself; the installer offers that. Warn, don't block.
2. **Account.** The first superuser via `POST /api/auth/setup`. It auto-creates the "My Home" household
   (`tokens.go:338`) and returns tokens, and the SPA logs straight in (`AuthContext.setup`). AQ2 adds a
   setup token.
3. **Models**, on the model manager only:
   - `GET /api/llm/v1/hardware` shows the detected GPUs and the proposal (one card: both LLM labels on it;
     two or more: live on the largest, background on the next; Apple: Metal).
   - `GET /api/llm/v1/models/catalog` gives `recommended` for live, background, embeddings, stt, tts and
     speaker, each with a fit verdict and co-residency (06 §5).
   - **"Install recommended"** issues one `POST /api/llm/v1/models/install` per distinct recommended model,
     with `assign` set to its labels and `with_mmproj` for the live model (LD4). It polls
     `GET /installs/{id}` about once a second and shows `warning`s without refusing.
   - "Advanced" exposes per-label GPU devices, context and remote endpoints (LD2: off by default, with a
     privacy note) via `PUT /api/llm/v1/models/labels`.
   - Gated repos ask for the `llm.hf_token` secret setting.
   - The step can be skipped. The dashboard banner keeps nagging while `live` is `not_configured`.
4. **Done.** Shows the effective prompt provider (AQ4), the voice models' state (`/v1/hardware.voice`),
   a doctor re-run, and next steps: install the mobile app, add a node by QR provisioning in the app, and
   which port and mDNS name the phone will find.

**Gaps a fresh install hits today, independent of the UI:**

- **G1, prompt provider unset is a hard error.** `cc.Module.DefaultPromptProvider` exists
  (`internal/modules/cc/cc.go:122-124`) but `cmd/jarvisd/main.go` never sets it. With
  `llm.prompt_provider` empty, `promptProvider()` fails (`internal/modules/cc/warmup.go:132-138`), so the
  first voice turn fails until someone sets a setting the admin no longer has a screen for. Fix: wire it to
  the live label's installed model `prompt_provider` (06 §5 "installed" carries it). AQ4.
- ~~**G2, the setup race is open to the LAN**~~ **fixed (AD2)**: `/auth/setup` needs the setup token
  (`<home>/setup-token`, `X-Jarvis-Setup-Token` header or `setup_token` field) while no superuser exists.
- **G3, the update opt-in and Twilio creds** have no home without `admin.json` or `.env`. AQ5, AQ6.

### 3.4 Steady state

- **Settings write.** `PUT /api/settings/{service}/{key}` goes to the BFF, which looks up that module's
  `settings.Service`, then `Set` and audit-logs the key (never the value). The response is
  `ServiceUpdateResponse` (`src/types/settings.ts:29-36`), with `requires_reload` from the definition.
- **Model change.** The Models page calls `PUT /api/llm/v1/models/labels`; the resolver swaps only the
  affected engine (06 §3). No restart button.
- **Health.** The dashboard polls `/api/config/services/health`, `/api/llm/v1/models/labels` (engine
  states and VRAM warnings) and `/api/doctor` (cached; §6.2 #4).

---

## 4. Data

**Today**

- Browser `localStorage`: `jarvis-admin:{access_token,refresh_token,user}`, legacy
  `access_token`/`refresh_token`, `jarvis-admin:llm-setup-dismissed` (`DashboardPage.tsx:12`).
- `~/.jarvis/admin.json`: service URLs, `allowUpdates`, the install marker
  (`server/src/config.ts:34-42,122-128`).
- `~/.jarvis/custom-quick-sets.json` (`quick-sets.ts:54`).
- `~/.jarvis/upgrade-in-progress.json` (`update.ts:9`).
- `~/.jarvis/compose/{docker-compose.yml,.env}` and `~/.jarvis/state.json`.

**Under jarvisd**

- No new tables except an `admin` settings set if AQ5 lands: one key, `updates.enabled`, through
  `settings.New(deps.DB, "admin", …)` like every module.
- The SPA bundle is embedded in the binary (§11.1).
- No files under `~/.jarvis`.
- `localStorage` keys stay the same, so an operator's existing session simply 401s and re-logs in.
- None of the files above are imported. They belong to the Docker stack, which LD5 retires clean.

---

## 5. Settings

| Key | Module | Default | Notes |
|---|---|---|---|
| `updates.enabled` | admin (NEW) | `false` | Replaces `JARVIS_ALLOW_UPDATES` / `admin.json.allowUpdates`. Env fallback `JARVIS_ALLOW_UPDATES` keeps the CLI path. AQ5. |
| `llm.hf_token` | llm | `""` (secret) | Asked for by the Models page on a 403 (gated) repo. Already exists (06 §3). |
| `llm.prompt_provider` | cc | `""` | Shown and overridable on the Models page. Empty means derived from the live model (AQ4, G1). |
| `phone.twilio_*` | cc (NEW, if AQ6 = settings) | `""` (secret) | Env fallback `TWILIO_ACCOUNT_SID` / `TWILIO_AUTH_TOKEN` / `TWILIO_FROM_NUMBER`. |
| `JARVIS_PORT_ADMIN` | env | `7710` | Already generic: `JARVIS_PORT_<LISTENER>` (`internal/platform/config/config.go:67`). Empty disables the admin listener. |
| `JARVIS_ADMIN_UI_DIR` | env (dev only) | unset | Serve the SPA from disk instead of the embed (§11.2). |

---

## 6. Dependencies

### 6.1 What the admin consumes (exists today)

| Need | jarvisd today | Shape notes |
|---|---|---|
| Login, refresh, setup, me | auth `/auth/*` (`internal/modules/auth/auth.go:157-167`) | Same as the legacy auth (contract-tested). |
| Users, households, nodes, temp password | auth `/superuser/*` (`auth.go:222-225`) | Same as what Fastify proxied. Verify with a contract test that `GET /superuser/users` still has `households[]` and `must_change_password` (`src/api/admin.ts:28-38`). |
| Model manager, hardware, engines | llm `/v1/models/*`, `/v1/hardware*` (`internal/modules/llm/models/api.go:92-103`), superuser-guarded | 06 §5 is the contract. |
| Registry and module health | config `GET /services`, `GET /services/health` (`internal/modules/config/config.go:121-124`) | Public reads. Writes need `X-Admin-Token` (`config.go:511-526`), so they go through the BFF. |
| Per-module settings | `settings.Service` in every module that calls `settings.New` (auth, cc, llm, config, logs, ocr, stt, tts) | Each module's field is private, so expose an accessor (§6.2 #2). |
| Traces | cc `/api/v0/admin/traces*` (`cc.go:324-325`, admin key) | Need an exported Go method. |
| Logs | logs `/api/v0/logs*` (app credentials) | Need exported Go methods. |
| Doctor | `internal/doctor.Run`, CLI only (`cmd/jarvisd/doctor.go:19-37`) | `doctorPorts` lives in `cmd/jarvisd`; move it or inject it. |

### 6.2 Missing in jarvisd: new Go pieces (verified by grep of `internal/modules`, `cmd/jarvisd`)

| # | Piece | Proposed path and shape |
|---|---|---|
| 1 | **Admin listener and module** (`internal/modules/admin`). `config.ListenerAdmin="admin"`, `DefaultPorts[admin]=7710`, `ServiceNames[admin]="jarvis-admin"`. It serves the embedded SPA, `GET /health` and the gateway. Today there is no 7710 anywhere in `internal/platform/config/config.go:13-40`. | Static plus `/api/*` per §3.2. Doctor picks the port up automatically, because `doctorPorts` iterates the modules. |
| 2 | **Settings aggregator.** There is no `/v1/settings` gateway in the config module (its routes are `config.go:115-127`). Add `SettingsService() *settings.Service` on each module, or a registry passed in from main. | `GET /api/settings[?service=<module>]` returns `{services:[{service_name, display_name, success, settings:[SettingResponse], error, latency_ms}], total_services, successful_services, failed_services}`, i.e. `src/types/settings.ts` plus an additive `display_name`. `service_name` is the module name (`llm`, `cc`, …), and `display_name` is a human label. `PUT /api/settings/{service}/{key...}` with `{value}` returns `ServiceUpdateResponse`. Secrets render as `********` (as `router.go` does). Unknown service → 404; validation → 422. |
| 3 | **Traces for a superuser.** Export from cc `ListTraces(ctx, TraceFilter{Limit,Offset,Status,Source,HouseholdID,NodeID}) ([]Trace, int, error)` and `GetTrace(ctx, id)`, and have `handleListTraces`/`handleGetTrace` (`internal/modules/cc/traces.go:115,178`) call them. | `GET /api/traces?limit&offset&status&source&household_id&node_id` returns `{traces, total}`; `GET /api/traces/{id}` returns a trace with `spans[]` (unchanged from `src/api/traces.ts`). |
| 4 | **Doctor over HTTP.** | `GET /api/doctor[?refresh=true]` returns `{checks:[{name,status,detail,fix}], ran_at}` (`internal/doctor/doctor.go:25-30`), cached 30 s. Pre-superuser: shown in the Check step only while `needs_superuser` (AQ2 decides whether that counts as public). |
| 5 | **System info.** | `GET /api/system/info` returns `{hostname, platform, release, cpuCount, totalMemoryMb, version, uptime}` (today's shape), plus `arch`, `home`, `db_bytes`, `disk_free_bytes` (for the jarvisd home) and `modules[]`. |
| 6 | **Setup state.** | `GET /api/setup/state`. Pre-auth: `{needs_superuser, version, setup_token_required}`. With a superuser: plus `{labels: {live,background,embeddings,stt,tts,speaker: state}, prompt_provider: {value, effective, source}, doctor: {status, failing}, households, nodes}`. |
| 7 | **Logs for a superuser.** Export from logs `Query(ctx, Filter)`, `Services(ctx)` and a tail subscription; today those exist only as handlers (`logs.go:522-600`). | `GET /api/logs?service&level&since&until&q&limit` returns `[logOut]` (the module's own shape). `GET /api/logs/services` returns `[string]`. `GET /api/logs/stream?service&level` is SSE read with `fetch()` (§3.2 Streams). |
| 8 | **Connections.** Registry writes (config `create`/`delete`) and app clients (auth `/admin/app-clients*`, `auth.go:170-173`) exist only behind admin tokens. Export Go methods and wrap them. | `GET /api/connections` returns `{listeners:[{name,url,health}], external:[…], apps:[{app_id,name,active,created_at,last_rotated_at}]}`. `POST /api/connections/services` takes `{name,url,health_path}`. `DELETE /api/connections/services/{name}` (external only; 409 for jarvisd's own). `POST /api/connections/apps` takes `{app_id,name}` and returns `{app_id, app_key}` (shown once). `POST /api/connections/apps/{id}/rotate` returns `{app_key}` once. `POST /api/connections/apps/{id}/revoke`. |
| 9 | **Update check (AQ5).** | `GET /api/update` returns `{current_version, latest_version, update_available, updates_enabled, release_url, release_notes, published_at, install_hint}`. `POST /api/update/check` forces a check. `PUT /api/update/settings` takes `{enabled}`. When disabled: no network call, `update_available:false`, `updates_enabled:false` (keep the honesty rule, `src/api/update.ts:10-18`). |
| 10 | **Prompt-provider default (G1).** Not a route: in `main.go` set `cc.DefaultPromptProvider` to the live label's installed model `prompt_provider`. | Surfaced in `/api/setup/state.prompt_provider`. |
| 11 | **Runner handler lookup** for pass-through (§3.2 "Mechanism"). | `module.Deps.Handler(listener) http.Handler`, resolved at request time. Built (A2). |

*As built (A3, `internal/modules/admin/bff.go`; interfaces `SettingsSource`, `TraceStore`, `Accounts`,
`Models`, `PromptProviders`, wired in `cmd/jarvisd/main.go`; a nil one answers 503):*

- **#2** every module with settings has `Settings() *settings.Service`; `settings.Service.List` and
  `SettingResponse` are exported. Modules are sorted by name; `display_name` is a fixed label per module.
  `GET /api/settings` and `/api/settings/` both work (the SPA calls the latter). PUT checks the value
  against the setting's type and `options` (422; `null` clears to the default), 404 for an unknown service
  or key, and audit-logs service+key. `requires_reload` answers `message: "Applies after jarvisd
  restarts"` (AQ8 a). `cc/llm.prompt_provider` goes through cc's validated setter. `llm.<label>.*` keys are
  still listed and writable here; hiding them (I4) is SPA work in A5.
- **#3** cc exports `TraceFilter`, `ParseTraceFilter(url.Values)`, `ListTraces`, `GetTrace(ctx,id) (map,
  found, err)`; the admin-key routes use them too. A bad query is cc's own 400 `validation_error` shape.
- **#4** `doctor.Exposure{Listeners,MQTTAddr,MQTTWSAddr,MDNS}.Ports(cfg.Ports)` replaces `doctorPorts`'
  body (CLI and admin share it). Response `{status: ok|warn|fail, checks, ran_at}`; open while no
  superuser exists, gated after.
- **#5** as listed, minus `modules[]`, plus `go_version`, `started_at` and `listeners: [{name, port,
  served}]` sorted by port. `uptime` is the process's, in seconds. RAM/release/disk come from
  `internal/platform/sysinfo` (x/sys; Windows via `GlobalMemoryStatusEx`).
- **#6** anonymous (or a non-superuser/invalid token, never a 401): `{needs_superuser,
  setup_token_required, version, superuser:false}` plus `setup_token_file` while setup is open. A
  superuser token adds `superuser:true, labels{live,background,embeddings,stt,tts,speaker}, live_ready
  (live is ready|degraded|remote), models_configured (live assigned, even while loading), hardware
  ({hardware, proposal, flavours{llama-server,whisper-server}} from llm `HardwareSummary`, the Hardware
  step's starting point), hardware_url ("/api/llm/v1/hardware"), prompt_provider, doctor{status, failing,
  ran_at}, households, nodes`.
- **AD4** `GET /api/prompt-provider` → `{value, derived, effective, source: "setting"|"model"|"", valid,
  options}`; `PUT /api/prompt-provider {value}` overrides (`""`/`null` clears; unknown → 422). The
  Models page offers the pick-list when `derived` is empty.

*As built (A4; interfaces `LogStore`, `Registry`, `AppClients` wired in `cmd/jarvisd/main.go`; every route
superuser-gated, a nil interface answers 503):*

- **#7 Logs (AD9)**, `internal/modules/admin/logs.go` over logs' new in-process API (`logs.Filter`,
  `Query`, `After`, `LatestID`, `Sources`; `internal/modules/logs/inprocess.go`). Shared filters: `service`,
  `node_id` (context.node_id), `level` (comma list, exact), `min_level` (that level and worse), `since`/`until`
  (RFC 3339), `q` (case-insensitive literal substring of message or context, not a regexp). Bad values are
  422s.
  - `GET /api/logs[?…&limit=1..1000 (default 200)&cursor=]` → `{logs: [{id, timestamp (RFC 3339 UTC), service,
    level, message, context, node_id?}], next_cursor}`, newest first; `next_cursor` (`"<ts>_<id>"`, opaque)
    fetches the next older page, `null` on the last.
  - `GET /api/logs/sources[?since=]` → `{services, nodes, since}` (default the last 24 h), for the dropdowns.
  - `GET /api/logs/stream[?filters&after=<id>]` → `text/event-stream` read with `fetch()` (the bearer header
    rules out `EventSource`): one event per entry (`id: <id>`, `data: <entry JSON>`), `: ping` every 15 s
    when idle, polled every second. Starts after the newest entry; `after=` or `Last-Event-ID` resumes with
    no gap. The legacy app-credential `/api/v0/logs/stream` is unchanged.
- **#8 Connections (AD7)**, `connections.go` over config's `Services`/`ProbeAll`/`AddService`/
  `RemoveService` (`internal/modules/config/inprocess.go`; rows carry `managed`: `listener`, `broker`,
  `setting` (synced from a jarvisd setting, e.g. jarvis-pantry) or `""`) and auth's `AppClients`/
  `CreateAppClient`/`RotateAppClient`/`RevokeAppClient` (`internal/modules/auth/apps_inprocess.go`; the
  legacy `/admin/app-clients*` handlers now call them).
  - `GET /api/connections[?health=false]` → `{listeners: [{name, url, port, managed, health}], external:
    [{name, url, health_path, description, managed, removable, health}], apps: [{app_id, name, is_active,
    created_at, last_rotated_at}]}`. `health` is config's `{healthy, latency_ms, error}` (http rows GET their
    health path, the MQTT broker a TCP connect; all concurrent), `null` with `health=false`.
  - `POST /api/connections/services {name, url, health_path?, description?}` → 201 entry. `url` is a base URL
    (scheme http/https/ws/wss/mqtt/mqtts, host, optional port; no path/user/query); 422 invalid, 409 taken or
    a jarvisd-managed name. `DELETE /api/connections/services/{name}` → 204, 404, 409 for managed rows.
  - `POST /api/connections/apps {app_id, name}` → 201 `{app_id, name, is_active, created_at,
    last_rotated_at, app_key}` (app_id `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`, name 1–128; 409 taken).
    `POST …/apps/{id}/rotate` → `{app_id, app_key, last_rotated_at, is_active: true}` (reactivates, the
    "reissue" action). `POST …/apps/{id}/revoke` → `{app_id, is_active: false}`. Key responses are
    `Cache-Control: no-store`; keys are shown once and never logged (only app_id is audit-logged).
- **#9 Update check (AD5, check only)**, `update.go`. New `admin` settings service (`updates.enabled`, bool,
  default false, env fallback `JARVIS_ALLOW_UPDATES`), listed in the Settings page as "Admin & updates".
  - `GET /api/update` (cached 1 h), `POST /api/update/check` (forced), `PUT /api/update/settings {enabled}`
    (stores it; never checks by itself). All answer `{updates_enabled, checked, reason, checked_at,
    current_version, latest_version, update_available, up_to_date, prerelease, release_url, release_notes,
    published_at, platform ("linux-amd64"…), asset {name, url, size}, checksums_url, install_command,
    install_hint}`.
  - Off: **no outbound request** (tested with a transport that fails the test), `checked: false` + reason.
  - On: GitHub `GET /repos/alexberardi/jarvis-server/releases?per_page=30`; drafts skipped; prereleases only
    when the running build is a prerelease; highest semver wins. `asset` is
    `jarvisd-<tag>-<os>-<arch>.tar.gz` (`.zip` on Windows); `install_command` only when the release carries
    `install.sh` (`curl -fsSL … | sh`) or `install.ps1` (`irm … | iex`), which I8 will publish; until then
    `install_hint` says to download, verify against SHA256SUMS and replace the binary.
  - Honesty (I1): `up_to_date` is true only after a successful check of a comparable version. A failed
    check is `checked: false` with the reason; a dev build is `checked: true` but neither available nor up
    to date, with a reason.
- **AD6 Twilio** (cc, not a BFF route): `internal/modules/cc/phone/telephony.go`, STATUS 2026-10-07 (A4). The system default is
  set through `PUT /api/settings/cc/phone.twilio_*` (masked in `GET /api/settings`); households set their
  own from the app (`PUT /api/v0/mobile/household/{id}/settings/phone.twilio_*`, write-only).

Optional, AQ8: `POST /api/system/restart`. It exits with a restart code when a supervisor is detected
(systemd `INVOCATION_ID`, launchd, a Windows service); otherwise it returns 409 with the command to run.

---

## 7. Invariants and non-obvious behaviour

- **I1. Never claim "up to date" without a check.** The update UI must read `updates_enabled`, not just
  `update_available` (`src/api/update.ts:10-18`; CLAUDE.md config notes). Keep the Updates nav entry
  permanent (`Sidebar.tsx:23-29` explains why it had to be).
- **I2. Updates are opt-in and off by default.** When off, no outbound request at all, the same as the
  current gate (`routes/update.ts:99-106`, `update-checker.ts`).
- **I3. Superuser everywhere.** Every `/api/*` except the bootstrap list in §3.2 checks superuser on the
  server. The SPA's own check (`AuthContext.tsx:135`) is UX only.
- **I4. One validated path for labels.** `llm.<label>.*` keys are written through
  `PUT /v1/models/labels`, which validates the whole body (06 §5). The generic Settings page hides those
  keys (category filter) so they can't be half-edited past validation.
- **I5. Secrets are write-only.** The settings aggregator masks secrets the way the router does. App keys
  and temporary passwords are shown once (`UsersPage.tsx`, `ServicesPage` rotate modal).
- **I6. The SPA fallback must not swallow `/api/*` or `/health`.** Unknown `/api/*` returns a JSON 404;
  every other unknown path serves `index.html` (`app.ts:179-182`, CLAUDE.md invariant 5).
- **I7. Same security headers** as today: CSP `default-src 'self'`, `script-src 'self'`,
  `style-src 'self' 'unsafe-inline'`, `connect-src 'self'`, `frame-ancestors 'none'`, `X-Frame-Options: DENY`,
  `nosniff`, `Referrer-Policy` (`app.ts:113-130`). The built SPA must keep having no inline scripts.
- **I8. Pass-through is an allow-list, not a wildcard**, so the admin port does not become a second copy
  of every module surface.
- **I9. Arg-less POSTs with `Content-Type: application/json` and an empty body are accepted**
  (`app.ts:84-96`, CLAUDE.md invariant 8). The Go JSON decoder in BFF handlers must treat an empty body as
  `{}`.

---

## 8. Oddities

- **O1. Stale docs.** jarvis-admin's CLAUDE.md is wrong in places:
  - It says CORS is `origin: true`. It is an allow-list, default off (`app.ts:98-105`).
  - It says the update check hits ghcr.io. It hits GitHub releases of `alexberardi/jarvis-admin`
    (`update-checker.ts:3,50`).
  - It says nodes come "via command-center". They come from auth (`routes/nodes.ts:10-33`).
- **O2. Dead SPA calls.** `fetch('/api/containers/${id}/logs')` (`InstallStep.tsx:209`) and
  `fetch('/api/models')` (`ReconcilePage.tsx:104`) have no backend route.
- **O3. Quick Sets reads a stale key.** It reads `model.main.context_window` (`quick-sets.ts:104`) while
  it writes `model.live.*`.
- **O4. Admin login ignores `must_change_password`.** It is in the login response (`tokens.go:84`), but a
  superuser holding a temporary password can use the admin without changing it. Users only see a badge
  (`UsersPage.tsx:174`). Port fix: route to a change-password screen (`/api/auth/change-password`
  pass-through).
- **O5. Two token-key conventions.** `jarvis-admin:*` (`AuthContext.tsx:33-35`) versus raw
  `access_token` (`AccountStep.tsx:28-29`, `client.ts:33`).
- **O6. `GET /api/native-services/:id/install` is unauthenticated forever.** It clones a repo and runs
  `deploy-launchd.sh`, with no install-state gate (`native-services.ts:140-145,183`). It is cut, but it is
  the lesson behind §3.2 Streams.
- **O7. The Nodes page shows only the caller's households** (`routes/nodes.ts:10-19`, auth
  `/households`), while Traces uses the cross-household `/superuser/*` views for the same dropdowns.
- **O8. Train Adapter still ships** (`NodesPage.tsx:64`), although LoRA is cut everywhere.

---

## 9. Tests

**Today.** Backend Vitest (`server/tests/`), mostly mocked Docker and Compose, cut with the backend. One
SPA component test: `src/components/wizard/HardwareStep.test.tsx` (cut with that step). The Vitest and
jsdom setup (`vite.config.ts` `test`) is kept and moves with the SPA.

**For the port:**

- **Go unit (`internal/modules/admin`):**
  - static serving: `index.html` fallback, `/api` 404 is JSON, cache headers, explicit MIME types (§11.3)
  - gate: no token is 401, a non-superuser is 403, and the bootstrap allow-list stays reachable
  - pass-through: path rewrite and the allow-list, with an unknown path returning 404
  - BFF handlers against real modules on a temp DB (settings aggregate/put, traces, setup state,
    connections)
- **Contract** (`contract/`, build tag `contract`): a small `admin_test.go` against the jarvisd binary.
  Log in through `/api/auth/login`, then `GET /api/settings`, `/api/admin/users`, `/api/llm/v1/hardware`,
  `/api/traces` and `/api/setup/state`. No Python oracle exists for the BFF paths; the pass-through paths
  inherit their modules' contract tests.
- **SPA:** Vitest component tests for the new wizard steps, the Models page install flow (mocked API) and
  the `errorMessage` helper. `tsc -b` and lint in CI.
- **Fresh-install rehearsal (LD5):** on the MBP (Metal) and this box (CUDA), wipe `~/.jarvisd`, run the
  install script, complete the wizard in a browser, then drive one voice turn with a fake node. Record
  friction in STATUS.

---

## 10. Questions for the user

Asked one at a time from [`QUESTIONS.md`](QUESTIONS.md), which holds the full context. Most
consequential first.

1. **AQ1 [scope] Origin model: a same-origin gateway on 7710, or the SPA calling module ports?**
   *Why:* it decides CORS, whether secrets ever reach the browser, and how much SPA code changes.
   *Options:* (a) the SPA calls 7701/7703/7704 directly with CORS; (b) a loopback reverse proxy;
   (c) an in-process gateway: allow-listed pass-through plus a few Go BFF endpoints.
   **Recommendation: (c).**
2. **AQ2 [behaviour] Protect first-superuser creation with a setup token?**
   *Why:* on a fresh install, anyone on the LAN who reaches `/auth/setup` first owns the box. One binary
   with an install script makes "start it and walk away" more likely.
   *Options:* (a) keep it open, as today; (b) loopback-only until a superuser exists; (c) a one-time token
   printed by jarvisd and the installer (`<home>/setup-token`, 0600), required by `/auth/setup` only
   while no superuser exists. install-e2e `seed.py` would read the file.
   **Recommendation: (c).**
3. **AQ3 [scope] Setup-wizard shape.**
   *Why:* LD5 dogfoods it at the prod cutover.
   *Options:* (a) Check → Account → Models → Done, with hardware inside Models under "Advanced" and
   "Install recommended" as one click; (b) a separate Hardware step; (c) no wizard, just a Models page and
   a banner.
   **Recommendation: (a), with Models skippable.**
4. **AQ4 [behaviour] Prompt provider: derive it from the live model, or keep a setting the operator
   picks?**
   *Why:* an unset `llm.prompt_provider` is a hard error today (G1), and Quick Sets, the old way to set
   it, is cut.
   *Options:* (a) derive it from the live model's catalog/installed `prompt_provider`, show it, and allow
   an override; (b) a required pick in the wizard; (c) keep Quick Sets.
   **Recommendation: (a).** Wire `DefaultPromptProvider`; a hand-registered GGUF with no
   `prompt_provider` gets a pick-list of kept providers.
5. **AQ5 [scope] Updates for jarvisd.**
   *Why:* the old flow self-updated a Docker stack. A single binary can be replaced, but in-place
   replacement under systemd, launchd or a Windows service is real work, plus signing.
   *Options:* (a) check only, opt-in, showing the install-script command; (b) verified in-place
   self-update (minisign, port of `services/upgrade/minisign.ts`, then a supervisor restart); (c) drop
   updates from the admin.
   **Recommendation: (a) in Phase 6, (b) later.** Store the opt-in as the `admin` setting
   `updates.enabled`.
6. **AQ6 [behaviour] Twilio credentials: env-only or secret settings?**
   *Why:* the Service-credentials card has no `.env` to write under jarvisd.
   *Options:* (a) env only, with the card cut and documented; (b) cc secret settings with env fallback,
   edited as write-only rows on the Settings page.
   **Recommendation: (b).** The DB already holds the JWT signing key at 0600.
7. **AQ7 [scope] Keep a "Connections" page (external registry entries plus app clients)?**
   *Why:* jarvisd self-registers its listeners, but external callers (the recipes add-on, a remote GPU
   satellite) still need app credentials and registry rows, which today means curl with admin tokens.
   *Options:* (a) a slim Connections page; (b) cut it and use the CLI.
   **Recommendation: (a).**
8. **AQ8 [behaviour] `requires_reload` settings: restart jarvisd from the admin?**
   *Why:* there is no container to restart. Most llm keys hot-swap, but a few module settings only apply
   at start.
   *Options:* (a) show "applies after restart" plus the command; (b) `POST /api/system/restart` when
   running under a supervisor; (c) audit every `requires_reload` key and make each hot-reload.
   **Recommendation: (a) now, plus (c) as each key is touched.** Add (b) only if the friction shows up at
   cutover.
9. **AQ9 [scope] Add a Logs page?**
   *Why:* Grafana and `docker logs` are gone, so the logs module has no UI.
   *Options:* (a) a simple filter table plus live tail; (b) none, use `/api/v0/logs` with app creds or the
   CLI.
   **Recommendation: (a).**
10. **AQ10 [minor] How to move the code.**
    *Why:* history and the freeze of the old repo.
    *Options:* (a) a plain copy of `src/`, configs and tests at a recorded jarvis-admin SHA, with history
    left in the archived repo; (b) `git subtree`, which drags in `server/`.
    **Recommendation: (a).** jarvis-admin is frozen (fixes only, for the legacy stack) from that SHA until
    the prod cutover, then archived.

---

## 11. Go port notes

### 11.1 Layout and embed

```
web/admin/                    the SPA (from jarvis-admin src/, index.html, public/, package*.json,
                              vite/ts/eslint configs, tests/) — no server/
web/admin/embed.go            package adminui: //go:embed all:dist   (all: keeps Vite's _-prefixed chunks)
web/admin/dist/placeholder.html  committed: "admin UI not built — run `make admin`"
web/admin/dist/ui/            Vite outDir (gitignored); emptyOutDir only cleans this subdir
internal/modules/admin/       listener 7710: static handler, gateway, BFF handlers
```

- `go:embed` cannot reach parent directories, so the embed package sits beside `dist`.
- The handler serves `fs.Sub(dist, "ui")` when `ui/index.html` exists, and otherwise `placeholder.html`
  for every non-API path.
- Result: `go build ./...` and `go test ./...` keep working **without Node** (developers, the existing CI
  test job, the Windows and macOS platform-test jobs). This mirrors how sherpa libraries are fetched
  rather than committed (`internal/voice/sherpa/embed_*.go`), but with a placeholder instead of a build
  tag, because the UI is optional at dev time.
- **Release builds must include it.** The release job builds the SPA first, then runs
  `go test ./web/admin -run TestEmbeddedUI -tags release_ui`, which fails if only the placeholder is
  embedded.

### 11.2 Build and dev workflow

**CI.** A new `admin` job:

- `actions/setup-node` (Node 22 LTS, pinned via `web/admin/.nvmrc`, npm cache)
- `npm ci`, `npm run lint`, `npx tsc -b`, `npx vitest run`, `npm run build`
- upload `web/admin/dist/ui` as an artifact

The release workflow builds the SPA itself (setup-node from `.nvmrc`, `npm ci`, `npm run build`) before
the cross-compile loop, then runs the `release_ui` embed check. *As built (A0):* CI and release are
separate workflows, so release does not download the CI artifact; it rebuilds from the same lockfile.

- The bundle is **platform-independent**: built once on Linux, embedded in all four targets.
- `CGO_ENABLED=0` is untouched, since these are static files.
- jarvisd stays one download. The binary grows by the bundle size. **Measured at A0** (jarvis-admin
  `74e3637`, linux/amd64, `-trimpath -s -w`): `dist/ui` is 637 KB (JS 599 KB, CSS 36 KB, gzip ~171 KB),
  and jarvisd grows from 52,285,732 to 52,924,708 bytes, **+639 KB**.
- `go.mod` has `ignore ./web/admin/node_modules`, so `go build ./...` and `go vet ./...` never walk the
  npm tree (some npm packages ship stray `.go` files).
- No compression step is needed. Optionally gzip assets at build time and serve `.gz` when
  `Accept-Encoding` allows.

**Dev.**

- `cd web/admin && npm run dev`: Vite on :5173 with `server.proxy` for `/api` and `/health` pointed at
  `http://localhost:7710` (`JARVISD_ADMIN_URL` overrides it). Today's config proxies to the Fastify
  backend on 7711 (`vite.config.ts` `server`); only the target changes.
- Hot reload works against a running jarvisd with no CORS, because the browser only talks to Vite.
- Testing the embed path without rebuilding Go: `JARVIS_ADMIN_UI_DIR=web/admin/dist/ui jarvisd serve`
  serves from disk.
- A `make admin` (or `scripts/build-admin.sh`) runs `npm ci && npm run build` for local release-like
  builds.

### 11.3 Windows and path notes

- Embedded paths are always `/`-separated. Use `io/fs`, `path` and `http.FileServerFS`, never
  `path/filepath` or `http.Dir`, on request paths. Then a `..\` or drive-letter request can't escape, and
  behaviour is identical on Windows.
- **Set Content-Type explicitly** for `.js`, `.mjs`, `.css`, `.html`, `.svg`, `.json`, `.woff2`, `.ico` and
  `.png` instead of trusting `mime.TypeByExtension`. On Windows that function consults the registry, where
  `.js` is sometimes `text/plain`, and browsers refuse module scripts served that way.
- Caching: hashed `assets/*` get `Cache-Control: public, max-age=31536000, immutable`; `index.html` gets
  `no-cache`, so an upgraded binary is picked up at once.
  *As built (A1):* every response carries a content-hash `ETag`, so `no-cache` files revalidate with a
  304. A **missing** file under `assets/` is a 404, not the `index.html` fallback, so a stale page asking
  for an old chunk never gets HTML cached as immutable JavaScript. Request paths go through
  `fs.ValidPath` and reject `\` and `:` before reaching the FS (embedded or `JARVIS_ADMIN_UI_DIR`).
  `/health` adds `ui_built` (whether the real UI or the placeholder is served).
- Node is only a CI and dev dependency. A Windows developer runs the same `npm` scripts. The Vite output
  is identical, and line endings don't matter for built assets.

### 11.4 Risks and simplifications

- **Simpler than today.** No URL discovery at startup (CLAUDE.md invariant 2), no admin restart when
  URLs change, no `/auth/me` round trip per request, no Docker socket, no `~/.jarvis/admin.json`.
- **Risk: gateway drift.** Pass-through paths change if a module's routes change. The contract test in §9
  covers the allow-list.
- **Risk: remote GPU satellite.** Managing a satellite's models needs pass-through over HTTP plus
  cross-instance JWT trust. Design it when satellite mode is built (PLAN Phase 6); in the first cut the
  Models page manages the local llm module only.
- **The admin is optional.** `JARVIS_PORT_ADMIN=""` disables the listener, as with any listener.

### 11.5 Port plan (agent-sized, in order)

Each step leaves `main` green and is reviewable alone. Steps A2 to A4 are Go; A5 to A9 are SPA. After A4
they can run in parallel worktrees.

| Step | Scope | Done when |
|---|---|---|
| **A0** | Copy the SPA into `web/admin/` at a recorded jarvis-admin SHA (AQ10). Change Vite `outDir: dist/ui` and the dev proxy target to 7710. Add `.nvmrc`, the `dist/placeholder.html`, `.gitignore` and the CI `admin` job. No behaviour change. | `npm run build` and the CI job are green, the bundle size is recorded, and `go build ./...` still works without Node. |
| **A1** | `internal/modules/admin` skeleton: `ListenerAdmin`/7710/`jarvis-admin` in config and registry, `web/admin/embed.go`, static handler (fallback, MIME table, cache and CSP headers, `JARVIS_ADMIN_UI_DIR`), `GET /health`. Wire it in `main.go`. Add the release-job embed check. | `jarvisd serve` serves the SPA on :7710, doctor lists the admin port, and the static tests from §9 pass. |
| **A2** | Gateway: superuser gate with the bootstrap allow-list, the runner `Handler(listener)` lookup, the pass-through prefix table (§3.2), and the empty-JSON-body rule (I9). | Login through `/api/auth/login` and `/api/admin/users` work against a real jarvisd; gate tests pass. **Done 2026-10-07**, with AD2 (setup token) in the same pass. |
| **A3** | BFF part 1: settings aggregator (module accessors), `/api/system/info`, `/api/traces*` (export cc methods), `/api/doctor` (move `doctorPorts` out of `cmd/`), `/api/setup/state`. Plus G1 (`DefaultPromptProvider` wiring) if AQ4 = (a). | Unit and contract tests pass; Settings, Users, Traces and Nodes render with **no SPA change** beyond S2/S13. **Done 2026-10-07** (G1 was already wired; AD4 route added; see "As built (A3)" under §6.2). Unit + end-to-end tests and a smoke run against a real jarvisd; no `contract/` admin test yet and no SPA change. |
| **A4** | BFF part 2: `/api/logs*` (export logs methods, fetch-stream tail), `/api/connections*` (export config and auth methods), `/api/update*` plus the `admin` settings set (AQ5), Twilio secret settings (AQ6). | Unit tests pass; the opt-in-off path makes no network call (test with a failing transport). **Done 2026-10-07** (see "As built (A4)" under §6.2); no SPA change yet. |
| **A5** | SPA, small edits: the setup token (read `#token=` from the `/setup` URL fragment, strip it from the address bar, send it as `X-Jarvis-Setup-Token` on `POST /api/auth/setup`; on 401/403 ask the operator to paste it from `<home>/setup-token`), the `errorMessage` helper, LoginPage probe removal (S2), AccountStep through `AuthContext.setup` (O5), the must-change-password screen (O4), Nodes on `/api/admin/*` with Train Adapter cut (S13), the Settings restart action replaced (AQ8) with `llm.<label>.*` hidden (I4), the SystemInfoBar fields. | `tsc -b`, lint and Vitest are green, and a manual pass on jarvisd. **Done 2026-10-07.** As built: `lib/errors.ts` (`errorMessage`/`errorStatus`); `auth/setupToken.ts` captures the fragment in `main.tsx` before render (`/setup` only), keeps it in memory + sessionStorage until setup succeeds; AccountStep asks for the token up front when the link had none and on 401/403, naming `setup_token_file`. Boot gate on `/api/setup/state.needs_superuser`; interim `/setup` is the Account step only (A7 builds the full wizard; the old Docker steps are still on disk for A9). LoginPage redirects to `/setup` when `needs_setup` (no first-user form of its own). O4: `/change-password` page, AppShell gate, Header key icon. Nodes: `/api/admin/{households,nodes}` + cc liveness (online, last seen, room); nodes without a known household grouped as "No household". Settings: label keys hidden by category `engine.*` except `engine.downloads` (so `llm.<label>.reasoning_budget`, category `llm.<label>`, stays: the labels PUT is not its path); secrets show set/not set, editor starts empty, Clear sends `null`; **AD8 hook:** `restartAction` in `lib/settings.ts` (null until `POST /api/system/restart` exists; SettingRow then offers it in the toast). ServiceEnvCard deleted (S7). |
| **A6** | SPA Models page rewrite on the model manager (S8): catalog with fit, HF browser, installs with progress, installed with delete/force, labels editor, hardware/engines panel, remote (LD2), HF token prompt, prompt-provider display (AQ4). Delete `data/models.ts`, Quick Sets and LlmSetupWizard. | Install, assign and delete a real small model on this box through the UI; Vitest covers the install flow. **Done 2026-10-07.** As built: `api/llm.ts` (typed client for all 13 routes + prompt provider + `llm.hf_token`), `hooks/useModelManager.ts` (installs polled 1 s while active; a finished install refreshes installed/catalog/labels/prompt/hardware), components in `components/models/` each standalone for A7: `HardwarePanel` (+ `DetectedHardware` for the Hardware step), `CatalogList` (fit + residents, recommended, assign picker pre-checked for recommended labels with no model, projector, "Install recommended" = one install per distinct model; `kinds` prop), `HfBrowser`, `InstallsList`, `InstalledList` (409 → forced delete), `LabelsEditor` (`labels` prop; engine modes, remote with privacy note and write-only key, advanced fields, voice labels), `PromptProviderCard` (AD4), `HfTokenPrompt`; pure helpers in `logic.ts`. Dashboard banner now reads `live == not_configured` and links to `/models`. Verified in headless Chromium against a throwaway jarvisd here (see STATUS). |
| **A7** | SPA setup wizard rewrite (S3, AQ3): Check, Account, Hardware, Models (reusing A6 components), Privacy, Done (AD3, AD3a). App gate on `/api/setup/state` (S1). | A wiped `~/.jarvisd` reaches a working voice turn through the browser only, on CUDA and on Metal. **SPA done 2026-10-07** (voice turn and Metal are A10). As built: `pages/SetupWizard.tsx` + `components/wizard/{CheckStep,AccountStep,HardwareStep,ModelsStep,PrivacyStep,DoneStep}`; step order and resume in `wizard/steps.ts` (sessionStorage, never back before Hardware once signed in). Check = `GET /api/doctor` with copyable fixes, never blocks. Hardware (`wizard/hardware.ts`) starts from `/api/setup/state.hardware` + the labels: flavour radio limited to `flavours["llama-server"]`, STT GPU/CPU (GPU only with a detected GPU and a whisper GPU build), live/background/STT device selects when >1 GPU; "effective" values treat `auto`/empty as the detection/proposal, so "Looks good" writes nothing and changes write the minimal labels PUT (detected flavour → `auto`). Models = InstallsList + CatalogList ("Install recommended") + HF browser (collapsed) + PromptProviderCard, "Skip for now" until live has a model. Privacy (`wizard/privacy.ts`): off-box `cc/web_search.enabled`, `cc/web_scraping.allow_external`, `admin/updates.enabled`; local `cc/memory.enabled`, `cc/memory.extraction_enabled`, `cc/ambient_context.enabled`, `stt/voice.recognition_enabled`; info lines for the push relay (env-only, see STATUS gap), Pantry URL, HF endpoint, remote endpoints; writes only changed keys via `PUT /api/settings`. Done: label states, prompt provider (warns when invalid), doctor problems, next steps (app, QR node, `http://<host>:<config port>`). Verified end to end in headless Chromium on a wiped throwaway home here (CUDA). |
| **A8** | SPA dashboard rewrite (S5) plus the Logs page (S20, AQ9) plus Connections (S11, AQ7) plus Updates (S17, AQ5). | Pages render against jarvisd; no `/api/containers` or `/api/install` references remain (grep). **Done 2026-10-07.** As built: Dashboard (system, doctor, labels + VRAM warnings, nodes online via cc liveness, recent traces, banners from `models_configured`/`live_ready`; setup state polls 3 s while live loads). Logs (`api/logs.ts`, `lib/sse.ts`): infinite query over `next_cursor`, live tail = fetch + ReadableStream + bearer, reconnect with `?after=<last id>` (or the id in the opening `: tail after N` comment), one refresh on 401. Connections: `?health=false` then a probing query; remove only where `removable`; app key modal shown once (create, rotate/reissue), revoke. Updates: `updateStatus()` (disabled/unchecked/available/up_to_date/unknown) is the only source of claims (I1); `PUT /api/update/settings`; install command/hint, asset + SHA256SUMS links. AD8/AD5 buttons are feature-detected (`lib/features.ts`): restart (Settings header + the requires_reload toast) and "Update now" show until the route answers 404 once; restart 202 waits for a new `started_at`, 409 shows `{detail, command}`; apply posts `{version}` and waits up to 5 min for a new process. Nav: Services → Connections (`/services` redirects), + Logs, − Native/Reconcile. |
| **A9** | Cut pass: delete Native, Reconcile and containers hooks and types, ServiceEnvCard, the wizard steps not kept, and dead `api/*` modules. Update `docs/EXTERNAL-CHANGES.md` (the admin rows are done or moot) and the README. | `grep -r "api/(install|containers|native-services|quick-sets|llm-setup|service-env)" web/admin/src` is empty. **Done 2026-10-07**: grep empty; deleted Native, Reconcile and Services pages, containers/install/native/services api + hooks + types, `useInstallStream`, `WizardContext`, the Docker-era wizard steps (Welcome, Services, Review, Install, TerminalOutput; old Hardware step replaced in A7), `ServiceHealthCard`, `ServiceRegistrationRow`. Lint has no warnings left. EXTERNAL-CHANGES admin rows marked done/moot; `web/admin/README.md` lists the pages. |
| **A10** | Fresh-install rehearsal (LD5) with the Phase 6 install script: MBP (Metal), this box (CUDA), and Windows if available. Log friction in STATUS. | Friction list recorded; blockers fixed or filed. |
