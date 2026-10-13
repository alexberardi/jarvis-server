# jarvis-agent (Go desktop node, POC)

`jarvis-agent` is a **Jarvis node that runs on a desktop**. It enrolls with jarvisd like a Pi
node, holds a node id/key, connects to jarvisd's MQTT, shows up in the app's node list, and
offers OS control as client tools: open a link or launch an installed app by name, list apps,
get/set volume, list/minimise/focus/close windows, and control the user's own browser through
the Jarvis Browser Control extension (tabs, page text, media, fill a field, click a button),
read the screen, type dictated text into a window or a named app and press dialog keys, and
answer the time and date. With `run --voice` it is a voice node (wake word, the on-screen island, and on a Pi
the ReSpeaker HAT). Status: pre-release, built from source; not in the jarvisd release archives.
Moved here from the `jarvis-host-agent` repo on 2026-10-10 so it can share jarvisd's packages
(`internal/voice`, `internal/audio`, later `internal/update`). Design, decisions and open
questions: **[docs/agent/DESIGN.md](../../docs/agent/DESIGN.md)**: read it first. The voice/Pi
roadmap is [docs/agent/go-pi-node.md](../../docs/agent/go-pi-node.md).

Nothing listens on a port: the agent talks to jarvisd only as a node (HTTP client + MQTT client).
The one local listener is the browser socket (a user-only Unix socket, see `browser/`).

> **Identity rule:** `internal/agent/host` owns *how to do a thing on this OS*. It owns no policy, no
> routing and no voice logic. Deciding *which* window the user meant, or *whether* they may,
> belongs in command-center or the tool layer (`internal/agent/desktop`). The host answers "minimise
> window 0x1234", never "minimise the thing I was just talking about".

## Quick reference

```bash
scripts/agent-dev-run.sh --server http://<jarvisd-host>:7700 --email you@example.com   # build+enroll+run
scripts/agent-dev-run.sh                             # later runs; --install / --uninstall / --status

export GOTOOLCHAIN=local                       # this box: mise exec go@1.27 -- go ...
go test ./internal/agent/... ./cmd/jarvis-agent  # contract, tools, node, deploy guards (any OS)
CGO_ENABLED=1 go test -race ./...              # what CI runs (whole repo)
JARVIS_HOST_NATIVE=1 go test -v -run Native ./internal/agent/host/   # drive the real host
for os in linux darwin windows; do GOOS=$os go vet ./internal/agent/... || echo FAIL $os; done
CGO_ENABLED=0 go build -o jarvis-agent ./cmd/jarvis-agent

./jarvis-agent status                          # capabilities now + enrollment + reachability
./jarvis-agent try                             # list the tools this host offers now
./jarvis-agent try desktop_set_volume '{"volume": 30}'   # run one tool locally, no jarvisd
./jarvis-agent apps steam                      # what a name resolves to (never launches)
./jarvis-agent browser install [--no-open]     # register the native host, set up the extension
./jarvis-agent browser status | uninstall      # registrations + connected browsers / remove
./jarvis-agent try browser_list_tabs           # waits for the extension, runs one browser tool
./jarvis-agent try --save-images /tmp/x desktop_read_screen '{"scope":"window"}'   # a real capture (0600 files)
JARVIS_SCREEN_NATIVE=1 go test -v -run TestNativeCapture ./internal/agent/screen/   # capture this desktop
JARVIS_DICTATE_NATIVE_LIST=1 go test -v -run TestNativeWindowsReadOnly ./internal/agent/dictate/  # list windows, read-only
./jarvis-agent run --confirm-dictation submit  # ask before dictation sends anything (off|submit|all)
scripts/browser-ext-lint.sh                    # web-ext lint of the Firefox build (CI runs it)
AMO_JWT_ISSUER=… AMO_JWT_SECRET=… scripts/browser-ext-sign.sh   # Mozilla-sign (unlisted) -> signed/
scripts/browser-ext-store-zip.sh chrome|firefox   # store upload zips -> dist/ (docs/agent/store-listing.md)
./jarvis-agent enroll --server http://<jarvisd-host>:7700 --email you@example.com
./jarvis-agent run
./jarvis-agent run --voice                     # + "hey jarvis" (Linux, macOS; off by default)
./jarvis-agent run --voice --no-overlay        # ... without the on-screen island (on by default)
./jarvis-agent run --voice --voice-input dsnoopmic --voice-output output   # a Pi with the HAT (--hat auto)
./jarvis-agent hat cycle | hat pattern thinking --for 5s | hat button   # the HAT by hand (no poweroff)
./jarvis-agent island-demo [--hold follow_up --for 20s]   # play the island's states (no mic, no server)
./jarvis-agent listen --voice-input wav:clip.wav --voice-output wav:reply.wav --once --no-chimes
                                               # one scripted voice turn, no MQTT (exit 0 = spoken);
                                               # runs the same client tools as `run` (--no-tools: none);
                                               # --follow-up 0 turns the follow-up window off
JARVIS_SHERPA_MODELS=$PWD/.models go test ./internal/agent/voice/   # + the real-model wake test
JARVIS_AUDIO_NATIVE=1 go test -v -run Native ./internal/audio/device/   # real default mic/speaker
```

