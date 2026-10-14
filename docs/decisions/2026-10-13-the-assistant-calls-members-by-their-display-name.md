# The assistant calls members by their display name

- **Date:** 2026-10-13
- **Moved from** the STATUS.md decision log on 2026-10-14.

**The assistant calls members by their display name.** auth's in-process `UserNames` (cc's speaker name, household member list, phone-call initiator name, command-data `user_ref` `_display`, voice-presence signal summaries) now answers `COALESCE(NULLIF(TRIM(display_name), ''), username)`. `GET /internal/users/batch` keeps `users` = id → username (legacy contract) and adds `display_names` = id → display name or null; its only consumers are legacy Python cc's name lookups, which keep working unchanged. Usernames stay wherever a user is identified (ids, auth, logs).
