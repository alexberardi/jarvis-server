# A10c upgrade rehearsal from the published releases: `v0.1.0-rc1` → `v0.1.0-rc2`, this box, 2026-10-08

The first upgrade between two **real GitHub prereleases**: rc1 installed the documented way, upgraded by
re-running rc2's (stamped) `install.sh`, rolled back by hand, upgraded again with the admin's
**Update now**, then everything restored. Follows [A10b](A10b-rc1-rehearsal.md) (fresh install from rc1),
whose blocker R6 (no dated tool call possible) rc2 fixes; this run checks that fix live through the
upgrade.

**Box:** 10.0.0.122 (Arch/Omarchy, systemd, RTX 3080 Ti, ufw admitting 10.0.0.0/24, no passwordless
sudo), so `--user`. Prod and the MBP untouched.

**Reused, not re-downloaded:** the A10b home (`~/.jarvisd.rehearsal-rc1-2026-10-08`: rc1's database with
the rehearsal superuser and household, Qwen3.5-9B + mmproj, MiniLM, whisper small.en, Kokoro, ERes2Net,
llama-server/whisper-server CUDA engines; 9.2 GB) was renamed to `~/.jarvisd`, and rc1 installed over
it. Every label came up `ready` straight away.

**What was preserved:** the dev jarvisd (`dev-433f208`, pid 3676336, `~/.jarvisd/bin/jarvisd serve` from
`run.sh`, cwd `~/jarvis/jarvis-server`, fd0 `/dev/null`, fd1/2 `~/.jarvisd/jarvisd.log`, parent systemd
--user) was stopped with SIGTERM (it and its three engines gone in 0.34 s) and its home renamed
`~/.jarvisd.pre-upgrade`; afterwards renamed back and restarted with the same command, cwd, stdio, session
leader, parent and `JARVIS_*`/`ADMIN_API_KEY` environment (sha256 of the sorted vars equal to the
recorded `/proc/<pid>/environ`), engines healthy 5 s later, jarvis-dev back on it. Linger `no` →
`yes` (`service install --user`) → `no`. jarvis-dev's `config.json` sha256 `68e496ae…` before and
after. `~/.local/bin/jarvisd*` and `~/.local/lib/jarvisd` did not exist before and don't now (the
binaries are in the rehearsal home's `rehearsal-bin/`: `jarvisd` = rc2, `jarvisd.prev` = rc1).

## Timeline

