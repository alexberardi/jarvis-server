# Device-code pairing is approved by code into the approver's own household, superuser or not

- **Date:** 2026-10-13
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Device-code pairing is approved by code into the approver's own household, superuser or not** ([agent/DESIGN.md §5](../agent/DESIGN.md) item 3). Pairings live in memory (10 minutes; a restart means pairing again). The poll is a POST bound to a secret only the start answer carries. Approval yields a provisioning token minted by the approver, so the registration path is unchanged.
