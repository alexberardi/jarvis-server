#!/usr/bin/env bash
# Run the black-box contract suite (contract/) against a live stack.
#
# Reads the target and credentials from .contract.env at the repo root (untracked; copy
# .contract.env.example), unless the variables are already set in the environment.
# Extra arguments go to `go test`, e.g.:
#   scripts/contract.sh -run 'TestAuth|TestConfig'
#   scripts/contract.sh -count=1 -run TestHealth/llm
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ENV_FILE="${JARVIS_CONTRACT_ENV_FILE:-$ROOT/.contract.env}"

if [ -f "$ENV_FILE" ]; then
  set -a
  # shellcheck disable=SC1090
  . "$ENV_FILE"
  set +a
elif [ -z "${JARVIS_CONTRACT_HOST:-}" ]; then
  echo "contract: no $ENV_FILE and JARVIS_CONTRACT_HOST unset; every test would skip." >&2
  echo "contract: cp .contract.env.example .contract.env and fill it in (docs/contract/README.md)." >&2
  exit 2
fi

cd "$ROOT"
# -count=1: results depend on a live stack, never on the test cache.
GOTOOLCHAIN=local CGO_ENABLED=0 exec mise exec go@1.27 -- go test -tags contract -count=1 ./contract/... -v "$@"