| Step | Time | Notes |
|---|---|---|
| Stop dev jarvisd, swap homes | 0.34 s | SIGTERM via `ps` + `awk` on argv. |
| rc1: `curl -fsSLo install.sh …/v0.1.0-rc1/install.sh && sh install.sh --version v0.1.0-rc1 --user` | ~2 s | The mise minisign shim → checksum only (as in A10b). Unit written, healthy, doctor all OK. Closing line "Setup is done (or jarvisd hasn't started yet)" (U3). All six labels `ready`, prompt provider `Qwen3_5_9B_Compressed`, superuser login works. |
| rc1 chat | 1.6 s | "Paris is the capital of France." |
| jarvis-dev pointed at the rehearsal | ~70 s to tools | Only the node's `api_key` swapped to the one A10b registered for the same `node_id` (backup on the Pi and in the scratchpad); the node fetched its MQTT credentials after one broker refusal, warmup 21 client tools. |
| rc1 weather (baseline) | 13.8 s | "What's the weather like?" through the real node: five `get_weather_meteo` runs, **"Too many tool iterations"**: R6 reproduced on the published rc1. |
| **Script upgrade:** `curl -fsSLo install.sh …/v0.1.0-rc2/install.sh && sh install.sh --user` | **~10 s** | "Upgrading jarvisd v0.1.0-rc1 -> v0.1.0-rc2 with `jarvisd upgrade`" (the stamp picked rc2 with no `--version`), rc1's binary verified rc2's `SHA256SUMS.minisig` with its built-in key, 20 MB download, snapshot `backups/jarvis-v0.1.0_rc1-20261008T051736…db`, swap, restart, **health gate passed 18 ms after the new process logged its start**, "jarvisd v0.1.0-rc2 is up and healthy". |
| rc2 checks | — | `jarvisd version` v0.1.0-rc2, `jarvisd.prev` v0.1.0-rc1, `last-upgrade.json` `succeeded rc1 → rc2`, `updates/` holds nothing else, labels all `ready`, doctor all OK, `service status` restarts 0, `upgrade --check` up to date. |
| rc2 chat / weather / rain | 2.8 s / **3.1 s** / 2.8 s | "Paris…"; **"It's 52 degrees and clear in Brick, New Jersey. No rain expected for the next hour…"**; "Will it rain tomorrow?" → "Nope… overcast with a high of 68°F". The R6 fix works on the published build. |
| **Rollback:** `jarvisd upgrade --rollback` | CLI 0.1 s; healthy ≤ 4.5 s | "restored …/jarvisd.prev; the database is unchanged…", "restarting the service", exit 0, **without waiting** for the restart (U2); rc1 started 80 ms later, `/health` answering by +4.5 s; rc1 chat 2.7 s. `last-upgrade.json` still said `succeeded rc1 → rc2` (U1). DB untouched (rc2 adds no migrations; the downgrade guard passed). |
| **Admin Update now** (rc1) | **6.9 s** POST → rc2 healthy | Update checks were already on from A10b. `GET /api/update`: rc2 offered (`prerelease: true`, `can_apply: true`). `POST /api/update/apply {"version":"v0.1.0-rc2"}` → 202; polled job: checking → verifying (+0.25 s) → downloading → unpacking (+0.75 s) → installing, marker `staged` (+1.0 s) → restarting, marker `swapped` (+1.25 s) → connection refused for ~5 s → `/api/system/info` version v0.1.0-rc2 with a new `started_at`, marker gone, `last` `succeeded` (+6.9 s). Second snapshot in `backups/`, `jarvisd.prev` rc1, `service status` restarts 1 (R10). Weather turn on rc2 again 5.0 s, correct. |
| Voice turn | skipped | Optional this time; the weather turns already ran the real node's tool over MQTT. |
| Restore | ~2 min | `jarvisd service uninstall --user --keep-firewall`, binaries into `rehearsal-bin/`, empty `~/.local/lib/jarvisd` removed, homes swapped back, dev jarvisd restarted (see U7), node `config.json` copied back from its backup and the node restarted (Restart=always), linger off. jarvis-dev's warmup on the dev jarvisd 01:21:15. |

## Friction

Fixed (each with a test; branch `worktree-agent-a7e847439692f246c`):

| # | Where | Problem | Fix |
|---|---|---|---|
| U1 | `jarvisd upgrade --rollback` | After an upgrade that passed its gate (no marker), the manual rollback restored `jarvisd.prev` but left `last-upgrade.json` at `succeeded rc1 → rc2` while rc1 ran; the admin's `GET /api/update/apply` reported it as `last`. | `ad6b185` `RestorePrevious` records `rolled_back` (from = `jarvisd.prev`'s version, to = the running one, reason "rolled back by hand"). Test: `TestRestorePreviousRecordsResult`; `upgrade-e2e.sh` step 5 checks the outcome. |
| U2 | `jarvisd upgrade --rollback` | Returned 0.1 s after asking the service manager to restart, without saying whether the restored version came up (an upgrade waits for its gate). | `ad6b185` waits (gate timeout) for the service to run and answer `/health`, then "jarvisd v0.1.0-rc1 is up and healthy", or fails naming the log. Test: `TestReportHealthy`. |
| U3 | `jarvisd setup-link` (install.sh's last line) | "Setup is done (or jarvisd hasn't started yet)" on every install over a set-up home. | `e82c62e` with no token it asks `/health`: "jarvisd is set up. The admin is at …", or "isn't answering yet; once it is, `jarvisd setup-link` prints the setup link…". Test: `TestPrintSetupLink`. |

