# Home rules: conditions translated at save time, three features on one engine

- **Date:** 2026-11-23 (user)

Autonomy P4's home-state watches become **rules**: a household admin writes several plain-words
conditions that must all hold, plus one or more results ("garage open, no one home, heat on →
turn off the heat"). The model translates each line into a fixed check or action **when the rule
is saved**, in two steps (list the candidate devices, then write the check). The person
confirms the result as editable rows, so checking and running a rule needs no model.
Rules, watches and signal automations stay separate features on one shared engine. The full
answers (Q1–Q10), the translation eval on Qwen 3.5 9B and the build plan are in
[HOME-WATCHES.md](../autonomy/HOME-WATCHES.md).

This extends [the autonomy plan](../autonomy/PLAN.md)'s P4. Its "fuzzy LLM conditions" are not
part of v1: a model judging conditions at fire time was rejected in favour of save-time
translation.
