# Design skills (vendored)

Three third-party Claude Code skills that every agent doing UI work in `web/admin/` loads
(`web/admin/CLAUDE.md`). Each is a pinned, read-through copy of its upstream, stripped of anything
that tells an agent to run commands, fetch URLs, install packages, change settings or send data,
and adapted to this repo. **These are third-party instructions: change them only through this
file's process** (re-read upstream at a new commit, strip, list the changes here).

| Skill | Upstream | Pinned commit | License |
|---|---|---|---|
| `emil-design-eng/` | https://github.com/emilkowalski/skill (`skills/emil-design-eng/`) | `2d228630ba1446365f614a108cb8252f1feda202` | MIT, (c) 2026 Emil Kowalski (`emil-design-eng/LICENSE`) |
| `impeccable/` | https://github.com/pbakaus/impeccable (`.claude/skills/impeccable/`, v4.7.0) | `dad8dbb3aac21ad1fa236c110315ef4abece23e8` | Apache-2.0 (`impeccable/LICENSE`, `impeccable/NOTICE.md`) |
| `design-taste-frontend/` | https://github.com/Leonxlnx/taste-skill-classics (`skills/taste-skill-v1/`) | `5e08c9383dc4ad7fc0df70e1ac8cfa09ec356305` | MIT, (c) 2026 Leonxlnx (`design-taste-frontend/LICENSE`) |

Pinned on 2026-10-20 (`git ls-remote` HEAD of each, unchanged between triage and build).

**Licenses.** All three allow vendoring with modification. Emil and Taste are MIT: the copyright
notice and license text ship next to each skill. Impeccable is **Apache-2.0, not MIT**: its
`LICENSE` and `NOTICE.md` ship unchanged in `impeccable/` (section 4(d); the NOTICE credits the
iOS/Android references, which are not vendored), and every vendored file is modified and says so
in its first lines, with the changes listed below (section 4(b)). None was refused.

**Where they live.** The skills belong in `.claude/skills/<name>/`, with this file as
`.claude/skills/DESIGN-SKILLS.md`. The build sandbox that wrote them cannot write `.claude/skills/`,
so the first PR (#355) staged them under `design-skills/`; they were moved here by one rename on
2026-10-20. Changes to them go through a person's session, for the same reason.

## Precedence

When the three disagree, in this order:

1. **Accessibility and contrast.** WCAG AA contrast in both themes, visible focus, touch targets,
   `prefers-reduced-motion`, semantics. Nothing below overrides it.
2. **Impeccable's system rules.** The incumbent design system (tokens in `web/admin/src/index.css`,
   `.dark`, `src/theme/`, the existing components), Operate-mode restraint, the craft floor and its
   bans, the audit.
3. **Emil's motion rules, for animation.** Whether something animates, easing, duration (under
   300 ms for UI), what properties, reduced motion. Where Taste or Impeccable's `animate` disagree on
   motion, Emil wins.
4. **Taste, for layout feel.** Layout, spacing and density decisions inside the space the rules
   above leave, at its dials.

