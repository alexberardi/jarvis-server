# import-legacy reads the legacy Postgres directly, not an export bundle

- **Date:** 2026-10-09
- **Moved from** the STATUS.md decision log on 2026-10-14.

**`import-legacy` reads the legacy Postgres directly, not an export bundle** (user: the end goal is a one-time wizard screen pointed at `~/.jarvis/compose`). Core in `internal/legacyimport` behind a `Source` interface (pgx read-only sessions; JSON fixtures for tests), credentials found by compose discovery (installer `.env` or a source checkout). System-scope legacy settings come over only when someone changed them (legacy seeds every default); llm-proxy settings and `auth.algorithm` never. The wizard screen is not built; recommended shape: the installer hands over a 0600 connection file and leaves only `jarvis-postgres` running (legacy-import.md §8).
