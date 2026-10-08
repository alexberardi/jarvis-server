# A10e macOS (Metal) rehearsal: installed `v0.1.0-rc4` on the MBP, Apple Vision photo import, 2026-10-08

The Mac checks left open by A10b–A10d and R8/R10: the installed LaunchDaemon's health, signing and
notarization of the published binary, the 5 legacy `image_based` photo sets with **Apple Vision**,
and recipes basics on Metal.

**Box:** MacBook Pro `alexanderberardi@10.0.0.103`, M2 Max, 32 GB, macOS 26.1 (25B78). jarvisd
**v0.1.0-rc4** installed by the user through the wizard as the LaunchDaemon
`net.jarvisautomation.jarvisd` (home `~/.jarvisd`, live + background = Qwen3.5-9B + mmproj on Metal,
MiniLM embeddings, whisper small.en, Kokoro, ERes2Net; jarvis-dev node registered to it and in use
during the run). Prod untouched. No sudo was available or used.

**Rule for the installed daemon: read only.** `jarvisd doctor`, `jarvisd service status`,
`launchctl print`, `codesign`/`spctl`, process list, `sqlite3 -readonly` on `~/.jarvisd/jarvis.db`.
Everything that needed a JWT ran in a **throwaway jarvisd** (the same `/usr/local/bin/jarvisd`, run
as my own process): home `/tmp/a10e/home`, `JARVIS_HOST=127.0.0.1`, listeners 627xx/62030/62031,
MQTT 62883/62884, `JARVIS_MDNS=0`, `--no-browser`; its own superuser through its setup token. The
Metal llama-server build was copied in from `~/.jarvisd/engines` and the installed Qwen3.5-9B +
mmproj were **registered in place** (`POST /v1/models/installed` with `catalog_id`, sha256-checked,
`external: true`; nothing downloaded, nothing copied, never deleted). Live label: context 16384,
background `shared`, embeddings off.

## 1. Installed daemon health

| Check | Result |
|---|---|
| `jarvisd version` | `v0.1.0-rc4` |
| `jarvisd doctor` (as the user) | all OK, rc 0: listening on all **13** TCP ports; firewall "no active host firewall found"; sleep "this Mac does not go to sleep"; data dir + DB owner-only; legacy stack's files in `~/.jarvis` but none of its containers run. |
| `jarvisd service status` | `supervisor: launchd`, `running, pid 88315`, `/Library/LaunchDaemons/net.jarvisautomation.jarvisd.plist, last exit (never exited)`, home `~/.jarvisd`, `health: ok`. |
| `launchctl print system/net.jarvisautomation.jarvisd` | `type = LaunchDaemon`, `username = alexanderberardi`, `umask = 77`, program `/usr/local/bin/jarvisd serve --home …/.jarvisd`, stdout/stderr `~/.jarvisd/logs/jarvisd.log`, **PATH = `/usr/bin:/bin:/usr/sbin:/sbin`** (see M1). |
| Updater helper | **None exists for launchd** (ID11 is not built; §8.2 "Not done"). Only the one plist in `/Library/LaunchDaemons`. `/usr/local/bin/jarvisd` is `root:wheel 0755`, so the admin's Update button answers 409 "jarvisd can't replace its own binary … needs administrator rights" with `sudo jarvisd upgrade` (`applyBlocker`); not exercised (no JWT on the installed daemon, no sudo). |
| Metal engines (process list) | children of pid 88315: `whisper-server/b5454-metal` (small.en), `llama-server/b11457-metal` MiniLM `--embedding -ngl 0 -dev none`, `llama-server/b11457-metal` **Qwen3.5-9B `-c 32768 -ngl 999 --jinja --chat-template-file …/7f0e529032c25183.jinja --mmproj …/mmproj-F16.gguf`** (RSS 7.3 GB). The Qwen engine had been restarted at 17:08 by the user's model change (log: start → healthy in 5 s). |
| Notarization | `spctl -a -t open --context context:primary-signature -v /usr/local/bin/jarvisd` → **accepted, source=Notarized Developer ID**. |
| Signature | `codesign -dv --entitlements -`: Identifier `net.jarvisautomation.jarvisd`, `flags=0x10000(runtime)`, Timestamp Oct 8 2026 13:22:17, TeamIdentifier `8H5GA7SX77`, entitlements only `com.apple.security.cs.disable-library-validation`; `codesign --verify --strict` valid, satisfies its DR; no `com.apple.quarantine` xattr. |
| Application firewall | `socketfilterfw --getglobalstate` → **disabled (State = 0)**, stealth off; so doctor's OK is right and no allow entry exists to survive upgrades (§8.4's DR claim still unexercised). |
| Recipes on the installed DB | 0 recipes, 0 parse jobs, 0 ingestions (nobody has used 7030 yet). `ocr_settings` empty → `ocr.enable_apple_vision` and `ocr.enable_llm_proxy_vision` both **false**. |

