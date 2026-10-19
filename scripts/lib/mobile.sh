# shellcheck shell=bash
# The mobile harness's shared parts, sourced by scripts/mobile-shot and scripts/e2e: the private
# AVD (jarvis-harness), the cached release APK built from a git archive of jarvis-node-mobile, a
# disposable headless emulator on a port of its own, and the adb reverse tunnels to a sandbox.
# The caller defines ROOT, say and die, and before boot_emulator sets WORK (logs, pid) and
# STATEF (the --keep port file). Every adb call names $SERIAL; the user's own emulator
# (USER_SERIAL) is never touched. docs/dev/mobile-shots.md.

CACHE=${XDG_CACHE_HOME:-$HOME/.cache}/jarvis-harness/mobile
# The main checkout's parent, also from a worktree under .claude/worktrees/.
MAIN_CHECKOUT=$(cd "$ROOT" && cd "$(git rev-parse --git-common-dir)/.." && pwd)
MOBILE_REPO=${JARVIS_MOBILE_REPO:-$(dirname "$MAIN_CHECKOUT")/jarvis-node-mobile}
SDK=${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/Android/Sdk}}
export ANDROID_HOME=$SDK ANDROID_SDK_ROOT=$SDK
export ANDROID_AVD_HOME=$CACHE/avd # never the user's ~/.android/avd
AVD=jarvis-harness
IMAGE_FLAVOR=google_apis # use_play_store: google_apis_playstore, for real FCM delivery
PKG=com.jarvis.nodemobile
# The config URL baked into the APK. The emulator's localhost:7700 is adb-reversed to the
# sandbox's config port, so one APK serves every sandbox.
BAKED_CONFIG=http://localhost:7700
RECIPE=1 # bump when the build recipe changes, to invalidate cached APKs
USER_SERIAL=emulator-5554 # the user's dev emulator: never touched

ADB=$SDK/platform-tools/adb
EMULATOR=$SDK/emulator/emulator
AVDMANAGER=$SDK/cmdline-tools/latest/bin/avdmanager

# --- the AVD: the user's jarvis-pixel8 system image, a pixel_8 profile ------------------------
system_image() {
  local cfg=$HOME/.android/avd/jarvis-pixel8.avd/config.ini dir=
  if [ "$IMAGE_FLAVOR" = google_apis_playstore ]; then
    dir=$(play_image) || die "no Google Play x86_64 system image under $SDK/system-images (sdkmanager 'system-images;android-36;google_apis_playstore;x86_64')"
    echo "${dir//\//;}"
    return
  fi
  dir=$(sed -n 's/^image.sysdir.1=//p' "$cfg" 2>/dev/null || true)
  [ -n "$dir" ] || dir=$(cd "$SDK" && ls -d system-images/android-*/google_apis/x86_64/ 2>/dev/null | sort -V | tail -1)
  [ -n "$dir" ] && [ -d "$SDK/$dir" ] || die "no x86_64 system image under $SDK/system-images (sdkmanager 'system-images;android-36;google_apis;x86_64')"
  dir=${dir%/}
  echo "${dir//\//;}"
}

# play_image prints the newest Google Play x86_64 system image (relative to $SDK), if any.
play_image() {
  local dir
  dir=$(cd "$SDK" 2>/dev/null && ls -d system-images/android-*/google_apis_playstore/x86_64/ 2>/dev/null | sort -V | tail -1)
  [ -n "$dir" ] && [ -d "$SDK/$dir" ] && echo "${dir%/}"
}

# use_play_store switches to the real push lane's own AVD (jarvis-harness-play), on a Google
# Play image: FCM needs Play services, which google_apis images lack. The default lane's AVD
# is untouched.
use_play_store() { AVD=jarvis-harness-play IMAGE_FLAVOR=google_apis_playstore; }

