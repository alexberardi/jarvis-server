# The macOS apps and disk images: names and behaviour

- **Date:** 2026-10-15 (agent, building install plan item 2.2; within the plan's decided shape)

**Names.** `Jarvis Automation Server.app` (bundle id `net.jarvisautomation.server`) and
`Jarvis Automation Agent.app` (`net.jarvisautomation.agent`, decided Q1). The disk images are
`Jarvis-Automation-Server-<tag without v>.dmg` and `Jarvis-Automation-Agent-<tag without v>.dmg`,
with volumes named "Jarvis Automation Server" and "Jarvis Automation Agent". Hyphens, as for the
Windows installers, because GitHub turns spaces in release asset names into dots. They replace the
plan's `Jarvis-Server-<v>.dmg` / `Jarvis Server.app` (the naming rule says "Jarvis Automation" for
the product). The server's LaunchDaemons name `net.jarvisautomation.server` in
`AssociatedBundleIdentifiers`, so "Allow in the Background" shows the app, not a bare label.
Existing macOS installs read as stale until their next refresh (item 1.3 does it on the next
admin-button upgrade).

**The server app is a launcher beside the release's jarvisd.** Its executable is
`cmd/jarvis-server-app`. `Contents/MacOS/jarvisd` is the release's own notarized binary, unchanged,
so `/usr/local/bin/jarvisd` matches the archive, its firewall allow entry stays the same, and the
updater keeps swapping it (not SMAppService, as the plan says). The launcher asks with NSAlerts over
purego. The first-open dialog has the ticked "Let devices on my network reach it" box. Root comes
from `do shell script … with administrator privileges` (the standard password dialog), which runs
`install-from-app`.

Later opens open the setup link or the admin. Two cases ask first:

- After an admin Stop, the app offers **Start**.
- When the app carries a newer jarvisd than the installed one (the person downloaded a newer dmg),
  it offers **Update**. The update passes `--no-firewall`, so the install-time firewall choice
  stands.

**Uninstall is Option-open** (hold Option while opening the app): Open, or Uninstall…. Uninstall
runs `service uninstall` and removes the binary, as `install.sh --uninstall` does. It asks "Also
delete … data?" with **Keep data** as the default (Q6). The plan's "app menu" would need the
launcher to stay running with a menu bar. Option-open is the cheaper equivalent. It isn't
discoverable, so the release notes and the admin should say it. The stopped-server dialog also
offers Uninstall….

**dmgbuild, pinned with hashes, makes the window layout.** It writes `.DS_Store` (background,
icon positions, the Applications link) without driving Finder. Finder scripting needs a GUI
session and an Automation grant, and it flakes on CI. dmgbuild runs `hdiutil` itself. It and its
two pure-Python dependencies are pinned by version and SHA-256 in
`scripts/packaging/macos/dmg-requirements.txt` and installed into a throwaway venv. The images
are ULFO (lzfse, macOS 10.11+).

**Bundle versions are numeric.** `CFBundleShortVersionString` and `CFBundleVersion` are the tag's
numbers only (`v0.1.0-rc1` → `0.1.0`), as Apple requires. Self-update compares the program's own
`version`, never the plist.

**Info.plist usage strings for Accessibility and Screen Recording** are informational. macOS
shows no prompt text for those two grants: they are switches the setup page opens. The agent has
them so the bundle says what each permission is for.

Plan: [01-no-cli-plan](../install/01-no-cli-plan.md) items 2.2 and 2.5; built as described in
[00-installers §8.4](../install/00-installers.md) ("The macOS apps and disk images").
