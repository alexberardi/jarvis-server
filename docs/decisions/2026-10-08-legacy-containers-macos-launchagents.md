# Legacy = containers + macOS LaunchAgents

- **Date:** 2026-10-08
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Legacy = containers + macOS LaunchAgents** (ID7, after rc3 `sudo sh install.sh --stop-legacy` refused on a Mac): `doctor.LegacyAgents` = `com.jarvis.{llm-proxy,whisper-api,tts,ocr.service,ocr.worker,admin}` in the invoking user's GUI domain; `--stop-legacy` disables + boots them out (restore lines printed), stopping legacy *before* the ports check. Nothing else (osx-api, host-agent, nightly-benchmark) is touched; no Linux unit equivalents exist. Under sudo on macOS the script puts `/usr/local/bin` + `/opt/homebrew/bin` back on PATH (docker CLI, minisign).
