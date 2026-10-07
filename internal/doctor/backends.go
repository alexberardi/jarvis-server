package doctor

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
)

// Tag marks every firewall rule jarvisd adds: the ufw comment, the firewalld service name
// and the Windows rule name. Uninstall removes what carries it.
const Tag = "jarvisd"

// memo caches a runner's answers for the life of one firewall inspection: a check asks the
// same question once per port and subnet.
func memo(run Runner) Runner {
	type result struct {
		out []byte
		err error
	}
	var mu sync.Mutex
	cache := map[string]result{}
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		key := name + "\x00" + strings.Join(args, "\x00")
		mu.Lock()
		r, ok := cache[key]
		mu.Unlock()
		if ok {
			return r.out, r.err
		}
		out, err := run(ctx, name, args...)
		mu.Lock()
		cache[key] = result{out, err}
		mu.Unlock()
		return out, err
	}
}

// linuxFirewall is ufw when it is enabled, else firewalld when it runs.
type linuxFirewall struct {
	ufw *UFW
	run Runner
}

func (l *linuxFirewall) Name(ctx context.Context) string {
	if l.ufw.Active() {
		return "ufw"
	}
	if l.firewalld(ctx) {
		return "firewalld"
	}
	return ""
}

func (l *linuxFirewall) firewalld(ctx context.Context) bool {
	out, err := l.run(ctx, "firewall-cmd", "--state")
	return err == nil && strings.TrimSpace(string(out)) == "running"
}

func (l *linuxFirewall) Allows(ctx context.Context, p Port, lan *net.IPNet) (bool, bool) {
	if l.ufw.Active() {
		return l.ufw.Allows(p, lan)
	}
	out, err := l.run(ctx, "firewall-cmd", "--list-ports")
	if err != nil {
		return false, false
	}
	if portListed(string(out), p) {
		return true, true
	}
	rich, err := l.run(ctx, "firewall-cmd", "--list-rich-rules")
	if err != nil {
		return false, true
	}
	for _, rule := range strings.Split(string(rich), "\n") {
		if !strings.HasSuffix(strings.TrimSpace(rule), "accept") || !covers(richAttr(rule, "source address"), lan) {
			continue
		}
		if port := richAttr(rule, "port port"); port != "" && richAttr(rule, "protocol") == p.Proto &&
			portIn(strings.ReplaceAll(port, "-", ":"), p.Port) {
			return true, true
		}
		if richAttr(rule, "service name") == Tag && l.servicePort(ctx, p) {
			return true, true
		}
	}
	return false, true
}

// portListed matches p in a firewalld port list ("7700-7712/tcp 1884/tcp").
func portListed(list string, p Port) bool {
	for _, f := range strings.Fields(list) {
		ports, proto, _ := strings.Cut(f, "/")
		if proto == p.Proto && portIn(strings.ReplaceAll(ports, "-", ":"), p.Port) {
			return true
		}
	}
	return false
}

// servicePort reports whether firewalld's jarvisd service lists p.
func (l *linuxFirewall) servicePort(ctx context.Context, p Port) bool {
	out, err := l.run(ctx, "firewall-cmd", "--info-service="+Tag)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ports:"); ok && portListed(v, p) {
			return true
		}
	}
	return false
}

// richAttr is the value of key="value" in a firewalld rich rule ("" when absent).
func richAttr(rule, key string) string {
	_, rest, ok := strings.Cut(rule, key+`="`)
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(rest, `"`)
	return v
}

