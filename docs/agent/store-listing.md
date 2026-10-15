# Listing Jarvis Browser Control on the Chrome Web Store and AMO

How to submit the browser extension (DESIGN.md §10) to the Chrome Web Store and
addons.mozilla.org (AMO), and how to ship updates afterwards. This is the store-listing step of
§13. Each first submission takes about ten minutes once the accounts exist. Review then takes
from a few days to a couple of weeks.

## The kit

Everything lives in `internal/agent/browser/store/`:

| File | What |
|---|---|
| [`listing.md`](../../internal/agent/browser/store/listing.md) | Text to paste: descriptions, categories, URLs, Chrome privacy answers, AMO reviewer notes |
| `images/` | Store images (table below) |
| `src/render.mjs` | Regenerates the images and the extension's icons (headless Chromium via Playwright; instructions in its header) |

**Privacy policy URL** (both stores): `https://docs.jarvisautomation.dev/security/privacy-policy/`.
It is the project-wide policy (alexberardi/jarvis-docs, `docs/security/privacy-policy.md`); its
"Browser extension and desktop agent" section covers the extension.

**Upload packages:** `scripts/browser-ext-store-zip.sh chrome` or `… firefox` writes
`dist/jarvis-browser-<store>-<version>.zip`, with `manifest.json` at the root. It is built from
`jarvis-agent browser pack`, so it needs Go, Node and `zip`. The output is reproducible byte for byte. The Chrome zip
drops the manifest's `key`: the Web Store won't take one it didn't issue, and assigns the item
its own id. The Firefox zip is the Firefox build unchanged.

| Image | Chrome Web Store | AMO |
|---|---|---|
| `store-icon-128.png` (96 px art, transparent padding) | Store icon | Icon (it scales it down) |
| `amo-icon-64.png` | — | Optional: a 64 px icon drawn for that size |
| `screenshot-1-voice-tabs.png` (1280×800) | Screenshot 1 | Screenshot 1 |
| `screenshot-2-read-page.png` (1280×800) | Screenshot 2 | Screenshot 2 |
| `screenshot-3-private.png` (1280×800) | Screenshot 3 | Screenshot 3 |
| `promo-small-440x280.png` | Small promo tile | — |
| `promo-marquee-1400x560.png` | Marquee promo tile (optional) | — |

The screenshots are illustrative frames: a generic browser window, made-up `.example` sites,
the demo household's "Jordan", and the agent's real island indicator (from its golden-test
frames). They contain no real data.

## Chrome Web Store

**Before you start:** the developer account (Chrome Web Store Developer Dashboard, one-time
US$5 fee). When the dashboard asks for the EU trader declaration, a free hobby/open-source
project declares itself a **non-trader**.

1. Build the package: `scripts/browser-ext-store-zip.sh chrome` →
   `dist/jarvis-browser-chrome-<version>.zip`.
2. Dashboard → **Add new item** → upload the zip.
3. **Store listing** tab:
   - Description: "Full description" from `listing.md`. The short description comes from the
     manifest and can't be edited here.
   - Category: Productivity → Tools. Language: English.
   - Store icon: `store-icon-128.png`. Screenshots: the three 1280×800 frames. Small promo tile:
     `promo-small-440x280.png`. Marquee: `promo-marquee-1400x560.png` (optional).
   - Homepage URL: the repo. Support URL: the issues page. (Both are in the `listing.md` table.)
4. **Privacy** tab, all from `listing.md` → "Chrome: Privacy practices tab":
   - the single-purpose statement;
   - one justification each for `nativeMessaging`, `tabs`, `scripting`, `alarms` and the host
     permission `<all_urls>`;
   - remote code: **No**;
   - data usage: tick **Web history** and **Website content** only, then all three
     certifications;
   - the privacy policy URL above.
5. **Test instructions** tab: paste the reviewer notes from `listing.md` (no test account is
   needed).
6. **Distribution:** Public, all regions. Then **Submit for review**.

## After the first Chrome upload: the store item id

The Web Store gives the item its own 32-letter id, shown in the dashboard and in the listing URL.
Chrome only starts a native host whose manifest lists the extension's origin in
`allowed_origins`, so until the agent knows the store id, a store-installed extension can't reach
the agent.

**The ids** (`internal/agent/browser/protocol.go`):

| Constant | Id | Build |
|---|---|---|
| `ChromeDevExtensionID` | `hdjdhmmlggbjjdokpofkghjnjnmjfhmk` | Unpacked ("Load unpacked"). Derived from the public `key` in the source manifest; `TestChromeExtensionIDMatchesKey` checks it. |
| `ChromeStoreExtensionID` | `abkbjmelelgelkhneobebgoilejlgbob` | The Chrome Web Store listing (assigned 2026-10-12). Edge, Brave, Arc and Vivaldi installing from the Web Store get it too. |

**The store id is fixed for the life of the listing.** Every update uploaded to the same item
keeps it. It changes only for a *new* item: another developer account, a deleted and re-created
listing, or a separate store such as Edge Add-ons. A new id goes into `ChromeExtensionIDs` beside
the others; never remove one while builds with it exist.

