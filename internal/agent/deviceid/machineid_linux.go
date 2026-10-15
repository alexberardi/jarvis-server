package deviceid

import (
	"os"
	"strings"
)

// machineIDPaths: systemd's id, then D-Bus's copy (older or non-systemd distributions).
var machineIDPaths = []string{"/etc/machine-id", "/var/lib/dbus/machine-id"}

func machineID() (string, error) { return firstMachineID(machineIDPaths) }

func firstMachineID(paths []string) (string, error) {
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		// "uninitialized" is systemd's placeholder during first boot.
		if s := strings.TrimSpace(string(b)); s != "" && s != "uninitialized" {
			return s, nil
		}
	}
	return "", errNoMachineID
}
