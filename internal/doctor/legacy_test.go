package doctor

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyContainer(t *testing.T) {
	for _, tc := range []struct {
		name, project, wd string
		want              bool
	}{
		{"jarvis-auth", "", "", true},
		{"jarvis-demo-node", "jarvis-node", "/home/j/jarvis-node", true},
		{"llama-server", "jarvis", "/home/j/.jarvis/compose", true},
		{"llama-server-bg", "jarvis", "/host/compose", true}, // the admin ran compose in its container
		{"go2rtc", "jarvis", "/home/j/.jarvis/compose", true},
		{"llm-proxy-worker", "jarvis-llm-proxy-api", "/home/j/src/jarvis-llm-proxy-api", true},
		{"llm-proxy-worker", "compose", "/home/j/.jarvis/compose/", true}, // before `name: jarvis`
		{"plex", "", "", false},
		{"go2rtc", "cameras", "/home/j/cameras", false},
		{"llama-server", "myllm", "/home/j/llm", false},
		{"web", "jarvisish", "/srv/jarvisish", false},
		{"x", "compose", "/home/j/compose", false},
	} {
		if got := LegacyContainer(tc.name, tc.project, tc.wd); got != tc.want {
			t.Errorf("LegacyContainer(%q, %q, %q) = %v", tc.name, tc.project, tc.wd, got)
		}
	}
}

// Prod (2026-10-07): the two legacy llama-servers fill both cards; a desktop holds a little;
// jarvisd's own engine is not counted.
func TestLegacyGPUMemory(t *testing.T) {
	o := opts(nil, []*net.IPNet{})
	o.Firewall = &linuxFirewall{ufw: &UFW{Root: t.TempDir()}, run: noFirewalld}
	ps := "docker ps --format " + dockerPS
	smi := "nvidia-smi --query-compute-apps=pid,process_name,used_memory --format=csv,noheader,nounits"
	answers := map[string]string{
		ps: "0123456789ab;llama-server;;jarvis;/home/j/.jarvis/compose\n" +
			"bbbbbbbbbbbb;llama-server-bg;;jarvis;/home/j/.jarvis/compose\n" +
			"cccccccccccc;jarvis-postgres;;jarvis;/home/j/.jarvis/compose\n",
		smi: "100, /app/llama-server, 18100\n101, /app/llama-server, 20200\n200, /usr/lib/Xorg, 300\n" +
			"300, /var/lib/jarvisd/engines/llama-server, 9000\n400, [N/A], [N/A]\n",
	}
	o.Run = fakeRun(answers)
	files := map[string]string{
		"/proc/100/cgroup": "0::/system.slice/docker-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.scope\n",
		"/proc/101/cgroup": "12:memory:/docker/bbbbbbbbbbbbffff\n",
		"/proc/200/cgroup": "0::/system.slice/display-manager.service\n",
		"/proc/200/stat":   "200 (Xorg) S 1 200 200 0 -1",
		"/proc/300/cgroup": "0::/system.slice/jarvisd.service\n",
		"/proc/300/stat":   "300 (llama-server) S 299 300 300 0 -1",
		"/proc/299/stat":   "299 (jarvisd) S 1 299 299 0 -1",
	}
	o.ReadFile = func(name string) ([]byte, error) {
		if s, ok := files[name]; ok {
			return []byte(s), nil
		}
		return nil, os.ErrNotExist
	}
	checks := Run(context.Background(), o)
	c := find(t, checks, "legacy stack")
	if c.Status != Warn || !strings.Contains(c.Detail, "holds 38300 MB (llama-server-bg 20200 MB, llama-server 18100 MB) of GPU memory") ||
		strings.Contains(c.Detail, "jarvisd's ports") || !strings.Contains(c.Fix, "docker stop llama-server llama-server-bg jarvis-postgres\n") {
		t.Errorf("legacy %+v", c)
	}
	g := find(t, checks, "gpu memory")
	if g.Status != OK || !strings.Contains(g.Detail, "other programs hold 300 MB (Xorg (pid 200) 300 MB)") ||
		strings.Contains(g.Detail, "9000") || !strings.Contains(g.Detail, "legacy Jarvis stack holds 38300 MB") {
		t.Errorf("gpu %+v", g)
	}
	// Without docker access (the admin's in-process doctor) a container still shows as one.
	delete(answers, ps)
	g = find(t, Run(context.Background(), o), "gpu memory")
	if !strings.Contains(g.Detail, "llama-server (pid 101, in a Docker container) 20200 MB") {
		t.Errorf("gpu without docker %+v", g)
	}
	// After --stop-legacy: only jarvisd's engine; the legacy check is quiet again.
	answers[ps] = ""
	answers[smi] = "300, /var/lib/jarvisd/engines/llama-server, 9000\n"
	checks = Run(context.Background(), o)
	if g := find(t, checks, "gpu memory"); g.Detail != "no other program holds GPU memory" {
		t.Errorf("gpu after %+v", g)
	}
	if c := find(t, checks, "legacy stack"); c.Status != OK {
		t.Errorf("legacy after %+v", c)
	}
	// Without nvidia-smi, or off Linux, there is no GPU check.
	delete(answers, smi)
	for _, c := range Run(context.Background(), o) {
		if c.Name == "gpu memory" {
			t.Errorf("no nvidia-smi %+v", c)
		}
	}
	answers[smi] = "200, /usr/lib/Xorg, 300\n"
	o.GOOS = "windows"
	for _, c := range Run(context.Background(), o) {
		if c.Name == "gpu memory" {
			t.Errorf("windows %+v", c)
		}
	}
}

