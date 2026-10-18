package provisioning

import (
	"os"
	"runtime"
	"strings"
)

// Identity is what GET /api/v1/info says about the machine.
type Identity struct {
	NodeID   string // the enrolled node id, else jarvis-<last 8 hex of the MAC>
	Hardware string // pi-zero-w, raspberry-pi, arm-linux, macos, linux-x86_64, …
	MAC      string
}

// SystemIdentity reads this machine's identity as _get_node_id, _get_hardware_type and
// _get_mac_address do. enrolledID is the node id from the credentials file, or "".
func SystemIdentity(enrolledID string) Identity {
	mac := macAddress("/sys/class/net")
	id := enrolledID
	if id == "" {
		hex := strings.ReplaceAll(mac, ":", "")
		id = "jarvis-" + hex[max(0, len(hex)-8):]
	}
	model, _ := os.ReadFile("/proc/device-tree/model")
	return Identity{NodeID: id, Hardware: hardware(runtime.GOOS, runtime.GOARCH, string(model)), MAC: mac}
}

func macAddress(sysNet string) string {
	for _, iface := range []string{"wlan0", "eth0", "en0"} {
		if b, err := os.ReadFile(sysNet + "/" + iface + "/address"); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				return s
			}
		}
	}
	return "00:00:00:00:00:00"
}

// hardware follows _get_hardware_type, except that a 64-bit Pi is recognised too (Python's
// "arm" in platform.machine() misses aarch64 and says linux-aarch64).
func hardware(goos, goarch, model string) string {
	model = strings.ToLower(model)
	switch {
	case goos == "linux" && strings.Contains(model, "raspberry") && strings.Contains(model, "zero"):
		return "pi-zero-w"
	case goos == "linux" && strings.Contains(model, "raspberry"):
		return "raspberry-pi"
	case goos == "linux" && strings.HasPrefix(goarch, "arm"):
		return "arm-linux"
	case goos == "darwin":
		return "macos"
	}
	machine := map[string]string{"amd64": "x86_64", "arm64": "aarch64", "386": "i686"}[goarch]
	if machine == "" {
		machine = goarch
	}
	return goos + "-" + machine
}
