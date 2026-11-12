# A test install's Python packages live in its own folder

- **Date:** 2026-11-12 (user)

A Forge test install installs the Python packages its manifest declares (`packages:`, plus any
`requirements.txt`), and refuses the install when they don't install: a draft without what it
imports can't be tested, which defeats the feature. Before this only `requirements.txt` was
read, so a draft that declared `websockets` in its manifest ran without it.

They go in `tests/<name>/.deps` (uv `--target`), never the shared venv. The sidecar puts that
folder on the import path after the venv's packages.

- **Why not the shared venv:** removing a package doesn't remove what it pulled in, and a draft
  asking for a newer version would upgrade it under the installed packages. Unreviewed code
  must not change what they run on. In its own folder, cleanup is the removal of the test
  directory that already happens at expiry.
- **Why after the venv:** one interpreter serves every package, so one version of a module is
  loaded. The installed packages keep theirs; when the draft asked for another version the
  install result lists it (`pip_shadowed`) instead of failing later without a reason.
- **Why not a venv and sidecar per test:** it would isolate versions and crashes, but the
  package manager serves one sidecar; scans, agents, commands and secrets would need routing
  across two. Worth doing later for crash isolation (a draft that hangs still takes the shared
  sidecar down). Not part of this.
