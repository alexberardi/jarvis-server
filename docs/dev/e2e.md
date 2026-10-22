# End-to-end regression (`scripts/e2e`)

`scripts/e2e` runs the regression suites against sandboxes it brings up itself, writes a report,
and takes the sandboxes down again. It is run on demand (the user, or the loop's trigger, E2E
9/9). Why it is built this way: [`../decisions/2026-10-17-e2e-runner.md`](../decisions/2026-10-17-e2e-runner.md).

```sh
scripts/e2e web                       # the admin SPA suite
scripts/e2e                           # every suite (all): web, then mobile
scripts/e2e mobile                    # the mobile app's Maestro flows on a headless harness emulator
scripts/e2e mobile --mobile-repo ~/src/jarvis-node-mobile-branch   # flows + APK of that checkout's HEAD
scripts/e2e web --filter 'inbox'      # only tests whose title matches (Playwright --grep)
scripts/e2e web --keep                # leave the sandboxes up to poke at afterwards
scripts/e2e web --name loop-142       # pick the sandbox name (default e2e-<run>); never default
scripts/e2e computer --llm scripted   # the computer-task suite's plumbing (below; not in all)
```

Exit status: 0 when every test passed, 1 when one failed or a suite could not run.

## What it brings up

- `<name>`: `scripts/sandbox up --profile rich --llm scripted --ui`: the seeded Avery household,
  a login per role (`SBX_EMAIL`, `SBX_EMAIL_ADMIN`, `_POWER`, `_MEMBER`, one password), the
  seeded nodes, and the scripted model (`docs/decisions/2026-10-17-scripted-llm.md`), built from
  this checkout with the SPA.
- `<name>-setup`: `scripts/sandbox up --profile empty`: no seeding, no superuser, first-run
  setup still to do. `sandbox.env` has `SBX_SETUP_TOKEN` (the wizard link is
  `$SBX_ADMIN/setup#token=$SBX_SETUP_TOKEN`). Only the setup wizard's test uses it, so
  `scripts/e2e mobile` does not bring it up.

Both are purged afterwards (`scripts/sandbox down --purge`) unless `--keep`. A run takes about
2 minutes on the dev box (build, seed, ~70 browser tests).

## The report

Everything a run leaves is under `.e2e/<run>/` (gitignored), `<run>` = `YYYYMMDD-HHMMSS-xxxxxx`:

- `report.md`: verdict, counts, a table per test, and per failure the error, the screenshot
  inline, the trace and the page's console errors.
- `report.json`: the same for programs (the loop's trigger, E2E 9/9, and the coverage gate
  read it). Keep its shape stable: add fields, don't rename or remove them.
- `web/results.json`: the web suite's own records (`e2e/web/reporter.ts`), merged into
  `report.json` by `scripts/lib/e2e_report.py`. A new suite writes `<suite>/results.json` in the
  same shape and `scripts/e2e` lists it.
- `web/artifacts/<test>/`: per failed test `test-failed-N.png` and `trace.zip`
  (`cd e2e/web && npx playwright show-trace <path>`).
- `mobile/results.json` (`tools/e2emobile`), and per failed flow `mobile/<flow>/`:
  `screenshot.png`, `window.xml` (uiautomator dump), `logcat.txt`, `maestro.log` and Maestro's
  debug output `maestro/`. Skipped flows carry `skip_reason`; a record's `attachments` lists
  the files beyond the screenshot.
