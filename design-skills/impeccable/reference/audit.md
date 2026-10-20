> Modified from pbakaus/impeccable@dad8dbb (Apache-2.0) for jarvis-server; changes are listed in DESIGN-SKILLS.md.

Run systematic **technical** quality checks and generate a comprehensive report. Don't fix issues; document them for other commands to address. (When you run this as the pre-PR audit on your own change, fix the P0/P1 findings before opening the PR and report what remains.)

This is a code-level audit, not a design critique. Check what's measurable and verifiable in the implementation.

**Web only.** This repo's UI is the admin SPA in `web/admin/` (React 19 + Vite + Tailwind 4 + `lucide-react`, light and dark themes via the `.dark` class and the `--color-*` variables in `web/admin/src/index.css`, system font stack, phones first). Upstream's audit runs a bundled detector; it is not vendored here, so every check below is one you make by reading the files and the rendered screenshots.

## 0. Scope and render

1. **Scope.** The changed files under `web/admin/src` on this branch (components, pages, CSS), plus the pages that render them. Read each changed file in full, and the shared components it uses. Note which pages (by the sidebar's slug) the change shows up on, and which roles see them.
2. **Render.** Start a sandbox with the SPA built and capture the affected pages:

   ```
   scripts/sandbox up --name <name> --ui
   scripts/screenshots --sandbox <name> --pages <page,page>
   ```

   That gives full-page PNGs at phone 390x844, tablet 820x1180 and desktop 1440x900, each in light and dark, plus `index.json`. Add `--user member` (or `admin`, `power`) when the change is on a page non-superusers see, and `--tiles 1600` when phone pages are long enough to be unreadable as one image. Pages such as `nav-drawer`, `login` or `not-found` are named states too. Read **every** PNG for the affected pages (6 per page) and read `index.json`: console and page errors, failed requests, HTTP >= 400, and `overflow` (how far a page scrolls sideways, and which elements stick out).
3. **States the screenshots cannot reach.** Hover, focus, open menus, loading, error and empty states usually do not appear in a default capture. Check them in the code (the classes and branches that render them), and say in the report which ones you verified only in code.

If the sandbox cannot start, do the source checks anyway and say in the report that the rendered check did not run and why. A source-only audit is never reported as a full one.

## Diagnostic Scan

Run comprehensive checks across 5 dimensions. Score each dimension 0-4 using the criteria below. Every item is a check you make; record a finding with its file and line, or a screenshot name.

### 1. Accessibility (A11y)

**Check for**:
- **Contrast issues**: Text contrast ratios < 4.5:1 (3:1 for large text, 18.66px bold / 24px regular and up; 3:1 for icons, focus rings and control borders that carry meaning), in **both themes**. Work out the pair from the code: the text class against the background it actually sits on. Token pairs as of this writing (recompute if `index.css` changed):
  - Light: `--color-text` passes everywhere. `--color-text-muted` (#64748b) is 4.76 on surface and 4.55 on background, but **4.34 on `--color-surface-alt`: fails for body text**. `--color-primary` (#8A00C4) is 7.3 on surface; white on primary is 7.3. `--color-secondary` (#22c55e, 2.3) and `--color-tertiary` (#0891b2, 3.4–3.7) **fail as text** on any light background.
  - Dark: text, muted, primary, secondary and tertiary all pass on background, surface and surface-alt (lowest: muted on surface-alt 5.7). **White on `--color-primary` (#c084fc) is 2.64: fails.** Text on a dark primary fill needs a dark ink such as `--color-background` (6.8).
  - `--color-border` against the surface is 1.2 (light) / 1.8 (dark): fine for dividers, not enough on its own to mark an input or control boundary.
  - Tailwind palette classes (`text-gray-400`, `text-red-500`, `bg-amber-100` ...) and opacity modifiers (`/50`, `opacity-60`) are where contrast fails most: check each one in both themes, and check that each has a `dark:` counterpart where needed.
- **Motion sensitivity**: `prefers-reduced-motion` needs an intentional alternative that preserves state change and hierarchy (Tailwind `motion-safe:` / `motion-reduce:` variants or a `@media (prefers-reduced-motion: reduce)` block); flag a global `0.01ms` kill that destroys useful feedback, flashing above threshold, and motion that blocks focus, reading, or task completion. List every `transition`, `animate-*`, `@keyframes` and JS-driven animation the change adds.
- **Missing ARIA**: Interactive elements without proper roles, labels, or states. Icon-only buttons (a lucide icon alone) need an `aria-label`; toggles need `role="switch"` / `aria-checked` or `aria-pressed`; disclosure needs `aria-expanded`; the current nav item needs `aria-current`; live status (toasts, saving, errors) needs to be announced.
- **Keyboard navigation**: Missing focus indicators, illogical tab order, keyboard traps. Flag `outline-none` / `focus:outline-none` with no `focus-visible:` ring in its place, click handlers on `div` / `span` with no keyboard path, dialogs and drawers that do not move focus in, trap it, close on Esc and return it.
- **Semantic HTML**: Improper heading hierarchy (skipped levels), missing landmarks, divs instead of buttons, links that act as buttons and buttons that navigate.
- **Alt text**: Missing or poor image descriptions; decorative icons not hidden (`aria-hidden`).
- **Form issues**: Inputs without labels (a placeholder is not a label), poor error messaging, missing required indicators, errors not tied to their field (`aria-describedby`, `aria-invalid`).

**Score 0-4**: 0=Inaccessible (fails WCAG A), 1=Major gaps (few ARIA labels, no keyboard nav), 2=Partial (some a11y effort, significant gaps), 3=Good (WCAG AA mostly met, minor gaps), 4=Excellent (WCAG AA fully met, approaches AAA)

### 2. Performance

**Check for**:
- **Layout thrashing**: Reading/writing layout properties in loops
- **Expensive animations**: Casual layout-property animation (width, height, top, left, margin), unbounded blur/filter/shadow effects, or effects that visibly drop frames
- **Missing optimization**: Images without lazy loading or explicit dimensions, unoptimized assets
- **will-change overuse**: `will-change` applied broadly or left on at rest (it is a targeted hint for known expensive animations, not a baseline requirement)
- **Bundle size**: Unnecessary imports, unused dependencies. Any new entry in `web/admin/package.json` is a finding: this repo adds no icon, font, animation or component libraries. Import lucide icons by name, never the whole set.
- **Render performance**: Unnecessary re-renders, missing memoization where a list or a hot path re-renders on every keystroke or poll, effects with missing or unstable dependencies, polling or refetching more often than the data changes, long lists rendered without bound

**Score 0-4**: 0=Severe issues (layout thrash, unoptimized everything), 1=Major problems (no lazy loading, expensive animations), 2=Partial (some optimization, gaps remain), 3=Good (mostly optimized, minor improvements possible), 4=Excellent (fast, lean, well-optimized)

### 3. Theming

**Check for**:
- **Hard-coded colors**: Colors not using design tokens: hex / rgb literals in classes or styles, `bg-white`, `text-black`, Tailwind palette classes for surfaces and text where a `var(--color-*)` exists. Status colors (success, warning, danger) from the Tailwind palette are an accepted convention here only when they carry a `dark:` variant.
- **Broken dark mode**: Missing dark mode variants, poor contrast in dark theme. Compare the light and dark PNG of every affected page side by side.
- **Inconsistent tokens**: Using wrong tokens (`--color-text-muted` for a primary label, `--color-surface` for a page background), mixing token types
- **Theme switching issues**: Values that don't update on theme change (colors computed once in JS, inline styles from a theme read at mount)

**Score 0-4**: 0=No theming (hard-coded everything), 1=Minimal tokens (mostly hard-coded), 2=Partial (tokens exist but inconsistently used), 3=Good (tokens used, minor hard-coded values), 4=Excellent (full token system, dark mode works perfectly)

### 4. Responsive Design

**Check for**:
- **Fixed widths**: Hard-coded widths that break on mobile (`w-[600px]`, `min-w-*` on content, fixed grid column counts with no breakpoint)
- **Touch targets**: Interactive elements too small to hit. The base layer in `index.css` raises `button`, `select`, text `input` and `[role="button"]` to a 2.5rem (40px) floor under 48rem or on coarse pointers (icon-only buttons get the width too). Flag what it does not cover: links and clickable `div`s used as controls, checkboxes and radios without a large label, `role="switch"` toggles, controls whose own classes undercut the floor, and targets packed closer than 8px. 44x44px is the comfortable target; under 24x24px fails WCAG 2.2 AA.
- **Broken touch interaction**: Custom sliders, drag surfaces, and scrollable control strips whose primary gesture fails under touch, that swallow page scroll or lose the drag to it, or that stay stuck after an interrupted gesture. Code tells: mouse-only handlers, no `touch-action` on a pointer-event drag surface, drag state that nothing clears on cancel, lost capture, or blur. Screenshots prove layout, not the gesture: say what produced the evidence (code reading, rendered viewport) and what stayed untested.
- **Horizontal scroll**: Content overflow on narrow viewports. Any non-zero `overflow` in `index.json` at phone width is a finding; tables and code blocks need their own `overflow-x-auto` container instead of widening the page.
- **Text scaling**: Layouts that break when text size increases (fixed heights on text containers, truncation that hides the only copy of a value)
- **Missing breakpoints**: No phone/tablet variants. Phones come first here: the phone PNG is the primary layout, not a squeezed desktop. Check the tablet width too, where desktop sidebars and phone drawers meet.

**Score 0-4**: 0=Desktop-only (breaks on mobile), 1=Major issues (some breakpoints, many failures), 2=Partial (works on mobile, rough edges), 3=Good (responsive, minor touch target or overflow issues), 4=Excellent (fluid, all viewports, proper touch targets, gestures work under touch)

### 5. Implementation Integrity (CRITICAL)

Read the changed files and the screenshots against the anti-pattern list below and against [craft-floor.md](craft-floor.md)'s Refuse list, and verify each hit in context. Look for repeated implementation shortcuts, design-system drift (a new spacing, radius or type size off the existing scale; a hand-rolled control where a shared component exists), misleading or decorative content, and structure that is interchangeable with an unrelated product. Keep checklist hits separate from visual judgment and call out the ones that are justified in context.

Anti-patterns to look for (the list upstream's detector checks):

- **Side-tab accent**: a thick colored border on one side of a rounded card, list item or alert (`border-l-4` and the like).
- **Gradient text** (`bg-clip-text text-transparent` with a gradient).
- **AI color palette**: purple/violet gradients and cyan-on-dark glow. The brand primary here is purple, so flat `var(--color-primary)` is the system, not the tell; a purple-to-anything gradient still is.
- **Nested cards**: bordered or shadowed containers inside bordered or shadowed containers.
- **Icon tile above a heading**: a small rounded square holding an icon, stacked over a title, repeated as the layout.
- **Kicker / eyebrow** above a heading (tiny tracked uppercase label), and tiny numbered section labels.
- **Hero-metric template**: big number, small label, supporting stats, accent.
- **Glow and halo**: zero-offset colored shadows, colored blurred shadows on dark, radial-gradient spotlights or halos, hairline border plus wide diffuse shadow on every card, decorative grid-line or stripe backgrounds, glass blur as decoration.
- **Bounce or elastic easing**; pulsing status dots that are not tied to live data; decorative blinking cursors; auto-scrolling marquees; content invisible at rest waiting for a reveal.
- **Type problems**: flat hierarchy (heading and body sizes under 1.25x apart), body text under 14px or functional UI text under 11px (`text-[10px]`), line height under 1.3 on multi-line text, lines over ~80 characters, all-caps or widely tracked body text, crushed letter spacing, skipped heading levels, unbalanced or orphaned headings where `text-wrap: balance` / `pretty` would fix them.
- **Spacing problems**: one spacing value everywhere, text cramped against its container's edge, body text touching the viewport edge on phones, a heading closer to the block above than to its own content.
- **Overflow and clipping**: content wider than its container, tooltips and menus cut off by an `overflow-hidden` ancestor, text under an overlapping element, cards flush against a scroller edge.
- **Copy tells**: marketing buzzwords (streamline, empower, supercharge, world-class, seamless), "X. No Y." aphorisms, em-dash saturation, dismissing something as "theater".
- **Broken or placeholder images**, uncaught script errors on load (from `index.json`).
- **Off-system choices**: an icon that is not from `lucide-react`, emoji or Unicode glyphs used as icons, a web font or font import, a new animation or component library.

**Score 0-4**: 0=systemic drift, 1=major repeated failures, 2=several verified issues, 3=minor isolated issues, 4=coherent and intentional

## Generate Report

### Audit Health Score

| # | Dimension | Score | Key Finding |
|---|-----------|-------|-------------|
| 1 | Accessibility | ? | [most critical a11y issue or "--"] |
| 2 | Performance | ? | |
| 3 | Theming | ? | |
| 4 | Responsive Design | ? | |
| 5 | Implementation Integrity | ? | |
| **Total** | | **??/20** | **[Rating band]** |

**Rating bands**: 18-20 Excellent (minor polish), 14-17 Good (address weak dimensions), 10-13 Acceptable (significant work needed), 6-9 Poor (major overhaul), 0-5 Critical (fundamental issues)

### Implementation Integrity Verdict
**Start here.** Pass/fail: does the implementation express a coherent product-specific system? Cite verified evidence and checklist hits.

### Executive Summary
- Audit Health Score: **??/20** ([rating band])
- Total issues found (count by severity: P0/P1/P2/P3)
- Top 3-5 critical issues
- Recommended next steps

### Detailed Findings by Severity

Tag every issue with **P0-P3 severity**:
- **P0 Blocking**: Prevents task completion. Fix immediately
- **P1 Major**: Significant difficulty or WCAG AA violation. Fix before release
- **P2 Minor**: Annoyance, workaround exists. Fix in next pass
- **P3 Polish**: Nice-to-fix, no real user impact. Fix if time permits

For each issue, document:
- **[P?] Issue name**
- **Location**: Component, file, line (and the screenshot, e.g. `chat__phone__dark.png`, when the evidence is rendered)
- **Category**: Accessibility / Performance / Theming / Responsive / Implementation Integrity
- **Impact**: How it affects users
- **WCAG/Standard**: Which standard it violates (if applicable)
- **Recommendation**: How to fix it
- **Suggested command**: Which command to use (prefer: /impeccable adapt, /impeccable animate, /impeccable audit, /impeccable bolder, /impeccable clarify, /impeccable colorize, /impeccable critique, /impeccable delight, /impeccable distill, /impeccable document, /impeccable harden, /impeccable layout, /impeccable onboard, /impeccable optimize, /impeccable overdrive, /impeccable polish, /impeccable quieter, /impeccable shape, /impeccable typeset)

Issues that were already there before this change are reported separately from the ones it introduces; an existing problem is not a reason to fail the change, and not a reason to hide it either.

### Patterns & Systemic Issues

Identify recurring problems that indicate systemic gaps rather than one-off mistakes:
- "Hard-coded colors appear in 15+ components, should use design tokens"
- "Touch targets consistently too small (<44px) throughout mobile experience"

### Positive Findings

Note what's working well: good practices to maintain and replicate.

## Recommended Actions

List recommended commands in priority order (P0 first, then P1, then P2):

1. **[P?] `/command-name`**: Brief description (specific context from audit findings)
2. **[P?] `/command-name`**: Brief description (specific context)

**Rules**: Only recommend commands from: /impeccable adapt, /impeccable animate, /impeccable audit, /impeccable bolder, /impeccable clarify, /impeccable colorize, /impeccable critique, /impeccable delight, /impeccable distill, /impeccable document, /impeccable harden, /impeccable layout, /impeccable onboard, /impeccable optimize, /impeccable overdrive, /impeccable polish, /impeccable quieter, /impeccable shape, /impeccable typeset. Map findings to the most appropriate command. End with `/impeccable polish` as the final step if any fixes were recommended.

After presenting the summary, tell the user:

> You can ask me to run these one at a time, all at once, or in any order you prefer.
>
> Re-run `/impeccable audit` after fixes to see your score improve.

Keep P3s few: a long tail of minor issues buries the ones that matter.

## PR findings block

When the audit is the pre-PR check on your own change, end with this short block and put it in the PR description, after fixing what you could. Keep it under about 25 lines; the full report stays in the conversation.

```
### UI audit (impeccable audit.md)
Scope: <changed files under web/admin/src>; pages: <slugs>; roles: <users>
Rendered: scripts/screenshots --sandbox <name> --pages <...>  (or: not run, <reason>)
index.json: console errors <n>, failed requests <n>, overflow <none | page@viewport: Npx>
Score: A11y ?/4 · Perf ?/4 · Theming ?/4 · Responsive ?/4 · Integrity ?/4 = ??/20
Checked in both themes at 390 / 820 / 1440: <yes | which not>
Verified in code only: <hover / focus / loading / error / empty states not captured>
Fixed in this PR: <one line each>
Remaining: [P?] <issue> (<file:line>), one line each, or "none"
Pre-existing, not touched: <one line each, or "none">
```

**Avoid**:
- Report issues without explaining impact (why does this matter?)
- Provide generic recommendations (be specific and actionable)
- Skip positive findings (celebrate what works)
- Forget to prioritize (everything can't be P0)
- Report findings you did not verify in the code or the screenshots
- Claim a theme, width or state was checked when only its code was read
