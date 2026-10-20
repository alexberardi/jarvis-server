---
name: design-taste-frontend
description: "Taste (Leonxlnx's taste-skill v1): layout, typography, colour, spacing and motion rules against generic AI-looking UI, driven by three dials (design variance, motion intensity, visual density) set here for jarvis-server's admin and member app. Use for any UI work in web/admin: new or changed pages, components, layouts and forms."
---

> Vendored from Leonxlnx/taste-skill-classics@5e08c93 (`skills/taste-skill-v1`, MIT) and adapted for jarvis-server; changes are listed in DESIGN-SKILLS.md.

# High-Agency Frontend Skill

## 1. ACTIVE BASELINE CONFIGURATION
* DESIGN_VARIANCE: 3 (1=Perfect Symmetry, 10=Artsy Chaos)
* MOTION_INTENSITY: 2 (1=Static/No movement, 10=Cinematic/Magic Physics)
* VISUAL_DENSITY: 5 (1=Art Gallery/Airy, 10=Pilot Cockpit/Packed Data)

**AI Instruction:** The standard baseline for all generations is strictly set to these values (3, 2, 5), chosen for jarvis-server's admin and member app (see DESIGN-SKILLS.md for why). Do not ask the user to edit this file. Otherwise, ALWAYS listen to the user: adapt these values dynamically based on what they explicitly request in their chat prompts. Use these baseline (or user-overridden) values as your global variables to drive the specific logic in Sections 3 through 7.

Where this skill disagrees with the other design skills, DESIGN-SKILLS.md decides: accessibility and contrast first, then Impeccable's system rules, then Emil's motion rules for animation; Taste sets the layout feel.

## 2. DEFAULT ARCHITECTURE & CONVENTIONS
This repo's UI is `web/admin/`: a React 19 single-page app built with Vite (no Next.js, no Server Components), Tailwind CSS 4 through the Vite plugin, and lucide-react. Light and dark themes come from a `.dark` class and CSS variables in `web/admin/src/index.css` (`--color-primary`, `--color-surface`, `--color-text-muted`, ...). Phones first: `scripts/screenshots` checks every page at phone, tablet and desktop widths in light and dark.

* **Dependencies:** Use what `web/admin/package.json` already has. Do not add packages (animation, icon, font or component libraries) as part of design work; if a design truly needs one, say so in the PR instead.
* **State Management:** Use local `useState`/`useReducer` for isolated UI. Use global state strictly for deep prop-drilling avoidance.
* **Styling Policy:** Use Tailwind CSS v4 for 90% of styling, and the theme's CSS variables for colour so both themes follow.
* **ANTI-EMOJI POLICY [CRITICAL]:** NEVER use emojis in code, markup, text content, or alt text. Replace symbols with lucide-react icons or clean SVG primitives. Emojis are BANNED.
* **Responsiveness & Spacing:**
  * Standardize breakpoints (`sm`, `md`, `lg`, `xl`).
  * Contain page layouts using `max-w-[1400px] mx-auto` or `max-w-7xl`.
  * **Viewport Stability [CRITICAL]:** NEVER use `h-screen` for full-height layouts. ALWAYS use `min-h-[100dvh]` to prevent layout jumping on mobile browsers (iOS Safari).
  * **Grid over Flex-Math:** NEVER use complex flexbox percentage math (`w-[calc(33%-1rem)]`). ALWAYS use CSS Grid (`grid grid-cols-1 md:grid-cols-3 gap-6`) for reliable structures.
* **Icons:** Use `lucide-react` only. Standardize `strokeWidth` globally (e.g., exclusively use `1.5` or `2.0`).


## 3. DESIGN ENGINEERING DIRECTIVES (Bias Correction)
LLMs have statistical biases toward specific UI cliché patterns. Proactively construct premium interfaces using these engineered rules:

**Rule 1: Deterministic Typography**
* **Display/Headlines:** Page headings in an app are not marketing heroes: control hierarchy with weight and colour first (see Section 7, "NO Oversized H1s"). Upstream's `text-4xl md:text-6xl tracking-tighter leading-none` is for landing pages.
    * **FONTS:** web/admin uses the system font stack and loads no web fonts; keep it. (Upstream pushes `Geist`, `Outfit`, `Cabinet Grotesk` or `Satoshi`; adopting a font is a design-system decision for the user, not a side effect of a page change.)
    * **TECHNICAL UI RULE:** Serif fonts are strictly BANNED for Dashboard/Software UIs.
* **Body/Paragraphs:** Default to `text-base leading-relaxed max-w-[65ch]` in the theme's text colours (`--color-text`, `--color-text-muted`).

