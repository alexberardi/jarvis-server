package install

import (
	"os"
	"path/filepath"
)

// The Pi node scripts/install-pi.sh sets up (go-pi-node.md §6): the agent in PiDir, which
// belongs to the node's user (so the agent replaces itself there), run as that user by the
// system unit PiUnitFile with Restart=always. Its sudoers lines let that user run exactly
// PiResetFailed and PiRestart.
const (
	PiDir      = "/opt/jarvis-agent"
	PiUnit     = "jarvis-agent.service"
	PiUnitFile = "/etc/systemd/system/" + PiUnit
	// PiRootEnv puts those paths under another root, as install-pi.sh's --root does (tests and
	// the agent-upgrade e2e's -pi lane only).
	PiRootEnv = "JARVIS_PI_ROOT"
)

// PiRestart restarts the Pi node's unit, as sudoers allows it (install-pi.sh install_sudoers):
// non-interactive, and systemctl by the path the sudoers line names.
var PiRestart = []string{"sudo", "-n", "/usr/bin/systemctl", "restart", PiUnit}

// PiResetFailed clears the unit's failed state, as sudoers allows it. A release that crash-loops
// hits the unit's start limit (StartLimitBurst) before the update's health gate gives up, and
// systemd then refuses a restart ("Start request repeated too quickly") until reset-failed, so
// the rollback would leave the node down.
var PiResetFailed = []string{"sudo", "-n", "/usr/bin/systemctl", "reset-failed", PiUnit}

// PiSystemUnit reports whether exe is the Pi node's agent: PiDir/jarvis-agent under root, with
// the system unit installed.
func PiSystemUnit(root, exe string) bool {
	if exe != filepath.Join(root+PiDir, "jarvis-agent") {
		return false
	}
	_, err := os.Stat(root + PiUnitFile)
	return err == nil
}
