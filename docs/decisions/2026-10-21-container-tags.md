# The jarvisd image: :dev from main, :<tag> and :latest from every tag, both architectures

- **Date:** 2026-10-21 (user, plan P5)

**Tags.** Every push to main publishes `ghcr.io/alexberardi/jarvisd:dev`. Every `v*` tag
publishes `:<tag>` and `:latest`, prereleases included, so the Unraid template's `:latest`
exists from the next release on (every tag so far is an rc). If stable releases make that
wrong, because an rc would move `:latest` back to a prerelease, the fix is one line in
`release.yml`: skip `latest` for `*-*`.

**Architectures.** The images are built natively per architecture, linux/amd64 on
`ubuntu-latest` and linux/arm64 on `ubuntu-24.04-arm` (no QEMU), each checked on its own
runner, and joined into the multi-arch tags with `docker buildx imagetools create`. That
leaves the per-architecture tags (`:<tag>-amd64`, `:<tag>-arm64`, `:dev-amd64`, `:dev-arm64`)
visible in the package; they are what the joined tags point at.

**Visibility.** A GHCR package is private when first pushed, and its visibility can only be
changed by hand in the package settings. That is a one-time switch for the user; until it is
made, `release.yml`'s `publish` job warns (`scripts/ghcr-public-check.sh`) in the run and its
summary instead of failing.

This supersedes the `:latest`-only-for-stable rule `release.yml` had since #386.

See [the container install guide](../install/container.md).
