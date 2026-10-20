"""The three proposed visual directions for Jarvis Automation (docs/design/AUDIT-2026-10.md).

Tokens only: colours (light and dark), type, spacing, radius, motion. build.py turns them into
directions.html (the comparison page) and contrast.md (every pair checked against WCAG AA).
Fonts are system stacks only: nothing is fetched.
"""

SYSTEM = "system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif"
ROUNDED = "ui-rounded, 'SF Pro Rounded', system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif"
MONO = "ui-monospace, 'SF Mono', 'Cascadia Mono', 'JetBrains Mono', Menlo, Consolas, monospace"

# Colour roles every direction defines, in both themes.
#   bg            page background
#   surface       cards, sidebar, composer (one step up from bg in both themes)
#   surface-alt   wells: inputs, code, table headers, hover
#   border        dividers (decorative, no contrast requirement)
#   border-strong input and control boundaries (>= 3:1 on surface, WCAG 1.4.11)
#   text, muted   body and secondary text (>= 4.5:1 on bg, surface and surface-alt)
#   primary       the one accent: primary buttons, links, selection, focus
#   on-primary    text on a primary fill (>= 4.5:1)
#   primary-soft  tinted selection / user-bubble background (text on it >= 4.5:1)
#   success, warning, danger   status text and icons (>= 4.5:1 on surface)
#   *-soft        status chip backgrounds (status text on them >= 4.5:1)
#   focus         focus ring (>= 3:1 on bg and surface)

