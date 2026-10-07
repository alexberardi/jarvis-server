package doctor

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Runner runs a command and returns its combined output (tests replace it).
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// HostFirewall returns the firewall checks for goos ("" = this OS), or nil when there are
// none.
func HostFirewall(goos string) Firewall {
	if goos == "" {
		goos = runtime.GOOS
	}
	exe, _ := os.Executable()
	switch goos {
	case "linux":
		return &linuxFirewall{ufw: &UFW{Root: "/"}, run: execRunner}
	case "darwin":
		return &macFirewall{exe: exe, run: execRunner}
	case "windows":
		return &windowsFirewall{exe: exe, run: execRunner}
	}
	return nil
}

// UFW reads ufw's own files, which are world-readable on stock installs, so no root is needed.
type UFW struct {
	Root string // filesystem root ("/"; tests use a temp dir)
}

func (u *UFW) read(p string) (string, error) {
	b, err := os.ReadFile(strings.TrimSuffix(u.Root, "/") + p)
	return string(b), err
}

// Active reports ufw.conf's ENABLED=yes.
func (u *UFW) Active() bool {
	s, err := u.read("/etc/ufw/ufw.conf")
	return err == nil && confValue(s, "ENABLED") == "yes"
}

func confValue(s, key string) string {
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(k) == key {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// Allows checks the default input policy and user.rules' "### tuple ###" lines.
func (u *UFW) Allows(p Port, lan *net.IPNet) (allowed, known bool) {
	if def, err := u.read("/etc/default/ufw"); err == nil && confValue(def, "DEFAULT_INPUT_POLICY") == "ACCEPT" {
		return true, true
	}
	rules, err := u.read("/etc/ufw/user.rules")
	if err != nil {
		return false, false
	}
	return ufwAllows(rules, p, lan), true
}

// ufwAllows matches tuple lines: "### tuple ### allow tcp 7700:7712,7031 0.0.0.0/0 any
// 10.0.0.0/24 in [comment=…]". Fields: action proto dport dst sport src direction. Only the
// first matching rule counts, as in ufw; a deny first wins.
func ufwAllows(rules string, p Port, lan *net.IPNet) bool {
	for _, line := range strings.Split(rules, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "### tuple ###")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 7 || !strings.HasPrefix(f[6], "in") {
			continue
		}
		action, proto, dport, src := f[0], f[1], f[2], f[5]
		if proto != "any" && proto != p.Proto {
			continue
		}
		if dport != "any" && !portIn(dport, p.Port) {
			continue
		}
		if !covers(src, lan) {
			continue
		}
		return action == "allow" || action == "limit"
	}
	return false
}

// portIn matches "7700", "7700:7712" or a comma list of those.
func portIn(spec string, port int) bool {
	for _, part := range strings.Split(spec, ",") {
		lo, hi, isRange := strings.Cut(part, ":")
		a, err := strconv.Atoi(lo)
		if err != nil {
			continue
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil {
				continue
			}
		}
		if port >= a && port <= b {
			return true
		}
	}
	return false
}

// covers says whether a rule source admits the whole LAN.
func covers(src string, lan *net.IPNet) bool {
	if src == "any" || src == "0.0.0.0/0" {
		return true
	}
	if !strings.Contains(src, "/") {
		return false // a single host is not the LAN
	}
	_, n, err := net.ParseCIDR(src)
	if err != nil {
		return false
	}
	lanOnes, _ := lan.Mask.Size()
	ones, _ := n.Mask.Size()
	return ones <= lanOnes && n.Contains(lan.IP)
}

type linuxFirewall struct {
	ufw *UFW
	run Runner
}

func (l *linuxFirewall) Name(ctx context.Context) string {
	if l.ufw.Active() {
		return "ufw"
	}
	if out, err := l.run(ctx, "firewall-cmd", "--state"); err == nil && strings.TrimSpace(string(out)) == "running" {
		return "firewalld"
	}
	return ""
}

func (l *linuxFirewall) Allows(ctx context.Context, p Port, lan *net.IPNet) (bool, bool) {
	if l.ufw.Active() {
		return l.ufw.Allows(p, lan)
	}
	out, err := l.run(ctx, "firewall-cmd", "--list-ports")
	if err != nil {
		return false, false
	}
	for _, f := range strings.Fields(string(out)) {
		ports, proto, _ := strings.Cut(f, "/")
		if proto == p.Proto && portIn(strings.ReplaceAll(ports, "-", ":"), p.Port) {
			return true, true
		}
	}
	return false, true
}

func (l *linuxFirewall) FixFor(ports []Port, lan *net.IPNet) string {
	if l.ufw.Active() {
		var cmds []string
		for _, proto := range []string{"tcp", "udp"} {
			if spec := ufwPortSpec(ports, proto); spec != "" {
				cmds = append(cmds, fmt.Sprintf("sudo ufw allow from %s to any port %s proto %s comment jarvisd", lan, spec, proto))
			}
		}
		return strings.Join(cmds, "\n")
	}
	var cmds []string
	for _, p := range ports {
		cmds = append(cmds, fmt.Sprintf("sudo firewall-cmd --permanent --add-rich-rule='rule family=ipv4 source address=%s port port=%d protocol=%s accept'", lan, p.Port, p.Proto))
	}
	return strings.Join(append(cmds, "sudo firewall-cmd --reload"), "\n")
}

// ufwPortSpec is a ufw multiport list for one protocol (ufw takes at most 15 entries).
func ufwPortSpec(ports []Port, proto string) string {
	var s []string
	for _, p := range ports {
		if p.Proto == proto && len(s) < 15 {
			s = append(s, strconv.Itoa(p.Port))
		}
	}
	return strings.Join(s, ",")
}

// macFirewall is the macOS application firewall: it admits or blocks programs, not ports.
type macFirewall struct {
	exe string
	run Runner
}

const socketfilterfw = "/usr/libexec/ApplicationFirewall/socketfilterfw"

func (m *macFirewall) Name(ctx context.Context) string {
	out, err := m.run(ctx, socketfilterfw, "--getglobalstate")
	if err == nil && strings.Contains(strings.ToLower(string(out)), "enabled") {
		return "the macOS firewall"
	}
	return ""
}

func (m *macFirewall) Allows(ctx context.Context, _ Port, _ *net.IPNet) (bool, bool) {
	if out, err := m.run(ctx, socketfilterfw, "--getblockall"); err == nil && strings.Contains(strings.ToLower(string(out)), "enabled") {
		return false, true // "block all incoming connections" overrides per-app permits
	}
	out, err := m.run(ctx, socketfilterfw, "--getappblocked", m.exe)
	if err != nil {
		return false, false
	}
	s := strings.ToLower(string(out))
	switch {
	case strings.Contains(s, "permitted"):
		return true, true
	case strings.Contains(s, "blocked"), strings.Contains(s, "not part of the firewall"):
		return false, true
	}
	return false, false
}

func (m *macFirewall) FixFor([]Port, *net.IPNet) string {
	return fmt.Sprintf("sudo %s --add %q\nsudo %s --unblockapp %q\n(and turn off \"Block all incoming connections\" if it is on)",
		socketfilterfw, m.exe, socketfilterfw, m.exe)
}

// windowsFirewall is Windows Defender Firewall: jarvisd needs an inbound allow rule for its
// program on the private profile (Windows asks on first listen; "Cancel" there blocks it).
type windowsFirewall struct {
	exe string
	run Runner
}

func (w *windowsFirewall) Name(ctx context.Context) string {
	out, err := w.run(ctx, "netsh", "advfirewall", "show", "currentprofile", "state")
	if err == nil && strings.Contains(strings.ToUpper(string(out)), " ON") {
		return "Windows Defender Firewall"
	}
	return ""
}

func (w *windowsFirewall) Allows(ctx context.Context, _ Port, _ *net.IPNet) (bool, bool) {
	out, err := w.run(ctx, "netsh", "advfirewall", "firewall", "show", "rule", "name=all", "dir=in", "verbose")
	if err != nil {
		return false, false
	}
	return windowsAllows(string(out), w.exe), true
}

// windowsAllows scans netsh's verbose rule blocks for an enabled inbound allow rule on exe.
// Blocks are separated by blank lines; a block rule on the program wins.
func windowsAllows(out, exe string) bool {
	allowed := false
	for _, block := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n\n") {
		fields := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			k, v, ok := strings.Cut(line, ":")
			if ok {
				fields[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		if !strings.EqualFold(fields["Program"], exe) || !strings.EqualFold(fields["Enabled"], "Yes") {
			continue
		}
		if strings.EqualFold(fields["Action"], "Block") {
			return false
		}
		if strings.EqualFold(fields["Action"], "Allow") {
			allowed = true
		}
	}
	return allowed
}

func (w *windowsFirewall) FixFor([]Port, *net.IPNet) string {
	return fmt.Sprintf(`netsh advfirewall firewall add rule name="jarvisd" dir=in action=allow program="%s" enable=yes profile=private`+
		"\n(from an Administrator prompt)", w.exe)
}
