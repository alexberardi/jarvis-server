package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The L2 target's scenarios: one call each way and the lockout path (loop #490), each once.
func TestL2Scenarios(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range l2Scenarios {
		if seen[s.Name] {
			t.Errorf("scenario %s twice", s.Name)
		}
		seen[s.Name] = true
		if s.Run == nil || s.What == "" {
			t.Errorf("scenario %s: no Run or What", s.Name)
		}
	}
	for _, n := range []string{"l2-registers", "l2-outbound", "l2-inbound", "l2-lockout"} {
		if !seen[n] {
			t.Errorf("no scenario %s", n)
		}
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"l2", "--list"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "l2-lockout") || strings.Contains(out.String(), "wrong-password-ban") {
		t.Errorf("siplane l2 --list lists the wrong scenarios:\n%s", out.String())
	}
}

// useL2 moves the registrar, and every URI a far end dials, to the VM's forwarded SIP port,
// without touching the L1 lane's own env.
func TestUseL2(t *testing.T) {
	l1 := map[string]string{"SIP_LANE_SIP": "127.0.0.1:15060", "SIP_LANE_OFFSET": "10000"}
	l := &lane{env: l1}
	if err := l.useL2(l2VM{sip: "127.0.0.1:5260", ssh: "127.0.0.1:2247"}); err != nil {
		t.Fatal(err)
	}
	if got := l.uri("5551230000"); got != "sip:5551230000@127.0.0.1:5260" {
		t.Errorf("uri = %s, want the VM's port", got)
	}
	if l1["SIP_LANE_SIP"] != "127.0.0.1:15060" {
		t.Error("useL2 changed the L1 lane's env")
	}
	if l.env["SIP_LANE_OFFSET"] != "10000" {
		t.Error("useL2 lost the lane's offset (jarvisd's local port follows it)")
	}
	if err := (&lane{env: l1}).useL2(l2VM{sip: "127.0.0.1"}); err == nil {
		t.Error("useL2 took a SIP address with no port")
	}
}

// fakeFreePBXLane puts a scripts/freepbx-lane under a temporary root that records its arguments
// and prints out, and a docker on PATH that fails the test if called.
func fakeFreePBXLane(t *testing.T, out string) (root, calls string) {
	root = t.TempDir()
	calls = filepath.Join(root, "calls")
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	sh := "#!/bin/sh\nfor a; do printf '%s\\n' \"$a\"; done >>" + calls + "\nprintf '%s' '" + out + "'\n"
	if err := os.WriteFile(filepath.Join(root, "scripts", "freepbx-lane"), []byte(sh), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\necho docker called >&2\nexit 1\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return root, calls
}

// On L2 the Asterisk CLI runs in the VM over the lane's SSH (scripts/freepbx-lane ssh), never
// in the L1 lane's container.
func TestL2CLIGoesOverTheLaneSSH(t *testing.T) {
	root, calls := fakeFreePBXLane(t, "Contact:  100/sip:100@10.0.2.2:15073  abc Avail  1.2")
	l := &lane{root: root, env: map[string]string{"SIP_LANE_SIP": "127.0.0.1:15060"}}
	if err := l.useL2(l2VM{sip: "127.0.0.1:5260", ssh: "127.0.0.1:2247"}); err != nil {
		t.Fatal(err)
	}
	out, err := l.cli(context.Background(), "pjsip show contacts")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "100/sip:100@10.0.2.2") {
		t.Errorf("cli output = %q", out)
	}
	b, _ := os.ReadFile(calls)
	if got, want := string(b), "ssh\nsudo asterisk -rx 'pjsip show contacts'\n"; got != want {
		t.Errorf("scripts/freepbx-lane got %q, want %q", got, want)
	}
	// No stand-in holds 100 on FreePBX: release has nothing to give back and calls nothing.
	restore, err := l.release(context.Background())
	if err != nil {
		t.Fatalf("release on L2: %v", err)
	}
	restore()
}

func TestShellQuote(t *testing.T) {
	if got, want := shellQuote(`pjsip show endpoint 100`), `'pjsip show endpoint 100'`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got, want := shellQuote(`it's`), `'it'\''s'`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// fail2ban-client status and fail2ban.log as FreePBX 17.0.33's fail2ban 1.0 writes them (the
// lane's first real run, 2026-10-22).
func TestFail2banParsing(t *testing.T) {
	status := "Status for the jail: asterisk-iptables\n|- Filter\n|  |- Currently failed:\t2\n|  |- Total failed:\t6\n" +
		"|  `- File list:\t/var/log/asterisk/fail2ban\n`- Actions\n   |- Currently banned:\t1\n   |- Total banned:\t1\n   `- Banned IP list:\t10.0.2.2\n"
	if got := f2bField(status, "Currently failed"); got != "2" {
		t.Errorf("Currently failed = %q", got)
	}
	if got := f2bField(status, "Banned IP list"); got != "10.0.2.2" {
		t.Errorf("Banned IP list = %q", got)
	}
	log := strings.Repeat("2026-10-23 00:32:22,666 fail2ban.filter [2289]: INFO    [asterisk-iptables] Found 10.0.2.2 - 2026-10-23 00:32:22\n", 6) +
		"2026-10-23 00:32:24,474 fail2ban.actions [2289]: NOTICE  [asterisk-iptables] Ban 10.0.2.2\n" +
		"2026-10-23 00:32:24,476 fail2ban.filter [2289]: INFO    [recidive] Found 10.0.2.2 - 2026-10-23 00:32:24\n" +
		"2026-10-23 00:33:57,353 fail2ban.actions [2289]: NOTICE  [asterisk-iptables] Unban 10.0.2.2\n"
	if got, want := summarizeF2B(log), "asterisk-iptables: 6 Found, 1 Ban, 1 Unban; recidive: 1 Found"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	if got := summarizeF2B(""); got != "nothing in fail2ban.log" {
		t.Errorf("empty summary = %q", got)
	}
}

// The ban shows from the host as the VM's SSH no longer sending its banner (qemu's forward
// still accepts the connection, then the guest drops it).
func TestL2SSHAnswers(t *testing.T) {
	serve := func(banner string) string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				c.Write([]byte(banner))
				c.Close()
			}
		}()
		return ln.Addr().String()
	}
	if !(&lane{vm: &l2VM{ssh: serve("SSH-2.0-OpenSSH_9.2p1 Debian-2+deb12u3\r\n")}}).l2SSHAnswers() {
		t.Error("an SSH banner is not taken for SSH answering")
	}
	if (&lane{vm: &l2VM{ssh: serve("")}}).l2SSHAnswers() {
		t.Error("a connection closed with no banner (banned) is taken for SSH answering")
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	if (&lane{vm: &l2VM{ssh: addr}}).l2SSHAnswers() {
		t.Error("nothing listening is taken for SSH answering")
	}
}
