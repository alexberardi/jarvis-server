# Forge test installs from the web admin get their own page

- **Date:** 2026-11-09 (user)

The web admin can run a Forge test install, which until now only the mobile app could do. Each node
card on the Nodes page has a **Test a Forge command** button that opens its own page,
`/admin/nodes/:id/test`, instead of a dialog or a card that grows in place. The user chose the page
because it has room for everything a test needs at once: the share code, the install, a chat, the
runs and Send to Forge. The request id is kept in the URL, so a reload returns to the same test.

The page has a chat pinned to that node, so the developer can **type** the command instead of
saying it. Typed turns count as runs: cc records any conversation on the test node during the
test window ([Send to Forge](2026-11-09-send-to-forge.md)). The chat starts after the install
finishes, so its tool list includes the draft's commands. It is not saved, so it never replaces
the Chat page's conversation. A reply shows only in the browser; the node just runs the command.

The share code is pasted from the Forge page. A "Test on my Jarvis" link from the Forge page into
the admin was left for later: it is separate work in pantry-web, and the link needs the admin's
address.

The mobile app has the same flow: a **Test a Forge command** button on the node's detail screen
(above Danger Zone) opens a screen with the share code, the install, then a chat pinned to the
node. The run count and Send to Forge sit behind one header button that opens a sheet. The user
preferred that to keeping the runs on screen, which would take room from the chat on a phone.
