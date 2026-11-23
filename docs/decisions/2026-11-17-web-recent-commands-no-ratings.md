# The web's Recent commands has no thumbs ratings

- **Date:** 2026-11-17, user ("leave them out").

Mobile's Recent Commands lets you rate each command up or down, and its help text says the
ratings help Jarvis learn and suggest model fine-tunes. jarvisd still stores them
(`POST /api/v0/transcripts/{id}/rate`), but nothing reads them since the LoRA fine-tune
pipeline was cut in the port ([PLAN](../PLAN.md) cut list; the inbox's adapter cards are not
ported for the same reason, [webapp/03](../webapp/03-inbox-parity.md)).

So the web's Recent commands (`/recent`) shows what was said, the commands that ran with their
arguments, and the reply, with no rating control: a button that promises learning which doesn't
happen would mislead. The rate route stays off the gateway. If ratings get a use again (an eval
set, a fine-tune), add the control and the route then.
