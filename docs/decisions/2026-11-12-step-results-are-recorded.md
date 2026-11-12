# The recorder captures each step's result; the owner can write each step's goal

- **Date:** 2026-11-12 (user; Q3 of [WORKFLOW-SKILLS.md](../agent/WORKFLOW-SKILLS.md))

A run verifies every step, so a workflow must know what each step causes. A recording held only
what the user did: a desktop keyframe is taken at the click, so it shows the screen before the
action, and a browser recording has no screenshots.

- **The recorder keeps the result of each action** once it has settled: the window title, the
  page address and the element list (accessibility tree or page), and a screenshot where there is
  no tree. Step expectations are learnt from that, in the same kind of data a run observes.
- **Fallback:** a recording without results (an older one, or a step the capture missed) gets its
  expectation inferred from what the next step acted on.
- **Every step has a goal the owner can enter or correct** on the workflow page (user: "a goal
  property per step that the user can enter the point of each step if needed"). It is learnt
  first and optional to edit.

Inferring only was rejected: correctness is the point, and an inferred expectation is a guess.
Cost: an agent release and an extension release (store review) before browser recordings have
results.
