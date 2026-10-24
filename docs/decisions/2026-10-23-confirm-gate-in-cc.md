# desktop_confirm is gated in cc, with the user's own words

- **Date:** 2026-10-23 (user's ask, #574; the request_validation requirement awaits their review)

A desktop node parks a click, a submitting fill or an Enter behind the user's yes
(`dictate.Confirmer`), and the model answers it with `desktop_confirm`. On the dev box the 9B
called `desktop_confirm {answer: "Yes"}` right after `browser_fill` parked, in the same turn, so
the submit ran without the user ever seeing the question. The tool text ("Never answer it
yourself") was the only guard, and a prompt is not one.

cc now enforces it (`internal/modules/cc/confirm_gate.go`). A `desktop_confirm` reaches the node
only when all of these hold:

1. the conversation's last parked action has that `confirmation_id`;
2. a `request_validation` question was shown to the user after the park (the turn ended with
   `validation_required`);
3. the user answered after it: a new turn (chat, voice, typed) or the node's
   `validation-response` result. cc's own synthetic user messages (the text path's tool-result
   continuation, nags) don't count, and a newer `request_validation` needs a newer answer.
   Only the first answer after a shown question counts: a "cancel" followed by a later "yes"
   (about something else) stays a cancel.

The call's `answer` is replaced with the user's words; the model's is ignored, and the node's
Confirmer still decides yes or no from them; that call's result closes the gate. Otherwise cc doesn't call the node and returns a
tool error telling the model to ask with `request_validation` and wait.

Point 2 is what the ask says. It means a question asked in plain text isn't enough: after the
user's "yes" to it the model is told to ask again with `request_validation` (the next-turn hint
says so too). That costs one extra round, but without it a "yes" to some other plain-text
question could confirm a parked action the user never saw. It overlaps #577 (the model asks in
plain text instead of `request_validation`); if that lands a way to show the question in plain
text reliably, point 2 can be relaxed then.