The sidecar test needs a Python that can `import jarvis_command_sdk`
(`JARVIS_SIDECAR_PYTHON`, default `python3`); it skips otherwise. Locally:
`PYTHONPATH=../jarvis-command-sdk go test ./internal/agent/sidecar/`.

Gate on exit codes: never judge a run by output piped through `tail`.

**Never launch real GUI apps or open real links on a desktop someone is using** (that includes
`jarvis-agent try desktop_open ...`). App launching is covered by the fake app trees in
`internal/agent/apps` and the fake `Sys`/`Win32` in the host contract suite. The only native test
that touches the screen, `TestNativeOpen`, is off unless `JARVIS_HOST_NATIVE_OPEN=1`; keep any
future real-launch test behind an opt-in variable the same way. Use `jarvis-agent apps NAME`
to check a resolution: it never launches.

## Layout

```
cmd/jarvis-agent/     CLI: enroll, run, status, try, apps, browser, version (+ native host mode)
internal/agent/
  host/               Backend interface + Host (gate, clamp, validate) + linux/darwin/windows backends
                      -- read host.go first
  desktop/            the 8 client tools over Host, and the open policy
  apps/               installed-app catalogue (per-OS scanners), name resolution, apps.json
  macax/              macOS Accessibility trust check + prompt (purego, no cgo)
  node/               reusable node core: creds, discovery, CC client, enroll, MQTT runner, dispatch
  sidecar/            Python package sidecar spike (JSON lines over stdin/stdout)
  browser/            browser control: native-messaging host relay (nativehost.go), the agent's
                      socket + peer check (ipc.go), Hub of connected browsers (hub.go), the 9
                      browser_* tools (tools.go), host-manifest registration per OS/browser
                      (install.go), the embedded MV3 extension (extension/, extension.go) and
                      the Mozilla-signed .xpi when a build has one (signed/)
  screen/             read the screen: desktop_read_screen, grim/hyprctl/swaymsg (linux.go),
                      screencapture + the purego front-window lookup (mac.go, frontwin_darwin.go)
  winmatch/           naming a window ("firefox", "the terminal", title words): Pick (screen),
                      PickUnique (dictation: one match or ambiguous_window)
  dictate/            dictation (DESIGN.md §14): desktop_type_text, desktop_press_key and
                      desktop_confirm (tools.go, confirm.go); keyboards: the built-in Wayland
                      virtual keyboard and X11 XTEST, else wtype/ydotool/xdotool (linux.go), CGEvent (mac.go, cgevent_darwin.go), SendInput (win.go,
                      sendinput_windows.go); focus: hyprctl/swaymsg (linuxwin.go), the host
                      backend + an active-window check (hostwin.go)
  vkbd/               the Wayland virtual keyboard (zwp_virtual_keyboard_manager_v1), pure Go:
                      what wtype does, without wtype (DESIGN.md §14)
  x11/                X11 without helpers: XTEST typing (spare-keycode remap, settle, restore)
                      and EWMH windows (list, activate, close, iconify, active) over a narrow
                      Conn (x11.go, unit-tested with a fake everywhere); the real Conn is
                      github.com/jezek/xgb (dial_linux.go)
  xkb/                keysyms for text and dictation's keys, and the minimal XKB keymap a
                      virtual keyboard uploads (pure, every OS)
  wlwire/             the Wayland client wire (connection, ids, args, registry), shared by the
                      island and vkbd
  hat/                the ReSpeaker 2-mics HAT (Pi nodes): APA102 LEDs with the Python node's
                      patterns (leds.go, golden-tested), the GPIO17 button (button.go), spidev +
                      gpiocdev (device_linux.go); voice states reach it through voice.Indicator
  island/             the on-screen voice indicator (DESIGN.md §11) and dictation's note (Note):
                      pure-Go renderer (render.go),
                      animation model + loop (island.go), NSPanel backend (mac_darwin.go, purego),
                      Wayland layer-shell client (wayland.go, over wlwire); Run keeps the main
                      thread on macOS
  voice/              the voice pipeline: wake -> record -> STT -> stream reply -> follow-up
                      window -> listen again (pipeline.go), the conversation loop: tool rounds +
                      validation questions + the final message (converse.go), client tool calls
                      (tools.go), the follow-up window and its noise/echo filters (followup.go),
                      the speech level the follow-up gate weighs (level.go),
                      Python-node endpointing (endpoint.go), the CC voice client (client.go),
                      wake-model fetch (models.go), Serve (reopen on failure)
internal/audio/device/  mic + speaker: ALSA (purego, Linux), AudioQueue (purego, macOS), wav:FILE
  deploy/             systemd user unit, LaunchAgent, Scheduled Task, macOS Info.plist (+ guard tests)
scripts/agent-dev-run.sh  build (+ sign the .app on macOS), enroll if needed, run / install
```

