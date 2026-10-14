---
name: browser-ext-release
description: Ship a new version of the Jarvis Browser Control extension (internal/agent/browser/extension). Covers the version bump, lint, the store zips for the Chrome Web Store and addons.mozilla.org, Mozilla signing, the embedded .xpi, and the extension ids the agent's native host admits. Use it when the extension's code, manifest or permissions changed, or when asked to submit or update the store listings.
---

# Releasing the browser extension

The full runbook is `docs/agent/store-listing.md`. Read it, and especially "Publishing updates".
The paste-in text and images are in `internal/agent/browser/store/` (`listing.md`, `images/`,
`src/render.mjs`). This skill is the checklist and the traps.

**The store dashboards and AMO credentials are the user's.** You prepare the packages and the
text. The user uploads, submits and signs.

## Checklist

1. **Bump `version`** in `internal/agent/browser/extension/manifest.json`. Both stores refuse a
   version they have seen, on any channel. AMO counts the unlisted signed versions too.
2. **Lint and test:**
   ```bash
   scripts/browser-ext-lint.sh                       # web-ext lint of the Firefox build (CI runs it)
   go test ./internal/agent/browser/...              # TestManifestStoreLimits (Chrome's 132-char
                                                     # description), TestChromeExtensionIDs, …
   ```
3. **Build the uploads** (needs Go, Node and `zip`; byte-reproducible):
   ```bash
   scripts/browser-ext-store-zip.sh chrome    # dist/jarvis-browser-chrome-<version>.zip, manifest "key" dropped
   scripts/browser-ext-store-zip.sh firefox   # dist/jarvis-browser-firefox-<version>.zip, unchanged build
   ```
4. **Permissions or data read changed?** Update `store/listing.md`, the privacy policy's extension
   section (jarvis-docs repo, `docs/security/privacy-policy.md`), both stores' privacy answers,
   and `data_collection_permissions` in the manifest. Do this **before** upload.
5. **Hand off to the user:**
   - Chrome: dashboard → item → Package → Upload new package → Submit.
   - AMO: Developer Hub → Upload New Version → "On this site" (listed) → source: No → reviewer notes.
6. **After AMO approves:** download the AMO-signed `.xpi` and commit it as
   `internal/agent/browser/signed/jarvis-browser.xpi`. The agent embeds it and
   `jarvis-agent browser install` offers it to Firefox.
7. **Release the agent**, so new installs embed the new `.xpi`. Existing hosts re-run
   `jarvis-agent browser install`.

## Ids (internal/agent/browser/protocol.go)

- `ChromeDevExtensionID` (`hdjdhmmlggbjjdokpofkghjnjnmjfhmk`) is the unpacked build, derived from
  the manifest's public `key` (`TestChromeExtensionIDMatchesKey`).
- `ChromeStoreExtensionID` (`abkbjmelelgelkhneobebgoilejlgbob`) is the Web Store item. It is fixed
  for the life of the listing. A new store or account gets a new id. **Add** it to
  `ChromeExtensionIDs`, and never remove one while builds with it exist.
- The native host manifest's `allowed_origins` lists every id. Chrome enforces it, so a
  store-installed extension reaches an agent only after that agent re-ran `browser install`.
  `jarvis-agent browser status` reports a host that "doesn't admit the Jarvis extension".
- Firefox's id is `browser@jarvis.agent`, permanent once signed.

## Traps

- `scripts/browser-ext-sign.sh` signs **unlisted** with `AMO_JWT_ISSUER`/`AMO_JWT_SECRET` from
  the env (never on the command line). It uses up a version number the listed channel then
  can't reuse. Once the listed version exists, ship listed versions only.
- The embedded `signed/jarvis-browser.xpi` lags the source until it is re-signed. As of
  2026-10-13 it is 0.1.0, which answers dictation's `fill`/`click` with `unknown_command`, while
  the source is 0.2.0. Check STATUS "Before the official release" for the current state.
- Don't commit `dist/`. It is gitignored.
- Store review takes days to weeks, so submit early (STATUS, user 2026-10-12).
