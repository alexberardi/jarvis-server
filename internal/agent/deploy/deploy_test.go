// Package deploy holds the supervisor templates. These tests guard one rule from several
// angles: the agent runs inside the user's interactive desktop session. Get it wrong and the
// agent still starts, still heartbeats and still sets the volume, while open launches into a
// session nobody sees and window enumeration returns nothing. They parse files only, so they
// run on every OS in the matrix.
package deploy

import (
	"encoding/xml"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const (
	unitPath  = "systemd/jarvis-agent.service"
	plistPath = "launchd/" + appid.ID + ".plist"
	taskPath  = "windows/" + appid.ID + ".xml"
	// bundlePlistPath is the LaunchAgent inside the app, registered with SMAppService.
	bundlePlistPath = "macos/LaunchAgent.plist"
)

// --- systemd user unit ---

// unitValues parses key=value lines per [Section].
func unitValues(text string) map[string]map[string]string {
	out := map[string]map[string]string{}
	sec := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			sec = line[1 : len(line)-1]
			out[sec] = map[string]string{}
		default:
			if k, v, ok := strings.Cut(line, "="); ok && sec != "" {
				out[sec][k] = v
			}
		}
	}
	return out
}

func TestSystemdIsAUserUnit(t *testing.T) {
	u := unitValues(read(t, unitPath))
	// multi-user.target is never reached in a user manager: such a unit installs and never
	// starts. default.target is reached before the compositor imports the display variables.
	if u["Install"]["WantedBy"] != "graphical-session.target" {
		t.Fatalf("WantedBy = %q", u["Install"]["WantedBy"])
	}
	for _, k := range []string{"User", "Group", "DynamicUser"} {
		if _, ok := u["Service"][k]; ok {
			t.Fatalf("a user unit must not set %s", k)
		}
	}
	if u["Service"]["Restart"] != "on-failure" {
		t.Fatalf("Restart = %q", u["Service"]["Restart"])
	}
	// run exits 78 when the computer isn't set up: no restart loop for it.
	if u["Service"]["RestartPreventExitStatus"] != "78" {
		t.Fatalf("RestartPreventExitStatus = %q", u["Service"]["RestartPreventExitStatus"])
	}
	if u["Service"]["ExecStart"] != "__AGENT_BIN__ run" {
		t.Fatalf("ExecStart = %q", u["Service"]["ExecStart"])
	}
	if !strings.Contains(u["Unit"]["After"], "graphical-session.target") {
		t.Fatal("must start after the graphical session")
	}
}

// --- LaunchAgent ---

// plistDict reads the top-level dict of a plist into key -> raw value element.
func plistDict(t *testing.T, text string) map[string]string {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(text))
	dec.Strict = false
	out := map[string]string{}
	var key string
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch e := tok.(type) {
		case xml.StartElement:
			depth++
			if depth != 3 { // plist > dict > (key|value)
				continue
			}
			if e.Name.Local == "key" {
				var s string
				_ = dec.DecodeElement(&s, &e)
				depth--
				key = s
				continue
			}
			var inner struct {
				Inner string `xml:",innerxml"`
			}
			_ = dec.DecodeElement(&inner, &e)
			depth--
			out[key] = e.Name.Local + ":" + strings.Join(strings.Fields(inner.Inner), " ")
		case xml.EndElement:
			depth--
		}
	}
	return out
}

func TestLaunchAgentRunsInTheGUISession(t *testing.T) {
	p := plistDict(t, read(t, plistPath))
	if p["RunAtLoad"] != "true:" || p["KeepAlive"] != keepAlive {
		t.Fatalf("RunAtLoad/KeepAlive = %q/%q", p["RunAtLoad"], p["KeepAlive"])
	}
	if p["ProcessType"] != "string:Interactive" {
		t.Fatalf("ProcessType = %q", p["ProcessType"])
	}
	if p["ProgramArguments"] != "array:<string>__AGENT_BIN__</string> <string>run</string>" {
		t.Fatalf("ProgramArguments = %q", p["ProgramArguments"])
	}
	// A daemon-only key would mean someone turned it into a LaunchDaemon.
	for _, k := range []string{"UserName", "GroupName", "InitGroups"} {
		if _, ok := p[k]; ok {
			t.Fatalf("%s makes this a daemon", k)
		}
	}
}

