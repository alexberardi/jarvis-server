# The Debian/Ubuntu packages: names and behaviour

- **Date:** 2026-10-14 (package names: user; the rest: agent, building install plan item 2.4)

**Names (user):** `jarvis-automation-server` holds jarvisd, `jarvis-automation-agent` the desktop
agent, each for amd64 and arm64. They replace the plan's `jarvisd_<v>_<arch>.deb` and
`jarvis-agent_<v>_<arch>.deb`. Files are `<package>_<tag without v>_<arch>.deb`; the package
version is the tag with its first `-` as `~`, so `0.1.0~rc1` sorts before `0.1.0`. Built with nfpm,
pinned (v2.48.0, through `go run`), and listed in the release's `SHA256SUMS` under the same
minisign signature as the archives.

**Choices made while building them:**

- **Remove takes the tagged firewall rules away, not only purge.** `prerm remove` runs `jarvisd
  service uninstall`, which deletes the rules tagged `jarvisd`, as `install.sh --uninstall` does:
  no port stays open with nothing listening, and a reinstall opens it again. Purge keeps a
  tag-only sweep for rules a failed removal left. Data, `/etc/jarvisd` and the `jarvisd` account
  stay until purge (Q6).
- **The browser opens only for the setup link.** An upgrade or a reinstall of a set-up server
  opens nothing. The link opens in the active local graphical session's browser, started through
  that user's own systemd manager, so it is not killed with PackageKit's daemon.
- **"Jarvis Automation Server" in the app menu** opens the setup link (or the admin) through
  `pkexec`, since the link's token is root-only. It asks for a password even after setup; an
  unprivileged way to find the admin URL can come later.
- **The agent's menu entry names its icon** (`Icon=net.jarvisautomation.agent`, plus `Keywords`)
  for the per-user copy too, so a person's own entry, which replaces the package's after their
  first launch, looks the same. Only the `.deb` installs the icon; the one-liner's entry falls back
  to the theme's generic one.
- **Maintainer:** `Jarvis Automation <help@jarvisautomation.io>` (user, 2026-10-15: the support address). `DEB_MAINTAINER` overrides it.
  (`DEB_MAINTAINER` overrides it). Change it if the project gets a packaging address.

Built as described in [00-installers §8.3](../install/00-installers.md) ("The Debian/Ubuntu
packages"); plan: [01-no-cli-plan](../install/01-no-cli-plan.md) item 2.4.
