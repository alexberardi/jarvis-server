// Package service runs jarvisd under the operating system's service manager and registers it
// there (00-installers §2, I1): systemd (a system unit with a dedicated `jarvisd` user, or a
// --user unit), a launchd LaunchDaemon on macOS, and the Windows Service Control Manager.
//
// It also answers "who supervises this process?" (Detect), which the admin restart button
// (AD8) and self-update (AD5) need: under a supervisor jarvisd restarts by exiting with
// RestartExitCode and letting the supervisor start it again; unsupervised, it can't.
package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
)

const (
	// Name is the systemd unit and Windows service name, and the Linux service account.
	Name = "jarvisd"
	// LaunchdLabel is the LaunchDaemon label (a domain the project controls, not com.jarvis.*,
	// which the legacy admin uses).
	LaunchdLabel = "net.jarvisautomation.jarvisd"
	// WindowsAccount is the virtual service account the Windows service runs as (ID1).
	WindowsAccount = `NT SERVICE\jarvisd`
	// RestartExitCode is the status jarvisd exits with when it asks its supervisor to restart
	// it (EX_TEMPFAIL). systemd (Restart=always), launchd (KeepAlive) and the SCM (recovery
	// actions on non-crash failures) all start it again.
	RestartExitCode = 75
)

// Kind is the service manager supervising this process.
type Kind string

const (
	None    Kind = "none"
	Systemd Kind = "systemd"
	Launchd Kind = "launchd"
	SCM     Kind = "windows-service"
)

// Supervised reports whether something will start jarvisd again after it exits.
func (k Kind) Supervised() bool { return k != None && k != "" }

// EnvUpgradeHelper is set (to 1) by the systemd system unit, whose ExecStartPre=+ runs
// `jarvisd upgrade --prestart` as root: a self-update can be staged by the unprivileged
// service and swapped in by that helper on restart.
const EnvUpgradeHelper = "JARVIS_UPGRADE_HELPER"

// RestartCommand is what an operator runs to restart jarvisd under kind (user: a systemd
// --user unit). Unsupervised, it says to restart the process by hand.
func RestartCommand(kind Kind, user bool) string {
	switch kind {
	case Systemd:
		if user {
			return "systemctl --user restart " + Name
		}
		return "sudo systemctl restart " + Name
	case Launchd:
		return "sudo launchctl kickstart -k system/" + LaunchdLabel
	case SCM:
		return "Restart-Service " + Name
	}
	return "stop jarvisd (Ctrl-C) and start it again: jarvisd serve"
}

// probe is what Detect looks at, injectable for tests.
type probe struct {
	getenv     func(string) string
	getppid    func() int
	readFile   func(string) ([]byte, error)
	winService func() bool
}

// Detect reports which service manager runs this process: the jarvisd systemd unit (system
// or user), a launchd job, the Windows SCM, or none (a terminal, a container's init, a
// foreign supervisor jarvisd can't count on).
func Detect() Kind {
	return detect(probe{getenv: os.Getenv, getppid: os.Getppid, readFile: os.ReadFile, winService: IsWindowsService})
}

func detect(p probe) Kind {
	if p.winService() {
		return SCM
	}
	// systemd sets INVOCATION_ID for every unit it starts, and a shell in a terminal that was
	// itself launched as a unit inherits it, so also require that this process sits in the
	// jarvisd.service cgroup.
	if p.getenv("INVOCATION_ID") != "" {
		if b, err := p.readFile("/proc/self/cgroup"); err == nil && inUnitCgroup(string(b), Name+".service") {
			return Systemd
		}
	}
	// launchd starts jobs directly (parent pid 1) and names them in XPC_SERVICE_NAME; a
	// terminal session has "0" or an application name there and a shell as parent.
	if xpc := p.getenv("XPC_SERVICE_NAME"); xpc != "" && xpc != "0" && p.getppid() == 1 {
		return Launchd
	}
	return None
}

// inUnitCgroup reports whether any line of /proc/self/cgroup ends in the unit's cgroup.
func inUnitCgroup(cgroup, unit string) bool {
	for _, line := range strings.Split(cgroup, "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), "/"+unit) {
			return true
		}
	}
	return false
}

// ErrRestart is returned by `serve` when a restart was requested; main exits with
// RestartExitCode for it.
var ErrRestart = errors.New("restart requested")

// ErrUnsupervised means nothing would start jarvisd again if it exited.
var ErrUnsupervised = errors.New("jarvisd is not running under a service manager; restart it yourself")

// ExitCode maps run's error to the process exit status.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrRestart):
		return RestartExitCode
	default:
		return 1
	}
}

// Restarter lets a request handler (the admin restart button, self-update) end serve so the
// supervisor restarts jarvisd. serve creates it with the cancel of its own context and
// returns ErrRestart when Requested.
type Restarter struct {
	kind      Kind
	cancel    context.CancelFunc
	requested atomic.Bool
}

// NewRestarter returns a Restarter for a process supervised by kind.
func NewRestarter(kind Kind, cancel context.CancelFunc) *Restarter {
	return &Restarter{kind: kind, cancel: cancel}
}

// Kind is the supervisor that will restart jarvisd.
func (r *Restarter) Kind() Kind { return r.kind }

// Request begins a graceful shutdown that the supervisor turns into a restart. Without a
// supervisor it does nothing and returns ErrUnsupervised (the route answers 409 with the
// manual command).
func (r *Restarter) Request() error {
	if !r.kind.Supervised() {
		return ErrUnsupervised
	}
	r.requested.Store(true)
	r.cancel()
	return nil
}

// Requested reports whether Request succeeded.
func (r *Restarter) Requested() bool { return r.requested.Load() }

// Err is what serve returns after its runner stopped: ErrRestart when a restart was
// requested, else err.
func (r *Restarter) Err(err error) error {
	if r.Requested() {
		return ErrRestart
	}
	return err
}
