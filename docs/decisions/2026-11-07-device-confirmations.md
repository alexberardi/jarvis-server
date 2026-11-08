# A device command that worked is confirmed in a fixed sentence

- **Date:** 2026-11-07 (user)

Timing a kitchen Pi's turns (the agent's "voice timing" line) showed that after `control_device`
succeeds, the server asks the live model a second time just to word "OK, the living room
lights are on": ~0.6-0.8 s of every light command. The user: "let's just do a simple 'turned
on/off X device' or 'turned {light} blue'". They're unsure a spoken confirmation is needed for
every device, but want to keep one for now.

Decided:

- **When every call in the round was `control_device` and every one succeeded,** the continue
  answers with a fixed sentence per action ("Turned on the living room lights.", "Locked the
  front door.", "Set the bedroom lamp to 40 percent.", "Turned the play room lights blue.")
  and ends the exchange. No model call; the history keeps the sentence.
- **The model still answers** when a command failed, another tool is in the round, the request
  also asked something ("…and what's the time?"), or the action or value isn't one the phrases
  cover (an `r,g,b` colour, a word for a temperature).
- The confirmation stays spoken. Dropping it (a chime, or silence for some devices) is a later
  choice.
