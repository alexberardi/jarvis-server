# jarvis-server

Jarvis Automation's server: one Go executable, `jarvisd`, that runs Jarvis, a private,
self-hosted voice assistant, plus its Go node agent, `jarvis-agent`.

> **Status: pre-release.** Only release candidates exist so far (see
> [Releases](https://github.com/alexberardi/jarvis-server/releases)); there is no `v0.1.0` yet.
> Expect rough edges and breaking changes between release candidates. jarvisd is the way to install
> Jarvis Automation. The old Docker/microservice stack is retired for new installs (see
> [Legacy](#legacy-docker--microservices)). If you run it today, keep a database backup until you
> are happy with jarvisd: the installer stops the legacy containers but deletes nothing, so you can
> go back.

Questions, bug reports and quick help: the project [Discord](https://discord.gg/9SuQFVexxJ), or a
[GitHub issue](https://github.com/alexberardi/jarvis-server/issues).

## What is in this repository

**`jarvisd`** replaces the Python microservices with one executable:

- **Services it replaces:** config, auth, logs, notifications, command-center, LLM proxy, STT, TTS,
  recipes and OCR, plus the admin web UI (embedded). settings-server and mcp are dropped.
- **Infrastructure, all embedded:** SQLite instead of Postgres, a durable job queue instead of
  Redis, a filesystem blob store instead of MinIO, and an MQTT broker instead of Mosquitto. No
  Docker.
- **Inference:** text-to-speech (Kokoro) and speaker recognition run in-process. The LLM
  (`llama-server`) and STT (`whisper-server`) engines and the model weights are downloaded when you
  pick them in the admin UI, not before.
- It listens on the legacy ports (7700 config, 7701 auth, 7703 command-center, 7710 admin, …), so
  existing nodes and the mobile app talk to it unchanged.

**`jarvis-agent`** is the Go node: it enrolls with jarvisd like a Pi node and connects over MQTT.

- **On a desktop** (Linux, macOS; Windows untested on real hardware) it offers OS control as tools:
  open links and apps, volume, window list/focus/minimise/close, the user's own browser through the
  Jarvis Browser Control extension, reading the screen (Hyprland/sway and macOS), and dictation
  ("tell claude to run the tests", "fill in username abcde on browser", "click OK", "press
  enter"). On Linux and macOS it also listens for "Hey Jarvis" and shows a small on-screen
  indicator, the island. Its status icon (menu bar, tray) turns listening on and off and quits it.
- **On a Raspberry Pi** with the ReSpeaker 2-mics HAT it runs the voice pipeline, LEDs and button.
  This is the newest part and is a **preview**: `install-pi.sh` installs it (below), but it is not
  yet at parity with the Python node (provisioning hotspot and Pantry/Home Assistant packages,
  among others). **The Python [jarvis-node-setup](https://github.com/alexberardi/jarvis-node-setup)
  node remains the supported Pi node for now**, and it works against jarvisd.

Both ship with each release: as installers for macOS, Windows and Debian/Ubuntu, as install scripts
and as archives. Building them from source is described further down.

## Supported platforms (jarvisd)

| OS | CPU | Service | Notes |
|---|---|---|---|
| Linux (systemd) | amd64, arm64 | system unit, or `--user` unit | GPU engines: CUDA, Vulkan, ROCm, CPU |
| macOS | Apple silicon (arm64) only | LaunchDaemon | Metal. Intel Macs are not supported. Developer ID signed and notarized |
| Windows | amd64 | Windows service | Unsigned. Built and smoke-tested in CI; not yet rehearsed on real hardware |

## Install

Download from the [Releases](https://github.com/alexberardi/jarvis-server/releases) page: take
the newest release (at the top). Each release has these files, with `<v>` its version:

| | Server (jarvisd) | Desktop agent |
|---|---|---|
| macOS (Apple silicon) | `Jarvis-Automation-Server-<v>.dmg` | `Jarvis-Automation-Agent-<v>.dmg` |
| Windows | `Jarvis-Automation-Server-Setup-<v>.exe` | `Jarvis-Automation-Agent-Setup-<v>.exe` |
| Debian, Ubuntu | `jarvis-automation-server_<v>_<arch>.deb` | `jarvis-automation-agent_<v>_<arch>.deb` |
| From a terminal | `install.sh` (Linux, macOS), `install.ps1` (Windows) | `install-agent.sh` (any Linux) |
| Raspberry Pi (preview) | | `install-pi.sh` |

`<arch>` is `amd64` or `arm64`. `SHA256SUMS` and its minisign signature `SHA256SUMS.minisig`
cover every file. The install scripts check the archive they download against them.

While only pre-releases exist, use the release's own URL. GitHub's `releases/latest` skips
pre-releases, so `releases/latest/download/...` links give a 404. The commands below use
`v0.1.0-rc18` as the example tag: replace it with the newest tag. Each install script installs
the release it came from.

### The server

Install jarvisd on one computer that stays on: it is the server for your whole household.

**macOS:** open `Jarvis-Automation-Server-<v>.dmg` and drag **Jarvis Automation Server** to
Applications, then open it. It asks whether devices on your network may reach it (ticked), asks
for your password, installs the background service and opens the setup page in your browser.
Opening the app again later opens the setup page or the admin. To uninstall, hold Option while
opening it and choose **Uninstall…** (it keeps your data unless you say otherwise).

**Windows:** download `Jarvis-Automation-Server-Setup-<v>.exe` and open it. Windows may say
*Windows protected your PC*: Jarvis Automation is safe and open source, and Windows shows this for
any installer that isn't signed with a paid code-signing certificate, which we don't buy. Click
**More info**, then **Run anyway** (to check the file first, compare its SHA-256 with
`SHA256SUMS`). Setup asks for administrator rights, installs the service, opens the firewall for
your local network (a ticked box), and opens the setup page in your browser. Uninstalling it
(Settings → Apps) asks whether to delete the data too; the default keeps it in
`%ProgramData%\jarvisd`.

**Debian and Ubuntu:** download `jarvis-automation-server_<v>_<arch>.deb` and open it: App Center
or Software installs it after your password, and the setup page opens in your browser (a computer
without a screen prints the link and a QR code; `sudo apt install ./jarvis-automation-server_….deb`
works too). It runs the same `jarvisd service install` and LAN-only firewall fix as the script.
Removing the package stops the service and keeps the data in `/var/lib/jarvisd`;
`sudo apt purge jarvis-automation-server` deletes it.

**From a terminal, Linux and macOS:**

```sh
curl -fsSLO https://github.com/alexberardi/jarvis-server/releases/download/v0.1.0-rc18/install.sh
less install.sh          # read it first
sh install.sh            # asks for sudo
```

Options (`sh install.sh --help`):

- `--user` (Linux): a per-user service without root, data in `~/.jarvisd`.
- `--stop-legacy`: stops a legacy Docker stack on the same host (its containers and macOS
  LaunchAgents; no data is removed).
- `--uninstall`: removes the service and the binary and keeps the data; add `--purge` to delete
  the data too.
- `--yes` applies the firewall fix without asking; `--version <tag>` installs another release.

**From a terminal, Windows** (PowerShell, as Administrator or it asks for elevation):

```powershell
irm https://github.com/alexberardi/jarvis-server/releases/download/v0.1.0-rc18/install.ps1 | iex
```

Options (`-StopLegacy`, `-Yes`, `-Uninstall`, `-Purge`, `-Version`) need the downloaded file:
`powershell -ExecutionPolicy Bypass -File install.ps1 -StopLegacy`.

The scripts verify the archive against `SHA256SUMS` (and its minisign signature when `minisign`
is installed), place the binary, register the service with `jarvisd service install`, run
`jarvisd doctor`, offer to open the host firewall for private LAN subnets, and print the setup
link. Data lives in `/var/lib/jarvisd` (Linux system service), `~/.jarvisd` (Linux `--user`, and
the service account's home on macOS) or `%ProgramData%\jarvisd` (Windows).

### The desktop agent

Install the agent on each computer you want Jarvis to control or listen on, after the server. It
is per user: each person who wants it installs their own, without administrator rights. Its
first-run page pairs it with your server and sets up browser control (and, on a Mac, the
permissions it needs).

- **macOS:** open `Jarvis-Automation-Agent-<v>.dmg`, drag **Jarvis Automation Agent** to
  Applications and open it.
- **Windows:** open `Jarvis-Automation-Agent-Setup-<v>.exe` (the same *Windows protected your PC*
  note as above applies). It installs for you only, starts when you sign in, and opens its setup
  page the first time.
- **Debian and Ubuntu:** `jarvis-automation-agent_<v>_<arch>.deb` adds "Jarvis Automation Agent"
  to the app menu: each person who opens it gets their own self-updating copy and the setup page.
  Removing the package leaves those copies; each person removes theirs from its setup page
  ("Remove Jarvis Automation Agent") or with `~/.local/lib/jarvis-agent/jarvis-agent uninstall --purge`.
- **Any other Linux** (Fedora, Bazzite, Arch, …), as yourself, not with sudo:

  ```sh
  curl -fsSLO https://github.com/alexberardi/jarvis-server/releases/download/v0.1.0-rc18/install-agent.sh
  sh install-agent.sh      # --no-start, --version <tag>, --uninstall [--purge]
  ```

The agent updates itself to follow its server's version.

### Raspberry Pi node (preview)

`install-pi.sh` installs the Go node on a Raspberry Pi with 64-bit Raspberry Pi OS (arm64) and the
ReSpeaker 2-mics HAT v2.0, next to or instead of the Python node. It does the system setup the
Python node's installer does (HAT overlay, ALSA, PulseAudio, networking) and installs
`jarvis-agent.service`, **disabled**: only one node may own the HAT.

```sh
curl -fsSLO https://github.com/alexberardi/jarvis-server/releases/download/v0.1.0-rc18/install-pi.sh
sudo sh install-pi.sh                 # --user NAME (default pi), --version <tag>
/opt/jarvis-agent/jarvis-agent enroll --server <server> --room kitchen   # as the node's user; approve the code in the app or admin
sudo sh install-pi.sh --activate      # make the Go node the one that runs (--deactivate goes back)
```

Reboot when the script says so (HAT or audio changes). This is a preview: the Python
[jarvis-node-setup](https://github.com/alexberardi/jarvis-node-setup) node is still the supported
Pi node. See [docs/agent/go-pi-node.md](docs/agent/go-pi-node.md).

## First run

The installers open the setup wizard, `http://<server>:7710/setup`, in your browser. If yours
didn't, `sudo jarvisd setup-link` prints the link again (it looks like
`http://<server>:7710/setup#token=…`). The wizard checks that nodes can reach the server, creates the
first account, picks hardware and a model, and asks the privacy questions; nothing large is
downloaded until you choose a model there. After setup the admin UI stays on port 7710.

Point nodes and the mobile app at `http://<server>:7700` (the config service); jarvisd also
advertises itself over mDNS (`_jarvis-config._tcp`).

## Day to day and upgrades

```sh
jarvisd service status            # also: sudo jarvisd service start|stop|restart
jarvisd doctor                    # can nodes and phones reach it? (sudo jarvisd doctor --fix)
sudo jarvisd upgrade --version v0.1.0-rc18   # signed upgrade with a DB snapshot and rollback
jarvisd --help
```

`jarvisd doctor` is the first thing to run when a node or phone can't reach the server.

To upgrade, use the admin UI (it offers new releases and updates jarvisd in place), run
`sudo jarvisd upgrade --version <tag>`, or install the newer release's installer or script over
the old one. `jarvisd upgrade` checks the release's signature and snapshots the database first;
`sudo jarvisd upgrade --rollback` goes back. Desktop agents follow their server's version by
themselves.

## Legacy (Docker / microservices)

The original Jarvis was a set of Python microservices run with Docker Compose (Postgres, Redis,
MinIO, Mosquitto, …). **It is retired for new installs as of `v0.1.0-rc18`:** it no longer installs
out of the box (MinIO is gone), and its documentation is frozen as it was. Install jarvisd instead.

If you already run the legacy stack, move to jarvisd: install it on the same host with
`--stop-legacy` (or on another host), then import your data before the setup wizard.

`jarvisd import-legacy` copies a legacy install's users (with their passwords), households, nodes
(with their keys, so Pi nodes reconnect without re-provisioning), rooms, devices, settings,
memories, routines, contacts and inbox, keeping ids. It reads the legacy Postgres read-only, runs
once on a fresh jarvisd **before** the setup wizard, and needs the jarvisd service stopped. It is a
dry run unless you pass `--apply`:

```sh
sudo jarvisd service stop
jarvisd import-legacy --home <jarvisd data dir> --compose ~/.jarvis/compose            # dry run: read the report
jarvisd import-legacy --home <jarvisd data dir> --compose ~/.jarvis/compose --apply
sudo jarvisd service start
```

The legacy Postgres must be running for the import, and the service account must be able to read the
compose `.env`. Read [docs/install/legacy-import.md](docs/install/legacy-import.md) (what is
carried over, which node versions recover, refusals) and the worked sequence in
[docs/install/cutover-runbook.md](docs/install/cutover-runbook.md) §4.3a before you run it. Recipes
come over separately with `jarvisd import-recipes`.

## Running the agent from source

Needs Go 1.27 (see `go.mod`) and a clone of this repository. For development; everyone else
installs the agent from a release (above).

**Desktop (Linux, macOS):** `scripts/agent-dev-run.sh` fetches the native voice libraries, builds,
enrolls and runs the agent. On macOS it builds a signed `~/Applications/Jarvis Agent.app` so
permission grants (Accessibility, Microphone, Screen Recording) stick.

```sh
scripts/agent-dev-run.sh --server http://<server>:7700 --email you@example.com   # first time; prompts for the password
scripts/agent-dev-run.sh                      # later runs ("Hey Jarvis", the island, the status icon)
scripts/agent-dev-run.sh --install            # start at login instead (systemd user unit / LaunchAgent)
```

Enrolling asks whether to set up browser control (default yes); `jarvis-agent browser install` does
it later. Firefox gets a Mozilla-signed extension; Chrome, Edge, Brave and Arc are also supported
(see [docs/agent/DESIGN.md](docs/agent/DESIGN.md) §10).

Dictation types into the window in front or into an app you name, and presses Enter, Escape, Tab
or the arrows to answer dialogs ([docs/agent/DESIGN.md](docs/agent/DESIGN.md) §14). On Hyprland,
sway and other wlroots desktops it needs nothing extra (the agent speaks the Wayland
virtual-keyboard protocol itself), and on X11 neither (XTEST and EWMH are built in, so window
control needs no `wmctrl`/`xdotool` either). On GNOME and KDE (Wayland) it types through the
desktop portal once you allow it: run `jarvis-agent dictation allow` and accept the dialog (asked
once). macOS needs the Accessibility grant. Typing into a *named* app works on Hyprland, sway, X11,
macOS and Windows (on GNOME/KDE, click into the app first). `jarvis-agent status` says what works. By default Jarvis asks
"Send … to Claude?" before anything is sent (Enter, a click, a submitting fill); `run --confirm-dictation off`
turns that off, `all` asks before every keystroke too. Typing presses keys, as you would: in a terminal app
with key bindings (Neovim/Vim in normal mode, `less`, `htop`) they run as commands, so switch it to
insert mode first (DESIGN §14, "Safety summary").

Or by hand:

```sh
scripts/fetch-sherpa-libs.sh                  # once; embeds onnxruntime for the wake word
CGO_ENABLED=0 go build -o jarvis-agent ./cmd/jarvis-agent
./jarvis-agent enroll --server <server> --email you@example.com   # the host, or any URL of it (the web app's too)
./jarvis-agent run                           # --no-overlay hides the island, --no-tray the icon; --no-browser
./jarvis-agent stop                          # quit until the next login (the icon's menu: Quit)
./jarvis-agent status                         # what this host can do right now
```

**Raspberry Pi, by hand:** 64-bit Raspberry Pi OS (arm64) only. Cross-compile on another
machine with `scripts/fetch-sherpa-libs.sh linux-arm64` and
`GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o jarvis-agent ./cmd/jarvis-agent`, copy the binary
over, enroll it as above (`--room` names the room), stop the Python node so the microphone is free
(`sudo systemctl stop jarvis-node`), and run
`./jarvis-agent run --voice --voice-input dsnoopmic --voice-output output`. The ALSA devices
`dsnoopmic`/`output` come from the Python node's installer. The HAT's LEDs and button are used when
it is detected (`--hat auto|on|off`; holding the button 3 s powers the Pi off unless
`--button-poweroff=false`). `install-pi.sh` (above) does the system setup for you and installs a
service unit. See
[docs/agent/go-pi-node.md](docs/agent/go-pi-node.md).

Some flags are not in `jarvis-agent --help` yet (`--no-overlay`, `--hat`, `--led-brightness`,
`--button-poweroff`, `--follow-up`, `--follow-up-silence`, `--wake-debounce`,
`--listen-onset-wait`, `--listen-preroll`); `jarvis-agent run -h` lists them all.

## Documentation

| Doc | What |
|---|---|
| [docs/STATUS.md](docs/STATUS.md) | Where the project is: progress, decision log, session log |
| [docs/PLAN.md](docs/PLAN.md) | The original architecture and migration plan |
| [docs/install/00-installers.md](docs/install/00-installers.md) | Installers, service registration, upgrades and rollback, by OS |
| [docs/install/legacy-import.md](docs/install/legacy-import.md) | `jarvisd import-legacy` |
| [docs/install/cutover-runbook.md](docs/install/cutover-runbook.md) | A worked Python → jarvisd cutover on a real install |
| [docs/agent/DESIGN.md](docs/agent/DESIGN.md) | jarvis-agent: tools, enrollment, browser control, the island, read the screen |
| [docs/agent/go-pi-node.md](docs/agent/go-pi-node.md) | The Go voice/Pi node: protocol, measurements, phased plan |
| [docs/cc/README.md](docs/cc/README.md) | command-center specs (voice pipeline, tool loop, memory, smart home, …) |
| [docs/llm/00-overview.md](docs/llm/00-overview.md) | LLM proxy: engines, API, queue, models |
| [docs/admin/00-inventory.md](docs/admin/00-inventory.md) | The admin UI and first-run wizard |
| [docs/recipes/00-inventory.md](docs/recipes/00-inventory.md) | The recipes module |
| [docs/schema/](docs/schema/) | Per-module database notes and legacy import mappings |
| [docs/contract/README.md](docs/contract/README.md) | The wire-contract test suite |

Most of `docs/` is design and planning material written while porting from the Python services; it
records decisions and legacy behaviour as well as how jarvisd works. STATUS.md is the most current.

## Building jarvisd

```sh
scripts/fetch-sherpa-libs.sh                      # once
make jarvisd                                      # with the admin UI (needs Node); or:
CGO_ENABLED=0 go build -o jarvisd ./cmd/jarvisd   # without it (a placeholder page)
```

Builds never use cgo. Tests: `CGO_ENABLED=1 go test -race ./...` (the race detector needs cgo).

### Demo data for a dev server

`scripts/seed-dev.py` (Python 3, standard library only) fills a fresh jarvisd with pseudonymous
demo data for screenshots and dev work: three logins, a named household, two nodes that really
authenticate, rooms, devices, routines, a phonebook, memories, recent voice history and signals.
It finishes first-run setup itself with the setup token from `<home>/setup-token`, and reruns are
idempotent. On a Linux system install it runs as the service account:

```sh
sudo -u jarvisd python3 scripts/seed-dev.py --home /var/lib/jarvisd    # --profile minimal | --reset
```

See the script's header for prerequisites and options, and
[docs/dev/demo-reset.md](docs/dev/demo-reset.md) for the whole reset: clearing the database, seeding,
and reconnecting the desktop agent and nodes.

## License

AGPL-3.0. See [LICENSE](LICENSE).
