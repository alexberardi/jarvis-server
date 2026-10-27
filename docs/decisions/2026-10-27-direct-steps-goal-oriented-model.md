# Steered steps are direct; anything the model does is goal-oriented

Date: 2026-10-27. Decided by the user (VOICE-FLOWS.md Q32).

**Context.** Voice control of the desktop maps each request to one action. In real use the model
claimed actions it never took ("already scrolled", "already clicked") and needed seven calls and
two wrong pages for "open the second link". The question was whether to make every request
goal-oriented instead: a goal, the tools, and a time budget.

**Decision.** Split by who plans.
- When the user steers ("click 7", "scroll down", "switch to the Claude tab"), funnel stage 1
  acts at once with no model: the mouse-and-keyboard replacement must be fast and predictable.
- Whenever the model acts (stage 3), it works toward a goal in a short loop: act, observe the
  real state through the tools, retry within about 3 steps or 10 s, with progress on the island.
  Success is the observed state (URL, title, focused window, scroll position, field value),
  never the model's reply; a claimed action without a successful tool call isn't done.
- Delegated errands (stage 4, computer tasks) keep minute-scale budgets, show each step, can be
  interrupted, and ask before anything irreversible.

**Rejected.** A goal loop for everything: every step costs a model round trip and an observation,
errors compound over steps on local models, and it acts on its own reading of intent. A bare time
budget as the success test: it bounds cost but can't tell success from a claim.

**Consequences.** Tools report what changed, not only that they ran. The model turn needs a check
that a claimed action matches a successful call and a changed state, and a retry within budget.
