# Pantry packages install from an admin Store page; no server-side assignments

- **Date:** 2026-10-30 (user)

Installing a Pantry package on several nodes is done from a **Store** page in the admin
(`/admin/store`): pick a package, then the households, then their nodes. Each install is the
ordinary per-node request (`POST /api/v0/nodes/{id}/package-install`), so cc's household, role
and Pantry checks apply to every node, and a computer is never sent a package it can't run.

A household keeps a **default bundle** (`pantry.default_bundle`): the packages a new node usually
needs, installed together from the same page. Nothing installs on its own: a new node gets its
packages when an admin installs them, then its secrets through the mobile app's Sync from a node
that has them. The server keeps no desired state and runs no background installs.

This was chosen over server-side package assignments (all nodes, including ones paired later,
retried hourly; contributor PR #541), which needed new tables, a sweep and a repository URL typed
by the caller.

Package secrets stay with the mobile app: they are K2-encrypted end to end and jarvisd never
holds them in plaintext ([2026-10-14-desktop-agent-node-settings-k2](2026-10-14-desktop-agent-node-settings-k2.md)).
Server-managed secrets (contributor PR #542) were declined for that reason.

The Store reads the household's Pantry through cc, so the household's `pantry.base_url` and
`pantry.enabled` apply and the browser needs no Pantry CORS or CSP exception. Its routes are
superuser-tier in the admin gateway; cc still requires membership of the node's household.
Details: [cc/12 §2.1a](../cc/12-packages-and-command-data.md).
