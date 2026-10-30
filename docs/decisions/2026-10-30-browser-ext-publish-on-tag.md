# Publish the browser extension to the stores on every release tag

- **Date:** 2026-10-30 (user: "we should cut actual releases for the browser in the store …
  let's add ci/cd to do this on tag")

The Chrome Web Store listing sat at 0.3.5 while the source reached 0.3.9, because every upload
was a manual dashboard step. Each release tag now runs `browser-ext-publish.yml`: Chrome gets the
version through the Chrome Web Store API v2, and AMO gets it on the listed channel, each only when
the store doesn't have it yet. Both still go through the store's review.

- **Credentials in CI, behind an approval.** The workflow runs in the `browser-stores`
  environment, which holds the secrets and has the user as required reviewer, so a tag can't
  publish to every user's browser without the user's click.
- **Chrome auth is a service account** linked in the Developer Dashboard. The script signs its
  own JWT for the token, not an OAuth refresh token (an OAuth app left in "testing" has its refresh
  tokens expire after 7 days) and not `google-github-actions/auth` (its access-token mode needs the IAM
  Credentials API and a token-creator role on top).
- **Firefox is listed-only through CI and doesn't wait** for review. The signed-`.xpi` release
  gate ([2026-10-21](2026-10-21-signed-xpi-release-gate.md)) still requires the signed package
  before a tag, so today a version reaches the listing only if it was submitted listed before
  the tag. Whether the gate should allow a tag before AMO approves a listed version is open.
