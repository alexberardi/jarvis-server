# A10d upgrade rehearsal `v0.1.0-rc2` → `v0.1.0-rc3` (recipes), this box, 2026-10-08

The second upgrade between published prereleases, aimed at what rc3 adds: the **recipes module on 7030**.
rc2 was installed the documented way, upgraded by re-running rc3's `install.sh`, recipes were exercised
through their API (CRUD, editor photo, planner week, staples, shopping list, webview URL import, photo
import), rc3 was rolled back by hand to rc2, upgraded again with the admin's **Update now**, then
everything was restored. Same procedure as [A10c](A10c-upgrade-rehearsal.md).

**Box:** 10.0.0.122 (Arch/Omarchy, systemd, RTX 3080 Ti, ufw admitting 10.0.0.0/24 on 7700:7712 and
7031 only, no passwordless sudo), so `--user`. Prod and the MBP untouched.

**Port 7030 beforehand:** free (`ss -ltnp`: the legacy dev jarvisd held 7700–7712 only; no legacy
recipes container on this box). Nothing to record for the doctor's "port held by another program" check.

**Reused:** the A10b/A10c home `~/.jarvisd.rehearsal-rc1-2026-10-08` (rehearsal superuser + household,
Qwen3.5-9B + mmproj, MiniLM, whisper small.en, Kokoro, ERes2Net, CUDA engines) renamed to `~/.jarvisd`;
every label `ready` straight away.

**What was preserved:** the dev jarvisd (`dev-433f208`, pid 3752171, `~/.jarvisd/bin/jarvisd serve` from
`run.sh`, cwd `~/jarvis/jarvis-server`, fd0 `/dev/null`, fd1/2 `~/.jarvisd/jarvisd.log`, parent systemd
--user, own session) was stopped with SIGTERM picked by `ps` + `awk` on argv (it and its three engines
gone in 0.07 s), home renamed `~/.jarvisd.pre-rc3`; afterwards renamed back and restarted with `setsid
-f nohup run.sh` from the same cwd: same command, stdio, parent, own session, and `JARVIS_*`/`ADMIN_API_KEY`
environment (sha256 of the sorted vars `9472f6d4…` before and after), engines healthy 5 s later,
jarvis-dev's MQTT reconnected 9 s after the restart (node journal: "MQTT connected", subscribed to its
topics). jarvis-dev's `config.json` was never touched (sha256 `68e496ae…`). Linger `no` → `yes` (rc2's
`service install --user`) → `no` (by hand, see V6). `~/.local/bin/jarvisd*` and `~/.local/lib/jarvisd`
did not exist before and don't now; the rehearsal home's `rehearsal-bin/` now holds `jarvisd` = rc3,
`jarvisd.prev` = rc2, `jarvisd-v0.1.0-rc1`.

## Timeline

