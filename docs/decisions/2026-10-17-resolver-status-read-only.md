# The LLM resolver never stops an engine under its lock, and Status only reads

- **Date:** 2026-10-17 (loop item #112, agent)

`engine.Resolver` used to hold its one mutex while an engine stopped (up to StopTimeout + 5 s),
so a model swap stalled every label's lookup, remote ones included, and `Status()` resolved each
label, so an admin status read could start and stop engines.

Now engines are detached from the resolver's maps under the lock and stopped after it is
released. No new engine starts while one is stopping (the old one's GPU memory is freed first,
as before): a Resolve or Reconcile that needs one waits for the stops outside the lock and tries
again. Labels whose engine already runs, and remote/off labels, never wait.

`Status()` is read-only: a local label reports the engine it is bound to, or its last
Resolve/Reconcile outcome (`stopped` if it has none yet). A changed setting shows in Status once
the reconcile loop has applied it (`Notify()` after a settings write), not on the status read.

Retrying a failed engine stays under the lock: a failed supervisor has no process, so `Start`
only launches one and returns.