ensure_avd() {
  [ -x "$EMULATOR" ] || die "no emulator at $EMULATOR (sdkmanager emulator)"
  [ -x "$AVDMANAGER" ] || die "no avdmanager at $AVDMANAGER (sdkmanager 'cmdline-tools;latest')"
  [ -r /dev/kvm ] && [ -w /dev/kvm ] || die "/dev/kvm is not usable (add yourself to the kvm group)"
  if [ -f "$ANDROID_AVD_HOME/$AVD.ini" ]; then return; fi
  local img
  img=$(system_image)
  mkdir -p "$ANDROID_AVD_HOME"
  say "creating AVD $AVD ($img) in $ANDROID_AVD_HOME"
  echo no | "$AVDMANAGER" -s create avd -n "$AVD" -k "$img" -d pixel_8 -p "$ANDROID_AVD_HOME/$AVD.avd" >/dev/null
  # Headless and lean: no camera/audio, 3 GB of RAM (the host also runs gradle and jarvisd), a
  # hardware keyboard so the soft keyboard never covers the screen.
  local cfg=$ANDROID_AVD_HOME/$AVD.avd/config.ini
  sed -i -E '/^(hw\.ramSize|hw\.camera\.(back|front)|hw\.audio(Input|Output)|hw\.keyboard|showDeviceFrame)=/d' "$cfg"
  cat >>"$cfg" <<CFG
hw.ramSize=3072
hw.camera.back=none
hw.camera.front=none
hw.audioInput=no
hw.audioOutput=no
hw.keyboard=yes
showDeviceFrame=no
CFG
  [ -f "$ANDROID_AVD_HOME/$AVD.ini" ] || die "avdmanager did not create $ANDROID_AVD_HOME/$AVD.ini"
}

# --- the APK: a release build (JS bundled in, no Metro) from a git archive of the mobile repo --
apk_path() { echo "$CACHE/apk/jarvis-mobile-$1-r$RECIPE.apk"; }

