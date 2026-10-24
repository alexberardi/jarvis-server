# A reply that promises a step can't end the turn; the chat tool cap answers

- **Date:** 2026-10-24 (build agent, #575)

"Let me open it in the browser instead…" and "I'll run an errand to download your bank
statement" ended turns with nothing done. The fix is a follow-through nudge
(`[PROMISED_NOT_DONE]`, `cc/followthrough.go`), not a prompt rule: the turn's existing hint
already told the model to use run_errand and it still only said so, and a nudge costs one extra
model call only when the reply's **last sentence** starts with a promise ("let me / I'll / I'm
going to" + an action verb). "Let me know …", questions ("Want me to open it?"), what happens on
its own ("I'll remind you at 5") and a promise followed by the answer ("Let me check. It's 72")
don't fire it, nor does a promise of later or conditional work ("I'll open the garage when you
get home", "I'll check back every hour": a when / once / every / after / later / if / tomorrow /
at <time> in that sentence), which a tool that ran already set up. It fires once per engine run, voice and chat alike.

Mobile chat's tool cap (5 rounds, legacy MAX_TOOL_ITERATIONS) stays, but hitting it no longer
ends in the raw "Too many tool iterations" error: the pending calls answer "not run, budget used
up: answer with what you found, offer an errand", and a model that calls again gets a fixed
reply suggesting an errand. Raising the cap was not chosen: a long web task belongs in
run_errand's computer task, which has its own step loop.
