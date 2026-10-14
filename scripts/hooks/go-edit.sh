#!/usr/bin/env bash
# Claude Code PostToolUse hook (.claude/settings.json) for Edit/Write: when the edited file is
# Go, gofmt it in place and vet its package (contract tag included). Problems go to stderr with
# exit 2, which hands them back to the model; nothing is printed when all is well.
set -uo pipefail

f=$(jq -r '.tool_input.file_path // empty')
case $f in *.go) ;; *) exit 0 ;; esac
[ -f "$f" ] || exit 0

# The checkout the edited file belongs to, not the one this hook lives in: agents edit inside
# worktrees under .claude/worktrees/, each its own module root. Files outside a jarvis-server
# checkout are left alone.
ROOT=$(git -C "$(dirname "$f")" rev-parse --show-toplevel 2>/dev/null) || exit 0
grep -q '^module github.com/alexberardi/jarvis-server$' "$ROOT/go.mod" 2>/dev/null || exit 0

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
