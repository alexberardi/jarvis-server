#!/usr/bin/env bash
# The real push lane's push.env example (scripts/e2e mobile --real-push): it must name the two
# keys scripts/e2e's load_push_env reads, nothing else, and the docs must point at it.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
fail=0
err() { echo "push-env-example: $*" >&2; fail=1; }

EXAMPLE=$ROOT/push.env.example
[ -f "$EXAMPLE" ] || { err "no $EXAMPLE (the real push lane's push.env example)"; exit 1; }

bash -n "$EXAMPLE" || { err "$EXAMPLE is not valid bash (bash -n)"; exit 1; }

# Sourcing it must set exactly the two keys scripts/e2e's load_push_env reads (scripts/e2e:179).
out=$(bash -c "unset E2E_REAL_RELAY_URL E2E_PUSH_GOOGLE_SERVICES; . '$EXAMPLE'; printf '%s\n' \"\${E2E_REAL_RELAY_URL:-}\" \"\${E2E_PUSH_GOOGLE_SERVICES:-}\"")
relay=$(sed -n 1p <<<"$out")
gs=$(sed -n 2p <<<"$out")
[ -n "$relay" ] || err "sourcing $EXAMPLE does not set E2E_REAL_RELAY_URL"
case $relay in http://* | https://*) ;; *) err "E2E_REAL_RELAY_URL in $EXAMPLE is not an http(s) URL: $relay" ;; esac
[ -n "$gs" ] || err "sourcing $EXAMPLE does not set E2E_PUSH_GOOGLE_SERVICES"

# Every line that is not blank or a comment must be one of the two keys, so a renamed or added
# key (which scripts/e2e would ignore) fails here.
while IFS= read -r line; do
  case $line in
    '' | '#'*) continue ;;
    E2E_REAL_RELAY_URL=* | E2E_PUSH_GOOGLE_SERVICES=*) continue ;;
    *) err "unexpected line in $EXAMPLE: $line" ;;
  esac
done <"$EXAMPLE"

# The docs and the example's header name the default file it is copied to: the PUSH_ENV= default
# in scripts/e2e (with $HOME shown as ~), so following them is what load_push_env sources.
DOCS=$ROOT/docs/dev/e2e.md
default=$(sed -n 's/^PUSH_ENV=\${JARVIS_HARNESS_PUSH_ENV:-\(.*\)}$/\1/p' "$ROOT/scripts/e2e")
[ -n "$default" ] || { err "no PUSH_ENV=\${JARVIS_HARNESS_PUSH_ENV:-...} default in scripts/e2e"; exit 1; }
shown=${default/#\$HOME/\~}
grep -q 'push\.env\.example' "$DOCS" || err "docs/dev/e2e.md does not name push.env.example"
grep -qF "copy [\`push.env.example\`](../../push.env.example) to \`$shown\`" "$DOCS" ||
  err "docs/dev/e2e.md does not say to copy push.env.example to $shown (the default scripts/e2e reads)"
grep -qF "Copy to $shown" "$EXAMPLE" || err "$EXAMPLE does not say to copy it to $shown"
grep -q 'JARVIS_HARNESS_PUSH_ENV' "$DOCS" || err "docs/dev/e2e.md does not name \$JARVIS_HARNESS_PUSH_ENV"
grep -q 'skipped' "$DOCS" || err "docs/dev/e2e.md does not say the lane is skipped until the needs exist"

[ "$fail" = 0 ] || exit 1
echo "push-env-example: ok"
