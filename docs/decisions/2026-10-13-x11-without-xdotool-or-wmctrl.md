# X11 without xdotool or wmctrl

- **Date:** 2026-10-13
- **Moved from** the STATUS.md decision log on 2026-10-14.

**X11 without xdotool or wmctrl** ([agent/DESIGN.md §14](../agent/DESIGN.md), installers §9.3 item 6, now done for wlroots and X11). Typing through XTEST (spare-keycode remap with a settle, always restored; Shift and Caps Lock handled) and window list/focus/close/minimise plus the active window through EWMH, over `github.com/jezek/xgb` (pure Go). The helpers are fallbacks. Verified on headless Xvfb + openbox with xterm (UTF-8, CJK, all nine keys, a named app, every window action, 30/30 remap runs); not on a real X11 desktop.
