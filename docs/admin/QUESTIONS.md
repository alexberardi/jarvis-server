# admin (Phase 6, D9) questions for the user

These are asked **one at a time**, most consequential first. Record each answer here as a decision (AD1,
AD2, …), then fold it into [`00-inventory.md`](00-inventory.md). Technical choices that need no product
call are already made in the inventory: build/embed layout (§11.1), dev workflow (§11.2), Windows MIME and
path handling (§11.3), and error-shape handling (§3.2).

**Already decided upstream, and not re-asked here:**

- **D9.** The SPA is embedded in jarvisd on 7710, the Fastify compose/installer machinery is dropped, and
  the jarvis-admin repo is retired.
- **LD3.** No automatic model download; the model manager is the UI's backend.
- **LD5.** Prod cutover starts clean, through the same first-run path.
- **LD2.** Remote LLMs are OpenAI-compatible only and off by default.
- **D11 / D12.** `llm.prompt_provider` replaces `llm.interface`; the catalog keeps only shipped providers.
- **PLAN §7.** LoRA is cut, which removes Train Adapter.

## Decisions

| # | Date | Question | Decision |
|---|---|---|---|
| AD1 | 2026-10-07 | AQ1 origin model | **(c) in-process gateway on 7710.** Superuser-gated `/api/*`; allow-listed module routes dispatched in process; BFF endpoints call modules through Go interfaces. No CORS, no secrets in the browser. |
| AD2 | 2026-10-07 | AQ2 setup token | **(c) one-time setup token.** `<home>/setup-token` (0600) written on first start; URL `http://<lan-ip>:7710/setup#token=…` logged and printed by the install script; `/auth/setup` requires it only while no superuser exists. User refinement: jarvisd prints **both the raw token and the full link** plainly on stderr (not only as a log field), and opens a browser at the link only when started interactively at a desktop (stderr is a TTY, not under systemd/launchd/a Windows service or SSH, a display is available), best-effort, with an opt-out (`serve --no-browser`, `JARVIS_NO_BROWSER=1`). Built 2026-10-07. |
| AD3 | 2026-10-07 | AQ3 wizard shape | **Check → Account → Hardware → Models → Done** (user asked for a hardware step). Hardware sits after Account (settings writes need the superuser) and before Models (recommendations depend on what is on the GPU). Detected backend/GPUs shown with every choice pre-filled to the detection default and a one-click "Looks good". Choices: engine flavour (only flavours this platform has builds for), STT on GPU/CPU + device, per-label devices when >1 GPU. TTS and speaker ID are CPU-only (embedded sherpa libs are CPU builds), so no toggle is shown; GPU sherpa is a possible later addition. |
| AD10 | 2026-10-07 | AQ10 code move | **(a) plain copy**, done in A0 at jarvis-admin `74e3637`. |

## Queue

### AQ1 [scope] Origin model: a same-origin gateway on 7710, or the SPA calling module ports?

**Context.**

The routes the admin needs are spread over four listeners: auth 7701, config 7700, cc 7703 and llm 7704.
Today Fastify is a backend-for-frontend:

- It forwards the user's JWT to auth, config-service and llm-proxy.
- It uses its **own** CC admin key for traces (`server/src/services/commandCenter.ts`).

In jarvisd two of the needed surfaces sit behind credentials a browser must not hold:

- cc traces need `ADMIN_API_KEY`, which a fresh install doesn't set.
- logs queries need app credentials.

The model-manager routes, on the other hand, are already superuser-guarded with exactly the shapes the UI
needs (docs/llm/06 §5).

**Options.**

- **(a) The SPA calls 7701/7703/7704 directly.**
  - This needs CORS on every listener, a CSP listing every port, and knowledge of port overrides.
  - Traces and logs still need new superuser routes on those listeners.
- **(b) The admin listener reverse-proxies over loopback** to `127.0.0.1:<port>`.
  - Same-origin, but it is a network hop to itself and still can't reach admin-key or app-cred routes.
- **(c) An in-process gateway on 7710.**
  - Serve the SPA.
  - Superuser-gate every `/api/*`.
  - Pass an **allow-list** of existing module routes through to their handlers in process: auth
    `/auth/*` and `/superuser/*`, llm `/v1/models/*` and `/v1/hardware`, config `/services` reads.
  - Add a few Go BFF endpoints (settings aggregate, traces, logs, doctor, setup state, connections,
    system, update) that call modules through Go interfaces.

**Recommendation: (c).**

- No CORS and no secrets in the browser.
- No dependence on `ADMIN_API_KEY`.
- The SPA keeps most of its current `/api/*` paths, including `/api/admin/*` → `/superuser/*`, which is
  exactly what Fastify did.
- Modules stay the single owners of their routes.

