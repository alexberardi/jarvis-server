# Desktop tasks on Linux: what decides the window, the lock and "at the computer"

- **Date:** 2026-10-20 (plan P3 piece 35, agent's choices; the user asked that real users never need
  a `sudo pacman` for it)

Desktop tasks on Linux (`../agent/DESIGN.md` §12) run on Hyprland, sway and X11 through what the
agent already has: the compositor's IPC (`hyprctl`, `swaymsg`) and grim on Wayland, the built-in
X11 client on X11, AT-SPI for elements. Nothing new is shelled out to; grim is the one program a
Wayland task needs, and its absence is reported by name. tk and Xvfb are test fixtures only.

- **Window order and focus are separate.** The window list is the stacking order (fullscreen,
  floating, tiled), and the focused window is marked on it: on a tiling compositor the focused
  window can sit under a floating one. A click is checked against the window on top at the point,
  keys against the focused window. macOS marks none: its front window is the focused one.
- **A title can stand in for an app name** when no app has the name (every Tk app's class is
  `Tk`). Only for finding the window: the deny check still goes by its app, so a terminal titled
  "Notes" is still a terminal.
- **The lock is logind's LockedHint** for `XDG_SESSION_ID`, else for this process's session,
  else for the user's graphical session. The installed agent is a systemd user unit
  (`user@.service`), which is in no logind session and whose environment has no
  `XDG_SESSION_ID`, so it finds its session as `Manager.GetUser(uid)`'s `Display` (the session
  the user's desktop runs in). Only when the user has no graphical session (`Display` is `/`, or
  logind knows no such user: a headless test session, a container) is there no lock to see, and
  only the idle check applies, rather than refusing every task there. Any other logind failure
  (no system bus, a timeout, access denied) is an error, and the task refuses. So a suite run on
  a computer whose own screen is locked refuses its desktop cases too: the headless session it
  drives is not a logind session, and the user's desktop is the one asked about.
- **"At the computer" needs input the agent saw.** A compositor's idle notifications only report
  input after the monitor began, so the monitor starts with the agent, and a task with no input
  seen yet is refused as such. The computer suite gives its headless sessions a user: Shift,
  pressed through the agent's own input backend before each desktop case.
- **A bare X server** (Xvfb, a kiosk) has no window manager: its windows are the root's mapped
  children in the server's stacking order, none marked focused.
