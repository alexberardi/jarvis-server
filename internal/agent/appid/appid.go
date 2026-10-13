// Package appid is the desktop agent's application identity, in one place: the macOS bundle id
// and codesign identifier, the LaunchAgent label, the portal app id and its .desktop file name,
// and the Windows task name all derive from ID (docs/install/01-no-cli-plan.md §6 Q1).
//
// Changing ID resets every macOS TCC grant and every GNOME/KDE portal grant, because both key
// on it. It was renamed once, before the first release, from LegacyID; Legacy* names exist only
// so dev installs made under the old id can be cleaned up.
package appid

const (
	// ID is the agent's reverse-DNS application id (jarvisd uses net.jarvisautomation.jarvisd).
	ID = "net.jarvisautomation.agent"
	// DisplayName is what the OS shows for the app: System Settings, menus, dialogs.
	DisplayName = "Jarvis Automation Agent"

	// WindowsTaskFolder and WindowsTaskName make up the optional logon task's path,
	// \Jarvis Automation\net.jarvisautomation.agent.
	WindowsTaskFolder = `\Jarvis Automation`
	WindowsTaskName   = WindowsTaskFolder + `\` + ID
)

// The identity used before the rename (dev installs only).
const (
	LegacyID = "io.jarvis.agent"
	// LegacyDisplayName names the old macOS bundle, "~/Applications/Jarvis Agent.app".
	LegacyDisplayName = "Jarvis Agent"
	// LegacyWindowsTaskName was the logon task's path.
	LegacyWindowsTaskName = `\Jarvis\JarvisAgent`
)

// DesktopFile is the freedesktop .desktop file name: the portal requires it to be ID + ".desktop".
const DesktopFile = ID + ".desktop"

// LegacyDesktopFile is the .desktop file `dictation allow` wrote before the rename.
const LegacyDesktopFile = LegacyID + ".desktop"
