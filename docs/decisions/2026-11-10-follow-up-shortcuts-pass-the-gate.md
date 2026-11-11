# Follow-up shortcuts pass command-center's gate first, except steering steps

- **Date:** 2026-11-10 (user)

Funnel stage 1 ([VOICE-FLOWS.md](../agent/VOICE-FLOWS.md) Q27) ran before command-center's
follow-up gate, so in the wake-free window after Jarvis spoke, room talk that parsed as a
command acted at once. The never-direct corpus (`internal/agent/node/nodetest`) found "good
night everyone" running the good-night routine, "turn it down a notch, you're yelling"
lowering the volume, and "type two diabetes runs in our family" typed into the window in front.

Decision: a follow-up heard right after a spoken reply runs on the node only if it is a
**steering** step (`node.SteeringDirector`): scrolling, or switching to a tab or window. Those
change nothing and are seen at once. Everything else said then (typing, keys, routines, volume,
timers, clicks, web searches, media) goes to command-center, whose gate asks whether the speech
was for Jarvis before any tool runs. That costs one round trip on such follow-ups.

Unchanged: the woken turn, and the steps of a process said one after another with no reply
between them (a silent step's window), stay on the node: the user is steering.

Also decided: a "click …" that names nothing on screen ("click your seatbelt in") may still be
taken by the node with "I couldn't find that control"; Jarvis telling such room talk apart
("on the microwave") is a later improvement. The corpus tests check that nothing acts.
