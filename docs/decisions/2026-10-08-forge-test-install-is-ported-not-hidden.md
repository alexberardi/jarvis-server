# Forge test install is ported, not hidden

- **Date:** 2026-10-08
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Forge test install is ported, not hidden** (user: port the Forge test install instead of hiding the mobile screen; reverses D5's drop). The four legacy `test-install` routes, `cc_test_install_requests` (cc migration 00160) and the `jarvis/nodes/{id}/test-install` `{request_id}` nudge, mirroring `test_install.py`; it rides the package request state machine, so D39 deadlines, D8 sticky status, D4 node binding and a household check on the poll apply. Mobile/node unchanged; EXTERNAL-CHANGES "hide TestInstallScreen" withdrawn.
