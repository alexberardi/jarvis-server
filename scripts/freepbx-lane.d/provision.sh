#!/usr/bin/env bash
# Runs inside the L2 lane's FreePBX VM as root (scripts/freepbx-lane copies this directory in):
# extension 100 (Jarvis) with whatever defaults this FreePBX release gives a new PJSIP extension,
# the inbound route DID 5551230000 → 100, a trunk to the fake carrier and the outbound route to
# it, then fwconsole reload. Mirrors the L1 lane (test/sip/asterisk), so the two compare.
set -euo pipefail
cd "$(dirname "$0")"

# Extension and inbound route: FreePBX's own import, so the extension gets the release's
# defaults exactly as the admin UI would give them.
fwconsole bulkimport --type=extensions extensions.csv
fwconsole bulkimport --type=dids dids.csv

# Trunk and outbound route: SQL (routes.sql says why and from where).
mysql asterisk <routes.sql

fwconsole reload
asterisk -rx 'core waitfullybooted' >/dev/null
asterisk -rx 'pjsip show endpoint 100' | grep -q 'Endpoint:  100' || { echo "provision: extension 100 is not in Asterisk after the reload" >&2; exit 1; }
asterisk -rx 'pjsip show endpoint carrier' | grep -q 'Endpoint:  carrier' || { echo "provision: the carrier trunk is not in Asterisk after the reload" >&2; exit 1; }
echo "provision: extension 100, DID 5551230000 → 100, trunk carrier and its outbound route are in"
