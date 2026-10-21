# The release notifies the installer site with a fine-grained PAT, and never fails the release for it

- **Date:** 2026-10-21 (user, item #375)

After `release.yml`'s `publish` job has created the release and pushed the container image, it
sends a `repository_dispatch` (event type `jarvisd-release`, payload `{tag}`) to
`alexberardi/jarvis-installer`, whose `deploy.yml` rebuilds the landing page with the new tag
(the site resolves the newest release itself, so the tag is informational; it is sent anyway).

**The token is a repo secret, not the workflow's token.** `github.token` can't dispatch to
another repository, so the step uses `INSTALLER_DISPATCH_TOKEN`: a fine-grained PAT for
`alexberardi/jarvis-installer` only, Contents: read and write, stored as a repo secret on
jarvis-server. The user creates it; until then the step warns and exits 0 (secrets can't be
tested in a step `if:`, so the script checks the env var).

**A dispatch error never fails the release.** By the time the step runs the release and the
image are already out, so a failed dispatch only prints a `::warning::`; the site picks the tag
up on its daily rebuild (06:17 UTC) as a backstop.
