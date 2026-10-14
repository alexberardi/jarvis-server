# AD8b admin Stop button: an exit each supervisor leaves alone

- **Date:** 2026-10-08
- **Moved from** the STATUS.md decision log on 2026-10-14.

**AD8b admin Stop button: an exit each supervisor leaves alone.** systemd exits 98 (`StopExitCode`; units gain `RestartPreventExitStatus=98` + `SuccessExitStatus=98`, so `Restart=always` stays for everything else and the unit ends inactive, not failed); launchd and the SCM exit 0 (plist `KeepAlive` becomes `{SuccessfulExit: false}`: crashes, `kill -9` and exit 75 still restart; the SCM's recovery actions never fire on a clean `SERVICE_STOPPED`). A unit/plist from before AD8b would restart jarvisd at once, so jarvisd reads its own definition (`service.StopBlocker`) and reports `capabilities.stop: false` + `sudo jarvisd service install` instead of exiting; `service status` calls it stale, so `jarvisd upgrade` refreshes it. Start again with `jarvisd service start` (`sudo`, `--user`, elevated PowerShell) or a reboot; unsupervised, `jarvisd serve`.
