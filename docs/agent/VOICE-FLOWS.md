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

- **2026-10-25: a running task asks you by voice, and you answer by voice** (Q14). On the
  computer you're at, Jarvis speaks the question and shows it on the island, then listens
  without the wake word for the answer: about 20 s, since real answers ("senior engineer, 2019
  to 2023") run longer than a pick from a list. The answer resumes the task. With no answer in
  time the task waits and the question also goes to the phone as a card, so nothing is lost.
  Confirmations ("Send this email to James?") work the same way: a spoken yes or no. This closes
  G1 for the computer you're at.

- **2026-10-25: the model routes, through one task tool** (Q15). On a computer with computer
  control on, the model is offered `computer_task {goal}` for anything that needs several steps
  or content Jarvis has to make up, and needsComputer stops excluding "write"/"type". The line:
  **you give the words** ("type hello world", "tell Slack I'm running late") → dictation and
  single actions, as today; **Jarvis makes the content or plans the steps** ("write my resume",
  "write an email to James", "open spreadsheet X and run the budget workflow") → a task, started
  at once and attended (Q13). A taught workflow's name still goes to the workflow runner. No
  "use the computer to …" prefix is needed (#575's approach, rejected as unnatural). Ships behind
  toolbench scenarios on both sides of the line, including near-misses ("write 'on my way' in
  Slack"). Settles G2.

- **2026-10-25: a long draft lands straight in the app** (Q16). The resume goes into the Word
  document, the email into the email body: you read it where it will live, and the app's undo
  covers a bad draft. No preview step: the island would get too big or show too little.

- **2026-10-25: a draft is written in one step and pasted** (Q17, v1). A separate model call
  writes the whole piece with a large output budget from what the task knows and has asked; it
  goes in through the clipboard (newlines intact, about a second for a page), and the clipboard is
  restored afterwards. Typing key by key is the fallback only where pasting is blocked. Landing at
  once also means nobody tries to steer a half-typed draft mid-fill. Settles G6's output and
  newline limits for drafts.

- **2026-10-25: after a draft lands, Jarvis asks "anything to change?" and listens** (Q18). A
  one-line summary ("Your resume's in: two pages, four roles. Anything to change?"), then about
  20 s without the wake word, as in Q14. A change ("make the summary shorter") continues the same
  task with the draft in context; "looks good" or silence ends it.

- **2026-10-25: a change rewrites and replaces the whole draft** (Q19, v1). Jarvis selects all
  and copies first, so the redraft starts from what is on the page now (your own typed edits
  included), revises it changing only what you named, and pastes it back over. One step that
  works in any app; editing just the named passage in place is left for later.

- **2026-10-25: files are found in your usual folders, and the list is a setting** (Q20). By
  default Jarvis searches Documents, Desktop and Downloads plus synced folders in your home
  (OneDrive, Dropbox, iCloud Drive); never app data, config or code trees. The folder list is
  configurable in the node's settings, to add or remove folders.

## Where we are (origin/main @ 5ef4d899, survey 2026-10-25)

None of the three examples works end to end by voice yet. What exists: the computer-task loop on
the desktop (macOS, Linux wlroots/X11; `internal/agent/computer`), taught workflows replayed by
voice (`cc/workflows_run.go`: name match, asks for missing parameters by voice), browser errands,
dictation. What breaks:

| # | Gap | Where | Blocks |
|---|---|---|---|
| G1 | **Questions, confirmations and results mid-task go to phone cards.** The loop can `ask` (`computer/loop.go` StateAsk), but it parks on a card (`errands/computer_handler.go` postComputerCard); the desktop node is never told a task waits, and no voice path resumes it. | cc errands, agent activity | A outright; B and C at any confirmation |
| G2 | **Routing.** `cc/computer_control.go` needsComputer returns false for "write"/"type" (what follows is text to type), so "write an email to James" never becomes a task. "use the computer to …" (#575) is on an unmerged branch. Settled by Q15; to build. | cc | A, B |
| G3 | **A phone tap to start**, unless household autonomy is on; a non-autorun task may run on another computer than the one spoken to. Settled by Q13; to build. | cc errands | A, B, C |
| G4 | **No file by name.** `desktop_open` refuses paths; `computer_files` reads two folders and four text types; a workflow's `file` parameter is only text. | agent, cc | C |
| G5 | **No contacts with email, no email tool.** Phone contacts have no email; memory knows James's address only if told. | cc | B |
| G6 | **Long writing.** 1024 output tokens per round, 2000 runes per action; the live typing path flattens newlines; no draft-then-review. | computer loop, dictate | A, B |
| G7 | **Platforms.** No desktop tasks on Windows or GNOME/KDE Wayland. | agent | all, there |
| G8 | **Desktop replay fixes** (#581, each step opens its app) unmerged; file parameters don't resolve. | computer replay | C |

## Open (asked one at a time, in this order)

1. **Q21 — file kinds:** which kinds of files "open X" may find and open.
2. **Q22 — contacts and email:** where "James" comes from, and how mail is sent.
3. **Q23 — wake arbitration:** when another node wins the wake word on a request only this computer can do.
