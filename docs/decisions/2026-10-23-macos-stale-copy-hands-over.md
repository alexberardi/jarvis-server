# macOS: a stale copy of the agent app hands over to the installed one

- **Date:** 2026-10-23 (agent, from item #483; the user confirms the behaviour on the MBP)

On macOS the login item (SMAppService) is registered by whichever bundle ran first, and
`launchctl kickstart` of the label starts that bundle. So after rc18.3 was installed in
/Applications, the old rc18 app in ~/Downloads kept launching: at login, and again when setup
kickstarted the label, which made setup fail against a reachable server (MBP, 2026-10-22).

The decision (`install.DecideHandover`, unit-tested): a copy running from outside /Applications
while an equal-or-newer copy is installed there hands the session over to it — `start` spawns
the installed copy's `start`, `run` spawns its `run` directly (never through launchd, whose
label may still point at the stale copy) and exits cleanly, which launchd doesn't restart. The
installed copy (or a strictly newer copy outside /Applications), started by anything other than
launchd, re-points the login item at itself: unregister then register (`Options.Adopt`), because
SMAppService's register returns early on "already enabled". A dev build, a translocated run and
a copy whose counterpart's version can't be read (`jarvis-agent version`) never hand over or
adopt. Versions compare with internal/update's semver (rc18 < rc18.3); the bundle's
CFBundleShortVersionString is useless here, as the build strips the rc suffix.

Why spawn `run` directly instead of kickstarting: if adopting ever fails to re-point BTM's
record, a kickstart would start the stale copy again and ping-pong forever; a directly spawned
`run` ends the chain with the right copy running.

Two copies are told apart by file identity (`os.SameFile`), not path: the running copy's path
has its symlinks resolved, while the install dirs come from `$HOME`, which may go through one
(`/var` → `/private/var`). Compared as strings, the agent-upgrade E2E's app under a temp home
took itself for another copy at the same version and handed over to itself forever. And a copy
started by a handover (`JARVIS_AGENT_HANDED_OVER`) never hands over again, so no such mistake
can loop — but it still adopts the login item: the installed copy a stale one spawns must
re-point the login item at itself, or the stale bundle would own it forever.
