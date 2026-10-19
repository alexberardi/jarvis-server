# Inside a computer task: screenshots after every action, shortcuts and the clipboard are allowed

- **Date:** 2026-10-19 (user, plan P3 brief/blueprint)

Inside a computer task the agent may take a screenshot after every action, use modifier
shortcuts and use the clipboard, read and write (C7 in
[agent/COMPUTER-CONTROL.md](../agent/COMPUTER-CONTROL.md)). That lifts, for computer tasks only:

- agent invariant 25 (a screenshot exists only for the turn that asked,
  [DESIGN.md §12](../agent/DESIGN.md));
- the dictation clipboard ban (dictation never touches the clipboard);
- the no-shortcuts rule (the keyboard tools take a short fixed key set, no modifier chords).

Dictation and plain voice turns keep all three: the lift applies only while a computer task runs.
The lifted capabilities are part of the computer-task action schema in `internal/computeruse`;
enforcement (deny list, rules, confirmations) is unchanged.
