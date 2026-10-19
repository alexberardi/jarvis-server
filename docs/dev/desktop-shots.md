# Screenshots of the desktop agent (island and status icon)

`scripts/desktop-shot` runs `jarvis-agent` in a throwaway, headless Wayland desktop and takes
screenshots of it. An agent can then Read what the island and the status icon actually look
like, rather than only confirming that a window exists. Nothing appears on the user's screen,
in their tray or on their session bus.

## Run it (Linux)

```sh
scripts/desktop-shot deps                     # once: sway + swaybar (system, or built into ~/.cache)
scripts/sandbox up --name desk --llm none     # a jarvisd to enroll against (or use --offline)
scripts/desktop-shot --sandbox desk           # ~30 s: 2 themes x (icon + 9 island states)
scripts/desktop-shot --offline --states listening --bars dark   # no server, one shot
scripts/sandbox down --name desk --purge
```

The output goes to `.sandbox/<name>/desktop/<timestamp>/`. The script lists the files at the
end (see `--help`):

| File | What it is |
|---|---|
| `desktop__dark.png`, `desktop__light.png` | The whole 1920x1080 output: a full-screen window in the theme's colour, and the bar at the bottom with the icon at its right end. |
| `tray__<theme>.png` | The status icon, found by diffing the bar before and after the agent started, cropped and enlarged 4x. |
| `island__<state>__<theme>.png` | The island, found by diffing the top third of the screen, cropped. The states are `running` (the "Jarvis is running" pill, asked for over the control socket), `listening`, `thinking`, `speaking`, `follow_up`, `error`, and the computer-task indicators `controlling`, `paused` and `recording` (`island-demo --hold <state> --text <intent>`, a sample intent). |
| `desktop-island__<theme>.png` | The whole output with the last state showing, for placement. |
| `menu.txt` | The icon's StatusNotifierItem properties and its dbusmenu, read the way a tray host reads them (swaybar draws no menus). |
| `icon-pixmap.png` | The largest pixmap the icon offers (64x64). |
| `agent-<theme>.log`, `sway-<theme>.log`, `enroll.log` | Logs. |

The script exits 1 if the icon or any island state never showed up, so it also works as a
smoke check.

### How it stays isolated

- **Compositor:** sway runs with `WLR_BACKENDS=headless` and `WLR_RENDERER=pixman`, which
  opens no display, takes no DRM master, uses no GPU and reads no input devices
  (`WLR_LIBINPUT_NO_DEVICES=1`). Its Wayland and IPC sockets live in a fresh
  `XDG_RUNTIME_DIR` (`/tmp/jds.XXXXXX`, kept short because a socket path is limited to 108
  bytes). The script deletes that directory on exit.
- **Environment:** every process starts under `env -i`, so `WAYLAND_DISPLAY`,
  `HYPRLAND_INSTANCE_SIGNATURE`, `DBUS_SESSION_BUS_ADDRESS` and PipeWire's socket from the
  user's session are never inherited. `HOME` and the `XDG_*_HOME` directories point at
  `.sandbox/<name>/desktop/agent-home/`, so the user's `~/.config/jarvis-agent` is never
  read or written.
- **Session bus:** `dbus-run-session` gives the agent a private bus. Its StatusNotifierItem
  registers with the swaybar running inside the sandbox, not with the user's bar.
- **Voice:** the agent runs with `--voice=false`. There is no microphone in the sandbox, and
  the user's PipeWire is out of reach anyway.
- **Processes:** each themed session is started with `setsid`, so it is a single process
  group. The EXIT trap kills that group and nothing outside it.
- **States:** the island states come from `jarvis-agent island-demo --hold <state>` running
  in the same session, next to `run`. The pill comes from the control socket's
  `show-running`.

The helper `tools/desktopshot` (Go, unit-tested) handles the image diff and crop
(`diff --base A --img B --region x,y,w,h --out C`) and the D-Bus menu dump (`menu`).

### What has to be installed

You need grim, foot, dbus (`dbus-run-session`), util-linux (`setsid`), Go and
`/usr/bin/python3`, which Omarchy already ships. You also need **sway with swaybar**, which
Omarchy does not ship:

- `sudo pacman -S sway` is the one-line install. The script uses a system sway when one is on
  `PATH`.
- Without it, `scripts/desktop-shot deps` builds sway 1.12 with wlroots 0.20.2 as a static
  subproject into `~/.cache/jarvis-harness/desktop/`. meson and ninja go into a venv there,
  and nothing is installed system-wide. It needs no sudo, because everything it links against
  is already installed as part of Hyprland's dependencies. The build takes about 2 minutes,
  and the log is `~/.cache/jarvis-harness/desktop/build.log`.

