# Queue jobs survive restarts: refund on shutdown, requeue orphans at boot

- **Date:** 2026-10-17 (build agent, #104; recommendation from triage)

A graceful shutdown (admin restart, upgrade) no longer leaves an in-flight job `running` for
the lease reaper to fail. When the handler returns an error after the queue's context is
cancelled, the job goes back to `queued` with the attempt it claimed refunded (fenced on that
attempt number, so it composes with #105). A handler that returns success as shutdown begins
is recorded as `done`, not re-run.

At boot, before any worker starts, `Queue.Start` requeues every `running` row: jarvisd is one
process, so they were all orphaned by a crash, and waiting for their lease (30 min for LLM
chat, 1 h for engine binaries) only delays them. The crashed run **counts as an attempt**
(attempts unchanged). A job on its last attempt still gets one more run, so a crash doesn't
fail a `MaxAttempts: 1` job; a row already past its last attempt (orphaned again on that extra
run) is failed with `interrupted by restart`, so a job that crashes the process can't loop on
every boot.

The lease reaper stays for jobs that overrun or hang while the process lives.
