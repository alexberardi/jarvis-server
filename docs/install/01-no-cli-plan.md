# install 01: no-terminal installers, the buildable plan

> **Plan, 2026-10-13. Nothing here is built yet.** It joins up [00-installers.md](00-installers.md)
> §8 (what's built: `install.sh`/`install.ps1`, `jarvisd service`, the updater) and §9 (the
> no-terminal goal and its decisions) with [agent/DESIGN.md](../agent/DESIGN.md) §13 (agent install
> and pairing) and §14 (dictation). Those sections hold the reasoning. This doc says what to build,
> in what order, and how to sign it off. Where it disagrees with them, §7 lists the conflict.

**Goal (user, 2026-10-12):** "absolutely 0 cli involvement". A user downloads a file, double-clicks
it, and finishes in a browser. That covers install, first run, pairing, permissions, updates and
uninstall. The one-liners stay as "Advanced".

**Out of scope:** the Pi voice node (user, 2026-10-13). Neither the Python node's installer nor the
Go Pi node is planned here.

---

## 1. Inventory: what a user installs, and where a terminal is still needed

| Component | Ships today as | Terminal touchpoints today |
|---|---|---|
| **jarvisd** (server) | `install.sh` / `install.ps1` one-liners; release archives (§8.3) | the one-liner itself; `sudo`/elevation; the `[Y/n]` firewall prompt (`doctor --fix`); **reading the setup link** (`jarvisd setup-link`, printed by the script; the token file is 0600 and root- or service-owned); `jarvisd service start/stop/status` after an admin Stop (§8.1, AD8b); `sudo jarvisd service install` to refresh a stale definition after an admin-button upgrade (§8.2); `--stop-legacy`; uninstall (`install.sh --uninstall [--purge]`); `jarvisd doctor` / logs (`journalctl`, `<home>/logs`) |
| **jarvis-agent** (desktop: voice, browser control, read the screen, dictation, island) | **not released** (PLAN layout, release.yml builds jarvisd only); built from source or by `scripts/agent-dev-run.sh` | download/build; `jarvis-agent enroll --server … --email …` (types a password); `jarvis-agent browser install`; `jarvis-agent dictation allow` (GNOME/KDE Wayland, §14); `agent-dev-run.sh --install` for autostart (systemd user unit, LaunchAgent, logon task from `internal/agent/deploy/`); `run --voice` is a flag, so voice on/off is a command line; macOS permission grants with no guidance; `status`; upgrades (rebuild); uninstall (`--uninstall`, by hand) |
| **Browser extension** | signed unlisted `.xpi` embedded in the agent (0.1.0); Chrome "Load unpacked"; store kit ready ([store-listing.md](../agent/store-listing.md)) | none in the store flow; today it is reached only through `jarvis-agent browser install`, and Chrome needs developer mode |
| **Mobile app** | app stores | none. Already fine |

## 2. Target experience, per component and OS

Shared rules, so the installers stay thin (IQ3: logic lives in Go and runs on the four CI runners):

- **Every server installer ends in the same three calls:** `jarvisd service install`, `jarvisd doctor
  --fix` (when the firewall box is ticked), and `jarvisd setup-link`. Those are the calls `install.sh`
  already makes. Legacy-stack detection stays in the scripts (§5.2). A GUI installer that finds a
  port held by another program shows the doctor's message and stops.
- **Every agent installer ends in `jarvis-agent install`.** This is a new Go subcommand that does the
  per-user setup: copy or keep the binary, register the login item, create the menu entry and
  optional desktop shortcut, register the native-messaging host (by default, DESIGN §13), then
  start the agent. It replaces `agent-dev-run.sh --install` and DESIGN §9's open question 9.
  `jarvis-agent uninstall` reverses it. The templates in `internal/agent/deploy/` become its
  golden files.
- **The binary path stays what the updater expects.** The server binary lives in `/usr/local/bin` or
  `%ProgramFiles%\jarvisd` (§2.0). The agent's binary is user-owned, so it can swap itself in
  process, the way `--user` mode does (§8.2).

### 2.1 jarvisd

