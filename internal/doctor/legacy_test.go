package doctor

import (
	"context"
	"net"
	"os"
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
