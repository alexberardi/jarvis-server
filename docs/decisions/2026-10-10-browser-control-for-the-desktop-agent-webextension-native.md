# Browser control for the desktop agent = WebExtension + native messaging into the user's own browser

- **Date:** 2026-10-10
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Browser control for the desktop agent = WebExtension + native messaging into the user's own browser** ([agent/DESIGN.md §10](../agent/DESIGN.md)), not CDP or a Jarvis-owned browser; basic set now (tabs, page text, media, scroll/find), click/type/fill later behind CC validation. One MV3 codebase for Firefox (Mozilla-signed *unlisted* .xpi embedded in the agent) and Chrome/Edge/Brave/Arc/Vivaldi (load unpacked until a Web Store listing). The agent binary is also the native host (argv-detected); host ⇄ agent over a user-only Unix socket with a peer-uid check. `browser_*` tools are offered only while a browser is connected and live in the shared `node.Multi`, so chat and voice get them with no voice changes. Page text is returned between untrusted-content markers; form fields are never read.