`internal/agent/node` knows nothing about desktops; a Go Pi node would reuse it unchanged and plug
in its own `Toolset`.

**Voice** (phase 3 of docs/agent/go-pi-node.md §7; what landed and what's measured: its §3.7)
is off unless `run --voice`. It uses jarvisd's shared blocks: `internal/voice/ort`
(onnxruntime over purego, the *same* embedded `libonnxruntime` as `internal/voice/sherpa`, so
linux/darwin agent binaries embed it: ~44 MB instead of ~12), `internal/voice/oww`
(openWakeWord, golden-tested against Python) and `audio.Decimator`. `internal/agent/voice`
knows nothing about desktops: the Pi node will run it with HAT device names. Self-update will
reuse `internal/update` once it stops assuming the jarvisd binary.

## How drift between platforms is prevented

1. **`host.Host` is the only caller of a `Backend`.** Capability gating, the volume clamp and
   capability-name validation live there once, so three backends can't disagree on them.
2. **`Backend.ApplyVolume` receives an already-clamped level.** One clamp (`Host.SetVolume`).
3. **One contract suite, every backend, every OS** (`internal/agent/host/contract_test.go`), with
   fakes for `Sys` (shell-outs) and `Win32`. It was mutation-checked: removing the clamp,
   no-oping the gate, echoing the requested volume, killing instead of closing (each
   platform), typo'ing a capability, skipping the unknown-capability check, splicing an
   unvalidated macOS window id, and advertising minimise without xdotool are all caught.

## Invariants & gotchas

1. **Backends are constructible on any OS.** No probes, subprocesses or syscalls at
   construction. Backend files are deliberately *not* named `*_linux.go` / `*_darwin.go` /
   `*_windows.go`; only `win32_windows.go` / `win32_other.go` are build-constrained, behind
   the `Win32` interface. Break this and the Windows backend becomes untestable off Windows.
2. **Capabilities are computed per call, never cached** (AX grants get revoked, sessions come
   and go). Tools are offered per `report_tools` from the capabilities at that moment.
3. **Volume clamps, never errors.** 150 -> 100, -5 -> 0. Don't add range validation at any
   layer; numeric strings ("30", "45%") are accepted.
4. **A backend exposes exactly the `Backend` methods** (reflect test). Platform helpers stay
   unexported.
5. **Capability names are a closed set** (`KnownCapabilities`); `Host` rejects others.
6. **`open` is fire-and-forget.** No launch confirmation; it is unreliable everywhere. Linux
   refuses without a graphical session (xdg-open on a headless box exits 0 having done nothing).
7. **Close is graceful, never a kill:** `WM_CLOSE`, `wmctrl -c`, the window's AXCloseButton.
8. **Window ids are opaque** (decimal HWND, X11 `0x…`, macOS `pid/index`), but each backend
   validates its own format before an id reaches a subprocess or AppleScript. The Python
   macOS provider spliced ids into AppleScript unvalidated; don't regress that.
9. **The open policy is in `internal/agent/desktop` + `internal/agent/apps`, not `host`.** Allowed: http,
   https and mailto links, and any installed app *named* by the model, resolved against the
   catalogue (platform app folders + `apps.json` allow globs − deny globs). The launched path is
   always the catalogue's: never a path, URI scheme or argument from the model. Ambiguity is
   reported with candidates, never guessed. `--allow-open-any` restores "anything the OS can
   open" (i.e. program execution).
10. **Runs in the user's interactive session on all three platforms** (systemd *user* unit,
    LaunchAgent in `gui/$UID`, Scheduled Task with `InteractiveToken` + `LeastPrivilege`).
    Never root/system/daemon/Session 0. `internal/agent/deploy/deploy_test.go` fails CI on a conversion.