| | macOS | Windows | Debian/Ubuntu | Fedora / Bazzite / immutable | Arch / other |
|---|---|---|---|---|---|
| **Artifact** | `Jarvis-Server-<v>.dmg` (decided, §9.3 Q1) | `Jarvis-Server-Setup-<v>.exe` (unsigned, decided) | `jarvisd_<v>_<arch>.deb` (decided) | the one-liner (decided, §9.3 Q5) | the one-liner (decided) |
| **Tooling** | Wrap `jarvisd` in `Jarvis Server.app` (Info.plist, a tiny launcher), build the `.dmg` with `hdiutil` (or `create-dmg` for the background and Applications link), `codesign` it with the existing Developer ID, then `notarytool` and **`stapler staple`**. A stapled dmg/app works offline on first open, which the bare binary can't (§8.4). No `.pkg` | **Inno Setup** (`iscc`). It's free and scriptable, handles UAC, adds the Apps & features entry, supports `/VERYSILENT` for CI, and installs on the windows runner with `choco install innosetup`. Not WiX: an MSI buys Group Policy deployment nobody needs and costs far more authoring. Not NSIS: same result, weaker scripting | **nfpm** (Go, one YAML, runs on ubuntu-latest), checked with `lintian` | `install.sh` | `install.sh` |
| **Click by click** | Open the dmg → drag to Applications → open it (Gatekeeper: "downloaded from the internet… Open") → a dialog: "Jarvis Server runs in the background on this Mac. ☑ Let devices on my network reach it [Install]" → the admin password dialog → the browser opens the setup link | SmartScreen (§2.3 below) → UAC ("Unknown publisher") → wizard page: ☑ Let devices on my network reach Jarvis, ☐ Desktop shortcut to the admin → Install → Finish with ☑ "Open Jarvis setup" | Double-click → App Center / GNOME Software → **Install** → polkit password → the browser opens the setup link (best effort, see below); the app menu has "Jarvis Server" | One paste into a terminal, then the browser or the QR code | Same |
| **How it installs** | The app runs `do shell script "<bundle>/jarvisd install-from-app --run-as <user>" with administrator privileges`: copy to `/usr/local/bin/jarvisd`, `service install`, `doctor --fix`, then return the setup link to the app, which opens it. **Not SMAppService:** a daemon registered from the bundle must run the bundle's binary, which the updater (§8.2) can't swap without breaking the bundle signature | `[Files]` → `{commonpf}\jarvisd\jarvisd.exe`; `[Run]` `service install`, `doctor --fix`, and `setup-link --url` written to a temp file; Finish opens the URL with `runasoriginaluser` | **Bootstrap layout:** the package carries the binary at `/usr/lib/jarvisd/jarvisd`. `postinst` copies it to `/usr/local/bin/jarvisd`, which dpkg doesn't own, so the updater can swap it; it then runs `service install`, and `doctor --fix` (see Q4). The package's own version goes stale after the first self-update, which is harmless | §8.3 | §8.3 |
| **Where** | §2.0: `/usr/local/bin`, LaunchDaemon as the user, `~/.jarvisd` | §2.0: `%ProgramFiles%\jarvisd`, `%ProgramData%\jarvisd` | §2.0 system unit, `/var/lib/jarvisd` | §2.0 | §2.0 |
| **Service** | `jarvisd service install` (built). Add `AssociatedBundleIdentifiers` to the plist so macOS's "Allow in the Background" list says *Jarvis Server*, not a bare label | built (SCM + `jarvisd-updater`) | built (system unit + `ExecStartPre` helper) | built | built |
| **First run** | The browser opens the setup link. Later launches of the app open the admin, and offer Start (after an admin Stop) and Uninstall | The Finish page opens the setup link. A Start-menu "Jarvis Server" opens the admin | `postinst` opens the setup link in the active graphical session (`loginctl` → `runuser` + `xdg-open`). If that fails, the menu entry "Jarvis Server" runs `pkexec jarvisd setup-link --url`, which shows a polkit dialog, then `xdg-open`s the link as the user. After setup the same entry opens the admin | `install.sh` prints the link and a QR code (decided, §9.3 Q4) | Same |
| **Updates** | Admin Updates page (built, §8.2). Close the remaining gap: a definition left stale by an admin-button upgrade is refreshed by the privileged helper (S-item 1.3), so nobody runs `sudo jarvisd service install` | Same; the helper also writes `DisplayVersion` into the Inno uninstall key | Same; the root `ExecStartPre` helper can re-render the unit (§8.2 judged this "not worth the risk"; Q7) | built | built |
| **Uninstall** | The app's menu → "Uninstall Jarvis Server…" → admin password → `service uninstall` → "Also delete Jarvis data (N GB)?" defaulting to **No**. Dragging the app to the Trash leaves the service running, as with any Mac app that has a daemon, so the app says so | Apps & features → Uninstall → `[UninstallRun]` `service uninstall`, then the same data question, defaulting to No | Software → Remove → `prerm` `service uninstall` (data kept; `apt purge` purges) | `install.sh --uninstall` | Same |
| **Unavoidable** | Gatekeeper's first-open confirmation; the admin password; macOS 13+ "Background item added" notification; the application firewall's allow is applied by `doctor --fix`, so no prompt | SmartScreen (wording below); UAC "Unknown publisher"; possibly a Defender false positive on an unsigned Go binary (submit it to Microsoft if it happens) | polkit password; the App Center may warn about a third-party `.deb` (check the wording on 24.04) | SELinux untested (§9.3 Q5) | — |

**Headless Linux:** `.deb` over SSH, or the one-liner. Both print the QR code (decided, §9.3 Q4).

