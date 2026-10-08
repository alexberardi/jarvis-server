package doctor

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// fakeRun answers commands from a table keyed by the joined command line; anything else
// fails like a missing program.
func fakeRun(answers map[string]string) Runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		if out, ok := answers[key]; ok {
			return []byte(out), nil
		}
		return nil, errors.New("not found: " + key)
	}
}

func TestUFWRemoveCmds(t *testing.T) {
	// user.rules after `jarvisd doctor --fix` (ufw stores the comment hex encoded) next to a
	// rule the user made, which must stay.
	rules := `
### tuple ### allow tcp 22 0.0.0.0/0 any 10.0.0.0/24 in comment=7373682066726f6d204c414e
### tuple ### allow tcp 7700,7703,7031,1884,9883 0.0.0.0/0 any 10.0.0.0/24 in comment=6a617276697364
### tuple ### allow udp 5353 0.0.0.0/0 any 10.0.0.0/24 in comment=6a617276697364
`
	got := ufwRemoveCmds(rules)
	want := [][]string{
		{"ufw", "delete", "allow", "from", "10.0.0.0/24", "to", "any", "port", "7700,7703,7031,1884,9883", "proto", "tcp"},
		{"ufw", "delete", "allow", "from", "10.0.0.0/24", "to", "any", "port", "5353", "proto", "udp"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
	// What the fix adds is what removal finds: the fix's argv round-trips through the tuple.
	fw := &linuxFirewall{ufw: writeUFW(t, "yes", "DROP", rules), run: noFirewalld}
	if cmds := fw.RemoveCmds(context.Background()); len(cmds) != 2 {
		t.Errorf("remove %q", cmds)
	}
}

func TestFirewalldServiceRule(t *testing.T) {
	home := lan(t, "10.0.0.0/24")
	run := fakeRun(map[string]string{
		"firewall-cmd --state":                       "running\n",
		"firewall-cmd --list-ports":                  "\n",
		"firewall-cmd --list-rich-rules":             `rule family="ipv4" source address="10.0.0.0/24" service name="jarvisd" accept` + "\n",
		"firewall-cmd --info-service=jarvisd":        "jarvisd\n  ports: 7700/tcp 5353/udp\n  protocols: \n",
		"firewall-cmd --permanent --get-services":    "ssh dhcpv6-client jarvisd\n",
		"firewall-cmd --permanent --list-rich-rules": `rule family="ipv4" source address="10.0.0.0/24" service name="jarvisd" accept` + "\n" + `rule family="ipv4" source address="10.0.0.5" port port="22" protocol="tcp" accept` + "\n",
	})
	fw := &linuxFirewall{ufw: &UFW{Root: t.TempDir()}, run: run}
	ctx := context.Background()
	if ok, known := fw.Allows(ctx, Port{"config", 7700, "tcp"}, home); !ok || !known {
		t.Error("7700 is in the jarvisd service")
	}
	if ok, _ := fw.Allows(ctx, Port{"cc", 7703, "tcp"}, home); ok {
		t.Error("7703 is not")
	}
	if ok, _ := fw.Allows(ctx, Port{"config", 7700, "tcp"}, lan(t, "192.168.0.0/16")); ok {
		t.Error("the rule's source doesn't cover another LAN")
	}
	// The service exists already: no --new-service.
	if cmds := fw.FixCmds(ctx, []Port{{"cc", 7703, "tcp"}}, home); cmds[0][2] != "--service=jarvisd" {
		t.Errorf("fix %q", cmds)
	}
	want := [][]string{
		{"firewall-cmd", "--permanent", `--remove-rich-rule=rule family="ipv4" source address="10.0.0.0/24" service name="jarvisd" accept`},
		{"firewall-cmd", "--permanent", "--delete-service=jarvisd"},
		{"firewall-cmd", "--reload"},
	}
	if got := fw.RemoveCmds(ctx); !reflect.DeepEqual(got, want) {
		t.Errorf("remove %q", got)
	}
}

func TestMacFixAndRemove(t *testing.T) {
	exe := "/usr/local/bin/jarvisd"
	listed := &macFirewall{exe: exe, run: fakeRun(map[string]string{
		socketfilterfw + " --getappblocked " + exe: "The application /usr/local/bin/jarvisd is permitted\n",
	})}
	if got := listed.RemoveCmds(context.Background()); !reflect.DeepEqual(got, [][]string{{socketfilterfw, "--remove", exe}}) {
		t.Errorf("remove %q", got)
	}
	unlisted := &macFirewall{exe: exe, run: fakeRun(map[string]string{
		socketfilterfw + " --getappblocked " + exe: "The application /usr/local/bin/jarvisd is not part of the firewall\n",
	})}
	if got := unlisted.RemoveCmds(context.Background()); got != nil {
		t.Errorf("nothing to remove: %q", got)
	}
	fix := RenderFix("darwin", listed.FixCmds(context.Background(), nil, nil), listed.FixNote())
	if !strings.HasPrefix(fix, "sudo "+socketfilterfw+" --add /usr/local/bin/jarvisd\n") || !strings.Contains(fix, "Block all incoming") {
		t.Errorf("fix %q", fix)
	}
}

func TestWindowsFixAndRemove(t *testing.T) {
	exe := `C:\Program Files\jarvisd\jarvisd.exe`
	blocked := "Rule Name:                            jarvisd.exe\r\nEnabled:                              Yes\r\n" +
		"Program:                              " + exe + "\r\nAction:                               Block\r\n"
	w := &windowsFirewall{exe: exe, run: fakeRun(map[string]string{
		"netsh advfirewall firewall show rule name=all dir=in verbose": blocked,
		"netsh advfirewall firewall show rule name=jarvisd":            "Rule Name: jarvisd\r\n",
	})}
	ctx := context.Background()
	got := w.FixCmds(ctx, nil, nil)
	want := [][]string{
		{"netsh", "advfirewall", "firewall", "delete", "rule", "name=all", "dir=in", `program="` + exe + `"`},
		{"netsh", "advfirewall", "firewall", "add", "rule", "name=jarvisd", "dir=in", "action=allow", `program="` + exe + `"`, "enable=yes", "profile=private"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fix %q", got)
	}
	if fix := RenderFix("windows", got[1:], ""); fix != `netsh advfirewall firewall add rule name=jarvisd dir=in action=allow program="`+exe+`" enable=yes profile=private`+"\n(from an Administrator prompt)" {
		t.Errorf("rendered %q", fix)
	}
	if got := w.RemoveCmds(ctx); !reflect.DeepEqual(got, [][]string{{"netsh", "advfirewall", "firewall", "delete", "rule", "name=jarvisd"}}) {
		t.Errorf("remove %q", got)
	}
	none := &windowsFirewall{exe: exe, run: fakeRun(nil)}
	if got := none.RemoveCmds(ctx); got != nil {
		t.Errorf("no rule, nothing to remove: %q", got)
	}
	if got := none.FixCmds(ctx, nil, nil); len(got) != 1 {
		t.Errorf("no existing rules: just the add, got %q", got)
	}
}

func TestPrivateLANOnly(t *testing.T) {
	for s, want := range map[string]bool{
		"10.0.0.0/24": true, "192.168.1.0/24": true, "172.16.0.0/12": true, "10.0.0.0/8": true,
		"0.0.0.0/0": false, "8.8.8.0/24": false, "10.0.0.0/7": false,
	} {
		if got := PrivateLAN(lan(t, s)); got != want {
			t.Errorf("%s: %v", s, got)
		}
	}
	// A public "LAN" gets the finding but no commands to apply.
	fw := &linuxFirewall{ufw: writeUFW(t, "yes", "DROP", ""), run: noFirewalld}
	c := Run(context.Background(), opts(fw, []*net.IPNet{lan(t, "8.8.8.0/24")}))[1]
	if c.Status != Fail || c.FixCmds != nil {
		t.Errorf("public subnet %+v", c)
	}
}

func TestApply(t *testing.T) {
	var ran []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		if name == "fail" {
			return nil, errors.New("boom")
		}
		return nil, nil
	}
	checks := []Check{
		{Name: "firewall a", FixCmds: [][]string{{"x", "1"}, {"shared"}}},
		{Name: "firewall b", FixCmds: [][]string{{"shared"}, {"y", "2"}}},
	}
	var out bytes.Buffer
	n, err := Apply(context.Background(), checks, run, &out)
	if err != nil || n != 3 || !reflect.DeepEqual(ran, []string{"x 1", "shared ", "y 2"}) {
		t.Errorf("n=%d err=%v ran=%q", n, err, ran)
	}
	if !strings.Contains(out.String(), "+ x 1\n") {
		t.Errorf("output %q", out.String())
	}
	ran = nil
	if n, err := Apply(context.Background(), []Check{{FixCmds: [][]string{{"fail"}, {"never"}}}}, run, &out); err == nil || n != 0 || len(ran) != 1 {
		t.Errorf("stops at the first failure: n=%d err=%v ran=%q", n, err, ran)
	}
}

func TestPortsHeldByAnotherProgram(t *testing.T) {
	o := opts(nil, []*net.IPNet{})
	o.Firewall = &linuxFirewall{ufw: &UFW{Root: t.TempDir()}, run: noFirewalld}
	// The legacy stack: uvicorn answers on the HTTP ports, mosquitto on the broker's.
	o.ServerHeader = func(context.Context, string) (string, error) { return "uvicorn", nil }
	checks := Run(context.Background(), o)
	c := find(t, checks, "ports")
	if c.Status != Fail || !strings.Contains(c.Detail, "config (7700)") || !strings.Contains(c.Detail, "mqtt (1884)") ||
		!strings.Contains(c.Fix, "ss -ltnp 'sport = :7700'") {
		t.Errorf("ports %+v", c)
	}
	for _, c := range checks {
		if c.Name == "listening" {
			t.Errorf("no listening verdict when something else answers: %+v", c)
		}
	}
	// jarvisd itself: its HTTP listeners answer with its header, so the broker's ports are its.
	o.ServerHeader = jarvisdHeader
	if c := Run(context.Background(), o)[0]; c.Name != "listening" || c.Status != OK {
		t.Errorf("jarvisd %+v", c)
	}
}

func TestOSChecks(t *testing.T) {
	public := "\r\nPublic Profile Settings:\r\n----------------------------------------------------------------------\r\nState                                 ON\r\n"
	if c := windowsProfile(public); c[0].Status != Warn || !strings.Contains(c[0].Fix, "NetworkCategory Private") {
		t.Errorf("public %+v", c)
	}
	if c := windowsProfile(strings.Replace(public, "Public", "Private", 1)); c[0].Status != OK {
		t.Errorf("private %+v", c)
	}
	pmset := "System-wide power settings:\nCurrently in use:\n standby              1\n sleep                10 (sleep prevented by sharingd)\n displaysleep         10\n"
	if c := macSleep(pmset); c[0].Status != Warn || c[0].Fix != "sudo pmset -a sleep 0" {
		t.Errorf("sleep %+v", c)
	}
	if c := macSleep(strings.Replace(pmset, "sleep                10", "sleep                0", 1)); c[0].Status != OK {
		t.Errorf("no sleep %+v", c)
	}
	o := opts(nil, []*net.IPNet{})
	o.GOOS = "darwin"
	o.Firewall = &macFirewall{run: fakeRun(nil)}
	o.Run = fakeRun(map[string]string{"pmset -g": pmset})
	if c := find(t, Run(context.Background(), o), "sleep"); c.Status != Warn {
		t.Errorf("run %+v", c)
	}
}

func TestLegacy(t *testing.T) {
	dir := t.TempDir()
	o := opts(nil, []*net.IPNet{})
	o.Firewall = &linuxFirewall{ufw: &UFW{Root: t.TempDir()}, run: noFirewalld}
	o.LegacyDirs = []string{filepath.Join(dir, ".jarvis")}
	ps := "docker ps --format " + dockerPS
	// llama-server is in the legacy Compose project without a jarvis- name.
	o.Run = fakeRun(map[string]string{ps: "a1;jarvis-auth;0.0.0.0:7701->7701/tcp, :::7701->7701/tcp;jarvis;/home/j/.jarvis/compose\n" +
		"a2;jarvis-config-service;0.0.0.0:7700->7700/tcp;;\n" +
		"a3;llama-server;;jarvis;/home/j/.jarvis/compose\n" +
		"a4;home-assistant;;;\n"})
	c := find(t, Run(context.Background(), o), "legacy stack")
	if c.Status != Warn || !strings.Contains(c.Fix, "docker update --restart=no jarvis-auth jarvis-config-service llama-server\n") ||
		!strings.Contains(c.Fix, "systemctl --user disable --now jarvis-admin.service") ||
		strings.Contains(c.Fix, "home-assistant") || strings.Contains(c.Fix, "down") {
		t.Errorf("running %+v", c)
	}
	// Only its infrastructure runs, on other ports: no conflict.
	o.Run = fakeRun(map[string]string{ps: "b1;jarvis-postgres;0.0.0.0:5432->5432/tcp;;\nb2;jarvis-redis;;;\n"})
	if c := find(t, Run(context.Background(), o), "legacy stack"); c.Status != OK || !strings.Contains(c.Detail, "jarvis-postgres, jarvis-redis") {
		t.Errorf("infra only %+v", c)
	}
	// A host-network stack publishes nothing, but holds the ports.
	o.ServerHeader = func(context.Context, string) (string, error) { return "uvicorn", nil }
	o.Run = fakeRun(map[string]string{ps: "c1;jarvis-config-service;;;\n"})
	if c := find(t, Run(context.Background(), o), "legacy stack"); c.Status != Warn {
		t.Errorf("host network %+v", c)
	}
	o.ServerHeader = jarvisdHeader
	// Stopped, with its files left: fine.
	if err := os.MkdirAll(filepath.Join(dir, ".jarvis", "compose"), 0o755); err != nil {
		t.Fatal(err)
	}
	o.Run = fakeRun(map[string]string{ps: "d1;home-assistant;;;\n"})
	if c := find(t, Run(context.Background(), o), "legacy stack"); c.Status != OK || !strings.Contains(c.Detail, "none of its containers run") {
		t.Errorf("stopped %+v", c)
	}
}

func TestPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses the directory's ACL")
	}
	home := filepath.Join(t.TempDir(), "jarvisd")
	if c := permissions(home); c[0].Status != OK || !strings.Contains(c[0].Detail, "doesn't exist yet") {
		t.Errorf("missing %+v", c)
	}
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	os.Chmod(home, 0o755)
	if err := os.WriteFile(filepath.Join(home, "jarvis.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chmod(filepath.Join(home, "jarvis.db"), 0o644)
	c := permissions(home)
	if len(c) != 1 || c[0].Status != Warn || !strings.Contains(c[0].Detail, "is 0755") || !strings.Contains(c[0].Detail, "jarvis.db is 0644") {
		t.Errorf("wide %+v", c)
	}
	os.Chmod(home, 0o700)
	os.Chmod(filepath.Join(home, "jarvis.db"), 0o600)
	if c := permissions(home); len(c) != 1 || c[0].Status != OK {
		t.Errorf("tight %+v", c)
	}
	if c := permissions(""); c != nil {
		t.Errorf("no home, no check: %+v", c)
	}
}
