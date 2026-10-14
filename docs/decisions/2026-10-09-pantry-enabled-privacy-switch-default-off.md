# pantry.enabled privacy switch, default off

- **Date:** 2026-10-09
- **Moved from** the STATUS.md decision log on 2026-10-14.

**`pantry.enabled` privacy switch, default off** (cc, household-scoped, category `pantry`; mobile household-settings allowlist + admin wizard privacy step). Off → `POST …/package-install` and `POST …/test-install` answer 403 `{"detail","code":"pantry_disabled"}` after auth/household checks (admin key included); uninstall, revert, polls and node callbacks are not gated. Upgraded installs start with it off; deliberately no migration turning it on. Docs: cc/12 §Pantry, cc/13 §3.7, EXTERNAL-CHANGES (mobile).
