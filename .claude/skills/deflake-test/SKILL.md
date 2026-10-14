---
name: deflake-test
description: Find and fix the cause of a flaky Go test (passes locally, fails on a CI runner or 1 run in N) instead of retrying, widening a timeout or adding a sleep. Use it when a CI job fails in a test unrelated to the change, when a test fails only under -race, on Windows/macOS/arm64 runners or on a fast desktop, or when asked to make a test deterministic.
---

# Deflaking a Go test

House rule (#53, #55): **wall-clock waits are only hang guards.** Ordering comes from injected
clocks, timers and explicit sync points. A sleep or a wider window is not a fix. Two of the
flakes here were real production bugs (the mqtt takeover ghost in #55, the phone hang-up in
#115), so look for one before you touch the test.

## 1. Reproduce

```bash
# stress the one test: many runs, several GOMAXPROCS, race detector on (needs cgo; test-only)
CGO_ENABLED=1 go test -race -count=200 -cpu=1,2,4 -run '^TestName$' ./internal/<pkg>/
# whole package, in case another test's leftovers are the cause
CGO_ENABLED=1 go test -race -count=20 ./internal/<pkg>/
```

- Find the CI failure with `gh run view <run-id> --log-failed`. Note the runner: windows-latest,
  macos-14, linux-arm64 and slow `-race` runners each expose different windows.
- If it won't fail locally, **widen the window on purpose**. Add a temporary delay at the
  suspected point and confirm the test then fails every time. #80 delayed a fake engine's start
  line by 300 ms. Remove the delay once the fix holds under it.
- For a race between `Add`/`Wait`, or with a library goroutine, a dedicated test that stops
  right after starting reproduces it reliably (#38, `TestAdvertiseStopImmediately`: 10/10 before,
  0/10 after, at `-count=200 -cpu=1,2,4`).
- Failing on every run on a fast desktop but green on CI points to a zero-duration
  measurement (#78: a span rounded to 0 ms and was dropped).

## 2. Fix patterns used in this repo

| Symptom | Fix (example) |
|---|---|
| A fake clock advanced after an async call races the worker | Make the clock return t0 on its first read and t0+Δ on every later one (`svc.setNow`, #107 `internal/modules/llm/jobs_test.go`) |
| A duration that is too small to observe | Make the clock replaceable (an atomic pointer, nil = `time.Now`) and step it 1 ms per read (#78, `internal/modules/cc/traces.go`) |
| A wait for health/ready returns before the thing under test counted itself | Wait for the observable event, such as the first start line, before the next step (#80, `internal/platform/engines/engines_test.go`) |
| A timer or ticker inside the code | An injectable `afterFunc` / ticker field, fired by the test (#53 `internal/agent/hat/button.go`, the island loop) |
| A producer outruns a lossy consumer under `-race` | Run them in lockstep in tests only (unexported `Pipeline.lockstep`, #53 `internal/agent/voice/pipeline.go`) |
| A negative check ("X did not happen") done with a sleep | Wait for a later event that proves X had its chance, then assert |
| A handler registered after a request that triggers it | Register before you send (#53, Wayland `wl_output`: a real bug) |
| A file is the readiness signal but is written before state is set | Set the state first, then write the file, and wait on the file with a hang guard (#72, auth setup token) |
| A `select` with two ready channels picks at random | Handle both orders in the code. Test with both channels ready ×32 (#115 phone `supervise`) |
| Ports from a FreePort helper race other tests | The test holds the listener or serves the fake itself (#55 engines, cmd/jarvisd) |
| Multicast, a real desktop or a real network is needed | A hermetic fake by default, with the real thing behind an opt-in env var (`JARVIS_TEST_MULTICAST=1`, `JARVIS_TRAY_WIN_LIVE=1`) |

Hang guards are 30 s on the server side (#55): `select { case <-done: case <-time.After(30*time.Second): t.Fatal("hang") }`.
They are never waited on when the code works.

## 3. Gotchas

- `-count>1` can break package-level registration. Register test backends once (#55,
  `internal/platform/blob/open_test.go`).
- A fake server kept alive with `select{}` deadlocks (exit 2) when no other goroutine runs
  without `-race` (#55 engines).
- On Windows a DB or DLL stays locked briefly after Close or Load. Use a temp dir with retried
  removal, or a persistent dir for extracted libraries (fd214e9, 0c9e4e0).
- mochi-mqtt and grandcat/zeroconf had their own races. Check upstream before blaming your
  code. zeroconf was replaced by libp2p/zeroconf/v2 (#38), and mochi's Close deadlock is worked
  around in `internal/platform/mqtt/shutdown.go`.
- Judge by exit codes, never by output piped through `tail`.

## 4. Land it

- Commit subject: `test(<pkg>): make TestX deterministic` (#107). If production code was wrong,
  use `fix(<pkg>): …` instead (#72, #115). In the body, give the CI symptom (quoted), the cause
  in a sentence or two, how you reproduced it, and why the fix closes the window.
- Re-run the stress command from step 1 with the fix in place and say so in the PR.
- If STATUS.md lists the flake as a follow-up, strike it through with `~~…~~ **Fixed <date> (#N):** <cause>`
  (see the `TestInstallRepoShardsResumeAndSlices` entry), then follow `land-pr`.
