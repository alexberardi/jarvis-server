# Phone over SIP (FreePBX and other PBXs)

Design and risk review for the SIP phone provider: jarvisd joins a PBX as an ordinary extension,
places outbound calls through it and answers inbound calls as a receptionist/screener. Decision:
[../decisions/2026-10-21-phone-sip-provider-and-receptionist.md](../decisions/2026-10-21-phone-sip-provider-and-receptionist.md).
Today's phone feature (Twilio, outbound only): [11-phone.md](11-phone.md).

Written 2026-10-21 from a read of `internal/modules/cc/phone`, the FreePBX 17 source
(`release/17.0`: core `PJSip.class.php`, `install.php`, sipsettings) and the Go SIP libraries.
Nobody on the project runs a PBX, so §5 is how we test without one.

**Verdict: viable.** No blocker found. The risk is in the many small PBX behaviours (§3), a
young SIP library (§2) and the fact that we cannot see the user's PBX (§5, §6).

---

## 1. What changes in jarvisd

The live loop (VAD, STT, model, TTS, escalation, recorder) is transport-agnostic in substance
but Twilio-shaped in code. The seams:

- **`Provider`** (`phone/provider.go:18`) takes TwiML in `StartCall`. It becomes "place a call,
  return a `Call`" where a `Call` yields 20 ms 8 kHz PCM frames, accepts paced outbound PCM,
  reports playback position, sends DTMF, hangs up, and signals answered / ended with a reason.
  Twilio's adapter wraps today's Media Streams WebSocket; SIP's wraps an RTP session.
- **`mediaSession`** (`phone/media.go:97`) is concrete and Twilio-coupled (`streamSID`, WS,
  `MarkMessage`). `turn` and `runEscalation` use only `speak`, `requestHangup`, `armIdleHangup`:
  extract that as an interface. Marks become "pacer has sent sample N".
- **Credentials / settings** (`telephony.go`, AD6) are three Twilio keys. Add `phone.provider`
  (`twilio` default | `sip`) and a SIP set resolved the same way (household → system, complete or
  error): server host/port, transport, extension (auth user), password (secret), dial format
  (§3.3), answer delay, inbound enabled. `JARVIS_PHONE_PUBLIC_URL` and the WS signature gate only
  apply to Twilio (`dialer.go:150` fails without a URL today).
- **Answer/outcome detection** improves: Twilio today only knows "media stream started within
  60 s" (`dialer.go:200`); SIP gives real 486 busy / 480 / 404 / 503 / CANCEL.
- **Schema**: `twilio_call_sid` → provider-neutral call id; a `provider` column. Inbound gets its
  **own table** (`phone_inbound_calls`: caller number/name, reason, summary, message, outcome,
  recording) rather than bending the outbound state machine, whose states, confirm card,
  `goal NOT NULL`, caps and DNC all assume we dial out (`store.go:17-41`, baseline `:563-595`).
- **Lifecycle of a registration**: unlike Twilio, SIP is a long-lived client per configured
  account (one UA per household that sets its own, one for the system set). It starts with cc,
  re-registers, and reports status to settings/admin.
- **Not ported to SIP**: `LineType` (Twilio Lookup) returns `unknown`; the plan already handles it.

## 2. Library: diago on sipgo

`emiago/diago` (MPL-2.0, v0.41, media + dialogs) on `emiago/sipgo` (BSD-2, v1.5/1.6, signalling).
Pure Go with default build tags (Opus is behind `with_opus_c`; we use G.711 only), so
`CGO_ENABLED=0` holds. Has REGISTER with digest + refresh, UAC/UAS INVITE, CANCEL/BYE, SDP
offer/answer, hold, RTP/RTCP, jitter buffer, 20 ms paced writer, RFC 4733 DTMF, REFER + NOTIFY,
configurable RTP port range, `diagotest`. LiveKit's SIP bridge uses sipgo in production (its media
stack is its own and needs cgo, so it is a reference to read, not a dependency).

Risks and what we do about them:

