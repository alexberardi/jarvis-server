package deviceid

import "os/exec"

// machineID is the hardware's IOPlatformUUID, which a reinstall of macOS keeps.
func machineID() (string, error) {
	out, err := exec.Command("/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return "", err
	}
	return parseIOReg(string(out))
}