# build_apk REF REBUILD FETCH [PUSH_CONFIG] → prints the APK path. PUSH_CONFIG is a
# google-services.json (the real push lane): it goes into the build as ./google-services.json
# and $GOOGLE_SERVICES_JSON, for the app config's android.googleServicesFile, and the APK is
# cached apart (by the file's hash) from the default one.
build_apk() {
  local ref=$1 rebuild=$2 fetch=$3 push=${4:-} sha apk src lockhash lockfd
  # Any git repo: a checkout, a worktree (.git is a file) or a bare repo (no .git at all).
  git -C "$MOBILE_REPO" rev-parse --git-dir >/dev/null 2>&1 ||
    die "no jarvis-node-mobile git repo at $MOBILE_REPO (set JARVIS_MOBILE_REPO)"
  # fetch updates remote-tracking refs only; the user's branch and working tree stay as they are.
  if [ "$fetch" = 1 ] && [ "$ref" = origin/main ]; then git -C "$MOBILE_REPO" fetch -q origin main 2>/dev/null || true; fi
  sha=$(git -C "$MOBILE_REPO" rev-parse --verify -q "$ref^{commit}") || die "--ref $ref: no such commit in $MOBILE_REPO"
  sha=${sha:0:12}
  apk=$(apk_path "$sha")
  if [ -n "$push" ]; then
    [ -r "$push" ] || die "push config $push is not readable"
    apk=$(apk_path "$sha-push-$(sha256sum <"$push" | cut -c1-8)")
  fi
  if [ "$rebuild" = 0 ] && [ -f "$apk" ]; then say "APK (cached): $apk"; echo "$apk"; return; fi

  command -v mise >/dev/null || die "mise not found (it provides node 22 and java 17)"
  command -v flock >/dev/null || die "flock not found (util-linux)"
  src=$CACHE/build
  mkdir -p "$CACHE/apk" "$src"
  # One build at a time: the build dir, its node_modules and build.log are shared by every run.
  exec {lockfd}>"$CACHE/build.lock"
  if ! flock -n "$lockfd"; then
    say "another mobile-shot is building in $src; waiting for it"
    flock "$lockfd"
  fi
  # It may have just built this very commit.
  if [ "$rebuild" = 0 ] && [ -f "$apk" ]; then
    exec {lockfd}>&-
    say "APK (cached): $apk"; echo "$apk"; return
  fi
  say "building the APK for jarvis-node-mobile $sha ($ref) in $src (log: $CACHE/build.log)"
  local t0=$SECONDS
  (
    # Fresh sources each time, but keep node_modules while package-lock.json is unchanged.
    lockhash=$(git -C "$MOBILE_REPO" show "$sha:package-lock.json" | sha256sum | cut -c1-16)
    if [ "$(cat "$src/.lockhash" 2>/dev/null)" != "$lockhash" ]; then rm -rf "$src/node_modules"; fi
    find "$src" -mindepth 1 -maxdepth 1 ! -name node_modules ! -name .lockhash -exec rm -rf {} +
    git -C "$MOBILE_REPO" archive --format=tar "$sha" | tar -x -C "$src"
    cd "$src"
    if [ ! -d node_modules ]; then
      mise exec node@22 -- npm ci --no-audit --no-fund
      echo "$lockhash" >.lockhash
    fi
    if [ -n "$push" ]; then
      cp "$push" google-services.json
      export GOOGLE_SERVICES_JSON=$src/google-services.json
    fi
    export EXPO_PUBLIC_DEV_MODE=true EXPO_PUBLIC_MANUAL_CONFIG_URL=$BAKED_CONFIG
    export NODE_ENV=production EXPO_NO_TELEMETRY=1 CI=1
    mise exec node@22 -- npx expo prebuild --platform android --clean --no-install
    cd android
    # x86_64 only (the emulator's ABI), no daemon left behind; release = debug-signed in
    # Expo's template, JS bundle embedded.
    mise exec node@22 java@temurin-17 -- ./gradlew --no-daemon -q assembleRelease \
      -PreactNativeArchitectures=x86_64 -Dorg.gradle.jvmargs=-Xmx3g
  ) >"$CACHE/build.log" 2>&1 || { tail -40 "$CACHE/build.log" >&2; die "APK build failed; see $CACHE/build.log"; }
  # Expo's prebuild copies the file into the native project only when the app config names it.
  [ -z "$push" ] || [ -f "$src/android/app/google-services.json" ] ||
    die "the APK has no push config: jarvis-node-mobile's app config sets no android.googleServicesFile (./google-services.json or \$GOOGLE_SERVICES_JSON)"
  # Atomic, so a run checking the cache without the lock never installs half a file.
  cp "$src/android/app/build/outputs/apk/release/app-release.apk" "$apk.tmp.$$"
  mv -f "$apk.tmp.$$" "$apk"
  exec {lockfd}>&-
  say "APK built in $((SECONDS - t0))s: $apk"
  echo "$apk"
}

# --- the emulator -------------------------------------------------------------------------------
STATEF= SERIAL= PORT= STARTED=0 KEEP=0

port_free() { ! ss -Hltn "sport = :$1" 2>/dev/null | grep -q . && ! ss -Hltn "sport = :$(($1 + 1))" 2>/dev/null | grep -q .; }

is_ours() { # SERIAL: a harness emulator (by its AVD name), never the user's
  [ "$1" != "$USER_SERIAL" ] || return 1
  [ "$("$ADB" -s "$1" emu avd name 2>/dev/null | head -1 | tr -d '\r')" = "$AVD" ]
}

