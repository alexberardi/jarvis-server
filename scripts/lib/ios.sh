# shellcheck shell=bash
# The iOS lane's parts, sourced by scripts/e2e (the ios suite) after scripts/lib/mobile.sh (for
# MOBILE_REPO, PKG and ensure_maestro): the app built for the simulator from a git archive of
# jarvis-node-mobile (expo prebuild + xcodebuild, no signing), cached per mobile commit and
# config URL, and the harness's own simulator (jarvis-harness-e2e), erased before every run.
# Every simctl call names $UDID, a device the harness created and recorded; never `booted`, never
# one of the user's simulators. The caller defines ROOT, say and die.
# docs/decisions/2026-10-18-e2e-ios-lane.md.

IOS_CACHE=${XDG_CACHE_HOME:-$HOME/.cache}/jarvis-harness/mobile/ios
SIM_NAME=jarvis-harness-e2e # tools/e2emobile refuses any other device name
SIM_RUNTIME=${E2E_IOS_RUNTIME:-com.apple.CoreSimulator.SimRuntime.iOS-18-0}
SIM_TYPE=${E2E_IOS_DEVICE_TYPE:-com.apple.CoreSimulator.SimDeviceType.iPhone-16}
IOS_RECIPE=2 # bump when the build recipe changes, to invalidate cached apps
# The loop runner fences the build in a Seatbelt sandbox, and macOS refuses a nested sandbox-exec:
# swift-frontend runs each out-of-process macro plugin (expo-dev-menu's #Preview, PreviewsMacros)
# under one unless told not to. scripts/ios-nested-sandbox-check.sh checks these flags.
IOS_NESTED_SWIFT_FLAGS=-disable-sandbox
UDID="" SIM_LOCKED=0

sha256() { if command -v sha256sum >/dev/null; then sha256sum; else shasum -a 256; fi; }
# node 22 through mise where there is one (as the APK build), else the node on PATH.
nodex() { if command -v mise >/dev/null; then mise exec node@22 -- "$@"; else "$@"; fi; }

ensure_xcode() {
  [ "$(uname -s)" = Darwin ] || die "macOS only (the iOS Simulator comes with Xcode)"
  xcrun --find simctl >/dev/null 2>&1 ||
    die "no simctl: install Xcode and xcode-select it (the command-line tools alone have no simulator)"
  xcrun xcodebuild -version >/dev/null 2>&1 || die "xcodebuild does not run (xcode-select -s /Applications/Xcode.app)"
  command -v pod >/dev/null || die "CocoaPods not found (brew install cocoapods)"
  command -v node >/dev/null || command -v mise >/dev/null || die "node not found (node 22, or mise)"
}

# lock DIR: a mkdir lock (macOS has no flock), taken over from a holder that is gone.
lock() {
  local d=$1 pid
  until mkdir "$d" 2>/dev/null; do
    pid=$(cat "$d/pid" 2>/dev/null || true)
    if [ -n "$pid" ] && ! kill -0 "$pid" 2>/dev/null; then rm -rf "$d"; continue; fi
    sleep 2
  done
  echo $$ >"$d/pid"
}
unlock() { rm -rf "$1"; }

# --- the app: a Release build for the simulator (JS bundled in, no Metro), unsigned -------------
ios_app_path() { # SHA CONFIG_URL: one build per mobile commit and baked-in config URL
  echo "$IOS_CACHE/app/jarvis-mobile-${1:0:12}-$(printf %s "$2" | sha256 | cut -c1-8)-r$IOS_RECIPE.app"
}