| Risk | Mitigation |
|---|---|
| Pre-1.0, fast API churn; one maintainer for both libs | Pin exact versions; keep diago behind our own `phone/sip` package so we can drop to sipgo + pion |
| sipgo v1.6.0 was retagged; the Go proxy serves the old commit | Pin v1.5.0 or a pseudo-version |
| Open bugs: a failed refresh tears down the registration; dead PBX unnoticed until next refresh | Our own registration supervisor: backoff, OPTIONS keepalive to the registrar |
| `Refer` returns on 202; failed REFER leaks the dialog | Wait for the final NOTIFY sipfrag; BYE ourselves on failure/timeout |
| Remote SDP can panic two indexing paths; media-level `c=` ignored | `recover` around handlers; fuzz SDP parsing in our tests; upstream fixes |
| DTMF assumes PT 101 | Read `telephone-event` PT from the SDP |
| No session timers (RFC 4028), no RTP-timeout | Answer re-INVITE/UPDATE with the same SDP; our own no-RTP watchdog (~30 s) |
| MPL-2.0 | File-level copyleft on diago's own files only; fine to link |

## 3. Gotchas (FreePBX 17 / Asterisk 22 defaults unless noted)

### 3.1 Registration and network

| Gotcha | Impact | Mitigation |
|---|---|---|
| **Responsive Firewall + Intrusion Detection (fail2ban)**: ~3 failed REGISTERs → dropped; repeated → banned 30 min to 24 h+ | A wrong password plus a retry loop locks the Jarvis host out of the PBX (and possibly its web UI) | Stop after 3 auth failures and say so ("check password; the PBX may have blocked this host, unblock under Firewall → Status"). Exponential backoff always. Setup guide: put the Jarvis IP in Firewall **Trusted** and the IDS whitelist |
| `max_contacts=1`, `remove_existing=no` | After a restart on a new port/IP, the new REGISTER is rejected until the old one expires; rejections feed the firewall | Stable local SIP port and Contact across restarts; `Expires: 0` on clean shutdown; guide says set Remove Existing = yes |
| `qualify_frequency=60` (OPTIONS) | Unanswered OPTIONS marks us unavailable → inbound calls go to voicemail | Answer OPTIONS 200 |
| Inbound needs the PBX to reach us | INVITE and RTP to jarvisd on the LAN | Listen on a configured port (not 5060 by default, in case the PBX shares the host) and a pinned RTP range; Docker installs need host networking; macOS firewall prompt; Windows firewall rule |
| SIP scanners | Random INVITEs if the port is exposed | Accept requests only from the registrar's address; never forward the port |
| NAT (PBX in the cloud, Jarvis at home) | One-way or no audio | Supported but second-class: symmetric RTP (reply to the source address), rport, correct Contact; guide recommends PBX-side `rtp_symmetric`/`force_rport`/`rewrite_contact` (FreePBX's NAT=yes). LAN is the main case |

### 3.2 Media

| Gotcha | Impact | Mitigation |
|---|---|---|
| **`rtptimeout=30`** | Asterisk hangs up after 30 s without RTP, e.g. while the model thinks if we stop sending | Send a frame every 20 ms always, silence included (the recorder already assumes continuous frames) |
| `timers=yes` (session timers) | Re-INVITE/UPDATE refresh; ignore it and the call drops at expiry | Answer refreshes; don't advertise `timer` |
| `direct_media=yes` | Re-INVITE moves RTP to another address mid-call | Handle a media re-INVITE; guide says Direct Media = no |
| SSRC changes across bridges/transfers | Dropped audio if we latch the first SSRC | Reset sequence/jitter state on a new SSRC |
| Codecs: ulaw, alaw, g722, gsm, g726 | — | Offer PCMU + PCMA (+ telephone-event/8000) |
| Half-duplex, no barge-in | Callers talk over the receptionist and are not heard; worse on inbound than on business calls | **Done (2026-10-21, provider-neutral, `media.go`):** while our audio plays, speech louder and longer than our echo (`VADConfig.BargeIn*`) calls `Call.Flush` and gets the next turn; never during the disclosure or the goodbye ([decision](../decisions/2026-10-21-phone-barge-in.md)). The SIP Call's `Flush` must drop its pacer queue |

### 3.3 Outbound

| Gotcha | Impact | Mitigation |
|---|---|---|
| Caller ID is set by the PBX (extension Outbound CID > route > trunk unless overridden); what we send is ignored | Calls show the wrong number or the carrier rejects them | Guide: set the extension's Outbound CID to the main number. We can't verify it ourselves; the "call my phone" test (§6) shows it |
| Dial patterns are site-specific (`NXXNXXXXXX`, `1NXX…`, `9` prefix); E.164 `+1…` often matches no route | "Cannot be completed as dialed" | `phone.sip.dial_format` (10-digit / 11-digit / E.164, optional prefix); still NANP-only via `NormalizeUS`, which refuses 911/premium |
| **183 is never an answer**; no-answer/no-route = 183 announcement then 503; **all trunks busy = 200 OK + "all circuits are busy" recording** | Disclosure and AI talk to a recording; wrong outcome | Answer = 200 only; map final codes to outcomes; after answer, treat a recorded announcement as a failed call (existing guard/loop-break plus a short "is this a person" check on the first utterance). Same class as voicemail, which Twilio also never detected |
| In-call feature codes (`##` blind transfer, `*2`, `**` hangup via `DIAL_OPTIONS=HhTtr`) | Sending those as DTMF would transfer or hang up the call | DTMF is not offered to the model today (`turn.go:411`); if IVR navigation is added, refuse `*`/`#` sequences that are feature codes |
| PBX may record calls on its own | Disclosure already says "may be recorded" | Nothing |
| Carrier answer supervision varies | 200 can arrive late/early | Disclosure on 200 after the first inbound audio, not on 200 alone |

### 3.4 Inbound

| Gotcha | Impact | Mitigation |
|---|---|---|
| Caller identity: P-Asserted-Identity (`sendrpid=pai`), then From; CNAM only if the trunk provides it; Diversion unreliable | Wrong/missing caller | Read PAI, then From; treat all of it as a hint, never as authentication |
| Ring groups: an instant answer steals every call | Humans never get to pick up | `answer_delay` setting (default 0 for direct routes; guide suggests No Answer destination instead of ring-all) |
| Call waiting on by default: a second INVITE mid-call | Two concurrent calls on one extension | Concurrent inbound cap; above it reply 486 so the PBX goes to voicemail |
| Untrusted caller and spam | Prompt injection, long robocalls burning model time | Inbound agent has only `take_message` / `hang_up` (and later `transfer` to fixed targets); no household data; short max duration (~3 min); daily cap |
| Transfer (REFER) | Allowed by default and reaches outbound routes; external transfers can be refused for a "foreign" caller ID | Later step; wait for the final NOTIFY; transfer targets are configured extensions/numbers, never chosen by the caller |

## 4. Build order

1. **Provider refactor** (no behaviour change): `Call` interface, Twilio adapter over today's
   WS, `mediaSession` split, neutral call id column. Existing tests (`call_test.go` `twilioSim`,
   goldens) must stay green unchanged; they become the Twilio adapter's tests.
   **Done** (`refactor/phone-provider-seam`): `Provider`/`Call` in `phone/provider.go`, the
   Twilio adapter in `phone/twilio.go` + `phone/twilio_media.go`, the transport-neutral loop
   in `phone/media.go` (turns see only `callLine`), `phone.provider` (`twilio` only; system
   scope, not yet in the mobile household settings). Playback position replaces marks
   (Twilio's marks still go out named `t<turn>`). Left for step 2: the `twilio_call_sid` →
   neutral column rename and a `provider` column (migrations; the Go field is `CallID`).
2. **SIP outbound**: `phone/sip` (registration supervisor, OPTIONS, outbound INVITE, pacer,
   watchdog, re-INVITE/UPDATE), settings, status in admin, self-tests (§6).
   **Done** (loop #443): `phone/sip` on diago v0.41.0 + sipgo v1.5.0 (the pin and what we do
   on top of diago: [decision](../decisions/2026-10-22-sip-library-pin.md)); `phone.provider`
   `sip` with the `phone.sip.*` account (`phone/sipline.go`: server, port, transport,
   extension, password, local port, dial format, dial prefix; system or household scope, not
   in the mobile household settings), one UA per account started with cc; final codes →
   `EndReason` and an immediate failure reason; a first utterance that is a recorded
   intercept fails the call. Verified on L1 (the `jarvis-*` scenarios). Left: status in admin,
   self-tests and the SIP trace (piece 3); the `twilio_call_sid` → neutral column rename (step 1's
   carry-over, not done here).
3. **Inbound receptionist**: answer, prompt, `take_message`, notification, `phone_inbound_calls`,
   caps, barge-in. New caller class: held for the user's review.
   **Done** (loop #446; choices in
   [decision](../decisions/2026-10-22-phone-inbound-receptionist.md)): `phone/sip` offers each
   INVITE from the registrar to `Config.Inbound` with its caller (P-Asserted-Identity, then From),
   refuses it (480, or 486 above a cap) or rings it for `phone.inbound.answer_delay_seconds` and
   answers (a CANCEL meanwhile leaves it unanswered); `phone/inbound.go` admits a call for the one
   household that turned `phone.inbound.enabled` on for that SIP account (with `phone_calls.enabled`),
   under `phone.inbound.max_concurrent_calls` (1) and `phone.inbound.calls_per_day` (20, UTC day),
   and only once the disclosure is synthesized; the receptionist (`live/inbound.go`,
   `phone/inbound_agent.go`) runs on the shared call loop (barge-in included) with only the
   household's name, `[TAKE_MESSAGE]` and `[HANGUP]` (any other bracketed command is dropped
   unspoken), and says goodbye at `phone.inbound.max_call_seconds` (180); cc migration 00300
   `cc_phone_inbound_calls`; one notification per call (caller marked unverified, reason, message,
   summary; no transcript) and the recording under `phone-calls/` like an outbound call's. Verified
   on L1 (`jarvis-inbound*`) and, for the conversation, on the installed model
   (`inbound_model_test.go`, prompt injection included). Left: transfer to a person (step 4), the
   inbound settings in the mobile household settings, a list of inbound calls in the apps.
4. **Transfer** (REFER) to configured targets.

## 5. Testing without a VoIP setup

No carrier, no PSTN, no PBX of our own to maintain. Everything below is disposable and scripted.

| Layer | What | Proves | When |
|---|---|---|---|
| **L0 unit** | A transport-agnostic "far end" sim replacing `twilioSim` for both adapters; `diagotest`: two diago UAs over loopback (one is jarvisd, the other plays the business/caller from WAV and records); SDP and PCM fuzzing | Call logic, pacing, watchdog, codes → outcomes, re-INVITE, DTMF PT, panics | Every PR (race suite) |
| **L1 Asterisk lane** | `andrius/asterisk:22` (same Asterisk as FreePBX 17) in Docker, host networking, with a `pjsip.conf` that **copies FreePBX 17's generated extension defaults** (§3: max_contacts=1, qualify 60, timers, direct_media, rtptimeout 30, sendrpid pai) and a dialplan that **copies FreePBX behaviours**: `*43` echo, busy → 486, no answer → 183 + 503, all-trunks-busy → 200 + recording, a ring-group call, call waiting. A "fake carrier" endpoint is baresip (WAV in, WAV out) or SIPp for signalling-only scenarios | Registration (incl. a wrong password → we stop at 3), outbound and inbound calls end to end with real RTP through a real Asterisk; the far end's recording is transcribed with the harness's CPU whisper and checked with `max_wer`, so both audio directions are proven | PRs touching `phone/**`, and nightly |
| **L2 real FreePBX** | A Debian 12 VM built once with `sng_freepbx_debian_install.sh`, provisioned by script (`fwconsole bulkimport` for the extension and inbound route; SQL + `fwconsole reload` for the outbound route and a trunk to the fake carrier; GraphQL can't make outbound routes), snapshotted | That L1's copied defaults still match what FreePBX generates (diff `pjsip show endpoint` against L1's config), the firewall lockout path, and one call each way | Weekly / before a release that touches SIP, on the dev box. Optional: L1 catches nearly everything; L2 catches drift between FreePBX releases |
| **L3 harness** | `scripts/sandbox up --sip` brings up the L1 Asterisk next to the sandbox; `pipeline say --sip` places a scripted business call through it | Agents verify phone changes like any other change | On demand |
| **L4 in the product** | Self-tests and diagnostics (§6) | That the *user's* PBX works, without us touching it | Every install that turns SIP on |
| **L5 field beta** | The requesting user, with SIP marked experimental | Real carrier, real PBX | After 2 |

**L1 as built** (2026-10-21; choices in
[../decisions/2026-10-21-sip-l1-lane.md](../decisions/2026-10-21-sip-l1-lane.md)):

```sh
scripts/sandbox up --name N --voice --sip      # sandbox + TTS/CPU whisper + the lane
go run ./tools/siplane --sandbox N [--filter RE] [--out DIR] [--list]
scripts/sip-lane status|cli|unban --name N      # registrations, `pjsip show endpoint 100`, lift a ban
scripts/sandbox down --name N --purge           # removes the lane's containers too
```

- `test/sip/asterisk/`: the PBX image (Asterisk 22 by digest + core prompts) and its
  `pjsip.conf` (100 = Jarvis, 101 = Alice, `carrier` trunk; FreePBX 17's generated defaults,
  kept by `TestLaneCopiesFreePBXDefaults`), `extensions.conf` (the behaviours below) and
  `lane-firewall` (3 failed authentications → banned, 403). Bound to 127.0.0.1; SIP 5060 and
  RTP 20000–20199 plus the sandbox's offset.
- `test/sip/farend/`: baresip (WAV in, WAV out, driven over `ctrl_tcp`), SIPp, sox.
  `test/sip/sipp/`: the signalling-only scenarios.
- `tools/siplane`: 23 scenarios: registration and the endpoint's defaults, `*43` echo, `*65`,
  an internal call, the inbound route (DID 5551230000 → 100), the outbound route (→ carrier),
  ring group 600 (DID 5551230600), call waiting, busy with call waiting off (486), trunk busy
  (486, SIPp), no answer (183 + 503, SIPp), all trunks busy (200 + recording), a second contact
  refused (max_contacts=1), wrong password ×3 → ban; and jarvisd's own SIP provider
  (`phone/sip`, run in the runner's process as 100): it registers, unregisters on shutdown and
  registers again on the same port; a wrong password stops it after exactly 3 attempts; it
  dials a 10-digit number through the outbound route with audio both ways; busy, no answer and
  all trunks busy come out as their outcomes; and jarvisd answering inbound calls: the carrier's
  call to the DID is answered after the answer delay with the caller from PAI, the disclosure is
  heard and the message transcribed; a ring group's other phone picking up during the delay
  cancels jarvisd's leg unanswered; a second call above the cap gets 486 (busy). Audio is checked both ways: each far end's
  recording, and jarvisd's, is transcribed by the sandbox's CPU whisper, max WER 0.25.
- CI: `.github/workflows/sip-lane.yml` on changes to `phone/**` or the lane, nightly, and by hand.
- A baresip stand-in holds 100 for the scenarios that play a desk phone or the inbound route;
  each `jarvis-*` scenario has it delete its account (unregistering) and create it again
  afterwards, since `max_contacts=1` lets one of them in.

No real PSTN call is needed in CI. If one is ever wanted, the existing Twilio account could front
the L1 Asterisk with Twilio Elastic SIP Trunking, but nothing in this plan depends on it.

## 6. Support without seeing the user's PBX

- **Status**: registration state, last error with a plain hint (auth failed, blocked by the
  PBX firewall, no route, unreachable), OPTIONS round-trip time, active calls.
- **Self-tests** (buttons in settings):
  - *Connection*: register + OPTIONS.
  - *Audio*: dial `*43` (echo test), play a known phrase, transcribe what comes back:
    proves both audio directions, the codec and round-trip latency.
  - *Hear the PBX*: dial `*65` (speaks the extension number), transcribe, compare.
  - *Call my phone*: outbound to the user's own number (normal confirm card) to check caller ID
    and the dial format on their real trunk.
  - *Inbound*: "call your Jarvis number now" and show the INVITE, caller ID and audio level live.
- **SIP trace**: a ring buffer of the last N calls' SIP messages (Authorization and passwords
  redacted, no audio), exportable from the admin, so a failed call can be sent to us and read.
- **Setup guide** (FreePBX screenshots): add PJSIP extension; Outbound CID; Direct Media = no;
  Remove Existing = yes; Firewall Trusted + IDS whitelist for the Jarvis IP; route an inbound
  number / No Answer destination to the extension.

## 7. Answered (user, 2026-10-21)

- **Barge-in ships before inbound** (part of step 3).
- **L2 runs weekly on the dev box** (a FreePBX VM).
- **Inbound messages**: a notification with caller, reason and summary, as outbound outcome cards
  do today; the recording is kept the same way (`audio_available`). No inbox transcript for now.