- `web.log` (Playwright's own output), `mobile.log` (APK build, emulator, Maestro),
  `sandbox-<name>.log` (sandbox start-up).

`report.json`, schema 1:

```json
{
  "schema": 1,
  "run": "20261017-225237-6caf1c",
  "status": "passed | failed",
  "started_at": "2026-10-17T22:52:37-04:00",
  "finished_at": "2026-10-17T22:54:16-04:00",
  "sandbox": "e2e-20261017-225237-6caf1c",
  "filter": null,
  "git": {"sha": "…", "branch": "…", "dirty": false},
  "counts": {"total": 43, "passed": 43, "failed": 0, "skipped": 0},
  "suites": {
    "web": {"status": "passed | failed | error", "exit_code": 0, "error": null, "duration_ms": 87400},
    "mobile": {"status": "passed | failed | error", "exit_code": 0, "error": null, "duration_ms": 312000}
  },
  "tests": [
    {
      "suite": "web",
      "id": "…",
      "title": "inbox.spec.ts › deleting from the detail page removes the item",
      "file": "tests/inbox.spec.ts",
      "line": 32,
      "status": "passed | failed | timedOut | skipped | interrupted",
      "duration_ms": 600,
      "error": null,
      "screenshots": ["web/artifacts/…/test-failed-1.png"],
      "trace": "web/artifacts/…/trace.zip",
      "console_errors": ["/inbox/…: Failed to load resource: … 404 (Not Found)"]
    },
    {
      "suite": "mobile", "id": "biometric-login", "title": "Biometric login",
      "file": ".maestro/biometric-login.yaml", "line": 1, "status": "skipped", "duration_ms": 0,
      "error": null, "screenshots": [], "trace": null, "console_errors": [],
      "skip_reason": "needs biometric enrollment, which the headless emulator has not got"
    }
  ],
  "coverage": {
    "web": {"entries": 34, "covered": ["/chat", "…"], "manual": ["<route>: <reason>"], "pending": [],
            "invalid": [], "missing": null, "stale": null, "checked_by": "CI (web/admin/src/coverage.test.tsx)"},
    "mobile": {"entries": 64, "covered": ["RoutinesStackNavigator/ScheduleList"], "manual": ["…"],
               "pending": ["MainTabNavigator/HomeTab: #145: nav-tabs.yaml, home-chat.yaml"],
               "invalid": [], "missing": [], "stale": [], "checked_by": "this run's mobile suite"}
  }
}
```

Paths are relative to `.e2e/<run>/`. A suite with `status: error` did not run (its `error` says
why, e.g. a sandbox that did not come up); the run is then `failed` too. `console_errors` are
recorded, not failures: a refused password or a deleted item's 404 logs one on purpose.

`coverage` (and report.md's Coverage section) is the coverage gate's state, below. `missing`
and `stale` are `null` where this run did not check them: always for web (CI does), and for
mobile when the mobile suite did not run.

## The coverage gate (`e2e/coverage.json`)

Every admin SPA route and every mobile screen has an entry in `e2e/coverage.json`: the specs
or flows covering it, or why it has none. **A new page or screen needs an entry in the same
PR**, or the check fails. Why and the format:
[`../decisions/2026-10-18-e2e-coverage-gate.md`](../decisions/2026-10-18-e2e-coverage-gate.md).

```json
"web":    {"/admin/traces/:id": {"tests": ["e2e/web/tests/admin/traces.spec.ts"]}},
"mobile": {"RoutinesStackNavigator/ScheduleList": {"flows": [".maestro/schedules.yaml"]},
           "DevicesStackNavigator/CameraView": {"manual": "a live camera stream; the sandbox has no camera device"},
           "NodesStackNavigator/AddComputer": {"pending": "#154 gave it a code to approve (…); the approval flow is jarvis-node-mobile's, not written yet"}}
```

- **Web, in CI:** `web/admin/src/coverage.test.tsx` walks the real route tree (`appRoutes()`
  in `routes.tsx`, which `App.tsx` renders) and fails on a route with no entry, an entry for a route
  that is gone, an entry that isn't exactly one kind, and a spec file that doesn't exist.
- **Mobile, in `scripts/e2e mobile`:** the suite's first test, `coverage: …`
  (`tools/e2emobile/coverage.go`), reads the `<X.Screen name=…>` registrations of the same
  mobile commit's `src/navigation/` (as `<Navigator file>/<name>`; a registration whose
  component is another navigator there is a container and not listed) and fails the same
  ways, with flow files looked up in that commit's `.maestro/`. It also fails when merged
  flows hide behind `pending`: a `pending` text that names a flow file which exists
  (`"#145: nav-tabs.yaml"` once `nav-tabs.yaml` is there), and a flow the suite runs (a
  top-level `.maestro/*.yaml` not tagged `skip`) that no entry lists under `flows`. By hand:
  `go run ./tools/e2emobile coverage --coverage e2e/coverage.json --flows <checkout>/.maestro --nav <checkout>/src/navigation`.
- `pending` is a screen with a test still to come (name the item); `manual` is one an
  automated test can't reach here (camera, microphone, third-party sign-in, the fake-node
  lane). When a pending flow lands, turn its entry into `flows`: the next mobile run fails until
  you do. jarvis-node-mobile's `.maestro/COVERAGE.md` says which screens each flow reaches.

## The web suite (`e2e/web/`)

Playwright Test in TypeScript, pinned to the Playwright version of `tools/screenshots` so the
cached Chromium is shared. One worker: the suite shares one sandbox, one active model script and
one auth rate limit.

