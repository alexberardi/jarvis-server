# Pantry packages install on desktop agents; no `targets` tag yet

- **Date:** 2026-10-15 (made while fixing the user's failed install; open to the user's review)

The user installed a Pantry package from the app onto their computer and it timed out: the
desktop agent had no installer. It now installs, updates, reverts and uninstalls packages from
the app as a Pi does, into a uv-managed Python under its data directory
([agent/DESIGN.md §6](../agent/DESIGN.md)).

- **A package without a `targets` field installs on a computer** when its `platforms` include the
  computer's OS. [DESIGN §8](../agent/DESIGN.md) recommended defaulting to `["node"]` so existing
  packages never land on a desktop by accident, but no Pantry package declares `targets` yet, the
  Pantry doesn't filter by node kind, and the user picks the computer in the app on purpose. A
  package that does declare `targets` without `desktop` is refused.
- **Pi-only parts are refused or skipped, never half-run.** A package that needs `apt_packages`,
  `apt_sources` or `post_install` is refused (a computer installs no system packages). Agents,
  device protocols and the other non-command components are skipped and listed in the result's
  `skipped_components`; only commands run in the sidecar.
- **No restart.** A Pi restarts its node after every package change; the agent starts a fresh
  sidecar on the new package list instead, and keeps the old one serving if the new list doesn't
  load.
