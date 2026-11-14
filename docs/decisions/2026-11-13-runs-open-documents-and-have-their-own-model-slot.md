# A run may open a document; running a workflow gets its own model slot

- **Date:** 2026-11-13 (user; review of the `verified-replay` spec in [WORKFLOW-SKILLS.md](../agent/WORKFLOW-SKILLS.md))

**Opening files.** The spec listed "open a file found by name" under ask-first, because the
agent's open policy takes no path from the model (`internal/agent/CLAUDE.md` invariant 9: a
path the OS opens can be a program). User: "i'm not sure why we'd block files from opening
anyway? that seems strange." So a run opens a document to meet a file prerequisite as an
ordinary part of running, with no special approval. What stays is the reason behind the old
rule: the node opens only document types (the same extensions learning recognises in a window
title), never a program, script or shortcut, and the model names a file, never a path: the node
finds it. Whether `desktop_open` itself should take documents outside a workflow run is a
separate change, not decided here.

**Cost.** One more model call for a step with a screen expectation is accepted (user: "that's
fine, especially if we can get this working on local models well").

**Which model.** User: "we should put this behind the new broken out section in the models page
on the admin panel where you can select which model you want per feature." Running a workflow
gets its own entry in the Models page's per-feature section (a slot setting beside
`workflows.model`, which stays the model that learns and revises). Its auto is what a computer
task uses today. The computer suite gates on qwen3.5-9b; a smaller model is the owner's choice
there, and what it does is reported, not promised.
