# Out-of-order migrations are applied

- **Date:** 2026-10-07
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Out-of-order migrations are applied** (goose `WithAllowOutofOrder`): sub-systems land in parallel and a fix may slot below a later release's migration; each module's migrations are independent.
