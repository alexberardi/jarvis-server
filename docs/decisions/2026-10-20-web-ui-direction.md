# Web UI visual direction: B · Quiet Violet (keep the purple)

- **Date:** 2026-10-20 (user, item #331)

The overhaul of the admin/member web app, the desktop agent's first-run page and the browser
extension's popup starts from **direction B, Quiet Violet**, from the design audit
(`../design/AUDIT-2026-10.md`, specs and mockups on `../design/directions/directions.html`).

- **Why B** (user: "i'd like to keep the purple if possible"): B is the only one of the three
  directions that keeps the brand purple, `#8A00C4` in light and `#c084fc` in dark. It is
  possible: every one of B's colour pairs passes WCAG AA in both themes
  (`../design/directions/contrast.md`). A (Hearth, clay) and C (Ledger, ink and teal) replaced the
  purple and are not taken.
- **What B fixes:** dark mode puts dark text on the light purple (`on-primary` `#1d0b2b`, not
  white). Purple is kept for actions, selection and focus only, and neutral greys do the rest.
- **What the overhaul items use:** B's tokens from `../design/directions/tokens.py`: colours light
  and dark, including status, `on-primary`, `border-strong` and focus; `system-ui` at 15px body
  on the 12-24px scale; spacing 4-40; radius 8 / 12 / 18; 44px rows; and B's motion rules,
  including reduced motion. The same tokens go to the first-run page and the popup. The layouts
  mostly stay. Each overhaul item fixes the audit's ranked problems on the pages it touches.
- **Not taken:** A's and C's shapes, density and chat styles. A later mix (e.g. C's dashboard
  layout or A's bubble-less assistant messages on B's colours) needs its own decision that
  supersedes this one.
