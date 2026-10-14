#!/usr/bin/env bash
# Claude Code PostToolUse hook (.claude/settings.json) for Edit/Write: when the edited file is
# Go, gofmt it in place and vet its package (contract tag included). Problems go to stderr with
# exit 2, which hands them back to the model; nothing is printed when all is well.
set -uo pipefail

f=$(jq -r '.tool_input.file_path // empty')
case $f in *.go) ;; *) exit 0 ;; esac
[ -f "$f" ] || exit 0

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
case $f in "$ROOT"/*) ;; *) exit 0 ;; esac

fail=0
if ! out=$(gofmt -w "$f" 2>&1); then
  echo "gofmt $f:" >&2
  echo "$out" >&2
  fail=1
fi

pkg=./$(realpath --relative-to="$ROOT" "$(dirname "$f")")
if ! out=$(cd "$ROOT" && CGO_ENABLED=0 go vet -tags contract "$pkg" 2>&1); then
  echo "go vet $pkg:" >&2
  echo "$out" | head -30 >&2
  fail=1
fi
[ "$fail" = 0 ] || exit 2
