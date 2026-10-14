# Decisions

One file per decision, so parallel branches never conflict over a shared table.

- **Name:** `YYYY-MM-DD-<slug>.md`: the date the decision was made and a few words of its title,
  lowercase with hyphens. `ls` lists them in order. If the name is taken, add `-2`.
- **Content:** a `# Title` line, then `- **Date:**` (and who decided, e.g. "user"), then the
  decision and its reason in plain prose. Link the plan or design doc it changes (links are
  relative to this folder: `../agent/DESIGN.md`). Say what it supersedes, if anything.
- **When:** whenever the user answers a design question or a choice is made that a later reader
  would otherwise have to rediscover. A decision is not a changelog entry: what was built goes
  in the commit and PR.
- **Changing one:** add a new file that supersedes it and say so in both; don't rewrite history.

Files dated up to 2026-10-14 were moved from the old STATUS.md decision-log table on that day.
