# Packages on a computer: its owner or an admin, from Pantry's repository, failing closed

- **Date:** 2026-10-17 (loop item #101; the merge is held for the user)

A desktop agent runs what's installed on it as the member signed in to that computer (the
sidecar imports and runs the package as that user). D5 lets any household member install any
URL on a node, which on a computer meant any member could run code as another member. For
**desktop nodes** (`node_kind: "desktop"` or install mode `desktop-agent`) only:

- **Who.** Install, uninstall, revert and Forge test install need the member who added the
  computer (`owner_user_id`) or a household admin/owner; anyone else in the household gets 403
  "Only the member who added this computer, or a household admin, can change its packages". A
  computer with no owner is admins' only. The admin key passes, and a superuser on a node
  without a household passes, as before (`requirePkgHousehold`). Polls and the tools view keep
  the member rule. A Pi keeps D5 (any member, docs/cc/12 Q2).
- **What.** An install asks the household's Pantry for `GET /v1/commands/{name}` and installs
  **Pantry's `github_repo_url`**, which is what the request row, the MQTT nudge and the verify
  answer carry. The caller's `github_repo_url` (still required on the wire) must name the same
  repository (case, spaces, a trailing `/` and `.git` aside), else 422 `repo_mismatch`.
- **Which version.** `git_tag` must be a `git_tag` that `GET /v1/commands/{name}/versions` lists, or
  a full 40-hex commit id (what the app sends: Pantry's `/download` reports the commit as
  `git_tag`). With no `git_tag`, Pantry's newest listed tag is used. Anything else is 422
  `version_not_found`.
- **Fails closed.** Pantry unreachable, answering another status than 200/404, or not JSON: 503
  `pantry_unavailable`. Pantry doesn't know the package or lists no repository for it: 422
  `package_not_found`. No request row is created. This supersedes the "if Pantry is down … the
  request goes ahead" sentence of
  [2026-10-15-store-hides-pi-only-packages](2026-10-15-store-hides-pi-only-packages.md); its
  Pi-only refusal (`not_for_computers`) is unchanged and is read from the same detail.

**Limitation: a commit id isn't checked against Pantry.** `/versions` doesn't carry
`git_commit_sha`, and `/download` counts an install and is rate-limited, so jarvisd can't tell
whether a commit is one Pantry published. The agent fetches `{repo}/archive/{ref}.tar.gz`, and
GitHub serves commits from any fork of the repository through that URL, so a caller who may
install (the computer's owner or an admin) could still name a commit that isn't the author's.
Closing this needs a Pantry change (`git_commit_sha` on each `/versions` entry); jarvisd would
then accept only listed commits.
