# Follow-through guards after a prose reply

- **Date:** 2026-10-15 (agent, from the user's desktop agent test pass)

A prose reply that leaves the request half done gets one targeted nudge, in addition to the
legacy must-call guard: a memory request answered "noted" without `remember`, "not in my memory"
without `recall`, and "open X and type Y" without the typing. Each fires at most once per engine
run and is scrubbed from history like the other nags. The nudges name tools and quote the user's
own words, never the computer's app names (the catalogue never reaches the LLM). Reason: on the
dev server a desktop agent's turn said "Parking spot B12 noted." with nothing saved, and told the
user to type the words they had asked Jarvis to type. Spec: [02 §3.2](../cc/02-tool-loop.md).
