#!/usr/bin/env bash
# Notarizes one file (a zip of a binary or of apps, or a dmg) for release.yml's sign-macos:
#
#   scripts/macos/notarize.sh <file>
#
# The App Store Connect API key: API_KEY_ID, API_ISSUER_ID, and the .p8 at
# $RUNNER_TEMP/signing/AuthKey.p8 (NOTARY_KEY overrides). It waits for Apple's verdict, always
# prints the notarization log, and fails unless the status is Accepted.
set -euo pipefail
[ $# -eq 1 ] || { echo "usage: $0 <file>" >&2; exit 2; }
key=${NOTARY_KEY:-"$RUNNER_TEMP/signing/AuthKey.p8"}
auth=(--key "$key" --key-id "$API_KEY_ID" --issuer "$API_ISSUER_ID")
out="$(xcrun notarytool submit "$1" "${auth[@]}" --wait --timeout 30m --output-format json)" || true
echo "$out"
field() { printf '%s' "$out" | python3 -c "import json,sys; print(json.load(sys.stdin).get('$1',''))" 2>/dev/null || true; }
id="$(field id)"
status="$(field status)"
if [ -n "$id" ]; then xcrun notarytool log "$id" "${auth[@]}" || true; fi
if [ "$status" != Accepted ]; then
  echo "::error::notarization of $(basename "$1"): status '${status:-none}' (log above)"
  exit 1
fi
