#!/usr/bin/env bash
# Runs inside the L2 lane's FreePBX VM as root (scripts/freepbx-lane copies this directory in):
# extension 100 (Jarvis) with whatever defaults this FreePBX release gives a new PJSIP extension,
# the inbound route DID 5551230000 → 100, a trunk to the fake carrier and the outbound route to
# it, then fwconsole reload. Mirrors the L1 lane (test/sip/asterisk), so the two compare.
set -euo pipefail
cd "$(dirname "$0")"

# Run right after boot: Asterisk may not accept console connections yet (fwconsole reload and the
# checks below need it). Up to 3 min, then the checks report what is missing.
for _ in $(seq 90); do asterisk -rx 'core waitfullybooted' >/dev/null 2>&1 && break; sleep 2; done

# Extension and inbound route: FreePBX's own import, so the extension gets the release's
# defaults exactly as the admin UI would give them. --replace because FreePBX 17.0.33's
# bulkimport fails without it ("Undefined variable $replace", Bulkimport.class.php:47); on a fresh
# provision there is nothing to replace, so it changes nothing else.
fwconsole bulkimport --type=extensions --replace extensions.csv
fwconsole bulkimport --type=dids --replace dids.csv

# Trunk and outbound route: SQL (routes.sql says why and from where).
mysql asterisk <routes.sql

fwconsole reload
asterisk -rx 'core waitfullybooted' >/dev/null
# Output captured first: under pipefail, `asterisk -rx … | grep -q` fails whenever grep exits on
# the first match and asterisk is still writing (SIGPIPE), about every other run.
ep=$(asterisk -rx 'pjsip show endpoint 100')
grep -q 'Endpoint:  100' <<<"$ep" || { echo "provision: extension 100 is not in Asterisk after the reload" >&2; exit 1; }
ep=$(asterisk -rx 'pjsip show endpoint carrier')
grep -q 'Endpoint:  carrier' <<<"$ep" || { echo "provision: the carrier trunk is not in Asterisk after the reload" >&2; exit 1; }
echo "provision: extension 100, DID 5551230000 → 100, trunk carrier and its outbound route are in"
