# The mobile E2E runner: Maestro flows from the mobile repo, on the harness emulator

- **Date:** 2026-10-18 (user chose where the flows live, option A of loop item #144; the rest by
  agent, E2E piece 4/9)

`scripts/e2e mobile` (and `all`) runs the mobile app's Maestro flows against the run's rich
sandbox. How to run it: [`../dev/e2e.md`](../dev/e2e.md). It builds on
[`2026-10-17-e2e-runner.md`](2026-10-17-e2e-runner.md).

- **The flows live in jarvis-node-mobile (user).** jarvis-server has only the runner; the flows
  sit next to the app's code in its `.maestro/`, so a mobile PR changes the app and its flows
  together. The shared subflows, the starter flows (login per role, the 5 tabs, chat with the
  scripted model, settings, logout) and the skip tags are a jarvis-node-mobile item, and so are
  the later flow pieces (#145, #153).
- **One commit gives both the APK and the flows.** By default `origin/main` of the mobile repo
  (fetched first, as `scripts/mobile-shot` does); `--mobile-repo PATH` takes the `HEAD` commit
  of another checkout or worktree, to test a branch. Only committed work counts: the APK is
  built from a `git archive`, and the flows come out of the same archive.
- **Same emulator as mobile-shot, shared code.** The AVD (`jarvis-harness` under
  `~/.cache/jarvis-harness/mobile/avd`), the cached release APK, the headless `-read-only`
  emulator on a port of its own (5580+), the adb reverse tunnels and the install moved into
  `scripts/lib/mobile.sh`, sourced by both scripts. Every adb call names the harness emulator's
  serial; the user's own emulator is never touched. The emulator is killed after the suite.
- **Maestro is pinned and checksummed:** 2.11.0, `maestro.zip` from the GitHub release, its
  sha256 in `scripts/lib/mobile.sh`, unpacked once into
  `~/.cache/jarvis-harness/maestro/<version>/` like the other harness tools. It needs Java 17+.
  A version bump changes both the version and the hash.
- **One Maestro process per flow** (`tools/e2emobile`), not `maestro test <dir>`: logcat is
  cleared before each flow, and a failure's artifacts are of that flow's last screen. On a
  failure, `.e2e/<run>/mobile/<flow>/` keeps `screenshot.png` (`screencap`), `window.xml`
  (`uiautomator dump`: texts and testIDs), `logcat.txt`, `maestro.log` and Maestro's
  `--debug-output` (`maestro/`). A flow gets 10 minutes, then it counts as `timedOut`.
  `results.json` uses the web suite's record shape, plus `skip_reason` and `attachments`.
- **No flows is an error, not a pass.** A checkout without `.maestro/`, or with no flow there
  (or none matching `--filter`), makes the suite `error`, so a broken checkout never looks
  green. With `all`, `--filter` applies to both suites.

## The contract with the flows (jarvis-node-mobile's `.maestro/`)

- **What runs:** every top-level `*.yaml`/`*.yml` in `.maestro/`, in file-name order, except
  `config.yaml`. Subflows go in a subdirectory (`.maestro/subflows/login.yaml`, used with
  `runFlow: subflows/login.yaml`) and are never run on their own. Today's `login.yaml` is a
  subflow at the top level, so the mobile item moves it.
- **Each flow stands alone:** it starts with `launchApp: {clearState: true}` and logs in
  itself (through the shared subflow). Flows run in one app install on one emulator, in file
  order, but must not depend on that order or on another flow's leftovers.
- **Skipping:** tag the flow `skip` and give the reason in its header's properties:

  ```yaml
  appId: com.jarvis.nodemobile
  name: Biometric login
  tags: [auth, skip]
  properties:
    skip_reason: needs biometric enrollment, which the headless emulator has not got
  ---
  ```

  It is reported as `skipped` with that reason (and without one, as "no
  properties.skip_reason given"). biometric-login, provisioning-devmode-happy-path and
  k2-crypto-roundtrip need this: biometrics, a DEV_MODE dev-client build and the fake node
  (piece 7).
- **The environment** (`-e`, use as `${NAME}`):

  | variable | value |
  |---|---|
  | `EMAIL`, `PASSWORD` | the seeded superuser (what today's `login.yaml` reads) |
  | `EMAIL_SUPERUSER` | the same superuser |
  | `EMAIL_ADMIN`, `EMAIL_POWER`, `EMAIL_MEMBER` | the household admin, power user and member; every role uses `PASSWORD` |
  | `CHAT_MESSAGE`, `CHAT_REPLY` | a message and the scripted model's exact reply to it (`what is 12 times 7` → `12 times 7 is 84.`) |
  | `TIMER_MESSAGE`, `LIST_MESSAGE`, `DEVICE_MESSAGE` | a message the scripted model answers with one tool call: `set_timer` (600 s, "tea"), `shopping_list` (add "oat milk"), `control_device` (Kitchen Ceiling, `light.kitchen_ceiling`, turn on) |
  | `TIMER_REPLY`, `LIST_REPLY`, `DEVICE_REPLY` | the reply when the node ran the tool |
  | `TIMER_REPLY_OFFLINE`, `LIST_REPLY_OFFLINE`, `DEVICE_REPLY_OFFLINE` | the reply when the node is offline: what the suite sees, as nothing runs the seeded node in a sandbox |
  | `INBOX_DETAIL_TITLE` | the superuser's seeded InboxDetail item (markdown body, sources) |
  | `INBOX_ACTIONS_TITLE`, `INBOX_ACTIONS_BUTTON`, `INBOX_ACTIONS_RESULT` | the seeded suggestion card (InboxDetail with server-plane buttons), its button that opens InboxCallbackResult, and the result's heading there (tapping it also records a suppression) |
  | `INBOX_LIST_TITLE`, `INBOX_DEPLOYED_TITLE` | the seeded InteractiveList and AdapterDeployed items |

  A flow's own `env:` defaults are overridden by these. The scripted model answers from
  `e2e/llm/mobile.json` (in this repo) during the mobile suite; a new chat flow that needs
  another answer adds a rule there. The seeded inbox items are `scripts/seed-dev.py`'s (rich
  profile); tests in `tools/scriptllm` and `tools/e2emobile` keep these values in step with the
  script and the seed (added 2026-10-22, #457).
- **The app** is the release APK `scripts/mobile-shot` uses (`com.jarvis.nodemobile`, JS
  bundled, `EXPO_PUBLIC_DEV_MODE=true`, config URL `http://localhost:7700`, which adb reverse
  points at the sandbox). It is not a dev client: no Metro, no dev menu.

The iOS lane runs these flows on the iOS Simulator too, and adds two rules to this contract
(platform conditions instead of copies, and `PUSH_URL`):
[`2026-10-18-e2e-ios-lane.md`](2026-10-18-e2e-ios-lane.md).
