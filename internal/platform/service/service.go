// Package service runs jarvisd under the operating system's service manager and registers it
// there (00-installers §2, I1): systemd (a system unit with a dedicated `jarvisd` user, or a
// --user unit), a launchd LaunchDaemon on macOS, and the Windows Service Control Manager.
//
// It also answers "who supervises this process?" (Detect), which the admin restart button
// (AD8) and self-update (AD5) need: under a supervisor jarvisd restarts by exiting with
// RestartExitCode and letting the supervisor start it again; unsupervised, it can't. The
// admin Stop button (AD8b) is the opposite: jarvisd exits in a way its supervisor leaves
// alone (StopExitCodeFor).
package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	// StopExitCode is the status jarvisd exits with under systemd when the admin asked it to
	// stay stopped (AD8b). The unit's Restart=always restarts even a clean exit 0, so the unit
	// names this code in RestartPreventExitStatus= (and SuccessExitStatus=, so the unit ends
	// inactive rather than failed). 98 is outside sysexits (64–78, where RestartExitCode
	// lives), is not 1 (any error) or 2 (a Go panic, flag usage), is below the shell's 126–127
	// and 128+signal, and below the 200–243 systemd uses for its own exec failures; nothing
	// in jarvisd or its engines exits with it. launchd and the Windows SCM get exit 0 instead
	// (StopExitCodeFor): both restart on a non-zero exit and leave a clean one alone.
	StopExitCode = 98
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

// The privileged upgrade helpers of macOS and Windows (ID11), installed next to the service.
const (
	// HelperLabel is the root LaunchDaemon that swaps a staged release in.
	HelperLabel = "net.jarvisautomation.jarvisd-updater"
	// HelperName is the LocalSystem Windows service that does the same.
	HelperName = "jarvisd-updater"
)

// Helper is the privileged upgrade helper installed for this service: how a self-update
// staged by the unprivileged service gets its binary swapped (00-installers §8.2, ID11).
type Helper string

const (
	// HelperNone: no helper; the binary must be writable by jarvisd, or the operator runs
	// `sudo jarvisd upgrade`.
	HelperNone Helper = ""
	// HelperPrestart: the systemd unit's root ExecStartPre=+, run on every (re)start.
	HelperPrestart Helper = "prestart"
	// HelperLaunchd: the root LaunchDaemon HelperLabel, woken by a request file.
	HelperLaunchd Helper = "launchd"
	// HelperSCM: the LocalSystem service HelperName, started by jarvisd through the SCM.
	HelperSCM Helper = "scm"
)

// OnDemand reports whether jarvisd has to wake the helper (which then restarts jarvisd),
// rather than the helper running before every start.
func (h Helper) OnDemand() bool { return h == HelperLaunchd || h == HelperSCM }

// HelperLogPath is the Windows updater's log, next to the binary in the administrators'
// directory: the home is writable by the service account, so a LocalSystem process must not
// open files there by path (a junction could send the write anywhere). The macOS updater
// logs to HelperLog.
func HelperLogPath(binary string) string {
	return filepath.Join(filepath.Dir(binary), "jarvisd-updater.log")
}

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

// StartCommand is what an operator runs to start jarvisd again after the admin stopped it
// (AD8b): the `jarvisd service start` CLI (user: a systemd --user install), which on Windows
// needs an elevated PowerShell (StartNote). Unsupervised, it is `jarvisd serve`.
func StartCommand(kind Kind, user bool) string {
	switch kind {
	case Systemd:
		if user {
			return "jarvisd service start --user"
		}
		return "sudo jarvisd service start"
	case Launchd:
		return "sudo jarvisd service start"
	case SCM:
		return "jarvisd service start"
	}
	return "jarvisd serve"
}

// StartNote is what goes with StartCommand: where to run it, and whether a reboot starts
// jarvisd too.
func StartNote(kind Kind) string {
	switch kind {
	case SCM:
		return "Run it in an elevated PowerShell (Run as administrator). Restarting the computer also starts jarvisd."
	case Systemd, Launchd:
		return "Run it on the server. Restarting the computer also starts jarvisd."
	}
	return "Run it on the server, in the directory and environment jarvisd ran in before."
}

// InstallCommand is what rewrites the installed service definition (keeping the data, home
// and account): the fix for a definition an older version wrote.
func InstallCommand(kind Kind, user bool) string {
	switch kind {
	case Systemd:
		if user {
			return "jarvisd service install --user"
		}
		return "sudo jarvisd service install"
	case Launchd:
		return "sudo jarvisd service install"
	case SCM:
		return "jarvisd service install"
	}
	return ""
}