| file | covers |
|---|---|
| `setup-wizard.spec.ts` | first-run setup on the empty sandbox: every page leads to the wizard; the link's token; superuser created; every model step skipped (nothing downloaded); the token is spent; the new superuser signs in |
| `auth.spec.ts` | anonymous → login; superuser sign-in; wrong password refused, then the right one; log out (server session revoked, app closed); temporary password forces a change, which then sticks |
| `account.spec.ts` | display name saved (server and page, survives reload); change password (form checks, old one refused, new one works) |
| `chat.spec.ts` | scripted reply shown; a tool call's effect (`remember` saves a memory, checked through the API); device picker (the turn goes through the picked node, remembered across reload); a photo with a vision model (sent, shown, in the model's request) and no photo button without one |
| `inbox.spec.ts` | list with unread marks and the sidebar badge; detail renders markdown and reads it (server and badge); delete from the detail; bulk mark read and delete |
| `push.spec.ts` | the push each event sends, as the fake relay recorded it (title, body, `data.type`, `data.inbox_item_id`, priority, target tokens): a node's timer, reminder and alert inbox items (and the web inbox opens the item the push names); a node push (confirmation card); send_link (`open_url`); a household push reaches every phone in it and none elsewhere; a service's `notify` and `notify/batch`; no push for an inbox item that asked for none; a token the relay calls unregistered is deactivated; a signed-out token gets nothing |
| `addons.spec.ts` | the page's sections; a paired desktop agent is listed; signing it out removes it on the server |
| `household.spec.ts` | the switcher appears with two households; the inbox and the chat devices follow the pick, across a reload; no switcher with one |
| `pairing.spec.ts` | a simulated node (below) asks to pair; the superuser types its code and a room on the Nodes page; the agent joins and its card shows up online in that room |
| `roles.spec.ts` | the role matrix from `routes.tsx`/`Sidebar.tsx`: superuser sees and opens every admin page, every old top-level path and `/admin` itself forward to their `/admin` page (query kept), an unknown path shows the 404 page in the shell and under `/admin`; admin, power user and member see Chat/Add-ons/Inbox only, get the notice on every admin URL, and the server answers the admin pages' calls with 403 |
| `admin/dashboard.spec.ts` | System card = `/api/system/info`; no Restart when unsupervised; Stop asks and Cancel leaves jarvisd up (stop is never sent); "Finish setup" vs "Voice isn't ready" banner from `setup.completed`, the latter naming the missing jobs and leading to their models; no update or GPU banner without their state |
| `admin/models.spec.ts` | every catalog model listed; Install asks and nothing downloads; a job's Set up narrows the catalog to its kind, Show all restores it; a model assigned to the live label is saved (server and page, across a reload; background follows). Skipped: Hugging Face installs |
| `admin/settings.spec.ts` | a household's own value of a household setting: edited, saved on the server, kept across a reload; Use default removes it; the default untouched |
| `admin/connections.spec.ts` | listeners and external services with their health as the server has it; an app client created here: its key (shown once) authenticates, Rotate replaces it, Revoke stops it |
| `admin/logs.spec.ts` | lines sent through jarvis-logs' ingest found with the module, text and level filters; context opens; no match says so |
| `admin/traces.spec.ts` | a chat turn becomes a trace: on the dashboard's recent requests, in the list, and its detail is that turn |
| `admin/nodes.spec.ts` | a pairing computer approved into a household (it then registers and is listed), another denied (told so, gone); a signed-out node behind Show signed-out nodes. Skipped: rename/remove (not in the web admin) |
| `admin/users.spec.ts` | every account with households, roles and badges; a generated and a chosen temporary password both sign the user in to the forced change; a deactivated account (seeded) marked and not resettable. Skipped: invite, role change, disable (mobile only in v1) |
| `admin/update.spec.ts` | checks off: says so, the running version, no update offered; the switch saved on the server both ways; apply is blocked throughout. Skipped: an available update and applying it |

### A simulated node for pairing

`scripts/sandbox node-sim --name N [--node-name X]` pairs a node with no hardware: it builds
`./cmd/jarvis-agent` and runs the real agent headless (`enroll --code --profile pi`, then
`run --voice=false`), with its own HOME, XDG dirs and no desktop session under
`.sandbox/N/simnode/X/`. It prints the pairing code on stdout once the agent shows it (cc never
lists codes, so this is the only place it comes from) and also writes it to
`.sandbox/N/simnode/X/code`. Once a member approves the code, the agent answers yes to "Join
this household?", registers and stays up as the node (MQTT, heartbeats) until
`scripts/sandbox down`. The agent's output is `.sandbox/N/simnode/X/agent.log`.

In the web suite, `startSimNode(name)` (`helpers/simnode.ts`) runs it and returns the code.
The mobile app's approval is a Maestro flow in jarvis-node-mobile (after the Maestro runner,
#144): start `node-sim`, read the code from the `code` file, approve it in the app, and check
the node in the app's node list.

### Writing a test

- Import `test` and `expect` from `helpers/fixtures.ts` (it records console errors).
- Assert behaviour: the data changed on the server (`helpers/api.ts`) and the page shows it, not
  only that a page loaded.
- Signed in as a seeded role: `test.use({ storageState: stateFile('member') })` (written once by
  `global-setup.ts`, through the login form). API calls as that role: `tokenOf('member')`.
