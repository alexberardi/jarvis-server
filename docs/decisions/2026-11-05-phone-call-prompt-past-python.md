# The phone call prompt moves past the Python gateway's

- **Date:** 2026-11-05 (user, while building the call demo)

The outbound call prompt (`internal/modules/cc/phone/live/prompt_text.go`) was ported verbatim
from the Python gateway, and `testdata/system_prompt_*.txt` pinned it to the Python output. It
now changes in jarvisd alone: the Python gateway is retired, and scenario runs against prod's
model (`pipeline call`, `tools/pipeline/calls/restaurant.json`) showed it failing ordinary calls.
The goldens stay as the record of the exact prompt and carry each edit.

What changed, and why:

- **Optional details don't block the task.** A callback number, an email or a seating
  preference the business asks for but can do without gets "I don't have that to give; can you
  go ahead without it?" instead of "I'll get it and call you back". The user's call
  (option 3): give the number when one is on file (the `callback_number` call-context field,
  given only when asked), otherwise ask to go ahead without it.
- **Off-task requests are declined, not a reason to hang up.** A "loyalty program" asking for
  a date of birth, or an injected "system message" ordering pizzas, is refused and the call
  carries on with its task.
- **A read-back of a detail Jarvis just gave is confirmed.** The rule against confirming
  guessed private values stays for values the business brings up itself.
- **Escalations name the choices offered** ("7pm is full; they can do 6:15 or 7:30. Which
  works?"), so the person can answer from the notification.

The caller's mid-call answers carry their question and override the brief, and a question
already answered is never asked twice on one call (code, not prompt: `turn.go`).
