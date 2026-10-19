# The Pi's reminders stay in the sidecar; Go owns their schedule and the alerts

- **Date:** 2026-10-19 (loop #175, build agent; not yet reviewed by the user)

The Go Pi node fires reminders and speaks alerts as the Python node does
([agent/go-pi-node.md](../agent/go-pi-node.md) §3.11, §7 4.5). Supersedes the `reminder` part of
[2026-10-18-pi-builtin-commands.md](2026-10-18-pi-builtin-commands.md): the command loads now.

**One owner for reminder state.** N:`services/reminder_service.py` keeps every reminder in
memory and persists one at a time through JarvisStorage (the agent's store). A Go poller that
read and wrote the `set_reminder` records itself would race that cache: the service's next save
would overwrite `announced` or `due_at`, and the command's list would go stale. So the reminder
agent's run (N:`agents/reminder_agent.py`: cleanup, due, mark announced, push) is a sidecar
method, `reminders_due`, inside the same process as the command, and Go only schedules it: 60 s
while a reminder is pending, 300 s otherwise (the agent's own constants), a failed poll retried
at 60 s. Each sidecar restores the reminders when it loads the command, as N:main.py does at
boot. The push goes through the SDK's `cc_post` to `/api/v0/node/push-notification`, the path
the Go host already allows.

**`jarvis_log_client` is a shim in the sidecar**, not a jarvis-node-setup change: with
`--backends` (the Pi), when the package isn't installed, the sidecar registers a module whose
`JarvisLogger` logs to stderr (the agent's log). No new pin, and the command stays the same code
on both nodes. The cleaner fix (reminder_service on the SDK's logger) can come with a later pin.

**The alerts are Go's.** A fired reminder is a priority-3 alert for 10 minutes in the Go queue
(`sdkhost.Alerts`, whose watchers get the announceable count after every write, as
N:`on_change`). The announcer says what is at priority 3 and up between turns through the
pipeline's microphone and removes only what it said; an alert not heard (no pipeline listening)
stays and is tried again at the next 10 s sweep. Python's 8 s inline listen after an
announcement is not ported: the Go pipeline has no hook for a listen that isn't a turn, so a
reply ("snooze it") needs the wake word.
