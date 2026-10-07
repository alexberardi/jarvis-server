package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// osChecks are the per-OS install checks: the Windows network profile and Mac sleep.
func osChecks(ctx context.Context, o Options) []Check {
	switch o.GOOS {
	case "windows":
		out, err := o.Run(ctx, "netsh", "advfirewall", "show", "currentprofile")
		if err != nil {
			return nil
		}
		return windowsProfile(string(out))
	case "darwin":
		out, err := o.Run(ctx, "pmset", "-g")
		if err != nil {
			return nil
		}
		return macSleep(string(out))
	}
	return nil
}

// windowsProfile reads `netsh advfirewall show currentprofile`, which titles each active
// profile "<Name> Profile Settings:". jarvisd's firewall rule is for the private profile.
func windowsProfile(out string) []Check {
	public := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, "Profile Settings:") && strings.HasPrefix(line, "Public") {
			public = true
		}
	}
	if !public {
		return []Check{{Name: "network profile", Status: OK, Detail: "the active network is not Public"}}
	}
	return []Check{{Name: "network profile", Status: Warn,
		Detail: "Windows treats the active network as Public: jarvisd's firewall rule covers private networks only, " +
			"so nodes and phones can't reach it until this network is marked Private",
		Fix: "Get-NetConnectionProfile | Where-Object NetworkCategory -eq Public | Set-NetConnectionProfile -NetworkCategory Private\n" +
			"(PowerShell as Administrator, and only on your home network)"}}
}

var pmsetSleep = regexp.MustCompile(`(?m)^\s*sleep\s+(\d+)`)

// macSleep reads `pmset -g`: "sleep N" is the idle minutes before the Mac sleeps (0 = never).
func macSleep(out string) []Check {
	m := pmsetSleep.FindStringSubmatch(out)
	if m == nil {
		return nil
	}
	if n, _ := strconv.Atoi(m[1]); n > 0 {
		return []Check{{Name: "sleep", Status: Warn,
			Detail: fmt.Sprintf("this Mac sleeps after %d idle minutes, and a sleeping server doesn't answer nodes or phones", n),
			Fix:    "sudo pmset -a sleep 0"}}
	}
	return []Check{{Name: "sleep", Status: OK, Detail: "this Mac does not go to sleep"}}
}

// secretFiles are the files under the data directory that must be owner-only.
var secretFiles = []string{"jarvis.db", "jarvis.db-wal", "jarvis.db-shm", "setup-token"}

// permissions checks what `jarvisd serve` enforces at start (secureHome, db.Open): the data
// directory 0700, the database and setup token 0600, and all of them owned by the data
// directory's owner (a `sudo jarvisd serve` leaves root-owned files the service can't open).
func permissions(home string) []Check {
	if home == "" || !unixModes {
		return nil
	}
	const name = "data directory"
	fi, err := os.Stat(home)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return []Check{{Name: name, Status: OK, Detail: home + " doesn't exist yet; jarvisd creates it owner-only on first start"}}
	case err != nil:
		return []Check{{Name: name, Status: OK, Detail: "can't inspect " + home + ": " + err.Error()}}
	}
	owner, hasOwner := fileOwner(fi)
	var wide, foreign []string
	if fi.Mode().Perm()&0o077 != 0 {
		wide = append(wide, fmt.Sprintf("%s is %04o", home, fi.Mode().Perm()))
	}
	for _, f := range secretFiles {
		p := filepath.Join(home, f)
		fi, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return []Check{{Name: name, Status: OK,
				Detail: home + " belongs to another account, so its files can't be inspected from here (run `sudo jarvisd doctor` to check them)"}}
		}
		if fi.Mode().Perm()&0o077 != 0 {
			wide = append(wide, fmt.Sprintf("%s is %04o", f, fi.Mode().Perm()))
		}
		if uid, ok := fileOwner(fi); ok && hasOwner && uid != owner {
			foreign = append(foreign, f)
		}
	}
	var out []Check
	if len(wide) > 0 {
		out = append(out, Check{Name: name, Status: Warn,
			Detail: strings.Join(wide, ", ") + ": others on this machine can read jarvisd's signing key and password hashes",
			Fix: fmt.Sprintf("restart jarvisd (it tightens them at start), or: chmod 700 %s && chmod 600 %s",
				shellQuote(home), shellQuote(filepath.Join(home, "jarvis.db"))+"*")})
	}
	if len(foreign) > 0 {
		who := strconv.Itoa(owner)
		if u, err := user.LookupId(who); err == nil {
			who = u.Username
		}
		out = append(out, Check{Name: name + " owner", Status: Warn,
			Detail: strings.Join(foreign, ", ") + " in " + home + " belong to another account than the directory (" + who +
				"), so jarvisd may fail to open them; was it started with sudo?",
			Fix: fmt.Sprintf("sudo chown -R %s %s", who, shellQuote(home))})
	}
	if len(out) == 0 {
		out = append(out, Check{Name: name, Status: OK, Detail: home + " and its database are owner-only"})
	}
	return out
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