DIRECTIONS = {
    "A": {
        "name": "Hearth",
        "tagline": "Warm and domestic. A home appliance, not a server console.",
        "primary_note": "Changes the brand colour: the purple becomes a clay/terracotta. This is your call.",
        "keeps_purple": False,
        "font": ROUNDED,
        "mono": MONO,
        "type": [  # name, px, line-height, weight, use
            ("display", 28, 1.2, 600, "Home status sentence"),
            ("title", 22, 1.25, 600, "Page title"),
            ("heading", 18, 1.35, 600, "Card and section heading"),
            ("body", 16, 1.6, 400, "Body, chat, form fields"),
            ("small", 14, 1.5, 400, "Secondary lines, meta"),
            ("caption", 13, 1.4, 500, "Chips, timestamps (the floor)"),
        ],
        "space": [4, 8, 12, 16, 24, 32, 48],
        "radius": {"control": 12, "card": 18, "bubble": 22, "pill": 999},
        "density": "Roomy: 48px rows, 24px card padding, 20px phone gutters.",
        "motion": [
            "Press: scale(0.97), 120ms, ease-out. Hover: background colour only, 150ms ease.",
            "Menus and popovers: opacity + scale(0.97 to 1) from the trigger, 180ms, --ease-out.",
            "Drawer and sheets: translateX/Y, 260ms, --ease-drawer; closes 200ms.",
            "New chat message: opacity 0 to 1 and 4px rise, 180ms, the newest only (never the history).",
            "Loading: static skeletons in the card's shape; a spinner only inside a pressed button.",
            "prefers-reduced-motion: every transform dropped; opacity fades of 120ms or less remain.",
        ],
        "light": {
            "bg": "#f6f2ec", "surface": "#fffdf9", "surface-alt": "#efe8df", "border": "#e5dbcf",
            "border-strong": "#8c7c6c", "text": "#2a221d", "muted": "#695b50",
            "primary": "#a8441f", "on-primary": "#ffffff", "primary-soft": "#f6e2d7",
            "success": "#2d6e45", "success-soft": "#e3f0e6", "warning": "#835300", "warning-soft": "#f7ebd2",
            "danger": "#b0281c", "danger-soft": "#f9e3df", "focus": "#a8441f",
        },
        "dark": {
            "bg": "#1b1714", "surface": "#25201c", "surface-alt": "#2f2924", "border": "#3a322b",
            "border-strong": "#8a7b6d", "text": "#f3ece4", "muted": "#b9aa9b",
            "primary": "#f0a07f", "on-primary": "#2a160d", "primary-soft": "#3d2a20",
            "success": "#86cf9f", "success-soft": "#20342a", "warning": "#e9bb62", "warning-soft": "#3a2f1a",
            "danger": "#f4a196", "danger-soft": "#3f2420", "focus": "#f0a07f",
        },
    },
    "B": {
        "name": "Quiet Violet",
        "tagline": "The current product, made consistent. Keeps the purple; neutral greys do the work.",
        "primary_note": "Keeps the incumbent --color-primary (#8A00C4 light, #c084fc dark), used only for actions, selection and focus.",
        "keeps_purple": True,
        "font": SYSTEM,
        "mono": MONO,
        "type": [
            ("title", 24, 1.25, 650, "Page title"),
            ("heading", 18, 1.35, 600, "Card heading"),
            ("subheading", 15, 1.4, 600, "Row title, list item"),
            ("body", 15, 1.55, 400, "Body, chat, fields"),
            ("small", 13, 1.45, 400, "Secondary lines, meta"),
            ("caption", 12, 1.35, 500, "Chips, counters (the floor; no 10-11px)"),
        ],
        "space": [4, 8, 12, 16, 20, 24, 32, 40],
        "radius": {"control": 8, "card": 12, "bubble": 18, "pill": 999},
        "density": "Daily-app: 44px rows, 16px card padding (20 desktop), 16px phone gutters.",
        "motion": [
            "Press: scale(0.98), 100ms, ease-out. Hover: colour only, 120ms ease.",
            "Menus, selects, popovers: opacity + translateY(-4px to 0), 160ms, --ease-out, origin at the trigger.",
            "Nav drawer: translateX, 220ms, --ease-drawer (today: 200ms with the default curve).",
            "Toasts: enter from the bottom, 200ms; leave the same way, 160ms.",
            "Chat messages and lists mount instantly (seen tens of times a day).",
            "prefers-reduced-motion: transforms off, colour and opacity changes stay; spinners slow to 1.6s.",
        ],
        "light": {
            "bg": "#f6f6f8", "surface": "#ffffff", "surface-alt": "#f0f0f3", "border": "#e2e2e8",
            "border-strong": "#84848f", "text": "#16161b", "muted": "#5b5b68",
            "primary": "#8A00C4", "on-primary": "#ffffff", "primary-soft": "#f2e6fa",
            "success": "#1d7339", "success-soft": "#e3f3e8", "warning": "#8a5300", "warning-soft": "#fbefd9",
            "danger": "#bf2530", "danger-soft": "#fbe5e6", "focus": "#8A00C4",
        },
        "dark": {
            "bg": "#111115", "surface": "#1a1a20", "surface-alt": "#23232b", "border": "#2e2e38",
            "border-strong": "#787889", "text": "#f2f2f5", "muted": "#a6a6b4",
            "primary": "#c084fc", "on-primary": "#1d0b2b", "primary-soft": "#2e1f3d",
            "success": "#72d697", "success-soft": "#173022", "warning": "#f0b44c", "warning-soft": "#352a16",
            "danger": "#ff8f8f", "danger-soft": "#3a1d20", "focus": "#c084fc",
        },
    },
    "C": {
        "name": "Ledger",
        "tagline": "Quiet monochrome utility. Ink on paper, one teal signal, numbers in mono.",
        "primary_note": "Changes the brand colour: actions are ink (near-black / near-white); teal is the only accent, for links, live state and focus. This is your call.",
        "keeps_purple": False,
        "font": SYSTEM,
        "mono": MONO,
        "type": [
            ("title", 20, 1.3, 600, "Page title"),
            ("heading", 15, 1.4, 600, "Section heading (no cards: a rule above)"),
            ("body", 14, 1.5, 400, "Body, chat, fields"),
            ("data", 13, 1.45, 400, "Mono: ids, times, durations, ports (tabular-nums)"),
            ("small", 13, 1.45, 400, "Secondary lines"),
            ("caption", 12, 1.35, 500, "Chips, counters (the floor)"),
        ],
        "space": [2, 4, 8, 12, 16, 24, 32],
        "radius": {"control": 4, "card": 6, "bubble": 6, "pill": 999},
        "density": "Dense: 36px rows (44px on touch), hairline dividers instead of cards, 16px phone gutters.",
        "motion": [
            "Press: background colour change only, no scale. Hover: colour only, 100ms ease.",
            "Menus and popovers: opacity only, 120ms, ease-out. No transforms anywhere else.",
            "Nav drawer: translateX, 200ms, --ease-drawer (the one movement in the app).",
            "Everything else is instant: lists, messages, tabs, cards.",
            "Loading: a one-line text status ('Loading nodes...') and fixed-height rows; no spinners in content.",
            "prefers-reduced-motion: the drawer appears without sliding; nothing else changes.",
        ],
        "light": {
            "bg": "#fbfbfa", "surface": "#ffffff", "surface-alt": "#f2f2f0", "border": "#e4e4e1",
            "border-strong": "#85857f", "text": "#151515", "muted": "#5c5c57",
            "primary": "#1c1c1c", "on-primary": "#ffffff", "primary-soft": "#ebebe8",
            "accent": "#0b6e66",
            "success": "#1a7342", "success-soft": "#e2f2e8", "warning": "#855400", "warning-soft": "#f6ecd9",
            "danger": "#b42318", "danger-soft": "#fbe6e3", "focus": "#0b6e66",
        },
        "dark": {
            "bg": "#0e0e0e", "surface": "#151515", "surface-alt": "#1d1d1d", "border": "#2a2a2a",
            "border-strong": "#7c7c76", "text": "#ececea", "muted": "#a3a39d",
            "primary": "#ececea", "on-primary": "#151515", "primary-soft": "#262626",
            "accent": "#5fd8c9",
            "success": "#6fdb9d", "success-soft": "#13291c", "warning": "#f3c26b", "warning-soft": "#2e2512",
            "danger": "#ff9a8c", "danger-soft": "#33191a", "focus": "#5fd8c9",
        },
    },
}

