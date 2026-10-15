// Package deviceid derives the desktop agent's stable identity for this computer and OS user:
// the device_id it sends when it registers and in heartbeats, so jarvisd can retire the node an
// earlier enrolment of the same computer left behind (docs/decisions/2026-10-15-reenroll-replaces-node.md).
//
// It is an HMAC of the OS machine id (/etc/machine-id, macOS IOPlatformUUID, Windows
// MachineGuid) and the OS user id (uid, or the Windows SID), keyed with an app-specific salt.
// The raw machine id never leaves the computer, the value can't be matched with another app's
// use of the same machine id, and it survives `uninstall --purge` because nothing is stored.
package deviceid

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os/user"
	"strings"
)

// salt is the HMAC key. Changing it changes every computer's id, so the next re-enrolment of
// each would leave its old node behind once: never change it.
const salt = "jarvis-agent device id v1"

// sources are the OS lookups, replaced in tests.
type sources struct {
	machineID func() (string, error)
	userID    func() (string, error)
}

func osUserID() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Uid, nil
}

var osSources = sources{machineID: machineID, userID: osUserID}

// Get returns this computer's and user's device id, or "" when the OS doesn't say (no machine
// id, e.g. a minimal container): the agent then registers as it did before device ids, and a
// later re-enrolment adds a node instead of replacing one.
func Get() string { return osSources.get() }

func (s sources) get() string {
	mid, err := s.machineID()
	if err != nil {
		return ""
	}
	mid = strings.ToLower(strings.TrimSpace(mid))
	uid, err := s.userID()
	if err != nil {
		return ""
	}
	uid = strings.TrimSpace(uid)
	if mid == "" || uid == "" {
		return ""
	}
	return derive(mid, uid)
}

func derive(machineID, userID string) string {
	h := hmac.New(sha256.New, []byte(salt))
	h.Write([]byte(machineID))
	h.Write([]byte{0})
	h.Write([]byte(userID))
	return hex.EncodeToString(h.Sum(nil))
}

var errNoMachineID = errors.New("deviceid: no machine id on this system")
