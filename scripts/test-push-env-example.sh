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

# The docs name the example and the default file it is copied to (the harness config dir's
# push.env, the path scripts/e2e:179 reads), and the lane's skip behaviour.
DOCS=$ROOT/docs/dev/e2e.md
grep -q 'push\.env\.example' "$DOCS" || err "docs/dev/e2e.md does not name push.env.example"
grep -q 'push\.env' "$DOCS" || err "docs/dev/e2e.md does not name the push.env file"
grep -q 'skipped' "$DOCS" || err "docs/dev/e2e.md does not say the lane is skipped until the needs exist"

# The default push.env (copied next to the example) must stay out of the repo.
git -C "$ROOT" check-ignore -q "$ROOT/.push.env" || err ".push.env (the default copy of push.env.example) is not gitignored"

[ "$fail" = 0 ] || exit 1
echo "push-env-example: ok"
