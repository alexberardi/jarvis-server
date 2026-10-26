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

## Principles (from the decisions below)

The three requests are examples, not the scope. Every flow follows these:

1. **You're there, so it just starts** (Q13). Said at the computer, it runs at once, attended.
2. **It talks to you, not your phone** (Q14). Questions, confirmations and results by voice and
   island, with a wake-free answer window; the phone is only the fallback.
3. **The model decides what is a task** (Q15). You give the words → single actions; Jarvis has
   to make the content or plan the steps → a task. No magic phrase.
4. **Your apps are the tools** (Q16, Q22, Q23). Work happens in the apps you already use, on
   screen, where you can see and undo it. Jarvis stores no app credentials and adds no parallel
   API, and leans on what the app already knows (its autocomplete, its recent files).
5. **Write whole, revise whole** (Q17–Q19). Content is written in one step and pasted; changes
   start from what's on the page now and replace it; after each, "anything to change?".
6. **Ask, don't guess** (Q24). Anything ambiguous is a spoken choice, every time.
7. **Nothing irreversible without a spoken yes** (Q14). Sending, overwriting, deleting.
8. **Safe defaults you can widen** (Q20, Q21). Where it looks and what it may open default to the
   ordinary, and the user can extend them in settings.
9. **Views inside apps are switched by name, matched by our code** (Q26). Tabs, worksheets,
   editor and terminal tabs, channels: one way for all of them, never one tool per app.
10. **A funnel, cheapest stage first** (Q27). Each stage settles what it is sure of and passes
    the rest down with what it learned; any stage may ask instead of guessing.

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

- **2026-10-25: "open X" opens files whose extension is on an allow list you can extend**
  (Q21). The default list is documents and media: office files (Word, Excel, PowerPoint,
  LibreOffice), PDF, text, Markdown, CSV, images, audio and video, each opened in its default
  app. Programs, scripts, installers and shortcuts are not on it, so a misheard name can't run
  code; apps still launch by name ("open Firefox"). The list is a node setting the user may
  extend with any extension, programs included: their install, their call.

- **2026-10-25: email goes through your own mail app on screen** (Q22). The task opens what you
  already use (Thunderbird, Outlook, Gmail in the browser), pastes the draft into a new message
  (Q17), revises it there (Q18) and sends only on a spoken yes (Q14). Jarvis stores no mail
  credentials and needs no mail API; you see exactly what goes out. A direct connection (SMTP,
  Gmail/Graph API) is not built.

- **2026-10-25: a recipient comes from the mail app's own autocomplete** (Q23). Jarvis types
  the name into the To field and reads the app's suggestions; with no match it checks memory
  ("James's email is …"), and only then asks you, offering to remember the answer. No contact
  sync or contact store in Jarvis. Settles G5.

- **2026-10-25: anything ambiguous is asked, every time** (Q24). When a reference matches more
  than one thing (two Jameses, two files called "budget", two terminals) Jarvis reads the
  choices and shows them on the island, and you pick by voice. It never guesses, and does not
  learn a default from past picks.

- **2026-10-25: views inside an app come from the accessibility tree** (Q26). Real use: a
  misheard tab name ("Claude pad", "Cloud Fair" for the Claude tab) matched nothing, the model
  saw an empty list and improvised; Excel worksheets, editor and terminal tabs would fail the
  same way. Design:
  - **Two generic tools**, `desktop_views` (the focused or named app's views) and `desktop_view`
    (switch by the name said), not per-app tools.
  - **Our code matches, not the model**: spelling and sound (winmatch, as for windows and apps);
    one match switches, several or none become a spoken choice on the island (Q24).
  - **View names are STT hints** for the turn, as app names are, so the name is heard right.
  - **Source: the accessibility tree** (AT-SPI on Linux, AX on macOS, UI Automation on Windows):
    tabs are standard tab elements in browsers, spreadsheets, editors and terminals, and
    switching is the element's own press action: no pointer, no screenshot. One backend per OS
    covers every well-behaved app. Richer per-app sources stay where they exist (the browser
    extension for tabs: URLs, audio). Apps that expose little (Electron without its flag, custom
    UIs) get nothing from it; vision stays the computer-task fallback.
  The list-tabs "closest first" fallback (#506) is the stopgap until this lands.

- **2026-10-25: requests go through a funnel; its first stage runs on the computer, on any
  CPU** (Q27). Real use: a 9B model got every request with 53 tools (22 server, 31 desktop)
  and picked wrong ones ("switch to Discord" launched the Discord app instead of the tab).
  Stages, each settling what it is sure of and passing the rest down with what it learned:
  1. **On the computer, no model:** a small grammar for clear one-step commands (switch to,
     close, maximize, minimize X; "the second one"; go back) matched against the live names
     (tabs, windows, apps) with winmatch. Sure: act. Several: ask. Else: pass down.
  2. **Narrow before the model** (server): only the tools for the request's kind, plus what's
     on screen ("open tabs: …").
  3. **The model with every tool**, as today.
  4. **A computer task** (Q15) for multi-step work and content Jarvis makes up.
  Each stage logs its decision, so a bad route names its stage. **Stage 1 must run on any PC**
  (no GPU, an older CPU, Windows, macOS or Linux, the server elsewhere on the network): plain
  code and string/sound matching over a few dozen names, no model, no download, microseconds.
  It starts once STT (on the server) returns the words.

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
| G5 | **No contacts with email, no email tool.** Phone contacts have no email; memory knows James's address only if told. Settled by Q22–Q23 (mail app on screen, its autocomplete); to build. | cc | B |
| G6 | **Long writing.** 1024 output tokens per round, 2000 runes per action; the live typing path flattens newlines; no draft-then-review. | computer loop, dictate | A, B |
| G7 | **Platforms.** No desktop tasks on Windows or GNOME/KDE Wayland. | agent | all, there |
| G8 | **Desktop replay fixes** (#581, each step opens its app) unmerged; file parameters don't resolve. | computer replay | C |
| G9 | **Views inside apps.** Only browser tabs are switchable (extension); no worksheets, editor or terminal tabs; a misheard name matches nothing. Settled by Q26; to build. | agent | all |

## Open (asked one at a time, in this order)

1. **Q25 — wake arbitration:** when another node wins the wake word on a request only this computer can do.
2. **Q28 — narrowing (funnel stage 2):** how the server picks the request's kind and its tools.
3. **Q29 — interrupting:** stopping, pausing or taking over a running task by voice.
4. **Q30 — after a task:** follow-ups that refer back to it ("send it to Sarah too").
5. **Q31 — stuck:** what a task does when the screen isn't what it expected.
