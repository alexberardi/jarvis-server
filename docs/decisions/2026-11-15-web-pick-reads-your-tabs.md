# The web's "Find ID" reads the owner's open tabs through the agent

- **Date:** 2026-11-15, user ("gives us the best control").

An interactive list's `webview_pick` row action can't embed the store's site in the web app:
sites refuse to be framed, and a page can't read another tab's address. On the web, "Find ID"
opens the site in a new tab, and the page then asks jarvisd which of the user's open tabs
match the action's pattern. jarvisd asks the desktop agent on the user's computer
(`browser/match-tabs`), which lists the tabs through the browser extension and matches them
there. The answer carries only the captured values and those tabs' titles: tab addresses
never leave the computer.

- **Owner only.** `GET /api/v0/mobile/nodes/{node_id}/browser/matches` answers only the member
  who paired the node. A household admin gets 404, as for any node that isn't theirs: open tabs
  are personal.
- **Fallback:** with no agent or extension on this computer, the user pastes the product
  page's address and the page applies the same pattern.
- The extension is unchanged (it already offers `list_tabs`). The broker lets a node publish
  `browser/match-tabs/response/{id}`.

Plan: [webapp 03](../webapp/03-inbox-parity.md) step 3.
