# Computer tasks run on the largest loaded vision model, 9B fallback, cloud opt-in

- **Date:** 2026-10-19 (user, plan P3 brief/blueprint)

The default model for a computer task is the largest local vision model the household has loaded
(Qwen3.8-27B on prod), with Qwen3.5-9B as the fallback (Q2 in
[agent/COMPUTER-CONTROL.md](../agent/COMPUTER-CONTROL.md)). A cloud model is an easy **per-task
opt-in** through a remote label, not the default: the loop uses whichever existing label carries
the largest vision model, and no third model slot is added (VRAM).

This is a first decision, not a final one: it is **re-decided from the computer-task suite's
numbers** (piece 16 of plan P3), which scores the required cases per label and may move the
`computer.model` default.

Re-decided on 2026-10-20 from the suite's numbers, with the same outcome (`auto` stays):
[2026-10-20-computer-task-default-model.md](2026-10-20-computer-task-default-model.md).

The cloud part is superseded on 2026-10-22: a third, remote-only `cloud` slot exists (it takes
no VRAM), and a computer task reaches it only through `computer.model = cloud`, not a per-task
opt-in: [2026-10-22-cloud-model-slot.md](2026-10-22-cloud-model-slot.md).
