# A workflow's skill is structured fields, not a document

- **Date:** 2026-11-12 (user; Q2 of [WORKFLOW-SKILLS.md](../agent/WORKFLOW-SKILLS.md))

What a taught workflow knows about its task (goal, when to use it, prerequisites, each step's
purpose and expected result, notes) is stored as separate fields. The workflow page and the
model's prompt render them as one readable whole.

A free-text skill file per workflow was rejected (user: "I don't want another readme based
approach. I want something structured"). The server must be able to check one prerequisite or one
step's result without a model re-reading prose, and a step must stay tied to its recorded element
and its risk whatever the owner edits.
