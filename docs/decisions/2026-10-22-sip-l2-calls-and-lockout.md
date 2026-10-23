# SIP L2 lane: real calls through the FreePBX VM, and what its lockout really does

- **Date:** 2026-10-22 (agent, loop item #490, the deferred half of
  [2026-10-22-sip-l2-freepbx-lane.md](2026-10-22-sip-l2-freepbx-lane.md); run for real on the dev
  box against FreePBX 17.0.33.2, Asterisk 22.11.0)

`scripts/freepbx-lane calls` (and `weekly`, after its drift capture, unless `--no-calls`) has
jarvisd's SIP provider register as extension 100 on the VM, place one call through the outbound
route to the fake carrier, answer one from it via DID 5551230000 (both audio directions
transcribed, max WER 0.25 as in L1), and go through the wrong-password path
(`go run ./tools/siplane l2`). The choices a later reader would otherwise rediscover:

**Forwards on loopback only, the RTP range narrowed to them.** qemu's user network gets SIP over
UDP and TCP on 127.0.0.1:5260 (→ 5060) and RTP on 127.0.0.1:30800-30819, each port to the same
number inside, so the ports in Asterisk's SDP stay right. Nothing is bound to another address
(`scripts/freepbx-lane-check.sh` checks every forward). 5260 and 30800-30819 sit between the
sandboxes' port blocks, so an L1 lane never collides. The VM's network stays on for the calls:
`restrict=on` also drops the VM's replies on forwarded UDP ports, so `weekly --no-update` is
offline only with `--no-calls`.

**Reuse an L1 sandbox's far ends and voice.** The carrier is the L1 lane's baresip and the voice
is the sandbox's TTS and CPU whisper (`scripts/sandbox up --voice --sip --llm none`, brought up by
`calls` unless `--sandbox N` is already up, purged after). One fake carrier and one scenario
runner for both lanes; L2 only swaps the PBX: the registrar becomes the VM's forward and the
Asterisk CLI runs over the lane's SSH.

**Fitting the VM to the host is a separate step, after the drift capture** (`calls.sh`, never
touching extension 100, so the drift check still reads the release's own defaults). Everything
the host sends reaches the VM from 10.0.2.2, and the VM reaches the host's loopback at 10.0.2.2.
Through FreePBX's own Sipsettings: the RTP range, External Address 127.0.0.1 with only the VM
(10.0.2.15/32) as a local network, and the Endpoint Identifier Order username first. In
`pjsip.transports_custom_post.conf`, FreePBX's place for what it has no field for:
`external_signaling_port=5260` (then an Asterisk restart: the transport is `allow_reload=no`).
The carrier trunk is re-pointed by SQL at the sandbox's carrier: SIP Server 127.0.0.1, Outbound
Proxy `sip:10.0.2.2:PORT;lr`. Two of these exist only because host and VM share one address
through qemu's NAT: by IP first, FreePBX takes extension 100's REGISTER from 10.0.2.2 for the
carrier trunk's (its identify matches 10.0.2.2) and refuses the right password; and baresip 1.1.0
answers 404 to an INVITE whose request URI names 10.0.2.2, an address not its own.

**What FreePBX's lockout actually is (corrects phone-sip.md §3.1).** On a fresh install the
Responsive Firewall is off (its setup wizard turns it on); what bans is the System Admin module's
intrusion detection, fail2ban: jail `asterisk-iptables`, 5 matches in 10 minutes, banned 30
minutes on every port (`iptables-allports`: SIP, SSH and the admin UI), and `recidive` bans for a
week after 20 matches in a day. Asterisk logs two matching lines per refused REGISTER, so the
third refused REGISTER bans: jarvisd's stop after exactly 3 (it does stop, and sends nothing
more) does not keep the Jarvis host under the ban. The unblock is fail2ban's
(`fail2ban-client unban`, Admin → System Admin → Intrusion Detection), not Firewall → Status;
`fwconsole firewall unbanall` does not exist on 17.0.33 (it prints the help and exits 0). The
lane's scenario expects exactly this, so a FreePBX release that counts differently fails the
weekly run. Whether jarvisd should stop after 2 and what its hint should name are product
questions, left to the user.

**The ban locks the lane out of its own VM**, since SSH also arrives from 10.0.2.2. Before the
wrong password, the scenario starts a timer in the VM (`systemd-run --on-active=90
fail2ban-client unban --all`); it lifts the ban whether or not SSH still gets in, and the scenario
then checks SSH answers and the right password registers. Ban detection from the host is the
VM's SSH no longer sending its banner.

**Seen, not fixed here:** FreePBX sends extension 100 unsolicited MWI NOTIFYs (voicemail is on
for a new extension) and jarvisd answers them 400 ("missing tag param in To header"); and L1's
`pjsip.conf` sets `endpoint_identifier_order=username,ip,anonymous` where FreePBX 17's default
is `ip,username,anonymous` (the drift check reads extension 100 only).
