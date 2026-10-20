# A signed-out computer's agent waits for the new sign-in instead of exiting

- **Date:** 2026-10-19 (agent, item #297)

When a computer's `jarvis-agent run` is signed out, it deletes `node.json`, opens the setup page
as before, and then **stays running**. It does what the login item does on a computer that isn't
set up yet: it shows "Not set up yet" in the status icon and keeps answering the control socket
(quit, show-running, ping). Once the page has saved the new credentials, it runs again in place:
the same process image (exec) on Unix, a fresh detached `run` on Windows. It does not open the
setup page a second time.

Why: the run used to exit with "not set up". A run in a terminal (the dev scripts, `./jarvis-agent
run`) or under a job that doesn't restart it was then gone for good, and the sign-in on the page
had nothing to start. Waiting in place works the same under every supervisor: a KeepAlive or
systemd job sees a process that's still running, as it does at a login before setup.

A Pi still exits with "not set up" (78). Removing a Pi sends it the factory reset, so it has no
setup page to wait on.

This changes the "exits with not set up" part of [2026-10-14-device-sign-out](2026-10-14-device-sign-out.md)
for computers ([agent/DESIGN.md §13](../agent/DESIGN.md), "Signing out").
