# Several pantries, chosen per action

- **Date:** 2026-11-11 (user)

`cc`'s `pantry.base_url` becomes a **list** of stores instead of one URL, under a new key, `pantry.stores`. The admin and the
mobile app show a picker, and the store the user picks is the one that action talks to: its
catalogue is browsed, and its URL is the `pantry_url` the node is given to download from.

The user has wanted this for a while; this records the shape before anything is built.

## Why a list and a picker, not aggregation

- **The node downloads, not the server.** A package install and a Forge test install hand the
  node a `pantry_url` and the node fetches from it, so every action already pins exactly one
  store. A picker makes the pinned store explicit; aggregating catalogues would still have to
  pin one at install time, with the user no longer sure which.
- **Provenance is the point.** A self-hosted store, a private repo's store and the public
  Pantry carry different trust. Merging them into one list hides which one a package came
  from, and two stores may publish the same package name.
- **The upstream mirror is not the same feature.** `jarvis-pantry`'s `PANTRY_UPSTREAM_URL`
  mirrors another catalogue into your own Pantry. It needs you to run a Pantry, and a mirrored
  package's clone URL still points at its origin, so it does not give a household a choice of
  stores. Both can exist; this decision is about the client's own list.

## What today's behaviour already tells us

`pantry.base_url` defaulted to `http://localhost:7721` on a dev install, which no node but the
server's own machine can reach: the Pi resolved it to itself and a Forge test install failed
with only "Failed to download from Pantry" in the UI (2026-11-11). Whatever the list's entries
look like, each one is a URL **every node** must be able to reach, and the failure should say
which store and which node could not reach it.

## Decided

All ten answered by the user, 2026-11-11, before anything was built; 11–14 settled in review the same day.

1. **An entry is `{id, url, name}`.** The name is what the picker shows. The `id` is a GUID the
   server generates when the entry is added; clients cannot set or edit it (see 11). The current
   single string migrates to one entry with a fresh id, named after its host. There is no
   per-entry enabled flag: a store you do not want is removed, which says the same thing with one
   less state.
2. **First in the list wins.** List order is preference order, so entry one is the default for
   anything that does not pick: `/services`, a node download, a background job. Adding a store
   appends and never silently changes behaviour. The admin can reorder.
3. **The picker's selection is per device**, remembered in the browser and in the app, never
   stored server-side. It is view state: the binding that matters is recorded at action time
   (4, 5), so browsing can never retroactively change where an installed package came from.
4. **A share code is resolved against the picked store only**, and that store's id is saved on
   the test install so Reinstall and Send to Forge (`testinstall_feedback.go`) return to it. A wrong
   code fails at once, naming the store. Codes are never broadcast to stores the user did not
   choose: a 6-character code is the only secret protecting an unpublished draft.
5. **An install records its store's id.** Updates and revert go back to it. When that store is no
   longer listed the package keeps running and the UI says updates are unavailable because its
   store is gone. Packages installed before this change have no origin and fall back to the
   default store until reinstalled.
6. **`pantry.enabled` stays one household switch** over all stores; per-store control is the
   list itself. **The hosted Pantry is a default, never a fixture:** the entry is editable and
   removable, so a household can run only its own store and talk to nothing else.
7. **The install names its store and does not warn.** You added the store deliberately, so a
   caution on your own infrastructure would be backwards. The unverified-draft warning on a
   Forge test install is unchanged.
8. **A name already installed is refused**, naming the store it came from. The node installs
   into `pkgs/<name>` and the sidecar refuses a command another package provides, so two stores'
   same-named packages cannot both be live on a node whatever we chose. Changing a package's
   store is a deliberate uninstall, then install.
9. **`/services` keeps reporting one URL** for `jarvis-pantry`, the default store, so every
   client that reads it today keeps working. An empty list publishes no entry at all.
   `/services` stays the **system** list: it is unauthenticated and a user can belong to several
   households, so it cannot tell whose list to return. A household's own list reaches the app
   through the household settings (`GET /api/v0/mobile/household/{id}/settings`), which already
   carry `pantry.enabled` and check membership: they gain a read-only `pantry.stores` (user,
   2026-11-11, settled while building 1b).
