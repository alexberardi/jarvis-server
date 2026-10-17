# Registration without an invite closes once a superuser exists

- **Date:** 2026-10-17 (agent, loop item #99, as asked; held for the user's review)

Legacy `POST /auth/register` let anyone who can reach the auth listener (it binds 0.0.0.0)
create an account, and with no invite code that account becomes admin of its own "My Home"
household. That is the first step of the pairing hijack.

New auth setting `auth.open_registration` (bool, default off, env fallback
`AUTH_OPEN_REGISTRATION`). While no superuser exists, registration is open as before (first
run). Once one exists, a sign-up without an invite code gets 403 `Registration is closed: ask a
household admin for an invite code` (the mobile app shows the `detail` as is) and creates
nothing, unless the setting is on. Sign-ups with a valid invite code, `/auth/setup` with the
setup token, and the existing 400s (bad invite, `X-Household-Id`) are unchanged.
`/auth/setup-status` keeps the legacy `{needs_setup}` shape.

The contract suite's user fixtures register without an invite, so a jarvisd target that has a
superuser needs the setting on ([contract README](../contract/README.md)).
