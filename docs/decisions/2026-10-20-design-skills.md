# Design skills for UI work: Emil's design-eng, Impeccable and Taste

- **Date:** 2026-10-20 (user, item #330)

Every agent doing UI work in `web/admin/` loads three vendored third-party Claude Code skills
and runs Impeccable's audit checklist before its PR (`../../web/admin/CLAUDE.md`):
`emil-design-eng` (emilkowalski/skill, MIT), `impeccable` (pbakaus/impeccable, Apache-2.0) and
`design-taste-frontend` (Leonxlnx/taste-skill-classics `taste-skill-v1`, MIT). Each is pinned to
a commit and stripped of anything that runs commands, fetches URLs, installs packages, changes
settings or sends data; the sources, the removals and the precedence between them are in
`.claude/skills/DESIGN-SKILLS.md`.

- **Taste is v1 from taste-skill-classics, not the current taste-skill** (user): current
  `design-taste-frontend` (v2.1.3) dropped the dials and became a script-driven marketing-site
  skill; v1 keeps the dials at a quarter of v2-experimental's size. Dials: variance 3, motion 2,
  density 5, for an admin and member app.
- **Precedence** (user): accessibility and contrast first, then Impeccable's system rules,
  Emil's rules for motion, and Taste for layout feel.
- **Impeccable's audit is a manual checklist** over our files plus `scripts/screenshots`,
  because upstream's audit runs a detector binary its launcher downloads.
- **Staged first** (user): the build sandbox cannot write `.claude/skills/`, so the PR adds the
  skills under `design-skills/` and they are moved to `.claude/skills/` by one `git mv` before it
  merges. jarvis-docs gets the same files in a follow-up item.
