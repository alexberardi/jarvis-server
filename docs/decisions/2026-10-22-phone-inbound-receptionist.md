# Phone: how the inbound receptionist decides, answers and reports a call

- **Date:** 2026-10-22 (agent, loop #446; the user's asks are in
  [2026-10-21-phone-sip-provider-and-receptionist.md](2026-10-21-phone-sip-provider-and-receptionist.md)
  and [../cc/phone-sip.md](../cc/phone-sip.md) §7)

Choices made building step 3 of [../cc/phone-sip.md](../cc/phone-sip.md) §4 that a later reader
would otherwise rediscover.

**Whose call is it.** A SIP account can be a household's own or the system's (shared by every
household on `phone.provider = sip` without one). An inbound call belongs to the one household
whose own `phone.inbound.enabled` is true and whose resolved SIP account is the line's (same
server, extension and local port), and which has `phone_calls.enabled` on too. A system-level
`phone.inbound.enabled` turns it on for nobody: inbound is per household, as asked. Two households
sharing one account with inbound on get no calls (480, and an error in the log): there is no way
to tell whose message it would be. No household: 480, today's answer, so the PBX goes on to its
next destination.

**Only from the registrar.** Package sip's existing guard answers 403 to any request whose source
address is not the registrar's (by address, resolved at start), before any inbound code runs. The
caller ID (P-Asserted-Identity, then From; trimmed to 64 printable characters) is stored and shown
in the notification as "not verified", and is never given to the model: a CNAM can carry text too.

**Caps and their defaults.** `phone.inbound.max_concurrent_calls` 1 (call waiting: a second call
gets 486 Busy Here, which FreePBX turns into voicemail), `phone.inbound.calls_per_day` 20 per UTC
day (the outbound cap's day), `phone.inbound.max_call_seconds` 180 (the call loop says goodbye and
hangs up; the supervisor ends it 20 s later if a turn hangs), `phone.inbound.answer_delay_seconds`
0 (a direct route; a ring group wants a few seconds). Only answered calls are rows, so only they
count against the daily cap; the concurrent slot is taken at admission, so a call that is still
ringing counts. Inbound calls do not count against the outbound caps, and an outbound call does not
take an inbound slot.

**No disclosure, no answer.** The disclosure is synthesized while the call is offered, before 180
Ringing; if TTS fails the call is refused 480 rather than answered silently.

**Tools are text tokens.** The live loop streams text, so the receptionist's two tools are tokens
like the outbound call's: `[TAKE_MESSAGE: name | reason | message]` and `[HANGUP]`. Its parser
swallows any other capitalised `[WORD]` or `[WORD: …]` (logged as refused, never spoken, never
acted on), so a command the model invents or a caller talks it into goes nowhere. `live.InboundTools`
and its test pin the set. A reply that ends in a question never hangs up, and an empty
take_message is ignored: the installed model did both on an injection attempt.

**What the household gets.** One notification per answered call (the existing `postCard`, so an
inbox card plus a push, as outbound outcome cards do): the caller's stated name, the caller ID
marked unverified, the reason, the message and a summary from the background model, with
`direction: inbound`, `inbound_call_id` and `audio_available` in its metadata. No transcript is
stored or sent ("no inbox transcript for now", §7); the summary is made from the in-memory
conversation. The recording goes to `phone-calls/<household>/inbound-<id>.wav` like an outbound
call's.

**A jarvisd restart** leaves no live call; rows still open at the next start are closed as failed
/ interrupted.

**Testing the conversation.** `tools/pipeline` belongs to the harness, so the prompt-injection
scenarios are a phone package test (`inbound_model_test.go`, `testdata/inbound_scenarios.json`) that
runs the receptionist's own turn against any OpenAI-compatible URL, a sandbox's LLM tap in practice,
and is skipped without one. A `phone-inbound` scenario kind in `tools/pipeline` would let the
loop's verify run them too.
