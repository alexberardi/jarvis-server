# Autonomy phases merge before v0.1.0

- **Date:** 2026-10-14 (user)

Autonomy phases merge into `main` as they are ready, migrations included, even before v0.1.0 is tagged. Everything new is off by default (`errands.autonomous_enabled`, `watches.enabled`, `goals.enabled`). A lane takes the next free cc migration number when it rebases onto `main`. Plan: [autonomy/PLAN.md](../autonomy/PLAN.md) "Building it alongside the release".
