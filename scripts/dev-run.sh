#!/usr/bin/env bash
# Run this checkout's jarvisd in place of the installed one, on the installed one's data.
#
#   scripts/dev-run.sh [--no-ui] [-- extra `jarvisd serve` arguments]
#
# Builds the admin UI and the binary, stops the installed service (system or --user), runs the
# build in the foreground on that service's data directory as that service's account, and starts
# the service again when you press Ctrl-C. Nodes and phones keep working because the accounts,
# households and node keys are the installed ones. Without an installed service it runs on
# ~/.jarvisd.
#
#   --no-ui   skip `npm run build` for the admin UI (reuse web/admin/dist as it is)
#
# The build reports itself as dev-<commit>, so it never takes part in update checks. If it
# brings database migrations the installed release doesn't have, the script says so and asks
# first: the installed release then refuses to start on that data (until the next release, or
# `serve --allow-downgrade`).
#
# Linux only (systemd).
set -euo pipefail

UI=1
while [ $# -gt 0 ]; do
  case $1 in
    --no-ui) UI=0; shift ;;
    --) shift; break ;;
    -h|--help) sed -n '2,21p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "dev-run: unknown argument $1 (serve arguments go after --)" >&2; exit 2 ;;
  esac
done

say() { echo "dev-run: $*" >&2; }
die() { say "$*"; exit 1; }

[ "$(uname -s)" = Linux ] || die "Linux only"
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

# Which install is there: the system unit (runs as jarvisd), the --user unit, or none.
MODE=none HOMEDIR=$HOME/.jarvisd
if [ -f /etc/systemd/system/jarvisd.service ]; then
  MODE=system
  HOMEDIR=$(sed -n 's/^ExecStart=.* serve .*--home \([^ ]*\).*/\1/p' /etc/systemd/system/jarvisd.service)
  HOMEDIR=${HOMEDIR:-/var/lib/jarvisd}
elif systemctl --user cat jarvisd.service >/dev/null 2>&1; then
  MODE=user
fi

# as runs a command as the account the installed service uses.
as() {
  if [ "$MODE" = system ]; then sudo -u jarvisd -H "$@"; else "$@"; fi
}

if [ "$UI" = 1 ]; then
  if [ ! -d web/admin/node_modules ] || [ web/admin/package-lock.json -nt web/admin/node_modules ]; then
    say "installing admin UI dependencies"
    npm --prefix web/admin ci --no-audit --no-fund >/dev/null
  fi
  say "building the admin UI"
  npm --prefix web/admin run build >/dev/null
fi

ls internal/voice/sherpa/libs/linux-amd64/* >/dev/null 2>&1 || die "native libs missing: run scripts/fetch-sherpa-libs.sh"

# The account running it must be able to read the binary, so it lives outside $HOME.
BIN=/tmp/jarvisd-dev-$(id -un)
VERSION=dev-$(git rev-parse --short HEAD)$(git diff --quiet HEAD -- . 2>/dev/null || echo -dirty)
say "building $VERSION"
GOTOOLCHAIN=local CGO_ENABLED=0 mise exec go@1.27 -- \
  go build -trimpath -ldflags "-X main.version=$VERSION" -o "$BIN.new" ./cmd/jarvisd
chmod 0755 "$BIN.new"
mv -f "$BIN.new" "$BIN"

# Stop the installed service; start it again however this script ends.
WAS_ACTIVE=0
case $MODE in
  system) systemctl is-active --quiet jarvisd && WAS_ACTIVE=1 ;;
  user) systemctl --user is-active --quiet jarvisd && WAS_ACTIVE=1 ;;
esac
restore() {
  [ "$WAS_ACTIVE" = 1 ] || return 0
  say "starting the installed jarvisd again"
  case $MODE in
    system) sudo systemctl start jarvisd ;;
    user) systemctl --user start jarvisd ;;
  esac
}
if [ "$WAS_ACTIVE" = 1 ]; then
  say "stopping the installed jarvisd ($MODE service)"
  case $MODE in
    system) sudo systemctl stop jarvisd ;;
    user) systemctl --user stop jarvisd ;;
  esac
  trap restore EXIT
fi

# Migrations the installed release doesn't have make the move one-way: ask first.
PENDING=$(as "$BIN" migrate status --home "$HOMEDIR" 2>/dev/null | awk 'NR > 1 && $4 > 0 {printf "%s%s(+%s)", sep, $1, $4; sep=", "}')
if [ -n "$PENDING" ]; then
  say "this build adds database migrations: $PENDING"
  say "after it runs, the installed release won't start on $HOMEDIR until the next release (or serve --allow-downgrade)"
  read -r -p "dev-run: continue? [y/N] " ok
  [ "$ok" = y ] || [ "$ok" = Y ] || exit 1
fi

say "running $VERSION on $HOMEDIR ($MODE); Ctrl-C to stop"
set +e
as "$BIN" serve --home "$HOMEDIR" --no-browser "$@"
set -e