# The pairs checked for each direction and theme: (label, fg role, bg role, minimum ratio).
PAIRS = [
    ("text on bg", "text", "bg", 4.5),
    ("text on surface-alt", "text", "surface-alt", 4.5),
    ("muted on bg", "muted", "bg", 4.5),
    ("muted on surface", "muted", "surface", 4.5),
    ("muted on surface-alt", "muted", "surface-alt", 4.5),
    ("primary (link) on surface", "primary", "surface", 4.5),
    ("primary (link) on bg", "primary", "bg", 4.5),
    ("on-primary on primary (button)", "on-primary", "primary", 4.5),
    ("text on primary-soft (user bubble, selection)", "text", "primary-soft", 4.5),
    ("success on surface", "success", "surface", 4.5),
    ("success on success-soft", "success", "success-soft", 4.5),
    ("warning on surface", "warning", "surface", 4.5),
    ("warning on warning-soft", "warning", "warning-soft", 4.5),
    ("danger on surface", "danger", "surface", 4.5),
    ("danger on danger-soft", "danger", "danger-soft", 4.5),
    ("border-strong on surface (input edge)", "border-strong", "surface", 3.0),
    ("border-strong on surface-alt", "border-strong", "surface-alt", 3.0),
    ("focus ring on bg", "focus", "bg", 3.0),
    ("focus ring on surface", "focus", "surface", 3.0),
]
ACCENT_PAIRS = [
    ("accent (link, live) on surface", "accent", "surface", 4.5),
    ("accent on bg", "accent", "bg", 4.5),
]


def lum(h):
    h = h.lstrip("#")
    r, g, b = (int(h[i:i + 2], 16) / 255 for i in (0, 2, 4))
    f = lambda c: c / 12.92 if c <= 0.03928 else ((c + 0.055) / 1.055) ** 2.4
    return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b)


def ratio(a, b):
    la, lb = sorted((lum(a), lum(b)), reverse=True)
    return (la + 0.05) / (lb + 0.05)


def checks(d):
    """Every pair for direction d in both themes: [(theme, label, fg, bg, ratio, need)]."""
    out = []
    for theme in ("light", "dark"):
        t = d[theme]
        for label, fg, bg, need in PAIRS + (ACCENT_PAIRS if "accent" in t else []):
            out.append((theme, label, t[fg], t[bg], ratio(t[fg], t[bg]), need))
    return out
