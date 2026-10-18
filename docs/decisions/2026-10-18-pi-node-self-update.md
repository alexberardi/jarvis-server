# The Pi node updates itself in a directory its user owns

- **Date:** 2026-10-18 (agent, loop #161; the merge is held for the user because it changes
  what the node's user may write)

The Go Pi node (`install-pi.sh`: `/opt/jarvis-agent/jarvis-agent`, run as `pi` by the system unit
`jarvis-agent.service`) uses the desktop agent's self-update unchanged: stage, rename the new
binary in beside the old one (`.prev` kept for a rollback), restart, health gate, rollback. That
needs the node's user to write `/opt/jarvis-agent`, so `install-pi.sh` now `chown`s it to that user.

Chosen over the alternative, a root-owned directory with a narrow sudoers'd helper that does the
swap, because it adds no new root entry point and keeps one update path for every platform (the
desktop's `~/.local/lib/jarvis-agent` is the same idea). The agent runs as that user anyway, so
being able to replace its own binary gives it nothing it didn't have.

What it costs, and how the installer pays for it: the directory is no longer root's, so root must
not trust anything in it. The installer runs the installed agent's `version` and writes the new
copy as the node's user (`runuser`), and the HAT overlay's source, which root compiles into
`/boot`, moved to the root-owned `/usr/local/share/jarvis-agent`.

The restart is the sudoers line `install-pi.sh` already wrote (`sudo -n /usr/bin/systemctl restart
jarvis-agent.service`), without asking the agent to quit first (`Restart=always` would restart it
anyway). The installer (`update --finish`) runs in a `systemd-run --user` unit of the node user's
manager (linger is enabled), with no detached fallback: the system unit's restart stops its whole
cgroup, so a detached installer would die with the agent it restarts.

Plan: [go-pi-node.md §7](../agent/go-pi-node.md) 4.2.
