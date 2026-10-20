> Modified from pbakaus/impeccable@dad8dbb (Apache-2.0) for jarvis-server; changes are listed in DESIGN-SKILLS.md.

# Command guidance

## Workflow questions

Give advice without executing commands; the menu below is only for bare invocations. Consult relevant command references as needed for prerequisites and scope. If the user also requests execution, follow that request.

## No-argument routing: the context-aware menu

Read this when the user invokes `/impeccable` with no argument. They are asking "what should I do?" Make the menu context-aware instead of static.

Gather the signals by reading, not by running tools: whether PRODUCT.md / DESIGN.md exist (they do not in this repo today; the system lives in `web/admin/src/index.css`, `web/admin/src/theme/` and the components), whether a critique of the surface already exists in this conversation or the PR you are on, and which files under `web/admin/src` the current branch changes (the session's git status, or the work you did in this conversation). Then lead with the **2-3 highest-value next commands**, each with a one-line reason pulled from those signals, followed by the full menu (the Commands table in SKILL.md, grouped by category). Leave out `live`, `generate`, `pin`, `hooks`, `doctor` and any native (iOS / Android) entry: they are not vendored here. **Never auto-run a command; the recommendation is a suggestion the user confirms.**

Reason over the signals; there is no score to obey:

- No DESIGN.md while the admin code exists → `document` (capture the visual system) is available, but do not lead with `init` or `document` over work the user is actually doing.
- No critique of the surface yet → for a real surface, offering `/impeccable critique <surface>` is a strong default.
- A critique with a low score or any P0 / P1 → `polish` (it reads that critique's Priority Issues as its backlog).
- Changed files pointing at one surface → scope `audit` or `polish` to those files specifically, naming them. A UI PR in this repo needs [audit.md](audit.md) before it opens, so `audit` is the default pick when there are changed UI files and no audit yet.
- Otherwise group by intent (build new / improve what's there), tailored to the current surface. The admin SPA is an Operate surface.

**If there are changed files under `web/admin/src`, read them once against [craft-floor.md](craft-floor.md)'s Refuse list and the anti-pattern list in [audit.md](audit.md)** before picking. Fold what you see into your picks: many contrast or quality problems → `audit` or `polish`; a specific slop family → the matching command (gradient text or eyebrows → `quieter` / `typeset`, flat or gray palette → `colorize`, and so on). If the change set is large, skip the read and recommend `audit`; never block the suggestion on it.

Keep it to 2-3 pointed picks with the exact command to type. The menu stays the fallback; the recommendation is the lede.
