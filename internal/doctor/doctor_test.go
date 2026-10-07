package doctor

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lan(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

var jarvisPorts = []Port{
	{"config", 7700, "tcp"}, {"command-center", 7703, "tcp"}, {"ocr", 7031, "tcp"},
	{"mqtt", 1884, "tcp"}, {"mqtt-ws", 9883, "tcp"}, {"mdns", 5353, "udp"},
}

// jarvis-dev's real user.rules after the fix (comments are hex, as ufw writes them).
const devRules = `
### tuple ### allow udp 53317 0.0.0.0/0 any 0.0.0.0/0 in
### tuple ### allow tcp 22 0.0.0.0/0 any 10.0.0.0/24 in comment=7373682066726f6d204c414e
### tuple ### allow udp 5353 0.0.0.0/0 any 10.0.0.0/8 in comment=6f6d61726368792d73756e7368696e65
### tuple ### allow tcp 7700:7712 0.0.0.0/0 any 10.0.0.0/24 in
### tuple ### allow tcp 7031 0.0.0.0/0 any 10.0.0.0/24 in
### tuple ### allow tcp 1884 0.0.0.0/0 any 10.0.0.0/24 in
### tuple ### allow tcp 9883 0.0.0.0/0 any 10.0.0.0/24 in
`

func TestUFWRules(t *testing.T) {
	home := lan(t, "10.0.0.0/24")
	for _, p := range jarvisPorts {
		if !ufwAllows(devRules, p, home) {
			t.Errorf("%v should be allowed", p)
		}
	}
	if ufwAllows(devRules, Port{"x", 7713, "tcp"}, home) {
		t.Error("7713 is outside the range")
	}
	if ufwAllows(devRules, Port{"x", 7700, "udp"}, home) {
		t.Error("udp 7700 has no rule")
	}
	// A /24 rule doesn't admit a wider LAN; a single host never admits the LAN.
	if ufwAllows(devRules, Port{"x", 7700, "tcp"}, lan(t, "10.0.0.0/16")) {
		t.Error("10.0.0.0/24 rule must not cover 10.0.0.0/16")
	}
	if ufwAllows("### tuple ### allow tcp 7700 0.0.0.0/0 any 10.0.0.5 in", Port{"x", 7700, "tcp"}, home) {
		t.Error("single-host rule")
	}
	// Multiport and first-match-wins.
	multi := "### tuple ### deny tcp 7703 0.0.0.0/0 any 0.0.0.0/0 in\n### tuple ### allow tcp 7700:7712,1884 0.0.0.0/0 any 0.0.0.0/0 in"
	if !ufwAllows(multi, Port{"x", 1884, "tcp"}, home) || ufwAllows(multi, Port{"x", 7703, "tcp"}, home) {
		t.Error("multiport / deny-first")
	}
	// Outbound rules don't count.
	if ufwAllows("### tuple ### allow tcp 7700 0.0.0.0/0 any 0.0.0.0/0 out", Port{"x", 7700, "tcp"}, home) {
		t.Error("out rule")
	}
}

func writeUFW(t *testing.T, enabled, policy, rules string) *UFW {
	t.Helper()
	root := t.TempDir()
	for p, s := range map[string]string{
		"/etc/ufw/ufw.conf":   "# comment\nENABLED=" + enabled + "\nLOGLEVEL=low\n",
		"/etc/default/ufw":    `DEFAULT_INPUT_POLICY="` + policy + `"` + "\n",
		"/etc/ufw/user.rules": rules,
	} {
		if err := os.MkdirAll(filepath.Dir(root+p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(root+p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &UFW{Root: root}
}

func noFirewalld(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("not found")
}

func dialOK(context.Context, string, string) (net.Conn, error) {
	a, b := net.Pipe()
	b.Close()
	return a, nil
}

func jarvisdHeader(context.Context, string) (string, error) { return "jarvisd", nil }

// opts runs on linux with no external commands (no docker, no legacy dirs).
func opts(fw Firewall, lans []*net.IPNet) Options {
	return Options{Ports: jarvisPorts, LANs: lans, Firewall: fw, Dial: dialOK, ServerHeader: jarvisdHeader,
		GOOS: "linux", Run: noFirewalld, LegacyDirs: []string{}}
}

func find(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q check in %+v", name, checks)
	return Check{}
}

// The jarvis-dev incident: ufw on, default DROP, no jarvisd rules. jarvisd answered on
// localhost while the Pi's registration timed out.
func TestDoctorFlagsUFWBlockingTheLAN(t *testing.T) {
	home := lan(t, "10.0.0.0/24")
	fw := &linuxFirewall{ufw: writeUFW(t, "yes", "DROP", "### tuple ### allow tcp 22 0.0.0.0/0 any 10.0.0.0/24 in\n"), run: noFirewalld}
	checks := Run(context.Background(), opts(fw, []*net.IPNet{home}))
	if len(checks) != 3 || checks[0].Status != OK || checks[2].Name != "legacy stack" {
		t.Fatalf("checks %+v", checks)
	}
	c := checks[1]
	if c.Status != Fail || !strings.Contains(c.Detail, "7700/tcp") || !strings.Contains(c.Detail, "5353/udp") {
		t.Errorf("firewall check %+v", c)
	}
	want := "sudo ufw allow from 10.0.0.0/24 to any port 7700,7703,7031,1884,9883 proto tcp comment jarvisd\n" +
		"sudo ufw allow from 10.0.0.0/24 to any port 5353 proto udp comment jarvisd"
	if c.Fix != want {
		t.Errorf("fix:\n%s\nwant:\n%s", c.Fix, want)
	}
	if len(c.FixCmds) != 2 || strings.Join(c.FixCmds[1], " ") != "ufw allow from 10.0.0.0/24 to any port 5353 proto udp comment jarvisd" {
		t.Errorf("fix_cmds %q", c.FixCmds)
	}

	// After the fix: all clear.
	fw.ufw = writeUFW(t, "yes", "DROP", devRules)
	if c := Run(context.Background(), opts(fw, []*net.IPNet{home}))[1]; c.Status != OK {
		t.Errorf("after fix %+v", c)
	}
	// Default ACCEPT, or ufw off: nothing to do.
	fw.ufw = writeUFW(t, "yes", "ACCEPT", "")
	if c := Run(context.Background(), opts(fw, []*net.IPNet{home}))[1]; c.Status != OK {
		t.Errorf("accept policy %+v", c)
	}
	fw.ufw = writeUFW(t, "no", "DROP", "")
	if c := Run(context.Background(), opts(fw, []*net.IPNet{home}))[1]; c.Status != OK || c.Name != "firewall" {
		t.Errorf("ufw off %+v", c)
	}
	// Unreadable rules: a warning, not a verdict.
	fw.ufw = writeUFW(t, "yes", "DROP", "")
	os.Remove(fw.ufw.Root + "/etc/ufw/user.rules")
	if c := Run(context.Background(), opts(fw, []*net.IPNet{home}))[1]; c.Status != Warn || !strings.Contains(c.Detail, "sudo jarvisd doctor") {
		t.Errorf("unreadable %+v", c)
	}
}

func TestDoctorListening(t *testing.T) {
	dial := func(_ context.Context, _, addr string) (net.Conn, error) {
		if strings.HasSuffix(addr, ":7703") {
			return nil, errors.New("refused")
		}
		return dialOK(nil, "", "")
	}
	o := opts(&linuxFirewall{ufw: &UFW{Root: t.TempDir()}, run: noFirewalld}, []*net.IPNet{})
	o.Dial = dial
	c := Run(context.Background(), o)[0]
	if c.Status != Fail || !strings.Contains(c.Detail, "command-center (7703)") {
		t.Errorf("listening %+v", c)
	}
}

func TestFirewalld(t *testing.T) {
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch args[0] {
		case "--state":
			return []byte("running\n"), nil
		case "--list-ports":
			return []byte("7700-7712/tcp 1884/tcp\n"), nil
		}
		return nil, errors.New("?")
	}
	fw := &linuxFirewall{ufw: &UFW{Root: t.TempDir()}, run: run}
	home := lan(t, "192.168.1.0/24")
	if fw.Name(context.Background()) != "firewalld" {
		t.Fatal("name")
	}
	if ok, known := fw.Allows(context.Background(), Port{"cc", 7703, "tcp"}, home); !ok || !known {
		t.Error("7703 in range")
	}
	if ok, _ := fw.Allows(context.Background(), Port{"ws", 9883, "tcp"}, home); ok {
		t.Error("9883 not listed")
	}
	fix := RenderFix("linux", fw.FixCmds(context.Background(), []Port{{"ws", 9883, "tcp"}}, home), fw.FixNote())
	want := "sudo firewall-cmd --permanent --new-service=jarvisd\n" +
		"sudo firewall-cmd --permanent --service=jarvisd --add-port=9883/tcp\n" +
		`sudo firewall-cmd --permanent '--add-rich-rule=rule family="ipv4" source address="192.168.1.0/24" service name="jarvisd" accept'` + "\n" +
		"sudo firewall-cmd --reload"
	if fix != want {
		t.Errorf("fix:\n%s\nwant:\n%s", fix, want)
	}
}

func TestMacAndWindowsFirewalls(t *testing.T) {
	mac := &macFirewall{exe: "/usr/local/bin/jarvisd", run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[0] {
		case "--getglobalstate":
			return []byte("Firewall is enabled. (State = 1)\n"), nil
		case "--getblockall":
			return []byte("Firewall has block all state set to disabled.\n"), nil
		case "--getappblocked":
			return []byte("The application /usr/local/bin/jarvisd is not part of the firewall\n"), nil
		}
		return nil, errors.New("?")
	}}
	if mac.Name(context.Background()) == "" {
		t.Fatal("mac name")
	}
	if ok, known := mac.Allows(context.Background(), Port{}, nil); ok || !known {
		t.Error("unknown app should be blocked")
	}

	const netsh = "Rule Name:                            jarvisd\r\n" +
		"----------------------------------------------------------------------\r\n" +
		"Enabled:                              Yes\r\nDirection:                            In\r\n" +
		"Program:                              C:\\Jarvis\\jarvisd.exe\r\nAction:                               Allow\r\n\r\n" +
		"Rule Name:                            other\r\nEnabled:                              Yes\r\n" +
		"Program:                              C:\\Other\\x.exe\r\nAction:                               Allow\r\n"
	if !windowsAllows(netsh, `C:\Jarvis\jarvisd.exe`) {
		t.Error("allow rule not found")
	}
	if windowsAllows(netsh, `C:\Elsewhere\jarvisd.exe`) {
		t.Error("other program's rule")
	}
	blocked := netsh + "\r\nRule Name:                            jarvisd.exe\r\nEnabled:                              Yes\r\n" +
		"Program:                              C:\\Jarvis\\jarvisd.exe\r\nAction:                               Block\r\n"
	if windowsAllows(blocked, `C:\Jarvis\jarvisd.exe`) {
		t.Error("a block rule (from Cancel on the first-run prompt) wins")
	}
}
