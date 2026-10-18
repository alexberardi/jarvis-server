# Refresh-family revocation on reuse is a setting, off until the mobile app stops replaying

- **Date:** 2026-10-17 (user, loop item #118, option B starting with C; held for the user's review)

Replaying a rotated refresh token after the grace window gets a 401, but the rest of the family
stays alive, so a stolen token's branch lives for the 30-day refresh lifetime. Legacy could
revoke the family on reuse behind the env flag `REFRESH_TOKEN_REVOKE_FAMILY_ON_REUSE`; jarvisd
had the code but nothing wired it.

It is now the auth setting `auth.token.revoke_family_on_reuse` (bool, env fallback
`REFRESH_TOKEN_REVOKE_FAMILY_ON_REUSE`), **default off**. When on, a replay of a token rotated
more than `auth.token.refresh_grace_seconds` ago (or of a revoked one) revokes its family. A
**grace miss** never revokes: a replay inside the window whose cached successor is gone because
jarvisd restarted is a benign double submit the server forgot, so it stays a plain 401 (a
deviation from legacy, which revoked on it).

Why off: jarvis-node-mobile's background geofence task rotates the refresh chain while the app
is suspended, and on resume the foreground replays its stale in-memory ancestor long after the
grace window, then retries with the keychain token (`src/api/apiClient.ts`). With revocation
on, the first replay kills the family and every such resume signs the phone out. The fix is in
the app (the foreground re-reads the keychain before refreshing); the default flips to on in a
later change, once that app version is out.

Rejected: (A) server-side branch detection, revoking only when a replayed token's successor
chain has since been refreshed by someone else. It is complex auth code that is easy to get
wrong, while fixing the app removes the stale replay at its source, so revocation then only
fires on real theft.
