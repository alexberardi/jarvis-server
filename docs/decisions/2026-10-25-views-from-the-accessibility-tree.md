# Views inside apps come from the accessibility tree

Date: 2026-10-25. Decided by the user (VOICE-FLOWS.md Q26).

**Context.** Switching browser tabs by voice failed in real use: speech-to-text heard "Claude pad"
and "Cloud Fair" for the Claude tab, the tab search matched nothing, and the model improvised
(it once opened an add-on page). Excel worksheets, editor tabs and terminal tabs would fail the
same way, and none of them can be switched by voice today.

**Decision.** Named views inside an app are one concept with two generic tools,
`desktop_views` and `desktop_view`. The names come from the OS accessibility tree (AT-SPI,
macOS AX, Windows UI Automation), where tabs are standard elements and switching is their press
action. Our code matches the spoken name (spelling and sound), switches on one match and offers
a spoken choice otherwise; the visible names are sent as STT hints. Per-app sources with more
detail stay where they exist (the browser extension for tabs).

**Rejected.** Per-app integrations (Excel COM, LibreOffice UNO, editor extensions) as the main
path: each app is its own project and the rest get nothing. Screenshots and a vision model: a
model call per switch, slow and least reliable; it stays the computer-task fallback.

**Consequences.** One backend per OS. Apps that expose little (Electron without
`--force-renderer-accessibility`, custom-drawn UIs) are not covered.
