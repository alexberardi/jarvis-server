# Desktop voice actions confirm by taint, behind control levels, with their own deny list

- **Date:** 2026-10-24 (user, desktop commands merge review Q11 and Q12)

Direct voice actions on a desktop node ([agent/DESKTOP-COMMANDS.md](../agent/DESKTOP-COMMANDS.md))
reconcile [2026-10-24-desktop-voice-actions.md](2026-10-24-desktop-voice-actions.md) (the user's
words authorize the action, terminals included) with `--confirm-dictation=submit` (text the model
read can steer it).

**Confirmation follows taint (Q11).** A turn is tainted once a tool result in it carried app
text (controls, screen reads, browser content, window titles). An untainted turn never asks; a
tainted one asks before the risky set (Enter after typing, risky labels, chords with Enter).
Window and navigation actions never ask. The user picks a **control level**: Full control
(`off`), Guided control (`submit`, the default, with the taint rule), Ask every time (`all`).
Every confirmation is logged with its reason, category, taint source and answer, so the default
can be revisited with real-use counts (user: "i'd need to see how often it happens in real use
to know for sure").

**Voice actions get their own deny list (Q12).** `computeruse.BuiltinDeny` stays as it is for
computer tasks. For direct voice actions: terminals are allowed; lock / log out / force quit and
protected System Settings panes are allowed in an untainted turn and denied in a tainted one;
OS authentication prompts and Jarvis's own windows are always denied, on every control level,
because a click there would let screen text approve Jarvis's own elevation or pending action.
