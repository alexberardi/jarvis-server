# Desktop agents get node settings through K2, managed in the mobile app

- **Date:** 2026-10-14 (user, through the coordinator)

Node settings for a desktop agent (command secrets, commands on or off, command data) work as
on a Pi: end-to-end encrypted with the node's K2 key, edited by household admins in the mobile
app, relayed as ciphertext by command-center. There is **no web-app settings view**: the
browser has no K2 and command-center must never see the plaintext. The web app's Add-ons only
says that settings for a computer are managed in the mobile app.

The agent gets K2 the way a headless Pi does: its heartbeat says `needs_k2: true`, an admin taps
"Generate Encryption Key" on that computer in the app, and the key goes through command-center's
in-memory relay to the agent's authenticated pull. No key at pairing or sign-in, so K2 delivery
needs the phone. Sidecar packages read the settings through `JarvisStorage`, as on a Pi.

Design, threat model and what is not built: [agent/DESIGN.md §16](../agent/DESIGN.md).
Supersedes the earlier plan for a web-app node settings page for agents.