// stopCommand is the `jarvisd service stop` an operator runs on the server.
func stopCommand(kind Kind, user bool) string {
	switch {
	case kind == Systemd && user:
		return "jarvisd service stop --user"
	case kind == SCM:
		return "jarvisd service stop"
	}
	return "sudo jarvisd service stop"
}

// StopExitCodeFor is the status jarvisd exits with when the admin stops it under kind: one
// its supervisor does not restart. systemd: StopExitCode (RestartPreventExitStatus=);
// launchd (KeepAlive SuccessfulExit=false) and the SCM (recovery actions fire on a non-zero
// exit only): 0. Unsupervised: 0.
func StopExitCodeFor(kind Kind) int {
	if kind == Systemd {
		return StopExitCode
	}
	return 0
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

// UserUnit reports whether this process runs in the jarvisd systemd --user unit (a `--user`
// install) rather than the system unit: the admin's suggested install command then needs
// --user (A10b R3). Only meaningful when Detect is Systemd.
func UserUnit() bool {
	b, err := os.ReadFile("/proc/self/cgroup")
	return err == nil && inUserUnitCgroup(string(b))
}

// inUserUnitCgroup reports whether the jarvisd.service cgroup line sits under a user manager
// (…/user@<uid>.service/…/jarvisd.service); the system unit is /system.slice/jarvisd.service.
func inUserUnitCgroup(cgroup string) bool {
	for _, line := range strings.Split(cgroup, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, "/"+Name+".service") && strings.Contains(line, "/user@") {
			return true
		}
	}
	return false
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

// ErrStop is what `serve` returns (wrapped in a *StopError) when the admin stopped jarvisd.
var ErrStop = errors.New("stop requested")

// StopError is ErrStop with the exit code that keeps the supervisor from restarting jarvisd.
type StopError struct{ Code int }

func (e *StopError) Error() string        { return ErrStop.Error() }
func (e *StopError) Is(target error) bool { return target == ErrStop }

// Requested reports whether err is a requested restart or stop rather than a failure: main
// exits with its code without printing it.
func Requested(err error) bool { return errors.Is(err, ErrRestart) || errors.Is(err, ErrStop) }

// ErrUnsupervised means nothing would start jarvisd again if it exited.
var ErrUnsupervised = errors.New("jarvisd is not running under a service manager; restart it yourself")

// ExitCode maps run's error to the process exit status.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrRestart):
		return RestartExitCode
	}
	var stop *StopError
	switch {
	case errors.As(err, &stop):
		return stop.Code
	default:
		return 1
	}
}

// scmExit is what the Windows service handler reports for serve's error: a
// service-specific exit code (recovery actions restart jarvisd) for anything but a clean
// exit, which the SCM records as a stop and leaves alone (an admin stop: StopExitCodeFor(SCM)
// is 0).
func scmExit(err error) (serviceSpecific bool, code uint32) {
	if c := ExitCode(err); c != 0 {
		return true, uint32(c)
	}
	return false, 0
}

// Restarter lets a request handler (the admin restart and stop buttons, self-update) end
// serve so the supervisor restarts jarvisd, or leaves it stopped. serve creates it with the
// cancel of its own context and returns ErrRestart or a *StopError for what was requested.
type Restarter struct {
	kind   Kind
	cancel context.CancelFunc
	// want is what ends serve: wantNone, wantRestart or wantStop (the first request wins).
	want atomic.Int32
}

const (
	wantNone int32 = iota
	wantRestart
	wantStop
)

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
	r.want.CompareAndSwap(wantNone, wantRestart)
	r.cancel()
	return nil
}

// RequestStop begins a graceful shutdown after which jarvisd stays stopped: serve returns a
// *StopError whose code the supervisor does not restart on (StopExitCodeFor). It works
// unsupervised too (jarvisd simply exits). Whether the installed service definition honours
// it is StopBlocker's question, asked before.
func (r *Restarter) RequestStop() error {
	r.want.CompareAndSwap(wantNone, wantStop)
	r.cancel()
	return nil
}

// Requested reports whether a restart was requested (and is what ends serve).
func (r *Restarter) Requested() bool { return r.want.Load() == wantRestart }

// StopRequested reports whether a stop was requested (and is what ends serve).
func (r *Restarter) StopRequested() bool { return r.want.Load() == wantStop }

// Err is what serve returns after its runner stopped: ErrRestart when a restart was
// requested, a *StopError when a stop was, else err.
func (r *Restarter) Err(err error) error {
	switch r.want.Load() {
	case wantRestart:
		return ErrRestart
	case wantStop:
		return &StopError{Code: StopExitCodeFor(r.kind)}
	}
	return err
}