func (l *linuxFirewall) FixCmds(ctx context.Context, ports []Port, lan *net.IPNet) [][]string {
	if l.ufw.Active() {
		var cmds [][]string
		for _, proto := range []string{"tcp", "udp"} {
			if spec := ufwPortSpec(ports, proto); spec != "" {
				cmds = append(cmds, []string{"ufw", "allow", "from", lan.String(), "to", "any", "port", spec, "proto", proto, "comment", Tag})
			}
		}
		return cmds
	}
	// firewalld: the ports go into a jarvisd service, admitted from the LAN by one rich rule
	// that names it, so uninstall finds both by name.
	var cmds [][]string
	if out, err := l.run(ctx, "firewall-cmd", "--permanent", "--get-services"); err != nil || !hasField(string(out), Tag) {
		cmds = append(cmds, []string{"firewall-cmd", "--permanent", "--new-service=" + Tag})
	}
	for _, p := range ports {
		cmds = append(cmds, []string{"firewall-cmd", "--permanent", "--service=" + Tag, fmt.Sprintf("--add-port=%d/%s", p.Port, p.Proto)})
	}
	return append(cmds,
		[]string{"firewall-cmd", "--permanent", "--add-rich-rule=" + firewalldRule(lan)},
		[]string{"firewall-cmd", "--reload"})
}

func firewalldRule(lan *net.IPNet) string {
	return fmt.Sprintf(`rule family="ipv4" source address="%s" service name="%s" accept`, lan, Tag)
}

func hasField(s, f string) bool {
	for _, x := range strings.Fields(s) {
		if x == f {
			return true
		}
	}
	return false
}

func (l *linuxFirewall) FixNote() string { return "" }

func (l *linuxFirewall) RemoveCmds(ctx context.Context) [][]string {
	var cmds [][]string
	if rules, err := l.ufw.read("/etc/ufw/user.rules"); err == nil {
		cmds = append(cmds, ufwRemoveCmds(rules)...)
	}
	if !l.firewalld(ctx) {
		return cmds
	}
	var fw [][]string
	if out, err := l.run(ctx, "firewall-cmd", "--permanent", "--list-rich-rules"); err == nil {
		for _, rule := range strings.Split(string(out), "\n") {
			if rule = strings.TrimSpace(rule); rule != "" && richAttr(rule, "service name") == Tag {
				fw = append(fw, []string{"firewall-cmd", "--permanent", "--remove-rich-rule=" + rule})
			}
		}
	}
	if out, err := l.run(ctx, "firewall-cmd", "--permanent", "--get-services"); err == nil && hasField(string(out), Tag) {
		fw = append(fw, []string{"firewall-cmd", "--permanent", "--delete-service=" + Tag})
	}
	if len(fw) > 0 {
		fw = append(fw, []string{"firewall-cmd", "--reload"})
	}
	return append(cmds, fw...)
}

// ufwTagComment is the tag as ufw stores a rule comment in user.rules (hex).
var ufwTagComment = "comment=" + hex.EncodeToString([]byte(Tag))