- Anything that changes a user or needs data nobody else sees makes its own throwaway user:
  `const u = await newUser('prefix')` (own household, its admin), then `await asUser(u)` for a
  signed-in page. Tests must not depend on order or on another test's leftovers.
- Login, register and refresh share one per-IP limit of 30 a minute and every test comes from
  127.0.0.1. `asUser` starts from the registration's session instead of the form; the form is
  `auth.spec.ts`'s. A 429 waits the minute out once (the test timeout allows it).
- Never stop, restart or update the shared sandbox, and put back any install-wide setting a
  test changes; what each admin page's test leaves out, and why:
  [`../decisions/2026-10-17-e2e-admin-pages.md`](../decisions/2026-10-17-e2e-admin-pages.md).
- A form sign-in that should succeed: fill it, then `pressSignIn(page)` (`helpers/login.ts`), which
  waits out the rate limit once.
- Pushes: `registerDevice(u)` gives the user a fake phone, `pushTo(token, title)` waits for the
  push the fake relay got (`helpers/push.ts`). Give each push a `unique()` title: the server
  drops an identical one within 60 s.
- Chat answers come from `e2e/llm/web.json` (`useScript` switches it); add a rule there for a
  new conversation, and match on the user's words.
- Run one file against a kept sandbox while writing it:
  `scripts/e2e web --keep --filter 'my test'`, then
  `cd e2e/web && (source ../../.sandbox/<name>/sandbox.env; E2E_OUT=/tmp/x npx playwright test -g 'my test')`
  (the setup test also needs `E2E_SETUP_ADMIN` and `E2E_SETUP_TOKEN` from `<name>-setup`).

## The mobile suite (`scripts/e2e mobile`)

The Maestro flows of jarvis-node-mobile's `.maestro/`, played by `tools/e2emobile` one Maestro
process per flow on a headless harness emulator (the AVD, APK and adb reverse of
`scripts/mobile-shot`, shared in `scripts/lib/mobile.sh`), logged in to the run's rich sandbox.
Why, and the contract the flows follow (which files run, the `skip` tag and
`properties.skip_reason`, the `-e` variables `EMAIL`, `PASSWORD`, `EMAIL_ADMIN`/`_POWER`/`_MEMBER`,
`CHAT_MESSAGE`/`CHAT_REPLY`): [`../decisions/2026-10-18-e2e-mobile-runner.md`](../decisions/2026-10-18-e2e-mobile-runner.md).

- Needs what `scripts/mobile-shot` needs (Linux, KVM, the Android SDK, mise for the APK build)
  plus Java 17+ for Maestro, which the first run installs into
  `~/.cache/jarvis-harness/maestro/<version>/` (pinned, sha256-checked).
- The flows and the APK come from one commit: `origin/main` of `$JARVIS_MOBILE_REPO` (default
  `../jarvis-node-mobile` next to the main checkout), or the `HEAD` of `--mobile-repo PATH`.
  Commit flow changes before running them; uncommitted edits are not picked up.
