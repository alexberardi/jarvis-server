# jarvis-agent: the desktop node (POC)

Status: proof of concept, 2026-10-09. Not for the next release. **Moved into jarvis-server on
2026-10-10** (from `jarvis-host-agent`, PR #1 there). Paths in this doc are relative to the old
repo: `internal/X` is now `internal/agent/X`, `deploy/` is `internal/agent/deploy/`, and
`scripts/dev-run.sh` is `scripts/agent-dev-run.sh`. The binary is still `cmd/jarvis-agent`.

## 1. What it is

A single Go binary, `jarvis-agent`, that **is a Jarvis node**, not a sidecar HTTP service a Pi
calls. It enrolls with jarvisd, holds a `node_id`/`node_key`, connects to jarvisd's MQTT,
appears in the household's node list, and offers the host-agent capabilities as client tools:

| Tool | Capability | Linux | macOS | Windows |
|---|---|---|---|---|
| `desktop_open` (link or app name) | `open` | xdg-open; apps: gio launch / gtk-launch | open(1) | ShellExecuteW |
| `desktop_list_apps` | `open` | XDG .desktop entries | .app bundles | Start Menu .lnk |
| `desktop_get_volume` | `volume.get` | pactl / wpctl / amixer | osascript | WASAPI (COM) |
| `desktop_set_volume` | `volume.set` | same | same | same |
| `desktop_list_windows` | `windows.list` | wmctrl (X11 only) | System Events (AX grant) | EnumWindows |
| `desktop_minimize_window` | `windows.minimize` | xdotool | AXMinimized | ShowWindow |
| `desktop_focus_window` | `windows.focus` | wmctrl -a | AXRaise | SetForegroundWindow |
| `desktop_close_window` | `windows.close` | wmctrl -c | AXCloseButton | WM_CLOSE |

Function names can't contain dots, so tool names don't spell the capability names; each maps
to exactly one. A tool is offered only while its capability is available, computed per
`report_tools`.

## 2. Decisions (agreed with the user)

- **Q1: the binary is a node.** Go owns node identity, the key and MQTT.
- **Pantry packages run in a Python sidecar** the agent manages: uv-managed standalone Python
  and a venv in the agent's data dir, started once and kept warm, JSON messages over
  stdin/stdout. Packages get a manifest tag: node-only, node-and-desktop, desktop-only.
- **A Go Pi node may follow.** The core (`internal/node`: enroll, creds, discovery, MQTT,
  heartbeat, dispatch) is desktop-agnostic so a Pi node can reuse it; no further abstraction.
- **The Python host-agent invariants carry over** (see CLAUDE.md): user-session supervisors only,
  open is fire-and-forget, close is graceful, ids are opaque, volume clamps, capabilities are
  computed per request, and capability names are a closed set.
- **The Python code is removed on this branch, not moved to `legacy/`.** It was wired into
  nothing; it stays in `main`'s history. The three supervisor templates were kept and
  converted (`deploy/`), and their guard tests ported to Go.

## 3. Changes from the Python agent (deliberate)

- **`open` has a policy: links plus "anything in your apps folder".** Links: http, https and
  mailto. Apps: whatever the platform's app locations hold (macOS `/Applications/**/*.app`,
  `/System/Applications/**/*.app`, `~/Applications/**/*.app`; Linux XDG `applications/**/*.desktop`
  in `$XDG_DATA_HOME` then `$XDG_DATA_DIRS`, honouring NoDisplay/Hidden and XDG id precedence, so
  a user's Hidden override hides a system entry; Windows per-user then all-users Start Menu
  `**/*.lnk`), plus `apps.json` allow globs, minus deny globs and `*uninstall*`. The model
  names an app; resolution is case-insensitive in tiers (exact name or desktop id, then prefix,
  then all words present, then an edit distance of 1, or 2 for longer names). The first tier with
  hits decides, and several hits come back as `ambiguous` with candidates, never a guess. The
  launched path is always the catalogue's. A path, a custom URI scheme (`steam://`) or a
  leading `-` is refused. `--allow-open-any` restores "anything the OS can open". The
  catalogue is rescanned per call (about 60 to 100 entries scan in milliseconds), so installs
  and `apps.json` edits apply without a restart.
