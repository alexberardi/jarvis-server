# Refresh tokens last 30 days

- **Date:** 2026-10-15 (user, Jarvis Loop item #1 "Mobile: stay signed in for 30 days")

`auth.token.refresh_expire_days` defaults to 30 instead of legacy jarvis-auth's 14. Every refresh
rotates the token with a fresh lifetime, so a phone stays signed in as long as it is opened at
least once every 30 days. An install that set the value explicitly keeps it. A legacy import
skips the row when it was never changed, so those installs get the new default. Reason: users
reported being signed out of the mobile app too often.

Every 401 from `POST /auth/refresh` now logs `refresh_rejected` with a `reason`, so the server
log shows why a session ended. This replaces the old `refresh_token_reuse_detected` line.
Spec: [schema/auth.md](../schema/auth.md).