// legacy looks for the legacy Docker stack (ID7). Its running jarvis-* containers are in the
// way when one publishes a jarvisd port, or when portsHeld (another program answers on
// jarvisd's ports: a host-network stack publishes nothing). Its infrastructure alone
// (postgres, redis, ...) and its directory are harmless.
func legacy(ctx context.Context, o Options, portsHeld bool) []Check {
	const name = "legacy stack"
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var running []string
	clash := portsHeld
	if out, err := o.Run(ctx, "docker", "ps", "--format", "{{.Names}};{{.Ports}}"); err == nil {
		var c bool
		running, c = legacyContainers(string(out), o.Ports)
		clash = clash || c
	}
	if len(running) > 0 && clash {
		list := strings.Join(running, " ")
		return []Check{{Name: name, Status: Warn,
			Detail: fmt.Sprintf("the legacy Jarvis Docker stack is running (%s) and uses jarvisd's ports, so only one of them can run", list),
			Fix: "stop it and keep it from starting at boot (its data is kept; `docker start` brings it back):\n" +
				"docker update --restart=no " + list + "\ndocker stop " + list}}
	}
	if len(running) > 0 {
		return []Check{{Name: name, Status: OK, Detail: "legacy containers run (" + strings.Join(running, ", ") + ") but none uses jarvisd's ports"}}
	}
	for _, d := range o.LegacyDirs {
		for _, marker := range []string{"compose", "tokens.env"} {
			if _, err := os.Stat(filepath.Join(d, marker)); err == nil {
				return []Check{{Name: name, Status: OK, Detail: "the legacy stack's files are in " + d + " but none of its containers run"}}
			}
		}
	}
	return []Check{{Name: name, Status: OK, Detail: "no legacy Jarvis Docker stack running"}}
}

// legacyContainers picks the legacy stack's containers (jarvis-*) from `docker ps` lines of
// "name;ports", and reports whether one publishes any of ports
// ("0.0.0.0:7700->7700/tcp, :::7700->7700/tcp").
func legacyContainers(out string, ports []Port) (names []string, clash bool) {
	for _, line := range strings.Split(out, "\n") {
		n, published, _ := strings.Cut(strings.TrimSpace(line), ";")
		if !strings.HasPrefix(n, "jarvis-") {
			continue
		}
		names = append(names, n)
		for _, p := range ports {
			if strings.Contains(published, fmt.Sprintf(":%d->", p.Port)) {
				clash = true
			}
		}
	}
	return names, clash
}

// FixRunner runs a fix command with up to two minutes for it (firewalld reloads are slow).
func FixRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := command(ctx, name, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// Apply runs every check's FixCmds (each distinct command once: several subnets can share
// one), printing each command before it runs. It stops at the first failure and reports how
// many commands ran.
func Apply(ctx context.Context, checks []Check, run Runner, out io.Writer) (int, error) {
	if run == nil {
		run = FixRunner
	}
	seen := map[string]bool{}
	n := 0
	for _, c := range checks {
		for _, cmd := range c.FixCmds {
			key := strings.Join(cmd, "\x00")
			if len(cmd) == 0 || seen[key] {
				continue
			}
			seen[key] = true
			fmt.Fprintf(out, "+ %s\n", strings.Join(cmd, " "))
			if _, err := run(ctx, cmd[0], cmd[1:]...); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// RemoveFirewallRules deletes the rules the firewall fix added (`jarvisd service uninstall`).
// It carries on past a failing command and returns the first error.
func RemoveFirewallRules(ctx context.Context, fw Firewall, run Runner, out io.Writer) error {
	if fw == nil {
		fw = HostFirewall("")
	}
	if fw == nil {
		return nil
	}
	if run == nil {
		run = FixRunner
	}
	var first error
	for _, cmd := range fw.RemoveCmds(ctx) {
		fmt.Fprintf(out, "+ %s\n", strings.Join(cmd, " "))
		if _, err := run(ctx, cmd[0], cmd[1:]...); err != nil && first == nil {
			first = err
		}
	}
	return first
}
