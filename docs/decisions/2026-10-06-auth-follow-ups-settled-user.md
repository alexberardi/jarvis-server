# Auth follow-ups settled (user)

- **Date:** 2026-10-06
- **Moved from** the STATUS.md decision log on 2026-10-14.

Auth follow-ups settled (user): (1) access tokens stay valid until expiry (30 min) after logout/password change: fine for now. (2) Rotating a revoked app client re-activates it: kept as the one explicit "reissue + reactivate" admin action (there is no separate un-revoke route), documented rather than changed. (3) An admin removing a member from their only household is by design.