11. **The node key never reaches a log or stdout.** `Creds` implements `slog.LogValuer` and
    `String()` without it; the creds file is 0600 in a 0700 dir and `LoadCreds` refuses a
    group/world-readable one.
12. **Every `tool_call` is verified** with `POST /commands/{rid}/verify` before running
    (single use). Broker ACLs already confine publishers; this is defence in depth for a node
    that can open things.
13. **Wayland has no window control** (no cross-client API). XWayland's `DISPLAY` does not
    count. Per-compositor backends are an open question.
14. **macOS window control needs Accessibility for "Jarvis Agent.app"** (`io.jarvis.agent`) plus
    an Automation grant for System Events. A bare binary added with "+" never shows in the list
    on macOS 26, so the agent ships as a bundle and calls `AXIsProcessTrustedWithOptions`
    (prompt) at startup, which makes macOS add the entry. Grants key on the signed identity:
    Developer ID / Apple Development signing (team + identifier) survives rebuilds, ad-hoc
    (cdhash) doesn't. Run it under launchd (dev-run.sh does, even in the foreground): started
    from a terminal, TCC attributes everything to the terminal app. For the same reason
    `jarvis-agent status` from a terminal reports the terminal's grants; use
    `scripts/agent-dev-run.sh --status`. Probing really lists windows (`accessProbe`).

15. **Voice behaviour is the Python node's, ported with its numbers** (threshold 0.4, 8 s
    debounce, RMS endpointing, normaliser, non-speech filter, 48 kHz uploads, speaker_audio =
    wake clip + command). `endpoint_test.go` holds goldens computed from the Python functions;
    change them only together with the Python node, or deliberately (log it in go-pi-node.md).
16. **`/conversation/end` goes out after the turn *and* start have finished**, never on start
    alone (a test catches the early-end bug that ended conversations before the stream).
17. **Any helper that forwards `uintptr(unsafe.Pointer(&x))` to C must be
    `//go:uintptrescapes`** (`ort.Runtime.call`, `host.comCall`). Without it `x` can live on the
    goroutine stack and move during the call; onnxruntime then saw NULL inputs at random
    (`TestProcessAtAnyStackDepth`).
18. **macOS microphone = TCC.** The bundle needs `NSMicrophoneUsageDescription` (guarded in
    deploy_test.go; without it macOS kills the app on first capture), and a process started over
    SSH or from a terminal gets silence or the terminal's grant: run the bundle under launchd
    (`scripts/agent-dev-run.sh -- --voice`) and allow the prompt.
