#!/usr/bin/env bash
# Builds the two Windows installers (01-no-cli-plan item 2.3) with Inno Setup from programs
# already built for windows/amd64:
#
#   scripts/build-windows-installers.sh VERSION BIN_DIR OUT_DIR
#
#   VERSION  the release tag, e.g. v0.1.0-rc1
#   BIN_DIR  holds jarvisd.exe, jarvis-agent.exe and jarvis-agentw.exe (the launcher, built
#            with -ldflags -H=windowsgui; checked here)
#
# It writes OUT_DIR/"Jarvis Automation Server Setup <tag without v>.exe" (per machine) and
# OUT_DIR/"Jarvis Automation Agent Setup <tag without v>.exe" (per user). SERVER_NAME and
# AGENT_NAME override those base names (the release uses names without spaces: GitHub turns
# spaces in asset names into dots). The scripts are scripts/packaging/windows/{server,agent}.iss.
# ISCC names Inno Setup's compiler (default: its standard install path); CI installs Inno Setup
# pinned (ci.yml job inno). Runs in Git Bash on Windows.
set -euo pipefail

[ $# -eq 3 ] || {
  echo "usage: $0 VERSION BIN_DIR OUT_DIR" >&2
  exit 2
}
VERSION=$1 BIN_DIR=$2 OUT_DIR=$3
case "$VERSION" in v[0-9]*) ;; *) echo "VERSION must look like v1.2.3, got '$VERSION'" >&2; exit 2 ;; esac
ISCC=${ISCC:-"/c/Program Files (x86)/Inno Setup 6/ISCC.exe"}
[ -x "$ISCC" ] || { echo "Inno Setup's compiler isn't at $ISCC (set ISCC)" >&2; exit 2; }

# subsystem FILE: the PE optional header's Subsystem field (2 = GUI, 3 = console).
subsystem() {
  local pe
  pe=$(od -An -t u4 -j 60 -N 4 "$1" | tr -d ' ')
  od -An -t u2 -j $((pe + 92)) -N 2 "$1" | tr -d ' '
}
for p in jarvisd.exe jarvis-agent.exe jarvis-agentw.exe; do
  [ -f "$BIN_DIR/$p" ] || { echo "missing $BIN_DIR/$p" >&2; exit 2; }
done
# The launcher exists to start the agent without a console window; the agent stays a console
# program so its CLI works in a terminal.
[ "$(subsystem "$BIN_DIR/jarvis-agentw.exe")" = 2 ] || { echo "jarvis-agentw.exe is not a GUI program: build it with -ldflags -H=windowsgui" >&2; exit 2; }
[ "$(subsystem "$BIN_DIR/jarvis-agent.exe")" = 3 ] || { echo "jarvis-agent.exe must stay a console program" >&2; exit 2; }

TAG=${VERSION#v}
# Windows' file version is four numbers: v0.1.0-rc1 -> 0.1.0.0.
CORE=${TAG%%-*}
NUMERIC=$CORE.0
case "$NUMERIC" in [0-9]*.[0-9]*.[0-9]*.0) ;; *) echo "can't make a file version of $VERSION" >&2; exit 2 ;; esac

mkdir -p "$OUT_DIR"
# Absolute: ISCC resolves relative paths against the .iss file's directory.
BIN_DIR=$(cd "$BIN_DIR" && pwd)
OUT_DIR=$(cd "$OUT_DIR" && pwd)
win() { if command -v cygpath >/dev/null; then cygpath -w "$1"; else echo "$1"; fi; }
here=$(cd "$(dirname "$0")" && pwd)
for p in server agent; do
  name=
  [ "$p" = server ] && name=${SERVER_NAME:-}
  [ "$p" = agent ] && name=${AGENT_NAME:-}
  args=("/DAppVersion=$TAG" "/DNumericVersion=$NUMERIC" "/DBinDir=$(win "$BIN_DIR")" "/DOutputDir=$(win "$OUT_DIR")")
  [ -n "$name" ] && args+=("/DOutputName=$name")
  # MSYS would read /D... as paths.
  MSYS2_ARG_CONV_EXCL='*' "$ISCC" /Q "${args[@]}" "$(win "$here/packaging/windows/$p.iss")"
done
ls -l "$OUT_DIR"
