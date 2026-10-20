> Modified from pbakaus/impeccable@dad8dbb (Apache-2.0) for jarvis-server; changes are listed in DESIGN-SKILLS.md.

# Craft floor

Load this after the direction is settled, and build without announcing the checklist. An agreed brief or the committed visual world overrides anything here; your own habit does not. No design hook runs in this repo: the mechanical checks below are yours to make, and [audit.md](audit.md) is the full checklist you run before the PR.

## Verify

Each of these is a check on the built result, not an intention. Run them together in each batched look at the render, not as separate screenshot trips; the checks share one render. In this repo that look is `scripts/screenshots --sandbox <name>` (phone 390x844, tablet 820x1180, desktop 1440x900, light and dark, plus `index.json` with console errors, failed requests and horizontal overflow; the sandbox comes from `scripts/sandbox up --name <name> --ui`). Read the PNGs and `index.json`.

- **Contrast:** body and placeholder text ≥4.5:1, large text ≥3:1, in both the light and the `.dark` theme. On colored surfaces tint secondary text from that hue or the foreground; never gray.
- **Depth:** shadows carry an offset and a soft blur. A zero-offset colored halo is decoration.
- **Spacing:** tight groups, generous separation, more space above a heading than below it. Read the computed values.
- **Type:** body measure 65–75ch, display max 6rem, tracking floor -0.04em, balanced headings, obvious scale and weight steps. Run the real copy at every breakpoint and fix what overflows.
- **Motion:** one authored moment, not scattered effects and not one identical entrance on every section. Exponential ease-out from an already-visible default. Reach past transform and opacity: blur, backdrop-filter, clip-path, mask, and shadow belong to the palette when they stay smooth. Motion here is CSS and Tailwind only (no animation library), and every animation has a `prefers-reduced-motion` alternative.
- **States:** hover, disabled, loading, error, empty. Plus real content, working controls, responsive composition, keyboard focus.
- **Browser surfaces:** the parts you did not draw still carry the design. Text selection, the caret, custom scrollbars, focus rings, underline offset, and the numerals in tabular data all ship with browser defaults that belong to no design system. Theme them from the palette. This is the cheapest signal that a page was built rather than assembled, and the one models skip most reliably.
- **Copy:** the product's own language. Controls name their action; errors name the problem and the recovery.
- **Coverage:** every brief requirement present and findable within seconds.

## Refuse

These are the category's defaults, not bans: the brief's own words can earn any of them. Reaching for one when the axis is free means you were not deciding; recognizing that means rewriting the element, not softening it.

Page scaffolds:

- Same-size cards of icon plus heading plus text as the page structure. Cards are the lazy container; nested cards are always wrong.
- The hero-metric template: big number, small label, supporting stats, accent.
- A kicker or eyebrow above a heading. This one is a ban, not a default: no brief earns it back. The heading carries its own weight; delete the label and let the heading speak.
- Section numbers (01 / 02 / 03) unless the sequence itself carries information the reader needs.
- A modal for a task that needs neither interruption nor protected focus.

Surface habits:

- Gradient text. Emphasis comes from weight or size.
- Glass and blur as decoration rather than as a specific effect.
- A colored `border-left` or `border-right` above 1px on cards, list items, callouts, or alerts.
- Hard offset shadows (`box-shadow: 4px 4px 0`) outside a world that is actually neobrutalist. The zero-blur block shadow is a costume, not a depth system; a world that did not choose it never earns it as a default.
- Sparklines, progress rings, and soft-shadowed rounded rectangles standing in for content.
- Monospace as a costume for "technical" rather than for code, data, or measurement.
- A system display face (Impact, Arial Black) reached for as a costume display voice. In this repo the admin app uses the system font stack set in `web/admin/src/index.css` and no web fonts: hierarchy comes from size, weight and spacing in that stack, not from sourcing a new face.
- Unicode glyphs or emoji standing in for an icon system. Icons are drawn, in one consistent stroke and weight; here that means `lucide-react` only (no other icon library).
- Geometric masks standing in for organic contours. A circle, polygon, or radial-gradient cutout approximating a photographic subject's edge is the cheap version of the effect and reads worse than omitting it. Derive an alpha matte from the actual image, or produce a cut-out asset.
- Light or dark picked by category. This app ships both themes (the `.dark` class with the CSS variables in `web/admin/src/index.css`); design and check every surface in both.

The floor holds the mechanics; it never picks the direction. With every check green, spend the page on the committed world, and when torn between refined and committed, commit.
