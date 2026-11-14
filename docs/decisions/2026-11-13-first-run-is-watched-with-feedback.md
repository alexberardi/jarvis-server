# A new workflow's first run is watched, with a place to say what went wrong

- **Date:** 2026-11-13 (user; verified-replay Q3 of [WORKFLOW-SKILLS.md](../agent/WORKFLOW-SKILLS.md))

Until a workflow has completed one verified run it is unproven: it runs only when its owner
starts it and is at the computer (the attended check computer control already has), never from
a schedule or a request made while away, and it pauses before its first risky step to show what
it is about to do. A run that finishes with the end state confirmed marks it proven; a large
edit or a Relearn makes it unproven again. A failed first run leaves it unproven, with what
went wrong shown on its page.

The user added a revision loop: "some sort of wizard/revision loop where the user watches the
flow and has a text box to say what went wrong, where it failed, etc." So the watched run has a
place for the owner to write what went wrong, and that feeds a revision of the workflow, which
is then run again. What the written feedback does (Q3a) is asked separately.

Rejected: no special first run (the first unattended run of a misread task could be a scheduled
one); every run supervised until switched to unattended (a setting to remember, and a workflow
that ran correctly twenty times treated like a new one).
