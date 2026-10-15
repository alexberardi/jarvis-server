# Not set up: install opens the setup page; login waits in the status icon

- **Date:** 2026-10-14 (agent, after a user report)

`jarvis-agent install` on a computer that isn't set up did nothing visible: its systemd unit
runs `run`, which exits 78 without credentials, so no page and no icon appeared. The user had
to run setup first, then install.

- **`install`** on a computer that isn't set up opens the setup page (`start`'s first-run path)
  instead of starting an agent that would only exit. Once the computer is added, the page starts
  the agent under its login item.
- **At login**, a computer that isn't set up gets **no browser window**: opening one every login
  would be wrong. The login item's `run` shows the status icon ("Not set up yet", with "Set up /
  Settings…" and Quit) and waits. Where the desktop shows no status icon, it opens the setup page
  **once**, at the first such login, then never again by itself. When setup saves the
  credentials, the waiting process becomes the agent in place, so its supervisor keeps it.
- `run` typed in a terminal keeps saying "not enrolled" and exiting 78.

Specified in [agent/DESIGN.md §13](../agent/DESIGN.md) ("Not set up: install and login").
Changes "never the login autostart" in the setup page's "Who opens it" there, and the
`start --login` behaviour (it returned without doing anything; it now starts the login item's
`run` where systemd doesn't).