- During the suite the scripted model answers from `e2e/llm/mobile.json`.
- adb transport drops: the harness emulator's adb connection can drop (adb's log:
  `emulator-5580: connection terminated: read failed`), and every drop wipes all the adb reverse
  tunnels and kills Maestro's on-device driver. So before every flow (and the real push lane)
  `e2emobile` waits for the device, sets the tunnels again (`--reverse`, from `reverse_pairs` in
  `scripts/lib/mobile.sh`) and probes the sandbox's `/health` from inside the emulator
  (`--health-port`). A flow that fails on a drop (`device offline`, `not found`,
  `DeviceServerDied`), or finds the device not ready, is played again, up to 3 tries with a
  growing pause (5 s, then 10 s), and before each retry the device is brought back:
  `adb -s <serial> reconnect` when adb does not list it as a `device` (that kicks this one
  transport; `reconnect offline` is host-wide and is never used), then two good `get-state` +
  `sys.boot_completed` checks in a row (3 s apart, at most 2 min), then
  `am force-stop dev.mobile.maestro.test` and `dev.mobile.maestro` so the next `maestro test`
  starts a fresh driver. The report says so on the flow (`note`, with the host's load average
  and available memory at each drop; the earlier tries' artifacts stay in `<flow>.try1`, ...)
  and `results.json` counts the retries (`device_retries`). Every adb call carries
  `-s <serial>`: never `adb kill-server`, nor `reverse --remove-all` without `-s`, as other
  emulators may be attached.
  What we know of the cause (#428, 2026-10-21): the host's one adb server (1.0.41,
  platform-tools 37.0.1) ran from 10-18 to 10-21 without a restart, and the only connection to
  the emulator's adb port (5581) was that server's, so a second server or Maestro's direct dadb
  fallback (it connects to ports 5555-5683 itself only when no adb server answers) is not the
  cause. Port 5580 is inside adb's scan range. Each emulator shutdown logs one `connection
  terminated` as well, so count drops between boot and kill. `adb -s <serial> reconnect`
  reproduces a drop exactly (`connection terminated: read failed`, then `DeviceServerDied` in
  the flow), but it also logs `resetting transport`, which the bad run's log lacked: nothing
  on the host kicked it. What the drops did line up with is memory: the dev box (31 GB) had
  23 GB in swap, during a 4-minute run it swapped about 9 GB out with up to 750 MB of the
  emulator's own memory in swap, and the one drop in a 19-minute run came after the emulator
  got no CPU for about 20 s while the host's load doubled (a stall, not a busy guest). The AVD
  already has 4 cores, so `-cores`/`-memory` flags would not help. To keep it from stalling,
  run the emulator outside swap (for example under
  `systemd-run --user --scope -p MemorySwapMax=0`, untried: the loop's agents may not run it)
  or keep the box from swapping while the lane runs. `boot_emulator` warns when the host has
  less than 6 GB available.
- Pushes: the superuser has a fake device token (`PUSH_TOKEN`); a flow checks
  `${FAKE_RELAY}/control/pushes?token=${PUSH_TOKEN}` from a `runScript` and can make a node
  event happen through `${CC_URL}` with `${NODE_API_KEY}` for `${USER_ID}`
  ([Push notifications](#push-notifications)).
- Writing a flow: run it by hand against a kept sandbox and a kept emulator
  (`scripts/mobile-shot --sandbox <name> --keep --screens login`, then
  `~/.cache/jarvis-harness/maestro/2.11.0/maestro/bin/maestro --device emulator-<port> test
  -e EMAIL=… -e PASSWORD=… .maestro/my-flow.yaml`), then the whole suite with
  `scripts/e2e mobile --mobile-repo <checkout> --filter my-flow`.

## Push notifications

Two layers ([`../decisions/2026-10-18-e2e-push.md`](../decisions/2026-10-18-e2e-push.md)):

- **Every run: a fake relay.** Every sandbox starts `tools/fakerelay` (port 7797 + offset) and
  points `relay.url` at it (`scripts/sandbox up --relay fake`, the default). It speaks the real
  relay's protocol and records each push instead of sending it: `GET $SBX_FAKERELAY/control/pushes`
  (`?token=`, `?household_id=`, `?title=`), `DELETE` to forget them, and every push is also a line
  in `.sandbox/<name>/pushes.jsonl`. A token starting with `fail:` answers `DeviceNotRegistered`;
  `POST /control/fail {"status": 503, "count": 1}` fails the next sends. `--relay none` is an
  install without push; `--relay URL` a real relay.
- **Overnight: real FCM.** `scripts/e2e mobile --real-push` runs one test (suite `mobile-push`)
  instead of the flows: on a Google Play emulator (its own AVD, `jarvis-harness-play`) the app,
  built with the push config, signs in through the mobile repo's login subflow and registers its
  real Expo token with a sandbox on the real relay; a node alert is sent; the test waits for it in
  the shade (`cmd statusbar expand-notifications` + `uiautomator dump`, up to 3 minutes), taps it
  and checks that the app opened the item. The shade and the opened screen are kept as
  `mobile-push/shade.png`, `opened.png` (and `.xml`).

  What it needs, else the test is **skipped** with the reason in the report:

  | need | how |
  |---|---|
  | the relay | `E2E_REAL_RELAY_URL` (e.g. `https://relay.jarvisautomation.io`), answering from this box. The relay holds the Expo credentials; jarvisd only registers the sandbox's household (`/v1/register`) |
  | the app's push config | `E2E_PUSH_GOOGLE_SERVICES`: the `google-services.json` of the Firebase project behind the Expo app's FCM credentials. It goes into the build as `./google-services.json` and `$GOOGLE_SERVICES_JSON`; the app config must name it in `android.googleServicesFile`, or the build stops saying so. The APK is cached apart from the default one |
  | a Google Play image | `sdkmanager 'system-images;android-36;google_apis_playstore;x86_64'` (FCM needs Play services; no Google account needed) |

  The two variables come from the environment or from the push.env file, which scripts/e2e
  reads from `~/.config/jarvis-harness/push.env` by default (or from `$JARVIS_HARNESS_PUSH_ENV`):
  copy [`push.env.example`](../../push.env.example) to `~/.config/jarvis-harness/push.env` and
  fill it in, or point `$JARVIS_HARNESS_PUSH_ENV` at your copy. Keep it out of the repo; a real
  push goes to the emulator only, since only its token is registered in the throwaway sandbox.

  What the user must provide before the lane can run — it is **skipped**, with the reason in
  the report, until they exist, and so will an overnight `--real-push` lane once jarvis-loop
  schedules one:

  - the relay URL, reachable from the dev box (it holds the Expo push credentials);
  - the `google-services.json` of the Firebase project behind the Expo app's FCM credentials
    (the FCM V1 service-account key uploaded in EAS credentials must be from the same project);
  - jarvis-node-mobile's app config naming `android.googleServicesFile` (the build stops
    otherwise);
  - the Google Play image: `sdkmanager 'system-images;android-36;google_apis_playstore;x86_64'`
    (the harness creates the AVD `jarvis-harness-play`);
  - the push.env file itself: `~/.config/jarvis-harness/push.env`, copied from
    [`push.env.example`](../../push.env.example).

