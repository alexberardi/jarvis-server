# jarvis-server

The single-binary Go server for Jarvis, a private, self-hosted voice assistant, and its Go node
agent.

> **Status: very beta.** Only pre-releases exist (`v0.1.0-rc1` … `v0.1.0-rc14`, see
> [Releases](https://github.com/alexberardi/jarvis-server/releases)). Expect rough edges and
> breaking changes between release candidates. If you run the Python stack today, keep it and keep
> a database backup until you are happy with jarvisd: the installer stops the legacy containers but
> deletes nothing, so you can go back.

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
  enter"). With `--voice`
  (Linux, macOS) it listens for "hey jarvis" and shows a small on-screen indicator, the island.
- **On a Raspberry Pi** with the ReSpeaker 2-mics HAT it runs the voice pipeline, LEDs and button.
  This is the newest part and is still experimental: there is no Pi installer, provisioning
  hotspot, self-update or Pantry/Home Assistant package support yet. **The Python
  [jarvis-node-setup](https://github.com/alexberardi/jarvis-node-setup) node remains the supported
  Pi node for now**, and it works against jarvisd.

`jarvis-agent` is not in the release archives yet: build it from source (below).

## Supported platforms (jarvisd)

| OS | CPU | Service | Notes |
|---|---|---|---|
| Linux (systemd) | amd64, arm64 | system unit, or `--user` unit | GPU engines: CUDA, Vulkan, ROCm, CPU |
| macOS | Apple silicon (arm64) only | LaunchDaemon | Metal. Intel Macs are not supported. Developer ID signed and notarized |
| Windows | amd64 | Windows service | Unsigned. Built and smoke-tested in CI; not yet rehearsed on real hardware |

## Install jarvisd

While only release candidates exist, install from the release's own URL. GitHub's
`releases/latest` skips pre-releases, so the `latest` links 404. Each published install script
installs the release it came from.

**Linux and macOS:**

```sh
curl -fsSLO https://github.com/alexberardi/jarvis-server/releases/download/v0.1.0-rc14/install.sh
less install.sh          # read it first
sh install.sh            # asks for sudo
```

Useful options (`sh install.sh --help`): `--stop-legacy` stops a legacy Python stack on the same
host (Docker containers and macOS LaunchAgents; no data is removed), `--user` (Linux: a per-user
service without root, data in `~/.jarvisd`), `--yes` (apply the firewall fix without asking),
`--uninstall [--purge]`.

**Windows** (PowerShell, as Administrator or it asks for elevation):

```powershell
irm https://github.com/alexberardi/jarvis-server/releases/download/v0.1.0-rc14/install.ps1 | iex
```

Options (`-StopLegacy`, `-Yes`, `-Uninstall`, …) need the downloaded file:
`powershell -ExecutionPolicy Bypass -File install.ps1 -StopLegacy`.

The installer verifies the archive against `SHA256SUMS` (and its minisign signature when `minisign`
is installed), places the binary, registers the service with `jarvisd service install`, runs
`jarvisd doctor`, offers to open the host firewall for private LAN subnets, and prints the setup
link. Data lives in `/var/lib/jarvisd` (Linux system service), `~/.jarvisd` (Linux `--user`, and the
service account's home on macOS) or `%ProgramData%\jarvisd` (Windows).

Day to day:

```sh
jarvisd service status            # also: sudo jarvisd service start|stop|restart
jarvisd doctor                    # can nodes and phones reach it? (sudo jarvisd doctor --fix)
sudo jarvisd upgrade --version v0.1.0-rcN   # signed upgrade with a DB snapshot and rollback
jarvisd --help
```

Upgrading to another RC can also be done from the admin UI, or by re-running that RC's install
script.

## First run

Open the setup link the installer printed (`sudo jarvisd setup-link` prints it again; it looks like
`http://<server>:7710/setup#token=…`). The wizard checks that nodes can reach the server, creates the
first account, picks hardware and a model, and asks the privacy questions; nothing large is
downloaded until you choose a model there. After setup the admin UI stays on port 7710.

Point nodes and the mobile app at `http://<server>:7700` (the config service); jarvisd also
advertises itself over mDNS (`_jarvis-config._tcp`).

## Migrating from the Python stack

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

## Install the agent (from source)

Needs Go 1.27 (see `go.mod`) and a clone of this repository.

**Desktop (Linux, macOS):** `scripts/agent-dev-run.sh` fetches the native voice libraries, builds,
enrolls and runs the agent. On macOS it builds a signed `~/Applications/Jarvis Agent.app` so
permission grants (Accessibility, Microphone, Screen Recording) stick.

```sh
scripts/agent-dev-run.sh --server http://<server>:7700 --email you@example.com   # first time; prompts for the password
scripts/agent-dev-run.sh -- --voice           # later runs, with "hey jarvis" and the island
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
macOS and Windows (on GNOME/KDE, click into the app first). `jarvis-agent status` says what works; `run --confirm-dictation submit` makes Jarvis ask
"Send … to Claude?" before anything is sent.

Or by hand:

```sh
scripts/fetch-sherpa-libs.sh                  # once; embeds onnxruntime for the wake word
CGO_ENABLED=0 go build -o jarvis-agent ./cmd/jarvis-agent
./jarvis-agent enroll --server http://<server>:7700 --email you@example.com
./jarvis-agent run --voice                    # --no-overlay hides the island; --no-browser
./jarvis-agent status                         # what this host can do right now
```

**Raspberry Pi (experimental):** 64-bit Raspberry Pi OS (arm64) only. Cross-compile on another
machine with `scripts/fetch-sherpa-libs.sh linux-arm64` and
`GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o jarvis-agent ./cmd/jarvis-agent`, copy the binary
over, enroll it as above (`--room` names the room), stop the Python node so the microphone is free
(`sudo systemctl stop jarvis-node`), and run
`./jarvis-agent run --voice --voice-input dsnoopmic --voice-output output`. The ALSA devices
`dsnoopmic`/`output` come from the Python node's installer. The HAT's LEDs and button are used when
it is detected (`--hat auto|on|off`; holding the button 3 s powers the Pi off unless
`--button-poweroff=false`). There is no service unit for it yet. See
[docs/agent/go-pi-node.md](docs/agent/go-pi-node.md).

Some flags are not in `jarvis-agent --help` yet (`--no-overlay`, `--hat`, `--led-brightness`,
`--button-poweroff`, `--follow-up`, `--follow-up-silence`, `--wake-debounce`,
`--listen-onset-wait`); `jarvis-agent run -h` lists them all.

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
