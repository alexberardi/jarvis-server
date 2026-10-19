# Browser recording reaches the extension by long-polling each frame, not by messages

- **Date:** 2026-10-19 (build agent, piece 28 of plan P3; the PR holds for the user's review)

The recorder (`record_start`/`record_stop` in `internal/agent/browser/extension/background.js`)
has to get the user's actions out of the page. The usual way, a content script calling
`runtime.sendMessage` with a `runtime.onMessage` (or `onConnect`) listener in the background,
would be the extension's first listener that pages' scripts could reach. `TestExtensionSurface`
bans those listeners, so the extension's only input stays the native port
([DESIGN §14](../agent/DESIGN.md)).

So the page side (`recInstall`, run by `scripting.executeScript` in every frame) queues events in
the extension's isolated world (`globalThis.__jarvisRec`). The background long-polls each frame
with another `executeScript` (`recPoll`), which returns as soon as the frame has an event, or
after 20 s with none. Each frame has one poll at a time. A new document ends that frame's poll,
and the tab's next load (`tabs.onUpdated` complete) installs the recorder again.

The cost is that an event queued in the moment between one poll returning and the next one
starting is lost if the page unloads right then. Every other event, including the click that
starts a navigation, arrives: the poll returns on the click before the document goes. Pending
typing is flushed on `pagehide`, on a later action and on stop.

Only `recInstall` and `snapCollect` may read `.value`. `TestExtensionSurface` checks that
`recInstall` keeps its secret-field guards: password, hidden, `cc-*`, one-time-code,
current-password, new-password, and labels naming a card, CVV, SSN or PIN. The agent masks the
same set again before the sink (`record.go`).
