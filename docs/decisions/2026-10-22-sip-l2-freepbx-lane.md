# SIP L2 lane: a qemu VM of FreePBX 17, updated and provisioned afresh each week

- **Date:** 2026-10-22 (user chose the scope at triage; agent, building piece 6 of the
  phone-over-SIP plan, loop item #447)

The L2 lane of [../cc/phone-sip.md](../cc/phone-sip.md) §5 is `scripts/freepbx-lane`,
`scripts/freepbx-lane.d/` and `go run ./tools/siplane drift`. The choices a later reader would
otherwise rediscover:

**What the agent built, what the user does (user, 2026-10-22).** The dev box had no hypervisor,
the loop's sandbox cannot reach Sangoma's or Debian's mirrors, and agents cannot write
`~/.config/systemd`. So the lane landed written and checked without a VM (the drift check as Go
tests on a captured L1 fixture, the script's guards and `--dry-run` plans in
`scripts/freepbx-lane-check.sh`) and waits in QA until the user installs qemu with KVM access,
runs `scripts/freepbx-lane build --dev-box <dev-box>` once and enables the timer
(`scripts/freepbx-lane timer` prints how). One call each way and the firewall lockout path were left
for a follow-up: they need the SIP provider (#443), which merged while this lane was in review,
so the follow-up drives jarvisd's SIP client against the snapshot. (Built in loop #490:
[2026-10-22-sip-l2-calls-and-lockout.md](2026-10-22-sip-l2-calls-and-lockout.md).)

**qemu/KVM, not Docker or VirtualBox.** FreePBX's installer expects a whole Debian system
(systemd, its own firewall and fail2ban, kernel modules); in a container the lockout path would
not behave as on a real install. qemu with user-mode networking needs no root once `/dev/kvm` is
readable, forwards only SSH (127.0.0.1:2247) and HTTP (127.0.0.1:8447) on loopback, and keeps
the VM off the LAN. The trunk to the "carrier" points at 10.0.2.2 (the host's loopback as qemu
shows it), never a real carrier.

**Debian's cloud image + cloud-init, not netinst + preseed.** Same Debian 12, no installer to
drive: a NoCloud seed adds the lane's own user and SSH key (kept next to the disk, never
`~/.ssh`). The image is checked against Debian's `SHA512SUMS`. The installer is FreePBX's
`sng_freepbx_debian_install.sh` from GitHub (`FREEPBX_INSTALLER_REF` pins a commit; its sha256
goes in `install.log`, and `FREEPBX_INSTALLER_SHA256`, when set, is checked before it runs as
root). It defaults to `master`, unchecked, because the lane exists to see what a fresh install
gets today; pin both to rebuild a known install.

**Two snapshots; weekly starts from the bare install.** FreePBX writes an extension's defaults
when the extension is created, so re-reading an extension made at build time would never show
drift. `build` snapshots `installed` (the installer's result) and `provisioned`; `weekly`
reverts to `installed`, updates Debian and every FreePBX module, provisions extension 100
afresh and compares. `weekly --no-update` reverts to `provisioned` with the network cut
(`restrict=on`) for a quick offline look.

**Provisioning: FreePBX's own import where it has one.** Extension 100 and the inbound route
(DID 5551230000 → 100, as L1) go in with `fwconsole bulkimport`, so the extension gets exactly
the defaults the admin UI would give it; the CSVs name only the number, name, tech and secret.
The trunk and the outbound route have no import and GraphQL cannot create outbound routes, so
they are SQL into core's tables (`routes.sql`, citing where each table comes from) and
`fwconsole reload`. The SQL was written from the core module's source, not checked against a
live database; the first build is its test.

**What counts as drift.** `siplane drift` compares FreePBX's `pjsip show endpoint 100` +
`pjsip show aor 100` with two things: every setting `test/sip/asterisk/pjsip.conf` spells out
for 100 (in either spelling Asterisk accepts: yes/true, `ef`/184, `a,b`/`(a|b)`, 3.0/3.000000),
and every other setting against L1's own Asterisk output, captured into
`tools/siplane/testdata/l1-show-100.txt`. The second catches FreePBX setting something L1 leaves
to Asterisk's default. Names of this install (contact, caller ID, auth, aors, mailboxes,
voicemail extension, transport) are ignored. `TestL1BaselineMatchesConf` fails when pjsip.conf
changes without the fixture being captured again. Drift exits 1, which fails the systemd unit.

**Only the dev box.** `build` needs `--dev-box NAME` equal to this host's name and writes it next
to the disk; every other command refuses on another host. The lane's files live in
`~/.cache/jarvis-harness/freepbx` (`JARVIS_FREEPBX_DIR`), outside any checkout.
