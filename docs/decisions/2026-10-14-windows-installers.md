# The Windows installers: names, ids and behaviour

- **Date:** 2026-10-14 (names, unsigned, firewall box, the data question: user, earlier; the rest:
  agent, building install plan item 2.3)

**Names (user):** `Jarvis Automation Server Setup <tag without v>.exe` (jarvisd, per machine) and
`Jarvis Automation Agent Setup <tag without v>.exe` (the desktop agent, per user). Unsigned, with
the SmartScreen text in [00-installers §8.3](../install/00-installers.md) "The Windows installers".
GitHub turns spaces in release asset names into dots, so the release publishes them as
`Jarvis-Automation-Server-Setup-<v>.exe` and `Jarvis-Automation-Agent-Setup-<v>.exe` (the
release.yml half, held for review).

**Choices made while building them:**

- **Inno Setup AppIds are the reverse-DNS ids:** `net.jarvisautomation.jarvisd` and
  `net.jarvisautomation.agent`. The uninstall entries are `…\Uninstall\<id>_is1`, which the
  updater (HKLM) and the agent's self-update (HKCU) rewrite with the version that runs. Changing an
  AppId later would leave the old entry orphaned, so they are fixed now; Go tests keep the `.iss`
  files and the Go constants in step.
- **The installers run the program's own command in `PrepareToInstall`**, from the payload in
  `{tmp}`, rather than having Setup copy the binary: `jarvisd install-from-app --bin …` and the new
  `jarvis-agent install --bin …`. Each keeps a newer binary already in place (a self-update is
  never undone by an older installer), and a failure (ports held by another program) stops Setup
  before anything is installed. Uninstall deletes the whole program folder, since its files are
  the programs', not Setup's.
- **No console window via a launcher, not `-H windowsgui` on the agent.** A GUI-subsystem
  `jarvis-agent.exe` would break its CLI: `cmd` doesn't wait for a GUI program, `enroll`'s password
  prompt would fight the shell for the console, PowerShell doesn't wait for one either, and
  Chrome starts native-messaging hosts through `cmd /c`. So the agent stays a console program and
  the installer adds `jarvis-agentw.exe`, a GUI launcher with no logic that starts it in a console
  with no window. It isn't self-updated: any launcher starts any agent.
- **The firewall rule is limited to the local subnet** (`remoteip=localsubnet`, on top of
  `profile=private`), for every `doctor --fix` on Windows, matching the `.deb`'s "local network
  only" (Q4). Existing rules are left as they are.
- **`/PURGE`** on a silent uninstall answers Yes to the data question of either uninstaller (the
  default stays No, Q6). The server's question reads "Also delete Jarvis Automation's data?" (the
  product name, per the naming rule).
- **The Start menu entry opens `http://localhost:<port>/`**, not the LAN address in the setup
  link, which can change. A desktop link to the admin is an unticked option.
- **Inno Setup 6.7.1, installed with choco** on the windows runner, pinned.

Plan: [01-no-cli-plan](../install/01-no-cli-plan.md) item 2.3.
