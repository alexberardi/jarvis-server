# Voice flows: multi-step work on the computer, by voice

Status: **spec in progress** (2026-10-25). Questions are asked one at a time; answers move to
Decided with their date. The single-action layers (L1–L3) are
[DESKTOP-COMMANDS.md](DESKTOP-COMMANDS.md); the task runner this builds on is
[COMPUTER-CONTROL.md](COMPUTER-CONTROL.md).

## North star (user, 2026-10-25)

**Deprecate the mouse altogether.** Not only every pointer action (DESKTOP-COMMANDS.md L1–L3),
but multi-step work, said once:

- "Open Word and write my resume. Ask me questions along the way."
- "Write an email to James."
- "Open spreadsheet X in my Documents and run the monthly budget summary workflow on it."

This lifts the earlier "single bounded actions, no planning" rule
([decision](../decisions/2026-10-24-desktop-voice-actions.md)) for requests like these: they run
in the computer-task loop (COMPUTER-CONTROL.md C6), started by voice on the computer you're at.
Single actions keep the direct path (DESKTOP-COMMANDS.md): fast, no planning.

## Decided

- **2026-10-25: a multi-step request you say to your computer starts there at once, attended**
  (Q13). When computer control is on (C11) and the request needs several steps, the task runs
  immediately on the computer you spoke to: no plan card, no "Tap Run" on the phone, because you
  are there and you asked. It runs in the existing computer-task loop with its rules (C2, C8),
  attended (C9: pauses when you touch the mouse or keyboard). Tasks started from elsewhere (the
  phone, a Pi, a schedule), or with nobody at the computer, keep the phone path.

## Where we are (origin/main @ 5ef4d899, survey 2026-10-25)

None of the three examples works end to end by voice yet. What exists: the computer-task loop on
the desktop (macOS, Linux wlroots/X11; `internal/agent/computer`), taught workflows replayed by
voice (`cc/workflows_run.go`: name match, asks for missing parameters by voice), browser errands,
dictation. What breaks:

| # | Gap | Where | Blocks |
|---|---|---|---|
| G1 | **Questions, confirmations and results mid-task go to phone cards.** The loop can `ask` (`computer/loop.go` StateAsk), but it parks on a card (`errands/computer_handler.go` postComputerCard); the desktop node is never told a task waits, and no voice path resumes it. | cc errands, agent activity | A outright; B and C at any confirmation |
| G2 | **Routing.** `cc/computer_control.go` needsComputer returns false for "write"/"type" (what follows is text to type), so "write an email to James" never becomes a task. "use the computer to …" (#575) is on an unmerged branch. | cc | A, B |
| G3 | **A phone tap to start**, unless household autonomy is on; a non-autorun task may run on another computer than the one spoken to. Settled by Q13; to build. | cc errands | A, B, C |
| G4 | **No file by name.** `desktop_open` refuses paths; `computer_files` reads two folders and four text types; a workflow's `file` parameter is only text. | agent, cc | C |
| G5 | **No contacts with email, no email tool.** Phone contacts have no email; memory knows James's address only if told. | cc | B |
| G6 | **Long writing.** 1024 output tokens per round, 2000 runes per action; the live typing path flattens newlines; no draft-then-review. | computer loop, dictate | A, B |
| G7 | **Platforms.** No desktop tasks on Windows or GNOME/KDE Wayland. | agent | all, there |
| G8 | **Desktop replay fixes** (#581, each step opens its app) unmerged; file parameters don't resolve. | computer replay | C |

## Open (asked one at a time, in this order)

1. **Q14 — questions mid-task by voice:** how a running task asks you something, and how you answer.
2. **Q15 — routing:** how a request becomes a task rather than single actions.
3. **Q16 — drafting long text:** draft-then-review, and where you see the draft.
4. **Q17 — files by name:** which folders, which kinds, and what "open X" may open.
5. **Q18 — contacts and email:** where "James" comes from, and how mail is sent.
6. **Q19 — wake arbitration:** when another node wins the wake word on a request only this computer can do.