boot_emulator() {
  if [ -f "$STATEF" ]; then
    PORT=$(cat "$STATEF") SERIAL=emulator-$PORT
    if is_ours "$SERIAL" && [ "$("$ADB" -s "$SERIAL" shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" = 1 ]; then
      say "reusing emulator $SERIAL (--keep)"
      return
    fi
    rm -f "$STATEF"
  fi
  PORT=
  for p in $(seq 5580 2 5680); do port_free "$p" && { PORT=$p; break; }; done
  [ -n "$PORT" ] || die "no free emulator port in 5580-5680"
  SERIAL=emulator-$PORT
  [ "$SERIAL" != "$USER_SERIAL" ] || die "refusing to use $USER_SERIAL"
  say "booting $AVD on $SERIAL (log: $WORK/emulator.log)"
  local t0=$SECONDS
  # -read-only: several harness runs can share the AVD, and nothing is written back to it.
  setsid "$EMULATOR" -avd "$AVD" -port "$PORT" -read-only -no-window -no-audio -no-boot-anim \
    -no-snapshot -gpu swiftshader_indirect -no-metrics -camera-back none -camera-front none \
    >"$WORK/emulator.log" 2>&1 </dev/null &
  echo $! >"$WORK/emulator.pid"
  STARTED=1
  [ "$KEEP" = 1 ] && echo "$PORT" >"$STATEF"
  local up=0
  for _ in $(seq 240); do
    kill -0 "$(cat "$WORK/emulator.pid")" 2>/dev/null || { tail -20 "$WORK/emulator.log" >&2; die "the emulator exited"; }
    if [ "$("$ADB" -s "$SERIAL" shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" = 1 ]; then up=1; break; fi
    sleep 1
  done
  [ "$up" = 1 ] || die "the emulator did not boot in 240s (see $WORK/emulator.log)"
  is_ours "$SERIAL" || die "$SERIAL is not the harness AVD"
  say "booted in $((SECONDS - t0))s"
}

prep_emulator() {
  # Deterministic screenshots: no animations, no keyboard popping over the screen (a hardware
  # keyboard is configured), status bar time frozen by demo mode.
  local s
  for s in window_animation_scale transition_animation_scale animator_duration_scale; do
    "$ADB" -s "$SERIAL" shell settings put global "$s" 0
  done
  "$ADB" -s "$SERIAL" shell settings put secure show_ime_with_hard_keyboard 0
  "$ADB" -s "$SERIAL" shell settings put global sysui_demo_allowed 1
  "$ADB" -s "$SERIAL" shell am broadcast -a com.android.systemui.demo -e command enter >/dev/null
  "$ADB" -s "$SERIAL" shell am broadcast -a com.android.systemui.demo -e command clock -e hhmm 0941 >/dev/null
  "$ADB" -s "$SERIAL" shell am broadcast -a com.android.systemui.demo -e command notifications -e visible false >/dev/null
}

kill_emulator() {
  [ -n "$SERIAL" ] || return 0
  if [ "$SERIAL" = "$USER_SERIAL" ]; then return 0; fi
  if [ "$KEEP" = 1 ]; then say "leaving $SERIAL running (--keep; scripts/mobile-shot stop --sandbox $NAME)"; return 0; fi
  is_ours "$SERIAL" && "$ADB" -s "$SERIAL" emu kill >/dev/null 2>&1 || true
  if [ -f "$WORK/emulator.pid" ]; then
    local pid
    pid=$(cat "$WORK/emulator.pid")
    for _ in $(seq 20); do kill -0 "$pid" 2>/dev/null || break; sleep 0.5; done
    kill -TERM -- "-$pid" 2>/dev/null || true
    rm -f "$WORK/emulator.pid"
  fi
  rm -f "$STATEF"
}

