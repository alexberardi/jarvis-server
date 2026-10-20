package deviceid

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"testing"
)

// TestNativeDeviceID reads the real OS sources (opt in with JARVIS_HOST_NATIVE=1; CI does on
// Linux, macOS and Windows). It logs only whether the id is present and stable, never the id.
// Across processes and users it compares a fingerprint (sha256 of the id) kept in a file:
//   - JARVIS_DEVICEID_SAME_AS=F: the first run writes F, later runs must match it;
//   - JARVIS_DEVICEID_DIFFERS_FROM=F: this run (another OS user) must not match F.
func TestNativeDeviceID(t *testing.T) {
	if os.Getenv("JARVIS_HOST_NATIVE") != "1" {
		t.Skip("set JARVIS_HOST_NATIVE=1 to read the real OS")
	}
	if _, err := machineID(); err != nil {
		t.Fatalf("machine id: %v", err)
	}
	if _, err := osUserID(); err != nil {
		t.Fatalf("user id: %v", err)
	}
	a, b := Get(), Get()
	if !hex64.MatchString(a) {
		t.Fatal("device id absent or malformed")
	}
	if a != b {
		t.Fatal("device id not stable within a process")
	}
	sum := sha256.Sum256([]byte(a))
	fp := hex.EncodeToString(sum[:])

	if f := os.Getenv("JARVIS_DEVICEID_SAME_AS"); f != "" {
		prev, err := os.ReadFile(f)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.WriteFile(f, []byte(fp), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Log("first run: fingerprint written")
		case err != nil:
			t.Fatal(err)
		case string(prev) != fp:
			t.Fatal("device id changed since the first run")
		default:
			t.Log("same device id as the first run")
		}
	}
	if f := os.Getenv("JARVIS_DEVICEID_DIFFERS_FROM"); f != "" {
		other, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if string(other) == fp {
			t.Fatal("device id is the same as the other OS user's")
		}
		t.Log("device id differs from the other OS user's")
	}
	t.Log("device id present and stable")
}