| Step | Time | Notes |
|---|---|---|
| Stop dev jarvisd, swap homes | 0.07 s | SIGTERM, then two `mv`. |
| **rc2:** `curl -fsSLo install.sh …/v0.1.0-rc2/install.sh && sh install.sh --user` | 2.4 s | mise minisign shim → checksum only (as before). Unit written, healthy, doctor all OK on **12** TCP ports, labels `ready`, engines healthy. Settings lists 10 services, no recipes; `/services` has no `jarvis-recipes-server`; 7030 refused; `/api/system/info` lists `recipes` 7030 `served: false`. |
| **Script upgrade:** `curl -fsSLo install.sh …/v0.1.0-rc3/install.sh && sh install.sh --user` | **3.2 s** | "Upgrading jarvisd v0.1.0-rc2 -> v0.1.0-rc3 with `jarvisd upgrade`", signature verified by rc2's binary, snapshot `backups/jarvis-v0.1.0_rc2-20261008T161642…db`, swap, restart, gate passed, "jarvisd v0.1.0-rc3 is up and healthy" (rc2's CLI, so no admin line: V4). |
| rc3 checks | — | `last-upgrade.json` `succeeded rc2 → rc3`; **recipes migrations ran**: `goose_recipes` 1, 2 and `goose_recipes_settings` 1 applied at 16:16:42, 19 `recipes_*` tables; no other module's versions changed (diff of every `goose_*` table against the rc2 snapshot). `GET :7030/health` `{"status":"ok"}`, `/info` `jarvis-recipes-server`; 7030 bound by jarvisd; **`/services` lists `jarvis-recipes-server` `http://localhost:7030`** ("served by jarvisd"); admin `GET /api/settings` 11/11 services incl. `recipes` (the Settings page's Recipes card). Update page: up to date, rc3 the latest pre-release. **Doctor: FAIL** `firewall 10.0.0.0/24: ufw drops 7030/tcp` (V1); the admin dashboard's `/api/doctor` shows the same failure. |
| Recipes exercise (superuser JWT from 7701) | ~12 s | See below. All 201/200/202 as expected. |
| **Rollback:** `jarvisd upgrade --rollback` (rc3 CLI) | **0.5 s** to healthy | "restored …/jarvisd.prev; the database is unchanged (if the newer version migrated it, jarvisd refuses to start…)" (V3), "kept as …/jarvisd.rolledback", "jarvisd v0.1.0-rc2 is up and healthy", admin URL line (U2/U4/U6 fixes working on a published build). `last-upgrade.json` `rolled_back rc2 ← rc3, "rolled back by hand"` (U1). **The database was not restored** and rc2 started on rc3's migrated DB: rc2's downgrade guard checks only modules rc2 has, and rc2 has no recipes module, so `goose_recipes` was ignored. Doctor all OK on 12 ports. `recipes_recipes` still 3 rows. **`/services` still listed `jarvis-recipes-server` at localhost:7030** with nothing listening (V2). |
| **Admin Update now** (rc2) | **7.4 s** POST → rc3 healthy | `GET /api/update`: rc3 offered, `can_apply`, install command `curl -fsSLo install.sh …/v0.1.0-rc3/install.sh && sh install.sh --user`. `POST /api/update/apply {"version":"v0.1.0-rc3"}` → 202; checking → verifying (+0.25 s) → downloading 20.9 MB (+0.75 s) → unpacking → installing, marker `staged` (+1.5 s) → restarting, `swapped` (+1.8 s) → refused ~5 s → rc3, `last` `succeeded rc2 → rc3` (+7.4 s). New snapshot; `jarvisd.prev` rc2; `jarvisd.rolledback` (rc3) still there (V5); `NRestarts=1` (R10). |
| Recipes after re-upgrade | — | **All step-3 data present**: 3 recipes (pancakes with its `/media/01ead05e….jpg`, chili, webview soup), the plan with 8 items as `/planner/current`, staples salt + olive oil, the photo `GET /media/…` 200 2374 bytes. Doctor FAIL on 7030 again. |
| Restore | ~1 min | `jarvisd service uninstall --user --keep-firewall`, binaries into `rehearsal-bin/`, empty `~/.local/lib/jarvisd` removed, homes swapped back, dev jarvisd restarted (`setsid -f`), linger disabled by hand. |

### Recipes exercise (rc3, port 7030, rehearsal superuser's JWT)

| Call | Result |
|---|---|
| `POST /recipes` (4 ingredients, 2 steps, tag) → `GET /recipes/1` | 201 → 200 |
| `POST /recipes/import/image` (320×240 JPEG, multipart `file`) | 200 `{"image_url": "/media/01ead05ef5e31518f473f0473e9e77d6.jpg"}` |
| `GET /media/01ead05e….jpg` (no auth) | 200, `image/jpeg`, bytes identical, `public, max-age=31536000, immutable`, ETag |
| `PATCH /recipes/1 {"image_url": …}` | 200 |
| `POST /planner/commit` (7 dinners alternating two recipes + a breakfast) → `GET /planner/current` | 200 → 200, plan 1 with 8 items |
| `POST /staples` salt, olive oil | 201, 201 |
| `GET /shopping-list` (the week) | 200, plan_count 1, 7 items summed by unit (flour 10 cup, milk 7.5 cup, eggs 10, beef 3 lb, beans 3 can, sugar 5 tbsp, salt 3 tsp flagged staple) |
| **URL import, webview path:** `POST /recipes/parse-payload/async` (`client_webview`, one schema.org `Recipe` JSON-LD block, html snippet) | 200 PENDING → **COMPLETE in 2.0 s**, `parser_strategy: client_json_ld`, title, 4 ingredients, 3 steps; saved with `POST /recipes … parse_job_id` → 201 (job `COMMITTED`) |
| **Photo import:** `POST /recipes/from-image/jobs?title_hint=rehearsal&tier_max=3` (rendered 1240×1754 recipe card, Liberation fonts, q90) | 202 → **COMPLETE in 8.0 s**: tier 1 `tesseract` (score 4, gate passed), structured by the installed Qwen3.5-9B: "Lemon Garlic Chicken", all 7 ingredients with quantities/units/notes, all 5 steps verbatim |
| `GET /recipes/parse-url/jobs` | the image job (`COMPLETE`, preview title); the committed webview job not listed (default `status=COMPLETE`, by design) |

