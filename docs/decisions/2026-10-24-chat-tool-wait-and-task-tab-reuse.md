# The chat outlasts a node tool's budget, and a site already open in the task tab is reused

- **Date:** 2026-10-24 (build agent, loop item #587, from the 2026-10-24 QA of #578)

**The chat's wait on a node tool (`chatToolWait`, `internal/modules/cc/mobile_chat.go`) is longer
than the tool's own budget.** It is 15 s; the desktop agent's browser_fill and browser_click
give up at 10 s (`internal/agent/browser/tools.go` `defaultTimeout`) with their own `error` and
`error_code`. Before, both were 10 s, so a slow fill on a just-opened page lost the race and the
model got the server's "didn't respond in time" with no `error_code`, then opened a second tab.
Every synthetic failure the server makes for a node call (offline, no broker, timeout, malformed;
in the chat, errands and signal reactions) now carries an `error_code`, and the chat's timeout
tells the model the command may still have run and not to open another tab.

**`browser_open` of a site's home page while the task tab is already on that site opens
nothing** (host compared without `www.` and scheme; only a URL with no path, query or fragment).
It answers `reused: true` with the task tab's id and a note to search with browser_fill. Another
page of the site, or another site, still opens a tab. The extension has no "load this URL in
this tab" command, and adding one needs a re-signed extension, so the task tab is not navigated.

**A browser_fill with no field, on a page where nothing is focused, retries once as
`field: "search"`** (Google's results page after a search). The retry's result counts only when
it matched; otherwise the extension's first error (which lists the page's fields) is returned.
This is in Go, so the signed extension is unchanged.