### AQ2 [behaviour] Protect first-superuser creation with a setup token?

**Context.**

`POST /auth/setup` creates the first superuser while none exists (`internal/modules/auth/tokens.go:305-340`,
race-safe inside the transaction). Until then it is **open to anyone who can reach port 7701 or 7710**. That
is unchanged from the legacy stack.

With a one-file install it gets more likely that someone starts jarvisd and comes back later, and the
admin's Check step plus the mDNS advertisement make the box easy to find on the LAN.

**Options.**

- **(a) Keep it open**, as today.
- **(b) Loopback-only until a superuser exists.** This breaks the common case of a headless box set up
  from a laptop.
- **(c) A one-time setup token.**
  - jarvisd writes `~/.jarvis/setup-token` (0600) on first start, logs a URL
    `http://<lan-ip>:7710/setup#token=…`, and the install script prints it.
  - `/auth/setup` requires the token **only while no superuser exists**; after that, setup returns 409
    as today.
  - install-e2e `seed.py` reads the file; the mobile app never calls `/auth/setup`, as far as a grep
    shows.

**Recommendation: (c).**

- It costs one file and one header.
- It closes the "first on the LAN wins" hole without hurting the headless-box flow.
- The `#fragment` keeps the token out of server logs.

### AQ3 [scope] Setup-wizard shape

**Context.**

Today's wizard has seven steps, and four of them exist only for Docker: Welcome/Docker check, Services,
Review, Install. Under jarvisd the real first-run work is:

- confirm nodes and phones can reach the box (doctor: listeners and firewall)
- create the superuser
- install models: live (plus projector), background (often shared), embeddings, stt, tts, speaker

LD5 says the prod cutover goes through this path so its pain is felt.

**Options.**

- **(a) Check → Account → Models → Done.**
  - Hardware detection and per-label GPU assignment live inside Models under "Advanced".
  - "Install recommended" is one click: one install per distinct recommended model, with labels
    assigned and the live projector included.
  - Models is skippable; the dashboard banner nags.
- **(b) A separate Hardware step** before Models: GPUs, proposal and per-label devices.
- **(c) No wizard**: only the Models page plus a banner.

**Recommendation: (a).**

- It is the shortest path to a working voice turn.
- Most users never need the GPU details: the proposal is already right for one card, two cards and Apple.
- Multi-GPU users (prod) open "Advanced" once.

### AQ4 [behaviour] Prompt provider: derive it from the live model, or keep an operator-picked setting?

> **Update 2026-10-07:** option (a)'s wiring is already done (commit 987f4ea: `llm.LivePromptProvider` → `cc.DefaultPromptProvider`), since it was a fresh-install blocker either way. The question left is only whether admin also offers an override/pick-list.

**Context.**

`llm.prompt_provider` unset is a hard error (D11; `internal/modules/cc/warmup.go:132-138`).
`cc.Module.DefaultPromptProvider` exists for this case (`cc.go:122-124`), but `main.go` never wires it. So
a fresh jarvisd fails its first voice turn until someone sets a key that no admin screen offers once Quick
Sets is cut.

Catalog and installed models already carry `prompt_provider` (docs/llm/06 §5).

**Options.**

- **(a) Derive and allow override.**
  - Wire `DefaultPromptProvider` to the live label's installed model `prompt_provider`.
  - The Models page shows the effective provider and its source ("from model" or "set by you") and
    allows an override.
  - A hand-registered GGUF with no `prompt_provider` shows a pick-list of the kept providers.
- **(b) A required pick** in the wizard's Models step.
- **(c) Keep Quick Sets** as presets that write it.

**Recommendation: (a).** Zero clicks for catalog models, an explicit choice only when unknowable, and
nothing new to keep in sync. This also settles EXTERNAL-CHANGES' "wizard and quick-sets write
`llm.prompt_provider`" row as moot.

### AQ5 [scope] Updates for jarvisd

**Context.**

Today's Updates page:

- checks GitHub releases (of the jarvis-admin repo), opt-in and off by default
- self-updates the whole Docker or native stack, with minisign-verified artifacts
  (`server/src/services/upgrade/*`)

The honesty rule stays: never say "up to date" when no check ran.

For jarvisd an update means replacing one binary, then restarting it under systemd, launchd or a Windows
service. Windows can't overwrite a running .exe; the usual trick is rename, write, restart. The extracted
native libraries are version-hashed, so they are safe.

**Options.**

- **(a) Check only.**
  - Opt-in, against `alexberardi/jarvis-server` releases.
  - The admin shows the new version, the notes and the install-script command to run.
- **(b) Verified in-place self-update.**
  - Download the right asset, verify it with minisign (port `minisign.ts` to Go with `crypto/ed25519` and
    BLAKE2b), swap it in, and exit for the supervisor to restart.
  - Progress is streamed with fetch, not EventSource.
