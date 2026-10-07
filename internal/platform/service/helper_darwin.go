package service

import (
	"context"
	"errors"
	"os"
)

// DetectHelper reports the updater LaunchDaemon when its plist is installed.
func DetectHelper() Helper {
	if st, err := os.Stat(helperPlist); err == nil && st.Mode().IsRegular() {
		return HelperLaunchd
	}
	return HelperNone
}

// StartHelper is for the Windows helper; the macOS one is woken by a request file
// (update.Request), which an unprivileged process can write.
func StartHelper(context.Context) error {
	return errors.New("the macOS helper is woken by a request file, not started")
}

// RestartFromHelper restarts the jarvisd LaunchDaemon (kill, then start again) after the root
// helper swapped or restored its binary. launchctl by absolute path: the helper trusts no PATH.
func RestartFromHelper(ctx context.Context) error {
	_, err := execRun(ctx, "/bin/launchctl", "kickstart", "-k", target)
	return err
}
