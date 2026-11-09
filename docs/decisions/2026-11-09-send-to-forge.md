# Send to Forge sends the install result and the command's runs

- **Date:** 2026-11-09 (user)

After a Forge test install, the developer can send what happened back to the draft so the
Forge's model can find the bug. The user chose the content: the install result **plus** each run
of the draft's commands during the node's test window (the arguments Jarvis passed, the command's
output or error) and an optional note, because that gives the model the full context of where a
bug occurs. An install result and a note alone were the smaller alternative.

It is sent only by an explicit **Send to Forge** button after a preview, never automatically
(decided 2026-11-08). Automatic iteration (results feeding the next Forge revision by
themselves) is the stated future goal, so the report is a plain structured record a later loop
can read.

Only the draft's own commands on the node that installed it are recorded, for the node's
20-minute test window, at most 20 runs of up to 8 KB each. Traces keep leaving outputs out
(continue.go `toolResultsMeta`): a test command's output is the developer's own test data, kept
with the request row and swept with it, and leaves jarvisd only when they send it.
