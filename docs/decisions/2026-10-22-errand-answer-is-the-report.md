# An errand's answer is its report, not a step

- **Date:** 2026-10-22 (agent, #452; open to the user's review)

#452 asked that an errand which asks a question "end with an answer step that delivers it". There is
no separate answer step: the finished errand's composed report (`ComposePrompt`, since #232) is
already what reaches the user, on the ran or completion card, with the facts the steps found. A
step for it would add nothing to deliver and would be one more step to check for autorun.

What changed instead:

- The planner prompt gets `answerRule` when the menu has `quick_search` or `remember`: a question's
  plan is its lookup step(s), the facts the user asked for (prices, ratings, times) go in the
  search query, and `remember` is only for goals that ask to remember, save or note something.
- `dropUnaskedRemember` removes `remember` steps from a plan (and a replan) whose goal has no
  memory verb, because Qwen3.5-9B adds one despite rules. A plan of nothing else keeps them. This
  is what lets a lookup-only plan autorun: `remember` asks, `quick_search` does not.
- The compose prompt's EXCEPTION says a save of the result is not a reason to report it unsent.

Refine (`RefinePlan`) is left alone: a remember step the user adds while editing a plan is theirs.
Routing (`deep_research` vs an errand or `quick_search`) needed no change on the phrasings tested
(`tools/pipeline/scenarios/errands.json`); "research … in depth" may stay `deep_research`.
