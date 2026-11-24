# Asked by voice at the computer, work in an app is done in its window

- **Date:** 2026-11-13 (user, live test of [computer use](../agent/SPEC-computer-use.md), checkpoint C)

A request spoken to a computer's own microphone ("write a note in LibreOffice to walk the dogs
and save it") that is work in an app is done **in the app's window, at once, while the user
watches**: a desktop `computer_task`, with no plan card to approve first. The same request typed
in chat, or spoken to another device, keeps today's path: a plan card, and documents written to
the file directly by `write_document`.

What decides it is **how the request was made, never what the job is** (user: "it should always
be consistent with how you trigger it, not based on the context of the job"). Voice at the
computer means the user is there and expects to see it happen; a chat request can queue, because
its plan card is in front of them.

The earlier rule ([ELEMENT-LAYER-WORK.md](../agent/ELEMENT-LAYER-WORK.md) C27: documents are
written with `write_document`, never typed in the app) stays for chat and unattended work. It was
written when the element-first loop took 15+ rounds in Writer and failed at the Save dialog; the
vision-first loop is what makes working in the window reasonable.

- Live only while the turn is recent and that computer has computer control on (attended, C9).
- The planner isn't offered `write_document` for such a request, so it can't pick the file path;
  spreadsheets keep `write_spreadsheet` (its totals are checked formulas).
- It starts without the autonomy setting: the user asked aloud at that computer. The task's own
  actions still go through the household's rules, and risky moments still ask (C2, C8). A plan
  that isn't only computer work still posts its card.
- A task that writes from "this page" is given the page open in that computer's browser.

Not yet shown by the computer suite: it speaks through chat, so it has no live case, and no case
drives Writer's window. Slower than writing the file (a screenshot per round).
