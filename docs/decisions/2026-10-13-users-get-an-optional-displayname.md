# Users get an optional display_name

- **Date:** 2026-10-13
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Users get an optional `display_name`** (for the mobile app's member lists). Auth migration 00002 adds `auth_users.display_name` (NULL = unset). Every user/member payload (`/auth/me`, login/refresh/register `user`, household member list/add/update, admin and superuser user views) carries `display_name` (null when unset). Set via new `PATCH /auth/me` `{"display_name"}` and, for household admins, `PATCH /households/{id}/members/{user_id}`, whose `role` is now optional when `display_name` is given. Trimmed, ≤ 64 characters, no control characters; "" or null clears. Contract shapes accept it as optional (legacy omits it). (`/internal/users/batch` and cc's `UserNames` were still usernames; see the next row.)
