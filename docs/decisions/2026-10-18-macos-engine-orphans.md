# macOS: engines orphaned by a crashed jarvisd are swept at the next start

- **Date:** 2026-10-18 (build agent, loop item #121)

On Linux an engine (llama-server, whisper-server) gets Pdeathsig and on Windows it sits in a
kill-on-close job object, so it dies with jarvisd. macOS has neither: if jarvisd is SIGKILLed or
crashes, the engine is reparented to launchd in its own process group and keeps holding unified
memory until someone kills it.

Chosen: a pid file per running engine (`<home>/engines/run/<engine>.pid`, written by
`internal/platform/engines` right after launch, removed once the process is reaped) and a sweep
the first time the llm resolver is used, before it starts any engine. The file records the
process's executable and start time as the OS reported them at launch; the sweep kills the
process group (SIGTERM, then SIGKILL after 5 s) only when a live process still matches both, so a
reused PID is never touched. It runs on Linux too (harmless, and it keeps the code path tested in
Linux CI); Windows writes no pid files.

Not chosen (for now): a kqueue `NOTE_EXIT` watchdog. The engines are third-party binaries, so it
would need a shim process between jarvisd and each engine. It would free the memory as soon as
jarvisd dies rather than at its next start; worth adding if jarvisd stays down after crashes in
practice (launchd's KeepAlive normally restarts it at once).
