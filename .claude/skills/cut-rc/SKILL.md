---
name: cut-rc
description: Cut and publish a jarvisd pre-release (v0.1.0-rcN). Covers the go/no-go checks, what changed and which migrations, the signed annotated tag, watching release.yml, verifying the 8 assets and the minisign signature, and the STATUS.md entry. Use it when the user asks to cut, tag or publish an rc or a release of jarvisd. It does not cover the node, mobile, browser extension or engine (whisper-builds) releases.
---

# Cutting v0.1.0-rcN

A pushed `v*` tag is the whole release: `.github/workflows/release.yml` builds 4 targets, signs and
notarizes the darwin binary, packages, minisigns SHA256SUMS, starts the binary on every OS, then
publishes. Any tag with a `-` becomes a GitHub **prerelease**.

## 0. Preconditions (stop if one fails)

- **The user said to cut it.** Every rc entry in STATUS records "user OK to cut". Never tag on your own.
- `main` CI is green: `gh run list --workflow ci.yml --branch main --limit 3`.
- You are tagging `origin/main`: `git fetch origin && git log -1 --oneline origin/main`.

## 1. What's in it

```bash
PREV=$(git tag -l 'v0.1.0-rc*' | sort -V | tail -1)      # sort -V: rc10 after rc9
git log --oneline "$PREV"..origin/main
git diff --stat "$PREV" origin/main -- ':(glob)internal/**/migrations/**'
```

- Use the `:(glob)` form for the migration check. The plain pathspec
  `'internal/modules/*/migrations'` silently prints **nothing**. The glob also covers
  `internal/platform/{queue,scheduler}/migrations`.
- Report any migration, because an older binary refuses a DB a newer one migrated
  (`serve --allow-downgrade`, docs/install/00-installers.md §4.3).
- A destructive migration (drop/rename) must be called out to the user before tagging.

## 2. Tag and push

Tags are annotated and SSH-signed (`tag.gpgsign=true` in this repo's git config). The message is
the version, a blank line, then one line per change with its PR number. Older tags use a single
summary line ending in "No migrations since rcN.".

```bash
N=15   # next number
git tag -a v0.1.0-rc$N origin/main -F - <<'EOF'
v0.1.0-rc15

- cc: <subject> (#NNN)
- agent: <subject> (#NNN)
EOF
git push origin v0.1.0-rc$N
```

## 3. Watch the workflow

```bash
gh run list --workflow release.yml --limit 1          # find the run id
gh run watch <run-id> --exit-status                   # jobs: build → sign-macos → package → verify → publish
```

`sign-macos` notarizes (several minutes). If it fails, do not re-tag the same name. Fix the cause,
then ask the user whether to delete the tag and release or to cut the next number.

## 4. Verify the release

```bash
gh release view v0.1.0-rc$N --json isPrerelease,assets -q '{pre: .isPrerelease, assets: [.assets[].name]}'
```

Expect `pre: true` and **8 assets**: 4 archives (`jarvisd-<v>-linux-amd64.tar.gz`,
`-linux-arm64.tar.gz`, `-darwin-arm64.tar.gz`, `-windows-amd64.zip`), `SHA256SUMS`,
`SHA256SUMS.minisig`, `install.sh` and `install.ps1`. The two install scripts are stamped with
the tag and are not listed in SHA256SUMS.

```bash
D=$(mktemp -d); gh release download v0.1.0-rc$N --dir "$D" -p SHA256SUMS -p SHA256SUMS.minisig -p '*linux-amd64*'
mise exec minisign@0.12 -- minisign -Vm "$D/SHA256SUMS" -P RWRyW6ICtU+iyX4p4RnS24ju0gRsWpxvv6B8pI9G+ZS01q8t8oupAQ8L
(cd "$D" && sha256sum -c --ignore-missing SHA256SUMS)
```

The key is `update.ProjectPublicKey` (internal/update/key.go). On this box the bare `minisign` shim
has no version set, hence `mise exec`.

## 5. Record it

Bump the "pre-releases through" line under "Current phase" in `docs/STATUS.md`. The rc's
details go in its GitHub release notes (`gh release edit vX --notes-file …`), not in STATUS:

```
v0.1.0-rc14 at 9c3ac2f (user OK to cut): <changes with (#N)>. No migrations since rc12.
Release workflow green, 8 assets, minisign verified. Not yet applied to prod.
```

Older rc entries are in `docs/status/archive-2026-10.md`.

Commit it as `docs(status): v0.1.0-rcN released` (79adeca, f3b3288).

## Gotchas

- **Prod is updated by the user**, through admin → Update (privileged helper). Never ssh to
  `<prod-host>` and upgrade. Write "Not yet applied to prod" until the user says otherwise.
- **`releases/latest` must stay jarvisd's** (2ad2133). Engine releases from `whisper-builds.yml`
  are `--prerelease --latest=false`. Without a full jarvisd release, GitHub falls back to the
  newest *full* release, so never publish an engine as a full release. The stable one-liners
  404 until `v0.1.0` exists. Each rc installs from its own URL.
- **rc ordering** (437358a, #18): `rcN` compares numerically from rc11 on. rc9 and older
  binaries reach rc10+ only with `sudo jarvisd upgrade --version <tag> --allow-older`.
- A manual `workflow_dispatch` run is a dry run (it builds and checks only) unless `publish` is set.
  Without `MINISIGN_SECRET_KEY` it is unsigned, with a warning.
