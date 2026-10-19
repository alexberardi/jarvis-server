# Household feature switches live on the web Account page

- **Date:** 2026-10-18 (loop #225; the user reviews the merge because it adds gateway routes)

The web admin shows the active household's feature switches (for now `watches.enabled` and
`errands.autonomous_enabled`) in a "Household" section of `/account`, not on a new page: every
user already has `/account`, and a household admin is usually not the install's superuser, so
the superuser-only `/admin/settings` can't be where they turn these on. Every member sees the
values; a household admin or owner can flip them; a member sees the switches disabled, with
"Only a household admin can change these."

To get there, the admin gateway passes cc's household settings routes through at `tierUser`
(`GET /api/cc/api/v0/mobile/household/{household_id}/settings` and
`PUT …/settings/{key...}`). This grants nothing a user token couldn't already do on cc's own
port: cc checks the role per household (any member reads, `RoleAdmin` or higher writes). The
web shows only these two keys; the rest of cc's allowlist (persona, Twilio, privacy toggles)
stays in the mobile app until someone asks for it on the web.
