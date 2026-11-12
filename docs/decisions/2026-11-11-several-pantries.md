# Several pantries, chosen per action

- **Date:** 2026-11-11 (user)

`cc`'s `pantry.base_url` becomes a **list** of stores instead of one URL. The admin and the
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

All nine answered by the user, 2026-11-11, before anything was built.

1. **An entry is `{url, name}`.** The name is what the picker shows. The current single string
   migrates to one entry, named after its host. There is no per-entry enabled flag: a store you
   do not want is removed, which says the same thing with one less state.
2. **First in the list wins.** List order is preference order, so entry one is the default for
   anything that does not pick: `/services`, a node download, a background job. Adding a store
   appends and never silently changes behaviour. The admin can reorder.
3. **The picker's selection is per device**, remembered in the browser and in the app, never
   stored server-side. It is view state: the binding that matters is recorded at action time
   (4, 5), so browsing can never retroactively change where an installed package came from.
4. **A share code is resolved against the picked store only**, and that store is saved on the
   test install so Reinstall and Send to Forge (`testinstall_feedback.go`) return to it. A wrong
   code fails at once, naming the store. Codes are never broadcast to stores the user did not
   choose: a 6-character code is the only secret protecting an unpublished draft.
5. **An install records its store.** Updates and revert go back to it. When that store is no
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
10. **Clients sniff for the list; its absence is today's behaviour.** The `/services` response
    is an envelope (`{"services": {...}}`), so it gains a sibling `pantries` array of
    `{url, name}` beside the unchanged `services` map. A client that finds it shows a picker; a
    client that does not carries on with the single `jarvis-pantry` URL. This is the only shape
    that is compatible in both directions at once, which matters because the mobile app ships
    on every merge: an installed app meets a newer jarvisd (no `pantries` key read, works), and
    a newer app meets an older jarvisd (no `pantries` key present, works). Adding a property is
    safe; requiring one is not. Being a wire change, it follows the wire-change skill and gets
    contract cases.

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
  `defaultPantryBaseURL` whenever the setting is empty. That fallback cannot survive: with a
  list, empty has to mean the household contacts nothing, or a household that removed every
  store still phones home. Nothing may hardcode the hosted URL as a fallback.
- **Every entry must be reachable from every node.** The node downloads, not the server
  (`pantry_url` in the install payload). `http://localhost:7721` is unreachable from any other
  machine, which is how this came up. A failure should name the store and the node.
