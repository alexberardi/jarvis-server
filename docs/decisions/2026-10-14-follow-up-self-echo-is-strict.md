# The follow-up window's self-echo check is strict

- **Date:** 2026-10-14 (agent, after a user report)

The follow-up window drops an answer as "the mic heard our own reply" only when nearly all of
it is the reply: at least 90 % of the answer's words (stopwords included) are the reply's words
**in the reply's order**, and they cover at least half of the reply. A one-word answer is never
an echo, and only an answer heard right after we spoke is checked (after an ignored answer
nothing was said).

The Python node's rule (85 % of the answer's distinct non-stopwords appear anywhere in the
reply) dropped a real request: after "Got it. I've opened Omawrite for you. Now, type \"hello
there\" to get started.", the user's "Can you type hello there?" was closed as an echo. A missed
echo costs little, because command-center's follow-up gate still rejects speech that isn't for
Jarvis; a dropped request loses what the user asked.

Changes the filter row in [agent/go-pi-node.md](../agent/go-pi-node.md) (§3.9, the follow-up filters).
