# install 00 — installers, service registration, upgrades (Phase 6)

Sources (read-only):

- `/home/alex/jarvis/jarvis-installer` (the browser wizard that generates compose bundles)
- `/home/alex/jarvis/jarvis-admin/install.sh` and `install.ps1` (the admin binary's installers)
- `/home/alex/jarvis/jarvis` (the `./jarvis` dev/ops CLI, a 3564-line bash script)
- `/home/alex/jarvis/jarvis-node-setup/install.sh` (the Pi node's release-tarball installer)

Legacy paths are relative to `/home/alex/jarvis/`. Paths starting with `cmd/`, `internal/`, `docs/` or
`.github/` are in this repo.

Decisions being implemented: PLAN §3.4 (one file per OS; "install scripts: `install.sh` for
systemd/launchd, `install.ps1` for a Windows service or scheduled task"), PLAN Phase 6, D9 (admin absorbed,
the first-run wizard lives in the embedded SPA), LD3 (no automatic model download), LD5 (prod cutover goes
through the fresh-install path), STATUS install-UX finding 1 (`jarvisd doctor`; "the installers run it and
offer to apply the fix"), admin AQ2 (setup token), AQ3 (wizard), AQ5 (updates), AQ6 (Twilio).

**Fate vocabulary** as in `docs/admin/00-inventory.md`: **KEEP**, **CHANGE**, **CUT**, **NEW**.

User principles that shape every choice below: *download one file*; native Windows; self-hosted and
private (no cloud dependency by default); never restrict members on their own install.

---

## 0. Summary

Everything the legacy tooling does falls into four buckets, and three of them disappear:

| Legacy job | Where today | Under jarvisd |
|---|---|---|
| Generate a compose stack, pull images, start containers in tiers, register services, stamp `.env` files | jarvis-installer, `./jarvis init/start`, admin Fastify | **CUT.** One process, self-registering listeners, one SQLite file. |
| Generate ~13 secrets and fan them out to per-service `.env` files | `./jarvis` `_generate_tokens`, `secret-generator.ts` | **CUT from the installer.** jarvisd already makes the only one it needs (the RS256 key); the rest are closed-when-unset or move to settings (§3). |
| Install a binary, register an autostart, print a URL | admin `install.sh`/`install.ps1` | **CHANGE.** Kept as the thin outer shell, with checksum verification, a real service on every OS, an upgrade and rollback path, and an uninstall. |
| Health, doctor, port checks | `./jarvis status/health/doctor` | **CHANGE.** `jarvisd doctor` (exists) plus the admin Check step. |

**Recommendation in one paragraph.** The install scripts stay tiny (~150 lines each): detect OS/arch,
download the archive and `SHA256SUMS` from the GitHub release, verify, place the binary, and hand off to
**`jarvisd service install`**, a new subcommand that writes the systemd unit, the launchd plist or the
Windows SCM registration in Go, tested on all four CI runners. The scripts then run `jarvisd doctor`,
offer `jarvisd doctor --fix` for the firewall, and print the admin URL with the setup token (AQ2).
jarvisd itself generates every secret on first start; the installer writes **no secrets**. Service data
lives in a `jarvisd`-named directory that cannot collide with anything the legacy stack owns (§2.0).
Upgrades are the same script re-run (or `jarvisd upgrade`), with a DB snapshot, a kept previous binary
and a health-gated rollback. Questions are in §7 and [`QUESTIONS.md`](QUESTIONS.md).

---

## 1. What the legacy tooling does, and the fate of each piece

### 1.1 jarvis-installer (static React wizard on GitHub Pages)

Not a script: a browser SPA at `installer.jarvisautomation.io` that generates compose files client-side
(`jarvis-installer/README.md:3-16`, `public/CNAME`). Its `CLAUDE.md:40` mentions an `install.sh` that does
not exist in the repo (stale).

| Piece | Citation | Fate |
|---|---|---|
| Landing page: download buttons for the jarvis-admin binary per OS, and the `curl … jarvis-admin/main/install.sh \| sh` one-liner | `src/components/landing/LandingPage.tsx:3-8,74,92` | **CHANGE.** The page links `jarvis-server` release assets and the jarvisd one-liners (§2). No darwin-x64 button today; jarvisd ships darwin/arm64 only (`release.yml:64`), so Intel Macs stay unsupported. |
| Wizard: Modules → Configuration → Output | `src/App.tsx:20-24` | **CUT.** No modules to pick; every module is in the binary. |
| Secret generation (13 keys, `crypto.getRandomValues` → hex, 16 B for passwords, 32 B otherwise) | `src/lib/secret-generator.ts:5-56` | **CUT.** Postgres/Redis/MinIO/Grafana/MQTT passwords have no jarvisd equivalent; the rest are covered in §3. |
| Port table and in-browser conflict check (only among chosen ports, never the host) | `public/service-registry.json:9-648`, `src/lib/port-utils.ts:14-32` | **CUT.** jarvisd binds every listener before serving and fails cleanly on a clash (`internal/platform/module/runner.go:65-67,105-108`); doctor and the installer's preflight report it (§5.2). |
| GPU type, Whisper/TTS backend, release track, digest pinning | `ConfigurationStep.tsx:245-330`, `env-generator.ts:97-117` | **CUT.** GPU detection is automatic in the model manager (`docs/llm/06-model-manager-api.md`); the release track becomes the installer's `--channel` (§4.1). |
| Relay URL, default `https://relay.jarvisautomation.io` | `ConfigurationStep.tsx`, `env-generator.ts:99` (admin) | **CHANGE.** Becomes a setting with an explicit opt-in (IQ8). |
| Output: `jarvis.zip` with compose + plaintext `.env` + `init-db.sh` | `src/lib/zip-bundle.ts:13-26`, `env-generator.ts:19-23` | **CUT.** |
| Compose-export: one YAML, per-service app keys (48 B, bcrypt-seeded into auth by inline Python), storage under `/var/lib/jarvis` | `compose-export-generator.ts:13-22,68-74,861-895`; `WizardContext.tsx:20` | **CUT.** Note `/var/lib/jarvis` as a path a legacy NAS install may own (§2.0). |
| CI boot-smoke before Pages deploy; dispatch to install-e2e | `.github/workflows/deploy.yml:46-120`, `trigger-install-e2e.yml:21-33` | **CHANGE.** The equivalent is the jarvisd release workflow's per-OS verify job (`release.yml:95-126`) plus an install-script job (I4). |

Oddities worth not repeating: `TTS_KOKORO_DEVICE: "${TTS_BACKEND:-cpu}"` in a file that has no `.env`, so
it is always `cpu` (`compose-export-generator.ts:711`); zip mode never provisions app keys; the default
output assumes the NVIDIA Container Toolkit (`WizardContext.tsx:21-22`).

### 1.2 jarvis-admin `install.sh` (POSIX sh, 332 lines)

Order: parse args → prereqs → platform → latest version → install binary → version file → PATH →
autostart → success (`install.sh:318-330`).

| Piece | Citation | Fate |
|---|---|---|
| `curl … \| sh` one-liner | `:5` | **KEEP** the shape: `curl -fsSL <url>/install.sh \| sh`. The script self-elevates with `sudo` for the system service (§2.1) rather than being piped to `sudo sh`. |
| Docker check (warn only) | `:66-75` | **CUT.** No Docker. |
| OS/arch: darwin/linux; x86_64→x64, aarch64→arm64; TrueNAS detection | `:24-52` | **CHANGE.** Map to the release names `linux-amd64`, `linux-arm64`, `darwin-arm64` (`release.yml:64-65`). Refuse darwin/x86_64 with a clear message. TrueNAS: see §2.1 notes. |
| Latest version from the GitHub API by grep/sed | `:56-57` | **CHANGE.** Use the redirect of `releases/latest/download/…` (no API, no rate limit), or `--version vX.Y.Z`. |
| Download straight to `~/.jarvis/bin/jarvis-admin`, `chmod +x`, **no checksum**, overwrite in place while running | `:79-88` | **CHANGE.** Download to a temp file on the target disk, verify against `SHA256SUMS`, extract, then swap atomically with the previous binary kept (§4). |
| `public.tar.gz` UI assets | `:92-98` | **CUT.** The SPA is embedded (D9). |
| `~/.jarvis/admin.json` `installedVersion`, merged by sed (duplicates the key on every run; `\+` is GNU-only) | `:102-117` | **CUT.** `jarvisd version` is the truth. |
| PATH: appends to `~/.zshrc` or `~/.bashrc` only | `:120-140` | **CUT.** The binary goes to a directory already on PATH (`/usr/local/bin`). |
| Linux: systemd **user** unit, `loginctl enable-linger`, `After=docker.service` (no effect from a user unit) | `:175-206` | **CHANGE.** System unit by default, `--user` mode kept as an option (§2.1). Written by `jarvisd service install`, not by a heredoc. |
| macOS: **LaunchAgent** `~/Library/LaunchAgents/com.jarvis.admin.plist`, legacy `launchctl unload/load` | `:208-266` | **CHANGE.** LaunchDaemon with `UserName` (§2.2), `launchctl bootstrap/bootout`. |
| TrueNAS / fallback: `nohup`; fallback writes into `~/.jarvis/logs` without creating it (fails under `set -e`) | `:144-173,268-274` | **CUT.** No unsupervised mode in the installer; `jarvisd serve` in a terminal is the manual path. |
| End message: `http://localhost:7711` | `:278-288` | **CHANGE.** Print `http://<lan-ip>:7710/setup#token=…` (AQ2) and the doctor result. |
| Not present: checksum, firewall, port-conflict check, uninstall, Gatekeeper, elevation | (agent survey) | **NEW** for all of them (§2, §4, §6). |

### 1.3 jarvis-admin `install.ps1` (137 lines)

| Piece | Citation | Fate |
|---|---|---|
| `irm … \| iex` one-liner | `:2` | **KEEP** the shape. |
| Hard-coded `windows-x64`, no detection | `:9,123` | **CHANGE.** `windows-amd64` (`release.yml:64`); refuse ARM64 with a message (x64 emulation is possible but untested with the purego DLLs). |
| Binary to `%USERPROFILE%\.jarvis\bin`; `public.tar.gz` via Windows `tar` | `:38-59` | **CHANGE.** `%ProgramFiles%\jarvisd\jarvisd.exe`; no UI archive. |
| No checksum, no `Unblock-File`, no Authenticode | `:26-64` | **NEW.** `Get-FileHash` against `SHA256SUMS`; `Unblock-File` to drop Mark-of-the-Web; Authenticode later (IQ9). |
| User PATH prepend | `:85-92` | **CHANGE.** Machine PATH entry for `%ProgramFiles%\jarvisd` (the installer is elevated anyway). |
| No autostart; `Start-Process -Environment` (PowerShell 7.3+ only; fails under 5.1, which `irm \| iex` usually runs); stdout and stderr redirected to the same file (rejected) | `:95-114,106-108` | **CHANGE.** A Windows service via SCM (§2.3). Script must run on Windows PowerShell 5.1. |
| `Write-Err` calls `exit 1`, which closes the user's shell under `iex` | `:13` | **CHANGE.** `throw` inside a script block; never `exit` from `iex`. |
| Overwriting a running exe fails (locked) | (no stop step) | **CHANGE.** Stop the service first (§4.2). |

### 1.4 The `./jarvis` CLI (bash, dev/ops)

The CLI assumes sibling repo checkouts (`jarvis:26,656`) and keeps state in `~/.jarvis/{pids,logs}`
(`:27-28`), master secrets in `~/.jarvis/tokens.env` and DB names in `~/.jarvis/databases.env` (`:29-31`).

| Subcommand / piece | Citation | Fate |
|---|---|---|
| `SERVICES` registry with tiers 0–5 and health paths | `:40-59` | **CUT.** One process; the runner orders module start internally. |
| macOS "local mode" for llm-proxy/tts/ocr: a backgrounded `run.sh` with a PID file (no launchd, despite comments at `:285,2343`) | `:121-134,2167-2171,313-327` | **CUT.** jarvisd runs natively everywhere; Metal comes from the llama-server/whisper-server engines it supervises. |
| `init` step 1: `_generate_tokens`: 8 × `openssl rand -hex 32` (AUTH_SECRET_KEY, JARVIS_AUTH_ADMIN_TOKEN, JARVIS_CONFIG_ADMIN_TOKEN, ADMIN_API_KEY, MODEL_SERVICE_TOKEN, LLM_PROXY_INTERNAL_TOKEN, JARVIS_ADAPTER_CALLBACK_TOKEN, RELAY_JWT_SECRET), RSA-2048 `AUTH_PRIVATE_KEY` via `openssl genpkey … \| base64 -w 0` (GNU-only), uuid4 `DEFAULT_HOUSEHOLD_ID`; `tokens.env` chmod 600 | `:543-603` | **CUT** (§3.1 maps each one). The RS256 key is generated by jarvisd on first start (`internal/modules/auth/keys.go:35-70`). |
| `init` steps 2–8: DB names, `.env` stamping per repo, container recreate, local venvs, docker network, infra with **hard-coded weak passwords** (`POSTGRES_PASSWORD=postgres`, `MINIO_ROOT_PASSWORD=minioadmin`, …), `create-db.sh`, alembic | `:447-1009`, weak defaults `:871-891` | **CUT.** goose migrations run at start (`runner.go:69`), SQLite needs no password. |
| `start --all`: tiered `docker compose up`, 60 s health waits, then `_auto_register` writing `JARVIS_APP_KEY` into each `.env`, then force-recreate | `:2479-2626,2284-2477` | **CUT.** jarvisd self-registers its listeners (`cmd/jarvisd/main.go:79-85,111-118`). |
| `stop`, `restart`, `rebuild` | `:2709,3515,3520` | **CHANGE.** `jarvisd service stop/start/restart` wrapping systemctl/launchctl/SCM. |
| `status`, `health` (curl each `/health`) | `:2750,2782` | **CHANGE.** `jarvisd service status` (supervisor state + `GET :7700/health`) and `jarvisd doctor`. |
| `logs <svc>` (`docker compose logs -f`, or `tail -f` local logs) | `:2808-2843` | **CHANGE.** `jarvisd logs [-f] [--module X]` reading the logs module's store (also the admin Logs page, AQ9); the supervisor log (journald / launchd file / Windows file) for crashes before the store opens. |
| `llm-setup` wizard: hardware probe, `huggingface_hub` download, settings PUT, restart | `:1204-1957` | **CUT.** Model manager + admin Models step (LD3, AQ3). |
| `doctor`: tool versions, postgres/redis/minio pings, `.env` presence, admin-token and JWT-secret consistency across `.env` files, `ss -tlnp` port conflicts, health | `:2861-3040` | **CHANGE.** `jarvisd doctor` (exists, `internal/doctor`) keeps the two checks that still mean something: listeners and port conflicts, plus the host firewall it already reads. A "legacy stack still running" check is NEW (§5.2). |
| `add-service` (register in config + create an app client + write `.env`) | `:3329-3418` | **CHANGE.** Admin Connections page (AQ7); optionally `jarvisd app-client create` for scripts. |
| `test`, `pantry` | `:3210`, `:3552` | **CUT** from the server CLI (dev tooling). |
| Absent: upgrade, uninstall, reset, firewall | (survey) | **NEW** (§4, §6). |

The CLI itself stays for the legacy stack until the prod cutover (PLAN Phase 6 "Update the `./jarvis`
CLI"): **CHANGE** it to print a pointer to jarvisd for `init`/`quickstart` once jarvisd releases exist,
then retire it with the Python repos (Phase 7).

### 1.5 jarvis-node-setup `install.sh` (the pattern to copy)

This is the best installer in the tree, and most of its upgrade discipline carries over to one binary.

| Pattern | Citation | Use in jarvisd |
|---|---|---|
| `--version TAG`, else latest; skip when the installed `VERSION` matches unless `--force` | `:157-186` | **KEEP.** Compare with `jarvisd version`. |
| Asset name `<name>-${VERSION}-${ARCH}.tar.gz` | `:575-576` | **KEEP.** jarvisd's are `jarvisd-${VERSION}-${os}-${arch}.tar.gz` / `.zip` (`release.yml:65`). |
| Download to a file on the target disk (not `/tmp` tmpfs, not `curl \| tar`, after a silent HTTP/2 truncation), HEAD size check, `gzip -t` | `:586-647` | **KEEP.** |
| minisign-signed `checksums.txt` with an embedded public key (key id `725ba202b54fa2c9`, shared with jarvis-admin's self-updater); warn when missing, fail when invalid, hash mismatch always fatal | `:480-566`, CI `.github/workflows/release.yml:108-133` | **CHANGE.** jarvisd's release has `SHA256SUMS` today (`release.yml:78`). Add `SHA256SUMS.minisig` signed with the **same key** (custody already exists). The shell/PowerShell installers verify the hash (TLS-anchored); `jarvisd upgrade` verifies the signature with the key embedded in the *running* binary (§4.1). |
| Move current install to `.bak`, extract, critical-file gate, roll back on failure; EXIT trap restores `.bak` | `:55-65,579-584,738-808` | **KEEP**, collapsed to `jarvisd` + `jarvisd.prev` and an atomic rename. |
| Health-gated upgrade: `ActiveState=active` with `NRestarts` unchanged for 15 s within 120 s, else roll back the binary and the unit | `:1905-1983` | **KEEP**, plus `GET :7700/health` and `jarvisd doctor --json` listeners OK. |
| Self-update from inside the service via `systemd-run --unit=…-update --collect --no-block`, with a `/proc/self/cgroup` guard; installer URL pinned to the tag | `scripts/jarvis-self-update`, `:2166-2171` | **KEEP** for the later in-admin self-update (AQ5 b). |
| System unit rendered from a template; `StartLimit*` in `[Unit]`; `Restart=always`; dedicated `User=`; `SyslogIdentifier` | `:1806-1893` | **KEEP** (written by `jarvisd service install`). |
| Config files created only if missing; user data carried forward from `.bak` | `:665-715,1168-1189` | **KEEP** in spirit: jarvisd's data never lives next to the binary, so nothing needs carrying. |
| Hard-coded `User=pi`; no uninstall; no firewall; no x86_64 | `:67-75,1550-1551,121-126` | Don't repeat. |

---

## 2. Per OS

### 2.0 Shared layout and naming

**Everything is named `jarvisd`, never `jarvis`.** The legacy stack owns `~/.jarvis/` (compose,
`tokens.env`, `admin.json`, `.models/` holding 150 GB on prod, `docs/llm/05-models-and-downloads.md:63`),
`/var/lib/jarvis` (compose-export default, `jarvis-installer/src/context/WizardContext.tsx:20`), the
service names `jarvis-admin`/`com.jarvis.admin`, and on prod a login user called `jarvis`. jarvisd's
default `JARVIS_HOME` is `~/.jarvis` (`internal/platform/config/config.go:76-82`), which shares a directory
with all of that. Sharing is not a file clash today (jarvisd writes `jarvis.db`, `blobs/`, `engines/`,
`models/`, `lib/`), but a legacy `rm -rf ~/.jarvis` "reset" would take jarvisd's DB with it, and an
uninstall `--purge` of jarvisd would take the legacy stack's models. The dev box already runs from
`~/.jarvisd` for exactly this reason (STATUS session log, "jarvisd runs as the dev server on this box
(`~/.jarvisd`, `run.sh`…)").

| | Linux (system) | Linux (`--user`) | macOS | Windows |
|---|---|---|---|---|
| Binary | `/usr/local/bin/jarvisd` | `~/.local/bin/jarvisd` | `/usr/local/bin/jarvisd` | `%ProgramFiles%\jarvisd\jarvisd.exe` |
| Previous binary | `/usr/local/lib/jarvisd/jarvisd.prev` | `~/.local/lib/jarvisd/jarvisd.prev` | `/usr/local/lib/jarvisd/jarvisd.prev` | `%ProgramFiles%\jarvisd\jarvisd.prev.exe` |
| Data (`JARVIS_HOME`) | `/var/lib/jarvisd` | `~/.jarvisd` | `~/.jarvisd` of the service user | `%ProgramData%\jarvisd` |
| Env file | `/etc/jarvisd/jarvisd.env` (0640 root:jarvisd) | `~/.jarvisd/jarvisd.env` (0600) | `~/.jarvisd/jarvisd.env` (0600) | `%ProgramData%\jarvisd\jarvisd.env` (ACL: SYSTEM, Administrators, service SID) |
| Supervisor | `jarvisd.service` system unit | `jarvisd.service` user unit + linger | `/Library/LaunchDaemons/net.jarvisautomation.jarvisd.plist` | SCM service `jarvisd` |
| Logs before the store opens | journald | journald (user) | `~/.jarvisd/logs/jarvisd.log` (`StandardErrorPath`) | `%ProgramData%\jarvisd\logs\jarvisd.log` (rotated by jarvisd) |

IQ1 asks whether this is the right account and directory model; the table is the recommendation.

**The env file is read by jarvisd itself (NEW, I1).** Today jarvisd reads only the process environment
(`cmd/jarvisd/main.go:47-77`, `config.go:70-72`), so the dev box needs `run.sh` to `source` an env file. A
service unit could use `EnvironmentFile=`, but then `jarvisd doctor` run from a shell would not see the
same ports, and launchd/SCM have no equivalent. Rule: resolve the home (`--home` flag, else
`JARVIS_HOME`, else the per-OS service default when a service is installed, else `~/.jarvisd`), then load
`<home>/jarvisd.env` (Linux system mode: `/etc/jarvisd/jarvisd.env`) for any variable **not already set
in the process environment**. The service definitions then carry only `--home`. A fresh install writes an
env file containing comments only: it needs no variables at all (§3).

**Changing the code default** from `~/.jarvis` to `~/.jarvisd` (I1) keeps a bare `jarvisd serve` off the
legacy directory too. Docs that say `~/.jarvis` (decision log, `docs/llm/06` paths) mean "the data dir".

**File permissions (gap found).** The decision log says the DB is "0600 under `~/.jarvis`", but nothing
enforces it: `db.Open` creates only the directory 0700 and only if missing (`internal/platform/db/db.go:31`),
and on this box `~/.jarvisd/jarvis.db`, `-wal` and `-shm` are `-rw-r--r--`. The DB holds the RS256
private key (`internal/modules/auth/keys.go:58-60`), password hashes and, after AQ6, Twilio secrets. I1
sets `umask 077` at process start on Unix, chmods an existing home to 0700 and the DB files to 0600, and
`doctor` warns when either is wider. `engines/` and `lib/` are created 0755 (`internal/voice/sherpa/extract.go:43`,
`internal/modules/llm/engine/download.go:77`); that is fine inside a 0700 home.

### 2.1 Linux (systemd)

**System unit (default).** `jarvisd service install` (run as root by `install.sh`) does:

1. Create the system user `jarvisd` (`useradd --system --home-dir /var/lib/jarvisd --shell /usr/sbin/nologin`),
   adding it to `video` and `render` when those groups exist (AMD/Intel GPUs and Vulkan need
   `/dev/dri/renderD*`; NVIDIA's `/dev/nvidia*` are normally 0666). The name avoids prod's existing
   `jarvis` login user.
2. Create `/var/lib/jarvisd` 0700 and `/etc/jarvisd/jarvisd.env`.
3. Write `/etc/systemd/system/jarvisd.service`:

   ```ini
   [Unit]
   Description=Jarvis server (jarvisd)
   After=network-online.target
   Wants=network-online.target
   StartLimitIntervalSec=600
   StartLimitBurst=10

   [Service]
   Type=notify                      # jarvisd sends READY=1 once every listener is bound (I1)
   User=jarvisd
   Group=jarvisd
   ExecStart=/usr/local/bin/jarvisd serve --home /var/lib/jarvisd
   Restart=always
   RestartSec=5
   TimeoutStopSec=30                # engines drain (STATUS 2026-10-06: drain on restart)
   KillMode=mixed                   # SIGTERM to jarvisd; it stops its own engine children
   LimitNOFILE=65536
   UMask=0077
   NoNewPrivileges=yes
   ProtectSystem=strict
   ReadWritePaths=/var/lib/jarvisd
   ProtectHome=yes
   PrivateTmp=yes
   SyslogIdentifier=jarvisd

   [Install]
   WantedBy=multi-user.target
   ```

   `ProtectSystem=strict` is safe because everything jarvisd writes is under its home (extracted libs,
   engines, models, blobs, DB). `PrivateDevices` must **not** be set (GPU device nodes). `Type=notify`
   uses jarvisd's own `sd_notify` over `NOTIFY_SOCKET` (no dependency; built in I1, see §8.1).
4. `systemctl daemon-reload && systemctl enable --now jarvisd`.

**`--user` mode.** For a box where the user has no sudo or wants models in their home: a user unit in
`~/.config/systemd/user/jarvisd.service` (same body minus `User=`, `ProtectSystem`, hardening that user
managers can't apply), `loginctl enable-linger $USER` (needs polkit or sudo on some distros; the script
says so when it fails, unlike the admin installer which swallowed the error at `install.sh:200-204`). The
firewall fix still needs sudo once.

**Upgrades** replace `/usr/local/bin/jarvisd` while the service is stopped (§4). **Uninstall**:
`jarvisd service uninstall` disables and removes the unit, removes the binary and `jarvisd.prev`, removes
the firewall rules it added (§6.1, identified by the `jarvisd` comment), and keeps `/var/lib/jarvisd` and
`/etc/jarvisd` unless `--purge` (§4.4).

**TrueNAS SCALE / other appliance distros** (the admin installer special-cased TrueNAS, `install.sh:44-51,144-173`):
`/usr/local` is read-only there and apps are expected as containers. Out of scope for the first cut; the
minimal container image in PLAN §3.4 is the answer for NAS users (I8 notes it).

**Distros without systemd** (Alpine/OpenRC, WSL1): the script refuses with "run `jarvisd serve` under your
own supervisor", printing the one-line command.

### 2.2 macOS (launchd)

**Agent vs daemon.**

| | LaunchAgent (`~/Library/LaunchAgents`) | LaunchDaemon (`/Library/LaunchDaemons`) with `UserName` |
|---|---|---|
| Runs without anyone logged in | No (a Mac mini server after a power cut sits dead until someone logs in) | Yes |
| Needs admin at install | No | Yes (one `sudo`) |
| **Local Network privacy** (macOS 15+; mDNS advertisement and outbound LAN connections such as Home Assistant or a remote GPU satellite) | Subject to it: a prompt that a background agent may never surface, so mDNS silently fails | Exempt: Apple TN3179 lists launchd daemons among the processes local network privacy doesn't apply to (**verify in I0**) |
| Metal | Works (the MBP runs it today from a terminal) | Expected to work for compute; **verify in I0** on the MBP |
| Application firewall | Prompts per binary; a background agent can't answer | Same; the installer adds the binary with `socketfilterfw --add` (§6.1) |

**Recommendation: a LaunchDaemon running as the installing user** (`UserName` = `$SUDO_USER`), data in
that user's `~/.jarvisd`. A dedicated role user (`dscl` + a hidden `_jarvisd`) buys little on a personal
Mac and makes models and logs harder to find.

```xml
<key>Label</key><string>net.jarvisautomation.jarvisd</string>
<key>UserName</key><string>alex</string>
<key>ProgramArguments</key><array>
  <string>/usr/local/bin/jarvisd</string><string>serve</string>
  <string>--home</string><string>/Users/alex/.jarvisd</string></array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><true/>
<key>ProcessType</key><string>Interactive</string>   <!-- no background throttling of a GPU server -->
<key>Umask</key><integer>63</integer>                <!-- 077 -->
<key>SoftResourceLimits</key><dict><key>NumberOfFiles</key><integer>65536</integer></dict>
<key>StandardErrorPath</key><string>/Users/alex/.jarvisd/logs/jarvisd.log</string>
```

Loaded with `launchctl bootstrap system <plist>` and `launchctl enable system/net.jarvisautomation.jarvisd`
(not the deprecated `load`/`unload` used at `jarvis-admin/install.sh:263-264`). The label uses a domain the
project controls rather than `com.jarvis.*` (the legacy admin's `com.jarvis.admin` stays distinct).

**Gatekeeper.** Release binaries are signed with the project's Developer ID (hardened runtime,
identifier `net.jarvisautomation.jarvisd`) and notarized (ID9; release.yml `sign-macos`, §8.4). A
**browser** download of the `.tar.gz` is quarantined, and Gatekeeper lets the notarized binary run
(a bare Mach-O can't be stapled, so the first assessment looks the ticket up online; offline, a
quarantined copy is refused until the Mac has been online once). `curl` (the one-liner) and
`jarvisd upgrade` don't set `com.apple.quarantine`, so those paths never meet Gatekeeper. Engines
jarvisd downloads itself (llama-server, whisper-server) are fetched over HTTP by Go and are not
quarantined; they are separate executables with their own signatures. Dry-run builds without the
Apple secrets keep the Go linker's ad-hoc signature (`xattr -d com.apple.quarantine jarvisd` then).

**Application firewall.** It admits or blocks *programs*, not ports (`internal/doctor/firewall.go:205-243`).
A Developer ID signature has a stable designated requirement (identifier + team), so the allow entry
survives upgrades; ad-hoc builds (dry runs, source builds) change it every build, which is why the
upgrade path still checks and re-runs `socketfilterfw --add` when needed (§4.2, §8.2).

**Sleep.** A Mac acting as a server should not sleep. The installer prints (does not run)
`sudo pmset -a sleep 0` when `pmset -g` shows sleep enabled on AC; doctor can warn (I2).

### 2.3 Windows

**Service vs scheduled task vs Startup folder.**

| | SCM service | Scheduled task "at startup" (SYSTEM) or "at logon" (user) | Startup folder |
|---|---|---|---|
| Runs before logon | Yes | At startup: yes; at logon: no | No |
| Restart on crash | SCM recovery actions | Task "restart on failure" settings (coarser) | No |
| Stop/start for upgrades | `sc stop` / `Stop-Service`, clean | `schtasks /end` kills the process (no graceful stop) | Kill |
| Needs Go code | **Yes**: the process must answer the SCM (`golang.org/x/sys/windows/svc`; `x/sys` is already in `go.mod`) | No | No |
| GPU | Session 0. CUDA compute works in services; Vulkan in session 0 is less certain. **Verify in I0** | At logon: user session, certain | User session |

**Recommendation: an SCM service**, with the small Go support it needs (I1):

- `jarvisd serve` checks `svc.IsWindowsService()`; when true it runs under `svc.Run`, maps
  Stop/Shutdown to the same context cancel as SIGTERM (`cmd/jarvisd/main.go:280`), and reports
  `StartPending → Running` once listeners are bound.
- `jarvisd service install` registers it with `mgr.CreateService`: start type automatic (delayed start,
  so the network is up), recovery actions restart after 5 s / 30 s / 60 s, description, and
  `ServiceSidType = unrestricted` so the env-file ACL and firewall rule can name the service SID.
- **Account: `NT SERVICE\jarvisd` (a virtual account)**, not LocalSystem. It has no password, no
  interactive rights and its own profile, and can be granted exactly `%ProgramData%\jarvisd`. Important
  detail: under any service account `os.UserHomeDir()` is the system profile
  (`C:\Windows\System32\config\systemprofile` for LocalSystem), so the service **must** pass
  `--home %ProgramData%\jarvisd`; the code default would otherwise put a 100 GB model store in
  `System32`.
- Logs: stderr goes nowhere under SCM, so jarvisd writes `<home>\logs\jarvisd.log` with size rotation
  when running as a service (plus the logs module's store as usual). Event Log only for start failures.

If I0 shows CUDA or Vulkan does not work in session 0, the fallback is a scheduled task at logon for the
installing user (no Go change; loses run-before-logon). That is the second option in IQ2.

**Firewall.** `install.ps1` is elevated, so it applies the rule directly through `jarvisd doctor --fix`:
`netsh advfirewall firewall add rule name="jarvisd" dir=in action=allow program="<exe>" enable=yes
profile=private` (`internal/doctor/firewall.go:293-296`). A program rule covers every port and survives
port overrides. Engines bind loopback only, so they need no rule (verify in I0 by `netstat` on Windows).
If the active network profile is **Public**, the rule does not apply: doctor must say so and print
`Set-NetConnectionProfile -NetworkCategory Private` (NEW check, I2).

**Defender and SmartScreen.**

- `irm | iex` downloads with `Invoke-WebRequest`, which does not add Mark-of-the-Web; a browser download
  of the zip does, and SmartScreen warns on an unsigned exe. `install.ps1` runs `Unblock-File` on what it
  downloads.
- Defender real-time scanning of the extracted sherpa DLLs (`<home>\lib\sherpa-<hash>\`,
  `internal/voice/sherpa/extract.go:43-56`) and of downloaded `llama-server.exe`/CUDA DLLs is the
  likeliest false-positive source (PLAN §3.4 already plans Authenticode). The installer does **not** add
  Defender exclusions (that weakens the user's machine); doctor reports a blocked engine with the
  Defender history pointer instead.
- Authenticode signing of `jarvisd.exe` is IQ9. The extracted DLLs are upstream-built; signing them
  ourselves is possible (they are embedded) but changes their hash.

**Paths.** `%ProgramFiles%\jarvisd\jarvisd.exe` (machine PATH entry added), data `%ProgramData%\jarvisd`
(ACL: SYSTEM, Administrators full; `NT SERVICE\jarvisd` modify; Users none). Long paths: Hugging Face
file names under `models\<owner>--<repo>\` can approach `MAX_PATH`; Go handles `\\?\` internally for its
own calls, but child engines receive paths as arguments, so keep `%ProgramData%\jarvisd` short and test a
long repo name in I0.

**Script constraints.** `install.ps1` must run on Windows PowerShell 5.1 (`irm | iex` default): no
`Start-Process -Environment`, no `&&`, `[Net.ServicePointManager]::SecurityProtocol` set to TLS 1.2 before
downloading, `throw` instead of `exit`, and self-elevation with
`Start-Process powershell -Verb RunAs -ArgumentList '-File', $tmpScript` when not elevated.

---

## 3. Secrets and bootstrap

### 3.1 Every secret and bootstrap value today

From every `os.Getenv` in `cmd/jarvisd/main.go` and `phone.go`, plus the legacy generators.

| Variable | Read at | Unset means | Legacy generator | Under jarvisd |
|---|---|---|---|---|
| RS256 signing key (was `AUTH_PRIVATE_KEY`) | DB `auth_signing_keys` (`internal/modules/auth/keys.go:35-70`) | generated on first start | `./jarvis:551-553` | **Done.** jarvisd generates it. Import copies the legacy key (decision log 2026-10-06). |
| `AUTH_SECRET_KEY` (HS256) | `main.go:73` | HS256 neither minted nor verified | `./jarvis:578-599`, `secret-generator.ts` | **Import only.** Never set on a fresh install. After import it should move into the DB as a verify-only key like the RS256 one (I7), so the env file holds no secret. |
| `JARVIS_AUTH_ADMIN_TOKEN` | `main.go:71` | `/admin/*` rejects everything (`internal/modules/auth/auth.go:80`) | hex32 | **Unset by default** (IQ4). |
| `JARVIS_CONFIG_ADMIN_TOKEN` | `main.go:47` | registry writes answer 500 "Admin token not configured" (`internal/modules/config/config.go:519-521`) | hex32 | **Unset by default** (IQ4). |
| `ADMIN_API_KEY` (cc, notifications) | `main.go:56,62` | admin routes reject (`internal/platform/authn/principal.go:75-78`) | hex32 | **Unset by default** (IQ4). The admin SPA no longer needs it (admin AQ1 gateway). |
| `JARVIS_APP_ID` / `JARVIS_APP_KEY` | `main.go:134,145` | outbound OCR/LLM job callbacks unsigned (`internal/modules/ocr/jobs.go:403-406`, `internal/modules/llm/jobs.go:281-284`) | registration (`./jarvis:2414-2420`) | **Done** (branch `fix/jarvisd-own-app-client`): with the env unset, the first OCR/LLM callback mints an `auth` app-client row `jarvisd` (`auth.SelfAppCreds`); the DB holds only its hash, the key lives in `<home>/app-key` (0600). A key that stops validating (revoked on the Connections page, restored DB) is reissued by rotation. The env pair still wins when set. |
| `TWILIO_ACCOUNT_SID/AUTH_TOKEN/FROM_NUMBER` | `phone.go:16` | no phone provider | admin service-env card | **Secret settings with env fallback** (admin AQ6). |
| `JARVIS_PHONE_PUBLIC_URL`, `…_WSS_URL` | `phone.go:18-19` | no public media URL | — | Setting with env fallback (same change as AQ6). |
| `RELAY_URL`, `RELAY_HOUSEHOLD_JWT` | `main.go:57-59` | **push delivery silently disabled** (`internal/modules/notifications/notifications.go:99-101`) | `env-generator.ts:99` defaulted to the public relay | Setting `notifications.relay_url`, opt-in in the wizard (IQ8). |
| `JARVIS_OSX_API_URL/KEY` | `main.go:138` | no Apple Vision OCR | — | Setting with env fallback (no installer involvement). |
| `JARVIS_MQTT_ADDR`, `…_WS_ADDR`, `JARVIS_MQTT_ALLOW_ANONYMOUS` | `main.go:64-66` | `:1884`/`:9883`, anonymous off | — | Bootstrap env, unchanged. |
| `JARVIS_MDNS` | `main.go:48` | advertise | — | Bootstrap env. |
| `JARVIS_HOME`, `JARVIS_HOST`, `JARVIS_PORT_*` | `config.go:65-97` | defaults | — | Bootstrap env; `--home` flag added (I1). |
| `JARVIS_BLOB_STORE`, `JARVIS_LOG_LEVEL`, `JARVIS_ADMIN_UI_DIR` | `main.go:253,267,77` | defaults | — | Bootstrap/dev env. |
| Setup token (AQ2) | NEW | — | — | jarvisd writes `<home>/setup-token` (0600) on first start while no superuser exists; the installer reads and prints it. |
| Postgres/Redis/MinIO/Grafana/MQTT passwords, `MODEL_SERVICE_TOKEN`, `LLM_PROXY_INTERNAL_TOKEN`, `JARVIS_ADAPTER_CALLBACK_TOKEN`, `RELAY_JWT_SECRET`, `DEFAULT_HOUSEHOLD_ID` | — | — | `./jarvis:578-599`, `secret-generator.ts:11-42` | **CUT.** No such components (in-process calls; per-node MQTT creds, D4; "My Home" household created at setup, `internal/modules/auth/tokens.go:338`). |

### 3.2 Rule: the installer generates nothing

Everything that must exist is generated by jarvisd on first start, inside the DB or its home, at 0600:

- the RS256 key (done),
- the setup token (AQ2),
- jarvisd's own app client (I3),
- the install ID used by the update check's `User-Agent` (if AQ5 wants one; optional).

Why: the same binary started by hand, by a service, in CI or in a container behaves identically; the
install scripts stay ~150 lines and contain no crypto (the legacy `base64 -w 0` macOS bug,
`./jarvis:551-553`, is the kind of thing that goes away); and a reinstall over an existing home never
rotates a key by accident.

### 3.3 Where secrets live

| Secret | Store | Permissions |
|---|---|---|
| Signing key, password hashes, app-client hashes, setup token hash, Twilio (AQ6), HF token (`llm.hf_token`) | `<home>/jarvis.db` | 0600, home 0700 (I0 enforces) |
| Setup token plaintext (until the superuser exists, then deleted) | `<home>/setup-token` | 0600 |
| Optional env overrides (admin tokens if IQ4 (a), legacy `AUTH_SECRET_KEY` before I7) | `jarvisd.env` | Linux system 0640 root:jarvisd; others 0600 / ACL |

Backups (§4.3) contain the signing key; they are written 0600 beside the DB.

---

## 4. Upgrades, rollback, migrations, uninstall

### 4.1 Getting and verifying a release

*Built 2026-10-07: §8.2 is what `jarvisd upgrade` and the admin button actually do; it supersedes the
sequence below where they differ (the swap happens before the restart, a privileged systemd pre-start
helper does it for the system unit, and the health gate runs inside the new jarvisd).*

- **Default channel: latest non-prerelease** via `https://github.com/alexberardi/jarvis-server/releases/latest/download/<asset>`
  (a redirect; no API call, no rate limit). `--version vX.Y.Z` pins; `--channel pre` picks the newest
  prerelease through the API (prereleases are marked by `release.yml:145`).
  *A10b R1:* the documented command is `curl -fsSLo install.sh …/releases/latest/download/install.sh && sh install.sh`
  (download, then run: piped, a 404 ran an empty script and exited 0). While only release candidates
  exist, `releases/latest` 404s; an rc is installed from its own URL (`releases/download/<tag>/install.sh`,
  flag-free since R2). Engine-build releases (`engines-whisper-*`) are published as prereleases so they
  are never `latest` (GitHub falls back to the newest full release even with `--latest=false`).
- **Verify `SHA256SUMS`** (`release.yml:78`): `sha256sum -c --ignore-missing` / `shasum -a 256` on macOS /
  `Get-FileHash` on Windows. Missing or mismatched is fatal. Both files come over TLS from the same
  origin, so this guards against truncation and mirror corruption, not a compromised release.
- **Signature (admin AQ5 b).** Add `SHA256SUMS.minisig` to the release, signed with the existing project
  key (`jarvis-node-setup/install.sh:480-491`). Verify it **in Go** inside `jarvisd upgrade`: the
  *running* binary carries the public key and checks the new one before swapping, which is a real chain
  of trust; a freshly downloaded binary checking itself is not. The shell installers verify the
  signature only if `minisign` happens to be installed (warn otherwise), as node-setup does
  (`:522-529`).
- Download to `<target dir>/.jarvisd.download` on the same filesystem (node-setup's lesson, `:586-599`),
  check size, extract, run `./jarvisd version` from the extracted copy (proves it runs on this CPU/OS)
  before touching anything.

### 4.2 The upgrade sequence (script re-run, or `jarvisd upgrade`)

1. Preflight: new version > installed (else exit 0 unless `--force`); free disk ≥ 2 × DB size + 200 MB.
2. **Snapshot the DB**: `jarvisd backup --to <home>/backups/jarvis-<oldver>-<ts>.db` using SQLite
   `VACUUM INTO` (online and consistent, works with jarvisd running). Keep the last 3. Models, engines
   and blobs are not copied (immutable or re-downloadable; blobs are append-mostly).
3. Stop the service (`systemctl stop` / `launchctl bootout` / `Stop-Service`; wait for exit so Windows
   releases the exe lock and engines are drained).
4. Move the current binary to `jarvisd.prev`; rename the new one into place (atomic on the same FS).
5. macOS: re-add the binary to the application firewall (§2.2). Windows: the program-path rule still
   matches (same path).
6. Start; wait up to 120 s for `GET http://127.0.0.1:7700/health` **and** the supervisor reporting stable
   (systemd `NRestarts` unchanged for 15 s, node-setup `:1935-1959`); then `jarvisd doctor --json`
   listeners OK.
7. On failure: stop, restore `jarvisd.prev`, **restore the snapshot only if the new binary applied
   migrations** (compare `jarvisd migrate status` before/after), start, report "rolled back to X; the
   failing version's log is at …".

`jarvisd upgrade` run from the admin (AQ5 b, later) must not be a child of the service it stops: on Linux
`systemd-run --unit=jarvisd-upgrade --collect` (node-setup `scripts/jarvis-self-update`); on macOS a
one-shot `launchctl submit`; on Windows a one-shot scheduled task. That is why AQ5 defers it.

### 4.3 Data migrations and downgrades

- goose runs every module's migrations at start (`internal/platform/module/runner.go:69`), with
  out-of-order allowed (`internal/platform/db/migrate.go:30`, decision log 2026-10-07). So an upgrade
  needs no separate migrate step, and `jarvisd migrate status` shows the state.
- **Downgrade guard (NEW, I5).** An older binary started on a DB that a newer one migrated has unknown
  versions in `goose_<module>`. What goose's provider does then is unverified; jarvisd should refuse to
  start with "this database was upgraded by vX; restore `<home>/backups/…` or install vX" rather than run
  against a schema it doesn't know. Hence the snapshot in step 2.
- Migrations must stay forward-only and additive within a release series so that a `jarvisd.prev`
  rollback *without* restoring the snapshot works for minor versions; a destructive migration is called
  out in release notes and forces the restore path. (Policy for module authors; noted in I5.)

### 4.4 Uninstall

`install.sh --uninstall` / `install.ps1 -Uninstall` → `jarvisd service uninstall [--purge]`:

- Stop and remove the unit/plist/service, the firewall rules jarvisd added (ufw rules carrying the
  `jarvisd` comment, the firewalld rich rules, the `jarvisd` netsh rule, the socketfilterfw entry), the
  binary, `jarvisd.prev`, the PATH entry on Windows, the `jarvisd` system user on Linux (only with
  `--purge`, since it owns the data).
- **Keep data by default** and print its path and size (models can be 100+ GB).
- `--purge` deletes the home and env file after a typed confirmation of the path (or `--yes` for
  scripts). It never touches `~/.jarvis` (legacy) — the §2.0 naming makes that structural, not a check.

---

## 5. Legacy Docker stack: migration and coexistence

### 5.1 Migration (prod cutover and anyone else on the Docker stack)

LD5 says prod's **models and LLM settings** start clean (the model manager re-downloads; the old
`~/.jarvis/.models/` is left alone). It does not settle **user data**: accounts, households, nodes and
their keys, memories, routines, phonebook, inbox. PLAN §5 calls `import-legacy` "wanted, not required",
and the per-table plan exists (`docs/schema/cc.md:94-120`, `docs/schema/*.md`).

What not importing costs: every family member re-creates an account and re-joins a household; every
node is factory-reset and re-provisioned (finding 5: reset wipes Wi-Fi, a re-join is needed); every
phone re-pairs; memories are lost. What importing costs: a Postgres client in jarvisd (pure Go `pgx`,
no cgo) and the transforms in `docs/schema`.

Recommended scope (IQ6): **import identity and user data, not models or engine settings.**

- `jarvisd import-legacy --from postgres://… [--dry-run]` runs **before the first `serve`** on an empty
  home (it refuses a DB with a superuser), reads each legacy DB at alembic head and writes the module
  tables. Auth includes node rows and keys and both legacy JWT keys (sessions survive, decision log
  2026-10-06), so nodes and phones keep working: they find the same host on the same ports.
- Settings: only user-facing ones (persona, household settings, TTS voice); the LLM keys are skipped per
  LD5 (`llm.interface` → nothing; the wizard's Models step sets labels).
- Blobs: copied from MinIO/SeaweedFS via S3 GET (PLAN §5).
- The admin wizard then sees a superuser and skips Account (admin §3.3 `/api/setup/state`), and Models
  runs exactly as on a fresh install, which is the pain LD5 wants to see.
- The installer does not run it: it prints the command when it detects a legacy stack (§5.2).

### 5.2 Coexistence on one box

jarvisd uses the same ports as the legacy stack by design (`config.go:29-42`): 7700–7707, 7712, 7030,
7031, 7710, and MQTT 1884/9883, which legacy mosquitto also publishes
(`jarvis-admin/server/src/services/generators/compose-generator.ts:359,384-405`). Prod listens on both
7710 and 7711 today (legacy admin: Fastify backend on 7711, `server/src/config.ts:112`; 7710 from
`server/src/index.ts:86`). jarvisd does not use 7711. Because the runner binds every listener before
serving, any one clash stops jarvisd from starting at all (`runner.go:105-108`), which is good: it never
half-runs next to legacy.

Installer preflight (I6):

1. **Detect legacy**: running containers named `jarvis-*` (`docker ps --format '{{.Names}}'` when docker
   exists), `~/.jarvis/compose` or `~/.jarvis/tokens.env`, and any jarvisd port held by a process that is
   not jarvisd (`ss -ltnp` / `lsof -iTCP -sTCP:LISTEN` / `Get-NetTCPConnection`).
2. If found, stop and explain, offering:
   - `--stop-legacy`: `docker compose stop` in the detected project directory (**stop, never `down -v`**;
     data stays for `import-legacy` and for rolling back to legacy), and disable its restart policy so a
     reboot doesn't bring it back;
   - or exit, printing the import command and how to stop legacy by hand.
3. Side-by-side on alternate ports is a developer case only (`JARVIS_PORT_*`, `JARVIS_MQTT_*`, as the
   MBP contract runs did); the installer does not offer it, since nodes and phones only ever find one
   config service per host.

`jarvisd doctor` gains the same "another program holds port N" check (today it only reports "nothing
answers", `internal/doctor/doctor.go:92-96`, which is wrong when *legacy* answers).

Rollback to legacy after a cutover: stop jarvisd (`jarvisd service stop`), `docker compose start`. The
legacy data was never modified by the import.

---

## 6. Doctor, firewall, GPU prefetch, mDNS

### 6.1 `jarvisd doctor` in the install flow

What exists: listener dial checks and per-subnet host-firewall checks for ufw (reading files, no root),
firewalld, the macOS application firewall and Windows Defender Firewall, each with a `Fix` string
(`internal/doctor/doctor.go:64-153`, `firewall.go`), `--json` output (`cmd/jarvisd/doctor.go:44-52`), exit 1
on any failure.

Install flow:

1. After the service starts, the installer runs `jarvisd doctor --json` (as the invoking user; ufw files
   are world-readable, `firewall.go:42`).
2. If a firewall check fails or warns, **ask** "Allow nodes and phones on 10.0.0.0/24 to reach jarvisd?
   [Y/n]" and run `sudo jarvisd doctor --fix` (Windows: already elevated). Non-interactive (`curl | sh`
   without a TTY, CI): apply only with `--yes`; otherwise print the fix. IQ5.
3. `--fix` is NEW (I2): doctor applies its own `FixFor` instead of printing it. The fix becomes a list of
   argv commands in the `Check` (`fix_cmds` in JSON) so neither the scripts nor the admin parse
   human text. It refuses to run without privileges and only ever admits **private LAN subnets**
   (`LocalLANs`, `doctor.go:163-188`), never `0.0.0.0/0`.
4. Rules are tagged (`comment jarvisd` in ufw, rule name `jarvisd` in netsh) so uninstall can remove them.

New doctor checks for install (I2): port held by another program (§5.2), Windows network profile is
Public (§2.3), DB/home permissions (§2.0), macOS sleep on AC (§2.2), legacy stack running (§5.2), "running
unsupervised" (info only).

Doctor's port list comes from the config (`cmd/jarvisd/doctor.go:17-36`), which is why the CLI must see
the service's env file (§2.0).

The admin Check step (admin §3.3) shows the same JSON through `/api/doctor`; jarvisd runs unprivileged
and cannot apply the fix from the browser, so the page shows the command with a copy button.

### 6.2 GPU engines and models: no prefetch

LD3 settles models: none downloaded automatically. Engines (llama-server, whisper-server, a few hundred
MB per flavour incl. the CUDA runtime) are fetched by the model manager with the first install that needs
them (`release.yml:7-8`, `docs/llm/06-model-manager-api.md` "engines"). The installer does **not**
prefetch either:

- the flavour depends on detection that jarvisd already does better than a shell script
  (`internal/modules/llm/engine/gpu.go`);
- the wizard's "Install recommended" is one click a minute later, with progress;
- a box used only as a control plane (LLM on a satellite or a remote endpoint, LD2) never needs them.

Offline/air-gapped install (copy engines and models from another box) is out of scope; the model manager's
"register an existing file" path (`docs/llm/05-models-and-downloads.md:156`) covers models.

The installer **does** print what it detected cheaply (`jarvisd` itself can expose `jarvisd hardware`
reusing the llm module's detection) so the user knows before opening the browser whether the GPU was seen
(e.g. "NVIDIA RTX 3090 ×2 (CUDA)", "Apple M2 Max (Metal)", "no GPU: CPU only, expect slow replies").
Optional; I3.

### 6.3 mDNS

jarvisd advertises `_jarvis-config._tcp` (`internal/platform/mdns`, `cmd/jarvisd/main.go:48`) using
`grandcat/zeroconf`, which binds UDP 5353 alongside the OS responder.

- **Linux:** coexists with avahi-daemon (both use `SO_REUSEPORT` on 5353). Firewall needs 5353/udp, which
  doctor already includes (`cmd/jarvisd/doctor.go:33-35`).
- **macOS:** coexists with mDNSResponder; the LaunchDaemon choice avoids the Local Network privacy prompt
  (§2.2). To verify in I0.
- **Windows:** the DNS Client service also answers mDNS on 5353; zeroconf's reuse should coexist, and the
  program firewall rule covers UDP. To verify in I0 by browsing from the Android app.
- `JARVIS_MDNS=0` stays the opt-out. The installer prints the hostname the phone will see.

---

## 7. Questions for the user

Asked one at a time from [`QUESTIONS.md`](QUESTIONS.md), which holds the full context. Most consequential
first.

1. **IQ1 [scope] Service account and data directory per OS.**
   *Why:* decides sudo at install, GPU access, where 100+ GB of models live, and whether jarvisd and the
   legacy stack can ever delete each other's data.
   *Options:* (a) dedicated system account: Linux `jarvisd` user + `/var/lib/jarvisd`; macOS LaunchDaemon
   as the installing user + `~/.jarvisd`; Windows `NT SERVICE\jarvisd` + `%ProgramData%\jarvisd`;
   (b) always the installing user and `~/.jarvisd` (Linux system unit with `User=$SUDO_USER`); (c) user-level
   supervisors everywhere (systemd `--user` + linger, LaunchAgent, logon task), no admin rights.
   Plus: rename everything `jarvisd` and change the code default from `~/.jarvis` to `~/.jarvisd`.
   **Recommendation: (a), with (c)'s Linux `--user` mode as a flag, and the rename.**
2. **IQ2 [scope] Windows: SCM service (needs `svc` support in Go) or a scheduled task?**
   *Why:* run-before-logon, crash restart and clean stop for upgrades vs GPU certainty in the user session.
   *Options:* (a) SCM service under a virtual account; (b) logon scheduled task; (c) (a) with automatic
   fallback to (b) when I0 finds no GPU in session 0.
   **Recommendation: (a), decided after the I0 session-0 GPU check; (b) only if CUDA fails there.**
3. **IQ3 [behaviour] Thin installers: jarvisd generates every secret and owns service registration?**
   *Why:* where install logic lives decides how it is tested (Go on 4 CI runners vs shell/PowerShell).
   *Options:* (a) scripts download/verify/place, then `jarvisd service install`; jarvisd generates every
   secret on first start; jarvisd reads its own env file; (b) scripts write units/plists/SCM entries and
   generate tokens, as `./jarvis init` did.
   **Recommendation: (a).**
4. **IQ4 [behaviour] Legacy admin tokens (`JARVIS_AUTH_ADMIN_TOKEN`, `JARVIS_CONFIG_ADMIN_TOKEN`,
   `ADMIN_API_KEY`) on a fresh install.**
   *Why:* unset closes those routes; only scripts (install-e2e, recipes registration, mcp) use them now
   that the admin goes through the gateway.
   *Options:* (a) unset (closed); `jarvisd admin-token create <scope>` writes one to the env file when a
   script needs it; (b) generated at first start into the DB and shown in the admin; (c) generated by
   the installer.
   **Recommendation: (a).**
5. **IQ5 [behaviour] Firewall: should the installer apply the fix?**
   *Why:* finding 1, the top fresh-install failure, but it edits system firewall config.
   *Options:* (a) ask, default yes; apply with `--yes` when non-interactive; LAN subnets only; tagged for
   uninstall; (b) always apply; (c) print only.
   **Recommendation: (a).**
6. **IQ6 [scope] Cutover data: `import-legacy` for identity and user data, or a clean start for
   everything?**
   *Why:* LD5 covers models; without user import every account, node and phone is redone and memories
   are lost.
   *Options:* (a) import users/households/nodes/keys/memories/routines/phonebook/inbox before first
   start; models and LLM settings clean (LD5); (b) start clean, re-provision all nodes; (c) (a) for prod
   only, not offered to others.
   **Recommendation: (a), offered to everyone on the legacy stack.**
7. **IQ7 [behaviour] A legacy stack on the same box.**
   *Why:* same ports; jarvisd refuses to start on any clash.
   *Options:* (a) detect, refuse, offer `--stop-legacy` (compose stop, data kept); (b) alternate ports
   side by side; (c) remove legacy.
   **Recommendation: (a).**
8. **IQ8 [behaviour] Push relay: on by default?**
   *Why:* `RELAY_URL` unset means pushes are silently dropped; set means each push leaves the house
   through the project's relay.
   *Options:* (a) opt-in in the wizard's Done step, explaining what leaves the box; (b) on by default
   (legacy installer); (c) env-only.
   **Recommendation: (a), as the setting `notifications.relay_url`.**
9. **IQ9 [scope] Code signing: Apple notarization and Windows Authenticode now?**
   *Why:* browser downloads are blocked or warned about without it; it costs money and key custody.
   *Options:* (a) one-liner installers only in Phase 6, signing when friends-and-family start
   downloading from a browser; (b) sign and notarize from the first release; (c) never.
   **Recommendation: (a).** minisign for the checksums now, since the key already exists.
10. **IQ10 [behaviour] Upgrade safety.**
    *Why:* migrations are forward-only; a bad release must not strand prod.
    *Options:* (a) installer re-run = upgrade with a `VACUUM INTO` snapshot, `jarvisd.prev`, a
    health-gated automatic rollback and a downgrade guard; (b) plain replace, manual recovery.
    **Recommendation: (a).**

---

## 8. Port plan (agent-sized, in order)

Each step leaves `main` green and is reviewable alone. I0 is a spike whose results can change IQ1/IQ2
answers, so it goes first. After I1 the script steps (I3, I4) and the jarvisd steps (I2, I5, I6) run in
parallel worktrees. I7 is independent and large.

| Step | Scope | Done when |
|---|---|---|
| **I0** | Spike, no product code. On the MBP: jarvisd from a LaunchDaemon with `UserName` serves a Metal chat and advertises mDNS that the Android app sees (no Local Network prompt). On Windows (runner or a real box): jarvisd as a service under `NT SERVICE\jarvisd` runs llama-server on CUDA and Vulkan in session 0; engines bind loopback only; a long HF path works; mDNS seen from the app next to the DNS Client service. Record results in STATUS. | Each row has a yes/no with evidence; IQ1/IQ2 confirmed or reopened. |
| **I1** (done, §8.1) | Bootstrap: `--home` flag; per-OS service-default home; `<home>/jarvisd.env` (and `/etc/jarvisd/jarvisd.env`) loaded for unset variables; code default `~/.jarvisd`; Unix `umask 077`, home 0700, DB files 0600 at start (the §2.0 permissions gap); `sd_notify` READY; Windows `svc.IsWindowsService` run path with stop → context cancel and file logging; `jarvisd service install|uninstall|start|stop|restart|status` for systemd (system + `--user`), launchd (daemon), SCM (`mgr`, virtual account, recovery actions, ACLs). | Unit tests for env precedence and unit/plist rendering; CI installs and starts the service on ubuntu (system unit), macos-14 (LaunchDaemon) and windows-latest (SCM), then `GET :7700/health`, then uninstalls. |
| **I2** (done, §8.3) | Doctor for installs: `fix_cmds` in `Check`, `--fix` (privilege check, LAN-only, tagged rules), new checks (port held by another program, Windows Public profile, home/DB permissions, macOS sleep, legacy running, unsupervised). Move `doctorPorts` out of `cmd/` (shared with admin A3). | Table tests per firewall backend for generated commands; `--fix` applied on this box's ufw and removed by `service uninstall`. |
| **I3** (done, §8.3) | `install.sh` (Linux + macOS, POSIX sh): flags `--version`, `--channel`, `--user`, `--yes`, `--stop-legacy`, `--uninstall [--purge]`, `--rollback`; arch map; download to disk + size + `SHA256SUMS` (+ minisign if present); `jarvisd service install`; doctor + offer `--fix`; print `http://<lan-ip>:7710/setup#token=…`, the detected GPU line, the doctor result, and how to see logs. jarvisd side: first-start setup token file (with admin AQ2) and jarvisd's own app client (§3.1). | CI job runs the script against the workflow's own built archive (a `--from-dir` test hook) on ubuntu and macos-14; idempotent re-run is a no-op; `--uninstall` leaves no unit/binary. |
| **I4** (done, §8.3) | `install.ps1` (Windows PowerShell 5.1): self-elevation, TLS 1.2, `Get-FileHash`, `Unblock-File`, `%ProgramFiles%` placement + machine PATH, `jarvisd service install`, `jarvisd doctor --fix`, same printout, `-Uninstall`, `-Purge`. | CI on windows-latest under `powershell.exe` (5.1), not `pwsh`; re-run idempotent. |
| **I5** (jarvisd side done, §8.2; script re-run path left for I3/I4) | Upgrades: `jarvisd backup` (`VACUUM INTO`, keep 3), downgrade guard at start, upgrade sequence in both scripts (stop, swap, start, health-gate, rollback incl. snapshot restore when migrations ran), `jarvisd upgrade` with in-Go minisign verify (key embedded); release workflow signs `SHA256SUMS` with the existing key (secret in repo settings). | CI: install vN-1 archive, upgrade to the built one, check data survives; a deliberately broken "release" rolls back; an old binary refuses a newer DB. |
| **I6** | Legacy detection and coexistence in both scripts and doctor; `--stop-legacy` (compose stop + restart policy off). | Tested on this box with the legacy containers stopped/started; never runs `down`. |
| **I7** | `jarvisd import-legacy` per `docs/schema/*.md` (pgx, dry-run, refuses a non-empty DB, blobs via S3 GET, legacy JWT keys into `auth_signing_keys` as verify-only incl. HS256 so the env var goes away). Likely split per module (auth+config, cc, notifications, blobs). | Dry run and real run against a prod snapshot restored on this box; a node and a phone from the snapshot work against the imported jarvisd without re-provisioning. |
| **I8** | Release and docs: publish `install.sh`/`install.ps1` as release assets and at a stable URL; landing page in jarvis-installer switches to them (EXTERNAL-CHANGES row); release notes template with the one-liners; minimal container image (PLAN §3.4) for NAS users; `./jarvis init` prints a jarvisd pointer. | A fresh VM per OS installs from the published one-liner. |
| **I9** | Fresh-install rehearsal with admin A10 (LD5): this box (CUDA), the MBP (Metal), a Windows box; then the prod cutover runbook (import, stop legacy, install, wizard Models step, re-check nodes). Log friction in STATUS. | Friction list recorded; blockers fixed or filed. **This box done 2026-10-07** ([A10-rehearsal.md](A10-rehearsal.md)). **Prod cutover runbook drafted 2026-10-07** ([cutover-runbook.md](cutover-runbook.md)): clean start (ID6), not yet run; 11 open questions for the user. |

### 8.1 I1 as built (2026-10-07)

Code: `cmd/jarvisd/{bootstrap,service}.go`, `internal/platform/service` (managers, templates,
detection, sd_notify, Windows run path), `internal/platform/config/home.go`,
`internal/platform/engines/job_windows.go`, `internal/modules/llm/models/layout.go`.

**Home and env file.** `--home DIR` (or `--home=DIR`) is accepted anywhere on the command line.
Order: `--home`, `JARVIS_HOME`, the `--home` of an installed service (read back from the unit's
`ExecStart`, the plist's `ProgramArguments`, or the SCM `BinaryPathName`), then `~/.jarvisd`. The
result is exported as `JARVIS_HOME`. Then `<home>/jarvisd.env` and, on Linux/macOS,
`/etc/jarvisd/jarvisd.env` fill in variables **not already in the environment** (environment >
home file > `/etc` file; `KEY=VALUE`, `#` comments, optional `export ` and matching quotes, a
BOM is tolerated; `JARVIS_HOME` inside a file is ignored). `serve` refuses to start on an
unreadable or malformed env file; `doctor`/`migrate`/`service status` only warn, since a user
running them can't read the system service's 0640 file (they then use the default ports).
`serve` and `migrate` set `umask 077` (unix) and chmod the home 0700; `db.Open` keeps the DB
files 0600.

**Readiness.** `module.Runner.OnReady` fires once every listener is bound and every module's
`Start` returned; `serve` sends `READY=1` (datagram to `$NOTIFY_SOCKET`, abstract `@` sockets
supported, no cgo) and, under the SCM, reports Running. `STOPPING=1` when shutdown begins.

**Windows run path.** `main` checks `svc.IsWindowsService()`; stderr (Go's `os.Stderr` and the
process's standard error handle, so crash traces land too) goes to `<home>\logs\jarvisd.log`,
rotated at start when over 10 MB (3 kept). Stop/Shutdown → StopPending and the same context cancel
as SIGTERM. A non-clean exit is reported as a service-specific exit code, so the recovery actions
(with "non-crash failures" on) restart it. Every engine child goes into one job object per jarvisd
with `KILL_ON_JOB_CLOSE`, so a crashed or killed jarvisd can't orphan a llama-server.

**Long model paths.** I0 run 2 never proved `\\?\` paths work with the upstream engines, so
model paths are kept short instead: repo dir ≤ 48 characters, subdirectories collapsed into one
≤ 24, base name ≤ 88 (extension and `-NNNNN-of-NNNNN.gguf` shard suffix kept, same stem for every
shard), over-long parts become prefix + 8 hex of SHA-256. A model path stays under 200 characters
below `C:\ProgramData\jarvisd` (test with I0's 314-character case). Short names are unchanged and
models placed before the caps keep resolving (legacy-layout check on the stored path).

**`jarvisd service install|uninstall|start|stop|restart|status`.**

| | Linux system | Linux `--user` | macOS | Windows |
|---|---|---|---|---|
| Needs | root | not root | root (`sudo`) | elevated prompt |
| Account | `jarvisd` (`useradd --system --user-group`, `nologin`), added to `video`/`render` when they exist | invoking user | `UserName` = `--run-as`, else `$SUDO_USER` (root refused) | `NT SERVICE\jarvisd` |
| Home default | `/var/lib/jarvisd` (chowned recursively to `jarvisd`) | `~/.jarvisd` | `~<user>/.jarvisd` (+ `logs/`), chowned | `%ProgramData%\jarvisd` (+ `logs\`) |
| Env file written if missing (comments only) | `/etc/jarvisd/jarvisd.env` 0640 root:jarvisd, dir 0750 | `<home>/jarvisd.env` 0600 | `<home>/jarvisd.env` 0600 | `<home>\jarvisd.env` (home ACL) |
| Definition | `/etc/systemd/system/jarvisd.service` | `$XDG_CONFIG_HOME/systemd/user/jarvisd.service` | `/Library/LaunchDaemons/net.jarvisautomation.jarvisd.plist` | SCM service `jarvisd` |
| Start | `daemon-reload`, `enable`, `restart` | same with `--user`, then `loginctl enable-linger` (failure explained, not fatal) | `bootout` if loaded, `bootstrap system`, `enable`, `kickstart` (I0 finding 4) | create or `UpdateConfig`, recovery 5 s/30 s/60 s (reset 1 day, non-crash failures on), `icacls /inheritance:r` SYSTEM+Administrators F, `NT SERVICE\jarvisd` M, start and wait for Running |

The binary is this executable (or `--bin`), symlinks resolved. Guards: a Linux system unit refuses a
binary under `/home`, `/root`, `/run/user`, `/tmp`, `/var/tmp` (hidden by `ProtectHome`/`PrivateTmp`);
Windows refuses one inside `%USERPROFILE%` (the virtual account can't read it). `--no-start`
registers only. Reinstall is idempotent: same account, env file kept, unit/plist/config rewritten,
service restarted. `install` then waits up to 2 minutes for the service to be running **and** `GET
/health` on the config listener (host/port from the service's env file) to answer, and fails
otherwise. `status [--json] [--wait D]` prints the supervisor's view (state, PID, restarts or last
exit, home) plus that health check and exits non-zero unless both are good. `stop` on macOS is
`bootout` (with `KeepAlive` a kill would just restart it). `uninstall` stops and removes the
definition only: data, env file and the `jarvisd` account are kept and their paths printed
(`--purge`, binary removal and firewall-rule removal are I2/I3). Lingering (`--user`): install reads
`loginctl show-user -p Linger` first and, when it was off and `enable-linger` worked, writes
`$XDG_CONFIG_HOME/jarvisd/linger-enabled` (the config dir, not the data home); uninstall runs
`disable-linger` only when that mark exists, then removes it. Lingering that was already on is left on
(A10c U8).

Unit/plist differences from §2.1/§2.2: the unit adds `TimeoutStartSec=300`, `ProtectKernelTunables`,
`ProtectKernelModules`, `ProtectControlGroups`, `RestrictSUIDSGID`, `LockPersonality` (no
`MemoryDenyWriteExecute`: the onnxruntime/llama.cpp backends JIT) and paths are quoted when needed;
the plist adds `HOME` (the engines' Metal shader cache), `WorkingDirectory`, `ExitTimeOut` 30 and
sends stdout to the same log. Golden files: `internal/platform/service/testdata/`.

**Supervisor detection and restart (for AD5/AD8).** `service.Detect()` → `systemd` (INVOCATION_ID
**and** this process in a `jarvisd.service` cgroup, so a shell inheriting INVOCATION_ID from a
terminal unit doesn't count), `launchd` (parent pid 1 and a real `XPC_SERVICE_NAME`),
`windows-service`, or `none`. `serve` builds a `service.Restarter` with its own cancel;
`Request()` returns `ErrUnsupervised` when nothing would restart jarvisd (the route answers 409),
else cancels serve, which returns `ErrRestart`, and `main` exits 75 (`RestartExitCode`): systemd
`Restart=always`, launchd `KeepAlive` and the SCM recovery actions each start it again. Not wired to
a route yet: `POST /api/system/restart` gets the restarter handed to the admin module.

**Verified.** CI job `service` (ci.yml) installs from `/usr/local/bin` / `%ProgramFiles%\jarvisd`
on ubuntu-latest (system unit: runs as `jarvisd`, `Type=notify`, home 0700, DB 0600, env file
root:jarvisd 0640), macos-14 (LaunchDaemon as the runner user, log written) and windows-latest
(`NT SERVICE\jarvisd`, delayed auto start, `--home` in the command line, DB under ProgramData, log
written), then checks `/health`, kills the process and sees the supervisor bring it back, restarts,
stops (Windows: the process exits), uninstalls and checks the data is kept. `--user` mode was run
on the dev box with a throwaway home and alternate ports from its env file (install → healthy,
kill → restarted, stop → graceful MQTT/HTTP shutdown, status exit codes, `migrate status` finding
the installed home), then uninstalled and linger turned back off.

### 8.2 Self-update as built (AD5, ID10; 2026-10-07)

Code: `internal/update` (minisign, release lookup, stage, swap, rollback, marker state machine),
`cmd/jarvisd/upgrade.go` (CLI, serve's start step and health gate), `internal/modules/admin/apply.go`
(admin routes, inventory §6.2), `internal/platform/db/versions.go` (downgrade guard), the systemd unit's
pre-start helper (`internal/platform/service/render.go`), `scripts/upgrade-e2e.sh`.

**Trust root.** The release workflow signs `SHA256SUMS` with the project minisign key (the node/admin key,
id `C9A24FB502A25B72`; secrets `MINISIGN_SECRET_KEY` + `MINISIGN_PASSWORD`) with the trusted comment
`jarvisd <tag> SHA256SUMS`, producing `SHA256SUMS.minisig`. jarvisd verifies it in Go with the public key
compiled into the **running** binary (legacy `Ed` and prehashed `ED` signatures, plus the global signature
over the trusted comment), requires the comment to name the release being installed, then checks the
archive's SHA-256 from that list. Unsigned releases are refused; there is no override. A publishing
release run fails without the secret; a dry run warns and ships unsigned. Test builds may trust one extra
key via `-ldflags -X …/internal/update.extraTrustedKey=` (never an env var). `JARVIS_UPDATE_API` replaces the
GitHub API base (the e2e uses it); it can't weaken anything since the signature is still checked.

**One flow, two front ends** (`jarvisd upgrade [--version vX [--allow-older]] [--check] [--rollback]
[--bin PATH] [--user]` and `POST /api/update/apply`):

1. Resolve: newest release this build may move to (prereleases only from a prerelease), or `--version`
   (older/same needs `--allow-older`; the admin only goes forward). Refuse if an upgrade marker exists.
2. Stage under `<home>/updates/staged`: fetch `SHA256SUMS` + `.minisig` first (nothing big is downloaded
   for an unsigned release), verify, download the archive (size-checked, progress reported), checksum,
   extract `jarvisd(.exe)`, run `<new> version` and require the tag.
3. Snapshot every `<home>/*.db` with `VACUUM INTO` → `<home>/backups/<db>-<fromver>-<UTC>.db` (0600, keep 3
   per DB) and record every `goose_*` table's applied versions. Write the marker
   `<home>/updates/upgrade.json` (`staged`).
4. Swap, when this process can write the executable's directory: re-verify the staged files, extract the
   binary next to the executable, copy the executable to `jarvisd.prev` (`jarvisd.prev.exe`), rename the
   new one over it (Windows: the running exe is renamed to `jarvisd.exe.old` first, deleted on the next
   start). Marker → `swapped`. The executable is `os.Executable()` with symlinks resolved (what the unit /
   plist / SCM entry runs); the CLI takes `--bin`.
5. Restart: the admin uses serve's `Restarter` (exit 75); the CLI restarts the installed service through
   the service manager and waits for the outcome.

**Who can write the binary.** The systemd system unit runs as `jarvisd` with `ProtectSystem=strict`, so it
can't replace `/usr/local/bin/jarvisd`. The unit now carries `Environment=JARVIS_UPGRADE_HELPER=1` and
`ExecStartPre=-+<bin> upgrade --prestart --home <home>`: on every start, as root and outside the sandbox
(`+`), never blocking the start (`-`), it performs a pending swap (re-verifying the staged archive with the
keys of the binary it runs, and refusing anything not newer than itself, so an unprivileged writer of the
data dir can't install or downgrade anything) or a requested rollback, chowning what it writes to the
home's owner. So the admin button stages as `jarvisd` and the restart does the swap. Existing system units
get the helper on the next `jarvisd service install`. `--user` units and unsupervised runs own their binary
and swap in process. launchd and SCM installs in root-owned dirs have no helper yet: the admin answers 409
with `sudo jarvisd upgrade` (Windows: an elevated PowerShell). Unsupervised: the admin answers 409
(`jarvisd upgrade`); the CLI refuses while an unsupervised jarvisd answers `/health` ("stop it first"),
otherwise swaps and tells you to start it.

**Start step and health gate** (serve, before the DB opens). `PreStart` does any pending swap/rollback (a
no-op when the helper already did); a process that just swapped itself exits 75 to run the new binary.
`BeginStart`: when the marker is `swapped` and this binary is the target version, count the attempt; with
`MaxFailedStarts` (2) failed starts already counted, request a rollback instead. Then the gate: once every
listener is bound (`OnReady`) jarvisd polls its own config listener's `/health` (admin's if config is
disabled); success within `JARVIS_UPGRADE_GATE_TIMEOUT` (default 2 m) records `succeeded` in
`<home>/updates/last-upgrade.json` and clears the marker. Timeout → serve stops, the DB is closed, and
rollback runs (in process if writable, else exit 75 for the helper). Rollback copies `jarvisd.prev` back
and restores a DB snapshot **only if** that DB's goose versions differ from the recorded ones, then records
`rolled_back` with the reason. A swapped marker seen by another version is closed as `failed`. If the new
binary never reports anything (can't start far enough to count), the waiting CLI rolls back itself after
`2 × (gate + 15 s) + 1 m`. `jarvisd upgrade --rollback` rolls back a pending upgrade, or (no marker) just
restores `jarvisd.prev` and leaves the DB (the guard then tells you about snapshots if needed), recording
`rolled_back` in `last-upgrade.json`; under a service manager it then restarts it and waits for `/health`
(the gate timeout) before saying "jarvisd vX is up and healthy" (A10c U1, U2). Every rollback first
copies the binary it replaces to `jarvisd.rolledback` (`.rolledback.exe`; best effort, one copy, the next
swap deletes it, `install.sh --uninstall` too), so `jarvisd.prev` stays the older version and the newer
one isn't lost (A10c U4). It is for inspection or copying back by hand: a re-upgrade still downloads,
since a swap only installs what it re-verifies against the signed `SHA256SUMS` and the root helper must
not trust a bare binary. A successful upgrade or rollback of the installed service ends with
`setup-link`'s line (the admin URL, or the setup link before setup): install.sh `exec`s `jarvisd
upgrade`, so that is the script's last word too (A10c U6).

**What the root helper trusts.** Only paths derived from the executable and `--home`: the marker is in the
data dir, which the service account writes. A rollback restores `<exe>.prev` (never the marker's `prev`),
restores only snapshot entries naming a regular `*.db` directly in the home from a regular file directly
in `backups/` (neither a symlink), and creates its temporaries in the data dir (`*.tmp`, `*.restore`)
fresh with `O_EXCL` instead of writing through whatever is at that name (fixed 2026-10-08 with A10c U4;
before, the marker's `prev` was copied over the root-run binary).

**Downgrade guard.** Before migrating, every module's (and the queue's/scheduler's) applied goose versions
must all be migrations this binary has: "unknown" rather than "higher", since out-of-order migrations are
allowed. Otherwise serve refuses with the module, the unknown versions, and how to recover (reinstall the
newer version, or restore a snapshot from `<home>/backups`); `serve --allow-downgrade` runs anyway.
Settings tables (`goose_*_settings`) are not checked.

**Verified.** Unit tests: minisign against a real project-key signature (node-setup v0.3.1 checksums) and
an independent implementation's legacy + prehashed vectors (aead.dev/minisign, throwaway key); fake GitHub
over httptest for stage and refusals (unsigned, wrong key, other tag in the comment, default comment, bad
checksum, wrong version); swap, re-verification of tampered staged files, the pre-start downgrade refusal,
crash-loop and gate rollbacks with and without DB restore, the Windows rename-aside path, snapshot pruning,
`VerifyDir`; runner and db downgrade guard; admin routes with a fake restarter. `scripts/upgrade-e2e.sh
user` passed twice on the dev box (systemd --user, throwaway home and ports): CLI upgrade, a crashing release
rolled back after 2 failed starts, the admin button, the restart button, manual rollback. CI job `upgrade`
runs the same script in `system` mode (the root `ExecStartPre` helper path). The release `verify` job
checks the signature with each OS's own binary (`jarvisd upgrade --verify-dir`).

**Integration with the install scripts (2026-10-07, §8.3).**
- *Flat release directory.* `JARVISD_RELEASE_BASE=<URL|file URL|path>` points `jarvisd upgrade` (and
  `--check`) at a directory holding `SHA256SUMS`, `SHA256SUMS.minisig` and the archives, with no API
  (`update.Source.Base`, `internal/update/flat.go`): the releases are the tags in the archive names
  `SHA256SUMS` lists (`jarvisd-<tag>-<os>-<arch>.tar.gz|.zip`), newest or `--version`; archive sizes
  from a HEAD / `stat` when cheap (else only checksummed). The signature check is unchanged and
  mandatory: a directory without `.minisig` is "no SHA256SUMS.minisig …; jarvisd only installs signed
  releases". Local paths are read only for a `Base` source, never from GitHub asset URLs. The admin
  button keeps using the API. This is what a script re-run hands over to.
- *Which service.* No `--user` needed: `service.Open` on Linux falls back to the `--user` unit when no
  system unit exists, and the home comes from the installed unit's `--home`. `--user` remains for the
  rare box with both.
- *Free-disk preflight* (`update.CheckDisk`, in `Stage` before anything is downloaded, so the admin
  button gets it too): the home needs max(3 × archive, archive + current binary) + every `*.db` and
  its `-wal`; the executable's directory needs 2 × the current binary (new + `jarvisd.prev`). An
  unknown archive size counts as the current binary; a filesystem whose free space can't be read is
  not checked. Error: "not enough free disk space in <dir>: the upgrade needs about N MB there (…),
  M MB is free; free some space and try again".
- *macOS firewall after a swap.* socketfilterfw keys its allow entry on the code signature. Release
  builds are Developer ID-signed (§8.4), whose designated requirement (identifier
  `net.jarvisautomation.jarvisd` + team) is the same in every version, so the entry carries over and
  this guard finds nothing to do; an ad-hoc build (dry run, source build) gets a new signature every
  build. `jarvisd upgrade` (and `--rollback`) checks the doctor's firewall checks before
  the swap; if the old binary was admitted and the new one isn't, it runs their `fix_cmds` as root
  (`sudo jarvisd upgrade`, which launchd installs need anyway), else prints the command. A fix the
  operator declined at install stays declined.

**Not done / follow-ups.** launchd/SCM privileged helpers (today: `sudo jarvisd upgrade`, an elevated
PowerShell); the admin button on macOS therefore never needs the firewall re-add, but would once a
helper exists; `jarvisd backup` as its own command; a binary that passes `version` but dies before
serve counts its start relies on the waiting CLI to roll back.

### 8.3 I2, I3, I4 as built (2026-10-07)

Code: `internal/doctor/{doctor,checks,backends,firewall,command_*,owner_*}.go`,
`cmd/jarvisd/{doctor,uninstall,setup}.go`, `internal/platform/service/purge.go`,
`scripts/install.sh`, `scripts/install.ps1`; CI job `install` (ci.yml); release step "Add the
install scripts" (release.yml).

**Doctor (I2).** A `Check` now carries `fix_cmds` (argv lists, no `sudo`); `fix` is rendered
from them (`sudo` + shell quoting on unix, verbatim + "(from an Administrator prompt)" on
Windows). Only the firewall checks set them, and only for a `PrivateLAN` (private IPv4, no
wider than /8). `jarvisd doctor --fix` refuses without root / an elevated token, runs each
distinct command once (stops at the first failure), then checks again; with `--json` the
progress goes to stderr. Per backend, everything is tagged so uninstall finds it:

| Firewall | Fix | Removed by `service uninstall` |
|---|---|---|
| ufw | `ufw allow from <lan> to any port <list> proto tcp\|udp comment jarvisd` (idempotent) | each `user.rules` tuple with the hex `jarvisd` comment → `ufw delete allow from … port … proto …` |
| firewalld | `--new-service=jarvisd` (if missing), `--service=jarvisd --add-port=…` per port, one rich rule `source address=<lan> service name="jarvisd" accept`, `--reload`; the check now also reads rich rules and the service's ports | rich rules naming the service, `--delete-service=jarvisd`, `--reload` |
| macOS | `socketfilterfw --add` + `--unblockapp` on the binary (block-all is left to the user, noted) | `socketfilterfw --remove` if listed |
| Windows | `delete rule name=all dir=in program="<exe>"` when any rule names the exe (a "Cancel" block rule beats any allow), then `add rule name=jarvisd … profile=private`; netsh gets its command line verbatim (`SysProcAttr.CmdLine`) | `delete rule name=jarvisd` if it exists |

New checks: **ports** (fail): a TCP port answers but its HTTP `/health` lacks `Server: jarvisd`
(every jarvisd listener now sends it, `httpx.ServerName`); the broker's ports count as
foreign only when no HTTP listener is jarvisd's; fix names `ss`/`lsof`/`Get-NetTCPConnection`.
**data directory** (warn): home not 0700, `jarvis.db*`/`setup-token` not 0600, or files owned
by another account than the home (a `sudo jarvisd serve`); unreadable as a plain user is
"OK, run sudo to check" (`Options.Home`; the admin's in-process doctor passes none).
**legacy stack** (warn): running legacy containers that publish a jarvisd port, or any
while the ports check failed (host-network stacks publish nothing), or that hold GPU memory;
infra-only containers and a leftover `~/.jarvis/compose` are OK. Fix: `docker update
--restart=no` + `docker stop`, and `systemctl --user disable --now jarvis-admin.service`.
*Legacy* (`doctor.LegacyContainer`, revised 2026-10-07 after the prod survey, cutover-runbook
§1): named `jarvis-*`, or by Compose labels in project `jarvis` (the admin installer pins
`name: jarvis` in `~/.jarvis/compose`; on prod that project also runs `llama-server`,
`llama-server-bg`, `llm-proxy-worker` — ~40 GB of VRAM — and `go2rtc`), in a `jarvis-*`
project (a source checkout's per-service projects such as `jarvis-llm-proxy-api`'s
`llm-proxy-*`; the dockerized node's `jarvis-node`), or in any project whose
`working_dir` is `…/.jarvis/compose` (installs from before the pin, project `compose`).
Anything else (plex, a user's own projects) is never touched; the scripts apply the same rule.
**gpu memory** (Linux + nvidia-smi, info): `--query-compute-apps` per process, sorted by
`/proc/<pid>/cgroup` (which names the container's ID) into the legacy containers' share
(reported, and warned about, under "legacy stack"), jarvisd's own engines (a `jarvisd`
ancestor in `/proc/<pid>/stat`; not reported) and other programs (named, "in a Docker
container" when the cgroup says so — the admin's in-process doctor has no docker access).
The llm module's fit verdicts already count the remainder as "other programs".
**network profile** (Windows, warn on Public) and **sleep** (macOS, `pmset -g` sleep > 0).
**env file** (warn): the env file a plain user can't read (the system unit's 0640 file) is
now a check pointing at `sudo jarvisd doctor` instead of a stderr warning. Not done:
"unsupervised" (info), and `doctorPorts` stays in `cmd/` (the admin uses `doctor.Exposure`).

**Uninstall / purge.** `jarvisd service uninstall [--purge [--yes]] [--keep-firewall]`:
removes the definition, then the tagged firewall rules (as root/Administrator; otherwise it
prints the removal commands), also when the service was already gone. `--purge` first shows
what goes (home with its size, `/etc/jarvisd`, the `jarvisd` account on Linux system mode)
and needs the home path typed back (or `--yes`; no terminal and no `--yes` refuses before
anything changes). `CheckPurgeHome` refuses a relative path, a root, a user's home, any
`.jarvis`, and a directory neither named for jarvisd nor holding `jarvis.db`. Binaries are
the scripts' to remove (a running `jarvisd.exe` can't delete itself).

**`jarvisd setup-link`** prints the link + token from `<home>/setup-token` (the scripts run
it with sudo for a system home), else the admin URL. **`jarvisd help`** prints the usage and
exits 0; the scripts grep it for an `upgrade` command.

**`install.sh` (I3)** — POSIX sh, ~200 lines, shellcheck clean. Flags: `--version`, `--user`,
`--yes`, `--stop-legacy`, `--force`, `--uninstall [--purge]`, `--base-url` (or
`JARVISD_RELEASE_BASE`: a flat directory holding `SHA256SUMS` and the archives). Flow:
OS/arch map (Intel Mac and non-systemd Linux refused) → `SHA256SUMS` from
`releases/latest/download/` (no API) or the tag; the archive name in it gives the version →
if installed: same version + healthy = no-op (prints the setup link); different version and
the installed binary lists `upgrade` = `exec [sudo] env JARVISD_RELEASE_BASE=<base or empty>
jarvisd upgrade --version vX` **before anything else is downloaded** (the installed binary checks
the signature with its own key, free disk, snapshot, health gate, rollback; §8.2) → signature
(fresh install, or a jarvisd too old to upgrade itself): the key is exactly
`internal/update/key.go` `ProjectPublicKey` (no override; `TestScriptsTrustTheProjectKey` keeps
both scripts equal to it). **With `minisign` installed the signature is required**: a missing
`SHA256SUMS.minisig` or an invalid one is fatal. Without minisign: checksum only, with a warning
(`JARVISD_REQUIRE_SIGNATURE=1` refuses that). Decision: a downloaded binary can't vouch for
itself, so the first install is TLS-anchored unless the operator has minisign; every later
upgrade is verified by the running jarvisd → archive download to `$TMPDIR` or `/var/tmp`,
SHA-256 must match → the extracted binary must print the version → an installed jarvisd without
`upgrade` (pre-§8.2): the binary is renamed over the running one (`jarvisd.prev` kept in
`/usr/local/lib/jarvisd`) and the service reinstall below restarts it → fresh install: the new binary's `doctor --json` names a `ports`
check → legacy containers (the doctor's rule above)? refuse, or with `--stop-legacy` `docker
update --restart=no` + `docker stop` (never `down`/`rm`); no containers = "another program"
refusal. `--stop-legacy` also stops the legacy stack when its ports are free (its
llama-servers still hold the GPUs) and on a re-run over an installed jarvisd, and turns off the
legacy admin's **systemd user unit** `jarvis-admin.service` (7711; its reconcile runs `docker
compose up -d`): `systemctl --user disable --now` for the invoking user, under sudo for
`$SUDO_USER` via `systemctl --user -M $SUDO_USER@` (systemd ≥ 248). Limits: needs that user's
manager running (the admin's installer enables linger, so it is); when it isn't reachable but
`~/.config/systemd/user/jarvis-admin.service` exists, a warning prints the command to run as
that user; run as root without sudo, the unit is not looked for; macOS's
`com.jarvis.admin` LaunchAgent and Windows are not handled → atomic rename
into `/usr/local/bin` (`~/.local/bin` with `--user`) → `jarvisd service install [--user]`
(waits for /health) + `service status --wait 90s`; an upgrade that fails goes back to
`jarvisd.prev` → `sudo jarvisd doctor --json`: any `fix_cmds` → "[Y/n]" on `/dev/tty`
(default yes), `--yes` without a terminal, else the command is printed → `doctor`,
`setup-link`, log and manage hints. Deviations from the row: no `--channel`/`--rollback`
(ID10's rollback belongs to `jarvisd upgrade`), no GPU line, no separate HEAD size check (the
checksum covers truncation), downloads go to the temp dir rather than beside the binary.

**`install.ps1` (I4)** — Windows PowerShell 5.1. Same flow with `-Version`, `-BaseUrl`,
`-Yes`, `-StopLegacy`, `-Force`, `-Uninstall`, `-Purge` (under `irm | iex`:
`JARVISD_VERSION`, `JARVISD_RELEASE_BASE`, `JARVISD_YES=1`). Self-elevates with
`Start-Process -Verb RunAs` (re-downloading itself when run from `iex`), TLS 1.2, progress bar
off, `Get-FileHash`, `Unblock-File` on the zip and exe, `%ProgramFiles%\jarvisd\jarvisd.exe` +
machine PATH entry, `jarvisd.prev.exe` for the rollback, `throw` never `exit`. Native stderr
is read through a helper with `ErrorActionPreference=Continue` (5.1 turns redirected stderr
into error records). Uninstall retries removing the directory while the exe lock clears and
drops the PATH entry.

**Release.** A separate step after "Build and package" copies both scripts into `dist/`;
they are published as assets but kept out of `SHA256SUMS` (fetched on their own over TLS),
so a signing step over `SHA256SUMS` is unaffected.

**Verified.** CI job `install` (ci.yml) builds a release-shaped archive (`v0.0.0-ci`) plus
`SHA256SUMS`, serves it with `python3 -m http.server` and runs the scripts with `--base-url`:
ubuntu-latest (ufw enabled; a fake legacy stack — Compose project `jarvis` in
`~/.jarvis/compose` with `jarvis-config-service` on 7700 and a `llama-server`, plus a
`jarvis-admin.service` user unit (linger on) — makes the script refuse; `--stop-legacy`
stops both containers with restart policy `no` and disables + stops the unit, while `plex`
(no project) and `go2rtc` in project `cameras` keep running `unless-stopped`; a later
reinstall as root through sudo stops the re-enabled unit via `-M $SUDO_USER@`; ufw gains the `# jarvisd`
rules for the runner's 10.1.0.0/20; `Server: jarvisd` on /health; `sudo jarvisd doctor`
clean; re-run is a no-op; `--uninstall` removes unit, binary and both ufw rules and keeps
`/var/lib/jarvisd` and the account; reinstall; `--uninstall --purge --yes` removes home,
`/etc/jarvisd` and the account), macos-14 (application firewall on; after taking the app
out, doctor fails and `--fix` re-admits it; uninstall removes it from the app list; purge),
windows-latest under `powershell.exe` 5.1 (rule `jarvisd` on the exe, machine PATH, setup
link, no-op re-run, `-Uninstall` removes service, exe, rule and keeps ProgramData;
`-Uninstall -Purge -Yes`). shellcheck runs on install.sh. Not run on this dev box (no
passwordless sudo, and no services are installed here).

**Upgrade glue (2026-10-07).** Both scripts now hand a re-run with another version to `jarvisd
upgrade` before downloading the archive (§8.2 "Integration"), trust exactly `ProjectPublicKey`,
require the signature when `minisign` is installed, and `--uninstall` also removes `jarvisd.prev`.
The CI `install` job builds two releases (`v0.0.0-ci`, `v0.0.1-ci`) whose binaries trust a
throwaway key (`-X …/internal/update.extraTrustedKey=`), signed by `scripts/testsign` (a ~60-line
Go minisign-compatible signer, checked against minisign 0.12; no minisign on PATH, which would make
the scripts demand the project signature), plus an unsigned `v0.0.2-ci`. On all three OSes: install
`v0.0.0-ci`, re-run with `--base-url …/next` → "Upgrading … with `jarvisd upgrade`" → "v0.0.1-ci is up
and healthy", `/health` and `jarvisd.prev` versions, `last-upgrade.json` `succeeded`; re-run of the
unsigned one → "only installs signed releases", binary unchanged. Ubuntu (system unit; `sudo jarvisd
upgrade` swaps in process as root, the unit's `ExecStartPre` helper finds nothing pending — the helper
path itself is covered by the `upgrade` job's admin button) also checks the DB snapshot, the no-op
re-run, and finally, with `apt install minisign`, that a fresh install of the throwaway-signed release
is refused ("signature is INVALID"). macOS: the app stays `permitted` after the swap and `sudo jarvisd
doctor` is clean; on the macos-14 runner socketfilterfw kept admitting the new build, so the re-admit
step had nothing to do there (it is the guard for when it doesn't). Windows: SCM restart, rename-aside
swap, `jarvisd.prev.exe`.

### 8.4 macOS signing and notarization as built (ID9; 2026-10-08)

Code: `.github/workflows/release.yml` (jobs `build` → `sign-macos` → `package` → `verify` →
`publish`), `scripts/macos/jarvisd.entitlements`.

**Order.** `build` compiles all four targets and hands them on as a tar (artifacts drop the
executable bit). `sign-macos` (macos-14) signs and notarizes the darwin binary and hands it back;
`package` swaps it in, then archives, computes `SHA256SUMS` and minisign-signs it. So the checksums
and `SHA256SUMS.minisig` cover the notarized binary, and the self-update chain (§8.2) is unchanged.

**Signing.** A throwaway keychain (random password, masked) gets the `.p12`
(`APPLE_DEVELOPER_ID_P12`, base64, + `APPLE_DEVELOPER_ID_P12_PASSWORD`); `codesign --force
--timestamp --options runtime --identifier net.jarvisautomation.jarvisd --entitlements …` with the
"Developer ID Application" identity found in it. Checked right after: `codesign --verify --strict`,
identifier, `TeamIdentifier` = `APPLE_TEAM_ID`, the `runtime` flag, a secure timestamp. An
`always()` step deletes the keychain, the `.p12`, the `.p8` and the zip.

**Entitlements: `com.apple.security.cs.disable-library-validation` only.** The hardened runtime
turns on library validation (only Apple- or same-team-signed libraries may load); the sherpa-onnx
and onnxruntime dylibs jarvisd extracts to `<home>/lib/sherpa-<hash>/` and dlopens (purego) carry
upstream **ad-hoc** signatures, so without it Kokoro and speaker ID would fail to load. Not needed
and not granted: `allow-jit` / `allow-unsigned-executable-memory` (onnxruntime's CPU kernels are
precompiled; purego's callback trampolines are in the binary's text), `allow-dyld-environment-variables`.
Verified: the notarized binary renders Kokoro TTS on the MBP (below). The alternative — signing the
two dylibs with the Developer ID before embedding, which would make the entitlement unnecessary —
needs the darwin build on a Mac; not worth it while the libraries live in the user's own data dir.

**Notarization.** `ditto -c -k --keepParent` → `xcrun notarytool submit --key/--key-id/--issuer
--wait` (App Store Connect API key: `APPLE_API_KEY_P8`, the `.p8` text or its base64,
`APPLE_API_KEY_ID`, `APPLE_API_ISSUER_ID`); the job prints `notarytool log` always and fails unless
the status is `Accepted`. **A bare Mach-O can't be stapled**: Gatekeeper fetches the ticket online
the first time it assesses a quarantined copy, so an offline Mac refuses a browser-downloaded
`jarvisd` until it has been online once. (`curl`, the install scripts and `jarvisd upgrade` don't
quarantine, so they never meet Gatekeeper.)

**Gatekeeper check.** `spctl --assess --type execute` rejects every bare binary ("the code is valid
but does not seem to be an app"; it judges app bundles), so the job uses `spctl --assess --type open
--context context:primary-signature` and requires `accepted` + `source=Notarized Developer ID`
(retried for 2 min while the ticket propagates). The macos-14 `verify` job re-checks the archived
binary (codesign strict, identifier, Developer ID authority, runtime flag, the same `spctl`), then
runs a copy carrying a Safari-style `com.apple.quarantine` and requires `version` to print within
90 s: Gatekeeper *holds* a quarantined binary it doesn't accept at exec (a dialog no one answers),
which the watchdog turns into a failure.

**Without the secrets** a dry run warns and keeps the Go linker's ad-hoc signature (`verify` warns
too); a publishing run (tag push or `publish: true`) fails, like the minisign step. Windows stays
unsigned (ID9).

**Firewall.** With a Developer ID signature the application firewall's allow entry is keyed on a
designated requirement that is the same for every release (identifier + team), so it survives
upgrades; the §8.2 re-admit guard stays for ad-hoc builds. (Not exercised on a real upgrade yet:
the MBP has no passwordless sudo.)

**Verified (2026-10-08).** Dry run `v0.0.0-notarize1` (run 37723014237): signed, notarization
`Accepted` in ~30 s with no issues in the log, then failed on `spctl -t execute` (the bare-binary
rejection above). `v0.0.0-notarize2` (run 37723548277): every job green; `spctl` "accepted,
source=Notarized Developer ID"; `SHA256SUMS.minisig` made with the shared project key and checked by
each OS's own binary; publish skipped. On the MBP (macOS 26.1, M2 Max) from that artifact: codesign
strict valid, runtime flag, only the one entitlement; `spctl -t open` accepted as Notarized Developer
ID; a quarantined copy runs `jarvisd version` at once, while a quarantined **ad-hoc** jarvisd
(control) hung at exec until killed. `jarvisd serve` from the quarantined copy (throwaway home,
ports 27xxx on loopback, mDNS off) installed Kokoro + ERes2Net through the model API and rendered
`/speak` (HTTP 200, 108 KB WAV, 24 kHz) — `tts: engine loaded` in 1.5 s, both dylibs mapped. The
extracted dylibs carry no quarantine attribute (none propagates from a quarantined parent) and keep
their ad-hoc signatures. `v0.0.0-notarize3` (run 37725013988) checked the exec watchdog.
