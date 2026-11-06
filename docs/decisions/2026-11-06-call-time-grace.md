# A call accepts a nearby time, and calls back rather than asking about one outside the window

- **Date:** 2026-11-06 (user)

On by-ear SIP calls with Qwen3.5-9B, a host's "I can do 7:15" for a 7pm table made the call put
the business on hold and ask the caller mid-call (the scheduling note told it to escalate). The
9B wrote those questions in the wrong role ("is Friday at 7:00 PM available?" to the caller), held
the line ~25 s and hung up. The user: 7:15 instead of 7 is acceptable almost always, and a time
outside the window shouldn't be negotiated live.

Decided:

- **A single acceptable time has a grace window of ±30 minutes** (`timewindow.Grace`): "Friday
  at 7pm" accepts 6:30–7:30. A range the caller gave ("Wed 1-5pm") stays exact.
- **An offer outside the window is not escalated.** The call asks whether the business has
  anything within the acceptable times; if not, it says it will check with the caller and call
  back, and hangs up. Each such offer is a call fact, so the result card names what the business
  offered ("offered Friday at 9:15, outside your acceptable times").
- Mid-call escalation stays for information the brief lacks (a callback number, a choice the
  brief doesn't cover).