## The iOS lane (`scripts/e2e ios`)

The same flows on the iOS Simulator of a Mac with Xcode, against a `--lan` sandbox. Why,
and what the flows add for iOS (platform conditions, `PUSH_URL`):
[`../decisions/2026-10-18-e2e-ios-lane.md`](../decisions/2026-10-18-e2e-ios-lane.md). `all`
does not include it.

Run the sandbox on the Mac itself (the MacBook Pro): nothing has to cross a firewall.

1. `scripts/sandbox up --name e2e-ios --profile rich --llm scripted --lan`
   (`JARVIS_LAN_HOST=<ip>` if it guesses the wrong address). It is open to the LAN until step 3.
2. `scripts/e2e ios --sandbox-env .sandbox/e2e-ios/sandbox-lan.env [--mobile-repo PATH] [--filter RE]`.
3. `scripts/sandbox down --name e2e-ios --purge`.

The sandbox can also run on the dev box, with `sandbox-lan.env` copied to the Mac for step 2,
but the dev box's ufw admits only the installed jarvisd's ports, not the sandbox's (its own
port + the offset `up` prints: config, admin and the services on 7700-7712, the scripted model
and the fake relay on 7797-7798, recipes and OCR on 7030-7031, MQTT on 1884). Open them to the
Mac alone for the run, and close them after step 3 (offset 10000 here):

```sh
sudo ufw allow from <mac-ip> to any port 11884,17030:17031,17700:17798 proto tcp
sudo ufw delete allow from <mac-ip> to any port 11884,17030:17031,17700:17798 proto tcp
```

- Needs Xcode (not just the command-line tools: the Mac mini has only those) with the iOS 18.0
  simulator runtime, CocoaPods, node 22 (or mise), Go and Java 17+. The first run creates the
  harness's simulator, `jarvis-harness-e2e`; it is erased before every run and shut down after.
  The user's simulators are never used.
- The app is a Release build for the simulator, unsigned, with the sandbox's config URL baked in,
  cached per mobile commit and config URL in `~/.cache/jarvis-harness/mobile/ios/` (the first
  build takes a while; its log is `build.log` there).
- On a failure, `.e2e/<run>/ios/<flow>/` keeps `screenshot.png`, `hierarchy.json`,
  `device.log`, `maestro.log` and `maestro/`.
- After a failed flow the runner reboots the simulator before the next one (the flow's
  `note` says so): a system alert the flow left up, such as iOS's 'Open in "Jarvis
  Automation"?' after an `openLink`, survives `launchApp` with `clearState` and would fail every
  later flow at `landing-login-button`. The flow itself still has to answer that alert.
- Writing an iOS step: `xcrun simctl boot <udid of jarvis-harness-e2e>` and
  `~/.cache/jarvis-harness/maestro/2.11.0/maestro/bin/maestro --device <udid> test -e … flow.yaml`.

## The voice lane (`scripts/e2e voice`)

The real node software hears spoken commands and its spoken replies are checked. Why it is
built this way: [`../decisions/2026-10-17-e2e-voice-lane.md`](../decisions/2026-10-17-e2e-voice-lane.md).
`all` does not include it: it takes the real model and a few minutes.

```sh
scripts/e2e voice                          # the real model through the tap, WAV files as mic and speaker
scripts/e2e voice --llm scripted           # the lane itself, on e2e/llm/voice.json (no installed jarvisd)
scripts/e2e voice --audio pipewire         # a private PipeWire's virtual mic and speaker (barge-in too)
scripts/e2e voice --filter 'timer|silent' --repeat 1
scripts/e2e voice --agent-arg=--wake-threshold=0.6   # any extra `jarvis-agent listen` flag
scripts/e2e voice --audio pi --pi-host <user>@<pi4-node>   # a real Pi in the room (overnight only, below)
scripts/e2e voice --voices ~/voices        # recorded human voices instead of the TTS, where there is one
```

