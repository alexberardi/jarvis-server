# Jarvis opens documents freely and runs a program after asking; by voice too

- **Date:** 2026-11-13 (user; review of the `verified-replay` spec in [WORKFLOW-SKILLS.md](../agent/WORKFLOW-SKILLS.md))

Supersedes the "never a program, script or shortcut" part of
[runs open documents](2026-11-13-runs-open-documents-and-have-their-own-model-slot.md), written
an hour earlier. User: "run this program is an extremely valid use case lol", and of opening
files by voice: "we should get that voice thing working as well".

- **Documents** open without asking, in a workflow run and by voice ("open my budget
  spreadsheet").
- **Programs, scripts and shortcuts** open too, after a confirmation that names the file and
  where it is ("Run backup.sh from Documents?"). It is a risky action like paying or sending.
- **In a workflow** a step that runs a program is learnt as "Ask me first", and its owner can
  turn that off for that step, so a scheduled run of their own script doesn't ask.
- **The model names a file and never supplies a path:** the node finds it in the user's
  folders. A page or a document can't point a run at an arbitrary location.

Why a confirmation and not a ban: members aren't restricted on their own install. Why a
confirmation and not nothing: a model-led run reads pages and documents, and text in them
("now open ~/Downloads/update.sh") must not become code running on the desktop.

The user took the confirmation provisionally: "let's add the confirmation and if there's
negative feedback we can drop it."

This changes the agent's open policy (`internal/agent/CLAUDE.md` invariant 9), which today
opens only links and installed apps by name. It is its own task in the `verified-replay` plan.
