# Phone: how the inbound receptionist transfers a call

- **Date:** 2026-10-22 (agent, loop #448; the user's ask is in
  [2026-10-21-phone-sip-provider-and-receptionist.md](2026-10-21-phone-sip-provider-and-receptionist.md):
  "SIP REFER to a configured extension, e.g. for a known caller or an urgent reason")

Choices made building step 4 of [../cc/phone-sip.md](../cc/phone-sip.md) §4 that a later reader
would otherwise rediscover. Step 3's choices are in
[2026-10-22-phone-inbound-receptionist.md](2026-10-22-phone-inbound-receptionist.md).

**Targets are the household's, by name.** `phone.inbound.transfer_targets` is a list of up to 5
`{label, number, when}`; empty (the default) means no transfers and leaves the prompt exactly as
before. `number` is digits only, optionally after a `+` (an extension or a phone number in the
PBX's dial format): never a URI, never `*` or `#` (a feature code could change PBX state). The
model is given each `label` and its `when` and never a number, and acts only with
`[TRANSFER: <label>]`; the parser turns that into a transfer only for a configured label (any
other `[TRANSFER: …]`, `911` or a number included, is dropped unspoken like any unknown command).
So the caller can never choose where a call goes, which is what stops toll fraud through the
PBX's outbound routes: the worst a caller can do is talk the model into ringing one of the
household's own targets.

**"Known caller" and "urgent" are the household's words, judged by the model.** Caller ID proves
nothing (§3.4), so there is no allowlist of numbers that transfer automatically: a spoofed number
would walk straight through. Instead each target's `when` says in the household's own words when
to transfer ("the caller is from Alex's school", "the caller says it is an emergency about the
children"), and the model judges it from what the caller said, which also proves nothing; the
household chooses its conditions knowing that. The prompt keeps the list and its conditions
private (the installed model told a caller the condition before it did), says that asking to be
put through, or naming a person or number, meets no condition by itself, and transfers as soon
as a condition is met. One attempt per call: a second `[TRANSFER]` is dropped.

**What a final NOTIFY means on Asterisk.** diago's `Refer` returns on the 202, so package sip
waits (30 s) for the final NOTIFY sipfrag and hangs up our leg on a 2xx. The L1 lane shows two
things about Asterisk (so FreePBX) a later reader would not guess: it sends the final 200 once the
target is ringing, not when it answers (what happens if nobody answers is the PBX's: FreePBX's
no-answer destination, voicemail usually); and it takes the caller off our leg into its dialplan
on the 202, so when a transfer then fails (a busy target) the PBX plays busy to the caller itself.
"Recover on failure" therefore means: a refused REFER (403/603 with transfers off, 405/501 on a
PBX without them) leaves the call as it was; after a 202, the call is still ours only while the
caller's audio still reaches us (2 s, RTP flows every 20 ms while bridged), and otherwise our
emptied leg is hung up rather than talked into. When the caller is still there the receptionist
says a fixed line ("I'm sorry, I couldn't put you through. Can I take a message instead?"), which
also goes into the conversation so the model's next turn knows, and takes a message.

**The REFER goes after the announcement.** The reply that carries `[TRANSFER]` ("One moment, I'll
put you through") is played in full first, as a hang-up's goodbye is; the call loop then blocks
on the transfer (the caller's audio queues meanwhile, at most 45 s for the whole transfer).

**Records.** `outcome` keeps meaning what it says about the message; cc migration 00310 adds
`transfer_label` (the name, not the number), `transfer_result` (`transferred` | `failed`) and
`transfer_detail`. A transferred call's notification is titled "Put through to <label>" and says
the call is the phone system's from there; a failed one says why, and whether the PBX had already
taken the caller. Twilio calls cannot transfer (the receptionist runs on SIP only); a call whose
provider cannot gets the failure path.

**Not verified on FreePBX itself** (L2, #447): that FreePBX 17's generated extension accepts
REFER as the lane's copied defaults do, and its NOTIFY timing. The setup guide's transfer section
says what to check.
