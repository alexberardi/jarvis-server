# Screenshots of the mobile app (Android emulator)

`scripts/mobile-shot` runs the mobile app (`jarvis-node-mobile`) in a throwaway Android
emulator, logs it in to a `scripts/sandbox` jarvisd, visits every main screen in light and
dark (and optionally a larger font), sends one chat message, and saves a PNG and a UI dump of
each screen. An agent can Read the PNGs, or read the `.xml` dumps and `index.json` for the
text without needing vision. The user's own emulator is never touched.

## Run it (Linux)

```sh
scripts/sandbox up --name mob --llm shared      # chat needs a model; --llm none + --no-chat otherwise
scripts/mobile-shot deps                        # once: checks the SDK, creates the AVD, builds the APK
scripts/mobile-shot --sandbox mob               # landing … inbox_detail, light + dark, one chat round trip
scripts/mobile-shot --sandbox mob --screens settings --themes dark --font-scale 1.0,1.3
scripts/mobile-shot --sandbox mob --keep        # leave the emulator up; the next run skips the boot
scripts/mobile-shot stop --sandbox mob          # kill a kept emulator
scripts/sandbox down --name mob --purge
```

`--help` lists every flag. `--ref REF` builds another commit of the mobile repo (default
`origin/main`, fetched first), `--no-fetch` never fetches into it, and `--rebuild` ignores the
cached APK.

### Which mobile repo

`JARVIS_MOBILE_REPO` (default: `jarvis-node-mobile` next to the main jarvis-server checkout)
can be any git repo: a normal checkout, a worktree or a bare repo. The script only reads it
(`rev-parse`, `show`, `archive`). The one write is the `git fetch origin main` before an
`--ref origin/main` build; pass `--no-fetch` when someone else keeps the repo current, as the
agent loop does with its mirror:

```sh
JARVIS_MOBILE_REPO=~/path/to/jarvis-node-mobile.git \
  scripts/mobile-shot --sandbox loop-42 --ref "$LOOP_HEAD" --no-fetch
```

A bare mirror has `refs/heads/main` but no `origin/main`, so give it a sha or `main`.

### Concurrent runs

Every run shares one build dir (`~/.cache/jarvis-harness/mobile/build`, with its
`node_modules` and `build.log`). A build takes `flock` on
`~/.cache/jarvis-harness/mobile/build.lock`, so a second run that needs a build waits
("another mobile-shot is building …") instead of clobbering the first. Once it has the lock it
checks the APK cache again, so two runs of the same commit build it once. A cached APK needs no
lock, and the APK is written to a temp name and renamed into place, so a reader never sees half
of one. Emulators don't collide either: each run takes its own port (see Isolation).

The output goes to `.sandbox/<name>/mobile/<timestamp>/`:

| File | What it is |
|---|---|
| `<screen>__<theme>[__f<scale>].png` | The whole 1080x2400 display. Screens: `landing`, `login` (logged out), `home`, `devices`, `pantry`, `routines`, `nodes`, `settings`, `inbox`, `inbox_detail` (logged in) and `chat`. `inbox_detail` is the newest inbox item's detail: the first `inbox-item-*` card of the inbox, tapped; with an empty inbox it is skipped with `no inbox items`. A font scale other than 1.0 adds `__f1.3`. |
| `….p2.png`, `….p3.png` | The next screenfuls of a scrolling screen. The tool swipes up and shoots again while the content still moves, up to `--pages` (3). |
| `<same>.xml` | The uiautomator dump for that shot: every view's text, content description, testID (`resource-id`), hint and bounds. |
| `index.json` | Per shot: the visible texts, the views sticking out past the screen edge (`overflow`), the mean luminance with `theme_ok` (is a dark shot actually dark?), and `still_loading` when a spinner or "Loading…" never went away. Also whether login worked, the chat message with its reply and timing, `skipped` (a screen with nothing to show in one look, with its theme, font scale and reason; not a failure), and `failures`. |
| `logcat.txt` | The app's JS console (`ReactNativeJS`) and crashes. |

The script exits 1 when login, a screen or the chat failed, so it doubles as a smoke test of the
app against the current server.

The chat round trip goes from Home: it waits until the composer is ready (the seeded `kitchen`
node is online once the script has made one node request for it, and the app has finished
"Loading tools…"), types the message, sends it, and waits until the reply stops changing.
The default message is "what is 12 times 7", and the reply must match `\b84\b`. Use `--chat`,
`--chat-expect` or `--no-chat` to change that. The sandbox node isn't a real Pi, so it
reports no tools ("0 tools loaded"), and a message that needs a node tool will hear that the
node is offline.

## How it works

