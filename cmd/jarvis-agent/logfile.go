package main

import (
	"path/filepath"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
)

// macLogFile is the agent's log on macOS, under either login item: the plain LaunchAgent
// names it for stdout and stderr; under the app's SMAppService LaunchAgent, whose plist can't
// name a path in the user's home, `run` opens it itself (redirectLaunchdOutput).
func macLogFile(home string) string {
	return filepath.Join(home, "Library", "Logs", "jarvis-agent.log")
}

// launchdLogTarget is where run sends its output: macLogFile when launchd started it as the
// agent's job (XPC_SERVICE_NAME is the label) with stderr on /dev/null, i.e. the SMAppService
// LaunchAgent; "" otherwise (the plain LaunchAgent already gave it the file; a terminal keeps
// its output).
func launchdLogTarget(service string, stderrIsNull bool, home string) string {
	if service != appid.ID || !stderrIsNull || home == "" {
		return ""
	}
	return macLogFile(home)
}
