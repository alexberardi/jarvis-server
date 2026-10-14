# Dictation types on Hyprland/sway without wtype

- **Date:** 2026-10-13
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Dictation types on Hyprland/sway without wtype** ([agent/DESIGN.md §14](../agent/DESIGN.md), installers §9.3 item 6). The agent speaks the Wayland virtual-keyboard protocol itself (`internal/agent/vkbd`: per-text XKB keymap in a memfd, round trip + 40 ms settle before the first key, batches of ≤ 247 keys so keycodes stay ≤ 255), preferred whenever the compositor offers it; wtype/ydotool are fallbacks. The Wayland wire is now one package shared with the island (`internal/agent/wlwire`). Verified on a headless nested sway (UTF-8, CJK, all nine keys, a named app, 80/80 first characters); not run on Hyprland (same protocol).
