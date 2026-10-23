package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/sip"
)

// `siplane l2` (loop #490) runs jarvisd's SIP provider against the L2 lane's real FreePBX 17 in
// its qemu VM (scripts/freepbx-lane calls, after calls.sh has fitted the VM to the host): it
// registers as 100, places one call through the outbound route to the carrier and answers one
// from the carrier through DID 5551230000, both audio directions transcribed as in L1; then the
// wrong-password path against FreePBX's own intrusion detection. The far ends and the voice are
// an L1 sandbox's (its carrier baresip, its TTS and whisper); only the PBX changes: SIP goes to
// the VM's forwarded port and the Asterisk CLI runs over the lane's SSH.

// l2VM is where the FreePBX VM answers on this host (scripts/freepbx-lane's loopback forwards).
type l2VM struct{ sip, ssh string }

// useL2 points the lane at the VM: the registrar (and every URI the far ends dial) becomes the
// VM's SIP, and the CLI goes over SSH.
func (l *lane) useL2(vm l2VM) error {
	if _, _, err := splitHostPort(vm.sip); err != nil {
		return fmt.Errorf("--sip: %w", err)
	}
	env := make(map[string]string, len(l.env))
	for k, v := range l.env {
		env[k] = v
	}
	env["SIP_LANE_SIP"] = vm.sip
	l.env, l.vm = env, &vm
	return nil
}