build_ios_app() { # SHA CONFIG_URL → prints the .app path
  local sha=$1 config=$2 app src lockhash built cand
  app=$(ios_app_path "$sha" "$config")
  if [ -d "$app" ]; then say "app (cached): $app"; echo "$app"; return; fi
  src=$IOS_CACHE/build
  mkdir -p "$IOS_CACHE/app" "$src"
  lock "$IOS_CACHE/build.lock" # the build dir, its node_modules and build.log are shared
  if [ -d "$app" ]; then
    unlock "$IOS_CACHE/build.lock"
    say "app (cached): $app"; echo "$app"; return
  fi
  say "building the app for the simulator: jarvis-node-mobile ${sha:0:12}, config $config (log: $IOS_CACHE/build.log)"
  local t0=$SECONDS
  # errexit is off in a subshell tested with ||, so every step that can fail says so itself.
  (
    # Fresh sources each time, but keep node_modules while package-lock.json is unchanged.
    lockhash=$(git -C "$MOBILE_REPO" show "$sha:package-lock.json" | sha256 | cut -c1-16)
    if [ "$(cat "$src/.lockhash" 2>/dev/null)" != "$lockhash" ]; then rm -rf "$src/node_modules"; fi
    find "$src" -mindepth 1 -maxdepth 1 ! -name node_modules ! -name .lockhash -exec rm -rf {} +
    git -C "$MOBILE_REPO" archive --format=tar "$sha" | tar -x -C "$src" || exit 1
    cd "$src" || exit 1
    if [ ! -d node_modules ]; then
      nodex npm ci --no-audit --no-fund || exit 1
      echo "$lockhash" >.lockhash
    fi
    export EXPO_PUBLIC_DEV_MODE=true EXPO_PUBLIC_MANUAL_CONFIG_URL=$config
    export NODE_ENV=production EXPO_NO_TELEMETRY=1 CI=1
    export LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8 # CocoaPods fails without a UTF-8 locale
    nodex npx expo prebuild --platform ios --clean --no-install || exit 1
    (cd ios && nodex pod install) || exit 1
    local ws scheme w
    ws=
    for w in ios/*.xcworkspace; do [ -d "$w" ] && { ws=$w; break; }; done
    [ -n "$ws" ] || { echo "expo prebuild made no ios/*.xcworkspace"; exit 1; }
    scheme=$(basename "$ws" .xcworkspace)
    # The simulator's arch only (arm64 on Apple silicon), no signing: nothing here needs the
    # user's team or certificates. No Seatbelt of Xcode's own (macro plugins, script phases): it
    # cannot nest inside the loop runner's.
    nodex xcodebuild -workspace "$ws" -scheme "$scheme" -configuration Release -sdk iphonesimulator \
      -destination 'generic/platform=iOS Simulator' -derivedDataPath "$src/ios/build" \
      ARCHS="$(uname -m)" ONLY_ACTIVE_ARCH=NO CODE_SIGNING_ALLOWED=NO CODE_SIGNING_REQUIRED=NO CODE_SIGN_IDENTITY= \
      OTHER_SWIFT_FLAGS="\$(inherited) $IOS_NESTED_SWIFT_FLAGS" ENABLE_USER_SCRIPT_SANDBOXING=NO \
      -quiet build
  ) >"$IOS_CACHE/build.log" 2>&1 || {
    unlock "$IOS_CACHE/build.lock"
    tail -40 "$IOS_CACHE/build.log" >&2
    die "iOS build failed; see $IOS_CACHE/build.log"
  }
  built=
  for cand in "$src"/ios/build/Build/Products/Release-iphonesimulator/*.app; do [ -d "$cand" ] && { built=$cand; break; }; done
  [ -n "$built" ] || { unlock "$IOS_CACHE/build.lock"; die "xcodebuild left no .app (see $IOS_CACHE/build.log)"; }
  # Whole or not at all: a run checking the cache without the lock never installs half an app.
  rm -rf "$app.tmp.$$"
  cp -R "$built" "$app.tmp.$$" && mv "$app.tmp.$$" "$app"
  unlock "$IOS_CACHE/build.lock"
  say "app built in $((SECONDS - t0))s: $app"
  echo "$app"
}

plist() { /usr/libexec/PlistBuddy -c "Print :$2" "$1/Info.plist"; } # APP KEY

# --- the simulator: the harness's own device, by UDID -------------------------------------------
# sim_name UDID: the name simctl lists for that device (empty when there is none).
sim_name() {
  xcrun simctl list devices -j | python3 -c '
import json, sys
for devs in json.load(sys.stdin)["devices"].values():
    for d in devs:
        if d["udid"] == sys.argv[1]:
            print(d["name"])' "$1"
}

is_our_sim() { [ -n "$1" ] && [ "$1" != booted ] && [ "$(sim_name "$1")" = "$SIM_NAME" ]; }

# ensure_sim sets UDID: the device the harness created before (its UDID is kept in the cache),
# or a new one, created once.
ensure_sim() {
  local f=$IOS_CACHE/simulator.udid
  mkdir -p "$IOS_CACHE"
  UDID=$(cat "$f" 2>/dev/null || true)
  if is_our_sim "$UDID"; then return; fi
  xcrun simctl list runtimes -j | grep -q "\"identifier\" : \"$SIM_RUNTIME\"" ||
    die "no $SIM_RUNTIME simulator runtime (Xcode → Settings → Components; or set E2E_IOS_RUNTIME)"
  say "creating simulator $SIM_NAME ($SIM_TYPE, $SIM_RUNTIME)"
  UDID=$(xcrun simctl create "$SIM_NAME" "$SIM_TYPE" "$SIM_RUNTIME") || die "simctl create failed"
  echo "$UDID" >"$f"
  is_our_sim "$UDID" || die "simctl created $UDID, but it is not listed as $SIM_NAME"
}

# boot_sim: the harness simulator, erased (a fresh device every run), booted headless.
boot_sim() {
  is_our_sim "$UDID" || die "refusing simulator '$UDID': not the harness's $SIM_NAME"
  lock "$IOS_CACHE/simulator.lock" # one run at a time on the device
  SIM_LOCKED=1
  local t0=$SECONDS
  say "erasing and booting $SIM_NAME ($UDID)"
  xcrun simctl shutdown "$UDID" >/dev/null 2>&1 || true
  xcrun simctl erase "$UDID" || die "simctl erase failed"
  xcrun simctl boot "$UDID" || die "simctl boot failed"
  xcrun simctl bootstatus "$UDID" -b >/dev/null || die "the simulator did not finish booting"
  # Deterministic screenshots: the status bar frozen, as demo mode does on the emulator.
  xcrun simctl status_bar "$UDID" override --time 9:41 --dataNetwork wifi --wifiBars 3 \
    --batteryState charged --batteryLevel 100 >/dev/null 2>&1 || true
  say "booted in $((SECONDS - t0))s"
}

shutdown_sim() {
  [ -n "$UDID" ] || return 0
  [ "$SIM_LOCKED" = 1 ] || return 0
  if is_our_sim "$UDID"; then xcrun simctl shutdown "$UDID" >/dev/null 2>&1 || true; fi
  unlock "$IOS_CACHE/simulator.lock"
  SIM_LOCKED=0
}

# install_ios_app APP: the app on the erased simulator, and the seeded node marked online.
install_ios_app() {
  local app=$1 id
  id=$(plist "$app" CFBundleIdentifier)
  [ "$id" = "$PKG" ] || die "$app is $id, not $PKG"
  say "installing $(basename "$app")"
  xcrun simctl install "$UDID" "$app" || die "simctl install failed"
  # The seeded node counts as online (chat needs one) once it has made a request within 15 min.
  curl -sf -o /dev/null "$SBX_CC/api/v0/generate/date-context" -H "X-API-Key: $SBX_NODE_ID:$SBX_NODE_KEY" || true
}
