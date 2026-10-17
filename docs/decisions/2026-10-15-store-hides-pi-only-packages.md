# The Store keeps Pi-only packages off computers; jarvisd refuses them too

- **Date:** 2026-10-15 (user: product decision)

A computer (a desktop agent, `node_kind: "desktop"`) is never offered a Pantry package it can't
run, and jarvisd refuses one if it is asked anyway. This builds on
[2026-10-15-pantry-packages-on-desktop-agents](2026-10-15-pantry-packages-on-desktop-agents.md),
which still decides the untagged case.

- **What counts as Pi-only:** a package whose `targets` leave out `desktop`, or that needs
  `apt_packages`. A package with **no `targets`** is still offered to a computer, as the earlier
  decision says.
- **Where the Store is.** The admin/member web app has no Store (the member web app plan dropped
  Pantry browsing, [webapp plan](../webapp/01-member-web-app-plan.md)); packages are browsed and
  installed in the mobile app. Its node picker is per package, so a computer that can't run the
  package is left out of the picker for it, rather than packages being hidden per node.
- **Server-side:** `POST /api/v0/nodes/{id}/package-install` to a desktop node asks the household's
  Pantry for the package's detail (`GET /v1/commands/{name}`: `targets`, `apt_packages`) and
  answers 422 `{"detail": "<name> is made for Pi nodes, not computers", "code":
  "not_for_computers"}` (or "… needs system packages that only a Pi node installs") without
  creating a request. A Pi install never makes this call. If Pantry is down, doesn't know the
  package, or answers garbage, the request goes ahead: the agent checks the manifest it actually
  downloads (`packages.Manifest.CheckDesktop`) with the same rules plus `platforms`,
  `apt_sources` and `post_install`, and refuses there. jarvisd checks the latest version's
  detail, which is what the Store installs.
- **Limitation: an older version is judged by the latest one.** A `package-install` request
  carrying an older version's `git_tag` is still checked against the latest version's
  `targets`/`apt_packages`, so jarvisd can let through an older Pi-only version (the agent's
  `CheckDesktop` still refuses that on the downloaded manifest), or refuse with a 422 an older
  version a computer could run, which nothing downstream overrides. Pantry has
  no side-effect-free per-version manifest: `GET /v1/commands/{name}/versions` carries neither a
  manifest nor the commit SHA that `/download` reports as `git_tag`, and `GET …/download?version=` is keyed
  by version, counts an install and is rate-limited per IP. Fixing this needs a Pantry change
  (for example, a manifest and `git_commit_sha` on each `/versions` entry).
- **Pantry** returns `targets` on the detail since jarvis-pantry #33; a Pantry without it reads as
  "no targets", so only the `apt_packages` rule applies against it.

Supersedes the "Pantry doesn't filter by node kind" line of
[agent/DESIGN.md §8](../agent/DESIGN.md)'s as-built note.

The "if Pantry is down … the request goes ahead" rule is superseded for computers by
[2026-10-17-desktop-installs-pinned-to-pantry](2026-10-17-desktop-installs-pinned-to-pantry.md):
an install on a computer is refused when Pantry can't vouch for it.