Known conflicts this settles: Taste bans "AI purple"; jarvis-server's `--color-primary` is a
purple and stays (2: the system wins; Taste's ban is applied to glows and gradients). Taste wants
staggered list reveals and spring physics; at motion 2 and under Emil's frequency rule, lists
mount instantly (3). Taste pushes display web fonts; Impeccable and this repo use the system stack
and fetch nothing (2). Impeccable's `bolder` and `overdrive` push expression; on Operate surfaces
they apply only when the user asks (2).

## Taste's dials

| Dial | Upstream | Here | Why |
|---|---|---|---|
| `DESIGN_VARIANCE` | 8 | **3** | An admin and member app: people come back to the same pages to get things done, so layouts are predictable, left-aligned and grid-based. Low-moderate, not 1: some offset (a wider primary column, a header aside) is still allowed. |
| `MOTION_INTENSITY` | 6 | **2** | State feedback only: `:hover`/`:active`, and short transitions for menus, dialogs and toasts (Emil's rules, under 300 ms, reduced motion respected). No ambient or perpetual motion, no scroll choreography; no animation library. |
| `VISUAL_DENSITY` | 4 | **5** | Lists, tables, logs and settings that must read on a 390 px phone and still use a desktop: "daily app" spacing, slightly denser than upstream's default, not cockpit. |

(Upstream defaults are 8, 6, 4. The user chose v1 of Taste and these values on 2026-10-20.)

## What was removed or changed

### emil-design-eng (`SKILL.md`)

Only `skills/emil-design-eng/` is vendored; the repo's other skills (animate, apple-design,
write-swift, ...) are not part of the ask. Line numbers are upstream's.

- Frontmatter `description` rewritten so it triggers on web/admin UI work.
- L8-14 "Initial Response" removed: it told the agent to reply with a fixed greeting and nothing
  else, which stalls an unattended agent.
- Added an "In this repo" section: the stack, no Framer Motion / Motion (not a dependency; do not
  add it), no Base UI (`var(--transform-origin)` means set the origin yourself), `scripts/screenshots`
  for rendered checks, and the precedence.
- L123 links to easing.dev and easings.co removed (told the agent to go to external sites);
  replaced with "start from the curves above, define them once as CSS variables".
- L160 `useSpring` from Framer Motion: marked as upstream's example only, out of scope here.
- L433 Framer Motion `useInView` replaced with plain `IntersectionObserver`; scroll reveals marked
  out of scope for the app.
- L495 Framer Motion hardware-acceleration caveat: kept, marked not applicable.
- L541 `useReducedMotion` (Framer Motion) replaced with `matchMedia`, plus Tailwind's
  `motion-safe:`/`motion-reduce:`.
- L656 "connect your phone via USB, visit your dev server by IP, use Safari's remote devtools"
  replaced: check with `scripts/screenshots` at phone width, list gesture feel under "not
  verified" for the user.
- L672 checklist row "Framer Motion `x`/`y` props under load" replaced with "animation library
  added for UI motion: remove it".

### design-taste-frontend (`SKILL.md`, from `taste-skill-v1`)

Renamed from `design-taste-frontend-v1` to `design-taste-frontend`. `agents/openai.yaml` (another
tool's invocation policy) not vendored. Line numbers are upstream's.

- L2-4 frontmatter: `disable-model-invocation: true` removed and `description` rewritten, so the
  skill loads for web/admin UI work instead of only by name.
- L10-14 dials set to 3, 2, 5 (above).
- L17-26 stack section rewritten for this repo: Vite SPA, not Next.js; the React Server Components
  rules (L20-22) removed; Tailwind v4 only.
- **L19 removed:** "if the package is missing, output the installation command (`npm install`)".
  Replaced with "use what package.json has; do not add packages".
- L27 emoji policy: "Radix, Phosphor" icons replaced with lucide-react.
- **L33 replaced:** "MUST use `@phosphor-icons/react` or `@radix-ui/react-icons`" is now "lucide-react
  only".
- L31 `h-screen` rule generalised from hero sections to full-height layouts.
- L40-42, L109, L200 the display-font rules (Geist, Satoshi, Outfit, Cabinet Grotesk; "Inter is
  banned"): replaced with "keep the system font stack; a font is the user's design-system call".
  Adopting them meant installing or fetching fonts.
- L43 `text-gray-600` replaced with the theme's text variables.
- L47 "The Lila ban": kept for glows and gradients, not for the brand purple (see Precedence).
- L51 anti-centre rule: note that at variance 3 app pages are left-aligned.
- L69-73 section 4: magnetic buttons, perpetual micro-animations, Framer Motion layout transitions
  and staggered list reveals removed (Framer Motion only, and off at motion 2); "liquid glass" kept
  with a contrast check; "lists mount instantly" added.
- L89-91 motion dial: level 1-3 points at Emil's allowed transitions; level 4-7's
  `transition: all` changed to named properties (Emil's rule); level 8-10's Framer Motion hooks
  marked out of scope.
- L115 "no 3-column card layouts": marked as a marketing-page rule.
- L118 generic names: use the sandbox's seeded users.
- **L119 softened:** "DO NOT use Lucide user icons for avatars" is now "prefer initials or a real
  picture; a lucide `User` icon is acceptable".
- **L125 removed:** placeholder images from `https://picsum.photos/...` (hotlinking an external
  service). Now: no external images; local assets, CSS or inline SVG.
- L126 shadcn/ui replaced with "no component kits; build on `web/admin/src/components/`".
- **L129-191 section 8 "Creative Arsenal" removed:** GSAP, ThreeJS/WebGL and Framer Motion
  defaults plus a catalogue of landing-page effects (dock magnification, gooey menus, scroll
  hijacking, particle buttons, ...). Libraries we don't have and motion far above our dial.
- **L193-217 section 9 "Motion-Engine Bento" removed:** mandatory perpetual Framer Motion
  animations, infinite loops and fixed marketing-site styling for "SaaS dashboards".
- L219-227 pre-flight check: phone-width check now names `scripts/screenshots`; the "perpetual
  animations in Client Components" item replaced with "reads correctly in light and dark".

### impeccable (`SKILL.md`, `reference/`)

**Not vendored at all:**

- `scripts/` (the `impeccable` launcher, which downloads a binary from GitHub releases with
  curl/wget on first run; the browser scripts for live mode; a 1.1 MB font index). This is the
  detector, context loader, pin, hooks, live and build-phase machinery every other step called.
- `reference/live.md`, `live-setup.md`, `generate.md`: live variant mode (starts a helper server,
  injects an overlay into the browser, hot-swaps generated variants).
- `reference/hooks.md`: installs a post-edit hook that runs the detector (changes agent settings).
- `reference/doctor.md`: repairs `.impeccable/` config drift and points at `npx impeccable update`.
- `reference/visualize.md`: comp-led builds through an image-generation API.
- `reference/ios.md`, `android.md`, `adapt.native.md`, `audit.native.md`: native platforms; this
  repo's UI is the web SPA (the mobile app is another repo).
- `reference/degraded/*` and upstream's `.claude/agents/impeccable-*.md`: its subagents
  (asset producer, documenter, finish reviewer, manual-edit applier).
- `reference/component-review.md`, `region-map.md`: the plate and region steps of comp-led
  builds (generated rasters, `comp-spec`, a review server); nothing vendored links to them once
  the comp-led path is gone.
- Upstream's `.claude/settings.json`, the plugin manifests and the other editors' copies.

**SKILL.md** (line numbers upstream's):

- Frontmatter `description` and `argument-hint` scoped to web/admin; `live`, `generate`, pin,
  hooks and doctor removed from the command table and the hint.
- L17-21 Setup step 1 (run `scripts/impeccable context`, which downloads its binary) and the
  "Launcher unavailable" fallback replaced with "read PRODUCT.md / DESIGN.md if they exist, then
  `index.css`, `theme/` and the components".
- Added "In this repo" (stack, Operate mode, `scripts/screenshots` as the rendered check,
  precedence).
- Native variants removed from the `audit` and `adapt` rows; `audit` marked required before a UI PR;
  `overdrive` marked off-mode for an Operate app.
- Routing: missing PRODUCT.md does not start an init interview unless the user asks (unattended
  agents; a new page extends the established world). Same note in `new-work.md` L5.
- L79-87 pin/unpin, hooks and doctor paragraphs removed (scripts and settings).

**Reference files: evaluate, polish and the floor** (upstream line numbers):

- `audit.md`: **rewritten as a self-contained manual checklist**, because upstream's audit runs the detector binary. L5 link to `audit.native.md` replaced with the repo stack. New section 0 "Scope and render": the changed files under `web/admin/src`, `scripts/sandbox up --ui` + `scripts/screenshots` (`--pages`, `--user`, `--tiles`), every PNG and `index.json` read; hover, focus, loading, error and empty states checked in code and reported as such; a source-only audit is never reported as a full one. The five dimensions and their 0-4 scoring are upstream's, with concrete checks added: contrast worked out from this repo's tokens (the ratios were recomputed for this PR: light muted on surface-alt 4.34, secondary 2.28 and tertiary 3.68 on white fail as text; dark white on primary 2.64 fails), Tailwind palette and opacity classes in both themes, `outline-none` without a `focus-visible` ring, icon-only lucide buttons without `aria-label`, dialog focus handling, `motion-safe:`/`motion-reduce:`, any new `package.json` entry, `dark:` variants, the repo's 40 px touch floor in `index.css` and what it does not cover, overflow from `index.json`. L51 "synthesize touch with a browser tool" now "say what evidence you have". L60-62 the detector run and the desktop-app/CDP flow replaced with a written anti-pattern list (distilled from upstream's detector rule registry and craft-floor's Refuse list; flat `--color-primary` purple is the brand, not the "AI palette" tell). L82 "detector findings" now "checklist hits". Report format upstream's, plus: pre-existing findings reported apart from the change's own, the screenshot name in Location, and a short **PR findings block** for the PR description.
- `critique.md`: L3 "persist a snapshot" removed. L7-14 Assessment B is now "mechanical checklist + rendered evidence" instead of the detector; the browser-tab, live-server and overlay invariants became one line (render with `scripts/screenshots`, bring a sandbox started for the critique down again). L8, L33, L108 native paths removed. L20-28 target examples point at `web/admin/src/pages/`; `critique-storage slug` removed; a render step added. L39, L44, L82-89 desktop-app/CDP flow and per-assessment browser tabs removed (generic subagents kept). L59-93 Assessment B reads the source against audit.md's checks, then `index.json` and the PNGs; the detector command, overlay injection and live-server steps removed. L97, L139-143 "LLM review vs detector" now "design review vs checklist". L133 the admin is an Operate surface. L163, L803 the `impeccable init` Design Context reference now "if PRODUCT.md exists". L190-234 the snapshot write (`IMPECCABLE_CRITIQUE_META`, `critique-storage write/trend`) removed; polish reads the Priority Issues from the conversation or the PR. L256-258 an "unattended run" exception added to the question gate (upstream only skips the questions with fewer than 3 Priority Issues). Kept: the persona names "Jordan" and "Sam" happen to match `scripts/screenshots --user` seed users.
- `polish.md`: L5 "detector result" now "audit finding". L9 DESIGN.md if it exists, otherwise our files. L22, L89, L92 native removed; evidence at the three widths via `scripts/screenshots`. L29-35 `critique-storage latest` now "a prior critique in the conversation or PR". L61 "font loading" dropped (system fonts). L66-68 both themes; lucide only. L95 `impeccable context` and hooks now "run audit.md once". L99-105 `critique-storage close` now "say in the PR which Priority Issues were cleared".
- `craft-floor.md`: L3 the design-hook sentence replaced with "no hook; audit.md before the PR". L7 the render look points at `scripts/screenshots`. L9 contrast in both themes. L13 motion is CSS/Tailwind with a reduced-motion alternative. L39 "source and self-host a face" replaced with the system font stack. L40 icons lucide only. L42 "pick light or dark from the use scene" now "both themes ship; check both". Kept: the Motion bullet still allows blur, backdrop-filter, clip-path and mask (design guidance; audit's performance checks counterweigh it).
- `routing.md`: L5 the impeccable.style docs link removed. L11 `impeccable context` / `NO_PRODUCT_MD` / `impeccable signals` replaced with signals the agent reads itself (PRODUCT.md/DESIGN.md exist, a critique in the conversation or PR, files changed on the branch). Live, generate, pin, hooks, doctor and native menu entries removed (L19 devServer bullet). Audit is the default pick when UI files changed. L22 `impeccable detect` replaced with a read of the changed files against the Refuse list and audit.md's anti-patterns.
- `craft.md`: L3 "create missing PRODUCT.md through init.md" now "init.md only on request"; reads our files when PRODUCT.md/DESIGN.md are missing.

**Reference files: build and modes** (upstream line numbers):

- `operate.md`: one sentence after L3 fitting it to web/admin (stack, Operate, phones first, `.dark` variables, system fonts, no new libraries). Nothing removed.
- `mode-operate.md`, `mode-read.md`, `mode-persuade.md`: L3 "`impeccable concept-seed --mode X` prints the MODE RULES block" replaced by "these two sections are the mode rules"; a comp is an HTML sketch, not a generated image. "a catalog world's source" now "a world's source" (the catalog came from concept-seed). "Prompt with..." now "Sketch with...", "regenerate" now "redo the sketch". mode-persuade L13: visualize.md's depth rule stated inline; marked as not for web/admin (Persuade).
- `shape.md`: only the modification notice.
- `init.md`: L3 `.impeccable/live/config.json` removed. L7 PRODUCT.md "resolved by `impeccable context`" now "at the project root if it exists". L19 live-mode dev command removed. L23 platform is `web`, native out of scope. L31 "or the decision page" (upstream's question server) removed. L102 "reported to you at boot" (the launcher) removed. L104 loading ios.md/android.md and the `impeccable context` explanation removed. L110-118 Step 5 removed (buildPath question, writing `.impeccable/config.json`, live setup). L129 `/impeccable live` recommendation and L131 "without rerunning `impeccable context`" removed.
- `document.md`: L3 the raw.githubusercontent.com spec URL removed (named as text, "do not fetch it"). L7, L107 live-panel mentions removed. L47, L112 what the sidecar carried now goes in DESIGN.md prose. L86 points at `web/admin/src/index.css` (`:root`, `.dark`) and `web/admin/src/theme/`; L87 Tailwind 4 has no config file, read `@theme`. L92 "if browser automation is available, load the live site" now `scripts/sandbox up --ui` + `scripts/screenshots`. L251-340 Step 4b (the `.impeccable/design.json` sidecar for the live panel) removed, with L345, L383, L388, L416 mentions.
- `new-work.md` (design method kept close to verbatim): repo note at L3 (PRODUCT.md/DESIGN.md if they exist, otherwise index.css, theme/, components; web/admin is an established Operate world; a "surface brief" is a note in the plan or PR, not a launcher file). L5 region-map.md (comp-led) removed. L37-41 the concept-seed dice and decision-page payload replaced by a manual round (three of 5-7 structures, at least one from the lower half, offered with the question tool, each as an HTML sketch or wireframe description). L48-49 roll/catalog, QUALITY BAR and IMPECCABLE'S PICK cards, `--register`/`--reroll` and payload fields removed; method, verdicts and re-roll registers kept. L51 the serve-question procedure (start, wait, exit codes, daemon) removed. L53 decision-comp image generation and asset-producer subagent fan-out removed. L55 every build is code-led (`.impeccable/config.json` buildPath removed). L57 QUALITY BAR board downloads removed. L65, L67 new colours need light and dark values; system font stack. L75 SEED REFERENCE and raster provenance removed. L79 "the shipped documenter" now document.md; detector clause removed. L81-87 `surface-brief read/write` launcher commands removed. L91, L99-118 the comp-led state machine removed (`build-phase`, `comp-spec`, `font-match` which fetched the Google Fonts catalog, `generate-image`, `embed-prompt`, `comp-diff`, asset-producer, component-review.md and region-map.md links). L128 "the detector flags" now "check the CSS". L129 WebGL/canvas: platform APIs, no new libraries. L131 "search... verify stock URLs resolve" now: images the user supplies or the repo ships; never fetch, download or hotlink. L132 motion in CSS. L138-140 inspection with `scripts/screenshots` (phone first, `index.json`); native device classes and `comp-diff` removed. L142 `impeccable detect` replaced by a manual read against craft-floor.md's bans. L144-146 the `impeccable-finish-reviewer` subagent replaced by a fresh self-review pass (same four dispositions, two-round budget). L148 `embed-prompt --scan` removed. L152 `impeccable-documenter` subagent and `.impeccable/design.json` removed (follow document.md). L154 `build-phase finish` removed.
- Kept, flagged: Persuade-only guidance in new-work.md (visual-world tournament, drenched colour, scroll pacing, generative motion, font blacklist) is gated to Persuade/Experience or new worlds; web/admin takes the "established world, extend" path. new-work L65 "dark or light is never a default" kept with a note (this repo ships both). document.md's example tokens (Stitch linter wording, Cormorant) are examples.

**Reference files: refine, enhance and fix commands** (upstream line numbers):

- `adapt.md`: L5 native routing and the `adapt.native.md` link replaced with "native is out of scope; web/admin is React 19 + Vite + Tailwind 4, phones first". L168: `scripts/screenshots` added as the first layout pass (every viewport, both themes; horizontal overflow is a failure). Kept but off-target for an SPA: print and email sections, hero `srcset` examples.
- `animate.md`: L11 native bullet and `ios.md`/`android.md` links removed; Operate mode noted. L66: no motion library (framer-motion is not a dependency): CSS/Tailwind and the Web Animations API, add no animation library.
- `colorize.md`: L14 DESIGN.md "if it exists", otherwise `index.css` variables, `.dark`, `theme/` and the components. L78-86 live-mode params and the `live.md` link removed.
- `delight.md`: L14 DESIGN.md "if it exists". L59 illustration means lucide icons or inline SVG; no icon, illustration, animation or sound libraries.
- `extract.md`: L7 names our design system (`index.css` tokens, `theme/`, the components, lucide only) and bans adding libraries.
- `layout.md`: L9 native bullet removed; Operate and phones first noted. L25-31 `impeccable detect --scope layout` replaced by a manual read-through (arbitrary Tailwind/inline spacing, fixed widths that overflow at 390px, nested cards, z-index and overflow) plus `scripts/screenshots`. L72 "rerun the scan" now "repeat the scan (and the screenshots)". L76-84 live-mode params and the `live.md` link removed.
- `harden.md`: L135-136 "use a proper i18n library" now `Intl.PluralRules` or an existing helper, no new library. L220 polyfills only what the build provides. L290 debounce is a small local helper. L314 "axe, WAVE" became a manual accessibility pass. L341 `scripts/screenshots` for the stress states. Kept but off-target: L175 "core functionality works without JavaScript" (an SPA).
- `onboard.md`: L198-199 Tippy, Popper, Intro.js, Shepherd, Joyride replaced with "build from existing components, add no libraries". L202 "Analytics: track completion" removed (sends data). L182 the icon is a lucide-react icon. L225 real-user testing by observing or asking, no analytics.
- `optimize.md`: L64 font section only if web fonts are ever added (user's call, never from a CDN). L109 react-window / TanStack Virtual replaced with hand-rolled, no library. L221-226 WebPageTest, CrUX, webpack-bundle-analyzer, Sentry/DataDog/New Relic replaced with DevTools and Lighthouse by hand on a sandbox, Vite's chunk sizes and `scripts/screenshots`; analyzers, uploads and telemetry banned. L252 "real user monitoring" removed. Kept but off-target: CDN (L34, L182, L196), GraphQL (L179), SSR (L197).
- `typeset.md`: L9 native bullet removed; Operate noted. Added after L9: the system font stack from `index.css`, no web font is added, fetched or installed, and the loading advice (L23, L52, L74) applies only if the user asks for a web font. L11 DESIGN.md "if it exists". L15 "detector findings" now "mechanical-scan findings". L24-30 `impeccable detect --scope type` replaced by a manual read-through plus screenshots. L67 the detector's `straggler`/`unbalanced-heading` findings now spotted in screenshots. L78 as layout.md. L82-90 live-mode params and the `live.md` link removed.
- `overdrive.md`: kept, flagged off-mode for an Operate app (note added after L8: only functional/performance senses usually fit; shaders, particles and cinematic transitions need the user's explicit pick; native APIs and CSS only). L24 browser check now `scripts/screenshots`, and motion timing is listed as unverified for the user to judge. L53, 59, 65-67 library suggestions (Motion/Framer Motion, GSAP, Three.js, OGL, regl, TanStack Virtual, deck.gl, D3) rewritten into native-API advice that bans adding them. Kept but off-target: WebGL/WebGPU/WASM (L59-76), device APIs including geolocation (L80).
- `bolder.md`, `clarify.md`, `distill.md`, `quieter.md`: only the modification notice added.
- Kept as is: "when a sub-agent tool is available, run these independently" (layout, typeset L15: a generic subagent, not upstream's named ones); "ask with AskUserQuestion" (bolder L9, distill L24, extract L9, quieter L31, overdrive L17: Claude Code's own tool).

**Every reference file** starts with a modification notice pointing here.

## What the remaining hits are

`grep -rniE 'npm|npx|pnpm|yarn|bun|curl|wget|https?://|install|fetch|settings\.json|bash|upload|analytics|telemetry'` plus upstream's tool names (`impeccable <verb>`, `critique-storage`, `comp-spec`, `concept-seed`, `serve-question`)
over the skills (licenses aside) finds only these (line numbers of the vendored files):

- `onboard.md` L225, `optimize.md` L227, `typeset.md` L14, `optimize.md` L66, `document.md` L5:
  the prohibitions this vendoring added ("do not add analytics", "do not upload pages to external
  services", "do not fetch a font", "do not fetch the spec").
- `optimize.md` L85 "prefetch likely next pages": browser resource hints, performance guidance.
- `new-work.md` L94: "use images the user supplies or the repo already ships; never fetch".
- `audit.md` L53, L181 and `critique.md` L724: "refetching", "failed requests" (from
  `index.json`) and an "analytics" product category; design text, no instruction.
- `emil-design-eng/SKILL.md` L562: "Sonner (13M+ weekly npm downloads)", a fact about a library.

The only commands the skills tell an agent to run are this repo's own: `scripts/sandbox up
--name <name> --ui`, `scripts/sandbox down --name <name>` and `scripts/screenshots --sandbox
<name>`. Kept on purpose: "ask with AskUserQuestion" (Claude Code's own question tool) and
"run these with generic subagents when available" (not upstream's named agents).

## Updating a skill

Clone the upstream at its new commit, diff it against the pinned one, read every changed file,
apply the same stripping rules, and update the commit and the change lists here in the same PR.
Never copy an upstream file in unread.

## Follow-ups

- Copy the same three skills and this file into alexberardi/jarvis-docs (separate item: one PR
  cannot touch both repos).
- The audit's token maths found existing contrast failures that belong to the UI items (#331
  audit, #332 design system), not to this one: white text on `--color-primary` in dark (2.64:1,
  about 20 uses of `bg-[var(--color-primary)] ... text-white`), `--color-text-muted` on
  `--color-surface-alt` in light (4.34:1), and `--color-secondary`/`--color-tertiary` as text on
  light backgrounds.
