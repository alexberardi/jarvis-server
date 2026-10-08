package doctor

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// dockerPS is the `docker ps --format` the legacy check reads: one container per line, the
// working directory last because it is the only field that may hold a ';'.
const dockerPS = `{{.ID}};{{.Names}};{{.Ports}};{{.Label "com.docker.compose.project"}};{{.Label "com.docker.compose.project.working_dir"}}`

// container is one running Docker container.
type container struct {
	ID, Name, Ports, Project, WorkingDir string
}

// parseDockerPS reads dockerPS lines.
func parseDockerPS(out string) []container {
	var cs []container
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimSpace(line), ";", 5)
		if len(f) < 2 || f[1] == "" {
			continue
		}
		for len(f) < 5 {
			f = append(f, "")
		}
		cs = append(cs, container{ID: f[0], Name: f[1], Ports: f[2], Project: f[3], WorkingDir: f[4]})
	}
	return cs
}

// LegacyContainer reports whether a container belongs to the legacy Jarvis stack (ID7):
//   - its name starts with jarvis- (every legacy service and its infrastructure), or
//   - it is in a legacy Compose project: "jarvis" (the jarvis-admin installer pins `name: jarvis`
//     in ~/.jarvis/compose/docker-compose.yml; that project also runs llama-server,
//     llama-server-bg, llm-proxy-worker and go2rtc), a "jarvis-*" project (a source checkout's
//     `./jarvis` CLI runs one project per service directory: jarvis-llm-proxy-api's
//     llm-proxy-*, jarvis-data-services; the dockerized node's jarvis-node), or any project
//     whose files are in ~/.jarvis/compose (installs from before the name was pinned, which
//     Compose named after the directory, "compose").
//
// Anything else (plex, home-assistant, a user's own project) is never legacy.
// scripts/install.sh and install.ps1 apply the same rule.
func LegacyContainer(name, project, workingDir string) bool {
	if strings.HasPrefix(name, "jarvis-") || project == "jarvis" || strings.HasPrefix(project, "jarvis-") {
		return true
	}
	wd := strings.TrimSuffix(strings.ReplaceAll(workingDir, `\`, "/"), "/")
	return strings.HasSuffix(wd, "/.jarvis/compose")
}

// legacyDirs are the legacy stack's directories for this user and, under sudo, the user who
// ran it.
func legacyDirs() []string {
	var dirs []string
	if h, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(h, ".jarvis"))
	}
	if su := os.Getenv("SUDO_USER"); su != "" {
		if u, err := user.Lookup(su); err == nil {
			dirs = append(dirs, filepath.Join(u.HomeDir, ".jarvis"))
		}
	}
	return dirs
}

// legacy looks for the legacy Docker stack (ID7). Its running containers are in the way when
// one publishes a jarvisd port, when portsHeld (another program answers on jarvisd's ports: a
// host-network stack publishes nothing), or when they hold GPU memory (its llama-servers hold
// whole cards, so jarvisd's models don't fit). Its infrastructure alone (postgres, redis, ...)
// and its directory are harmless. On Linux with nvidia-smi it also reports the GPU memory other
// programs hold ("gpu memory"). On macOS it also looks for the legacy native services
// (LegacyAgents), which are in the way whenever one is loaded.
func legacy(ctx context.Context, o Options, portsHeld bool) []Check {
	const name = "legacy stack"
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var running []container
	clash := portsHeld
	if out, err := o.Run(ctx, "docker", "ps", "--format", dockerPS); err == nil {
		var c bool
		running, c = legacyContainers(string(out), o.Ports)
		clash = clash || c
	}
	gpu, onGPU := gpuMemory(ctx, o, running)
	var out []Check
	if gpu != nil {
		out = append(out, *gpu)
	}
	names := make([]string, len(running))
	for i, c := range running {
		names[i] = c.Name
	}
	var details, fixes []string
	if len(running) > 0 && (clash || len(onGPU) > 0) {
		list := strings.Join(names, " ")
		var why []string
		if clash {
			why = append(why, "uses jarvisd's ports, so only one of them can run")
		}
		if len(onGPU) > 0 {
			why = append(why, "holds "+onGPU.String()+" of GPU memory that jarvisd's models need")
		}
		details = append(details, fmt.Sprintf("the legacy Jarvis Docker stack is running (%s) and %s", list, strings.Join(why, ", and ")))
		fixes = append(fixes, "stop it and keep it from starting at boot (its data is kept; `docker start` brings it back):\n"+
			"docker update --restart=no "+list+"\ndocker stop "+list+"\n"+
			"and, if the legacy admin runs as your user service (port 7711), whose reconcile starts it again:\n"+
			"systemctl --user disable --now jarvis-admin.service")
	}
	if agents := legacyAgents(ctx, o); len(agents) > 0 {
		d, f := agentsAdvice(o.LegacyUID, agents)
		details, fixes = append(details, d), append(fixes, f)
	}
	switch {
	case len(details) > 0:
		out = append(out, Check{Name: name, Status: Warn, Detail: strings.Join(details, "; "), Fix: strings.Join(fixes, "\n")})
	case len(running) > 0:
		out = append(out, Check{Name: name, Status: OK, Detail: "legacy containers run (" + strings.Join(names, ", ") + ") but none uses jarvisd's ports or the GPU"})
	default:
		out = append(out, legacyIdle(o))
	}
	return out
}

// LegacyAgents are the legacy stack's native services on macOS: user LaunchAgents (in
// ~/Library/LaunchAgents of the account that installed them) that each GPU service's
// deploy-launchd.sh, and through it the legacy admin's native mode, installs for Metal and
// Apple Vision: llm-proxy 7704/7705, whisper 7706, TTS 7707, OCR 7031 and its queue worker;
// plus the legacy admin itself (com.jarvis.admin, whose reconcile starts the Docker stack
// again). The legacy tooling has no Linux counterpart: there the services run in Docker (the
// admin's systemd user unit aside, which the Docker fix names), and a source checkout's
// `./jarvis` CLI runs "local" services as plain background processes, not units. Other
// com.jarvis.* and io.jarvis.* agents (osx-api, host-agent, nightly-benchmark) are not the
// server's and are never touched. scripts/install.sh --stop-legacy stops the same list.
var LegacyAgents = []string{"com.jarvis.llm-proxy", "com.jarvis.whisper-api", "com.jarvis.tts",
	"com.jarvis.ocr.service", "com.jarvis.ocr.worker", "com.jarvis.admin"}

// agent is a loaded legacy LaunchAgent.
type agent struct {
	Label, Path string
	PID         int // 0: loaded, not running
}

// legacyUID is the account whose LaunchAgents count: this user, or under sudo the one who
// ran it.
func legacyUID() string {
	if uid := os.Getuid(); uid > 0 {
		return strconv.Itoa(uid)
	}
	if su := os.Getenv("SUDO_UID"); su != "" && su != "0" {
		return su
	}
	return ""
}

// legacyAgents reads which LegacyAgents are loaded in o.LegacyUID's GUI domain (`launchctl
// print gui/<uid>/<label>` fails for one that isn't).
func legacyAgents(ctx context.Context, o Options) []agent {
	if o.GOOS != "darwin" || o.LegacyUID == "" {
		return nil
	}
	var as []agent
	for _, l := range LegacyAgents {
		out, err := o.Run(ctx, "launchctl", "print", "gui/"+o.LegacyUID+"/"+l)
		if err != nil {
			continue
		}
		a := agent{Label: l}
		for _, line := range strings.Split(string(out), "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), " = ")
			if !ok {
				continue
			}
			switch {
			case k == "path" && a.Path == "":
				a.Path = v
			case k == "pid" && a.PID == 0:
				a.PID, _ = strconv.Atoi(v)
			}
		}
		as = append(as, a)
	}
	return as
}

// agentsAdvice is the warning's detail and fix for uid's loaded legacy agents.
func agentsAdvice(uid string, as []agent) (detail, fix string) {
	who := "uid " + uid
	if u, err := user.LookupId(uid); err == nil {
		who = u.Username
	}
	names := make([]string, len(as))
	var stop, back []string
	for i, a := range as {
		names[i] = a.Label + " (not running)"
		if a.PID > 0 {
			names[i] = fmt.Sprintf("%s (pid %d)", a.Label, a.PID)
		}
		d := "gui/" + uid + "/" + a.Label
		stop = append(stop, "launchctl disable "+d, "launchctl bootout "+d)
		path := a.Path
		if path == "" {
			path = "~/Library/LaunchAgents/" + a.Label + ".plist"
		}
		back = append(back, "launchctl enable "+d+" && launchctl bootstrap gui/"+uid+" "+path)
	}
	detail = fmt.Sprintf("the legacy Jarvis native services are loaded as LaunchAgents of %s (%s): they use jarvisd's ports and start again at login",
		who, strings.Join(names, ", "))
	fix = "stop them and keep them from starting at login (`sudo sh install.sh --stop-legacy` does this), as " + who + ":\n" +
		strings.Join(stop, "\n") + "\nto bring them back later:\n" + strings.Join(back, "\n")
	return detail, fix
}

func legacyIdle(o Options) Check {
	for _, d := range o.LegacyDirs {
		for _, marker := range []string{"compose", "tokens.env"} {
			if _, err := os.Stat(filepath.Join(d, marker)); err == nil {
				return Check{Name: "legacy stack", Status: OK, Detail: "the legacy stack's files are in " + d + " but none of its containers run"}
			}
		}
	}
	return Check{Name: "legacy stack", Status: OK, Detail: "no legacy Jarvis Docker stack running"}
}

// legacyContainers picks the legacy stack's containers (LegacyContainer) from dockerPS output,
// and reports whether one publishes any of ports ("0.0.0.0:7700->7700/tcp, :::7700->7700/tcp").
func legacyContainers(out string, ports []Port) (legacy []container, clash bool) {
	for _, c := range parseDockerPS(out) {
		if !LegacyContainer(c.Name, c.Project, c.WorkingDir) {
			continue
		}
		legacy = append(legacy, c)
		for _, p := range ports {
			if strings.Contains(c.Ports, fmt.Sprintf(":%d->", p.Port)) {
				clash = true
			}
		}
	}
	return legacy, clash
}

// holders is GPU memory in MB by holder.
type holders map[string]int64

func (h holders) total() int64 {
	var n int64
	for _, mb := range h {
		n += mb
	}
	return n
}

// String is "N MB (a 1 MB, b 2 MB)", largest first.
func (h holders) String() string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if h[keys[i]] != h[keys[j]] {
			return h[keys[i]] > h[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d MB", k, h[k])
	}
	return fmt.Sprintf("%d MB (%s)", h.total(), strings.Join(parts, ", "))
}

// gpuMemory reads which processes hold NVIDIA GPU memory (Linux, nvidia-smi) and sorts them
// into the legacy containers' (by container, matched through /proc/<pid>/cgroup, which names
// the container's ID), jarvisd's own engines (a jarvisd process among the ancestors), and
// other programs. It returns the "gpu memory" check (nil without nvidia-smi) and the legacy
// containers' share.
func gpuMemory(ctx context.Context, o Options, legacy []container) (*Check, holders) {
	if o.GOOS != "linux" {
		return nil, nil // Windows (WDDM) and macOS report no per-process GPU memory
	}
	out, err := o.Run(ctx, "nvidia-smi", "--query-compute-apps=pid,process_name,used_memory", "--format=csv,noheader,nounits")
	if err != nil {
		return nil, nil
	}
	read := o.ReadFile
	if read == nil {
		read = os.ReadFile
	}
	onLegacy, others := holders{}, holders{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, ",")
		if len(f) != 3 {
			continue
		}
		pid, err1 := strconv.Atoi(strings.TrimSpace(f[0]))
		mb, err2 := strconv.ParseInt(strings.TrimSpace(f[2]), 10, 64)
		if err1 != nil || err2 != nil || mb <= 0 {
			continue // "[N/A]": a driver that doesn't report per-process memory
		}
		proc := filepath.Base(strings.TrimSpace(f[1]))
		cg, _ := read(fmt.Sprintf("/proc/%d/cgroup", pid))
		if c, ok := containerOf(string(cg), legacy); ok {
			onLegacy[c.Name] += mb
			continue
		}
		if jarvisdChild(read, pid) {
			continue
		}
		label := fmt.Sprintf("%s (pid %d", proc, pid)
		if strings.Contains(string(cg), "docker") {
			label += ", in a Docker container"
		}
		others[label+")"] += mb
	}
	c := &Check{Name: "gpu memory", Status: OK, Detail: "no other program holds GPU memory"}
	var parts []string
	if len(onLegacy) > 0 {
		parts = append(parts, "the legacy Jarvis stack holds "+onLegacy.String()+" (see \"legacy stack\")")
	}
	if len(others) > 0 {
		parts = append(parts, "other programs hold "+others.String()+"; jarvisd's models get what is left")
	}
	if len(parts) > 0 {
		c.Detail = strings.Join(parts, "; ")
	}
	return c, onLegacy
}

// containerOf finds the container whose ID a /proc/<pid>/cgroup names (cgroup v2
// ".../docker-<id>.scope", v1 "/docker/<id>"; `docker ps` gives the 12-character prefix).
func containerOf(cgroup string, cs []container) (container, bool) {
	if cgroup == "" {
		return container{}, false
	}
	for _, c := range cs {
		if len(c.ID) >= 12 && strings.Contains(cgroup, c.ID) {
			return c, true
		}
	}
	return container{}, false
}

// jarvisdChild reports whether a jarvisd process is pid or one of its ancestors (jarvisd
// starts its engines as children).
func jarvisdChild(read func(string) ([]byte, error), pid int) bool {
	for i := 0; i < 16 && pid > 1; i++ {
		b, err := read(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return false
		}
		// "pid (comm) state ppid ...": comm may hold spaces and parentheses.
		s := string(b)
		open, closing := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
		if open < 0 || closing < open {
			return false
		}
		if s[open+1:closing] == "jarvisd" {
			return true
		}
		f := strings.Fields(s[closing+1:])
		if len(f) < 2 {
			return false
		}
		if pid, err = strconv.Atoi(f[1]); err != nil {
			return false
		}
	}
	return false
}
