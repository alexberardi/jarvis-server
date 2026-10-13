// Package deploy holds the agent's supervisor templates: the systemd user unit, the LaunchAgent
// (a plain one, and the one inside the app bundle that SMAppService registers), the app's
// Info.plist and the Windows logon task. `jarvis-agent install` renders them; the tests guard
// that each keeps the agent inside the user's interactive session.
package deploy

import _ "embed"

// Placeholders the templates carry.
const (
	BinPlaceholder    = "__AGENT_BIN__"
	LogDirPlaceholder = "__LOG_DIR__"
	VersionHolder     = "__VERSION__"
	UserIDPlaceholder = "__USER_ID__"
)

var (
	//go:embed systemd/jarvis-agent.service
	SystemdUnit string
	//go:embed launchd/net.jarvisautomation.agent.plist
	LaunchAgent string
	//go:embed macos/LaunchAgent.plist
	BundleLaunchAgent string
	//go:embed macos/Info.plist
	InfoPlist string
	//go:embed windows/net.jarvisautomation.agent.xml
	WindowsTask string
)

// SystemdUnitName is the user unit's file name.
const SystemdUnitName = "jarvis-agent.service"