19. **Only the capture goroutine touches the Capture** until `Pipeline.Run` returns (ALSA's
    blocking read can't be interrupted from another thread); Run waits for it before the
    caller closes the device.
20. **Voice turns and MQTT serve one Toolset.** `run` builds a single `node.Multi` (desktop,
    then sidecar) and passes the same value to the dispatcher and `voice.Serve`; never give voice
    its own tool list. Voice continues are the *blocking* `/voice/command/continue` so tools can
    chain (list windows -> minimise); the stream route can't chain (go-pi-node.md §3.8). Tool
    calls in a voice turn aren't `/verify`'d: they come on the node's own authenticated HTTP
    response. Each call is bounded by `Config.ToolTimeout` and a panic is a failure result.
21. **Follow-up is the Python node's, widened by command-center's gate** (go-pi-node.md §3.9,
    §3.10): a window after every reply not marked `end_of_exchange` / `not_for_me`, 8 s then 6,
    then 5 (Python: 4 then 3), ≤ 5, answers end after 1.2 s of silence (Python 0.5) and may run
    10 s; sent as blocking `/voice/command` follow_up turns of the same conversation with
    `level_vs_wake_db`. A `not_for_me` with `keep_listening` is ignored silently and the window
    listens again (same iteration, no flush: nothing was said), at most `MaxIgnored`, for only
    what is left of the window (≥ `IgnoredGraceSecs`, 2 s): room speech never restarts it; without
    `keep_listening` (an older server) it closes the window as before. `validation_required` is
    asked aloud and answered as `validation-response` on the blocking continue. Before
    wake-free listening *after we spoke* the queue is flushed (our own reply) and a quiet-wait
    runs; keep both, a desktop has no AEC. Windows are counted in captured chunks, never
    wall-clock, so tests script the user through `Pipeline.listening` instead of racing timers.
22. **Command signals never leave the node.** A Toolset may put `node.Signals`
    (`wait_for_input`, `clear_history`, `on_response_complete`) under `node.SignalsKey` in an
    output; every consumer (the dispatcher, the voice loop) calls `node.TakeSignals` before
    posting. A deferred step runs after the conversation ends (voice) or after the result is
    posted (MQTT).
23. **Browser control** (DESIGN.md §10). One binary is agent and native host: `main()` checks
    argv (`chrome-extension://…`, or Firefox's `<manifest> browser@jarvis.agent`) before
    anything else, and in host mode **stdout is native frames only** (a test re-execs `main`
    to check). Browser tools are offered only while a browser is connected, from the same
    `node.Multi`. Page text is untrusted: it goes back between `PageBegin`/`PageEnd` with
    `<<<`/`>>>` defanged; never extract form fields. The extension takes commands only from
    its native port, from a closed set: `TestExtensionSurface` + `TestCommandParity` guard
    that; a new command needs both sides. Tab ids are `KEY:TAB` (stable browser keys).
    Socket: user-only dir, 0600, peer uid checked; never widen it. Test with headless
    browsers only (`web-ext run --arg=-headless`, `chromium --headless=new --load-extension`)
    in a scratch HOME with `JARVIS_AGENT_BROWSER_SOCKET` set: never load the extension into
    the user's real profiles. Sun_path is ~104 bytes: keep scratch socket/TMPDIR paths short.

24. **The island never takes focus, input or the agent down** (DESIGN.md §11). Click-through
    (`ignoresMouseEvents`; an empty Wayland input region), non-activating, no Dock/taskbar
    entry; any backend failure logs once and turns it off. On macOS **AppKit runs only on the
    main thread**: `main_darwin.go` locks the main goroutine, `island.Run` must be called from
    it (`run` does, directly from `main`), and every AppKit call goes through `onMain`
    (never call it from the main thread's own callbacks into Go code that waits on the agent).
    The pipeline only *reports* state (`voice.Indicator`, nil by default); it never waits on
    the screen. Don't show it on a desktop someone is using in tests: the live checks
    (`JARVIS_ISLAND_MAC_LIVE`, `JARVIS_ISLAND_WAYLAND_PROBE`) are opt-in, and the Wayland one
    never attaches a buffer.

25. **A screenshot exists only for the turn that asked** (DESIGN.md §12). Captured only by
    `desktop_read_screen` on request, kept in memory (macOS's `screencapture` file: 0600 in a
    0700 temp dir, removed before `Capture` returns), never logged, sent as `image_attachments`
    in the tool output; command-center drops it at commit. Never add a cache, a "last
    screenshot" or a capture the model didn't ask for. The island is suspended for the
    capture. Don't capture someone's desktop in tests unless they agreed (`JARVIS_SCREEN_NATIVE`
    is opt-in) and never commit a capture.

26. **The HAT is the Python node's, ported with its numbers** (go-pi-node.md §3.11): patterns,
    colours and brightness are golden-tested against `respeaker_led_service.py` and `apa102_pi`
    (`tools/golden/export_hat.py`, run on a Pi); button 50 ms debounce, warning at 1 s, poweroff
    at 3 s. Change them only together with the Python node, or deliberately (log it in
    go-pi-node.md). The HAT is optional at every step: not detected, an SPI or GPIO error (the
    Python node holding the button line), or `--hat off` leaves the agent running without it.
    Never press "power off" in a test: `hat button` never powers off, and `Button.OnHold` is a
    function so tests use a fake.

27. **Dictation never types into a window it couldn't confirm has focus** (DESIGN.md §14). A
    named app is resolved by `winmatch.PickUnique` (one match, or the focused/only visible one,
    else `ambiguous_window`), focused, then the active window is polled until it is that window;
    otherwise `focus_failed` and nothing is typed. Text: `dictate.Sanitize` (≤ 2000 characters,
    line breaks → spaces, control/format characters dropped), on stdin never argv, never in a
    log (`node.RedactedArgs`) or a result. Enter only as the `submit` step; keys from the closed
    `dictate.Keys` set. A confirmation (`--confirm-dictation`) is single use, replaced by a newer
    one, and cancels on no, unclear or timeout (`Confirmer.Now`/`Timeout` are injected in
    tests). **Never type or press keys in a desktop someone is using in tests:** use fakes, or a
    nested headless compositor (`WLR_BACKENDS=headless sway` with its own `WAYLAND_DISPLAY`,
    `env -i` so no `HYPRLAND_INSTANCE_SIGNATURE` leaks in) and `jarvis-agent try`.

28. **Built-in typing and window control first, helpers as fallback** (installers §9.3 item 6:
    the agent binary is the whole install). On Wayland the virtual keyboard (`vkbd`) is used
    whenever the compositor advertises it; wtype/ydotool only when it doesn't. On X11, XTEST
    (`x11`) types and EWMH drives windows (`host.Linux.X`, set only by `host.Native`, so tests never
    reach a real X server; the dictate backends read the session through `Exec.Getenv` for the
    same reason); xdotool/wmctrl only without XTEST or an EWMH window manager. An XTEST remap is
    always restored (a deferred blank of every spare keycode used). `JARVIS_X11_TYPE=…` /
    `JARVIS_X11_LIST=1 go test -run Native ./internal/agent/x11/` are for a throwaway Xvfb only. Availability is probed per call (one
    connection, nothing created). A built-in backend's failure is returned, never retried through a
    helper: keys may have half-arrived. `vkbd`'s settle and clock are injected; its tests use a
    scripted compositor on a socketpair. `JARVIS_VKBD_TYPE=… go test -run TestNativeType
    ./internal/agent/vkbd/` types for real, **in a nested compositor only**.

## Dependencies

Beyond jarvisd's own: `github.com/eclipse/paho.mqtt.golang` (jarvisd embeds a broker; the agent is a client), `golang.org/x/sys` (Windows syscalls),
`golang.org/x/term` (password prompt), `github.com/jezek/xgb` (X11 protocol for XTEST typing and
EWMH window control; pure Go, no dependencies, Linux builds only, +0.5 MB), `github.com/warthog618/go-gpiocdev` (the HAT button, pure
Go), `github.com/ebitengine/purego` (macOS AX, AudioToolbox,
libasound and onnxruntime via dlopen). `CGO_ENABLED=0` builds everywhere: Windows volume is
WASAPI over raw COM vtables, windows are user32, open is ShellExecuteW, macOS AX is dlopen'd
ApplicationServices. Voice at runtime needs `libasound.so.2` on Linux (present on any desktop).

## Not in scope (unchanged)

- **Mute** (if it returns: `set_muted(bool)`, never a toggle).
- **Clipboard and file mutation** (needs allowlisted roots and trash-not-unlink first).
- **Choosing which machine to act on**: the user picks the node; multi-machine targeting
  belongs in CC.