It brings up one sandbox, `scripts/sandbox up --profile rich --voice --llm shared|scripted`,
builds `cmd/jarvis-agent` and `tools/voicelane`, and runs every case in
`e2e/voice/cases.json` against `jarvis-agent listen --profile pi`, which has its own `HOME`
and XDG dirs under the run and the sandbox's seeded node identity. Per case:

1. The mic's audio: the sandbox's TTS says the case's lines (`say`), with silence and generated
   noise (`noise`: `pink` or `music`; `over` mixes a part over the end of the ones before it).
2. The node hears it: wake word, endpointing, speech-to-text and the voice turn through cc,
   the model, TTS, and the reply played to its speaker, which is recorded.
3. The sandbox's whisper transcribes the recording, and the case checks `expect`: `outcome`
   (default `spoken`), `reply` (a regexp on the transcript), `tool` (called by the model, from
   the run's new `llm.jsonl` lines, or run by the node), `max_secs` (the response time), or
   `silent` (no turn, no reply audio, no model call).

`model: true` cases run `--repeat` times (default 3) and pass only when every run does. Read
them by the regressions-only rule: a regression is 3/3 on main and 0/3 on the change. `needs:
pipewire` cases (barge-in) need real-time audio: they are skipped with `--audio wav`, whose
speaker is written at once, and run with `pipewire` and `pi`.

`--voices DIR` says a `say` line with a recording where there is one: `DIR/<slug of the
text>.wav` ("Hey Jarvis." is `hey-jarvis.wav`, "What time is it?" `what-time-is-it.wav`), any
sample rate (resampled to the TTS's); other lines are still the TTS.

| case | checks |
|---|---|
| wake + timer | "Hey Jarvis. Set a timer for five minutes." gets a spoken reply about the timer (the Go Pi node has no timer tool yet) |
| lights | "…Turn on the kitchen lights." gets a reply about the lights (no `control_device` for this node yet) |
| a question | "…How many days are there in a week?" is answered seven |
| the time in another city, through the clock tool | "…What time is it in Tokyo?" calls `get_current_time` and says a time (the local time is in the prompt, so a plain "what time is it?" is answered without the tool, #229) |
| no wake word stays silent | the timer and lights commands without "Hey Jarvis" |
| background noise stays silent | 8 s of pink noise |
| music with talk over it stays silent | a chord loop with a sentence spoken over it |
| barge-in stops a long reply | "Hey Jarvis" over a long story ends the turn `interrupted` (`--audio pipewire` or `pi`) |

What it leaves under `.e2e/<run>/`: `voice/results.json` (the report's test shape plus each
case's `runs`: the turn, the reply heard, the response time, the model's calls and tools, the
failures), `voice/cases/<case>/run-N/` (`utterance.wav`, `reply.wav`, `agent.log`, `run.json`;
`report.md` lists them under a failure), `voice.log`, and with PipeWire `voice/pipewire.log`
and `voice/agent.log` (the one long node; on a Pi also what the Pi side did, and
`results.json`'s `pi`: the card, the services before and after, what was stopped and
restored).

- **Offline caches.** A box that can't reach GitHub's release CDN can seed
  `$JARVIS_HARNESS_CACHE` (default `~/.cache/jarvis-harness`) with
  `engines/whisper-b5454-bin-ubuntu-x64/` (the extracted CPU whisper-server release jarvisd
  fetches; copied into the sandbox before it starts) and `openwakeword/` (the three wake models,
  checked against their pinned SHA-256). Without them jarvisd and the node download as usual.
- **PipeWire** needs `pipewire`, `wireplumber`, `pw-cli`, `pw-dump`, `pw-metadata`, `pw-play`,
  `pw-record` and alsa-lib's `pipewire` PCM (`/etc/alsa/conf.d/50-pipewire.conf`). It runs with
  `XDG_RUNTIME_DIR` and `PIPEWIRE_RUNTIME_DIR` set to `.e2e/<run>/voice/rt`, the pulse and
  session-bus variables unset, and is killed when the run ends.
- **A new case** goes in `e2e/voice/cases.json` (unknown keys are an error); with
  `--llm scripted` it needs a rule in `e2e/llm/voice.json` too.

### On a real Pi (`--audio pi`, overnight only)

The same cases and asserts on real hardware: the Pi 4 lab node (`<pi4-node>`, a Seeed 2-mic HAT),
in a room where playing speech out loud for half an hour is fine, hence overnight (the E2E
schedule, loop item #152, runs it). Why it is built this way:
[`../decisions/2026-10-18-e2e-pi-voice-lane.md`](../decisions/2026-10-18-e2e-pi-voice-lane.md).

- **What the Pi needs:** ssh key auth from the dev box for `--pi-host` (`BatchMode`: no
  prompts), aarch64, bash, `aplay`/`arecord` (alsa-utils), the HAT's driver loaded, and
  passwordless `sudo -n systemctl` when a system service (the installed Jarvis node) must be
  stopped. The wake models go along from the harness cache when they are there; otherwise the
  node on the Pi downloads them.
- **What a run does:** builds the node for linux-arm64 (fetching the arm64 sherpa-onnx
  libraries once), copies it, the sandbox's seeded node identity, the wake models and
  `tools/voicelane/pi.sh` into a fresh `mktemp -d /tmp/jarvis-e2e-*` on the Pi, and runs it
  there with `HOME` and XDG dirs in that directory, over one ssh session whose `-R` forwards
  put the sandbox's ports on the Pi's loopback. The node's config URL is
  `http://127.0.0.1:<sandbox port>`: the dev box's sandbox, never prod (voicelane refuses a
  sandbox whose services aren't on 127.0.0.1).
- **Leaving the Pi as found:** it records the active `jarvis*`, `pulseaudio*`, `pipewire*` and
  `wireplumber*` services (system and user), stops every Jarvis node (it would hear the cases and
  answer through its own server) and, only when the card is busy, the audio servers (sockets
  first). When the run ends, however it ends (done, a failure, Ctrl-C, voicelane killed: the
  Pi side watches its ssh stdin), it stops the test node, starts exactly what it stopped and
  deletes the run's directory, which holds the node identity: that is the un-pairing (the
  sandbox, and the node in it, are purged afterwards unless `--keep`). Both states are in
  `voice/agent.log` and printed as `voice: pi: …`.
- **Audio:** ALSA only, from an `.asoundrc` in the run's `HOME` (the Pi's own ALSA config is not
  touched): the node listens on a dsnoop of the HAT and speaks into its dmix through a tee that
  writes what it plays to the run's directory, fetched and transcribed per case. Each case's WAV
  is played with `aplay` straight into the same dmix, beside the node's stream and never
  through it, so the mic hears it in the room. The playback level is the Pi's mixer setting
  (`alsamixer -c <card>`); the response time is the turn less its recording and its reply's
  playback, as with WAV files.
- **When it fails to start:** the error says which step (`ssh`, the architecture, no seeed card:
  `--pi-card N`, a service it could not stop, the card still busy and who holds it).

## The computer-task suite (`scripts/e2e computer`)

Not in `all`, like `voice`: it takes the real model and many minutes. It builds and runs
`tools/ccsuite run --out .e2e/<run>/computer --rel computer`, which brings up its **own** sandbox
(`ccsuite-<run>`, purged afterwards, so `--name` and `--keep` don't apply), the fixture sites, and
a headless sway or Xvfb session with the real agent and Chromium; the cases are
`tools/ccsuite/cases/*.json`. What it covers and how a case is written:
[`computer-suite.md`](computer-suite.md).

```sh
scripts/e2e computer --llm scripted                    # the plumbing (CI's check; every pending case skipped)
scripts/e2e computer --filter 'store_' --repeat 3      # some cases, three runs each
scripts/e2e computer --include-pending --filter web_form
```

Overnight, on the dev box (a model run takes the installed jarvisd's one slot, so not while the
house uses it):

```sh
scripts/e2e computer --llm shared --label background --repeat 3
```

It leaves `computer/report.md`, `report.json` and `results.json` (one test per case; a pending
case that ran and failed is listed skipped, with how it failed, so only `required` cases fail the
run), `computer/cases/<case>/run-N/` (`state.json`: what the checks read; `failure.png`) and
`computer/logs/` (sandbox, sites, session, agent, browser), plus `computer.log`.

## Home Assistant (`--ha`)

Not built yet; split into its own item. `scripts/sandbox up --ha` will run the official Home
Assistant image (pinned) with demo and template devices, onboarded by script, so the smart-home
setup (discovery, auth, import, rooms) runs against the real thing. Its device-control test
stays skipped until the Go node talks to Home Assistant (#176/#177): on the server, control goes
through a node. See
[`../decisions/2026-10-18-e2e-real-ha-and-sim-node.md`](../decisions/2026-10-18-e2e-real-ha-and-sim-node.md).

## What stays manual

The suites cover what can run unattended against real local systems. These stay with a person:

- **Camera**: the camera and its streams need real hardware (a lens, a scene, the device's
  encoder); an emulator's fake camera only proves that a frame arrives, not that the feature works.
- **Biometrics**: the fingerprint and face prompts are the OS's own dialogs. They can't be
  passed honestly from a script on the emulator, and faking the result tests the fake.
- **Visual polish**: spacing, alignment and how a screen looks need a person's judgment. The
  screenshot tools (`scripts/screenshots`, `scripts/mobile-shot`, `scripts/desktop-shot`) make
  that review quick, and flag overflow and console errors, but don't decide it.
- **Packages** (pantry and store installs): out of scope by the user's decision (2026-10-17);
  they depend on the public pantry service and change outside this repo.
