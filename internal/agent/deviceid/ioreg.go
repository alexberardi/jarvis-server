package deviceid

import "regexp"

var platformUUID = regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([0-9A-Fa-f-]+)"`)

// parseIOReg finds IOPlatformUUID in `ioreg -rd1 -c IOPlatformExpertDevice` output (macOS; here
// so every OS tests it).
func parseIOReg(out string) (string, error) {
	m := platformUUID.FindStringSubmatch(out)
	if m == nil {
		return "", errNoMachineID
	}
	return m[1], nil
}