**Windows SmartScreen text for the download page** (decided wording style, DESIGN §13 Q3): "Windows
may say *Windows protected your PC*. Jarvis is open source and not code-signed with a paid
certificate. Click **More info**, then **Run anyway**. To check the file first, compare it with
`SHA256SUMS` on the release page (signed with the project's minisign key)."

### 2.2 jarvis-agent

| | macOS | Windows | Debian/Ubuntu | Fedora / Bazzite / immutable | Arch / other |
|---|---|---|---|---|---|
| **Artifact** | `Jarvis-Agent-<v>.dmg` (DESIGN §13) | `Jarvis-Agent-Setup-<v>.exe`, **per user** (no UAC) | `jarvis-agent_<v>_<arch>.deb` | the agent one-liner (`install-agent.sh`: download, verify, `jarvis-agent install`) (Q8) | the same one-liner |
| **Tooling** | `Jarvis Agent.app` (exists for dev: `deploy/macos/Info.plist`) signed with hardened runtime and entitlements `automation.apple-events`, `device.audio-input` (the microphone under the hardened runtime), and `disable-library-validation` (embedded ORT/sherpa dylibs, as for jarvisd §8.4); dmg as for jarvisd; notarize and staple | Inno Setup, `PrivilegesRequired=lowest` | nfpm; `Recommends: wtype, xdotool, wmctrl` only while built-ins don't cover them (§9.3 item 6, mostly done) | — | — |
| **Where** | `/Applications/Jarvis Agent.app` | `%LocalAppData%\Programs\Jarvis Agent\` | The package ships `/usr/lib/jarvis-agent/jarvis-agent`. First launch from the menu runs `jarvis-agent install`, which copies it to `~/.local/lib/jarvis-agent/`, a user-owned, self-updatable copy (Q5) | `~/.local/lib/jarvis-agent/` | Same |
| **Login item** | **SMAppService.agent** (macOS 13+, purego like the island): the LaunchAgent plist lives inside the bundle, so trashing the app removes the login item, and System Settings shows "Jarvis Agent" | HKCU `Run` key (works without admin). The `deploy/windows` logon task is the fallback if `Run` proves too early for audio | systemd user unit (`deploy/systemd`). **Risk:** `graphical-session.target` is never reached on compositors started without uwsm (plain Hyprland), so an XDG autostart entry is the fallback; test on the Hyprland box | Same | Same |
| **Shortcut** (DESIGN §13 item 6) | The app itself (Dock, Launchpad). Opening it starts the agent or flashes the island pill | Start menu + ☑ desktop shortcut | `~/.local/share/applications` entry + optional desktop icon | Same | Same |
| **First run** | The first-run browser page (decided, §13 Q1) at `http://127.0.0.1:<port>`. See below | Same | Same | Same | Same |
| **Updates** | Self-update through `internal/update` (decided in principle, §9.3 Q3; item 1.7). macOS replaces the whole signed `.app` from a zipped bundle listed in `SHA256SUMS`, so TCC grants (keyed on team + bundle id) survive | In-process swap (user-owned dir); the uninstall key's `DisplayVersion` is updated | In-process swap of the `~/.local` copy | Same | Same |
| **Uninstall** | Drag to Trash: the login item goes with it. Leftovers (native-host manifests, `~/Library/Application Support/jarvis-agent`, the node record) are removed by "Remove Jarvis Agent…" on the first-run page; otherwise they are inert | Apps & features → `jarvis-agent uninstall` (login item, host keys, shortcuts) | Software → Remove removes `/usr/lib`. Per-user copies are removed by "Remove Jarvis Agent" (menu entry and first-run page), because `prerm` can't reach each user's home | `jarvis-agent uninstall` via the page's Remove button | Same |
| **Unavoidable** | Gatekeeper first open; TCC prompts for Microphone, Accessibility (dictation, window control), Screen Recording (read the screen) and Automation (System Events). The page opens each pane and ticks live | SmartScreen; Defender may scan; typing into elevated windows is impossible (UIPI, §14) | — | **GNOME/KDE Wayland:** the RemoteDesktop portal dialog, once (§14). The first-run page offers it as a step (§14 Q7, recommended yes); `dictation allow` stays for the CLI. No browser control from Flatpak Firefox (DESIGN §10, accepted for Bazzite) | — |

**Flatpak and AppImage for the agent: no.** A Flatpak would need host escapes for everything the agent
does: launching host apps, `hyprctl`/EWMH window control, writing native-messaging manifests into
other apps' directories, its own login item. Flathub rejects that, and the sandbox would add
nothing. An AppImage is just our static binary in a wrapper, and still needs "Allow executing"; it
waits until someone asks (§9.3 Q1), and Q8 revisits it after the Bazzite sign-off.

**First-run page** (DESIGN §13 target flow, step 3; decided toolkit: the browser), served by the
agent on loopback with a per-launch secret in the URL:

1. **Server:** found over mDNS, or `127.0.0.1:7700` first when jarvisd runs on this machine. A manual
   URL field is the escape hatch.
2. **Pairing:** a device code and this computer's name. The page says "Approve it in Jarvis →
   Add-ons, or in the app", then turns green when the code is approved.
3. **Browser control:** the native host is already registered (decided). Buttons go to the store
   listing for each installed browser: AMO, Chrome Web Store. Each turns green when the
   extension connects.
4. **Permissions:** macOS panes with live ticks, or the GNOME/KDE portal step.
5. **Voice:** a microphone level meter and a wake-word toggle, stored in a new agent config file
   (`run --voice` stops being a flag the user must know). **Done 2026-10-13:** listening is on by default and the status icon's menu toggles it (item 1.11).
6. **Done:** "Jarvis is running", plus the island pill.

### 2.3 Browser extension

Store listings on AMO and the Chrome Web Store (decided, DESIGN §13 Q4). Chrome's listing also
covers Edge, Brave, Arc and Vivaldi. The no-terminal flow never uses the embedded `.xpi` or Load
unpacked. Updates come from the stores. Uninstall uses the browser's own UI. The extension popup
links to Add-ons when no agent answers (§13 item 5). **Blocked on the user:** upload **0.2.0** to
both stores (STATUS).

### 2.4 Mobile app

App stores; nothing to build for install. It gains "Add a computer" and pairing approval (DESIGN
§13 step 1).

## 3. Server and agent together, and the Add-ons tab

### 3.1 The wizard's optional step (user, 2026-10-13)

A step after Privacy, before Done, titled "Use Jarvis on this computer". It is skippable, and it
says "you can do this later in **Add-ons**". It has two checkboxes:

- ☐ **Use Jarvis on this computer (desktop agent)**
- ☐ **Let Jarvis control this browser (extension)** (enabled once the agent is ticked; extension
  without agent can't work)

With the agent ticked, the step runs §9.1's sequence, keeping §9.2's constraint (the service can't
install into the user's session):

1. **Download:** `GET /api/addons/agent/{os}/{arch}` (OS and arch from the browser's
   User-Agent/Client Hints, with a picker for other OSes). jarvisd fetches the release asset that
   matches **its own version**, verifies it against `SHA256SUMS` + `.minisig` with its compiled-in
   key (the updater's check, §8.2), caches it under `<home>/updates/agent/`, and streams it to the
   browser. Offline: a link to the release page.
2. **Install:** the user opens the download (§2.2). The agent finds jarvisd on loopback first.
3. **Pair:** the step polls pending pairings. The request shows its code and computer name **on
   both screens**, and the user clicks Approve (contradiction 3 in §7). The household is the one
   setup just created, and the room is "desktop".
4. **Extension:** the store button for the detected browser. It turns green when the agent's
   heartbeat reports the extension connected (item 1.8).
5. **macOS:** a note that the agent's own page handles permissions (it runs as the user and can see
   the grants; jarvisd can't).

Each tick goes green live by polling. Leaving the step at any point is fine.

### 3.2 The Add-ons tab (user, 2026-10-13)

It replaces DESIGN §13 item 5's "Desktop & browser" checklist name, and keeps its two-step
agent → extension flow with live green checks. Every household member reaches it. **Dependency:**
member access arrives with the member-facing web app (the admin opened to members as jarvis-web's
replacement; decided 2026-10-13, scoped separately). This plan only requires that the Add-ons BFF
routes are gated **server-side** to the caller's own households, never by hiding nav.

Layout:

```
Add-ons
This computer                                   (detected; see "Installed" below)
  ✓ Jarvis desktop agent      Office-PC · online           [Download for macOS ▾]
  Browser extension           [Add to Firefox] [Add to Chrome]   (link only, no check; Q2)
Your devices                                    (every desktop agent in your households)
  Office-PC     online · extension ✓            Laptop   offline 3 d · extension –
  [+ Add a computer]  → download + pairing approval
Phone app                                       [App Store] [Google Play]  [QR]
```

**"Installed", per device:**

- **Your devices** is exact. It comes from jarvisd's node list (`install_mode: desktop-agent`;
  online from the heartbeat; extension state from the agent's heartbeat capability, item 1.8),
  for the member's own households only. New BFF route: `GET /api/addons/devices`.
- **This computer** has to be inferred. The page can't tell which computer it's on. The user
  proposed detecting the extension from the page via `externally_connectable`, which can't work
  here: Chrome's `externally_connectable.matches` can't name a self-hosted LAN host unknown at
  build time; Firefox doesn't implement it; and it breaks the extension's audited surface
  (DESIGN §10 Security, `TestExtensionSurface`). **Recommendation (Q2):** the server
  correlates. `GET /api/addons/this-device` compares the request's remote address with each of
  the member's desktop agents' last MQTT/heartbeat address. Exactly one match means "This computer
  is Office-PC". Loopback on the server machine matches. None or several matches (NAT, a reverse
  proxy, VMs) show "We can't tell which computer this is", with the download button. The
  extension row then reads from that agent. An alternative, which reopens the §10 surface, is in
  Q2.
- **Before any agent exists:** "This computer" shows only the download for the detected OS.

**Mobile app in the tab: yes, as a link card** (store badges plus a QR code of the store link,
not a pairing QR), with no installed check. Cheap, and it is the one place a member finds the
app. **Pi nodes: not listed** (out of scope; the Nodes page stays).

**Naming:** "Add-ons" already appears in the admin copy for other things. ConnectionsPage.tsx:337
uses it for external services, and wizard `privacy.ts:46` says "add-on commands" for Pantry
packages. The mobile app calls Pantry items **Packages**, and the browser stores call the
extension an add-on, so the user's name fits if those two strings change ("External services",
"Pantry packages"). Q3.

### 3.3 The agent on its own

The landing page and the release page carry the agent installers (public assets, decided §13 Q2).
From another computer, the flow is install → the first-run page shows a code → any member approves
it in Add-ons or the app → done.

## 4. Pi node

Out of scope (user, 2026-10-13).

## 5. Work breakdown

Sizes: S ≤ 1 day, M ≤ 1 week, L > 1 week of agent work. **(User)** marks the steps only the user can
do. Each item lands green on `main` on its own (the §8 style).

### Phase 1: Go foundations (parallel worktrees)

| # | Item | Size | Depends on | CI | Tests |
|---|---|---|---|---|---|
| 1.1 | **Done (PR #101, 2026-10-13; DESIGN §5 item 3).** **Device-code pairing:** jarvisd `POST /nodes/pairings` (start, returns code + poll id, 10 min expiry), poll, member-scoped approve (choose household + room; DESIGN §13 Q2); `jarvis-agent enroll --code`; rate limit + single use | M | — | `build`, `agent` | Unit: expiry, reuse, cross-household approve refused, a non-member refused; contract test for the routes |
| 1.2 | **Done (PR #105, 2026-10-13).** `jarvisd setup-link --url` (machine output) and `--qr` (decided, §9.3 Q4; Unicode half-blocks); `install.sh`/`.ps1` print the QR | S | — | `install` | Golden QR, decodes back to the link |
| 1.3 | Stale definition refresh from the macOS/Windows helpers after an admin-button upgrade (closes §8.2 "Not covered"), behind Q7 for Linux | M | — | `upgrade` (3 OS) | Upgrade e2e asserts `definition_stale: false` after the admin button |
| 1.4 | `jarvis-agent install / uninstall` (§2 shared rules): login item per OS (SMAppService on macOS), menu entry + shortcut, native host by default, start; idempotent. **Done (#102):** plus the `jarvis-agent://` handler and the Linux per-user copy; DESIGN §13 "`jarvis-agent install`" | M | Q1 | `agent` (3 OS) | Golden files from `internal/agent/deploy/`; scratch HOME/HKCU fake registry; install twice, uninstall leaves nothing |
| 1.5 | **First-run page** (§2.2): loopback server with a per-launch secret, server discovery (loopback first), pairing, store buttons, macOS TCC panes with live status, GNOME/KDE portal step, mic meter, wake toggle, Remove | L | 1.1, 1.4, 1.6 | `agent` | Handler tests; headless browser test of the pairing happy path against a test jarvisd; TCC probes are already native-tested |
| 1.6 | Agent config file (voice, island, confirm-dictation) read by `run`; flags still win. **Done (#99):** `agent.json` in the agent's config dir, `internal/agent/settings`, `jarvis-agent config` (DESIGN §13 "The settings file"); `jarvis-agent restart` applies a change (DESIGN §13) | S | — | `agent` | Unit |
| 1.7 | **Agent self-update** via `internal/update`: agent archives in `SHA256SUMS`, check on heartbeat `pending_update` or daily, in-process swap; macOS `.app` replacement | M | 2.1 | `agent`, a new `agent-upgrade` e2e (throwaway key, as `scripts/testsign`) | Swap, refusal of unsigned/older, rollback on failed start |
| 1.8 | **Done (PR #104, 2026-10-13).** Agent heartbeat: `node_kind: desktop` (DESIGN §9 Q1) + `browser_extension: connected` capability | S | — | `agent`, `build` | Contract: node list carries both |
| 1.9 | Start-only shortcut: second launch → IPC to the running agent → island "Jarvis is running" pill (DESIGN §13 item 6). **Done (#103):** control socket + single instance, `start` (also the URL handler), first-run hook stubbed for 1.5; DESIGN §13 "The start-only shortcut". **Mac fixes (#106):** the pill asked for before the island exists, and opening the app or link while the agent runs (Apple events); DESIGN §13 "Fixed on the Mac" | S | 1.4 | `agent` | Single-instance test; island golden for the new pill |
| 1.10 | `jarvisd install-from-app` (the macOS app's privileged entry point) and the Windows/`.deb` hooks: thin wrappers over `service install` + `doctor --fix` + `setup-link --url` | S | 1.2 | `install` | Run under sudo on the macOS and Ubuntu runners |
| 1.11 | **Quit without a terminal** (user, 2026-10-13: "there needs to be an easy way to kill the app as well"): `jarvis-agent stop` (exit 0, so no supervisor restarts it), then a status icon with a menu (state, Listen for "Hey Jarvis", Open Jarvis Automation, Quit) per OS. **Done.** `stop` (#109); DESIGN §13 "`jarvis-agent stop`". Status icon on Linux + the live voice toggle + voice on by default: #110; macOS menu bar: #111; Windows notification area: #114 (DESIGN §15).** | S | 1.9 | `agent` | Unit (install.Stop per OS); a scratch systemd unit not respawned after `stop` |

### Phase 2: release artifacts

| # | Item | Size | Depends on | CI | Tests |
|---|---|---|---|---|---|
| 2.1 | **Agent in the release:** `release.yml` builds `jarvis-agent` for linux amd64/arm64, darwin arm64 and windows amd64 (ORT embedded), adds the archives to `SHA256SUMS` + `.minisig` | M | — | `release` dry run | `verify` job starts each agent with `version` |
| 2.2 | **macOS bundles + dmgs:** `Jarvis Server.app` and `Jarvis Agent.app`, entitlements (§2.2), sign, dmg, notarize, staple, in `sign-macos` | M | 2.1, 1.10, Q1 | `sign-macos`, `verify` (`spctl -t open`, `stapler validate`, quarantined-copy watchdog as §8.4) | Dry run `v0.0.0-dmg1` |
| 2.3 | **Windows Inno Setup scripts:** server (per machine) and agent (per user), built on windows-latest | M | 2.1, 1.4, 1.10 | new `installers` job (windows) | `/VERYSILENT` install → service Running + `/health`; agent Run key + process; silent uninstall leaves ProgramData only |
| 2.4 | **nfpm `.deb`s:** server (bootstrap `postinst`/`prerm`) and agent; `lintian` | M | 2.1, 1.4, 1.10 | `installers` (ubuntu): `apt install ./…deb` on the runner (systemd present), then `apt remove` | Service healthy, binary in `/usr/local/bin`, remove keeps `/var/lib/jarvisd`; `debian:13`/`ubuntu:24.04` containers for install/remove without systemd |
| 2.5 | `installers` smoke on macOS: mount the dmg, run `install-from-app` under the runner's passwordless sudo (the GUI password dialog itself is manual), then uninstall | S | 2.2 | `installers` (macos-14) | As §8.3's install job |
| 2.6 | `install-agent.sh` (Q8) for Fedora/Bazzite/Arch | S | 2.1, 1.4 | `installers` (ubuntu, a fedora container for the script path) | shellcheck; scratch HOME |

### Phase 3: UI

| # | Item | Size | Depends on | CI | Tests |
|---|---|---|---|---|---|
| 3.1 | BFF: `/api/addons/agent/{os}/{arch}` (fetch, verify, cache, stream), `/api/addons/devices`, `/api/addons/this-device`, pairing list/approve; member-gated **server-side** | M | 1.1, 1.8, 2.1 | `build`, `admin` | Unit with fake GitHub (as §8.2); a member sees only their households; an unsigned asset is refused |
| 3.2 | **Add-ons tab** (§3.2) | M | 3.1, Q2, Q3 | `admin` (vitest) | Live ticks with a mocked poll; "can't tell" state |
| 3.3 | **Wizard step** (§3.1) | M | 3.1 | `admin` | Wizard test: skip, agent-only, agent + extension |
| 3.4 | Mobile: "Add a computer" + pairing approval (jarvis-node-mobile) | M | 1.1 | mobile CI | Jest; physical-device pass |
| 3.5 | Extension popup → Add-ons link; ship as 0.2.x | S | — | `browser-ext-lint` | `TestExtensionSurface` unchanged |
| 3.6 | Landing page (jarvis-installer `feat/jarvisd-landing`): OS-detected buttons for server and agent, "Advanced (terminal)", the SmartScreen note | S | 2.2–2.4 | that repo | Manual |

### Phase 4: the user's part, and sign-off

| # | Item | Who |
|---|---|---|
| 4.1 | Confirm the Apple secrets (§8.4) also cover the new bundles. No new certificate: dmgs and apps sign with the same Developer ID Application, and there is no `.pkg`, so no Installer cert | **User** (check only) |
| 4.2 | Chrome developer registration; upload 0.2.0 to the Chrome Web Store and AMO; re-sign the embedded `.xpi` | **User** |
| 4.3 | Decide Q1–Q8 (§6), one at a time | **User** |
| 4.4 | **Clean-install sign-off** (STATUS checklist), each on a fresh machine or a fresh user account, starting from the landing page with no dev tools. The flow is install jarvisd → wizard (with the agent step) → add a computer → extension from the store → first voice command → admin Update → uninstall | **User** |

Sign-off matrix:

| Machine | Server | Agent | Extension | Notes |
|---|---|---|---|---|
| macOS (Apple silicon, current release) | dmg | dmg | Firefox + Chrome | TCC panes, offline first open of the stapled dmg |
| Windows 11 | exe | exe | Edge or Chrome + Firefox | SmartScreen wording; voice not yet on Windows |
| Ubuntu 24.04 desktop | deb | deb | Firefox is a **Snap**: browser control is expected to fail (DESIGN §10 known gap), so test Chrome; the gap must be fixed or stated before advertising Ubuntu | App Center `.deb` install |
| Bazzite (the friend's machine) | — (optional one-liner) | one-liner | none (accepted) | portal dialog, SELinux, KDE |
| Hyprland (dev box) | — | deb or one-liner | Firefox | `graphical-session.target` risk |

## 6. Open questions (one at a time, most blocking first)

**Answered (user, 2026-10-13):**

- **Q1: `net.jarvisautomation.agent`** for the agent's app identifier: the bundle id, the launchd
  label, the portal app id and `.desktop` name, and the Windows task name. The user was fine with
  either `io.jarvisautomation.agent` or `net.` and chose to match jarvisd's `net.jarvisautomation.*`.
  Rename it before the first release. The Firefox id `browser@jarvis.agent` stays. **Done**
  (#97, `internal/agent/appid`); the native-messaging host name `io.jarvis.agent.browser`
  stays too (DESIGN §13 "The app identifier").
- **Q2: no extension detection.** The Add-ons tab's browser-extension row is just a link to the
  store for the page's browser (Firefox Add-ons, Chrome Web Store), with no installed check.
  Option (b) (content script) and (c) are dropped. "This computer's agent" still uses (a), the
  server-side address correlation, because chat's default node needs it (webapp plan §0). The
  "Your devices" list still shows each agent's extension state from its heartbeat.
- **Q3: the tab is "Add-ons"** (the user deferred to Claude). The two existing admin strings that
  use "add-on" for something else change: ConnectionsPage.tsx:337 → "External services", wizard
  `privacy.ts:46` → "Pantry packages".
- **Q4: the `.deb` opens the firewall for the LAN by default.** Only when a host firewall is active,
  only for the local subnets, never the internet, and the rule is tagged so uninstall removes it.
  The macOS/Windows installers keep the ticked checkbox. (The user: you'd have to open it by hand
  to reach the page anyway.)
- **Q5: the Linux agent is per user, set up on first launch.** The `.deb` installs nothing per
  user. It adds "Jarvis Agent" to the app menu, plus the system-wide pieces (the `.desktop` file,
  the URL handler below). The first time a person opens it, it creates their self-updating copy
  under `~/.local`, opens the first-run page to sign in or pair, and registers their own login
  autostart. Nobody gets an agent they didn't set up, and accounts created later work. A single
  machine-wide agent was considered and rejected: typing, windows, mic, island, the browser host
  and the portal grant all live in one user's session, and a global account would make
  permissions unworkable (user and Claude agree). People who share one desktop account share
  one agent; voice ID tells them apart.
- **New: "Start agent" from the web app.** Every agent installer (dmg, exe, deb, install-agent.sh)
  registers a `jarvis-agent:` URL handler: CFBundleURLTypes on macOS, HKCU\Software\Classes on
  Windows, an `x-scheme-handler/jarvis-agent` `.desktop` MimeType on Linux. The Add-ons tab shows a
  **Start** button when this computer's agent is known but offline, linking to
  `jarvis-agent://start`. The browser asks "Open Jarvis Agent?" once, and the agent starts with
  the island pill, the same as the start-only shortcut. The handler only accepts `start` and opens
  the first-run page if the agent isn't set up. It never takes parameters from the URL.
- **New: sign out of an agent.** On the agent's own first-run/settings page, and in the web app's
  "Your devices" list (owner or household admin): **Sign out** revokes that agent's node
  credentials on the server. At its next connection, the agent finds it's been signed out, stops
  its tools and shows the sign-in page, so another person can sign in on that desktop account.
  The user isn't sure many people will use it. It's cheap because unpairing already exists, so
  it's in Phase 3 as S.
- **Q6: uninstall follows each platform's standard.** The macOS app and Windows uninstaller ask
  "Also delete Jarvis's data?", default **No**. The `.deb` keeps data on remove, and `apt purge`
  deletes it.
- **Q7: the narrow exception is allowed.** The root update helper may re-render the Linux systemd
  unit only for a binary whose signature has just verified, and only when the rendered unit
  differs. The change takes effect at the next start. This overrides §8.2's earlier "no" for
  this case only. The user's reason: users must never have to edit a service file by hand.
- **Q8: Fedora, Bazzite and Arch get the one-liner** (`install-agent.sh`) for the agent, with the
  same per-user setup as the `.deb`. AppImage is not planned. The user: those users are
  comfortable with a terminal.
- **All installer questions are answered.** Next: the web-app plan's remaining questions.
- **Members can't open the admin yet.** That is known and intended: member access is the
  web-app project ([webapp/01](../webapp/01-member-web-app-plan.md)), not this plan.


1. **The agent's app identifier.** The agent uses `io.jarvis.agent` (bundle id, launchd label, the
   portal's app id and `.desktop` name in §14, Windows task `\Jarvis\JarvisAgent`). jarvisd uses
   `net.jarvisautomation.*`, chosen in §2.2 because the project controls that domain.
   **Why it blocks:** every installer artifact (2.2–2.4) and item 1.4 bake it in. After release,
   changing it resets every macOS TCC grant and GNOME/KDE portal grant. *Options:* (a)
   `net.jarvisautomation.agent` everywhere before the first release; (b) keep `io.jarvis.agent`.
   **Recommend (a)**, now, while only dev installs exist. The Firefox extension id
   `browser@jarvis.agent` is separate and stays (it is permanent once signed).
2. **Detecting "This computer" in Add-ons.** *Options:* (a) server-side address correlation
   (§3.2), no extension change; (b) the extension, which learns the server URL from the agent,
   registers a content script for **that origin only** that announces "extension present, agent
   connected" to the page. That is exact, but it adds a content script to an extension whose
   audited surface has none (DESIGN §10), and needs a store re-review; (c) `externally_connectable`
   as proposed. That isn't feasible (§3.2). **Recommend (a)**, and (b) only if (a) proves wrong
   too often.
3. **The tab's name.** **Recommend** keeping "Add-ons" (the user's choice) and changing the two
   existing admin strings that use "add-on" for other things (§3.2). The alternative, "Apps &
   devices", avoids the collision without touching copy.
4. **Firewall in GUI installers** (IQ5 decided "ask, default yes"). The macOS and Windows dialogs
   have a ticked checkbox. The `.deb` can't ask (no prompts in App Center). **Recommend:** the
   `.deb` applies the LAN-only, tagged fix by default (ufw is usually inactive on Ubuntu desktops
   anyway), and the wizard's Check step shows the result.
5. **Linux agent `.deb` layout.** (a) bootstrap: the package ships a copy that installs a user-owned,
   self-updating binary per user (§2.2); uninstall needs the agent's own Remove; (b) dpkg owns
   `/usr/bin/jarvis-agent` and updates come from an apt repository we'd host. **Recommend (a)**: it
   matches the decided self-update and the one-liner layout. (b) adds hosting and a second update
   path.
6. **Server data on uninstall.** **Recommend:** the macOS app and the Windows uninstaller ask
   "Also delete Jarvis data (N GB)?", defaulting to No (§4.4 keeps data by default). The `.deb`
   keeps it, and `apt purge` purges.
7. **Linux unit refresh after an admin-button upgrade.** §8.2 declined letting the root
   `ExecStartPre` re-render the unit. Without that, a `.deb` user needs a terminal after some
   upgrades. **Recommend:** allow it only when the rendered unit differs and the new binary is the
   one that just passed signature verification, and write it for the *next* start (no in-job
   restart).
8. **Agent on Fedora/Bazzite/Arch.** One terminal paste (`install-agent.sh`) under the decided
   one-liner rule, or an AppImage now. **Recommend** the one-liner, then revisit after the Bazzite
   sign-off.

## 7. Contradictions found between the docs and the code

1. **Members can't open the admin today.** The SPA rejects non-superusers
   (`web/admin/src/auth/AuthContext.tsx:175`), and the gateway gates every route but a bootstrap
   allow-list on a superuser (`internal/modules/admin/gateway.go`). DESIGN §13 puts "Add a computer"
   and pairing approval in the admin for every member. Resolved by the member web app (decided
   2026-10-13, §3.2). Until then, members approve in the mobile app.
2. **`externally_connectable` vs the extension's security surface.** The Add-ons idea conflicts
   with DESIGN §10 ("no `externally_connectable`", `TestExtensionSurface`), and it isn't feasible
   for LAN hosts (§3.2, Q2).
3. **Approve with or without a code.** 00 §9.1 step 3 says the wizard approves "with no code to read
   out"; DESIGN §13 uses a device code. This plan shows the code on both screens (nothing to type),
   so a pairing request from another LAN device can't be approved by mistake.
4. **The service account on macOS.** 00 §9.2 says jarvisd runs as `jarvisd` on macOS. §2.2 and §8.1
   say it runs as a LaunchDaemon *as the installing user*. §9.2's conclusion still holds: a daemon
   is outside the GUI session and can't hold TCC grants.
5. **macOS permissions list.** 00 §9.1 names Accessibility and Screen Recording only. The agent also
   needs **Microphone** (voice) and **Automation** (System Events, DESIGN §4 evidence).
6. **Naming.** The "Desktop & browser" checklist (DESIGN §13 item 5) is now the **Add-ons** tab
   (user, 2026-10-13). STATUS still says "a first-run window", where DESIGN §13 Q1 decided a
   browser page.
7. **Agent ids vs project domain**: Q1.
8. **"Add-ons" already used** in the admin for external services and Pantry commands: Q3.