1. **APK.** The script builds a release APK, so the JS is bundled in and no Metro is needed.
   It takes a `git archive` of the mobile repo's commit into
   `~/.cache/jarvis-harness/mobile/build`, and keeps `node_modules` there while
   `package-lock.json` is unchanged. Then it runs `npx expo prebuild --platform android
   --clean` and `./gradlew assembleRelease -PreactNativeArchitectures=x86_64`. Expo's template
   signs the release build with the debug key. Node 22 (from the repo's `mise.toml`) and Java
   17 come from mise. The result is cached as
   `~/.cache/jarvis-harness/mobile/apk/jarvis-mobile-<sha>-r<recipe>.apk`. The user's checkout
   is only read (`git archive`, and `git fetch` unless `--no-fetch`), and its `android/`
   directory is never regenerated. No git worktree is registered in their repo. One build runs
   at a time (a `flock` on `build.lock`, see Concurrent runs).
2. **Server URL.** The build bakes in `EXPO_PUBLIC_DEV_MODE=true` and
   `EXPO_PUBLIC_MANUAL_CONFIG_URL=http://localhost:7700`, the same pair the repo's
   `development-e2e` EAS profile uses (see `src/config/env.ts` and
   `configDiscoveryService.ts`). They are needed because the emulator is NAT'd and can't see
   mDNS. One APK serves every sandbox, because the port mapping is done with adb reverse
   instead. `DEV_MODE` also shows the dev toggle on the node-scan screen, which is the only
   other effect it has.
3. **Emulator.** The AVD is `jarvis-harness`: the same system image as the user's
   `jarvis-pixel8` (android-36 google_apis x86_64), a pixel_8 profile, 3 GB of RAM, no
   camera or audio, and a hardware keyboard so the soft keyboard never covers the screen. It
   is created in `~/.cache/jarvis-harness/mobile/avd` (`ANDROID_AVD_HOME`), so it never shows
   up in the user's AVD list or Android Studio. Each run starts it with `-read-only
   -no-snapshot -no-window -no-audio -no-boot-anim -gpu swiftshader_indirect` on the first
   free even port from 5580. `-read-only` means every boot starts from the same clean image,
   several runs can share the AVD, and nothing is written back to it. The script turns off
   animations, and demo mode freezes the status bar at 9:41 with no notification icons.
4. **Network.** `adb -s <serial> reverse` maps each sandbox listener (config, auth, logs,
   cc, llm, whisper, tts, notifications, recipes, ocr, admin, plus MQTT 1884/9883 shifted by
   the offset) to the same port inside the emulator, because config-service hands out absolute
   `localhost:<port>` URLs. It also maps the baked `localhost:7700` to the sandbox's config
   port. Before installing, the script checks the tunnel from inside the emulator with
   toybox `nc`.
5. **Driving the app.** `tools/mobileshot` (Go, unit-tested) uses plain adb: `uiautomator
   dump` to find views by text, content description, testID or hint; `input tap`/`input
   text`/`input swipe` to act; `cmd uimode night` and `settings put system font_scale` to set
   the look; and `screencap` to shoot. The app is force-stopped and relaunched for each look,
   and the session survives in SecureStore. Before each shot it waits until two dumps in a
   row agree and no spinner or "Loading…" is showing (20 s at most). Bottom tabs are found by
   React Navigation's `"<label>, tab, i of n"` description, and Settings and Inbox by the
   `settings-button` and `inbox-button` testIDs. `mobileshot dump --serial S` prints what is on
   screen right now, which helps when adding a screen.

**Why not Maestro:** it would add a second runtime (a JVM CLI plus an on-device driver APK)
and a download, for flows that come down to "tap this label, wait for that one". adb and
uiautomator are already in the SDK. The dumps double as the text output for agents, and the
logic is ordinary Go with tests. If the flows grow to deep forms, Maestro's YAML would be
worth revisiting.

## Isolation guarantees

- **Never the user's emulator:** every adb call carries `-s <harness serial>`. The Go
  helper refuses `emulator-5554` outright, and the script uses only serials whose `adb emu avd
  name` is `jarvis-harness`. The user's emulator is never installed to, reversed, typed into,
  read or killed. The adb *server* is shared, which is how adb works, and the script never
  restarts it.
- **Kill on exit:** the EXIT trap runs `adb -s <serial> emu kill` and then kills the
  emulator's own process group (it was started with `setsid`). `--keep` skips that, records
  the port in `.sandbox/<name>/mobile/emulator.port`, and `stop` kills that emulator later.
- **A clean app every run:** `pm clear` before the flow, so every run logs in from scratch.

## Timings (this desktop, 24 cores, KVM)

| Step | Time |
|---|---|
| APK build, first (npm ci + prebuild + gradle; `~/.gradle` warm) | ~1.5 min |
| APK, cached | instant |
| Emulator cold boot (`-read-only -no-snapshot`) | ~20–30 s |
| Default run (10 screens × light/dark, one chat; 27 PNGs) | ~7.5 min including the boot |
| Same with `--font-scale 1.0,1.3` (4 looks) | ~13 min |
| One screen, two themes (`--screens nodes --no-chat`) | ~1.5 min |

Most of the time goes into `uiautomator dump`, which takes 1–2 s per call and runs a few times per shot while the screen settles. Home also waits the full 20 s for "Loading tools…" because the sandbox's node never answers over MQTT. Use `--screens`, `--pages 1` and `--keep` to iterate faster.

## iOS (out of scope here)

The iOS Simulator needs macOS and Xcode, so it can't run on the Linux desktop. On the Mac the
same design would work like this:

- Build: `npx expo prebuild --platform ios` in a `git archive` copy, then `xcodebuild -scheme
  JarvisAutomation -configuration Release -sdk iphonesimulator -derivedDataPath …`, with the
  same `EXPO_PUBLIC_*` env.
- Device: `xcrun simctl create jarvis-harness "iPhone 17"` in a private device set
  (`--set ~/.cache/jarvis-harness/sim`), so it stays out of the user's simulator list. Then
  `simctl boot`, `simctl install`, `simctl launch`.
- There is no adb reverse to set up: the simulator shares the Mac's network, so
  `localhost:<port>` already reaches the sandbox. The baked config URL would be the
  sandbox's own port, or a pinned URL entered on the Landing screen.
- Look and shots: `xcrun simctl ui <dev> appearance dark|light`, `simctl ui <dev>
  content_size extra-large` for the font, and `simctl io <dev> screenshot`.
- Driving: there is no uiautomator. Use Maestro (which supports the iOS simulator) or
  `idb ui describe-all` / `idb ui tap`. This is where Maestro would earn its keep.