// ufwRemoveCmds deletes each tuple carrying the jarvisd comment, by its rule spec (ufw
// matches a delete on everything but the comment).
func ufwRemoveCmds(rules string) [][]string {
	var cmds [][]string
	for _, line := range strings.Split(rules, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "### tuple ###")
		if !ok || !hasField(rest, ufwTagComment) {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 7 {
			continue
		}
		action, proto, dport, src := f[0], f[1], f[2], f[5]
		cmd := []string{"ufw", "delete", action, "from", src, "to", "any"}
		if dport != "any" {
			cmd = append(cmd, "port", dport)
		}
		if proto != "any" {
			cmd = append(cmd, "proto", proto)
		}
		cmds = append(cmds, cmd)
	}
	return cmds
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

func (m *macFirewall) FixCmds(context.Context, []Port, *net.IPNet) [][]string {
	return [][]string{{socketfilterfw, "--add", m.exe}, {socketfilterfw, "--unblockapp", m.exe}}
}

func (m *macFirewall) FixNote() string {
	return `(and turn off "Block all incoming connections" if it is on)`
}

func (m *macFirewall) RemoveCmds(ctx context.Context) [][]string {
	out, err := m.run(ctx, socketfilterfw, "--getappblocked", m.exe)
	if err != nil || strings.Contains(strings.ToLower(string(out)), "not part of the firewall") {
		return nil
	}
	return [][]string{{socketfilterfw, "--remove", m.exe}}
}

// windowsFirewall is Windows Defender Firewall: jarvisd needs an inbound allow rule for its
// program on the private profile (Windows asks on first listen; "Cancel" there blocks it).
// netsh gets its arguments verbatim (command_windows.go), so the program path is quoted in
// place.
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

func (w *windowsFirewall) rules(ctx context.Context) (string, error) {
	out, err := w.run(ctx, "netsh", "advfirewall", "firewall", "show", "rule", "name=all", "dir=in", "verbose")
	return string(out), err
}

func (w *windowsFirewall) Allows(ctx context.Context, _ Port, _ *net.IPNet) (bool, bool) {
	out, err := w.rules(ctx)
	if err != nil {
		return false, false
	}
	return windowsAllows(out, w.exe), true
}

// windowsRules parses netsh's verbose rule listing into one field map per rule. Blocks are
// separated by blank lines.
func windowsRules(out string) []map[string]string {
	var rules []map[string]string
	for _, block := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n\n") {
		fields := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			k, v, ok := strings.Cut(line, ":")
			if ok {
				fields[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		rules = append(rules, fields)
	}
	return rules
}

// windowsAllows scans for an enabled inbound allow rule on exe; a block rule on the program
// wins.
func windowsAllows(out, exe string) bool {
	allowed := false
	for _, r := range windowsRules(out) {
		if !strings.EqualFold(r["Program"], exe) || !strings.EqualFold(r["Enabled"], "Yes") {
			continue
		}
		if strings.EqualFold(r["Action"], "Block") {
			return false
		}
		if strings.EqualFold(r["Action"], "Allow") {
			allowed = true
		}
	}
	return allowed
}

// FixCmds replaces every inbound rule on jarvisd's program (including a block rule left by
// "Cancel" on Windows' first-run prompt, which beats any allow) with one allow rule named
// jarvisd.
func (w *windowsFirewall) FixCmds(ctx context.Context, _ []Port, _ *net.IPNet) [][]string {
	program := fmt.Sprintf(`program="%s"`, w.exe)
	var cmds [][]string
	if out, err := w.rules(ctx); err == nil {
		for _, r := range windowsRules(out) {
			if strings.EqualFold(r["Program"], w.exe) {
				cmds = append(cmds, []string{"netsh", "advfirewall", "firewall", "delete", "rule", "name=all", "dir=in", program})
				break
			}
		}
	}
	return append(cmds, []string{"netsh", "advfirewall", "firewall", "add", "rule", "name=" + Tag, "dir=in", "action=allow",
		program, "enable=yes", "profile=private"})
}

func (w *windowsFirewall) FixNote() string { return "" }

func (w *windowsFirewall) RemoveCmds(ctx context.Context) [][]string {
	if _, err := w.run(ctx, "netsh", "advfirewall", "firewall", "show", "rule", "name="+Tag); err != nil {
		return nil // "No rules match the specified criteria."
	}
	return [][]string{{"netsh", "advfirewall", "firewall", "delete", "rule", "name=" + Tag}}
}

// RenderFix is the fix as an operator types it: `sudo` and shell quoting on unix, verbatim
// for an Administrator prompt on Windows; then the note.
func RenderFix(goos string, cmds [][]string, note string) string {
	var lines []string
	for _, c := range cmds {
		if goos == "windows" {
			lines = append(lines, strings.Join(c, " "))
		} else {
			lines = append(lines, "sudo "+shellJoin(c))
		}
	}
	if goos == "windows" && len(cmds) > 0 {
		lines = append(lines, "(from an Administrator prompt)")
	}
	if note != "" {
		lines = append(lines, note)
	}
	return strings.Join(lines, "\n")
}

func shellJoin(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = shellQuote(a)
	}
	return strings.Join(out, " ")
}

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./:,=@%+-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