## 2. Apple Vision on the 5 legacy `image_based` photo sets

**How jarvisd reaches Apple Vision on macOS:** it has no in-process Vision binding. The ocr module's
`apple_vision` engine is built only when `JARVIS_OSX_API_URL` (+ `JARVIS_OSX_API_KEY`, an `ocr:read`
key minted by jarvis-osx-api's own CLI) is in the environment, and it POSTs `/v1/ocr` to the legacy
**jarvis-osx-api** (`com.jarvis.osx-api` LaunchAgent, pyobjc Vision, port 7723); the setting
`ocr.enable_apple_vision` then switches it on live. The installed daemon has neither variable.

Reading an existing osx-api key was refused by the permission system, so the throwaway used **its own
throwaway osx-api**: the osx-api source copied to `/tmp/a10e/osxapi-src` (without `.env`), run with
the existing venv's Python on `127.0.0.1:62723`, `OSX_API_DATA_DIR=/tmp/a10e/osxapi-data`, and a fresh
`ocr:read` key minted into that data dir. The user's osx-api (pid 67925, 7723) and its key store were
not touched. Vision needs no TCC grant and worked over SSH (one page: 806 ms).

**Harness:** each set's images in name order as `images` parts to `POST /recipes/from-image/jobs?tier_max=3`,
poll `GET /recipes/jobs/{id}`, then the legacy test's fuzzy check
(`test_recipe_parsing_integration.py`: title substring AND (ingredient hit rate ≥ 0.6 OR step hit
rate ≥ 0.5)). Model: Qwen3.5-9B Q4_K_M everywhere (Metal on the MBP; CPU on this box for the
tesseract baseline, same GGUF via a throwaway rc4 linux binary).

**MBP has no tesseract** (not in `/opt/homebrew/bin`, `/usr/local/bin`), so "Apple Vision +
tesseract" (R8's Mac assumption) isn't what a Mac has; the tesseract-only baseline ran on this box.

| Set (images) | Apple Vision only (MBP, Metal) | Apple Vision + LLM vision (MBP) | LLM vision only (MBP) | tesseract only (this box, CPU) |
|---|---|---|---|---|
| all_crust_sheet_pan_lasagna (2) | COMPLETE 68 s, "All-Crust Sheet-Pan Lasagna", ing 0.88 (13), **pass** | COMPLETE 121 s, same title, ing 0.88, pass | **ERROR `quality_gate_failed`** | COMPLETE 184 s, same title, ing 0.88 (13), pass (P3 cleanup timed out on CPU, draft kept) |
| date_night_chicken_mushroom (1) | COMPLETE 74 s, "Date Night Chicken in Creamy Mushroom Sauce", ing 0.67 (19), **pass** | COMPLETE 129 s, pass (LLM vision reading timed out at 60 s; Apple Vision carried it) | **ERROR `ocr_no_text`** (LLM vision 60 s timeout) | **ERROR `quality_gate_failed`** (41 chars read, gibberish; as R8) |
| fake_fried_chicken_special_sauce (2) | COMPLETE 24 s, "Fried Chicken Sandwich with Special Sauce", ing 0.71 (21), **pass** | COMPLETE 107 s, pass | COMPLETE 103 s, pass | COMPLETE 62 s, "Fried Chicken Sandwich", ing **0.43** (13), fuzzy **fail** |
| one_pot_creamy_shells (1) | COMPLETE 14 s, "One-Pot Creamy Shells with Peas and Bacon", ing 0.50 (13), fuzzy **fail** (expectation) | COMPLETE 58 s, same | COMPLETE 58 s, same | COMPLETE 56 s, "one-Pot Creamy Shells with Peas and Bacon", ing 0.50, fail (expectation) |
| spicy_beef_bowls (1) | COMPLETE 16 s, "Spicy Beef Rice Bowls", ing **1.00** (17), fuzzy **fail** on title (expectation) | COMPLETE 115 s, same | COMPLETE 68 s, same | **ERROR `text_structuring_exception`** "not enough ingredients" (R8 on Qwen3-8B: "Avocado Rice Bowls") |
| **Strict legacy check** | **3/5** (5/5 correct recipes) | 3/5 | 1/5 | **1/5** (3/5 usable drafts) |

**Apple Vision reads all 5 photos correctly.** The two strict "fails" are the expectation files, not
the parse: the photo's printed title is literally "Spicy Beef Rice Bowls" (Apple Vision's first line),
which contains neither `spicy beef bowls` nor `beef bowls` as a substring; and `one_pot_creamy_shells`'
expected ingredients (`ground meat`, `cream`, `spinach`) aren't in that recipe (it is bacon, peas and
half-and-half: 6 slices bacon, 8 oz frozen peas, … per the OCR text). The step hit rate is 0.00 in
every run because the expected steps are paraphrases ("brown meat"); the drafts carry the steps
verbatim (e.g. the shells' 12 steps from "Line a plate with paper towels." on). Every Apple Vision
reading passed the quality gate (score 3–4; `providers_used ["apple_vision"]`, tier 1).

Against **tesseract** (R8 on Qwen3-8B saw the same pattern): Apple Vision turns 2 tesseract failures (date night unreadable, spicy beef too few ingredients) and one partial read (fried chicken: 13 of 21 ingredients, half the title) into complete, correct drafts, and is faster: 14–74 s per set on Metal vs 56–184 s for tesseract + the same model on CPU (the LLM dominates; Apple Vision itself is ~0.8 s per page). Same model, same prompts, so the OCR engine is the difference.

LLM vision (Qwen3.5-9B's own projector) is slower (60–130 s per set) and fails two sets on its own:
the 60 s per-image `chatCompletion` timeout (legacy value) is too short for a dense page on a 9B model
while the same engine (`-np 1`) also serves P2; and its lasagna transcription
("PREV RECIPE ... NEXT RECIPE ...") didn't pass the gate. Adding it to Apple Vision changed no result
and doubled the time.

**The installed daemon, as configured, fails every photo import:** with no tesseract, no osx-api
variables and LLM vision off by default, the throwaway with both settings off answered
`ERROR ocr_unavailable` "No OCR providers available" in 2.1 s — exactly the installed MBP's state
(M2).

## 3. Recipes basics on Metal (throwaway, superuser JWT)

| Call | Result |
|---|---|
| `POST /recipes` (4 ingredients, 2 steps, tag) → `GET /recipes/1` | 201 → 200 |
| `POST /recipes/import/image` (the spicy-beef JPEG, multipart `file`) | 200 `{"image_url": "/media/01f314d6….jpg"}`; `GET /media/…` (no auth) 200, **bytes identical**; `PATCH /recipes/1 {image_url}` 200 |
| **Webview URL import:** `POST /recipes/parse-payload/async` (`client_webview`, one schema.org JSON-LD block) | 200 PENDING → **COMPLETE in 0.5 s**, `client_json_ld`, "Rehearsal Soup", 3 ingredients, 2 steps; `POST /recipes … parse_job_id` → 201, job `COMMITTED` |
| `POST /planner/commit` (7 dinners alternating the two + a breakfast) → `GET /planner/current` | 200 → 200, plan 1, 8 items |
| `POST /staples` salt, olive oil | 201, 201 |
| `GET /shopping-list` (the week) | 200: eggs 10, flour 10 cup, milk 7.5 cup (5 pancake servings summed), salt 5 tsp flagged `is_staple`, the soup's lines listed (they had no quantities: my commit body copied the draft without its quantity fields) |
| **Photo import** | the 20 jobs of §2; `GET /recipes/parse-url/jobs` lists every image job COMPLETE with its preview title (the committed webview job is not listed: default `status=COMPLETE`) |

## Friction

Fixed (each with a test; branch `a10e-macos-rehearsal`):

| # | Where | Problem | Fix |
|---|---|---|---|
| M1 | ocr engine discovery | launchd starts the LaunchDaemon with `PATH=/usr/bin:/bin:/usr/sbin:/sbin`, so a Homebrew tesseract (`/opt/homebrew/bin`, `/usr/local/bin` on Intel) or MacPorts' (`/opt/local/bin`) would never be found by the installed service even after `brew install tesseract`. | `2cf50d9` `findTesseract`: PATH first, then those three dirs (executable regular file, symlinks followed; none on Windows). Tests: `TestFindTesseract*`. |
| M2 | ocr start | Nothing says a jarvisd has no text OCR engine; on this Mac every recipe photo import fails (`ocr_unavailable`) and neither doctor nor the log mentions it. | `d6f0cf6` a start-up WARN when neither tesseract nor Apple Vision is built, naming the fixes. Test: `TestNoTextEngineHint`. |

Logged (need a decision):

| # | Severity | Problem | Suggestion |
|---|---|---|---|
| M3 | ~~high for Mac installs~~ **fixed (ID13, `fa6f4ee`)** | A fresh macOS install has no working OCR: Apple Vision needs the legacy jarvis-osx-api (Python, a LaunchAgent, a hand-minted key, two env vars in `jarvisd.env`), tesseract isn't installed by anything, and LLM vision is off by default. Photo import is a headline recipes feature. | Done as (a): Apple Vision in process on darwin through purego's Objective-C runtime, on by default on macOS; the osx-api route stays as a fallback when its env vars are set. Results in §4. |
| M4 | ~~medium~~ **fixed (`fa6f4ee`)** | LLM vision's per-image timeout is 60 s (legacy); Qwen3.5-9B on an M2 Max needs longer for a dense page while the single slot is shared with P2. | `ocr.llm_vision_timeout_seconds`, default 180 s, read live; and `ocr.Module.Recognize` now keeps the finished engines' readings when the caller's 5-minute OCR deadline passes. Results in §4. |
| M5 | low | The legacy `image_based` expectations are wrong for two sets (title "Spicy Beef Rice Bowls"; shells has bacon/peas, not ground meat/spinach) and every expected step is a paraphrase no draft can contain. | Fix the expected.json files if the sets become a CI/live gate. |
| M6 | info | No launchd updater helper (ID11) exists yet, so the Mac's admin Update button can only print `sudo jarvisd upgrade`. | As ID11. |
| M7 | info | The MBP's application firewall is off, so §8.4's "allow entry survives upgrades" is still unverified. | Verify on a Mac with the firewall on. |

## 4. Follow-up: native Apple Vision in jarvisd (ID13, M3/M4 fixed), 2026-10-08

`fa6f4ee` (`internal/modules/ocr/vision*.go`) reads with Vision in process on darwin; design in
`docs/schema/ocr.md` "Engines". Verified on the same MBP with a **throwaway jarvisd** built from
that commit (`GOOS=darwin GOARCH=arm64 CGO_ENABLED=0`), **ad-hoc re-signed with the hardened
runtime and the release entitlements file** (`codesign -s - --options runtime --entitlements
scripts/macos/jarvisd.entitlements`: `flags=0x10002(adhoc,runtime)`, only
`disable-library-validation`), i.e. the notarized binary's runtime constraints: Vision loaded with
no new entitlement and no TCC prompt, over SSH. Home `/tmp/id13/home`, `JARVIS_HOST=127.0.0.1`,
listeners 647xx/64030/64031, MQTT 64883/64884, `JARVIS_MDNS=0`, `--no-browser`; own superuser via
its setup token; llama-server copied from `~/.jarvisd/engines`; the installed Qwen3.5-9B + mmproj
registered in place (`catalog_id`, external); live label context 16384, background `shared`,
embeddings off. **No osx-api, no tesseract, no env vars, no OCR setting touched.**

| Check | Result |
|---|---|
| Start-up | no "no text OCR engine" WARN; `ocr.enable_apple_vision` **true** (`from_db: false`, the darwin default); `ocr.llm_vision_timeout_seconds` 180. |
| `GET /v1/providers` (app creds) | `apple_vision: true` (`reason ok`, detail `native (Vision.framework)`), tesseract false, llm_proxy_vision false. |
| `POST /v1/ocr/batch` `provider=apple_vision` | spicy beef 0.77 s, 70 lines, mean block confidence 1.00, first line "Spicy Beef Rice Bowls"; date night 0.61 s, 92 lines, 0.995 ("STAUB", "Date Night Chicken", "in Creamy Mushroom Sauce"); lasagna p2 0.53 s, 64 lines. Pixel boxes inside the image. |
| `go test` (darwin test binary on the MBP) | the whole ocr package green, incl. `TestNativeAppleVision` (real Vision: rendered text, boxes in bounds, hints `nil`/`en`/an unknown language, garbage and empty input fail cleanly, a done context, 8 concurrent calls). |

**The 5 `image_based` sets, same harness as §2 (`tier_max=3`, legacy fuzzy check):**

| Set | Native Apple Vision (this run) | osx-api Apple Vision (§2) |
|---|---|---|
| all_crust_sheet_pan_lasagna | COMPLETE 68 s, "All-Crust Sheet-Pan Lasagna", ing 0.88 (13), pass | COMPLETE 68 s, same, ing 0.88 (13), pass |
| date_night_chicken_mushroom | COMPLETE 74 s, "Date Night Chicken in Creamy Mushroom Sauce", ing 0.67 (19), pass | COMPLETE 74 s, same, 0.67 (19), pass |
| fake_fried_chicken_special_sauce | COMPLETE 24 s, "Fried Chicken Sandwich with Special Sauce", ing 0.71 (21), pass | COMPLETE 24 s, same, 0.71 (21), pass |
| one_pot_creamy_shells | COMPLETE 14 s, "One-Pot Creamy Shells with Peas and Bacon", ing 0.50 (13), fuzzy fail (expectation, M5) | same |
| spicy_beef_bowls | COMPLETE 16 s, "Spicy Beef Rice Bowls", ing 1.00 (17), fuzzy fail on title (expectation, M5) | same |
| **Strict legacy check** | **3/5, 5/5 correct drafts** | 3/5, 5/5 correct drafts |

**Exact parity**: same titles, same ingredient counts and hit rates, same times; every job
`providers_used ["apple_vision"]`, tier 1, no failed images.

**M4 check** (Apple Vision off, LLM vision on, date night alone): no timeout any more (the reading
ran ~85 s, past the old 60 s), but Qwen3.5-9B answered with an empty transcription (`""`), so the
job ended `quality_gate_failed` instead of §2's `ocr_no_text`. The timeout was the first wall; LLM
vision's reading quality on this model is a separate matter (it runs on the `background` label,
whose default thinking budget is unlimited (-1) within `max_tokens` 4096, a likely cause; not
investigated). Apple Vision is the default now, so this no longer decides whether a Mac can import
photos.

Cleanup: the throwaway jarvisd (pid 91537, picked by `ps` + `awk` on argv) stopped with TERM and
its llama-server went with it; `/tmp/id13` removed; nothing listens on 64xxx. Installed daemon
afterwards: same pids (88315; engines 88385, 88386, 90505), `service status` running/health ok,
both Qwen3.5-9B files in place; the user's osx-api (67925) untouched.

## Cleanup

Throwaway jarvisd (pid 90847) and its llama-server, the throwaway osx-api (pid 90821) stopped by pid
(picked with `ps` + `awk` on argv); `/tmp/a10e` (home, copied engine, osx-api copy and key, photos,
driver) removed. The throwaway rc4 linux jarvisd on this box and its scratch home removed. The
installed daemon afterwards: same pid 88315 and engine pids (88385, 88386, 90505), `jarvisd doctor` all OK on
13 ports, `service status` running/health ok, `/usr/local/bin/jarvisd` unchanged (root:wheel, 13:54, sha256
`cdd728fd…`), both Qwen3.5-9B files still in `~/.jarvisd/models`; the user's osx-api (pid 67925) still running.
Nothing was written to `~/.jarvisd` or `~/jarvis/jarvis-osx-api`.

## Not covered

`sudo jarvisd upgrade` and any updater path on the Mac (no sudo); the application firewall's allow
entry (firewall off); the phone app against 7030 on the installed daemon; meal-plan generation and the
grocery SKU job on Metal (covered by the contract's slow tier elsewhere).