# --- the app on the emulator, logged in to a sandbox -------------------------------------------
# reverse_sandbox tunnels the sourced sandbox.env's ports into the emulator.
reverse_sandbox() {
  local v p
  # Every sandbox listener at the same port inside the emulator (config-service hands out absolute
  # 127.0.0.1:<port> URLs), plus the baked localhost:7700 → the sandbox's config port.
  "$ADB" -s "$SERIAL" reverse --remove-all
  for v in SBX_CONFIG SBX_AUTH SBX_LOGS SBX_CC SBX_LLM SBX_WHISPER SBX_TTS SBX_NOTIFICATIONS SBX_RECIPES SBX_OCR SBX_ADMIN; do
    p=${!v##*:}
    "$ADB" -s "$SERIAL" reverse "tcp:$p" "tcp:$p" >/dev/null
  done
  for p in 1884 9883; do "$ADB" -s "$SERIAL" reverse "tcp:$((p + JARVIS_PORT_OFFSET))" "tcp:$((p + JARVIS_PORT_OFFSET))" >/dev/null; done
  "$ADB" -s "$SERIAL" reverse "tcp:${BAKED_CONFIG##*:}" "tcp:${SBX_CONFIG##*:}" >/dev/null
  # Prove the tunnel from inside the emulator before blaming the app.
  # (toybox nc drops the connection at stdin's EOF, hence the sleep.)
  "$ADB" -s "$SERIAL" shell "(printf 'GET /health HTTP/1.0\r\n\r\n'; sleep 2) | nc 127.0.0.1 ${BAKED_CONFIG##*:}" 2>/dev/null | grep -q ' 200 ' ||
    die "the emulator can't reach the sandbox through adb reverse (localhost:${BAKED_CONFIG##*:} → $SBX_CONFIG)"
  say "adb reverse: localhost:${BAKED_CONFIG##*:} → $SBX_CONFIG, and every sandbox port 1:1"
}

# install_app APK: a fresh, logged-out app, and the seeded node marked online.
install_app() {
  local apk=$1
  say "installing $(basename "$apk")"
  "$ADB" -s "$SERIAL" install -r -g "$apk" >/dev/null || die "adb install failed"
  "$ADB" -s "$SERIAL" shell pm clear "$PKG" >/dev/null # a fresh, logged-out app each run

  # The seeded node counts as online (chat needs one) once it has made a request within 15 min.
  curl -sf -o /dev/null "$SBX_CC/api/v0/generate/date-context" -H "X-API-Key: $SBX_NODE_ID:$SBX_NODE_KEY" || true
}

# --- Maestro: the mobile E2E suite's driver (scripts/e2e mobile), pinned and checksummed ----------
MAESTRO_VERSION=2.11.0
MAESTRO_SHA256=5384593cb4e7a106489e75a821d157dd43f4e438df6bc308b72e82c685e1283a # maestro.zip
MAESTRO_HOME=${XDG_CACHE_HOME:-$HOME/.cache}/jarvis-harness/maestro/$MAESTRO_VERSION

ensure_maestro() { # → prints the maestro binary, downloading the release once
  local bin=$MAESTRO_HOME/maestro/bin/maestro tmp
  command -v java >/dev/null || die "java not found (Maestro needs Java 17+)"
  if [ -x "$bin" ]; then echo "$bin"; return; fi
  command -v unzip >/dev/null || die "unzip not found"
  mkdir -p "$(dirname "$MAESTRO_HOME")"
  tmp=$(mktemp -d "$(dirname "$MAESTRO_HOME")/.dl.XXXXXX")
  say "installing Maestro $MAESTRO_VERSION into $MAESTRO_HOME"
  if ! curl -fsSL -o "$tmp/maestro.zip" \
    "https://github.com/mobile-dev-inc/maestro/releases/download/cli-$MAESTRO_VERSION/maestro.zip"; then
    rm -rf "$tmp"
    die "could not download Maestro $MAESTRO_VERSION"
  fi
  if ! echo "$MAESTRO_SHA256  $tmp/maestro.zip" | sha256sum -c --quiet - >/dev/null 2>&1; then
    rm -rf "$tmp"
    die "Maestro $MAESTRO_VERSION: maestro.zip does not match its pinned sha256"
  fi
  unzip -q "$tmp/maestro.zip" -d "$tmp/x" || { rm -rf "$tmp"; die "could not unpack maestro.zip"; }
  [ -x "$tmp/x/maestro/bin/maestro" ] || { rm -rf "$tmp"; die "maestro.zip has no maestro/bin/maestro"; }
  # Atomic: a concurrent run either finds the whole install or none (and then installs its own).
  mv -T "$tmp/x" "$MAESTRO_HOME" 2>/dev/null || [ -x "$bin" ] || { rm -rf "$tmp"; die "could not install Maestro into $MAESTRO_HOME"; }
  rm -rf "$tmp"
  echo "$bin"
}
