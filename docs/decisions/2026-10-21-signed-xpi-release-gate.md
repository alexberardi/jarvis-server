# The signed Firefox extension is checked at release, and the agent offers the update

- **Date:** 2026-10-21 (build agent, #449; held for the user's review)

The agent embeds the Mozilla-signed `.xpi` (`internal/agent/browser/signed/jarvis-browser.xpi`)
and Firefox runs that, not the extension source. Only the user can re-sign it (AMO credentials).
It stayed at 0.1.0 through eight source bumps, so Firefox users had none of 0.2.0–0.3.4's
commands.

**Where the check fails.** `TestSignedXPICurrent` (build tag `release_xpi`) fails when the
embedded package's version is not the source's. `release.yml` runs it before building, like
`release_ui`, so a tag or release dry run with a stale package does not build. It is not a plain
`go test`: then every PR that bumps the extension (most of them from the loop, which has no AMO
credentials) would stay red until the user re-signed, holding unrelated work behind a manual
step. The always-on `TestSignedXPIVersion` only checks that the package is readable and never
newer than the source. The release is what users install, so the release is where it has to
hold. To make every PR carry its re-signed `.xpi` instead, move the test out of the build tag.

**Upgrading installed copies.** An unlisted add-on doesn't update itself (no `update_url`;
hosting one is out of scope). When Firefox connects with an extension older than the embedded
package, the agent writes the package and opens it in Firefox once per version
(`<DataDir>/browser/firefox-offered-version`), which shows Firefox's update prompt. Opening it
on every connect would bring the prompt back every time the user dismissed it. The unpacked
Chromium folder is rewritten when the agent starts, and the browser loads it on its next start.
The extension itself is unchanged (no `runtime.reload` command), so the source doesn't need a
version bump and re-review for this. Chrome Web Store and listed AMO installs update through
their store and are left alone.

Steps for the user (signing, the stores): [../agent/store-listing.md](../agent/store-listing.md).
