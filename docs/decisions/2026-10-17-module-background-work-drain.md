# Modules' fire-and-forget work drains before the DB closes

- **Date:** 2026-10-17 (agent, item #111)

A module whose request handlers start work that outlives the request (cc's trace writes, inbox
pushes, wake verification, in-process signal reactions, the no-queue fallbacks for server
callbacks and chat image jobs, and phone's errand resume) runs it on a module-level task group
instead of a bare `go`. The module implements `module.Waiter`; `Runner.drain` waits on every
Waiter alongside the queue and the scheduler, under the same `DrainTimeout`, so a timeout still
lets shutdown finish (with a warning) rather than hang.

`Start(ctx)` cannot do the draining itself: it returns at once, and the runner only waits for
what it knows about before `main` closes the DB. Hence the runner hook.

The group never races `Add` with `Wait`: tasks started by running tasks while it drains are
still tracked, and once it has drained, `Go` refuses (the caller logs at Debug and fails open,
e.g. a dropped wake verification records no verdict). cc's `Wait` waits for phone's live calls
and plans first, so a call that ends during shutdown can still resume its errand on the group.
