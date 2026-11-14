# A dev instance of the agent is its own device

- **Date:** 2026-11-13 (user asked for several agents at once, one per worktree)

`scripts/agent-dev-run.sh --instance NAME` runs a second desktop agent beside the others.
With `JARVIS_AGENT_INSTANCE` set, the agent mixes the name into its device id
(`internal/agent/deviceid`), so enrolling an instance does not retire the main agent's node or
another instance's, and re-enrolling the same instance still replaces only its own node.

This narrows [re-enrolling replaces the node](2026-10-15-reenroll-replaces-node.md): "one
computer and OS user" becomes "one computer, OS user and instance". Installed agents never set
the variable, so their ids are unchanged. The server is unchanged: the scope that keeps a
forged device id harmless (same member, same household) still applies.

The wake word stays with one agent at a time by convention, not enforcement: instances start
with listening off and `--listen NAME` turns the others off first. Linux only for now; the
macOS dev agent is a single launchd job.
