# Simple routines implementation

Scope: [feature spec](../docs/cc/simple-routines.md). The user authorized building
after confirming Save activates the selected mode and voice can ask follow-ups.
Use the proposed incomplete result for unattended runs.

Keep one resource with additive `mode` and `utterance` fields and existing Advanced
content. The list advertises `supported_modes`; old servers leave it out. Require
explicit mode awareness when editing an existing Simple routine's content.

Agents advertise `simple_routines` and opt in on routine pulls. Older nodes receive
an inert replacement: no triggers and a nonempty unsupported step. This overwrites
protected/stale definitions without the old empty-step fallback. Run and schedules
check capability before execution.

Voice expands once into the existing conversation flow. Silent runs reuse chat
orchestration without an inferred speaker, with bounded cancellation, tracked tool
outcomes and explicit clarification handling. Simple tool results acknowledge deferred
effects so callback failures reach the result. Never create a second model tool loop.

Tasks and checks: [todo.md](todo.md). Use isolated server/mobile worktrees and verify
each slice. Prove stale defaults, unattended identity and callbacks first. Update the
spec for implementation adjustments. No production writes. Hold coordinated PRs for
user review: merging mobile ships builds, which must not precede server/agent support.
