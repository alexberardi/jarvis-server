//go:build unix

package service

// StopBlocker says why the admin Stop button (AD8b) can't keep jarvisd stopped under kind ("" when
// it can), with the command that fixes it or does the stop instead. Under systemd and launchd
// it reads the installed unit or LaunchDaemon: one an older version wrote restarts jarvisd on
// any exit, so stopping would only restart it until `service install` rewrites it.
// Unsupervised, jarvisd simply exits.
func StopBlocker(kind Kind, user bool) (reason, command string) {
	switch kind {
	case Systemd:
		return newSystemd(user, nil).stopBlocker()
	case Launchd:
		return newLaunchd(nil).stopBlocker()
	}
	return "", ""
}
