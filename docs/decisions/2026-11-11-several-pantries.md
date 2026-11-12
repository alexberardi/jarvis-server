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

## Open questions

Queued for the user, one at a time; none is answered yet.

1. An entry's shape — URL alone, or a name and an enabled flag too? And how does the current
   single-string setting migrate (one entry, keeping its value)?
2. Which store is the default for a new install, and for a household that never picks?
3. Is the picked store per household, per user, or just the last one that browser used?
4. A Forge share code only means something on the Pantry that minted it. Does the test-install
   page use the picker, carry the store with the code, or try each store in turn?
5. An installed package whose store later leaves the list: what happens to its updates and to
   revert?
6. Does `pantry.enabled` stay one household bool, or become per store?
7. Does an install from a store other than the public one warn, the way an unverified Forge
   draft does now?
8. Two stores publishing the same package name: refused, disambiguated by store, or first wins?
