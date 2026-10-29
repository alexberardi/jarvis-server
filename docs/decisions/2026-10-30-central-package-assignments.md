# Pantry packages are managed centrally and can target chosen nodes

- **Date:** 2026-10-30 (user: "pantry items should be able to be centrally managed but also
  pushed to selective nodes")

A household admin assigns a Pantry package to the household's nodes: to all of them, or to a
list. The server makes the install requests; nobody installs node by node. This is jarvisd-only
(legacy Python had one install request per node, from the app).

- **The server brokers, the node installs.** An assignment creates ordinary install requests
  (`cc_package_install_requests`), so the node still verifies and installs what the server
  returns, the household's Pantry switch (`pantry.enabled`) still gates it, and a computer still
  gets Pantry's pinned repository and version. Nothing new runs on a node.
- **Repository is the Pantry's.** `PUT` refuses a repository other than the one the household's
  Pantry lists for the package, so an admin's one request can't point every node at an arbitrary
  repository.
- **Desired state, retried gently.** `all_nodes` covers every active node of the household,
  including ones paired later. A node that is offline is skipped without using an attempt. A
  failed or never-collected request is retried at most 3 times, an hour apart (the hourly pass
  that also sweeps old request rows), until the assignment changes. A new repository or version
  starts every node again.
- **Admins only.** Changing an assignment needs the household admin role (or the admin key);
  any member reads it. Deleting an assignment leaves the package on the nodes unless
  `?uninstall=true`, which asks each node that finished installing it to uninstall it.
- **Not covered.** Package secrets (a Keep master token, a Tasks refresh token) stay per node,
  set in the app or admin; the server never holds them in plaintext. An assigned package that
  needs secrets installs but reports "not set up" until they are given. A package uninstalled by
  hand later still shows as installed in its assignment: install, uninstall and revert share one
  request table with no action column, so the assignment follows its own latest request.
- **Not built.** No admin page or app screen yet; the API is the first step. A node coming
  online is picked up by the hourly pass, not at its heartbeat.

See `docs/cc/12-packages-and-command-data.md` §2.3 and §4.
