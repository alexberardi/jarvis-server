# Dictation on GNOME/KDE Wayland through the RemoteDesktop portal

- **Date:** 2026-10-13
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Dictation on GNOME/KDE Wayland through the RemoteDesktop portal** ([agent/DESIGN.md §14](../agent/DESIGN.md); installers §9.3 item 6 now done for every Linux desktop). `jarvis-agent dictation allow` shows the desktop's dialog once and keeps the restore token 0600; each typing call is a short portal session (`NotifyKeyboardKeysym`), restored without a dialog; a rejected token is forgotten and `status` names `allow` again. Named apps stay refused there. `github.com/godbus/dbus/v5`. Verified against the real xdg-desktop-portal 1.22 frontend + permission store on a private bus with a fake backend, including the agent CLI end to end; **not yet on a real GNOME or KDE desktop** (user checklist in the PR).