10. **Clients sniff for the list; its absence is today's behaviour.** The `/services` response
    is an envelope (`{"services": [...]}`), so it gains a sibling `pantries` array of
    `{id, url, name}` beside the unchanged `services` array. A client that finds it shows a picker; a
    client that does not carries on with the single `jarvis-pantry` URL. This is the only shape
    that is compatible in both directions at once, which matters because the mobile app ships
    on every merge: an installed app meets a newer jarvisd (no `pantries` key read, works), and
    a newer app meets an older jarvisd (no `pantries` key present, works). Adding a property is
    safe; requiring one is not. Being a wire change, it follows the wire-change skill and gets
    contract cases.
11. **A store is identified by its id, never its URL or name.** Installs and test installs record
    the id server-side, so editing an entry's URL or name moves every install with it, including
    nodes that are offline at the time: they get the new URL with their next payload. Removing an
    entry discards its id; its packages keep running with updates unavailable, and getting
    updates again means uninstall, then install from another entry.
12. **`pantry.enabled` is the only off switch.** Disabling it leaves the list stored untouched.
    An unset setting takes the definition's default, `[hosted]`.
13. **Enabled with an empty list is an invalid config, not a mode.** Every Pantry action fails
    gracefully with an error saying no store is configured. Nothing falls back to the hosted URL.
14. **The server acts only on stores in the household's list.** A client picks a store by id; an
    unknown id is refused. The server never fetches a draft or POSTs feedback to a URL the client
    supplied.

## The mobile app talks to stores directly

`src/api/pantryApi.ts` uses raw axios against `getServiceConfig().pantryUrl`, resolved from
`/services`, not through jarvisd. So:

- The app is a client of every store's URL, exactly like a node. A LAN entry such as
  `http://10.0.0.122:7721` works on the home network and fails off it, where a node on the LAN
  is fine. A store that is unreachable should say so per store, not fail the screen.
- `pantry.enabled` is read from household settings and is unchanged by any of this.

## Rules these imply

- **A picker is shown only where there is something to pick.** With one store configured, no
  picker appears anywhere: the admin's Store page, the Forge test-install screen and the mobile
  app look exactly as they do now. The test-install screen gets a picker too, not just browse.
- **An empty list means no stores, not the hosted one.** `PantryBaseURL` currently returns
  `defaultPantryBaseURL` whenever the setting is empty. That fallback cannot survive: an enabled
  household with an empty list gets an error (13), never the hosted store, or a household that
  removed every store still phones home. Nothing may hardcode the hosted URL as a fallback.
- **Every entry must be reachable from every node.** The node downloads, not the server
  (`pantry_url` in the install payload). `http://localhost:7721` is unreachable from any other
  machine, which is how this came up. A failure should name the store and the node.

## Build order

Four slices. Only the first has a migration and a wire change, so it carries the risk; the rest
are additive on top of it and can land separately.

1. **The setting and discovery** (`internal/modules/cc/packages.go`, `cmd/jarvisd/main.go`).
   Split in two:
   - **1a, the setting.** A new `settings.JSON` key, `pantry.stores`, a list of
     `{id, url, name}`, replaces `pantry.base_url`. The key is new because settings rows keep
     their own `value_type` when updated, so retyping the old key would leave every stored row
     read as a string. cc's init converts the old key's rows at **every scope** with
     `settings.Convert` (the scoped, value-changing sibling of `Rename`), before config syncs
     `/services`: the public URL keeps the default store's fixed id, any other URL gets a fresh
     id and its host as its name, and an unusable URL becomes an empty list (13) instead of
     silently becoming the public Pantry. The `JARVIS_PANTRY_URL` env fallback goes: a JSON
     list cannot fall back to a bare URL, and nothing sets it. `PantryBaseURL` becomes
     `PantryStores` plus the default's URL; an enabled, empty list answers **503
     `pantry_no_store`** on every Pantry route. `/services` reports the first store's URL, or
     no entry. No wire shape changes.
   - **1b, discovery.** `/services` gains the sibling `pantries` array (the system list, read
     per request), and its `jarvis-pantry` row is removed at start when the list is empty. The
     household settings gain a read-only `pantry.stores`; the household PUT still refuses it.
     Contract: `pantries` is `Optional` in `contract/config_test.go`.