Fixed later (branch `worktree-agent-ac3f9d5ad9482bedd`):

| # | Where | Problem | Fix |
|---|---|---|---|
| U4 | `jarvisd upgrade --rollback` (and every rollback) | The rollback *copied* `jarvisd.prev` over the binary, so both were rc1 and rc2 was gone. | `44df387` the binary being replaced is first copied to `jarvisd.rolledback` (`.rolledback.exe`; best effort, one copy, the next swap deletes it, `install.sh --uninstall` removes it); `--rollback` says where it is. A re-upgrade still downloads: a swap only installs what it re-verifies against the signed `SHA256SUMS`, and the root helper must not trust a bare binary. Tests: `TestGateFailureRestoresMigratedDB`, `TestRestorePreviousRecordsResult`; `upgrade-e2e.sh` step 5. |
| U4+ | the root `ExecStartPre` rollback (found while fixing U4) | Rollback copied the **marker's** `prev` path over the binary and restored snapshot entries to/from any path the marker named; the marker is in the data dir the `jarvisd` account writes, so that account could have a binary of its choosing run as root. | `cbf9d17` paths come only from the executable and `--home`: `<exe>.prev`; snapshot entries must be a regular `*.db` in the home and a regular file in `backups/`, no symlinks; data-dir temporaries (`*.tmp`, `*.restore`) are created with `O_EXCL`. Test: `TestRollbackIgnoresMarkerPaths`. |
| U6 | script upgrade (install.sh → `jarvisd upgrade`) | Ended at "up and healthy", no admin URL. | `45d876e` a successful upgrade or rollback of the installed service ends with `setup-link`'s line (admin URL, or the setup link before setup). install.sh `exec`s the upgrade, so `jarvisd upgrade` prints it. Doctor is still not run after an upgrade. Test: `TestEndWithAdmin`. |
| U8 | `jarvisd service uninstall --user` | Left linger on although install turned it on. | `aa3928b` install reads `Linger` first and, if it was off and `enable-linger` worked, writes `$XDG_CONFIG_HOME/jarvisd/linger-enabled`; uninstall runs `disable-linger` only when that mark exists (failure explained, mark kept). Test: `TestSystemdUserLinger`. |

Logged:

| # | Severity | Problem | Suggestion |
|---|---|---|---|
| U4 | info | A manual rollback *copies* `jarvisd.prev` over the binary, so afterwards both are rc1 and the rc2 binary is gone. | **Fixed** (above). |
| U5 | info | On rc1 the Update page's `install_command` is still `curl -fsSL …/install.sh \| sh` without `--user` (R1/R3, fixed in rc2; rc2's API was up to date, so not shown). | — |
| U6 | info | A script upgrade ends at "up and healthy": no doctor and no admin URL. | **Fixed** (admin URL; above). |
| U7 | info (process) | Restarting the dev jarvisd with `setsid nohup run.sh … &` from the agent's shell left that shell as its waiting parent (the harness tracked it as a background task); restarted with `setsid -f nohup …` instead, giving the recorded shape (parent systemd --user, own session). | Use `setsid -f` in the restore notes. |
| U8 | info | `jarvisd service uninstall --user` leaves linger on although install turned it on. | **Fixed** (above). |
| R10 | info | (from A10b) the admin's update/restart counts as a systemd restart in `service status`. | — |

## Not covered

The system-unit (`sudo`) path's `ExecStartPre` helper with a published release (CI's `upgrade` job covers
it with throwaway releases); the MBP and Windows (need the user); a failed upgrade's automatic rollback
with published releases (covered by `upgrade-e2e.sh`); a voice turn (optional, skipped).
