# JWT signing key: one RS256 key, generated once on first run, stored in auth_signing_keys

- **Date:** 2026-10-06
- **Moved from** the STATUS.md decision log on 2026-10-14.

**JWT signing key: one RS256 key, generated once on first run, stored in `auth_signing_keys`** (DB file is 0600 under `~/.jarvis`). No env var to set, no rotation machinery, nothing to keep in sync: jarvisd is the only minter and verifier. Legacy import copies the old `AUTH_PRIVATE_KEY` (and the HS256 `AUTH_SECRET_KEY`, verify-only) so existing sessions survive the cutover. New installs never mint HS256. User: "whatever is simplest".
