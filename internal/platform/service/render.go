package service

import (
	"bytes"
	"encoding/xml"
	"path"
	"strings"
	"text/template"
)

// Unit is the input to the systemd unit template.
type Unit struct {
	Binary string // absolute path of the jarvisd binary
	Home   string // data directory, passed as --home
	// User renders a `systemctl --user` unit: no User=/Group=, no sandboxing a user
	// manager can't apply, WantedBy=default.target.
	User bool
	// Account is the system unit's User=/Group= (default "jarvisd").
	Account string
}

// systemdTmpl is 00-installers §2.1's unit. KillMode=mixed sends SIGTERM to jarvisd only; it
// stops its own engines and the rest of the cgroup is killed after TimeoutStopSec.
// PrivateDevices must stay off (GPU device nodes), and MemoryDenyWriteExecute too (the
// onnxruntime and llama.cpp backends JIT).
var systemdTmpl = template.Must(template.New("unit").Funcs(template.FuncMap{"q": systemdQuote}).Parse(
	`# Written by "jarvisd service install". Local changes are lost on the next install;
# put overrides in "systemctl{{if .User}} --user{{end}} edit jarvisd" instead.
[Unit]
Description=Jarvis server (jarvisd)
Documentation=https://github.com/alexberardi/jarvis-server
{{- if not .User}}
After=network-online.target
Wants=network-online.target
{{- end}}
StartLimitIntervalSec=600
StartLimitBurst=10

[Service]
Type=notify
{{- if not .User}}
User={{.Account}}
Group={{.Account}}
{{- end}}
{{- if not .User}}
# Self-update (AD5, ID11): the service account can't write the binary, so a privileged
# pre-start ("+": full privileges, no sandbox; "-": never blocks the start) swaps in a release
# the service staged, re-verifying it first, or restores the previous binary for a requested
# rollback. It treats the data directory as untrusted (it must belong to --owner).
Environment=JARVIS_UPGRADE_HELPER=1
ExecStartPre=-+{{q .Binary}} upgrade --prestart --home {{q .Home}} --owner {{.Account}}
{{- end}}
ExecStart={{q .Binary}} serve --home {{q .Home}}
Restart=always
RestartSec=5
TimeoutStartSec=300
TimeoutStopSec=30
KillMode=mixed
LimitNOFILE=65536
UMask=0077
NoNewPrivileges=yes
{{- if not .User}}
ProtectSystem=strict
ReadWritePaths={{q .Home}}
ProtectHome=yes
PrivateTmp=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes
{{- end}}
SyslogIdentifier=jarvisd

[Install]
WantedBy={{if .User}}default.target{{else}}multi-user.target{{end}}
`))

// RenderSystemd renders the jarvisd.service unit.
func RenderSystemd(u Unit) ([]byte, error) {
	if u.Account == "" {
		u.Account = Name
	}
	var b bytes.Buffer
	err := systemdTmpl.Execute(&b, u)
	return b.Bytes(), err
}

// systemdQuote quotes a path for a unit file when it holds a space, quote or backslash.
func systemdQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// Plist is the input to the LaunchDaemon template.
type Plist struct {
	Binary   string
	Home     string
	UserName string // the account the daemon runs as (the installing user, ID1)
	UserHome string // that account's home directory, set as HOME for the engines
}

// launchdTmpl is 00-installers §2.2's plist. ProcessType Interactive keeps launchd from
// throttling a GPU server; Umask 63 is 077.
var launchdTmpl = template.Must(template.New("plist").Funcs(template.FuncMap{"x": xmlEscape}).Parse(
	`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Written by "jarvisd service install". -->
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{x .Label}}</string>
	<key>UserName</key>
	<string>{{x .UserName}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{x .Binary}}</string>
		<string>serve</string>
		<string>--home</string>
		<string>{{x .Home}}</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>HOME</key>
		<string>{{x .UserHome}}</string>
	</dict>
	<key>WorkingDirectory</key>
	<string>{{x .Home}}</string>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>Umask</key>
	<integer>63</integer>
	<key>ExitTimeOut</key>
	<integer>30</integer>
	<key>SoftResourceLimits</key>
	<dict>
		<key>NumberOfFiles</key>
		<integer>65536</integer>
	</dict>
	<key>StandardOutPath</key>
	<string>{{x .Log}}</string>
	<key>StandardErrorPath</key>
	<string>{{x .Log}}</string>
</dict>
</plist>
`))

