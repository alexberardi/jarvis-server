package doctor

import (
	"context"
	"net"
	"os"
	"path/filepath"
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
	return command(ctx, name, args...).CombinedOutput()
}

// HostFirewall returns the firewall checks for goos ("" = this OS), or nil when there are
// none.
func HostFirewall(goos string) Firewall {
	if goos == "" {
		goos = runtime.GOOS
	}
	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	switch goos {
	case "linux":
		return &linuxFirewall{ufw: &UFW{Root: "/"}, run: memo(execRunner)}
	case "darwin":
		return &macFirewall{exe: exe, run: memo(execRunner)}
	case "windows":
		return &windowsFirewall{exe: exe, run: memo(execRunner)}
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