// install.sh --stop-legacy stops exactly the agents the doctor names.
func TestInstallScriptStopsLegacyAgents(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	want := `LEGACY_AGENTS="` + strings.Join(LegacyAgents, " ") + `"`
	if !strings.Contains(string(b), "\n"+want+"\n") {
		t.Errorf("install.sh has no line %s", want)
	}
}

// `launchctl print gui/501/com.jarvis.tts` on macOS 15, trimmed.
const launchctlTTS = `gui/501/com.jarvis.tts = {
	active count = 1
	path = /Users/j/Library/LaunchAgents/com.jarvis.tts.plist
	type = LaunchAgent
	state = running

	program = /bin/bash
	arguments = {
		/bin/bash
		/Users/j/.jarvis/native/jarvis-tts/run-prod.sh
	}

	pid = 4242
	immediate reason = speculative
	endpoints = {
	}
}
`

// The Mac dev box: the legacy GPU services run natively as LaunchAgents (no containers), next
// to agents that are not the server's. The doctor names the legacy ones and the commands.
func TestLegacyAgents(t *testing.T) {
	o := opts(nil, []*net.IPNet{})
	o.GOOS, o.LegacyUID = "darwin", "99999" // no such account: "uid 99999"
	answers := map[string]string{
		"launchctl print gui/99999/com.jarvis.tts":        launchctlTTS,
		"launchctl print gui/99999/com.jarvis.llm-proxy":  "gui/99999/com.jarvis.llm-proxy = {\n\tpath = /Users/j/Library/LaunchAgents/com.jarvis.llm-proxy.plist\n\tstate = running\n\tpid = 77\n}\n",
		"launchctl print gui/99999/com.jarvis.ocr.worker": "gui/99999/com.jarvis.ocr.worker = {\n\tstate = not running\n}\n",
		"launchctl print gui/99999/com.jarvis.osx-api":    launchctlTTS, // never asked
		"launchctl print gui/99999/io.jarvis.host-agent":  launchctlTTS,
	}
	o.Run = fakeRun(answers)
	c := find(t, legacy(context.Background(), o, true), "legacy stack")
	if c.Status != Warn || !strings.Contains(c.Detail, "LaunchAgents of uid 99999 (com.jarvis.llm-proxy (pid 77), com.jarvis.tts (pid 4242), com.jarvis.ocr.worker (not running))") ||
		strings.Contains(c.Detail, "Docker") || strings.Contains(c.Detail+c.Fix, "osx-api") || strings.Contains(c.Detail+c.Fix, "host-agent") {
		t.Errorf("legacy %+v", c)
	}
	for _, want := range []string{
		"launchctl disable gui/99999/com.jarvis.tts\nlaunchctl bootout gui/99999/com.jarvis.tts\n",
		"launchctl enable gui/99999/com.jarvis.tts && launchctl bootstrap gui/99999 /Users/j/Library/LaunchAgents/com.jarvis.tts.plist",
		"launchctl bootstrap gui/99999 ~/Library/LaunchAgents/com.jarvis.ocr.worker.plist", // no path printed
	} {
		if !strings.Contains(c.Fix, want) {
			t.Errorf("fix lacks %q:\n%s", want, c.Fix)
		}
	}
	// With the Docker stack too, both are named in one check.
	ps := "docker ps --format " + dockerPS
	answers[ps] = "0123456789ab;jarvis-config-service;0.0.0.0:7700->7700/tcp;jarvis;/Users/j/.jarvis/compose\n"
	c = find(t, legacy(context.Background(), o, false), "legacy stack")
	if !strings.Contains(c.Detail, "Docker stack is running (jarvis-config-service)") || !strings.Contains(c.Detail, "; the legacy Jarvis native services") ||
		!strings.Contains(c.Fix, "docker stop jarvis-config-service\n") || !strings.Contains(c.Fix, "launchctl bootout gui/99999/com.jarvis.llm-proxy") {
		t.Errorf("both %+v", c)
	}
	// After --stop-legacy nothing is loaded: quiet again.
	delete(answers, ps)
	for _, l := range LegacyAgents {
		delete(answers, "launchctl print gui/99999/"+l)
	}
	if c := find(t, legacy(context.Background(), o, false), "legacy stack"); c.Status != OK {
		t.Errorf("after %+v", c)
	}
	// Off macOS, or with no account to look at, launchctl isn't asked.
	answers["launchctl print gui/99999/com.jarvis.tts"] = launchctlTTS
	for _, oo := range []Options{{GOOS: "linux", LegacyUID: "99999"}, {GOOS: "darwin"}} {
		oo.Run, oo.LegacyDirs = o.Run, []string{}
		if c := find(t, legacy(context.Background(), oo, false), "legacy stack"); c.Status != OK {
			t.Errorf("%s: %+v", oo.GOOS, c)
		}
	}
}
