# A workflow's prerequisites are the starting point of its recording

- **Date:** 2026-11-12 (user; Q5 of [WORKFLOW-SKILLS.md](../agent/WORKFLOW-SKILLS.md))

A workflow lists what must be true before step 1, and a run tries to achieve each unmet one
before it starts (user: "file abc is open ... it should try to achieve those before starting").
The list comes from where the recording began (user: "the starting point of the workflow's
recording. What's open. What's logged in.").

- The recorder keeps the state when recording starts: the open apps and windows, the file or
  page showing, and whether the sites the steps use were signed in.
- The learner turns that into prerequisites, only for the apps, files and pages the steps act on,
  so an unrelated open window isn't one.
- The owner keeps, drops or adds prerequisites on the workflow page.

An empty list the owner fills in was rejected: a workflow nobody annotates would start blind.