### Why not headless Hyprland, waybar or Omarchy's own bar

- **Hyprland 0.56 can't run headless-only.** Its aquamarine headless backend has no DRM fd,
  so `CBackend::start` finds no allocator and fails (Hyprland's source has a "TODO: headless
  only"). The workaround would be to let it open `/dev/dri/card*`, the GPU that the user's
  session is driving, and running it nested would put a window on the user's screen. Both
  were ruled out.
- **The tray host:** Omarchy's bar is now quickshell (`omarchy-shell`), not waybar. It is
  bound to Hyprland's IPC, so it can't run inside sway. swaybar implements the same
  StatusNotifierItem host protocol and reads the same `IconPixmap`, so the icon's pixels are
  what Omarchy gets. The real bar's padding, icon size and menu styling are not covered.
  Running quickshell against a headless Hyprland would need the GPU caveat above solved.

## What the first run showed (2026-10-14)

- **Island:** all six states render correctly on both a dark (`#313244`) and a light
  (`#e5e9f0`) desktop. The pill is centred about 10 px below the top edge. Its dark fill and
  faint edge read well on both backgrounds. Each state has the colour the design calls for:
  the pill's text is "Jarvis is running" with a green dot, listening is blue with level bars,
  thinking is violet with flat bars, speaking is white, follow-up shows a countdown ring, and
  error is red. `follow_up` dims the pill on purpose (alpha 200, `render.go`). On the light
  desktop that makes it a noticeably greyer, lower-contrast pill than the other states. This
  is worth a look, but it is not a bug.
- **Status icon:** it registers and shows in the tray on both bar colours. The menu reads
  "Jarvis Automation Agent — Running", then the "Listen for "Hey Jarvis"" checkbox, then
  "Open Jarvis Automation" (once jarvisd's discovery answers; this item is missing with
  `--offline`), then Quit.
  **Finding:** at tray size (about 20 px on a 26 px bar), the shield is a plain downscale
  of the 64 px art (`tray.Color`, CatmullRom). It loses its white outline and pointed bottom,
  and reads as a purple rectangle with a white blob. On the dark bar the purple is also
  fairly low-contrast. A small-size variant hand-drawn for 16-24 px, or a thicker outline,
  would fix it. Left for the user to decide.

## macOS (not automated yet: needs the user)

An agent can't take screenshots on the Mac without the Screen Recording permission. Granting
that to the agent, or to Terminal, on the user's own account would let anything it runs
capture the user's real screen. The plan is a dedicated capture helper on a throwaway
account:

1. **A throwaway macOS user.** In System Settings → Users & Groups → Add User, create a
   Standard account, for example `jarvisshots`. It gets its own login keychain, its own TCC
   (privacy) database, its own menu bar and its own `~/Library`. Log into it once in the GUI
   so that its WindowServer session exists, then switch back with Fast User Switching. The
   account's session keeps running in the background.
2. **A dedicated capture helper, not the agent.** This would be a tiny signed command-line
   tool (proposed: `tools/macshot`, a Go program over purego calling
   `CGWindowListCreateImage`, or simply `/usr/sbin/screencapture` wrapped in an `.app`
   bundle with its own bundle id, for example `net.jarvisautomation.shots`). It needs its own
   bundle id because TCC grants are per app, so the grant covers only this helper. The agent
   (`net.jarvisautomation.agent`) must **not** get Screen Recording: its
   `desktop_read_screen` path stays under the user's own control.
3. **Grant it once, in the throwaway account only.** While logged into `jarvisshots`, run the
   helper once. Then, in System Settings → Privacy & Security → Screen & System Audio
   Recording, turn it on. This grant exists only in that account's TCC database. On macOS 15
   and later, the monthly "still allow?" re-prompt also appears only in that account.
4. **How an agent would use it:** over SSH as `jarvisshots`
   (`ssh jarvisshots@<mbp-host>`), install the agent built from the checkout as a test copy
   (with `JARVIS_AGENT_NO_AX_PROMPT=1`, as in the tray live check), then run
   `island-demo --hold <state>` and the helper. GUI processes started over SSH have to be
   started into the console user's session with `launchctl asuser $(id -u jarvisshots) …`,
   so `jarvisshots` must be the session that is logged in, though not necessarily the one in
   front. Whether a capture from a background (switched-away) session returns pixels or a
   blank frame on macOS 26 needs checking. If it returns a blank frame, the account has to
   be the one in front while capturing, which means a spare Mac or a time when nobody is
   using it.
5. **What the user has to do:** steps 1 and 3 (creating the account and the one toggle) and
   enabling Remote Login for that account. Everything else can be scripted once the helper
   exists. Don't grant anything on the user's main account.