// RenderLaunchd renders the LaunchDaemon plist.
func RenderLaunchd(p Plist) ([]byte, error) {
	var b bytes.Buffer
	err := launchdTmpl.Execute(&b, struct {
		Plist
		Label, Log string
	}{p, LaunchdLabel, path.Join(p.Home, "logs", LogFile)})
	return b.Bytes(), err
}

// HelperPlist is the input to the updater LaunchDaemon (ID11).
type HelperPlist struct {
	Binary   string
	Home     string
	UserName string // the account jarvisd runs as: the home must belong to it
}

// HelperLog is the updater's log: root-owned and outside the home, which the service account
// can write (launchd would open a path there as root).
const HelperLog = "/Library/Logs/jarvisd-updater.log"

// launchdHelperTmpl is the root LaunchDaemon that does the privileged step of a self-update
// (00-installers §8.2): launchd runs it while <home>/updates/requests is non-empty (jarvisd
// drops a file there) and once at load. No UserName: it runs as root. Its log and working
// directory are outside the home, and PATH is the system one.
var launchdHelperTmpl = template.Must(template.New("helper").Funcs(template.FuncMap{"x": xmlEscape}).Parse(
	`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Written by "jarvisd service install". -->
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{x .Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{x .Binary}}</string>
		<string>upgrade</string>
		<string>--helper</string>
		<string>--home</string>
		<string>{{x .Home}}</string>
		<string>--owner</string>
		<string>{{x .UserName}}</string>
	</array>
	<key>QueueDirectories</key>
	<array>
		<string>{{x .Requests}}</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>/usr/bin:/bin:/usr/sbin:/sbin</string>
	</dict>
	<key>WorkingDirectory</key>
	<string>/</string>
	<key>Umask</key>
	<integer>18</integer>
	<key>ExitTimeOut</key>
	<integer>120</integer>
	<key>StandardOutPath</key>
	<string>{{x .Log}}</string>
	<key>StandardErrorPath</key>
	<string>{{x .Log}}</string>
</dict>
</plist>
`))

// RenderLaunchdHelper renders the updater LaunchDaemon plist.
func RenderLaunchdHelper(p HelperPlist) ([]byte, error) {
	var b bytes.Buffer
	err := launchdHelperTmpl.Execute(&b, struct {
		HelperPlist
		Label, Requests, Log string
	}{p, HelperLabel, path.Join(p.Home, "updates", "requests"), HelperLog})
	return b.Bytes(), err
}

// HelperSDDL is the Windows updater service's security descriptor: the SCM's defaults
// (SYSTEM and Administrators full control, interactive and service logons may query) plus
// query and start, nothing else, for jarvisd's service SID (NT SERVICE\jarvisd): the
// unprivileged service can ask for the privileged step but can't stop, change or delete it.
func HelperSDDL(jarvisdSID string) string {
	return "D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)" +
		"(A;;CCLCSWLOCRRC;;;IU)(A;;CCLCSWLOCRRC;;;SU)(A;;CCLCRP;;;" + jarvisdSID + ")"
}

func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// splitUnitArgs splits an ExecStart value on blanks, honouring the double quotes and
// backslash escapes systemdQuote writes.
func splitUnitArgs(s string) []string {
	var args []string
	var cur strings.Builder
	inQuote, have := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && inQuote && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
		case c == '"':
			inQuote, have = !inQuote, true
		case (c == ' ' || c == '\t') && !inQuote:
			if have {
				args = append(args, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	if have {
		args = append(args, cur.String())
	}
	return args
}
