# Settings stay per household; the admin shows every level

- **Date:** 2026-10-09
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Settings stay per household; the admin shows every level** (user report: `pantry.enabled` on in the app, false in the admin). An install has several households and a household admin must not change the others, so household scope stays; the admin Settings page now shows each household's own value under the default and edits/resets it (`GET/PUT/DELETE /api/settings…?household_id=`, docs/admin/00-inventory #2b). Which keys: `settings.Definition.Household`, which cc sets from its mobile allowlist (the one source of truth). The wizard's privacy step still writes the defaults only. No migration.