- **(c) No updates in the admin**: the install script only.

**Recommendation: (a) in Phase 6, (b) once release signing and the install scripts exist.** Store the
opt-in as the `admin` setting `updates.enabled` (env `JARVIS_ALLOW_UPDATES` as fallback), replacing
`~/.jarvis/admin.json`.

### AQ6 [behaviour] Twilio credentials: env-only or secret settings?

**Context.**

The "Service credentials" card wrote the phone gateway's Twilio credentials into the stack `.env`, then
recreated the container (`server/src/routes/service-env.ts`).

In jarvisd the gateway is absorbed (docs/cc D16) and reads `TWILIO_ACCOUNT_SID`, `TWILIO_AUTH_TOKEN` and
`TWILIO_FROM_NUMBER` from the environment (`cmd/jarvisd/phone.go:76-91`, whose comment says secrets stay
out of the DB, as they did in the gateway). A single binary has no `.env` the admin could edit; the
variables live in a systemd unit, a launchd plist or a Windows service definition.

**Options.**

- **(a) Env only.** Cut the card and document setting the variables in the service definition.
- **(b) cc secret settings with env fallback** (`phone.twilio_account_sid`, `phone.twilio_auth_token`,
  `phone.twilio_from_number`).
  - Edited as write-only rows on the Settings page; the phone service re-reads them on change.

**Recommendation: (b).**

- The same DB already holds the RS256 signing key at 0600, so it is no weaker than the env.
- Editing a service unit by hand is exactly the papercut the old card was built to remove.

### AQ7 [scope] Keep a "Connections" page (external registry entries and app clients)?

**Context.**

jarvisd registers its own listeners in the config registry at startup, so the old "register all services"
and "probe" flows have nothing to do. Two things are still external:

- The **recipes add-on** (stays Python, PLAN §2) and a **remote GPU satellite** need registry rows.
- Both need **app credentials** to call jarvisd (PLAN §3.1).

Today, outside the old Services page, that means curl with `X-Admin-Token` (config) or
`X-Jarvis-Admin-Token` (auth).

**Options.**

- **(a) A slim Connections page.**
  - jarvisd listeners, read-only with health.
  - External services: add and remove.
  - App clients: list, create, rotate and revoke, with the key shown once.
  - Backed by `/api/connections/*`, which calls config and auth in process.
- **(b) Cut it.** Document the CLI/curl path.

**Recommendation: (a).** It is small, and it is the only way a non-technical operator connects the
recipes add-on.

### AQ8 [behaviour] `requires_reload` settings: restart jarvisd from the admin?

**Context.**

The Settings page offered "Restart" by restarting the owning container.

- The llm module's keys are all `requires_reload`, but the resolver hot-swaps only the affected engine,
  so no restart is needed (docs/llm/06 §3).
- Other modules may have keys that apply only at start.

**Options.**

- **(a) No restart button.** Show "applies after restart" plus the command for the detected supervisor.
- **(b) `POST /api/system/restart`.** jarvisd exits with a restart code when it detects systemd, launchd
  or a Windows service, and returns 409 with the command otherwise.
- **(c) Audit every `requires_reload` key** and make each one hot-reload.

**Recommendation: (a) now, plus (c) as each key is touched.** Add (b) only if the cutover shows the
friction. A self-restart button on a single-process server is easy to get wrong: unsupervised runs,
`run.sh` in a terminal.

### AQ9 [scope] Add a Logs page?

**Context.**

Grafana and `docker logs` are gone. The logs module stores every module's records plus node logs and
serves `/api/v0/logs` (query) and `/api/v0/logs/stream` (SSE tail), but only to app credentials. Today the
admin has no log view; the installer's "show logs" button even hits a route that doesn't exist.

**Options.**

- **(a) A simple page.** Filter by service (module or node), level, time and text, plus live tail, via
  superuser BFF routes.
- **(b) None.** Use the CLI or curl with app credentials.

**Recommendation: (a).** It is the first thing to open when a voice turn fails, and it replaces two tools
(Grafana and the containers view).

### AQ10 [minor] How to move the code

**Context.**

D9 retires the jarvis-admin repo. It still serves the legacy Docker stack until the prod cutover.

**Options.**

- **(a) Plain copy.** Copy `src/`, `index.html`, `public/`, the configs and `tests/` into `web/admin/` at
  a recorded jarvis-admin SHA, leaving history in the archived repo.
- **(b) `git subtree add`.** Keeps history, but drags `server/` and Docker files in to be deleted again.

**Recommendation: (a).**

- Freeze jarvis-admin from that SHA: fixes only, for the legacy stack.
- Archive it at the prod cutover.
- Record the SHA in the A0 commit message.
