# Commands offer follow-up buttons on the Call finished card, picked by the model, checked by the server

- **Date:** 2026-11-08 (user)

After a call, an installed command can offer a button on the Call finished card, such as the
calendar's "Add to Calendar". The user chose the shape: commands **declare** their follow-ups the
way they would expose tools (a label, a plain-English "when" and typed params), and after the
call the background model picks the ones that apply to the transcript and fills in the params:
"post-phone-call tool calls where commands can expose tools themselves".

Considered first and rejected: a typed `CallOutcome` (appointment / reservation / order / other)
that each command matches in code. It is deterministic and testable, but asks a third-party
author to learn an outcome taxonomy and write the matching logic; the user judged that a hard
sell. The post-call step runs in the background, so a slower, deliberate model pass costs nothing
the user waits on.

The model's picks are suggestions. The server checks each one before a button appears (declared
params of the declared types, every date and time grounded in what was said on the call, no
private call detail in a string), at most 3 buttons per card, each runs at most once and only on a
tap. A wrong pick costs an ignored button, never an action. Only commands with **Share with phone
calls** on are asked.

Taps route through a server-plane callback that dispatches to the offering node, so one card can
carry buttons from several nodes without a mobile change, and the params come from the server,
never from the tap.

Spec: [../cc/phone-followups.md](../cc/phone-followups.md).