// keepAlive restarts after a failure only (a clean exit: a second copy, or not set up yet).
const keepAlive = "dict:<key>SuccessfulExit</key> <false/>"

// --- Windows Scheduled Task ---

type task struct {
	Principals struct {
		Principal []struct {
			UserID    string `xml:"UserId"`
			LogonType string `xml:"LogonType"`
			RunLevel  string `xml:"RunLevel"`
		} `xml:"Principal"`
	} `xml:"Principals"`
	Triggers struct {
		Logon []struct {
			UserID string `xml:"UserId"`
		} `xml:"LogonTrigger"`
	} `xml:"Triggers"`
	Settings struct {
		ExecutionTimeLimit string `xml:"ExecutionTimeLimit"`
	} `xml:"Settings"`
	Actions struct {
		Exec []struct {
			Command   string `xml:"Command"`
			Arguments string `xml:"Arguments"`
		} `xml:"Exec"`
	} `xml:"Actions"`
}

func parseTask(t *testing.T) task {
	var tk task
	if err := xml.Unmarshal([]byte(read(t, taskPath)), &tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestScheduledTaskIsInteractiveAndUnelevated(t *testing.T) {
	tk := parseTask(t)
	if len(tk.Principals.Principal) != 1 {
		t.Fatalf("principals: %+v", tk.Principals)
	}
	p := tk.Principals.Principal[0]
	// Session 0 has no desktop: this is the whole ballgame on Windows.
	if p.LogonType != "InteractiveToken" {
		t.Fatalf("LogonType = %q", p.LogonType)
	}
	// UIPI blocks an elevated process from messaging non-elevated windows.
	if p.RunLevel != "LeastPrivilege" {
		t.Fatalf("RunLevel = %q", p.RunLevel)
	}
	if len(tk.Triggers.Logon) != 1 {
		t.Fatal("must trigger at logon")
	}
	// The PT72H default would kill a long-running agent after three days.
	if tk.Settings.ExecutionTimeLimit != "PT0S" {
		t.Fatalf("ExecutionTimeLimit = %q", tk.Settings.ExecutionTimeLimit)
	}
	if len(tk.Actions.Exec) != 1 || tk.Actions.Exec[0].Command != "__AGENT_BIN__" || tk.Actions.Exec[0].Arguments != "run" {
		t.Fatalf("actions: %+v", tk.Actions.Exec)
	}
}

// Check the declared identities, not the prose: an earlier guard grepped the file for "SYSTEM"
// and matched the comment explaining why SYSTEM is wrong.
func TestScheduledTaskNeverRunsAsASystemAccount(t *testing.T) {
	tk := parseTask(t)
	ids := []string{tk.Principals.Principal[0].UserID}
	for _, l := range tk.Triggers.Logon {
		ids = append(ids, l.UserID)
	}
	banned := map[string]bool{"S-1-5-18": true, "SYSTEM": true, "LOCALSERVICE": true, "NETWORKSERVICE": true, `NT AUTHORITY\SYSTEM`: true}
	for _, id := range ids {
		id = strings.ToUpper(strings.TrimSpace(id))
		if banned[id] || id != "__USER_ID__" {
			t.Fatalf("UserId %q: must stay the __USER_ID__ placeholder", id)
		}
	}
}

func TestScheduledTaskName(t *testing.T) {
	var tk struct {
		URI string `xml:"RegistrationInfo>URI"`
	}
	if err := xml.Unmarshal([]byte(read(t, taskPath)), &tk); err != nil {
		t.Fatal(err)
	}
	if tk.URI != appid.WindowsTaskName {
		t.Fatalf("URI = %q, want %q", tk.URI, appid.WindowsTaskName)
	}
}

func TestLaunchAgentLabel(t *testing.T) {
	if l := plistDict(t, read(t, plistPath)); l["Label"] != "string:"+appid.ID {
		t.Fatalf("Label = %q", l["Label"])
	}
}

func TestXMLDeclarationMatchesEncoding(t *testing.T) {
	first := strings.SplitN(read(t, taskPath), "\n", 2)[0]
	if !strings.Contains(first, `encoding="UTF-8"`) {
		t.Fatalf("declaration %q: a UTF-16 declaration on UTF-8 bytes breaks every parser", first)
	}
}

// --- all three ---

func TestEveryTemplateExplainsTheSessionRule(t *testing.T) {
	for _, p := range []string{unitPath, plistPath, taskPath, bundlePlistPath} {
		text := strings.ToLower(read(t, p))
		if !regexp.MustCompile(`session|window server|graphical`).MatchString(text) {
			t.Fatalf("%s does not say why it must be user-scoped", p)
		}
		if p != bundlePlistPath && !strings.Contains(text, "__agent_bin__") {
			t.Fatalf("%s: binary path must be a placeholder", p)
		}
	}
}

func TestMacBundleInfoPlist(t *testing.T) {
	p := plistDict(t, read(t, "macos/Info.plist"))
	for k, want := range map[string]string{
		"CFBundleIdentifier":  "string:" + appid.ID, // TCC grants key on this: keep it stable
		"CFBundleDisplayName": "string:" + appid.DisplayName,
		"CFBundleExecutable":  "string:jarvis-agent",
		"CFBundlePackageType": "string:APPL",
		"LSUIElement":         "true:",
	} {
		if p[k] != want {
			t.Errorf("%s = %q, want %q", k, p[k], want)
		}
	}
	if !strings.HasPrefix(p["NSAppleEventsUsageDescription"], "string:") {
		t.Error("NSAppleEventsUsageDescription is required to drive System Events")
	}
	// Without it macOS kills the process the moment the voice pipeline opens the microphone.
	if !strings.HasPrefix(p["NSMicrophoneUsageDescription"], "string:") {
		t.Error("NSMicrophoneUsageDescription is required for the voice pipeline")
	}
	if l := plistDict(t, read(t, plistPath)); l["AssociatedBundleIdentifiers"] != "string:"+appid.ID {
		t.Errorf("LaunchAgent AssociatedBundleIdentifiers = %q", l["AssociatedBundleIdentifiers"])
	}
}

func TestBundleLaunchAgent(t *testing.T) {
	p := plistDict(t, read(t, bundlePlistPath))
	for k, want := range map[string]string{
		"Label":                       "string:" + appid.ID,
		"BundleProgram":               "string:Contents/MacOS/jarvis-agent",
		"ProgramArguments":            "array:<string>Contents/MacOS/jarvis-agent</string> <string>run</string>",
		"RunAtLoad":                   "true:",
		"KeepAlive":                   keepAlive,
		"ProcessType":                 "string:Interactive",
		"AssociatedBundleIdentifiers": "string:" + appid.ID,
	} {
		if p[k] != want {
			t.Errorf("%s = %q, want %q", k, p[k], want)
		}
	}
	for _, k := range []string{"UserName", "GroupName", "InitGroups"} {
		if _, ok := p[k]; ok {
			t.Fatalf("%s makes this a daemon", k)
		}
	}
}

func TestInfoPlistRegistersTheURLScheme(t *testing.T) {
	p := plistDict(t, read(t, "macos/Info.plist"))
	if !strings.Contains(p["CFBundleURLTypes"], "<string>jarvis-agent</string>") {
		t.Fatalf("CFBundleURLTypes = %q", p["CFBundleURLTypes"])
	}
}

// The embedded copies are what install renders: they must be these files.
func TestEmbeddedTemplates(t *testing.T) {
	for p, got := range map[string]string{unitPath: SystemdUnit, plistPath: LaunchAgent, bundlePlistPath: BundleLaunchAgent,
		"macos/Info.plist": InfoPlist, taskPath: WindowsTask} {
		if got != read(t, p) {
			t.Errorf("%s: embedded copy differs", p)
		}
	}
}
