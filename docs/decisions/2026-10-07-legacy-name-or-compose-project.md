# Legacy = name or Compose project

- **Date:** 2026-10-07
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Legacy = name or Compose project** (ID7 refined after the prod survey): a container is the legacy stack's when named `jarvis-*` or labelled into project `jarvis`, a `jarvis-*` project, or a project in `~/.jarvis/compose`; `--stop-legacy` stops those (never `down`/`rm`) and disables the legacy admin user unit. Reason: prod's llama-servers (~40 GB VRAM) are not named `jarvis-*`; a project match never reaches unrelated containers (plex). go2rtc is in project `jarvis` and is stopped with it (runbook Q7).