- **The macOS AX probe tells the truth.** It lists the frontmost app's windows. "count
  processes" succeeds without assistive access and made the Python agent advertise window
  control that then failed. Verified on the MBP (macOS 26.1): absent over SSH *and* from a GUI
  LaunchAgent until granted.
- **macOS window ids are `pid/index` and validated before reaching AppleScript.** The Python
  agent spliced ids into AppleScript verbatim (an injection path), and used `id of w`, which
  System Events windows don't reliably have.
- **macOS close clicks the AXCloseButton**, not `button 1` (which isn't always close).
- **Linux:** wpctl is supported (PipeWire without pactl). Minimise is advertised only with
  xdotool. Wayland never qualifies for window control, even with XWayland's `DISPLAY`.
- **Windows:** no pywin32/pycaw. Pure syscalls (`CGO_ENABLED=0`). Without an interactive
  desktop (Session 0), `open` and window control are absent, not broken.
- **Close on Windows reports a refused SetForegroundWindow** instead of claiming success.

## 4. How a tool call flows (verified against jarvisd)

```
mobile app ── POST /api/v0/mobile/chat {node_id: <desktop node>} ──▶ jarvisd CC
CC ── report_tools on jarvis/nodes/{id}/commands ──▶ agent
agent ── POST /api/v0/mobile/node-tool-reports/{rid} {client_tools, available_commands} ──▶ CC
CC ⇄ LLM (tools = the agent's tools); the model calls desktop_set_volume
CC ── [{"command":"tool_call","details":{command_name, arguments, tool_call_id,
        reply_request_id, request_id, user_id, voice_command}}] ──▶ agent (MQTT, QoS 1)
agent ── POST /api/v0/commands/{request_id}/verify ──▶ CC   (single use; dropped if invalid)
agent runs the tool (host.Host gates on capabilities, clamps, calls the backend)
agent ── POST /api/v0/device-control-results/{reply_request_id} {"output": {...}} ──▶ CC
CC feeds the output back to the LLM and streams the answer to the app
```

- MQTT: client id `jarvis-node-{id}` (the broker requires exactly that), username `node_id`,
  password `node_key` (from `GET /node/mqtt-credentials`), clean session false, subscribed to
  `jarvis/nodes/{id}/commands`. Broker URL from discovery.
- Discovery: `GET {config}/services?style=remote&remote_host={host}` gives LAN URLs with
  jarvisd's `localhost` rows rewritten to the host the agent reached config on.
- Heartbeat: `POST /admin/nodes/heartbeat` every 60 s with
  `version_info.install_mode = "desktop-agent"` (how the app can tell it from a Pi) and
  `needs_k2: false`.
- Other verbs (`tts`, `action`, `routine`, ...) are logged and ignored, so CC times out as for
  an offline node. (Open question 1.)
- Outputs follow the Pi convention: `success`, and on failure `error` plus an `error_code`
  (`not_supported`, `window_not_found`, `invalid_argument`, `failed`).

### Evidence (2026-10-09)

Against a throwaway jarvisd (this repo's server built from source, scratch home, loopback-only
shifted ports, fake OpenAI-compatible LLM that scripts tool calls), with jarvisd's real
provisioning, registration, broker, report_tools and mobile chat:

- `enroll --email` (login, then household pick, mint token, register): node registered, creds
  0600; the node listed `online: true, install_mode: desktop-agent`.
- `GET /mobile/nodes/{id}/tools`: the 3 Wayland tools plus the sidecar's `shout`.
- Mobile chat on this box (Linux, Hyprland): `desktop_set_volume 150` → pactl 100%, trace
  shows a 6 ms node hop. Then set back to 30, `shout` through the Python sidecar, `file://`
  refused by policy, `desktop_close_window` → `not_supported`, and an https link opened in the
  browser.
- macOS (MBP, macOS 26.1, arm64) through an SSH reverse tunnel to the same throwaway jarvisd:
  enrolled as a second node; get_volume 50 → set 37 → set 50 over mobile chat; window tools
  absent (no AX grant) and `desktop_list_windows` → `not_supported`. The contract suite passes
  natively on darwin, and the native volume round-trip works. `open` was accepted (Firefox is
  the handler).
- macOS TCC after the on-screen prompts: Automation (System Events) is **allowed** for the
  binary's path (`kTCCServiceAppleEvents`, client `…/jarvis-agent-poc/jarvis-agent`, value 2).
  Accessibility has a row for the same path, but it is **off** (value 0), so window control is
  still correctly absent from both SSH and a GUI LaunchAgent. Approving the prompt only adds the
  binary to the list; the user still has to turn it on in System Settings. Grants key on the
  path plus the ad-hoc code signature, so a rebuilt binary needs granting again.
- **Follow-up round (apps, bundle, script), 2026-10-09:**
  - Linux, real catalogue (62 apps): `jarvis-agent apps` resolved steam, writer → LibreOffice
    Writer, witcher → The Witcher 3, "the steam app" → Steam, and "red dead"; it reported
    "google" as ambiguous among 4 and found nothing for photoshop. `desktop_open "image viewer"`
    launched imv via `gio launch`.
  - `scripts/dev-run.sh` on Linux against the throwaway jarvisd: built, enrolled once (second
    run "Already enrolled"), connected, and Ctrl-C (SIGINT) exited 0. `--install` brought up the
    systemd user unit (connected, with a Wayland session env), and `--uninstall` removed it.
  - macOS (MBP, over SSH): the script built and ad-hoc signed `~/Applications/Jarvis Agent.app`
    (codesign: Identifier=io.jarvis.agent), enrolled through the tunnel, and ran under launchd.
    The AX prompt **made macOS add an `io.jarvis.agent` row** to Accessibility (off until the
    user enables it). Over mobile chat, `desktop_list_apps calc` → Calculator,
    `desktop_open calculator` launched Calculator, and a `.app` path was refused. Ctrl-C
    unloaded the launchd job, and `--install`/`--uninstall` worked.
  - macOS window control on real hardware: the user's earlier toggle for the bare binary did
    land in TCC (allowed, even though the UI never listed it). Run as that binary under launchd,
    `desktop_list_windows` returned the 11 real windows (`pid/index` ids, titles, apps), and
    `desktop_focus_window` brought Firefox to the front.
  - Developer ID signing **fails over SSH** (`errSecInternalComponent`: the login keychain isn't
    available to the SSH session). Signing must run in the user's local Terminal, so the SSH
    tests used ad-hoc.
- Not exercised on real hardware: any Windows path (CI's windows runner only); X11 window
  control (no X11 box); macOS window control (awaiting the on-screen grant).

## 5. Enrollment UX

Implemented, both ending at `POST /nodes/register`:

1. **Sign in:** `jarvis-agent enroll --server URL --email you@x` prompts for the password
   (no echo; `--password-stdin` for scripts). The CLI logs in, lists households (picks the
   only one, or asks for `--household`), mints a token with the JWT, and registers. The JWT
   stays in memory.
2. **Paste a token:** `--token prov_… --node-id …`, for a token minted elsewhere (admin key,
   app).

The token's `node_command_center_url` is preferred; on a connection failure (nothing
redeemed), the CLI falls back to the discovered CC URL. The node gets `name` = hostname and
`room` = `desktop` unless overridden.

Gaps: the login leaves a refresh token on the server (no logout call yet); there is no
device-code flow; the creds file on Windows relies on the profile ACL.

## 6. Sidecar protocol draft

Spike (built, tested in CI): `internal/sidecar` embeds `jarvis_sidecar.py`, starts
`python -u jarvis_sidecar.py <commands-dir>`, and speaks JSON lines:

```
→ {"id": 1, "method": "describe"}
← {"id": 1, "result": {"tools": [{"name", "client_tool", "command"}]}}   # SDK-rendered
→ {"id": 2, "method": "call", "params": {"command_name", "arguments",
                                        "request": {"tool_call_id", "user_id", "voice_command"}}}
← {"id": 2, "result": {"output": {"success": true, ...}}}
← {"id": N, "error": "..."}                                             # protocol failures only
```

stdout is protocol only (`print()` inside commands goes to stderr, which Go logs). The child
never sees the node key. Requests are serialised. `jarvis-agent run --sidecar-python PY
--sidecar-commands DIR` merges the sidecar's tools after the built-ins.

Draft for the real thing:

- **Lifecycle:** uv-managed interpreter + venv under the agent's data dir; one child, started
  at boot, restarted with backoff on exit; `{"method":"health"}` ping.
- **Concurrency:** request ids already allow multiplexing; the child would run calls on a
  thread pool and answer out of order. Add `{"method":"cancel","id":N}` tied to the Go context.
- **Host services the child needs from Go** (reverse requests, child → Go): secrets
  (`get_secret`), storage, inbox, `chat_text` (LLM via CC with the node key held by Go). Same
  framing with a `"from":"child"` flag.
- **Install/update:** Go downloads a package from Pantry, verifies it, `uv pip install`s into the
  venv, then `{"method":"reload"}`.

## 7. macOS TCC and the sidecar child

TCC attributes a child process to its *responsible process*. A Python child spawned by a
LaunchAgent-run `jarvis-agent` is attributed to `jarvis-agent`, so the child inherits the
agent's Accessibility and Automation grants. Good: one prompt and no grant to a Homebrew Python
that a patch upgrade invalidates. Bad: **every desktop package gets the agent's
Accessibility**. Grants are also per code signature: an ad-hoc signed Go build loses them on
every rebuild.

## 8. Package tags

A manifest field, e.g. `targets: ["node"] | ["node", "desktop"] | ["desktop"]`, default
`["node"]` so existing packages never land on a desktop by accident. Pantry filters installs
by the node's kind; the desktop agent refuses to load a package without `desktop`. Desktop
packages may assume the sidecar but not audio in/out.

## 9. Open questions, by priority, with recommendations

1. **Node kind in jarvisd.** jarvisd treats this as a Pi: it would send `tts`/`action`/`routine`
   (each times out), the app offers Pi settings, and voice on a Pi can't reach desktop tools.
   *Recommend:* have the heartbeat declare `node_kind: "desktop"` and the verbs it speaks;
   jarvisd skips unsupported verbs. Later, let CC offer a household's desktop tools in a Pi's
   turn with node-qualified dispatch ("turn down my computer").
2. **Open policy.** Done as wildcards ("anything in your apps folder") + `apps.json`. Left
   open: whether `desktop_open` on an app the user has never launched should need a
   confirmation, and whether CC should get the app list up front (it's in `desktop_list_apps`
   now, costing a round trip). *Recommend:* no confirmation for v1; revisit if mishearings
   launch the wrong thing.
3. **macOS signing.** The dev bundle is signed by `dev-run.sh` with the keychain's Developer ID
   (designated requirement = team + `io.jarvis.agent`), so grants survive rebuilds. It is not
   notarized and has no hardened runtime. Hardened runtime would need the
   `com.apple.security.automation.apple-events` entitlement, plus a check that purego's
   dlopen of system frameworks passes library validation. *Recommend:* notarize release
   builds in CI with hardened runtime, and keep dev builds as they are.
4. **Sidecar privilege (TCC inheritance, §7).** *Recommend:* accept for v1, but require
   packages to declare `permissions: [accessibility, automation]` in the manifest and show it
   at install.
5. **Enrollment UX.** CLI login works but types a password into a terminal. *Recommend:* a
   device-code flow (`jarvis-agent enroll` prints a code, the app approves it), which needs a
   small jarvisd endpoint. Meanwhile, call logout after enrolling.
6. **Wayland window control.** *Recommend:* add a `hyprctl` backend behind the same Backend
   (list/focus/close work; minimise maps to a special workspace) only if desktop use on
   Hyprland matters. Otherwise keep "absent".
7. **Windows on real hardware.** The WASAPI float argument relies on the amd64 runtime mirroring
   integer registers into XMM; arm64 Windows is unverified, as is the foreground lock in
   focus. *Recommend:* one manual pass on a Windows box before anything ships.
8. **Repo and naming.** *Recommend:* rename the repo to `jarvis-agent` when it graduates. Move
   `internal/node` to an importable package only when the Go Pi node actually starts.
9. **Supervisor installers.** Templates exist; installers don't. *Recommend:* a
   `jarvis-agent service install|uninstall` subcommand that writes the user-scoped unit,
   agent or task, rather than shell scripts per OS.