## Rollback and the new migrations vs §8.2

The brief expected the rollback to restore the snapshot because migrations ran. It didn't, and that **is
what [§8.2](00-installers.md) specifies**: a snapshot is restored only by a rollback of an upgrade still in
progress (marker present: failed gate, crash loop); `jarvisd upgrade --rollback` after the gate has passed
"just restores `jarvisd.prev` and leaves the DB (the guard then tells you about snapshots if needed)". The
guard did not need to: rc2 doesn't have the recipes module, so it never looks at `goose_recipes`, and the
recipes tables sat unused until rc3 came back with the data intact. That is the better outcome here
(nothing lost), but two messages were wrong about it: the rollback note promised a refusal to start
(V3), and the registry kept advertising 7030 (V2).

## Friction

Fixed (each with a test; branch `worktree-agent-a6a5665cb60c25844`):

| # | Where | Problem | Fix |
|---|---|---|---|
| V1 | `jarvisd upgrade` / `--rollback` | rc3 adds a listener (7030); on an install whose firewall admits jarvisd's ports one by one (or as 7700:7712), the upgrade ends "up and healthy" while ufw drops 7030 from the LAN: the phone's recipes app can't reach it. Only `jarvisd doctor` and the admin dashboard say so. | `bfff9e7` a successful upgrade or rollback of the installed service runs `<exe> doctor --json` (the binary now installed, so its ports, not the CLI's) and prints the failed checks with their fix and "`sudo jarvisd doctor --fix` applies the firewall fix". Tests: `TestReportDoctor`, `TestDoctorAfterRunsInstalledBinary`. On a script upgrade the CLI is the *old* binary, so this shows from the upgrade after the one that ships it. |
| V2 | config registry after a downgrade | After rc3 → rc2, `/services` still listed `jarvis-recipes-server` at localhost:7030 with nothing listening, so a client discovering it gets connection refused. | `42f370a` `syncSelf` deletes the rows it wrote (`served by jarvisd`) for services this jarvisd doesn't serve; operator and external rows stay. Test: `TestSelfRegistrationDropsUnservedRows`. Helps rollbacks to versions that carry it (not rc2). |
| V3 | `jarvisd upgrade --rollback` | The note said "if the newer version migrated it, jarvisd refuses to start" while rc2 started on rc3's migrated DB; the flag help promised a snapshot restore "if migrations ran". | `cd03de0` the note says tables of modules the restored version lacks stay for the next upgrade, and a migrated module it does have makes it refuse to start (naming it) → restore a snapshot; flag help says the snapshot is restored only before the gate passed. Test: `TestRestoredNote`. |

Logged:

| # | Severity | Problem | Suggestion |
|---|---|---|---|
| V1a | **action for existing installs** | Every install upgraded to rc3 needs 7030 admitted by hand: here `sudo ufw allow from 10.0.0.0/24 to any port 7030 proto tcp comment jarvisd` (or `sudo jarvisd doctor --fix`). Not run here (no sudo; the box's rule is the user's 7700:7712). Prod's cutover is a fresh install (install.sh adds every port), so it isn't affected. | Release notes for rc3: "run `jarvisd doctor` after upgrading". |
| V4 | info | A script upgrade's output comes from the installed (old) binary: rc2's `jarvisd upgrade` ended at "up and healthy" without rc3's admin line (U6) or the doctor (V1). | Inherent to the hand-off; fixes reach the script path one release later. |
| V5 | info | After the admin's Update now (performed by rc2, which predates U4), `jarvisd.rolledback` (rc3) was left next to the binary. | Gone once the swapping binary has U4. |
| V6 | info | Linger stayed on after `service uninstall --user`: rc2 did the install, before U8, so there was no `linger-enabled` mark for rc3's uninstall to honour. Disabled by hand (`loginctl disable-linger`). | Transitional; installs made by rc3+ record the mark. |
| V7 | info | rc2's install still ends "Setup is done (or jarvisd hasn't started yet)" (U3, fixed in rc3). | — |
| R10 | info | The admin update counts as a systemd restart (`NRestarts=1`). | as before |

## Not covered

The system-unit (`sudo`) path and its root `ExecStartPre` helper with a published release; the MBP (needs
the user's sudo) and Windows; a URL import through `parse-url/async` against a real site (the webview
path is what the app uses when the server can't fetch; private hosts are refused by the SSRF guard);
meal-plan generation and the grocery SKU match job (covered by the contract's slow tier in R10); the
phone app against the rehearsal install (7030 blocked from the LAN anyway, V1).
