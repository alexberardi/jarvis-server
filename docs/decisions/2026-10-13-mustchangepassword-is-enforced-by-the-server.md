# must_change_password is enforced by the server

- **Date:** 2026-10-13
- **Moved from** the STATUS.md decision log on 2026-10-14.

**`must_change_password` is enforced by the server** (G8): a must-change account's tokens work only for `GET /auth/me` and `POST /auth/change-password`; every other user route in every module is **403** `{"detail", "code": "password_change_required"}`. 403 rather than 401 so clients don't refresh-and-retry or sign the user out. Checked per request, so pre-reset tokens are cut off too.
