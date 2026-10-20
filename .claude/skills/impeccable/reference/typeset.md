> Modified from pbakaus/impeccable@dad8dbb (Apache-2.0) for jarvis-server; changes are listed in DESIGN-SKILLS.md.

Typography carries information, hierarchy, and voice. Improve it inside the established visual world; do not replace the identity unless the user asked to.

---

## Visitor mode

- **Persuade + Experience:** display type may carry the voice. Use decisive contrast and responsive scale when the composition benefits.
- **Operate + Read:** stability, scanability, and measure come first. A single well-tuned family and fixed role scale are often right.

This repo's `web/admin/` surfaces are an admin/member app, i.e. **Operate** mode. Native iOS/Android typography is out of scope here.

The established family is the system font stack set in `web/admin/src/index.css` (plus a system monospace stack); the app loads no web fonts. Do not add a font, fetch one from a CDN, or add a font package; font loading advice below applies only if the user asks for a web font.

If typography replacement would create a new identity, route through [new-work.md](new-work.md) and update DESIGN.md if it exists. Otherwise preserve confirmed families and improve their use.

## Two isolated assessments

When a sub-agent tool is available and permitted, run these independently; otherwise run them yourself in this order. Do not let mechanical-scan findings anchor the design assessment.

1. **Typographic assessment:** inspect representative pages and styles. Answer every question below with a file, selector, or computed value:
   - **Authority and fit:** Which faces, weights, and roles are established? Do they fit the product and selected world, or are they unexamined defaults? Is every family necessary?
   - **Hierarchy:** Can heading, body, label, metadata, and data roles be distinguished at a glance? Are adjacent sizes or weights too close to carry different jobs?
   - **Scale and consistency:** Is there a deliberate role scale, or a collection of arbitrary values? Do repeated roles stay identical across screens and states?
   - **Reading:** Does body copy stay within a comfortable 45–75 character measure? Are line height, paragraph rhythm, contrast, and tracking tuned to the actual face, width, language, and surface?
   - **Stress:** What happens with long headings, localization expansion, zoom, narrow containers, missing weights, and font fallback?
   - **Delivery:** Are only used assets loaded? Do fallback metrics, loading strategy, and variable-font settings avoid invisible text and disruptive reflow?
2. **Mechanical scan:** read the target files and list, with file and line: font sizes, weights, line heights and tracking that sit outside the established scale (Tailwind arbitrary values such as `text-[13px]`, inline `style` font values), any `font-family` other than the system stacks, body text under 16px, and headings without `text-wrap: balance`. For rendered evidence, run `scripts/screenshots --sandbox <name>` (full-page PNGs of the admin SPA at phone 390x844, tablet 820x1180 and desktop 1440x900, light and dark, plus `index.json` with console errors, failed requests and horizontal overflow; the sandbox comes from `scripts/sandbox up --name <name> --ui`) and check every viewport in both themes. Also inspect dynamic font values a read-through cannot resolve. Synthesize both assessments before editing, noting what each caught alone. A clean scan is a floor, not proof of good typography.

## Set the system

Before editing, state:

- the roles the interface needs;
- the intended contrast between those roles;
- the reading measure and density;
- which existing faces and weights are authoritative;
- any performance, localization, or accessibility constraints.

Use the fewest roles and families that make the hierarchy unmistakable. Combine size, weight, space, and tone deliberately instead of asking size alone to do all the work. Role names and tokens should describe purpose rather than values.

## Apply

- Keep body copy comfortably readable and zoomable. Use 1rem / 16px as the ordinary web body floor unless a dense role, platform convention, or user setting justifies otherwise.
- Keep prose in the 45–75ch range. Tune line height inversely with measure: wider lines generally need more leading.
- Compensate light text on dark surfaces on all three perceptual axes: slightly more line height, a touch more tracking, and one step more weight when the face needs it.
- Tune line height to the face, width, language, and contrast, not a universal ratio.
- Keep repeated roles consistent across screens and states.
- Use numeric, tabular, code, and label features when their content benefits.
- Load only used font assets and weights. Provide metric-compatible fallbacks and avoid blocking text.
- Let marketing display type respond to available space when useful; keep dense product and reading surfaces spatially predictable.
- Preserve browser zoom, user font settings, Dynamic Type, and platform text scaling.
- Use paragraph spacing or first-line indentation as the primary paragraph rhythm; combining both usually double-marks the boundary.

Do not make type decorative at the expense of comprehension, or introduce a second family without a clear role it alone can perform.

## Line breaks

After Matthew Butterick's *Practical Typography*, paraphrased; where his numbers differ from these, these stand.

- Set `text-wrap: balance` on headings and short centered blocks, and `text-wrap: pretty` on body copy. Both are progressive: a browser that ignores them keeps its default wrap.
- `pretty` pulls a word down beside a lone last word. It does not fill the last line, and a two-word tail is fine.
- In a column too narrow for either to help, edit the copy or widen the column.
- Break a heading by hand only between phrases, and check the break at every width the heading renders at. A hard break that lands mid-line on a phone is worse than the default wrap.
- An uneven break can carry meaning: a headline that lands on one word for weight, a poster line, a pull quote. Make it deliberate with a hard break or a wrap set on that element, and keep it rare. Treat stragglers and unbalanced headings you spot in the screenshots as advisory for this reason; say which breaks are intended.

## Verify

- Primary, secondary, body, and metadata roles are recognizable without reading the copy.
- Long text remains comfortable across relevant widths and languages.
- The typography belongs to the product and its established world.
- Loading does not create disruptive reflow or invisible text.
- Zoom, text scaling, focus, contrast, and reduced viewport paths remain usable.
- The final mechanical scan has no unexplained findings.

Answer each item with rendered or source evidence, then repeat the scan (and the screenshots). Do not substitute a bare “yes” for verification.

When the hierarchy holds, hand off to `/impeccable polish`.
