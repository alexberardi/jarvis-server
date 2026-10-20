# web/admin (the admin and member SPA)

React 19 + Vite + Tailwind 4 + lucide-react, embedded in jarvisd (`embed.go`). Light and dark
themes (a `.dark` class and the CSS variables in `src/index.css`, `src/theme/`), phones first.
Read the root `CLAUDE.md` first; this file adds the rules for UI work.

## UI work loads the design skills

Any change to how a page or component looks or behaves (layout, styling, copy, states, motion)
loads these three skills from `.claude/skills/` before the first edit:

- **impeccable**: the design system rules, the commands (`shape`, `critique`, `polish`,
  `harden`, `clarify`, `layout`, ...) and the audit checklist.
- **emil-design-eng**: component craft and every animation decision (what animates, easing,
  under 300 ms, `prefers-reduced-motion`).
- **design-taste-frontend**: layout, type and spacing feel, with its dials set for this app
  (variance 3, motion 2, density 5).

Where they disagree, `.claude/skills/DESIGN-SKILLS.md` decides: accessibility and contrast
first, then Impeccable's system rules, Emil's rules for motion, and Taste for layout feel. It
also says where each skill comes from and what was changed in it.

Do not add icon, font, animation or component libraries as part of design work.

## Before a UI PR

1. Run Impeccable's audit (`.claude/skills/impeccable/reference/audit.md`) over the changed
   pages and components, with `scripts/screenshots --sandbox <name>` for the rendered check
   (phone, tablet and desktop in light and dark), and fix what it finds.
2. Put the audit's findings report in the PR body, with the screenshots it looked at as
   evidence, and list anything it could not check under "not verified".
3. Then the usual gate (`land-pr`): `npm test`, `npm run build` and `scripts/check`.
