# Computer tasks see an app's full accessibility tree

- **Date:** 2026-10-29 (user)

A computer task's desktop snapshot keeps the whole accessibility tree of the app window,
closed menus included (`atspi.Snapshot`), not only what is on screen. In real use the menus are
usually closed when the model starts, and giving it everything in one tree, hidden items
included, is easier for an autonomous task than opening and searching each menu.

Voice clicks are the other way round: they read only what is on screen
(`atspi.SnapshotShowing`), because a person names what they can see, and LibreOffice Writer's
closed menus otherwise filled the voice snapshot before its toolbars (2026-10-29 live check,
[ELEMENT-LAYER-WORK.md](../agent/ELEMENT-LAYER-WORK.md) C12).

Builds on [one element layer](2026-10-28-one-element-layer.md).
