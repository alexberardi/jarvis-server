# Custom Rules govern background work only

- **Date:** 2026-10-13 (user)

Allow/ask/block rules apply to errands, signal automations, goals and watches: work that runs while nobody is speaking. Live voice turns keep today's behaviour, because a person is right there. So the rules are checked in the errand planner and runner, in signal reactions, and in goal and watch actions, not in the live tool loop (`engine.go`). Plan: [autonomy/PLAN.md](../autonomy/PLAN.md) §1.
