---
name: cross-os
description: Keep code building, passing tests and behaving the same on linux, darwin and windows. Use it when touching OS-specific or native code (internal/agent/**, purego/syscall bindings, internal/voice, internal/audio, internal/platform/{service,update,engines,blob}, cmd/jarvisd service/upgrade), shell scripts that run on macOS or in CI, golden or testdata files compared byte for byte, or when a CI job fails on only macos-14 or windows-latest.
---

# Cross-OS changes (linux, darwin, windows)

Release targets are linux/amd64, linux/arm64, darwin/arm64 and windows/amd64. Everything is
`CGO_ENABLED=0`. Native libraries come in through purego. Only the race detector uses cgo,
in tests. Most work happens on Linux, so CI is the first time macOS or Windows sees the code
unless you vet for them yourself. For the desktop agent, read `internal/agent/CLAUDE.md`
("Invariants & gotchas") first.

## Check before pushing

```bash
scripts/fetch-sherpa-libs.sh linux-amd64 windows-amd64 darwin-arm64   # once: vet needs the embedded libs
for os in linux darwin windows; do GOOS=$os CGO_ENABLED=0 go vet -tags contract ./... || echo "FAIL $os"; done
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go vet ./...                    # CI's actual darwin target
```

- `scripts/check` runs the three-OS vet with gofmt and race tests. It vets darwin on the host
  arch, so `darwin/arm64`-only files need the extra line above.
- Without `libs/windows-amd64`, Windows vet fails with
  `embed_windows_amd64.go: pattern libs/windows-amd64: no matching files found`. That is a missing
  fetch, not a code error.
- CI (`.github/workflows/ci.yml`) vets and cross-compiles all 4 targets in `build`. Real code
  runs in `native-voice` (linux amd64/arm64, macos-14, windows-latest) and `agent` (ubuntu,
  macos-14, windows-latest), including `scripts/agent-install-check.sh` and the `-run Native`
  host tests.

## Rules that bit before

- **Line endings.** A testdata or template file compared byte for byte needs `-text` in
  `.gitattributes`. Otherwise Windows checkouts get CRLF and the comparison fails there only
  (b7279a6: signed update test vectors). Existing entries: `internal/update/testdata/**`,
  `internal/modules/llm/models/templates/**`, `internal/agent/deploy/**`,
  `internal/agent/install/testdata/**`, `internal/platform/qrterm/testdata/**`.
- **Paths for another OS.** Use `path.Join`, not `filepath.Join`, for a path that is Linux
  whatever OS runs the code, such as sockets, `/run/…` and unit files. Tests run on Windows too
  (`internal/agent/dictate/linux.go` `ydotoolWorks`).
- **Agent backends are constructible on any OS.** No probes or syscalls at construction, and
  no `_linux.go`/`_darwin.go`/`_windows.go` backend files. Only `win32_windows.go` /
  `win32_other.go` sit behind the `Win32` interface, so the Windows backend stays testable off
  Windows (agent CLAUDE.md invariant 1).
- **purego on Windows has no by-value float arguments.** It routes through
  `syscall.SyscallN`, so pick a C entry point that takes a struct or pointer instead
  (`internal/voice/sherpa/capi.go`: TTS uses `GenerateWithConfig`).
- **`uintptr(unsafe.Pointer(&x))` passed to C** needs a `//go:uintptrescapes` helper, or `x` may
  move during the call (agent invariant 17, `TestProcessAtAnyStackDepth`).
- **macOS ships bash 3.2.** Bash scripts that run there (`scripts/fetch-sherpa-libs.sh` in CI,
  `scripts/agent-dev-run.sh`, `scripts/agent-install-check.sh`) cannot use associative arrays,
  `mapfile`, `${x,,}` and similar. Use `case` (0c9e4e0). `scripts/install.sh` is POSIX `sh`.
  Linux-only dev scripts (`scripts/check`, `scripts/sandbox`) may use bash 4+.
- **macOS AppKit runs only on the main thread.** `cmd/jarvis-agent/main_darwin.go` locks the
  main goroutine for the island and tray.
- **Windows locks loaded DLLs and recently closed files.** Extract native libs to a persistent
  dir in tests, and retry temp-dir removal (0c9e4e0, fd214e9). Blob renames on Windows need POSIX
  semantics (62eb536).
- **launchd/systemd PATH is minimal.** Look for Homebrew/MacPorts tools explicitly (2cf50d9,
  tesseract).
- Per-OS messages in assertions: a test expecting a Linux hint fails on Windows (045bcac,
  `TestReportDoctor`). Assert on the OS-specific text or branch on `runtime.GOOS`.

## Live, desktop-touching tests are opt-in

Never show UI or launch apps on a desktop someone is using. Live checks skip unless their
variable is set: `JARVIS_TRAY_WIN_LIVE=1`, `JARVIS_TRAY_MAC_LIVE=1`, `JARVIS_ISLAND_MAC_LIVE=1`,
`JARVIS_HOST_NATIVE=1`, `JARVIS_HOST_NATIVE_OPEN=1`, `JARVIS_SCREEN_NATIVE=1`,
`JARVIS_AUDIO_NATIVE=1`, `JARVIS_TEST_MULTICAST=1`. Give a new live test its own variable the same way.

## Trying it on a real machine

- Linux/macOS agent: `scripts/agent-dev-run.sh` (build, enroll once, run; `--install`, `--status`).
  On macOS it signs the `.app` so the Accessibility grant survives rebuilds.
- `scripts/agent-install-check.sh <bin>` is **CI only**. On Windows it registers for real in the
  runner's profile. It refuses to run unless `CI` or `JARVIS_INSTALL_CHECK_OK=1` is set, so use it
  only on a throwaway machine.
- No Windows desktop is available here. Say "built, vetted and CI-tested, not run on a Windows
  desktop" in the PR and in STATUS, as #114 did.
