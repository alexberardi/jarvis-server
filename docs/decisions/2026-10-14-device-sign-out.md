# Signing a device out, and no factory reset for desktop agents

- **Date:** 2026-10-14 (user)

From the web app's Add-ons page, the member who paired a device, or an admin of its household, can
**sign it out**. The server removes the node and revokes its credentials (the node key stops
working for HTTP and MQTT).

- **Desktop agents get no factory reset.** Signing one out never wipes the computer. The agent
  notices on its next connection, or at once through a message, that its credentials are gone. It
  stops serving tools and deletes **only** its credentials (`node.json`). It keeps its settings
  (`agent.json`, `apps.json`), and goes back to the first-run page (`jarvis-agent setup`) so
  someone can sign in again on that desktop account.
- **Pi nodes keep today's behaviour:** removing one sends it the factory reset.
- The agent's status icon gets **"Sign out of Jarvis Automation"**, which does the same from the
  computer's side.
- **Owner:** the node records who paired it: the approver of a pairing code, or the member who
  signed in on the computer (the user who minted the provisioning token it redeemed).

Supersedes the factory-reset TODO in [webapp 01 P4](../webapp/01-member-web-app-plan.md) and
the "Remove … the node stays on the server (sign-out is a later item)" note in
[agent/DESIGN.md §13](../agent/DESIGN.md). It specifies the "sign out of an agent" answer in
[install/01-no-cli-plan.md §6](../install/01-no-cli-plan.md).
