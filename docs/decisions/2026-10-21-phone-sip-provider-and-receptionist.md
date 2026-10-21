# Phone: a SIP provider beside Twilio, and inbound calls as a receptionist/screener

- **Date:** 2026-10-21 (user: receptionist/screener, provider setting defaulting to Twilio;
  agent: SIP-extension approach, the rest below, as the user asked the agent to choose)

Asked for by a user running FreePBX (a web front end for the Asterisk PBX) who wants Jarvis on
their phone system for inbound and outbound calls. Changes [../cc/11-phone.md](../cc/11-phone.md),
whose scope was outbound-only, Twilio-only.

**Provider setting.** A new cc setting `phone.provider` (household or system scope),
`twilio` (default) or `sip`. Twilio stays exactly as it is. The `Provider` interface stops
carrying TwiML: a provider places or answers a call and hands the live loop a stream of 8 kHz
audio frames, and the Twilio Media Streams WebSocket becomes the Twilio adapter's transport.
The live loop (VAD, STT, model, TTS, escalation) is shared by both.

**SIP as an extension, not ARI/AudioSocket.** jarvisd registers to the PBX as an ordinary SIP
extension (server, extension, password, caller ID: one credential set resolved like AD6's
Twilio set, the password a secret). FreePBX setup is "add a PJSIP extension and enter its details
in Jarvis"; it also works with FreeSWITCH, 3CX or a SIP trunk pointed at Jarvis. Rejected:
Asterisk's ARI and AudioSocket, which are Asterisk-only and need custom dialplan the user would
have to write. Pure Go (`emiago/sipgo`, `pion/rtp`) so `CGO_ENABLED=0` holds; G.711 μ-law/A-law
with `live/mulaw.go`, RFC 2833 DTMF. On a LAN no public URL or tunnel is needed.
Line-type lookup has no SIP equivalent and returns `unknown` (the plan already handles it).

**Outbound over SIP** is today's flow unchanged (confirm card, disclosure, caps, do-not-call);
the call goes out through the PBX's own routes and trunk.

**Inbound is receptionist/screener only.** Jarvis answers calls the PBX routes to its extension,
says it is an automated assistant, asks who is calling and why, then either takes a message
(a notification to the household with caller ID, name, reason and a summary) or ends obvious
robocalls/spam. The caller is untrusted: caller ID can be spoofed and what they say is prompt
input, so the inbound agent gets **no household tools or data** (no calendar, memory, devices,
phonebook beyond "is this number known"), only `take_message` and `hang_up`. Caller ID never
grants access; calling in to control the house is out of scope (it would need a PIN or voice
match and its own design). Off by default (`phone.inbound.enabled`), and per household.
Transfer to a person (SIP REFER to a configured extension, e.g. for a known caller or an urgent
reason) is a later step, not the first.

**Order.** (1) Provider refactor with Twilio behind it, no behaviour change. (2) SIP outbound,
tested against Asterisk in a container (harness + CI integration test; never the user's PBX).
(3) Inbound receptionist. Inbound adds a new caller class, so its PR is held for the user's
review under the merge rules.