// vmRun runs a shell command in the VM as the lane's user (scripts/freepbx-lane ssh).
func (l *lane) vmRun(ctx context.Context, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	b, err := exec.CommandContext(ctx, filepath.Join(l.root, "scripts", "freepbx-lane"), "ssh", command).CombinedOutput()
	if err != nil {
		return string(b), fmt.Errorf("in the VM, %s: %w: %s", command, err, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}

// shellQuote quotes s for the VM's shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

var l2Scenarios = []scenario{
	{"l2-registers", "jarvisd's SIP provider registers as 100 on FreePBX, unregisters on shutdown, registers again", jarvisRegisters},
	{"l2-outbound", "jarvisd dials a 10-digit number through FreePBX's outbound route to the carrier: audio both ways, transcribed", jarvisOutbound},
	{"l2-inbound", "the carrier calls DID 5551230000: FreePBX's inbound route reaches jarvisd, which answers after the delay; audio both ways, transcribed", jarvisInbound},
	{"l2-lockout", "a wrong password: jarvisd stops after exactly 3, and those 3 get this host banned by FreePBX's fail2ban on every port; unbanned, the right password registers", l2Lockout},
}

// The lockout path (docs/cc/phone-sip.md §3.1). On a fresh FreePBX 17 the Responsive Firewall is
// off (its wizard turns it on) and the System Admin module's intrusion detection, fail2ban, is
// on: its asterisk-iptables jail bans an address with 5 matching log lines in 10 minutes, for 30
// minutes, on every port (iptables-allports), SSH and the admin UI included. Asterisk logs two
// matching lines for each refused REGISTER (the distributor's "Failed to authenticate" notice and
// the ChallengeResponseFailed security event), so jarvisd's 3 attempts, which stop it, are
// already enough for the ban. Everything from this host reaches the VM from 10.0.2.2, so the ban
// locks the lane out of its own VM too: a timer started in the VM first lifts it (fail2ban-client
// unban --all, what Intrusion Detection's unban does) whether or not SSH still gets in.
const (
	l2Jail        = "asterisk-iptables"
	l2UnbanAfter  = 90 * time.Second
	l2HostInVM    = "10.0.2.2"
	l2BanDeadline = 20 * time.Second
)

func l2Lockout(ctx context.Context, l *lane, r *report) error {
	if _, err := l.vmRun(ctx, "sudo fail2ban-client unban --all"); err != nil {
		return err
	}
	unit := fmt.Sprintf("lane-unban-%d", time.Now().Unix())
	if _, err := l.vmRun(ctx, fmt.Sprintf("sudo systemd-run --quiet --unit=%s --on-active=%d /usr/bin/fail2ban-client unban --all",
		unit, int(l2UnbanAfter.Seconds()))); err != nil {
		return err
	}
	armed := time.Now()
	defer func() {
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		l.vmRun(bg, "sudo systemctl stop "+unit+".timer; sudo fail2ban-client unban --all")
	}()

	if !l.l2WrongPassword(ctx, r) {
		return nil
	}
	// fail2ban reads the log as it grows: give it a moment.
	ban := l.l2BanState(ctx)
	for deadline := time.Now().Add(l2BanDeadline); !ban.banned && time.Now().Before(deadline); {
		time.Sleep(2 * time.Second)
		ban = l.l2BanState(ctx)
	}
	r.notef("after jarvisd's 3 wrong passwords: %s", ban)
	if !ban.banned {
		r.failf("FreePBX did not ban this host after jarvisd's 3 wrong passwords (§3.1 says it does: fail2ban's %s, 5 matches in 10 min, 2 per refused REGISTER)", l2Jail)
		return nil
	}
	// Banned on every port: SSH stopped answering (l2BanState), and the PBX drops this host's
	// REGISTER, the right password included.
	if st := l.l2Register(ctx, r, 12*time.Second); st.State == sip.StateRegistered {
		r.failf("the right password registered while banned")
	} else {
		r.notef("while banned the right password gets no answer either: %s (%s)", st.State, st.LastError)
	}

	// Unblock: the timer in the VM runs fail2ban-client unban --all.
	wait := time.Until(armed.Add(l2UnbanAfter))
	r.notef("waiting %.0f s for the unban timer in the VM", wait.Seconds())
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
	}
	for deadline := time.Now().Add(time.Minute); !l.l2SSHAnswers() && time.Now().Before(deadline); {
		time.Sleep(2 * time.Second)
	}
	if !l.l2SSHAnswers() {
		r.failf("SSH still blocked a minute after the unban")
		return nil
	}
	if st := l.l2Register(ctx, r, 15*time.Second); st.State != sip.StateRegistered {
		r.failf("after the unban the right password does not register: %+v", st)
	} else {
		r.notef("unbanned (fail2ban-client unban --all): SSH answers and the right password registers")
	}
	log, _ := l.vmRun(ctx, fmt.Sprintf("sudo grep -h -E '%s' /var/log/fail2ban.log | tail -40; echo; echo \"lines for %s in Asterisk's fail2ban log:\"; sudo grep -c '%s' /var/log/asterisk/fail2ban",
		regexp.QuoteMeta(l2HostInVM), l2HostInVM, l2HostInVM))
	l.keep("l2-lockout-fail2ban.log", log)
	r.notef("fail2ban for %s: %s", l2HostInVM, summarizeF2B(log))
	return nil
}

// l2WrongPassword is jarvisd started with a wrong password: it must stop after exactly 3
// attempts, with the firewall hint, and send nothing more. false when it did not stop (reported).
func (l *lane) l2WrongPassword(ctx context.Context, r *report) bool {
	ua, err := l.jarvisUA(ctx, "127.0.0.1", "not-the-password", r)
	if err != nil {
		r.failf("%v", err)
		return false
	}
	defer ua.Close(context.Background())
	st := waitUA(ua, sip.StateStopped, 30*time.Second)
	if st.State != sip.StateStopped {
		r.failf("still trying after %d attempts: %+v", st.Attempts, st)
		return false
	}
	if st.Attempts != 3 || st.AuthFailures != 3 {
		r.failf("stopped after %d attempts (%d refused), want exactly 3", st.Attempts, st.AuthFailures)
	}
	if !strings.Contains(st.Hint, "Firewall") {
		r.failf("hint %q does not point at the PBX firewall", st.Hint)
	}
	time.Sleep(3 * time.Second)
	if n := ua.Status().Attempts; n != 3 {
		r.failf("%d attempts after the stop", n)
	}
	r.notef("jarvisd stopped after %d attempts (%d refused): %q", st.Attempts, st.AuthFailures, st.LastError)
	return true
}

// l2Register starts jarvisd with the right password and waits up to d for it to register.
func (l *lane) l2Register(ctx context.Context, r *report, d time.Duration) sip.Status {
	ua, err := l.jarvisUA(ctx, "127.0.0.1", jarvisPassword, r)
	if err != nil {
		return sip.Status{LastError: err.Error()}
	}
	defer ua.Close(context.Background())
	return waitUA(ua, sip.StateRegistered, d)
}

// l2Ban is what the host sees of a ban: whether the VM's SSH still answers and, when it does,
// fail2ban's own count.
type l2Ban struct {
	banned         bool
	failed, listed string // fail2ban-client status: currently failed, banned IP list
}

func (b l2Ban) String() string {
	if b.banned && b.failed == "" {
		return "banned (the VM's SSH no longer answers this host)"
	}
	return fmt.Sprintf("fail2ban's %s: %s currently failed, banned [%s]", l2Jail, b.failed, b.listed)
}

func (l *lane) l2BanState(ctx context.Context) l2Ban {
	if !l.l2SSHAnswers() {
		return l2Ban{banned: true}
	}
	out, err := l.vmRun(ctx, "sudo fail2ban-client status "+l2Jail)
	if err != nil {
		return l2Ban{banned: !l.l2SSHAnswers()}
	}
	b := l2Ban{failed: f2bField(out, "Currently failed"), listed: f2bField(out, "Banned IP list")}
	b.banned = strings.Contains(" "+b.listed+" ", " "+l2HostInVM+" ")
	return b
}

// f2bField is a field of fail2ban-client status ("|  |- Currently failed:\t3").
func f2bField(out, name string) string {
	for _, line := range strings.Split(out, "\n") {
		if _, v, ok := strings.Cut(line, name+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// l2SSHAnswers says whether the VM's SSH sends its banner: a ban on every port stops it.
func (l *lane) l2SSHAnswers() bool {
	c, err := net.DialTimeout("tcp", l.vm.ssh, 3*time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(4 * time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	return err == nil && strings.HasPrefix(line, "SSH-")
}

var f2bAction = regexp.MustCompile(`\[([\w-]+)\] (Found|Ban|Unban|Restore Ban)\b`)

// summarizeF2B counts fail2ban.log's actions per jail ("asterisk-iptables: 6 Found, 1 Ban").
func summarizeF2B(log string) string {
	counts := map[string]map[string]int{}
	var jails []string
	for _, m := range f2bAction.FindAllStringSubmatch(log, -1) {
		if counts[m[1]] == nil {
			counts[m[1]] = map[string]int{}
			jails = append(jails, m[1])
		}
		counts[m[1]][m[2]]++
	}
	if len(jails) == 0 {
		return "nothing in fail2ban.log"
	}
	var parts []string
	for _, j := range jails {
		var acts []string
		for _, a := range []string{"Found", "Ban", "Restore Ban", "Unban"} {
			if n := counts[j][a]; n > 0 {
				acts = append(acts, strconv.Itoa(n)+" "+a)
			}
		}
		parts = append(parts, j+": "+strings.Join(acts, ", "))
	}
	return strings.Join(parts, "; ")
}