2. **Provenance** (`internal/modules/cc/packages.go`, the server's install record). An install
   records its store id; a test install too; updates and revert use it; a package whose store is gone keeps running
   with updates unavailable; a name already installed is refused, naming its store. As built:
   - Install (`POST …/package-install`), Forge test install (`POST …/test-install`) and the Store
     browse (`?pantry_store_id=`) take an optional, jarvisd-only `pantry_store_id`. Without it,
     the default (first) store. An id not in the household's list is **422
     `pantry_store_unknown`** (14).
   - The request rows record the store id (`pantry_store_id`, a nullable column on both request
     tables). Verify hands the node that store's current URL, looked up by id, so an edited URL
     reaches pending requests (11). Send to Forge posts to the test install's store.
   - Request rows are swept after 30 days, so what a node has installed from where lives in
     `cc_node_package_stores` (node, package → store id): written when an install completes,
     deleted when an uninstall completes. A test install records nothing there.
   - An install of a package recorded on the node goes to its store whatever the client sends
     (an update). A pick of another store is **409 `pantry_store_conflict`**, naming the store
     it came from (8). When that store is no longer listed, **409 `pantry_store_removed`**:
     no updates until it is uninstalled (5, 11). A package with no record (installed before
     this) is treated as from the default store.
   - Revert needs nothing: the node keeps the previous version itself and downloads nothing.
   - `GET /api/v0/mobile/nodes/{id}/tools` adds, to each recorded `installed_packages` entry,
     `pantry_store_id` and `pantry_store_name`, or `pantry_store_removed: true` (additive; the
     contract's report_tools case allows them under jarvisd).
   - Pantry errors name the store: the share-code check, the Store browse and the desktop pin.
3. **The admin** (`web/admin/`). The settings editor for the list, a picker on the Store page
   and on `/admin/nodes/:id/test`, each shown only when more than one store is configured, and
   the store's name on the install confirmation. Follows the UI guide: design skills, then the
   Impeccable audit with `scripts/screenshots` before the PR. As built:
   - **Who edits the list: superusers only** (user, 2026-11-12). The Settings page edits the
     system default and each household's own list (`pantry.stores` is `Household` for the
     superuser BFF, listed with the inbound-call settings as admin-only in
     `TestHouseholdSettingsAllowlistDeclared`). The household settings route stays read-only, so
     household admins only pick. The no-store error says "An admin can add one in Settings".
   - **Ids are the server's:** a settings `Normalize` hook gives an entry with no id a fresh UUID
     before validation, so the editor (and any client) sends a new store without one. Existing
     ids pass through, which is how an edited URL or name keeps its installs.
   - The editor (`PantryStoresEditor`) replaces the JSON box for this key: a name and URL per
     store, reorder, remove, add, the first marked Default, URL checks mirroring cc's.
   - The picker (`StorePicker`, `usePantryStore`) reads the household's list from the household
     settings, remembers the pick per browser and household (`jarvis-admin:pantry-store:{hh}`),
     and sends `pantry_store_id` only when there are several stores. The Store page, a package's
     page and the install dialog use it; the dialog says "From {store}". The test page has its own
     labelled picker; the test keeps its store in the URL (`&store=`), so Reinstall goes back.
4. **Mobile** (`jarvis-node-mobile`). Read `pantry.stores` from the household settings, falling
   back to `pantries` from `/services`, then to today's single URL; picker when it is
   present and has more than one entry; selection per device; a store that cannot be reached
   fails on its own rather than failing the screen (a LAN store is unreachable off the home
   network, and the app talks to stores directly).

The seam is small because `PantryBaseURL(ctx, householdID)` is the single accessor, with seven
real call sites: the catalogue and download helper, the two `pantry_url` payloads to nodes, the
draft fetch and the node's `pantry_download_url`, the Send to Forge feedback POST, and the
`/services` entry.
