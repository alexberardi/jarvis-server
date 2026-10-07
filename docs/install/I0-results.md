# I0 results: installer spike (Phase 6)

Spike for `00-installers.md` §8 row I0. No product code changed. Helpers are in [`i0/`](i0/):
`macos-launchd.sh` (runs jarvisd under launchd on alternate ports and checks health, Metal, mDNS
and LAN reachability), `svcwrap/` (a throwaway SCM wrapper, since jarvisd does not answer the SCM
yet), `mdnsbrowse/` (lists every address an instance advertises), `windows-spike.ps1` and
`windows-spike.yml` (the GitHub Actions run, from the deleted throwaway branch `spike/i0-windows`).

Dates: 2026-10-07. macOS: MBP M2 Max, macOS 26.1 (25B78), jarvisd cross-built
`CGO_ENABLED=0 -trimpath` with the darwin-arm64 sherpa libs embedded, ports +10000 next to the legacy
stack. Windows: `windows-latest` = Windows Server 2025 Datacenter 10.0.26100, Windows PowerShell
5.1, no GPU (run [37670434750](https://github.com/alexberardi/jarvis-server/actions/runs/37670434750);
a second run with the long-path workarounds is
[37671747908](https://github.com/alexberardi/jarvis-server/actions/runs/37671747908), see the end).

## Results

| Row | Result | Evidence |
|---|---|---|
| macOS: jarvisd from a LaunchDaemon (`UserName` = login user) serves `/health` | **blocked** | `sudo` on the MBP needs a password (`sudo -n true` → "a password is required"); not worked around. Commands for the user below. |
| macOS: same as a **LaunchAgent** (`gui/501`), as a proxy | yes | Parent `/sbin/launchd`, `domain = gui/501`; `/health` `{"status":"ok"}` 2 s after start. Bootstrapped over SSH the job sat in `pended nondemand spawn = speculative` with `runs = 0` and did not start until `launchctl kickstart` (I1: kickstart after bootstrap). |
| macOS: Metal compute from the launchd job | yes (agent); daemon blocked | `/v1/hardware`: flavour `metal`, `MTL0 Apple M2 Max`, source `llama-server --list-devices (metal)`. Model manager installed `Qwen/Qwen2.5-0.5B-Instruct-GGUF` q4_k_m (503 MB) with engine `b11457-metal`; label `live` ready; chat through jarvisd's `/v1/chat/completions` (app client) answered `OK`, prompt 237 tok/s. The engine process (child of jarvisd, `-ngl 999`) has `AGXMetalG14X.bundle` and `libggml-metal` mapped and the per-user `com.apple.metal` shader cache open. |
| macOS: mDNS `_jarvis-config._tcp` visible on the LAN, no Local Network prompt | yes (agent); daemon blocked | `dns-sd -B` on the Mac and `avahi-browse -rt` on 10.0.0.122 both list `Jarvis (Alexanders-MacBook-Pro)` port 17700; `mdnsbrowse` from 10.0.0.122 gets v4 `[10.0.0.103 192.168.64.1]`. Unicast probe: jarvisd (agent) fetched `http://10.0.0.122:7700/...` and got that host's 404 (a dead IP gave `i/o timeout`), so neither multicast nor unicast LAN traffic was blocked. No LNP/nehelper/tccd log lines for the process. I cannot see the Mac's screen, so "no prompt shown" is inferred from traffic not being held. |
| Windows: raw `jarvisd.exe` as an SCM service under `NT SERVICE\jarvisd-raw` | no (expected; I1 work) | `sc start` → `FAILED 1053` after 142 s, state STOPPED, process gone (SCM killed it). Before dying it created its home at `C:\Windows\ServiceProfiles\jarvisd-raw\.jarvis`, confirming the service **must** pass `--home`. |
| Windows: jarvisd as an SCM service under `NT SERVICE\jarvisd` (via `svcwrap`) | yes | `sc.exe create … obj= "NT SERVICE\jarvisd"`, `sidtype unrestricted`, ACL on `C:\ProgramData\jarvisd` granting `NT SERVICE\jarvisd:(OI)(CI)M` worked. `jarvisd.exe` owner `NT SERVICE\jarvisd`, session 0, `/health` ok, home `C:\ProgramData\jarvisd`, all listeners bound. Binaries in `C:\Program Files\jarvisd` were readable by the virtual account. |
| Windows: llama-server under the virtual account in session 0 (CPU) | yes | Model manager installed the same 0.5B model with `b11457-cpu` in 9 s; `live` ready; chat `OK`; `llama-server.exe` owner `NT SERVICE\jarvisd`, session 0. |
| Windows: engines bind loopback only | yes | `netstat`: `TCP 127.0.0.1:64493 LISTENING 5172` (llama-server), nothing else for that PID. (macOS same: `127.0.0.1:60621`.) |
| Windows: model path > MAX_PATH (314 chars, `models\<39>--<96>\<60>\<80>.gguf`) | **no** | jarvisd itself handled it (register via the API saw size/state ready), but llama-server failed `gguf_init_from_file: failed to open GGUF file … (No such file or directory)` with `LongPathsEnabled` 0 **and** 1 (runner default 1): the upstream llama-server.exe is not long-path-aware. Run 2 tests `\\?\` and 8.3 workarounds. |
| Windows: mDNS on 5353 next to the DNS Client service | yes, with a caveat | Before jarvisd: `svchost` (Dnscache, pid 1228) on `0.0.0.0:5353` and `:::5353`. jarvisd bound without error, logged `mdns advertising`, and a separate `mdnsbrowse` saw `Jarvis (runnervm…)` port 7700. Caveat: with jarvisd running `Get-NetUDPEndpoint` listed only jarvisd on 5353; whether Windows' own mDNS resolver still works is checked in run 2 (`Resolve-DnsName <host>.local`). Not tested from the Android app (no LAN path to a hosted runner). |
| Windows: GPU (CUDA / Vulkan) in session 0 | **untested** | Hosted runner has only "Microsoft Hyper-V Video". Needs a real Windows box with an NVIDIA GPU (or a GPU self-hosted runner): install the service as above, `POST /v1/models/install` with `gpu_backend` `cuda` then `vulkan`, confirm `CUDA0`/`Vulkan0` in `/v1/hardware` and the engine log. |

## Other findings

1. **mDNS hostname is doubled on macOS**: the SRV target is `Alexanders-MacBook-Pro.local.local.`
   (`os.Hostname()` already ends in `.local`; zeroconf appends `.local.`). avahi resolves it only
   via jarvisd's own answer. Fix in `internal/platform/mdns` (strip a trailing `.local`).
2. **mDNS advertises bridge addresses**: the MBP announced `192.168.64.1` (bridge100) besides
   10.0.0.103, and `avahi-resolve` picked the bridge one; this box announces 10 docker bridges. A
   client that takes the first address can fail. Worth restricting to interfaces with a default
   route / private LAN (I2 or a small fix).
3. **Windows stop**: jarvisd has no graceful stop on Windows today (no SIGTERM); the wrapper kills
   it. Run 2 checks whether that orphans a running llama-server. I1's `svc` handler cancelling the
   context avoids it on a clean stop; a Job Object would also cover crashes.
4. `launchctl bootstrap` from a non-GUI context did not honour `RunAtLoad` immediately (see the
   LaunchAgent row); `jarvisd service install` should `kickstart` after `bootstrap`.

## IQ1 / IQ2

- **IQ1 (accounts and directories): holds**, with the daemon check still open. The Windows half is
  confirmed: a virtual account works, the ACL model works, and the default home lands in
  `ServiceProfiles`, so `--home %ProgramData%\jarvisd` is required as the doc says. macOS: Metal and
  LAN/mDNS work from a launchd job as the login user; the doc's claim that a LaunchAgent would be
  held by Local Network privacy was **not** observed on macOS 26.1 (traffic flowed). That weakens the
  LNP argument for a daemon but not the main one (runs before login). Confirm with the daemon run.
- **IQ2 (SCM service vs scheduled task): (a) holds for everything testable** (virtual account,
  session 0, engine start, loopback bind, clean SCM stop through a wrapper). The deciding row,
  CUDA/Vulkan in session 0, is untested; keep "(a), switch to (b) only if CUDA fails" and run that
  on real hardware in I9 at the latest. New input for I1: long model paths must be handled by
  jarvisd (keep `%ProgramData%\jarvisd` short, cap/shorten model dir names, or pass a `\\?\`/8.3
  path), because `LongPathsEnabled` does not help the upstream engine.

## Blocked row: commands for the user (MBP, needs the sudo password once)

`~/jarvisd-i0/` on the MBP still holds the spike binary and script (the throwaway home was removed).

```sh
ssh alexanderberardi@10.0.0.103
cd ~/jarvisd-i0
./macos-launchd.sh up daemon        # sudo install plist to /Library/LaunchDaemons, bootstrap system, kickstart
./macos-launchd.sh check            # /health, launchd domain/user, dns-sd browse + lookup
./macos-launchd.sh metal            # superuser + app client, install 0.5B GGUF, chat, engine sockets
./macos-launchd.sh lnp http://10.0.0.122:7700   # jarvisd -> LAN unicast
# from 10.0.0.122: avahi-browse -rt _jarvis-config._tcp
./macos-launchd.sh down daemon      # bootout + remove plist
./macos-launchd.sh purge && rm -rf ~/jarvisd-i0
```

Run as the login user (not under `sudo`); the script calls `sudo` for the plist install, bootstrap,
kickstart and bootout only.
