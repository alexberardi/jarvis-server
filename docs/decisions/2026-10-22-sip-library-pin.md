# SIP provider: library pin and what we do on top of diago

- **Date:** 2026-10-22 (build agent, loop #443, piece 2 of the phone-over-SIP plan,
  [../cc/phone-sip.md](../cc/phone-sip.md))

**Pin.** `github.com/emiago/diago v0.41.0`, and `replace github.com/emiago/sipgo => v1.5.0` in
`go.mod`. diago v0.40/v0.41 both require sipgo v1.6.0, and that tag was moved after the Go proxy
cached it: the proxy's v1.6.0 is commit `e8676d2`, GitHub's is `86ec9f7`. A build that depends on
which one you get is not reproducible, so we take v1.5.0, the last release with one meaning. The
`require` line still says v1.6.0 (diago's requirement); the replace is what builds. diago v0.41.0
on sipgo v1.5.0 builds with `CGO_ENABLED=0` (Opus is behind diago's `with_opus_c` tag; we use
G.711 only) and passes our L0 suite and the L1 lane. Move off the replace only to a sipgo
release whose tag the proxy and GitHub agree on.

**diago stays inside `internal/modules/cc/phone/sip`.** Package phone sees a UA and a Call of
8 kHz PCM; nothing else imports diago or sipgo. What we use: sipgo's client and server (digest
auth, transactions, dialogs), diago's dialog sessions for the INVITE (CANCEL on a cancelled
context, ACK, BYE, its re-INVITE handling) and its RTP session (packet reader/writer, SSRC
changes reset its sequence state). What we do ourselves, because diago does not or gets it wrong:

- **Registration** is our own supervisor over sipgo's client, not diago's `Register` loop (which
  gives up on one failed refresh and notices a dead PBX only at the next one). Every attempt
  drops the previous attempt's `Authorization` header: resending it with a wrong password is a
  second failure per attempt for the PBX's intrusion detection.
- **Stopping**: 401/407 after our digest answer and 403 count toward the stop at 3. A timeout
  counts only after the PBX has refused us since the last success (the real FreePBX firewall
  drops a banned host's packets; the lane answers 403, `2026-10-21-sip-l1-lane.md`). A PBX that
  only ever times out is down, not refusing us, and is retried with backoff forever. A stopped
  account stays stopped until its settings change or jarvisd restarts.
- **The socket is ours** (`net.ListenPacket` + `Server.ServeUDP`), not diago's
  `ServeBackground`, so closing the UA releases the port before `Close` returns and a restart
  binds the same port, keeping the same Contact (FreePBX: `max_contacts=1`,
  `remove_existing=no`).
- **Remote SDP is rewritten before diago reads it** (`canonicalSDP`): diago reads SDP as one flat
  map, takes the first `c=` (ignoring a media-level one), refuses more than one `m=` line, indexes
  `c=` fields unchecked and overruns its codec slice on a repeated `rtpmap`. We keep the audio
  section only, one session-level `c=` with the address that applies to the audio, and checked,
  de-duplicated rtpmaps; an answer we cannot use ends the call, a re-INVITE we cannot use gets
  488. `FuzzSDP` checks that what we accept never panics diago.
- **DTMF** uses the answer's `telephone-event` payload type, read from the SDP ourselves:
  diago matches codecs by full equality, so a PBX offering telephone-event on 96 loses DTMF
  entirely, and its writer assumes 101.
- **UPDATE** (diago has no handler) is answered 200 with our offer's SDP; a re-INVITE gets the
  same from diago.
- **A media re-INVITE**: diago swaps its RTP session and stops the old reader with a read
  deadline that a read starting after the swap still sees; our reader clears it and goes on.
- **Started** waits for the first RTP packet after the 200 (answer supervision can run ahead of
  the audio, §3.3), and a 183 never starts a call.

**Inbound INVITEs** get 480 until the receptionist (piece 5) exists, so the PBX moves the caller
on. Requests from anywhere but the registrar's address get 403.

**Settings** (`phone.sip.*`, system or household scope, not in the mobile household settings
yet): server, port, transport, extension, password (secret), local port (default 5062, one per
account), dial format (10-digit default, 11-digit, e164) and dial prefix. Server, extension and
password are the set that resolves household → system like AD6's Twilio set; the other values
come from the same level. A UA starts for every account the settings call for when cc starts,
the set is reconciled every minute (and when a call needs an account that is not running), and
every UA unregisters on shutdown.

**A recorded announcement is a failed call.** FreePBX answers "all trunks busy" with 200 OK and
a recording. If the call's first utterance is a carrier or PBX intercept ("all circuits are
busy", "the number you have dialed…", `live.IsAnnouncement`), the call hangs up at once and the
session fails with what was heard; the model is never asked to talk to it. Provider-neutral, so
Twilio gets it too. Voicemail is not detected (as before).

**The L1 lane's 100.** The lane's decision left open how jarvisd takes 100 from the baresip
stand-in. `tools/siplane` runs the provider's UA in its own process (the same package a
`sip` household gets, not a whole jarvisd: placing a call through jarvisd needs the model, a
confirm card and its self-tests, piece 3). Each `jarvis-*` scenario has the stand-in delete its
account over `ctrl_tcp` (`uadel`, which unregisters; baresip 1.1.0 has no `uareg`) and create
it again from the lane's accounts file afterwards (`uanew`).
