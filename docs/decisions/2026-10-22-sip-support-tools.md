# SIP support tools: one process-wide trace, a feature-code dial path, self-tests that run in the background

- **Date:** 2026-10-22 (agent, loop #444: phone over SIP, piece 3; the user asked the agent to
  choose)

What [../cc/phone-sip.md](../cc/phone-sip.md) §6 asks for (status, self-tests, an exportable SIP
trace, a FreePBX guide) is built in `phone/sip` (`trace.go`, `Check`, `DialFeature`, the inbound
record), `phone/sipsupport.go`, the admin's Phone page and `docs/install/freepbx-sip.md`. The
choices a later reader would otherwise rediscover:

**One tracer for the whole process.** sipgo v1.5.0's message tracer is process-wide
(`sip.SIPDebug` plus `sip.SIPDebugTracer`), so package `sip` sets it once, in `init`, before any
socket exists (sipgo reads `SIPDebug` unlocked on every message). Each message is matched to the
UA whose local address it was sent or read on, or, over TCP, where our outgoing connection has an
ephemeral port, whose registrar it was exchanged with; a message of no UA's is dropped. With a
tracer set, `SIPDebug` only calls it: sipgo logs nothing more. The cost is one copy and a
redaction per SIP message; RTP never passes through it.

**What is kept.** The last 20 calls (INVITE dialogs and everything in them) and, apart from
them, the last 30 REGISTER/OPTIONS exchanges (one OPTIONS goes out every 30 s, which would
otherwise push the calls out), at most 100 messages per dialog and 16 KB per message, in memory
only (a restart clears it). Redaction happens when a message is recorded, so nothing secret is
ever stored: in `Authorization` and `Proxy-Authorization` only `username`, `realm`, `uri`,
`algorithm` and `qop` stay, every other parameter (response, nonce, cnonce, nc, opaque) becomes
`<redacted>`, since response and nonce together allow offline password guessing; every
occurrence of the account's password is replaced too, however short; SDP and other text bodies
stay (codecs and media addresses are what a support reader needs), a binary body is left out.
The trace is superuser-only: it carries every household's dialed numbers and caller IDs.

**Feature codes have their own dial path.** The normal path runs every number through
`NormalizeUS` (it refuses emergency, short and premium numbers) and now `UA.Dial` itself accepts
only digits after an optional `+`. The self-tests dial through `UA.DialFeature`, which allows
exactly `*43` (echo test) and `*65` (say extension number) and is reachable only from the
self-tests. Other feature codes change PBX state (`*72` forwarding) or reach things the normal
path refuses, so they are not added on request.

**Call my phone is the normal call.** It drafts an ordinary call plan with the number the operator
typed (`PlanRequest.Number`: no phonebook or web search) and posts the usual confirm card; caps,
do-not-call, the disclosure and the AI call itself are unchanged. It first checks that the
household's calls really go out through the account under test.

**Inbound watch observes, it does not answer.** Inbound calls are piece 5 (#446). Until then
every INVITE is refused 480 as before, so the PBX moves on to its next destination; the watch
reports the next one's caller ID (P-Asserted-Identity, then From), the dialed user and the SDP's
audio address and codecs. The caller's audio level, which §6 also lists, needs the call
answered, and stays with piece 5.

**Self-tests run in the background.** The echo test takes about 40 s (FreePBX's instructions
play for 22 s before `Echo()` starts), the inbound watch 60 s: longer than an HTTP request should
wait. `POST /api/phone/sip/{port}/tests/{test}` starts one (one at a time per account) and returns
202; `GET /api/phone/sip` shows its steps and result, and the page polls it each second while a
test runs. The echo test waits for the PBX's prompt to end (2 s of quiet after speech), plays a
fixed phrase, and measures the delay in received samples from the moment it was queued (the
inbound audio runs in real time, so samples are time); it passes at a word error rate of 0.35 or
less within what came back. The connection test asks the registration supervisor to re-register
now rather than sending a REGISTER of its own, and never retries a stopped registration (one more
refusal only extends a firewall ban). `tools/siplane` runs the same code (`phone.RunSIPSelfTest`)
against the L1 Asterisk.