**Rule 2: Color Calibration**
* **Constraint:** Max 1 Accent Color. Saturation < 80%.
* **No AI glow:** No purple button glows, no neon gradients. Use neutral bases (Zinc/Slate) with a singular accent. (Upstream bans the "AI Purple/Blue" look outright; jarvis-server's brand primary is a purple, `--color-primary`, and it stays. The ban applies to glows and gradients, not to the brand colour.)
* **COLOR CONSISTENCY:** Stick to one palette for the entire output. Do not fluctuate between warm and cool grays within the same project.

**Rule 3: Layout Diversification**
* **ANTI-CENTER BIAS:** Centered Hero/H1 sections are strictly BANNED when `DESIGN_VARIANCE > 4`. Force "Split Screen" (50/50), "Left Aligned content/Right Aligned asset", or "Asymmetric White-space" structures. (At variance 3, app pages are left-aligned and predictable; centring is for short single-purpose screens such as sign-in and empty states.)

**Rule 4: Materiality, Shadows, and "Anti-Card Overuse"**
* **DASHBOARD HARDENING:** For `VISUAL_DENSITY > 7`, generic card containers are strictly BANNED. Use logic-grouping via `border-t`, `divide-y`, or purely negative space. Data metrics should breathe without being boxed in unless elevation (z-index) is functionally required.
* **Execution:** Use cards ONLY when elevation communicates hierarchy. When a shadow is used, tint it to the background hue.

**Rule 5: Interactive UI States**
* **Mandatory Generation:** LLMs naturally generate "static" successful states. You MUST implement full interaction cycles:
  * **Loading:** Skeletal loaders matching layout sizes (avoid generic circular spinners).
  * **Empty States:** Beautifully composed empty states indicating how to populate data.
  * **Error States:** Clear, inline error reporting (e.g., forms).
  * **Tactile Feedback:** On `:active`, use `-translate-y-[1px]` or `scale-[0.98]` to simulate a physical push indicating success/action.

**Rule 6: Data & Form Patterns**
* **Forms:** Label MUST sit above input. Helper text is optional but should exist in markup. Error text below input. Use a standard `gap-2` for input blocks.

## 4. CREATIVE PROACTIVITY (Anti-Slop Implementation)
At this repo's dials (variance 3, motion 2) most of upstream's section is out of scope: magnetic buttons, perpetual micro-animations, Framer Motion layout transitions and staggered list reveals are removed (see DESIGN-SKILLS.md). What remains:
* **"Liquid Glass" Refraction:** When glassmorphism is needed (rarely, in an app), go beyond `backdrop-blur`. Add a 1px inner border (`border-white/10`) and a subtle inner shadow (`shadow-[inset_0_1px_0_rgba(255,255,255,0.1)]`) to simulate physical edge refraction, and check contrast in both themes.
* **Lists mount instantly.** Lists and tables in an app are seen many times a day; do not stagger them in.

## 5. PERFORMANCE GUARDRAILS
* **DOM Cost:** Apply grain/noise filters exclusively to fixed, pointer-event-none pseudo-elements (e.g., `fixed inset-0 z-50 pointer-events-none`) and NEVER to scrolling containers to prevent continuous GPU repaints and mobile performance degradation.
* **Hardware Acceleration:** Never animate `top`, `left`, `width`, or `height`. Animate exclusively via `transform` and `opacity`.
* **Z-Index Restraint:** NEVER spam arbitrary `z-50` or `z-10` unprompted. Use z-indexes strictly for systemic layer contexts (Sticky Navbars, Modals, Overlays).

## 6. TECHNICAL REFERENCE (Dial Definitions)

### DESIGN_VARIANCE (Level 1-10)
* **1-3 (Predictable):** Flexbox `justify-center`, strict 12-column symmetrical grids, equal paddings.
* **4-7 (Offset):** Use `margin-top: -2rem` overlapping, varied image aspect ratios (e.g., 4:3 next to 16:9), left-aligned headers over center-aligned data.
* **8-10 (Asymmetric):** Masonry layouts, CSS Grid with fractional units (e.g., `grid-template-columns: 2fr 1fr 1fr`), massive empty zones (`padding-left: 20vw`).
* **MOBILE OVERRIDE:** For levels 4-10, any asymmetric layout above `md:` MUST aggressively fall back to a strict, single-column layout (`w-full`, `px-4`, `py-8`) on viewports `< 768px` to prevent horizontal scrolling and layout breakage.

### MOTION_INTENSITY (Level 1-10)
* **1-3 (Static):** No automatic animations. CSS `:hover` and `:active` states only, plus the short state transitions Emil's skill allows (menus, dialogs, toasts; under 300ms; respecting `prefers-reduced-motion`).
* **4-7 (Fluid CSS):** Use `transition: transform 0.3s cubic-bezier(0.16, 1, 0.3, 1), opacity 0.3s cubic-bezier(0.16, 1, 0.3, 1)` (name the properties; never `transition: all`). Use `animation-delay` cascades for load-ins. Focus strictly on `transform` and `opacity`. Use `will-change: transform` sparingly.
* **8-10 (Advanced Choreography):** Complex scroll-triggered reveals or parallax. NEVER use `window.addEventListener('scroll')`. (Upstream uses Framer Motion hooks here; out of scope for this repo.)

### VISUAL_DENSITY (Level 1-10)
* **1-3 (Art Gallery Mode):** Lots of white space. Huge section gaps. Everything feels very expensive and clean.
* **4-7 (Daily App Mode):** Normal spacing for standard web apps.
* **8-10 (Cockpit Mode):** Tiny paddings. No card boxes; just 1px lines to separate data. Everything is packed. **Mandatory:** Use Monospace (`font-mono`) for all numbers.

## 7. AI TELLS (Forbidden Patterns)
To guarantee a premium, non-generic output, you MUST strictly avoid these common AI design signatures unless explicitly requested:

### Visual & CSS
* **NO Neon/Outer Glows:** Do not use default `box-shadow` glows or auto-glows. Use inner borders or subtle tinted shadows.
* **NO Pure Black:** Never use `#000000`. Use Off-Black, Zinc-950, or Charcoal.
* **NO Oversaturated Accents:** Desaturate accents to blend elegantly with neutrals.
* **NO Excessive Gradient Text:** Do not use text-fill gradients for large headers.
* **NO Custom Mouse Cursors:** They are outdated and ruin performance/accessibility.

### Typography
* **Fonts:** Keep the system font stack (Section 3, Rule 1). Upstream bans Inter and pushes display fonts; do not add a web font as part of a page change.
* **NO Oversized H1s:** The first heading should not scream. Control hierarchy with weight and color, not just massive scale.
* **Serif Constraints:** Use Serif fonts ONLY for creative/editorial designs. **NEVER** use Serif on clean Dashboards.

### Layout & Spacing
* **Align & Space Perfectly:** Ensure padding and margins are mathematically perfect. Avoid floating elements with awkward gaps.
* **NO 3-Column Card Layouts:** The generic "3 equal cards horizontally" feature row is BANNED. Use a 2-column Zig-Zag, asymmetric grid, or horizontal scrolling approach instead. (This is a marketing-page rule; in the app, a grid of equal items such as add-ons or nodes is fine when the items are equal.)

### Content & Data (The "Jane Doe" Effect)
* **NO Generic Names:** "John Doe", "Sarah Chan", or "Jack Su" are banned. Use highly creative, realistic-sounding names. (Sandbox seed users already have names; use those.)
* **Avatars:** Prefer the user's initials or real picture over a generic "egg" silhouette. A lucide `User` icon is acceptable where there is nothing better.
* **NO Fake Numbers:** Avoid predictable outputs like `99.99%`, `50%`, or basic phone numbers (`1234567`). Use organic, messy data (`47.2%`, `+1 (312) 847-1928`).
* **NO Startup Slop Names:** "Acme", "Nexus", "SmartFlow". Invent premium, contextual brand names.
* **NO Filler Words:** Avoid AI copywriting clichés like "Elevate", "Seamless", "Unleash", or "Next-Gen". Use concrete verbs.

### External Resources & Components
* **No external images:** Do not hotlink placeholder or stock images from any site. Use local assets, CSS or inline SVG.
* **No component kits:** web/admin has no shadcn/ui or other kit; build on the existing components in `web/admin/src/components/`.
* **Production-Ready Cleanliness:** Code must be extremely clean, visually striking, memorable, and meticulously refined in every detail.

## 8. FINAL PRE-FLIGHT CHECK
Evaluate your code against this matrix before outputting. This is the **last** filter you apply to your logic.
- [ ] Is global state used appropriately to avoid deep prop-drilling rather than arbitrarily?
- [ ] Does the layout collapse cleanly to one column at the phone width (`w-full`, `px-4`), with no sideways scroll (`scripts/screenshots` reports overflow)?
- [ ] Do full-height sections safely use `min-h-[100dvh]` instead of the bugged `h-screen`?
- [ ] Do `useEffect` animations contain strict cleanup functions?
- [ ] Are empty, loading, and error states provided?
- [ ] Are cards omitted in favor of spacing where possible?
- [ ] Does it read correctly in both light and dark themes?
