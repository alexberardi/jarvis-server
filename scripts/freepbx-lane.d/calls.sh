#!/usr/bin/env bash
# Runs inside the L2 lane's FreePBX VM as root, after provision.sh and after weekly has captured
# extension 100 (none of this touches the extension): fits the PBX to qemu's user network so the
# host can call through it. scripts/freepbx-lane calls it; `go run ./tools/siplane l2` then places
# the calls.
#
#   calls.sh SIP_PORT RTP_START RTP_END CARRIER_PORT
#
# The host reaches the VM's 5060 on 127.0.0.1:SIP_PORT and each RTP port at the same number;
# everything from the host arrives from 10.0.2.2 (qemu's alias for the host's loopback). So:
# - Asterisk's RTP range is the forwarded one (SIP Settings → RTP Port Ranges);
# - its SDP and Contact say 127.0.0.1 (SIP Settings → External Address, with only the VM itself
#   as a local network, so 10.0.2.2 counts as outside and gets the external address), and its
#   Contact carries SIP_PORT, which SIP Settings cannot set (pjsip.transports_custom_post.conf,
#   FreePBX's place for a transport setting it has no field for; the transport is allow_reload=no,
#   hence the restart);
# - endpoints are identified by username before IP (SIP Settings → Endpoint Identifier Order;
#   FreePBX 17's default is ip,username,anonymous): the carrier trunk's identify matches
#   10.0.2.2, which here is also where jarvisd's REGISTER comes from, so by IP first FreePBX
#   takes extension 100's REGISTER for the trunk's and refuses its password. On a real network
#   the PBX's trunk and the Jarvis host have different addresses;
# - the carrier trunk points at the L1 lane's carrier, whose port follows its sandbox's offset:
#   its SIP Server is 127.0.0.1 with 10.0.2.2 as its Outbound Proxy, so the INVITE leaves the VM
#   for the host but its request URI names 127.0.0.1, where the carrier (baresip 1.1.0, an
#   account that does not register) takes calls; it answers 404 to a request URI of 10.0.2.2.
set -euo pipefail
trap 'echo "calls.sh: line $LINENO failed: $BASH_COMMAND" >&2' ERR
[ $# = 4 ] || { echo "usage: calls.sh SIP_PORT RTP_START RTP_END CARRIER_PORT" >&2; exit 2; }
SIP_PORT=$1 RTP_START=$2 RTP_END=$3 CARRIER_PORT=$4
for n in "$@"; do [[ "$n" =~ ^[0-9]+$ ]] || { echo "calls.sh: $n is not a port" >&2; exit 2; }; done

for _ in $(seq 90); do asterisk -rx 'core waitfullybooted' >/dev/null 2>&1 && break; sleep 2; done

# SIP Settings through FreePBX's own module, as the admin UI saves them.
# php's own exit status is not the result: FreePBX's bootstrap ends php -r with 151 even when every
# setting is saved (FreePBX 17.0.33). The checks after the restart below say whether they took.
php -r "
include '/etc/freepbx.conf';
\$s = FreePBX::Sipsettings();
\$s->setConfig('rtpstart', '$RTP_START');
\$s->setConfig('rtpend', '$RTP_END');
\$s->setConfig('externip', '127.0.0.1');
\$s->setConfig('localnets', [['net' => '10.0.2.15', 'mask' => '32']]);
\$s->setConfig('pjsip_identifers_order', ['username', 'ip', 'anonymous', 'header', 'auth_username']);
" || true
cat >/etc/asterisk/pjsip.transports_custom_post.conf <<EOF
; scripts/freepbx-lane.d/calls.sh: the host reaches this transport on 127.0.0.1:$SIP_PORT
[0.0.0.0-udp](+)
external_signaling_port=$SIP_PORT
EOF
chown asterisk:asterisk /etc/asterisk/pjsip.transports_custom_post.conf
# (The proxy's ;lr is stored as \;lr: FreePBX writes the value into pjsip.endpoint.conf as it is,
# where a bare ; starts a comment.)
trunk="(SELECT trunkid FROM trunks WHERE name='carrier')"
mysql asterisk -e "UPDATE pjsip SET data='127.0.0.1' WHERE keyword='sip_server' AND id=$trunk;
  UPDATE pjsip SET data='$CARRIER_PORT' WHERE keyword='sip_server_port' AND id=$trunk;
  DELETE FROM pjsip WHERE keyword='outbound_proxy' AND id=$trunk;
  INSERT INTO pjsip (id, keyword, data, flags) SELECT trunkid, 'outbound_proxy', 'sip:10.0.2.2:$CARRIER_PORT\\\\;lr', 0 FROM trunks WHERE name='carrier'"

# fwconsole reload sometimes gives up on a pm2 status call that takes over 60 s (a busy host);
# a second try goes through.
for try in 1 2 3; do
  if out=$(fwconsole reload 2>&1); then break; fi
  [ "$try" = 3 ] && { echo "$out" >&2; exit 1; }
  echo "calls.sh: fwconsole reload failed (try $try), again" >&2
done
asterisk -rx 'core restart now' >/dev/null
sleep 3
for _ in $(seq 90); do asterisk -rx 'core waitfullybooted' >/dev/null 2>&1 && break; sleep 2; done
asterisk -rx 'core waitfullybooted' >/dev/null

# Check that Asterisk took it all (captured first: see provision.sh on pipefail and grep -q).
tr=$(asterisk -rx 'pjsip show transport 0.0.0.0-udp')
grep -q "external_signaling_port *: $SIP_PORT\$" <<<"$tr" || { echo "calls.sh: the transport has no external port $SIP_PORT" >&2; exit 1; }
grep -q 'external_media_address *: 127.0.0.1$' <<<"$tr" || { echo "calls.sh: the transport has no external media address" >&2; exit 1; }
set=$(asterisk -rx 'pjsip show settings')
grep -q 'endpoint_identifier_order *: username,ip' <<<"$set" || { echo "calls.sh: endpoints are not identified by username first" >&2; exit 1; }
rtp=$(asterisk -rx 'rtp show settings')
grep -q "Port start: *$RTP_START\$" <<<"$rtp" || { echo "calls.sh: RTP does not start at $RTP_START" >&2; exit 1; }
grep -q "Port end: *$RTP_END\$" <<<"$rtp" || { echo "calls.sh: RTP does not end at $RTP_END" >&2; exit 1; }
trunk=$(asterisk -rx 'pjsip show aor carrier')
{ grep -q "contact *: sip:carrier@127.0.0.1:$CARRIER_PORT\$" <<<"$trunk" &&
  grep -q "outbound_proxy *: sip:10.0.2.2:$CARRIER_PORT;lr\$" <<<"$trunk"; } ||
  { echo "calls.sh: the carrier trunk is not 127.0.0.1:$CARRIER_PORT through 10.0.2.2:" >&2; echo "$trunk" >&2; exit 1; }

# The lockout scenario starts from no bans, with fail2ban up (it restarts a few times as the VM
# boots).
for _ in $(seq 60); do fail2ban-client ping >/dev/null 2>&1 && break; sleep 2; done
fail2ban-client unban --all >/dev/null
echo "calls.sh: SIP on 127.0.0.1:$SIP_PORT, RTP $RTP_START-$RTP_END, carrier at 10.0.2.2:$CARRIER_PORT, no bans"