**How it is wired:** `jarvis-agent browser install` writes
`<browser config>/NativeMessagingHosts/io.jarvis.agent.browser.json` for every Chromium-family
browser it finds: Chrome, Chromium, Edge, Brave and Vivaldi, Arc on macOS, and HKCU registry keys
on Windows (DESIGN.md §10, install flow). Its `allowed_origins` is `AllowedOrigins`, one
`chrome-extension://<id>/` per entry of `ChromeExtensionIDs`, which are the dev id and the store
id. The host doesn't filter callers itself: Chrome enforces `allowed_origins` before starting
it. The host only recognises any `chrome-extension://` argv as a browser launch.

- **Tests:** `TestChromeExtensionIDs` requires the ids to be 32 letters a–p, distinct, with the
  dev one first and the store one present. `TestRegisterPresentOnly` and `TestWindowsRegistry`
  check that both origins are written.
- **Machines registered before this change:** their host manifests admit only the dev id.
  `jarvis-agent browser status` reports them ("manifest doesn't admit the Jarvis extension";
  `TestInspectFlagsPreStoreHost`), and re-running `jarvis-agent browser install` fixes them. The
  planned installers of §13 will do this on update. Until then, an agent release that includes
  this change, plus a re-run of `browser install`, is what makes the store extension connect.

*Optional, later:* the dashboard shows the item's public key (Package → View public key). Putting
it in place of the manifest's `key` would give the unpacked build the store id too. That changes
the unpacked id on every developer machine, so it is a separate decision. The list works either
way.

## addons.mozilla.org (listed)

The add-on already exists on AMO (`browser@jarvis.agent`, signed **unlisted**). Going public
means submitting a **listed** version of the same add-on.

1. **The version is already above the signed one:** `0.2.1` (0.2.0, 2026-10-13: dictation's `fill`
   and `click`; 0.2.1, 2026-10-14: the toolbar popup with the agent download link, install plan 3.5). AMO refuses any version it has already signed, on either channel, and `0.1.0`
   was signed unlisted. Submit 0.2.1 (not an older checkout) to both stores. The Chrome store will also need a version above the one it has
   for the next Chrome update, so this bump serves both.
2. Build: `scripts/browser-ext-store-zip.sh firefox` → `dist/jarvis-browser-firefox-<version>.zip`.
3. Developer Hub → My Add-ons → Jarvis Browser Control → **Upload New Version** → "On this
   site" (listed) → upload the zip.
4. "Do you need to submit source code?" → **No**. Nothing is minified, bundled or generated;
   `listing.md` → "Source code" explains this.
5. Release notes (for example "First public release.") and **Notes to reviewer**: paste the
   text from `listing.md`.
6. AMO then asks for the listing details (first listed version only):
   - Summary and Description: from `listing.md`.
   - Categories: Tabs; Photos, Music & Videos.
   - Support email: leave empty. Support site: the issues URL. Homepage: the repo.
   - License: AGPL-3.0.
   - Privacy policy: AMO takes the policy as **text**, so paste the "Browser extension and
     desktop agent" section of the policy page (with a line linking the full page).
   - Leave "requires payment, non-free services or additional hardware" unticked.
   - Icon: `store-icon-128.png`. Screenshots: the three 1280×800 frames.
7. Submit. The listing shows the manifest's `data_collection_permissions` (browsing activity,
   website content) on its own.

## The Firefox package inside the agent

`jarvis-agent browser install` opens the Mozilla-signed `.xpi` embedded in the binary
(`internal/agent/browser/signed/jarvis-browser.xpi`). That file is version 0.1.0 (no `fill` or
`click`: an agent with dictation gets `unknown_command` from it for those two) and still has
the **old manifest description and placeholder icons**: the source changed after it was signed.
It changes only when it is signed again, and signing is the user's step, because the AMO
credentials stay with them.

Once the listed version is approved:

- **Installed copies update themselves:** Firefox checks AMO for newer versions of
  `browser@jarvis.agent` and installs the listed one.
- **Embed the listed version:** download the approved, AMO-signed `.xpi` from the Developer
  Hub (the version's page) and commit it as `signed/jarvis-browser.xpi`. Ship listed versions
  only from then on. `scripts/browser-ext-sign.sh` (unlisted) stays for builds that shouldn't be
  public; it would use up a version number that the listed channel then can't reuse.
- **First-run window:** the "Add to Firefox" button of the planned window (§13) can link to the
  AMO listing instead of opening the embedded file.

## Publishing updates

1. Bump `version` in `internal/agent/browser/extension/manifest.json`. Both stores refuse a
   version they've seen.
2. Run `scripts/browser-ext-lint.sh` and the Go tests. `TestManifestStoreLimits` keeps the
   description within Chrome's 132 characters.
3. **Chrome:** `scripts/browser-ext-store-zip.sh chrome` → dashboard → the item → Package →
   **Upload new package** → Submit for review. Update the Privacy tab first if the permissions
   or the data the extension reads changed.
4. **Firefox:** `scripts/browser-ext-store-zip.sh firefox` → Developer Hub → **Upload New
   Version** → "On this site" → source: No → reviewer notes → submit. After approval, commit the
   signed `.xpi` as above.
5. If a permission or the data read changes, update `listing.md`, the privacy policy's extension section (jarvis-docs),
   both stores' privacy answers, and `data_collection_permissions`.
6. Release the agent so new installs embed the new `.xpi`.
